package migration

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/LuukBlankenstijn/docker-pvc-migration/internal/types"
	"gopkg.in/yaml.v3"
)

type Engine struct {
	migrationNamespace string // Namespace for migration pods
	yamlDirectory      string // Directory containing YAML files
}

func NewEngine(migrationNamespace, yamlDirectory string) *Engine {
	if migrationNamespace == "" {
		migrationNamespace = "default"
	}
	return &Engine{
		migrationNamespace: migrationNamespace,
		yamlDirectory:      yamlDirectory,
	}
}

func (e *Engine) StartMigration(pvcs []*types.PVCInfo) error {
	fmt.Println("\n=== Starting Migration Process ===")

	for i, pvc := range pvcs {
		if pvc.MatchedVolume == nil {
			fmt.Printf("Skipping %s (no volume selected)\n", pvc.Name)
			continue
		}

		fmt.Printf("\n[%d/%d] Migrating PVC: %s\n", i+1, len(pvcs), pvc.Name)

		if err := e.migratePVC(pvc); err != nil {
			fmt.Printf("❌ Failed to migrate %s: %v\n", pvc.Name, err)
			return fmt.Errorf("migration failed for PVC %s: %v", pvc.Name, err)
		}

		fmt.Printf("✅ Successfully migrated %s\n", pvc.Name)
	}

	fmt.Println("\n🎉 Migration completed successfully!")
	return nil
}

func (e *Engine) migratePVC(pvc *types.PVCInfo) error {
	// Apply the specific YAML file for this PVC
	fmt.Printf("  Applying YAML file for PVC %s to namespace %s...\n", pvc.Name, e.migrationNamespace)
	if err := e.createPVC(pvc); err != nil {
		return fmt.Errorf("failed to apply YAML file: %v", err)
	}

	// Step 2: Wait for PVC to be bound
	fmt.Printf("  Waiting for PVC %s to be bound...\n", pvc.Name)
	if err := e.waitForPVCBound(pvc); err != nil {
		return fmt.Errorf("PVC not bound: %v", err)
	}

	// Step 3: Copy data from Docker volume to PVC
	fmt.Printf("  Copying data from Docker volume %s...\n", pvc.MatchedVolume.Name)
	if err := e.copyData(pvc); err != nil {
		return fmt.Errorf("failed to copy data: %v", err)
	}

	return nil
}

func (e *Engine) createPVC(pvc *types.PVCInfo) error {
	// Find and apply only the YAML file containing this specific PVC
	yamlFile, err := e.findYAMLFileForPVC(pvc)
	if err != nil {
		return fmt.Errorf("failed to find YAML file for PVC %s: %v", pvc.Name, err)
	}

	fmt.Printf("    Applying %s to namespace %s...\n", yamlFile, e.migrationNamespace)

	// Apply the specific YAML file to the specified namespace
	cmd := exec.Command("kubectl", "apply", "-f", yamlFile, "-n", e.migrationNamespace)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("kubectl apply failed: %v\nOutput: %s", err, string(output))
	}

	return nil
}

func (e *Engine) findYAMLFileForPVC(pvc *types.PVCInfo) (string, error) {
	// Search through YAML files to find the one containing this PVC
	matches, err := filepath.Glob(filepath.Join(e.yamlDirectory, "*.yaml"))
	if err != nil {
		return "", err
	}

	yamlFiles := matches
	moreMatches, err := filepath.Glob(filepath.Join(e.yamlDirectory, "*.yml"))
	if err == nil {
		yamlFiles = append(yamlFiles, moreMatches...)
	}

	for _, file := range yamlFiles {
		if e.fileContainsPVC(file, pvc) {
			return file, nil
		}
	}

	return "", fmt.Errorf("no YAML file found containing PVC %s", pvc.Name)
}

func (e *Engine) fileContainsPVC(filename string, pvc *types.PVCInfo) bool {
	content, err := os.ReadFile(filename)
	if err != nil {
		return false
	}

	// Split content by document separator (---)
	documents := strings.Split(string(content), "\n---\n")

	for _, doc := range documents {
		if strings.TrimSpace(doc) == "" {
			continue
		}

		var obj map[string]interface{}
		err := yaml.Unmarshal([]byte(doc), &obj)
		if err != nil {
			continue
		}

		// Check if this is a PVC with the right name
		if kind, ok := obj["kind"].(string); ok && kind == "PersistentVolumeClaim" {
			if metadata, ok := obj["metadata"].(map[string]interface{}); ok {
				if name, ok := metadata["name"].(string); ok && name == pvc.Name {
					return true
				}
			}
		}
	}

	return false
}

func (e *Engine) waitForPVCBound(pvc *types.PVCInfo) error {
	timeout := 5 * time.Minute
	interval := 5 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for PVC %s to be bound", pvc.Name)
		default:
			cmd := exec.Command("kubectl", "get", "pvc", pvc.Name, "-n", e.migrationNamespace, "-o", "jsonpath={.status.phase}")
			output, err := cmd.Output()
			if err != nil {
				fmt.Printf("    Error checking PVC status: %v\n", err)
				time.Sleep(interval)
				continue
			}

			phase := strings.TrimSpace(string(output))
			fmt.Printf("    PVC status: %s\n", phase)

			if phase == "Bound" {
				fmt.Printf("    ✅ PVC is now bound!\n")
				return nil
			}

			if phase == "Failed" {
				return fmt.Errorf("PVC failed to bind")
			}

			// Don't proceed if PVC is not bound
			time.Sleep(interval)
		}
	}
}

func (e *Engine) copyData(pvc *types.PVCInfo) error {
	// Get current node name to schedule migration pod on the same node
	nodeName, err := e.getCurrentNodeName()
	if err != nil {
		return fmt.Errorf("failed to get current node name: %v", err)
	}

	// Create migration pod in the migration namespace (from --namespace flag)
	podName := fmt.Sprintf("migration-%s-%d", pvc.Name, time.Now().Unix())

	podYAML := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: %s
spec:
  restartPolicy: Never
  nodeName: %s
  containers:
  - name: migration
    image: busybox:latest
    command: ["/bin/sh", "-c"]
    args:
    - |
      echo "Starting data copy..."
      echo "Source: /docker-data"
      echo "Target: /pvc-data"
      ls -la /docker-data/ || echo "Source directory empty or missing"
      ls -la /pvc-data/ || echo "Target directory empty"
      
      if [ "$(ls -A /docker-data 2>/dev/null)" ]; then
        echo "Copying data..."
        cp -av /docker-data/* /pvc-data/ 2>/dev/null || echo "No files to copy or copy failed"
        echo "Copy completed"
      else
        echo "Source directory is empty"
      fi
      
      echo "Final target contents:"
      ls -la /pvc-data/
      echo "Migration pod completed"
    volumeMounts:
    - name: docker-volume
      mountPath: /docker-data
    - name: pvc-volume
      mountPath: /pvc-data
  volumes:
  - name: docker-volume
    hostPath:
      path: %s
      type: Directory
  - name: pvc-volume
    persistentVolumeClaim:
      claimName: %s
`, podName, e.migrationNamespace, nodeName, pvc.MatchedVolume.Mountpoint, pvc.Name)

	// Create the migration pod
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(podYAML)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create migration pod: %v\nOutput: %s", err, string(output))
	}

	fmt.Printf("  Migration pod %s created in namespace %s, scheduled on node %s\n", podName, e.migrationNamespace, nodeName)

	// Wait for pod to complete
	fmt.Printf("  Waiting for migration pod to complete...\n")
	if err := e.waitForPodCompletion(podName, e.migrationNamespace); err != nil {
		return fmt.Errorf("migration pod failed: %v", err)
	}

	// Show pod logs
	fmt.Printf("  Migration pod logs:\n")
	if err := e.showPodLogs(podName, e.migrationNamespace); err != nil {
		fmt.Printf("    Warning: Could not retrieve pod logs: %v\n", err)
	}

	// Clean up the migration pod
	if err := e.deletePod(podName, e.migrationNamespace); err != nil {
		fmt.Printf("    Warning: Could not delete migration pod: %v\n", err)
	}

	return nil
}

func (e *Engine) getCurrentNodeName() (string, error) {
	// Get all available nodes
	cmd := exec.Command("kubectl", "get", "nodes", "-o", "jsonpath={.items[*].metadata.name}")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get node list: %v", err)
	}

	nodes := strings.Fields(string(output))
	if len(nodes) == 0 {
		return "", fmt.Errorf("no Kubernetes nodes found")
	}

	// Try to find the best default node
	hostname, _ := os.Hostname()
	defaultNode := e.findBestDefaultNode(nodes, hostname)

	// Interactive node selection
	return e.interactiveNodeSelection(nodes, defaultNode)
}

func (e *Engine) findBestDefaultNode(nodes []string, hostname string) string {
	// Try to match hostname to node name
	for _, node := range nodes {
		if strings.Contains(strings.ToLower(node), strings.ToLower(hostname)) ||
			strings.Contains(strings.ToLower(hostname), strings.ToLower(node)) {
			return node
		}
	}

	// If no match, return first node
	if len(nodes) > 0 {
		return nodes[0]
	}

	return ""
}

func (e *Engine) interactiveNodeSelection(nodes []string, defaultNode string) (string, error) {
	reader := bufio.NewReader(os.Stdin)

	fmt.Printf("\nSelect Kubernetes node for migration pods:\n")

	// Find default index
	for i, node := range nodes {
		marker := "  "
		if node == defaultNode {
			marker = "* "
		}
		fmt.Printf("%s%d. %s\n", marker, i+1, node)
	}

	fmt.Printf("\nDefault: %s (press Enter to use default)\n", defaultNode)
	fmt.Printf("Enter choice (number 1-%d or node name): ", len(nodes))

	for {
		input, err := reader.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("failed to read input: %v", err)
		}

		input = strings.TrimSpace(input)

		// If empty, use default
		if input == "" {
			fmt.Printf("Selected: %s (default)\n", defaultNode)
			return defaultNode, nil
		}

		// Try to parse as number
		if choice, err := strconv.Atoi(input); err == nil {
			if choice >= 1 && choice <= len(nodes) {
				selected := nodes[choice-1]
				fmt.Printf("Selected: %s\n", selected)
				return selected, nil
			} else {
				fmt.Printf("Invalid number. Enter 1-%d or node name: ", len(nodes))
				continue
			}
		}

		// Try to match as node name (partial or exact)
		var matches []string
		for _, node := range nodes {
			if strings.EqualFold(node, input) {
				// Exact match
				fmt.Printf("Selected: %s\n", node)
				return node, nil
			}
			if strings.Contains(strings.ToLower(node), strings.ToLower(input)) {
				matches = append(matches, node)
			}
		}

		if len(matches) == 1 {
			// Single partial match
			fmt.Printf("Selected: %s\n", matches[0])
			return matches[0], nil
		} else if len(matches) > 1 {
			fmt.Printf("Multiple matches found: %s\n", strings.Join(matches, ", "))
			fmt.Printf("Please be more specific. Enter choice (number 1-%d or node name): ", len(nodes))
			continue
		}

		// No matches
		fmt.Printf("Node '%s' not found. Enter choice (number 1-%d or node name): ", input, len(nodes))
	}
}

func (e *Engine) waitForPodCompletion(podName, namespace string) error {
	timeout := 10 * time.Minute
	interval := 5 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for pod %s to complete", podName)
		default:
			cmd := exec.Command("kubectl", "get", "pod", podName, "-n", namespace, "-o", "jsonpath={.status.phase}")
			output, err := cmd.Output()
			if err != nil {
				time.Sleep(interval)
				continue
			}

			phase := strings.TrimSpace(string(output))
			if phase == "Succeeded" {
				return nil
			}
			if phase == "Failed" {
				return fmt.Errorf("migration pod failed")
			}

			fmt.Printf("    Pod status: %s\n", phase)
			time.Sleep(interval)
		}
	}
}

func (e *Engine) showPodLogs(podName, namespace string) error {
	cmd := exec.Command("kubectl", "logs", podName, "-n", namespace)
	output, err := cmd.Output()
	if err != nil {
		return err
	}

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			fmt.Printf("    %s\n", line)
		}
	}

	return nil
}

func (e *Engine) deletePod(podName, namespace string) error {
	cmd := exec.Command("kubectl", "delete", "pod", podName, "-n", namespace, "--ignore-not-found")
	_, err := cmd.CombinedOutput()
	return err
}

func (e *Engine) DryRun(pvcs []*types.PVCInfo) {
	fmt.Println("\n=== Dry Run - Migration Plan ===")

	for i, pvc := range pvcs {
		if pvc.MatchedVolume == nil {
			fmt.Printf("[%d] SKIP: %s (no volume selected)\n", i+1, pvc.Name)
			continue
		}

		fmt.Printf("[%d] MIGRATE: %s\n", i+1, pvc.Name)
		fmt.Printf("    Source: %s (%s)\n", pvc.MatchedVolume.Name, pvc.MatchedVolume.SizeHuman)
		fmt.Printf("    Target: PVC %s/%s (%s)\n", pvc.Namespace, pvc.Name, pvc.NewSize)
		fmt.Printf("    Path: %s → PVC mount\n", pvc.MatchedVolume.Mountpoint)
		fmt.Println()
	}

	fmt.Println("Use --execute to run the actual migration")
}

