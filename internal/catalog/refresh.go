package catalog

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DefaultProviders returns the three cloud providers used by terra-translate.
func DefaultProviders() []ProviderSpec {
	return []ProviderSpec{
		{Name: "aws", Source: "hashicorp/aws", Version: "latest"},
		{Name: "google", Source: "hashicorp/google", Version: "latest"},
		{Name: "azurerm", Source: "hashicorp/azurerm", Version: "latest"},
	}
}

// Refresh creates an immutable snapshot and atomically advances latest.json
// only after provider schemas, module metadata, mappings, and manifest succeed.
func Refresh(cfg Config) (*Manifest, string, error) {
	setRefreshDefaults(&cfg)
	registry := newRegistryClient(cfg.RegistryBaseURL, cfg.RequestTimeout)
	return refresh(cfg, registry)
}

func refresh(cfg Config, registry *registryClient) (*Manifest, string, error) {
	refreshedAt := time.Now().UTC()

	specs := append([]ProviderSpec(nil), cfg.Providers...)
	for index := range specs {
		if specs[index].Version == "" || strings.EqualFold(specs[index].Version, "latest") {
			progress(cfg, "resolving latest version: %s", specs[index].Source)
			version, err := registry.latestProviderVersion(specs[index].Source)
			if err != nil {
				return nil, "", err
			}
			specs[index].Version = version
		}
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })

	progress(cfg, "initializing %d pinned providers", len(specs))
	schemaJSON, terraformVersion, err := fetchProviderSchemas(cfg.TerraformBinary, specs, cfg.CommandTimeout)
	if err != nil {
		return nil, "", err
	}
	rawSchemas, indexes, schemaFormat, err := splitProviderSchemas(schemaJSON, specs, refreshedAt)
	if err != nil {
		return nil, "", err
	}

	moduleCatalogs := make(map[string]*ModuleCatalog)
	if cfg.RefreshModules {
		for _, spec := range specs {
			progress(cfg, "refreshing Registry modules: %s", spec.Name)
			modules, err := registry.modules(spec.Name, cfg.ModuleLimit, cfg.ModuleDetails, cfg.DetailWorkers, cfg.DetailRPS, refreshedAt, cfg.Progress)
			if err != nil {
				return nil, "", err
			}
			moduleCatalogs[spec.Name] = modules
		}
	}

	overrides, err := loadOverrides(cfg.OverridesPath)
	if err != nil {
		return nil, "", fmt.Errorf("load mapping overrides: %w", err)
	}
	progress(cfg, "generating cross-provider mappings")
	mappings := generateMappings(indexes, moduleCatalogs, overrides, refreshedAt)

	outputDir, err := filepath.Abs(cfg.OutputDir)
	if err != nil {
		return nil, "", fmt.Errorf("resolve catalog output: %w", err)
	}
	snapshotID := refreshedAt.Format("20060102T150405.000000000Z")
	snapshotRelative := filepath.Join("snapshots", snapshotID)
	snapshotDir := filepath.Join(outputDir, snapshotRelative)
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		return nil, "", fmt.Errorf("create catalog snapshot: %w", err)
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
		RefreshedAt:      refreshedAt,
		TerraformVersion: terraformVersion,
		Providers:        make(map[string]ProviderArtifact),
		Modules:          make(map[string]ModuleArtifact),
		Mappings:         make(map[string]string),
	}
	for _, spec := range specs {
		providerDir := filepath.Join(snapshotDir, "providers", spec.Name)
		rawPath := filepath.Join(providerDir, "schema.json.gz")
		indexPath := filepath.Join(providerDir, "index.json.gz")
		if err := writeGzip(rawPath, rawSchemas[spec.Name]); err != nil {
			return nil, "", err
		}
		if err := writeJSONGzip(indexPath, indexes[spec.Name]); err != nil {
			return nil, "", err
		}
		index := indexes[spec.Name]
		manifest.Providers[spec.Name] = ProviderArtifact{
			Source:              spec.Source,
			Version:             spec.Version,
			SchemaFormatVersion: schemaFormat,
			RawSchemaFile:       relativeSlash(outputDir, rawPath),
			IndexFile:           relativeSlash(outputDir, indexPath),
			Resources:           len(index.Resources),
			DataSources:         len(index.DataSources),
			EphemeralResources:  len(index.EphemeralResources),
			Functions:           len(index.Functions),
		}
		if modules := moduleCatalogs[spec.Name]; modules != nil {
			modulePath := filepath.Join(snapshotDir, "modules", spec.Name+".json.gz")
			if err := writeJSONGzip(modulePath, modules); err != nil {
				return nil, "", err
			}
			manifest.Modules[spec.Name] = ModuleArtifact{
				File:          relativeSlash(outputDir, modulePath),
				Modules:       len(modules.Modules),
				Detailed:      len(modules.Details),
				RegistryQuery: cfg.RegistryBaseURL + "/v1/modules?provider=" + spec.Name,
			}
		}
	}

	mappingNames := make([]string, 0, len(mappings))
	for name := range mappings {
		mappingNames = append(mappingNames, name)
	}
	sort.Strings(mappingNames)
	for _, name := range mappingNames {
		mappingPath := filepath.Join(snapshotDir, "mappings", name+".json.gz")
		if err := writeJSONGzip(mappingPath, mappings[name]); err != nil {
			return nil, "", err
		}
		manifest.Mappings[name] = relativeSlash(outputDir, mappingPath)
	}

	manifestPath := filepath.Join(snapshotDir, "manifest.json")
	if err := writeJSON(manifestPath, manifest); err != nil {
		return nil, "", err
	}
	pointer := LatestPointer{
		FormatVersion: FormatVersion,
		UpdatedAt:     refreshedAt,
		Manifest:      relativeSlash(outputDir, manifestPath),
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, "latest.json"), pointer); err != nil {
		return nil, "", err
	}
	complete = true
	progress(cfg, "catalog snapshot complete: %s", snapshotID)
	return manifest, manifestPath, nil
}

func setRefreshDefaults(cfg *Config) {
	if cfg.OutputDir == "" {
		cfg.OutputDir = "./catalog"
	}
	if cfg.TerraformBinary == "" {
		cfg.TerraformBinary = "terraform"
	}
	if cfg.RegistryBaseURL == "" {
		cfg.RegistryBaseURL = "https://registry.terraform.io"
	}
	if len(cfg.Providers) == 0 {
		cfg.Providers = DefaultProviders()
	}
	if cfg.DetailWorkers <= 0 {
		cfg.DetailWorkers = 6
	}
	if cfg.DetailRPS <= 0 {
		cfg.DetailRPS = 10
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 45 * time.Second
	}
	if cfg.CommandTimeout <= 0 {
		cfg.CommandTimeout = 20 * time.Minute
	}
}

func progress(cfg Config, format string, args ...interface{}) {
	if cfg.Progress != nil {
		cfg.Progress(format, args...)
	}
}

func writeJSON(path string, value interface{}) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return writeBytes(path, append(data, '\n'))
}

func writeJSONGzip(path string, value interface{}) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return writeGzip(path, append(data, '\n'))
}

func writeGzip(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create directory for %s: %w", path, err)
	}
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	writer := gzip.NewWriter(file)
	writer.Header.ModTime = time.Unix(0, 0).UTC()
	if _, err := writer.Write(data); err != nil {
		_ = writer.Close()
		_ = file.Close()
		return fmt.Errorf("compress %s: %w", path, err)
	}
	if err := writer.Close(); err != nil {
		_ = file.Close()
		return fmt.Errorf("compress %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}

func writeBytes(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create directory for %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func writeJSONAtomic(path string, value interface{}) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".latest-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func relativeSlash(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(relative)
}
