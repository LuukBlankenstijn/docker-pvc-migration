package kubernetes

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/LuukBlankenstijn/docker-pvc-migration/internal/types"
	"k8s.io/apimachinery/pkg/util/yaml"
)

type Parser struct{}

func NewParser() *Parser {
	return &Parser{}
}

func (p *Parser) ParseYAMLFiles(directory string) ([]*types.PVCInfo, error) {
	var pvcs []*types.PVCInfo

	err := filepath.Walk(directory, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml") {
			return nil
		}

		filePVCs, err := p.parseYAMLFile(path)
		if err != nil {
			return err
		}

		pvcs = append(pvcs, filePVCs...)
		return nil
	})

	return pvcs, err
}

func (p *Parser) parseYAMLFile(filename string) ([]*types.PVCInfo, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var pvcs []*types.PVCInfo
	decoder := yaml.NewYAMLToJSONDecoder(file)

	for {
		var obj map[string]interface{}
		err := decoder.Decode(&obj)
		if err != nil {
			break // End of file or error
		}

		if kind, ok := obj["kind"].(string); ok && kind == "PersistentVolumeClaim" {
			if pvc := p.parsePVCFromObject(obj); pvc != nil {
				pvcs = append(pvcs, pvc)
			}
		}
	}

	return pvcs, nil
}

func (p *Parser) parsePVCFromObject(obj map[string]interface{}) *types.PVCInfo {
	metadata, ok := obj["metadata"].(map[string]interface{})
	if !ok {
		return nil
	}

	name, ok := metadata["name"].(string)
	if !ok {
		return nil
	}

	namespace := "default"
	if ns, ok := metadata["namespace"].(string); ok {
		namespace = ns
	}

	spec, ok := obj["spec"].(map[string]interface{})
	if !ok {
		return nil
	}

	resources, ok := spec["resources"].(map[string]interface{})
	if !ok {
		return nil
	}

	requests, ok := resources["requests"].(map[string]interface{})
	if !ok {
		return nil
	}

	storage, ok := requests["storage"].(string)
	if !ok {
		return nil
	}

	return &types.PVCInfo{
		Name:          name,
		Namespace:     namespace,
		RequestedSize: storage,
	}
}
