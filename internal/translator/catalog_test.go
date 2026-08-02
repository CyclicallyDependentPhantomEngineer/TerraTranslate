package translator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/terra-translate/internal/catalog"
	"github.com/terra-translate/internal/ir"
)

func TestCatalogGeneratedMappingExtendsUnsupportedPair(t *testing.T) {
	root := t.TempDir()
	mappingPath := filepath.Join(root, "snapshots", "test", "mappings", "azurerm-to-google.json")
	mapping := catalog.MappingCatalog{
		FormatVersion: "1.0", SourceProvider: "azurerm", TargetProvider: "google",
		Resources: []catalog.ResourceMappingCandidate{{
			SourceType: "azurerm_linux_virtual_machine",
			TargetType: "google_compute_instance",
			Score:      1,
			Manual:     true,
			Attributes: []catalog.AttributeMappingCandidate{{
				SourcePath: "name", TargetPath: "name", Score: 1, Manual: true,
			}},
		}},
	}
	writeTestJSON(t, mappingPath, mapping)
	manifestPath := filepath.Join(root, "snapshots", "test", "manifest.json")
	writeTestJSON(t, manifestPath, catalog.Manifest{
		FormatVersion: "1.0", SnapshotID: "test",
		Mappings: map[string]string{"azurerm-to-google": "snapshots/test/mappings/azurerm-to-google.json"},
	})
	writeTestJSON(t, filepath.Join(root, "latest.json"), catalog.LatestPointer{
		FormatVersion: "1.0", UpdatedAt: time.Now(), Manifest: "snapshots/test/manifest.json",
	})

	mappings, err := LoadMappingsWithCatalog("azurerm", "google", "", root)
	if err != nil {
		t.Fatal(err)
	}
	resourceMapping, exists := mappings.Resources["azurerm_linux_virtual_machine"]
	if !exists || !resourceMapping.Generated || len(resourceMapping.AttrMaps) != 1 || !resourceMapping.AttrMaps[0].Generated {
		t.Fatalf("catalog mapping was not loaded: %+v", resourceMapping)
	}

	module := &ir.Module{Resources: []*ir.Resource{{
		OriginalType: "azurerm_linux_virtual_machine", Name: "web",
		Properties: map[string]*ir.Property{"name": {Name: "name", Value: "web"}},
	}}}
	result := New(mappings).Translate(module, 0.5)
	target := result.TargetResources[0]
	if target.ProviderType != "google_compute_instance" || target.TodoAttrs["name"] == "" {
		t.Fatalf("catalog translation should be marked for review: %+v", target)
	}
}

func writeTestJSON(t *testing.T, path string, value interface{}) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
