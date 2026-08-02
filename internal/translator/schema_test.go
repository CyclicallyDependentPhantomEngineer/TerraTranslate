package translator

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/terra-translate/internal/ir"
)

func TestProviderSchemaAddsWritableFuzzyCandidates(t *testing.T) {
	schema := `{
  "provider_schemas": {
    "registry.terraform.io/hashicorp/google": {
      "resource_schemas": {
        "google_storage_bucket": {
          "block": {
            "attributes": {
              "custom_bucket_flag": {"optional": true},
              "server_value": {"computed": true}
            },
            "block_types": {
              "lifecycle_rule": {
                "block": {
                  "attributes": {
                    "condition_name": {"optional": true}
                  }
                }
              }
            }
          }
        }
      }
    }
  }
}`
	path := filepath.Join(t.TempDir(), "schema.json")
	if err := os.WriteFile(path, []byte(schema), 0o600); err != nil {
		t.Fatal(err)
	}

	mappings, err := LoadMappings("aws", "google", path)
	if err != nil {
		t.Fatal(err)
	}
	mapping := mappings.Resources["aws_s3_bucket"]
	if !slices.Contains(mapping.TargetAttrs, "custom_bucket_flag") {
		t.Fatalf("writable schema attribute missing: %v", mapping.TargetAttrs)
	}
	if !slices.Contains(mapping.TargetAttrs, "lifecycle_rule.condition_name") {
		t.Fatalf("nested schema attribute missing: %v", mapping.TargetAttrs)
	}
	if slices.Contains(mapping.TargetAttrs, "server_value") {
		t.Fatalf("computed-only attribute was included: %v", mapping.TargetAttrs)
	}

	module := &ir.Module{Resources: []*ir.Resource{{
		OriginalType: "aws_s3_bucket",
		Name:         "assets",
		Properties: map[string]*ir.Property{
			"custom_bucket_flag": {Name: "custom_bucket_flag", Value: true},
		},
	}}}
	result := New(mappings).Translate(module, 1)
	if got := result.TargetResources[0].Attributes["custom_bucket_flag"]; got != true {
		t.Fatalf("schema candidate was not used, value = %#v", got)
	}
	if result.TargetResources[0].TodoAttrs["custom_bucket_flag"] == "" {
		t.Fatal("schema-driven fuzzy match should require manual review")
	}
}
