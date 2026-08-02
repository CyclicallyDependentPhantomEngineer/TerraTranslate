package catalog

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSplitProviderSchemasPreservesRawAndIndexesAllKinds(t *testing.T) {
	data := []byte(testSchemaJSON())
	specs := testProviderSpecs("1.2.3")
	raw, indexes, format, err := splitProviderSchemas(data, specs, time.Unix(1, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if format != "1.0" || len(raw) != 3 || len(indexes) != 3 {
		t.Fatalf("unexpected split result: format=%s raw=%d indexes=%d", format, len(raw), len(indexes))
	}
	aws := indexes["aws"]
	if len(aws.Resources) != 1 || len(aws.DataSources) != 1 || len(aws.EphemeralResources) != 1 || len(aws.Functions) != 1 {
		t.Fatalf("AWS index lost schema kinds: %+v", aws)
	}
	attributes := aws.Resources["aws_instance"].Attributes
	if len(attributes) != 3 || attributes[0].Path != "instance_type" || attributes[1].Path != "network_interface.subnet_id" {
		t.Fatalf("nested attributes not flattened deterministically: %+v", attributes)
	}
	if !json.Valid(raw["aws"]) {
		t.Fatal("stored raw schema is not valid JSON")
	}
}

func TestRegistryClientResolvesLatestAndPaginatesModules(t *testing.T) {
	client := newRegistryClient("https://registry.test", time.Second)
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := ""
		switch {
		case strings.HasSuffix(r.URL.Path, "/v1/providers/hashicorp/aws/versions"):
			body = `{"versions":[{"version":"6.2.0-beta.1"},{"version":"5.99.0"},{"version":"6.1.0"}]}`
		case r.URL.Path == "/v1/modules" && r.URL.Query().Get("offset") == "0":
			body = `{"meta":{"next_offset":1},"modules":[{"id":"org/network/aws/1.0.0","namespace":"org","name":"network","provider":"aws","version":"1.0.0"}]}`
		case r.URL.Path == "/v1/modules" && r.URL.Query().Get("offset") == "1":
			body = `{"meta":{},"modules":[{"id":"org/storage/aws/2.0.0","namespace":"org","name":"storage","provider":"aws","version":"2.0.0"}]}`
		default:
			return testHTTPResponse(http.StatusNotFound, "not found"), nil
		}
		return testHTTPResponse(http.StatusOK, body), nil
	})

	version, err := client.latestProviderVersion("hashicorp/aws")
	if err != nil {
		t.Fatal(err)
	}
	if version != "6.1.0" {
		t.Fatalf("latest stable version = %q, want 6.1.0", version)
	}
	modules, err := client.modules("aws", 0, false, 1, 10, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(modules.Modules) != 2 {
		t.Fatalf("module count = %d, want 2", len(modules.Modules))
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, time.August, 2, 0, 0, 0, 0, time.UTC)
	if got := parseRetryAfter("12", now); got != 12*time.Second {
		t.Fatalf("seconds retry-after = %s, want 12s", got)
	}
	date := now.Add(30 * time.Second).Format(http.TimeFormat)
	if got := parseRetryAfter(date, now); got != 30*time.Second {
		t.Fatalf("date retry-after = %s, want 30s", got)
	}
}

func TestGenerateMappingsUsesOverridesAndCreatesAllPairs(t *testing.T) {
	indexes := testIndexes()
	overrides := OverrideFile{Resources: []ResourceOverride{{
		SourceProvider: "aws", TargetProvider: "google",
		SourceType: "aws_instance", TargetType: "google_compute_instance",
		Attributes: map[string]string{"instance_type": "machine_type"},
	}}}
	mappings := generateMappings(indexes, nil, overrides, time.Now())
	if len(mappings) != 6 {
		t.Fatalf("mapping pairs = %d, want 6", len(mappings))
	}
	awsGoogle := mappings["aws-to-google"]
	if len(awsGoogle.Resources) != 1 {
		t.Fatalf("unexpected AWS to Google resources: %+v", awsGoogle.Resources)
	}
	resource := awsGoogle.Resources[0]
	if !resource.Manual || resource.TargetType != "google_compute_instance" || resource.Score != 1 {
		t.Fatalf("manual override not authoritative: %+v", resource)
	}
	if len(resource.Attributes) == 0 || !resource.Attributes[0].Manual {
		t.Fatalf("manual attribute override missing: %+v", resource.Attributes)
	}
}

func TestGenerateMappingsIgnoresAttributesForMissingOverrideTarget(t *testing.T) {
	overrides := OverrideFile{Resources: []ResourceOverride{{
		SourceProvider: "aws", TargetProvider: "google",
		SourceType: "aws_instance", TargetType: "google_removed_resource",
		Attributes: map[string]string{"instance_type": "machine_type"},
	}}}
	mappings := generateMappings(testIndexes(), nil, overrides, time.Now())
	resource := mappings["aws-to-google"].Resources[0]
	if resource.Manual {
		t.Fatalf("missing override target must not mark generated resource manual: %+v", resource)
	}
	for _, attribute := range resource.Attributes {
		if attribute.Manual {
			t.Fatalf("missing override target leaked manual attribute: %+v", attribute)
		}
	}
}

func TestAttributeMappingsReserveExactTopLevelPath(t *testing.T) {
	typeString := json.RawMessage(`"string"`)
	source := describeObject("azurerm_example", "azurerm", ObjectIndex{Attributes: []AttributeIndex{
		{Path: "identifier", Type: typeString, Optional: true},
		{Path: "name", Type: typeString, Optional: true},
	}})
	target := describeObject("google_example", "google", ObjectIndex{Attributes: []AttributeIndex{
		{Path: "name", Type: typeString, Optional: true},
		{Path: "network_interface.name", Type: typeString, Optional: true},
	}})
	mappings := generateAttributeMappings(source, target, nil)
	for _, mapping := range mappings {
		if mapping.SourcePath == "name" {
			if mapping.TargetPath != "name" {
				t.Fatalf("exact top-level name mapped to %q", mapping.TargetPath)
			}
			return
		}
	}
	t.Fatal("name mapping missing")
}

func TestRefreshWritesImmutableSnapshotAndLatestPointer(t *testing.T) {
	registry := newRegistryClient("https://registry.test", time.Second)
	registry.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/v1/providers/") {
			return testHTTPResponse(http.StatusOK, `{"versions":[{"version":"1.2.3"}]}`), nil
		}
		if r.URL.Path == "/v1/modules" {
			provider := r.URL.Query().Get("provider")
			return testHTTPResponse(http.StatusOK, `{"meta":{},"modules":[{"id":"org/network/`+provider+`/1.0.0","namespace":"org","name":"network","provider":"`+provider+`","version":"1.0.0"}]}`), nil
		}
		return testHTTPResponse(http.StatusNotFound, "not found"), nil
	})

	fakeTerraform := filepath.Join(t.TempDir(), "terraform")
	script := `#!/bin/sh
case "$*" in
  *"providers schema -json"*)
    printf '%s' "$FAKE_TERRAFORM_SCHEMA"
    ;;
  version)
    printf 'Terraform v1.15.8\n'
    ;;
  *)
    exit 0
    ;;
esac
`
	if err := os.WriteFile(fakeTerraform, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_TERRAFORM_SCHEMA", testSchemaJSON())
	output := t.TempDir()
	config := Config{
		OutputDir:       output,
		TerraformBinary: fakeTerraform,
		RegistryBaseURL: "https://registry.test",
		Providers:       testProviderSpecs("latest"),
		RefreshModules:  true,
		RequestTimeout:  time.Second,
		CommandTimeout:  10 * time.Second,
	}
	setRefreshDefaults(&config)
	manifest, manifestPath, err := refresh(config, registry)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Providers) != 3 || len(manifest.Modules) != 3 || len(manifest.Mappings) != 6 {
		t.Fatalf("incomplete manifest: %+v", manifest)
	}
	if _, err := os.Stat(manifestPath); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadLatest(output)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SnapshotID != manifest.SnapshotID {
		t.Fatalf("latest snapshot = %s, want %s", loaded.SnapshotID, manifest.SnapshotID)
	}
	if _, err := LoadMapping(output, "aws", "google"); err != nil {
		t.Fatal(err)
	}
	awsArtifact := manifest.Providers["aws"]
	if !strings.HasSuffix(awsArtifact.RawSchemaFile, ".json.gz") {
		t.Fatalf("raw provider schema is not compressed: %s", awsArtifact.RawSchemaFile)
	}
	rawSchema, err := readCatalogFile(filepath.Join(output, filepath.FromSlash(awsArtifact.RawSchemaFile)), maxArtifactBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(rawSchema) || !strings.Contains(string(rawSchema), `"aws_instance"`) {
		t.Fatal("compressed raw provider schema did not round trip")
	}
	remapped, remapManifestPath, err := Remap(output, "")
	if err != nil {
		t.Fatal(err)
	}
	if remapped.SnapshotID == manifest.SnapshotID {
		t.Fatal("remap did not create a new immutable snapshot")
	}
	if remapped.Providers["aws"].IndexFile != manifest.Providers["aws"].IndexFile {
		t.Fatal("remap should reference the existing provider index")
	}
	if !strings.Contains(remapped.Mappings["aws-to-google"], remapped.SnapshotID) {
		t.Fatalf("remap mapping does not belong to new snapshot: %s", remapped.Mappings["aws-to-google"])
	}
	if _, err := os.Stat(remapManifestPath); err != nil {
		t.Fatal(err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func testHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func testProviderSpecs(version string) []ProviderSpec {
	return []ProviderSpec{
		{Name: "aws", Source: "hashicorp/aws", Version: version},
		{Name: "google", Source: "hashicorp/google", Version: version},
		{Name: "azurerm", Source: "hashicorp/azurerm", Version: version},
	}
}

func testIndexes() map[string]*ProviderIndex {
	typeString := json.RawMessage(`"string"`)
	return map[string]*ProviderIndex{
		"aws": {Resources: map[string]ObjectIndex{
			"aws_instance": {Attributes: []AttributeIndex{{Path: "instance_type", Type: typeString, Optional: true}}},
		}},
		"google": {Resources: map[string]ObjectIndex{
			"google_compute_instance": {Attributes: []AttributeIndex{{Path: "machine_type", Type: typeString, Required: true}}},
		}},
		"azurerm": {Resources: map[string]ObjectIndex{
			"azurerm_linux_virtual_machine": {Attributes: []AttributeIndex{{Path: "size", Type: typeString, Required: true}}},
		}},
	}
}

func testSchemaJSON() string {
	return `{
  "format_version": "1.0",
  "provider_schemas": {
    "registry.terraform.io/hashicorp/aws": {
      "provider": {"version": 0, "block": {"attributes": {"region": {"type": "string", "optional": true}}}},
      "resource_schemas": {
        "aws_instance": {"version": 1, "block": {
          "attributes": {
            "instance_type": {"type": "string", "required": true},
            "tags": {"type": ["map", "string"], "optional": true}
          },
          "block_types": {"network_interface": {"nesting_mode": "list", "block": {"attributes": {"subnet_id": {"type": "string", "optional": true}}}}}
        }}
      },
      "data_source_schemas": {"aws_instance": {"version": 0, "block": {"attributes": {"id": {"type": "string", "required": true}}}}},
      "ephemeral_resource_schemas": {"aws_token": {"version": 0, "block": {"attributes": {"token": {"type": "string", "computed": true, "sensitive": true}}}}},
      "functions": {"arn_parse": {"summary": "Parse an ARN", "return_type": "string"}}
    },
    "registry.terraform.io/hashicorp/google": {
      "provider": {"version": 0, "block": {"attributes": {"project": {"type": "string", "optional": true}}}},
      "resource_schemas": {"google_compute_instance": {"version": 1, "block": {"attributes": {"machine_type": {"type": "string", "required": true}, "labels": {"type": ["map", "string"], "optional": true}}}}},
      "data_source_schemas": {}, "ephemeral_resource_schemas": {}, "functions": {}
    },
    "registry.terraform.io/hashicorp/azurerm": {
      "provider": {"version": 0, "block": {"attributes": {}}},
      "resource_schemas": {"azurerm_linux_virtual_machine": {"version": 1, "block": {"attributes": {"size": {"type": "string", "required": true}, "tags": {"type": ["map", "string"], "optional": true}}}}},
      "data_source_schemas": {}, "ephemeral_resource_schemas": {}, "functions": {}
    }
  }
}`
}
