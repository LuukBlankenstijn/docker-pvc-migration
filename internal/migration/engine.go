package migration

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/LuukBlankenstijn/docker-pvc-migration/internal/types"
)

type Engine struct {
	namespace string
}

func NewEngine(namespace string) *Engine {
	if namespace == "" {
		namespace = "default"
	}
	return &Engine{namespace: namespace}
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
	// Step 1: Create the PVC
	fmt.Printf("  Creating PVC %s...\n", pvc.Name)
	if err := e.createPVC(pvc); err != nil {
		return fmt.Errorf("failed to create PVC: %v", err)
	}

	// Step 2: Wait for PVC to be bound
	fmt.Printf("  Waiting for PVC to be bound...\n")
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
	// Use kubectl to create the PVC from the YAML files
	// We assume the YAML files have already been updated with the correct sizes

	// Find the YAML file containing this PVC and apply it
	cmd := exec.Command("kubectl", "apply", "-f", "-")

	// Generate PVC YAML directly
	pvcYAML := fmt.Sprintf(`apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: %s
  namespace: %s
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: %s
  storageClassName: longhorn
`, pvc.Name, pvc.Namespace, pvc.NewSize)

	cmd.Stdin = strings.NewReader(pvcYAML)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("kubectl apply failed: %v\nOutput: %s", err, string(output))
	}

	return nil
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
			cmd := exec.Command("kubectl", "get", "pvc", pvc.Name, "-n", pvc.Namespace, "-o", "jsonpath={.status.phase}")
			output, err := cmd.Output()
			if err != nil {
				time.Sleep(interval)
				continue
			}

			phase := strings.TrimSpace(string(output))
			if phase == "Bound" {
				return nil
			}

			fmt.Printf("    PVC status: %s\n", phase)
			time.Sleep(interval)
		}
	}
}

func (e *Engine) copyData(pvc *types.PVCInfo) error {
	// Create a temporary pod to mount both the Docker volume and the PVC
	podName := fmt.Sprintf("migration-%s-%d", pvc.Name, time.Now().Unix())

	podYAML := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: %s
spec:
  restartPolicy: Never
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
`, podName, pvc.Namespace, pvc.MatchedVolume.Mountpoint, pvc.Name)

	// Create the migration pod
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(podYAML)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create migration pod: %v\nOutput: %s", err, string(output))
	}

	// Wait for pod to complete
	fmt.Printf("  Waiting for migration pod to complete...\n")
	if err := e.waitForPodCompletion(podName, pvc.Namespace); err != nil {
		return fmt.Errorf("migration pod failed: %v", err)
	}

	// Show pod logs
	fmt.Printf("  Migration pod logs:\n")
	if err := e.showPodLogs(podName, pvc.Namespace); err != nil {
		fmt.Printf("    Warning: Could not retrieve pod logs: %v\n", err)
	}

	// Clean up the migration pod
	if err := e.deletePod(podName, pvc.Namespace); err != nil {
		fmt.Printf("    Warning: Could not delete migration pod: %v\n", err)
	}

	return nil
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
