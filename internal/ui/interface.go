package ui

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/LuukBlankenstijn/docker-pvc-migration/internal/types"
	"k8s.io/apimachinery/pkg/api/resource"
)

type Interface struct {
	reader *bufio.Reader
}

func NewInterface() *Interface {
	return &Interface{
		reader: bufio.NewReader(os.Stdin),
	}
}

func (ui *Interface) InteractiveSetSizes(pvcs []*types.PVCInfo) error {
	fmt.Println("\n=== PVC Size Configuration ===")
	fmt.Println("For each PVC, review the matched Docker volume and set the desired size.")
	fmt.Println("Use formats like: 1Gi, 500Mi, 2Ti, etc.")
	fmt.Println()

	for _, pvc := range pvcs {
		fmt.Printf("PVC: %s (namespace: %s)\n", pvc.Name, pvc.Namespace)
		fmt.Printf("  Kompose suggested size: %s\n", pvc.RequestedSize)

		if pvc.MatchedVolume != nil {
			fmt.Printf("  Matched Docker volume: %s\n", pvc.MatchedVolume.Name)
			fmt.Printf("  Current volume size: %s\n", pvc.MatchedVolume.SizeHuman)
			fmt.Printf("  Volume path: %s\n", pvc.MatchedVolume.Mountpoint)
		} else {
			fmt.Printf("  ⚠️  No matching Docker volume found!\n")
		}

		fmt.Print("  Enter desired PVC size (or press Enter to use suggested): ")
		input, err := ui.reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("failed to read input: %v", err)
		}

		input = strings.TrimSpace(input)
		if input == "" {
			pvc.NewSize = pvc.RequestedSize
		} else {
			if ui.isValidSize(input) {
				pvc.NewSize = input
			} else {
				fmt.Printf("  ⚠️  Invalid size format, using suggested: %s\n", pvc.RequestedSize)
				pvc.NewSize = pvc.RequestedSize
			}
		}

		fmt.Printf("  ✅ Set PVC size to: %s\n", pvc.NewSize)
		fmt.Println()
	}

	return nil
}

func (ui *Interface) isValidSize(size string) bool {
	_, err := resource.ParseQuantity(size)
	return err == nil
}

func (ui *Interface) PrintSummary(pvcs []*types.PVCInfo) {
	fmt.Println("\n=== Migration Summary ===")
	fmt.Printf("Found %d PVCs to migrate:\n\n", len(pvcs))

	for _, pvc := range pvcs {
		fmt.Printf("PVC: %s/%s\n", pvc.Namespace, pvc.Name)
		fmt.Printf("  Size: %s → %s\n", pvc.RequestedSize, pvc.NewSize)

		if pvc.MatchedVolume != nil {
			fmt.Printf("  Source: %s (%s)\n", pvc.MatchedVolume.Name, pvc.MatchedVolume.SizeHuman)
		} else {
			fmt.Printf("  Source: ⚠️  No matching volume found\n")
		}
		fmt.Println()
	}
}
