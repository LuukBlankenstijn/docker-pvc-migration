package yaml

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/LuukBlankenstijn/docker-pvc-migration/internal/types"
	"gopkg.in/yaml.v3"
)

type Updater struct{}

func NewUpdater() *Updater {
	return &Updater{}
}

func (u *Updater) UpdateYAMLFiles(directory string, pvcs []*types.PVCInfo) error {
	fmt.Println("\nUpdating YAML files with new PVC sizes...")

	// Walk through all YAML files in the directory
	err := filepath.Walk(directory, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml") {
			return nil
		}

		return u.updateYAMLFile(path, pvcs)
	})

	if err != nil {
		return fmt.Errorf("failed to update YAML files: %v", err)
	}

	fmt.Println("✅ YAML files updated successfully!")
	return nil
}

func (u *Updater) updateYAMLFile(filePath string, pvcs []*types.PVCInfo) error {
	// Read the file
	content, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read file %s: %v", filePath, err)
	}

	// Split content by document separator (---)
	documents := strings.Split(string(content), "\n---\n")
	var updatedDocuments []string
	hasUpdates := false

	for _, doc := range documents {
		if strings.TrimSpace(doc) == "" {
			continue
		}

		updatedDoc, updated := u.updateDocumentIfPVC(doc, pvcs)
		if updated {
			hasUpdates = true
			fmt.Printf("Updated PVC in %s\n", filePath)
		}
		updatedDocuments = append(updatedDocuments, updatedDoc)
	}

	// Only write back if we made changes
	if hasUpdates {
		// Join documents back with separator
		newContent := strings.Join(updatedDocuments, "\n---\n")

		// Write back to file
		err = os.WriteFile(filePath, []byte(newContent), 0644)
		if err != nil {
			return fmt.Errorf("failed to write file %s: %v", filePath, err)
		}
	}

	return nil
}

func (u *Updater) updateDocumentIfPVC(document string, pvcs []*types.PVCInfo) (string, bool) {
	// Parse the YAML document
	var obj map[string]interface{}
	err := yaml.Unmarshal([]byte(document), &obj)
	if err != nil {
		// If we can't parse it, return unchanged
		return document, false
	}

	// Check if this is a PVC
	kind, ok := obj["kind"].(string)
	if !ok || kind != "PersistentVolumeClaim" {
		return document, false
	}

	// Get the PVC name and namespace
	metadata, ok := obj["metadata"].(map[string]interface{})
	if !ok {
		return document, false
	}

	name, ok := metadata["name"].(string)
	if !ok {
		return document, false
	}

	namespace := "default"
	if ns, ok := metadata["namespace"].(string); ok {
		namespace = ns
	}

	// Find matching PVC from our list
	var matchingPVC *types.PVCInfo
	for _, pvc := range pvcs {
		if pvc.Name == name && pvc.Namespace == namespace {
			matchingPVC = pvc
			break
		}
	}

	if matchingPVC == nil || matchingPVC.NewSize == "" {
		return document, false
	}

	// Update the storage size
	spec, ok := obj["spec"].(map[string]interface{})
	if !ok {
		return document, false
	}

	resources, ok := spec["resources"].(map[string]interface{})
	if !ok {
		return document, false
	}

	requests, ok := resources["requests"].(map[string]interface{})
	if !ok {
		return document, false
	}

	// Update the storage size
	oldSize := requests["storage"]
	requests["storage"] = matchingPVC.NewSize

	fmt.Printf("  %s/%s: %v → %s\n", namespace, name, oldSize, matchingPVC.NewSize)

	// Convert back to YAML
	updatedYAML, err := yaml.Marshal(obj)
	if err != nil {
		// If we can't marshal, return unchanged
		return document, false
	}

	return string(updatedYAML), true
}
