package catalog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Remap creates a new mapping snapshot from the latest stored provider and
// module indexes. It performs no network access and preserves the source
// snapshot artifacts by reference.
func Remap(catalogDir, overridesPath string) (*Manifest, string, error) {
	root, err := filepath.Abs(catalogDir)
	if err != nil {
		return nil, "", fmt.Errorf("resolve catalog directory: %w", err)
	}
	current, err := LoadLatest(root)
	if err != nil {
		return nil, "", err
	}

	indexes := make(map[string]*ProviderIndex, len(current.Providers))
	providerNames := make([]string, 0, len(current.Providers))
	for name := range current.Providers {
		providerNames = append(providerNames, name)
	}
	sort.Strings(providerNames)
	for _, name := range providerNames {
		var index ProviderIndex
		if err := loadArtifactJSON(root, current.Providers[name].IndexFile, &index); err != nil {
			return nil, "", fmt.Errorf("load %s provider index: %w", name, err)
		}
		indexes[name] = &index
	}

	modules := make(map[string]*ModuleCatalog, len(current.Modules))
	for name, artifact := range current.Modules {
		var moduleCatalog ModuleCatalog
		if err := loadArtifactJSON(root, artifact.File, &moduleCatalog); err != nil {
			return nil, "", fmt.Errorf("load %s module catalog: %w", name, err)
		}
		modules[name] = &moduleCatalog
	}
	overrides, err := loadOverrides(overridesPath)
	if err != nil {
		return nil, "", fmt.Errorf("load mapping overrides: %w", err)
	}

	generatedAt := time.Now().UTC()
	mappings := generateMappings(indexes, modules, overrides, generatedAt)
	snapshotID := generatedAt.Format("20060102T150405.000000000Z")
	snapshotDir := filepath.Join(root, "snapshots", snapshotID)
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		return nil, "", fmt.Errorf("create remap snapshot: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(snapshotDir)
		}
	}()

	manifest := &Manifest{
		FormatVersion:    FormatVersion,
		SnapshotID:       snapshotID,
		RefreshedAt:      generatedAt,
		TerraformVersion: current.TerraformVersion,
		Providers:        make(map[string]ProviderArtifact, len(current.Providers)),
		Modules:          make(map[string]ModuleArtifact, len(current.Modules)),
		Mappings:         make(map[string]string, len(mappings)),
	}
	for name, artifact := range current.Providers {
		manifest.Providers[name] = artifact
	}
	for name, artifact := range current.Modules {
		manifest.Modules[name] = artifact
	}
	mappingNames := make([]string, 0, len(mappings))
	for name := range mappings {
		mappingNames = append(mappingNames, name)
	}
	sort.Strings(mappingNames)
	for _, name := range mappingNames {
		path := filepath.Join(snapshotDir, "mappings", name+".json.gz")
		if err := writeJSONGzip(path, mappings[name]); err != nil {
			return nil, "", err
		}
		manifest.Mappings[name] = relativeSlash(root, path)
	}

	manifestPath := filepath.Join(snapshotDir, "manifest.json")
	if err := writeJSON(manifestPath, manifest); err != nil {
		return nil, "", err
	}
	pointer := LatestPointer{
		FormatVersion: FormatVersion,
		UpdatedAt:     generatedAt,
		Manifest:      relativeSlash(root, manifestPath),
	}
	if err := writeJSONAtomic(filepath.Join(root, "latest.json"), pointer); err != nil {
		return nil, "", err
	}
	complete = true
	return manifest, manifestPath, nil
}

func loadArtifactJSON(root, relative string, target interface{}) error {
	path, err := safeCatalogPath(root, relative)
	if err != nil {
		return err
	}
	data, err := readCatalogFile(path, maxArtifactBytes)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}
