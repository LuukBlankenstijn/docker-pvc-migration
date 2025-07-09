package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type ComposeFile struct {
	Version  string                      `yaml:"version"`
	Services map[string]Service          `yaml:"services"`
	Volumes  map[string]VolumeDefinition `yaml:"volumes"`
}

type Service struct {
	Image   string   `yaml:"image"`
	Volumes []string `yaml:"volumes"`
}

type VolumeDefinition struct {
	Driver     string            `yaml:"driver,omitempty"`
	DriverOpts map[string]string `yaml:"driver_opts,omitempty"`
	External   bool              `yaml:"external,omitempty"`
}

type VolumeMapping struct {
	ServiceName  string
	VolumeName   string
	DockerVolume string // The actual Docker volume name
	MountPath    string
}

type Parser struct {
	projectName string
}

func NewParser() *Parser {
	return &Parser{}
}

func (p *Parser) FindComposeFile(directory string) (string, error) {
	candidates := []string{
		"docker-compose.yml",
		"docker-compose.yaml",
		"compose.yml",
		"compose.yaml",
	}

	for _, candidate := range candidates {
		fullPath := filepath.Join(directory, candidate)
		if _, err := os.Stat(fullPath); err == nil {
			return fullPath, nil
		}
	}

	return "", fmt.Errorf("no docker-compose file found in %s", directory)
}

func (p *Parser) ParseComposeFile(filePath string) (*ComposeFile, error) {
	// Extract project name from directory
	dir := filepath.Dir(filePath)
	p.projectName = strings.ToLower(filepath.Base(dir))

	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read compose file: %v", err)
	}

	var compose ComposeFile
	if err := yaml.Unmarshal(data, &compose); err != nil {
		return nil, fmt.Errorf("failed to parse compose file: %v", err)
	}

	return &compose, nil
}

func (p *Parser) ExtractVolumeMappings(compose *ComposeFile) []VolumeMapping {
	var mappings []VolumeMapping

	for serviceName, service := range compose.Services {
		for _, volumeSpec := range service.Volumes {
			mapping := p.parseVolumeSpec(serviceName, volumeSpec)
			if mapping != nil {
				mappings = append(mappings, *mapping)
			}
		}
	}

	return mappings
}

func (p *Parser) parseVolumeSpec(serviceName, volumeSpec string) *VolumeMapping {
	// Handle different volume specification formats:
	// - volume_name:/path/in/container
	// - /host/path:/path/in/container
	// - volume_name:/path/in/container:ro

	parts := strings.Split(volumeSpec, ":")
	if len(parts) < 2 {
		return nil // Invalid volume spec
	}

	source := parts[0]
	target := parts[1]

	// Skip bind mounts (absolute paths)
	if strings.HasPrefix(source, "/") || strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../") {
		return nil
	}

	// This is a named volume
	dockerVolumeName := p.generateDockerVolumeName(source)

	return &VolumeMapping{
		ServiceName:  serviceName,
		VolumeName:   source,
		DockerVolume: dockerVolumeName,
		MountPath:    target,
	}
}

func (p *Parser) generateDockerVolumeName(volumeName string) string {
	// Docker Compose typically creates volume names as: {project}_{volume}
	// But there can be variations, so we'll try multiple patterns
	return fmt.Sprintf("%s_%s", p.projectName, volumeName)
}

func (p *Parser) GetProjectName() string {
	return p.projectName
}

// GetVolumeVariations returns possible Docker volume names for a given compose volume
func (p *Parser) GetVolumeVariations(volumeName string) []string {
	variations := []string{
		// Standard docker-compose naming
		fmt.Sprintf("%s_%s", p.projectName, volumeName),
		// Without project prefix
		volumeName,
		// With directory name variations
		fmt.Sprintf("%s-%s", p.projectName, volumeName),
		// Uppercase project name
		fmt.Sprintf("%s_%s", strings.ToUpper(p.projectName), volumeName),
		fmt.Sprintf("%s-%s", strings.ToUpper(p.projectName), volumeName),
	}

	return variations
}
