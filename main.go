package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/LuukBlankenstijn/docker-pvc-migration/internal/docker"
	"github.com/LuukBlankenstijn/docker-pvc-migration/internal/kubernetes"
	"github.com/LuukBlankenstijn/docker-pvc-migration/internal/matcher"
	"github.com/LuukBlankenstijn/docker-pvc-migration/internal/migration"
	"github.com/LuukBlankenstijn/docker-pvc-migration/internal/ui"
	"github.com/LuukBlankenstijn/docker-pvc-migration/internal/yaml"
)

func main() {
	var execute = flag.Bool("execute", false, "Execute the migration (default is dry-run)")
	var namespace = flag.String("namespace", "default", "Kubernetes namespace for PVCs")
	flag.Parse()

	if len(flag.Args()) < 1 {
		fmt.Println("Usage: go run main.go [--execute] [--namespace=default] <yaml-directory>")
		os.Exit(1)
	}

	yamlDir := flag.Args()[0]

	// Initialize Docker client
	dockerClient, err := docker.NewClient()
	if err != nil {
		fmt.Printf("Error creating Docker client: %v\n", err)
		os.Exit(1)
	}

	// Load Docker volumes
	fmt.Println("Loading Docker volumes...")
	dockerVolumes, err := dockerClient.LoadVolumes()
	if err != nil {
		fmt.Printf("Error loading Docker volumes: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Found %d Docker volumes\n", len(dockerVolumes))

	// Parse Kubernetes YAML files
	fmt.Printf("Parsing YAML files in %s...\n", yamlDir)
	k8sParser := kubernetes.NewParser()
	pvcs, err := k8sParser.ParseYAMLFiles(yamlDir)
	if err != nil {
		fmt.Printf("Error parsing YAML files: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Found %d PVCs in YAML files\n", len(pvcs))

	// Match Docker volumes to PVCs
	fmt.Println("Matching Docker volumes to PVCs...")
	volumeMatcher := matcher.NewVolumeMatcher(dockerVolumes)

	// Load compose context for better matching
	if err := volumeMatcher.LoadComposeContext(yamlDir); err != nil {
		fmt.Printf("Warning: %v\n", err)
	}

	matchedPVCs := volumeMatcher.MatchVolumes(pvcs)

	// Interactive size configuration
	userInterface := ui.NewInterface()
	if err := userInterface.InteractiveSetSizes(matchedPVCs); err != nil {
		fmt.Printf("Error during interactive setup: %v\n", err)
		os.Exit(1)
	}

	// Print summary
	userInterface.PrintSummary(matchedPVCs)

	// Update YAML files with new sizes
	yamlUpdater := yaml.NewUpdater()
	if err := yamlUpdater.UpdateYAMLFiles(yamlDir, matchedPVCs); err != nil {
		fmt.Printf("Error updating YAML files: %v\n", err)
		os.Exit(1)
	}

	// Migration phase
	migrationEngine := migration.NewEngine(*namespace)

	if *execute {
		fmt.Println("\n🚀 Starting actual migration...")
		if err := migrationEngine.StartMigration(matchedPVCs); err != nil {
			fmt.Printf("Migration failed: %v\n", err)
			os.Exit(1)
		}
	} else {
		migrationEngine.DryRun(matchedPVCs)
	}

	fmt.Println("Process complete!")
}
