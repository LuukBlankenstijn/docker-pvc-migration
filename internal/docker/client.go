package docker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/LuukBlankenstijn/docker-pvc-migration/internal/types"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
)

type Client struct {
	client *client.Client
}

type volumeSize struct {
	bytes int64
	human string
	links int
}

func NewClient() (*Client, error) {
	dockerClient, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %v", err)
	}

	return &Client{client: dockerClient}, nil
}

func (c *Client) LoadVolumes() (map[string]*types.DockerVolumeInfo, error) {
	volumes, err := c.client.VolumeList(context.Background(), volume.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list Docker volumes: %v", err)
	}

	// Get volume sizes using docker system df -v
	fmt.Println("Getting volume sizes (this may take a moment)...")
	volumeSizes, err := c.getVolumeSizesFromDockerDF()
	if err != nil {
		fmt.Printf("Warning: Failed to get volume sizes from docker df, falling back to filesystem walk: %v\n", err)
	}

	result := make(map[string]*types.DockerVolumeInfo)
	for _, volume := range volumes.Volumes {
		var size int64
		var sizeHuman string
		var links int

		// Try to get size from docker df first
		if volumeSizes != nil {
			if dfSize, exists := volumeSizes[volume.Name]; exists {
				size = dfSize.bytes
				sizeHuman = dfSize.human
				links = dfSize.links
			}
		}

		// Skip volumes that are currently in use (links > 0)
		if links > 0 {
			fmt.Printf("Skipping volume %s (in use: %d links)\n", volume.Name, links)
			continue
		}

		// Fallback to filesystem walk if docker df didn't work
		if size == 0 {
			size, sizeHuman = c.getVolumeSize(volume.Mountpoint)
		}

		result[volume.Name] = &types.DockerVolumeInfo{
			Name:       volume.Name,
			Mountpoint: volume.Mountpoint,
			Size:       size,
			SizeHuman:  sizeHuman,
		}
	}

	return result, nil
}

func (c *Client) getVolumeSizesFromDockerDF() (map[string]volumeSize, error) {
	// Set a generous timeout for docker system df -v since it can be slow
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "docker", "system", "df", "-v")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to run docker system df -v: %v", err)
	}

	return c.parseDockerDFOutput(string(output))
}

func (c *Client) parseDockerDFOutput(output string) (map[string]volumeSize, error) {
	lines := strings.Split(output, "\n")
	volumeSizes := make(map[string]volumeSize)

	// Look for lines that have the volume table format
	// Based on your output: VOLUME NAME    LINKS    SIZE
	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Skip header and empty lines
		if line == "" || strings.HasPrefix(line, "VOLUME NAME") || strings.Contains(line, "Local Volumes") {
			continue
		}

		// Parse volume line: split by whitespace and expect at least 3 fields
		fields := strings.Fields(line)
		if len(fields) >= 3 {
			volumeName := fields[0]
			linksStr := fields[1]
			sizeStr := fields[len(fields)-1] // Size is the last field

			// Parse links (number of containers using this volume)
			links, err := strconv.Atoi(linksStr)
			if err != nil {
				continue // Skip if we can't parse links
			}

			// Parse size (like "67.42MB", "291.7MB", "0B")
			bytes, err := c.parseSizeString(sizeStr)
			if err != nil {
				continue // Skip this volume if we can't parse the size
			}

			volumeSizes[volumeName] = volumeSize{
				bytes: bytes,
				human: sizeStr,
				links: links,
			}
		}
	}

	return volumeSizes, nil
}

func (c *Client) parseSizeString(sizeStr string) (int64, error) {
	// Handle docker df size format like "67.42MB", "291.7MB", "0B", etc.
	re := regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*([KMGTPE]?B)$`)
	matches := re.FindStringSubmatch(strings.ToUpper(sizeStr))

	if len(matches) != 3 {
		return 0, fmt.Errorf("invalid size format: %s", sizeStr)
	}

	value, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return 0, err
	}

	unit := matches[2]
	var multiplier int64 = 1

	switch unit {
	case "B":
		multiplier = 1
	case "KB":
		multiplier = 1000
	case "MB":
		multiplier = 1000 * 1000
	case "GB":
		multiplier = 1000 * 1000 * 1000
	case "TB":
		multiplier = 1000 * 1000 * 1000 * 1000
	case "PB":
		multiplier = 1000 * 1000 * 1000 * 1000 * 1000
	case "EB":
		multiplier = 1000 * 1000 * 1000 * 1000 * 1000 * 1000
	default:
		return 0, fmt.Errorf("unknown unit: %s", unit)
	}

	return int64(value * float64(multiplier)), nil
}

func (c *Client) getVolumeSize(mountpoint string) (int64, string) {
	var totalSize int64

	err := filepath.Walk(mountpoint, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors, continue walking
		}
		if !info.IsDir() {
			totalSize += info.Size()
		}
		return nil
	})

	if err != nil {
		// Fallback to filesystem stats if walk fails
		var stat syscall.Statfs_t
		if syscall.Statfs(mountpoint, &stat) == nil {
			totalSize = int64(stat.Blocks * uint64(stat.Bsize))
		}
	}

	return totalSize, c.formatBytes(totalSize)
}

func (c *Client) formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
