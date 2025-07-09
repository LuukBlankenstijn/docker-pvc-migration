package matcher

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/LuukBlankenstijn/docker-pvc-migration/internal/compose"
	"github.com/LuukBlankenstijn/docker-pvc-migration/internal/types"
)

type VolumeMatcher struct {
	dockerVolumes  map[string]*types.DockerVolumeInfo
	volumeMappings []compose.VolumeMapping
	composeParser  *compose.Parser
}

func NewVolumeMatcher(dockerVolumes map[string]*types.DockerVolumeInfo) *VolumeMatcher {
	return &VolumeMatcher{
		dockerVolumes: dockerVolumes,
		composeParser: compose.NewParser(),
	}
}

func (vm *VolumeMatcher) LoadComposeContext(directory string) error {
	// Try to find and parse docker-compose file
	composeFile, err := vm.composeParser.FindComposeFile(directory)
	if err != nil {
		fmt.Printf("Warning: %v - using basic matching\n", err)
		return nil // Don't fail, just use basic matching
	}

	fmt.Printf("Found compose file: %s\n", composeFile)

	compose, err := vm.composeParser.ParseComposeFile(composeFile)
	if err != nil {
		fmt.Printf("Warning: Failed to parse compose file: %v - using basic matching\n", err)
		return nil
	}

	vm.volumeMappings = vm.composeParser.ExtractVolumeMappings(compose)
	fmt.Printf("Found %d volume mappings in compose file\n", len(vm.volumeMappings))

	// Debug: show the mappings
	for _, mapping := range vm.volumeMappings {
		fmt.Printf("  %s:%s -> %s (expected Docker volume: %s)\n",
			mapping.ServiceName, mapping.VolumeName, mapping.MountPath, mapping.DockerVolume)
	}

	return nil
}

func (vm *VolumeMatcher) MatchVolumes(pvcs []*types.PVCInfo) []*types.PVCInfo {
	for _, pvc := range pvcs {
		fmt.Printf("\n--- Matching PVC: %s ---\n", pvc.Name)

		// Find all Docker volumes that contain parts of the PVC name
		candidates := vm.findVolumesContainingPVCName(pvc)

		if len(candidates) == 0 {
			fmt.Printf("No Docker volumes found containing '%s'\n", pvc.Name)
			pvc.MatchedVolume = vm.interactiveVolumeSelection(pvc, vm.getAllDockerVolumes())
		} else {
			pvc.MatchedVolume = vm.interactiveVolumeSelection(pvc, candidates)
		}
	}

	return pvcs
}

func (vm *VolumeMatcher) findComposeMatch(pvc *types.PVCInfo) *types.DockerVolumeInfo {
	// Try to match PVC name to compose volume mappings
	for _, mapping := range vm.volumeMappings {
		// Check if PVC name matches the compose volume name or service name pattern
		possibleMatches := []string{
			// Direct volume name match
			mapping.VolumeName,
			// Service name + volume name
			fmt.Sprintf("%s-%s", mapping.ServiceName, mapping.VolumeName),
			// Just service name (for single-volume services)
			mapping.ServiceName,
		}

		for _, match := range possibleMatches {
			if vm.pvcNameMatches(pvc.Name, match) {
				// Found a compose mapping, now find the actual Docker volume
				return vm.findDockerVolumeForMapping(mapping)
			}
		}
	}

	return nil
}

func (vm *VolumeMatcher) pvcNameMatches(pvcName, candidateName string) bool {
	// Remove namespace prefix for comparison
	cleanPVCName := pvcName
	if strings.Contains(pvcName, "-") {
		parts := strings.Split(pvcName, "-")
		if len(parts) > 1 {
			cleanPVCName = strings.Join(parts[1:], "-") // Remove first part (likely namespace)
		}
	}

	// Try different comparison methods
	comparisons := []string{
		cleanPVCName,
		pvcName,
		strings.ReplaceAll(cleanPVCName, "-", "_"),
		strings.ReplaceAll(pvcName, "-", "_"),
	}

	for _, comp := range comparisons {
		if strings.EqualFold(comp, candidateName) {
			return true
		}
		if strings.EqualFold(comp, strings.ReplaceAll(candidateName, "_", "-")) {
			return true
		}
	}

	return false
}

func (vm *VolumeMatcher) findDockerVolumeForMapping(mapping compose.VolumeMapping) *types.DockerVolumeInfo {
	// Try the expected Docker volume name first
	if volume, exists := vm.dockerVolumes[mapping.DockerVolume]; exists {
		return volume
	}

	// Try variations of the volume name
	variations := vm.composeParser.GetVolumeVariations(mapping.VolumeName)
	for _, variation := range variations {
		if volume, exists := vm.dockerVolumes[variation]; exists {
			return volume
		}
	}

	// Try fuzzy matching based on the volume name
	return vm.findFuzzyMatchForCompose(mapping.VolumeName)
}

func (vm *VolumeMatcher) findFuzzyMatchForCompose(volumeName string) *types.DockerVolumeInfo {
	bestMatch := ""
	bestScore := 0

	for dockerVolumeName := range vm.dockerVolumes {
		score := vm.calculateComposeMatchScore(volumeName, dockerVolumeName)
		if score > bestScore && score > 0 {
			bestScore = score
			bestMatch = dockerVolumeName
		}
	}

	if bestMatch != "" {
		return vm.dockerVolumes[bestMatch]
	}

	return nil
}

func (vm *VolumeMatcher) calculateComposeMatchScore(volumeName, dockerVolumeName string) int {
	score := 0

	// Direct substring match
	if strings.Contains(strings.ToLower(dockerVolumeName), strings.ToLower(volumeName)) {
		score += 3
	}

	// Ends with volume name
	if strings.HasSuffix(strings.ToLower(dockerVolumeName), strings.ToLower(volumeName)) {
		score += 2
	}

	// Contains volume name with separators
	if strings.Contains(strings.ToLower(dockerVolumeName), "_"+strings.ToLower(volumeName)) ||
		strings.Contains(strings.ToLower(dockerVolumeName), "-"+strings.ToLower(volumeName)) {
		score += 4
	}

	return score
}

func (vm *VolumeMatcher) findVolumesContainingPVCName(pvc *types.PVCInfo) []*types.DockerVolumeInfo {
	var candidates []*types.DockerVolumeInfo

	// Extract meaningful parts from PVC name
	pvcParts := vm.extractPVCParts(pvc.Name)

	for _, volume := range vm.dockerVolumes {
		if vm.volumeContainsPVCParts(volume.Name, pvcParts) {
			candidates = append(candidates, volume)
		}
	}

	// Sort by name for consistent display
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Name < candidates[j].Name
	})

	return candidates
}

func (vm *VolumeMatcher) extractPVCParts(pvcName string) []string {
	// Remove namespace prefix if present
	cleanName := pvcName
	if strings.Contains(pvcName, "-") {
		parts := strings.Split(pvcName, "-")
		if len(parts) > 1 {
			cleanName = strings.Join(parts[1:], "-") // Remove first part (likely namespace)
		}
	}

	// Split by common separators and filter out very short parts
	var parts []string
	for _, separator := range []string{"-", "_"} {
		for _, part := range strings.Split(cleanName, separator) {
			if len(part) > 1 { // Only include parts longer than 1 character
				parts = append(parts, strings.ToLower(part))
			}
		}
	}

	// Also include the full clean name
	parts = append(parts, strings.ToLower(cleanName))

	return parts
}

func (vm *VolumeMatcher) volumeContainsPVCParts(volumeName string, pvcParts []string) bool {
	volumeNameLower := strings.ToLower(volumeName)

	// Check if any PVC part is contained in the volume name
	for _, part := range pvcParts {
		if strings.Contains(volumeNameLower, part) {
			return true
		}
	}

	return false
}

func (vm *VolumeMatcher) getAllDockerVolumes() []*types.DockerVolumeInfo {
	var volumes []*types.DockerVolumeInfo
	for _, volume := range vm.dockerVolumes {
		volumes = append(volumes, volume)
	}

	// Sort by name for consistent display
	sort.Slice(volumes, func(i, j int) bool {
		return volumes[i].Name < volumes[j].Name
	})

	return volumes
}

func (vm *VolumeMatcher) interactiveVolumeSelection(pvc *types.PVCInfo, candidates []*types.DockerVolumeInfo) *types.DockerVolumeInfo {
	reader := bufio.NewReader(os.Stdin)

	fmt.Printf("\nSelect Docker volume for PVC '%s':\n", pvc.Name)
	fmt.Println("0. Skip (no volume)")

	for i, volume := range candidates {
		fmt.Printf("%d. %s (%s)\n", i+1, volume.Name, volume.SizeHuman)
	}

	for {
		fmt.Print("Enter choice: ")
		input, err := reader.ReadString('\n')
		if err != nil {
			fmt.Printf("Error reading input: %v\n", err)
			continue
		}

		input = strings.TrimSpace(input)
		choice, err := strconv.Atoi(input)
		if err != nil {
			fmt.Println("Please enter a valid number")
			continue
		}

		if choice == 0 {
			return nil // No volume selected
		}

		if choice >= 1 && choice <= len(candidates) {
			selected := candidates[choice-1]
			fmt.Printf("Selected: %s\n", selected.Name)
			return selected
		}

		fmt.Printf("Invalid choice. Please enter 0-%d\n", len(candidates))
	}
}

func (vm *VolumeMatcher) findExactMatch(name string) *types.DockerVolumeInfo {
	// Direct match
	if volume, exists := vm.dockerVolumes[name]; exists {
		return volume
	}

	// Try with common docker-compose prefixes
	for volumeName, volume := range vm.dockerVolumes {
		if strings.HasSuffix(volumeName, "_"+name) || strings.HasSuffix(volumeName, "-"+name) {
			return volume
		}
	}

	return nil
}

func (vm *VolumeMatcher) findFuzzyMatch(pvcName string) *types.DockerVolumeInfo {
	pvcParts := strings.Split(pvcName, "-")

	bestMatch := ""
	bestScore := 0

	for volumeName := range vm.dockerVolumes {
		score := vm.calculateMatchScore(pvcParts, volumeName)
		if score > bestScore && score >= len(pvcParts)/2 {
			bestScore = score
			bestMatch = volumeName
		}
	}

	if bestMatch != "" {
		return vm.dockerVolumes[bestMatch]
	}

	return nil
}

func (vm *VolumeMatcher) calculateMatchScore(pvcParts []string, volumeName string) int {
	volumeParts := strings.Split(volumeName, "_")
	volumeParts = append(volumeParts, strings.Split(volumeName, "-")...)

	score := 0
	for _, pvcPart := range pvcParts {
		for _, volumePart := range volumeParts {
			if strings.EqualFold(pvcPart, volumePart) {
				score++
				break
			}
		}
	}

	return score
}
