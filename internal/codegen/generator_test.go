package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/terra-translate/internal/ir"
	"github.com/terra-translate/internal/translator"
)

func generate(t *testing.T, provider string, result *translator.Result) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "main.tf")
	if err := New(provider).WriteHCL(path, result); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Everything this package writes has to be parseable Terraform.
	if _, diagnostics := hclparse.NewParser().ParseHCL(data, path); diagnostics.HasErrors() {
		t.Fatalf("generated invalid HCL: %s\n%s", diagnostics.Error(), data)
	}
	return string(data)
}

func TestWriteHCLEmitsParseableOutputForAnEmptyModule(t *testing.T) {
	out := generate(t, "google", &translator.Result{SourceModule: &ir.Module{}})
	if !strings.Contains(out, "terraform") {
		t.Fatalf("expected a terraform block:\n%s", out)
	}
}

func TestWriteHCLPreservesTheModuleInterface(t *testing.T) {
	result := &translator.Result{
		SourceModule: &ir.Module{
			Variables: map[string]*ir.Variable{
				"bucket_name": {Name: "bucket_name", Type: "string", Description: "Bucket"},
				"db_password": {Name: "db_password", Type: "string", Sensitive: true},
			},
			Outputs: map[string]*ir.Output{
				"bucket_id": {Name: "bucket_id", Value: "aws_s3_bucket.assets.id"},
			},
		},
		RefGraph: ir.NewRefGraph(),
	}
	out := generate(t, "google", result)

	// Terragrunt callers depend on the variable and output contract surviving.
	for _, want := range []string{`variable "bucket_name"`, `variable "db_password"`, `output "bucket_id"`} {
		if !strings.Contains(out, want) {
			t.Errorf("generated module dropped %s:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "sensitive") {
		t.Errorf("sensitive marking was lost:\n%s", out)
	}
}

func TestWriteHCLRewritesOutputReferencesThroughTheGraph(t *testing.T) {
	graph := ir.NewRefGraph()
	graph.Register("aws_s3_bucket", "assets", "google_storage_bucket", "assets")

	result := &translator.Result{
		SourceModule: &ir.Module{
			Outputs: map[string]*ir.Output{
				"bucket": {Name: "bucket", Value: "${aws_s3_bucket.assets.id}"},
			},
		},
		RefGraph: graph,
	}
	out := generate(t, "google", result)

	// The parser represents references as "${resource.name.attr}", and only
	// that form is rewritten.
	if strings.Contains(out, "aws_s3_bucket.assets") {
		t.Errorf("output still references the source provider:\n%s", out)
	}
	if !strings.Contains(out, "google_storage_bucket.assets") {
		t.Errorf("output was not rewritten to the target resource:\n%s", out)
	}
}

func TestWriteHCLWritesResourcesAndTodos(t *testing.T) {
	result := &translator.Result{
		SourceModule: &ir.Module{},
		TargetResources: []*ir.TargetResource{
			{
				ProviderType: "google_storage_bucket",
				Name:         "assets",
				Attributes: map[string]interface{}{
					"name":     "example-assets",
					"location": "US",
				},
				TodoAttrs: map[string]string{"location": "verify the target region"},
			},
		},
		RefGraph: ir.NewRefGraph(),
	}
	out := generate(t, "google", result)

	if !strings.Contains(out, `resource "google_storage_bucket" "assets"`) {
		t.Fatalf("resource block missing:\n%s", out)
	}
	if !strings.Contains(out, "TODO") {
		t.Fatalf("TODO annotations are the review signal and must be emitted:\n%s", out)
	}
}

func TestWriteHCLSkipsNilTargetResources(t *testing.T) {
	// A 1:N expansion can leave nil slots; they must not panic or emit blocks.
	result := &translator.Result{
		SourceModule:    &ir.Module{},
		TargetResources: []*ir.TargetResource{nil, {ProviderType: "google_storage_bucket", Name: "a"}, nil},
		RefGraph:        ir.NewRefGraph(),
	}
	out := generate(t, "google", result)
	if strings.Count(out, "resource \"") != 1 {
		t.Fatalf("expected exactly one resource block:\n%s", out)
	}
}

func TestWriteHCLEmitsWarnings(t *testing.T) {
	result := &translator.Result{
		SourceModule: &ir.Module{},
		Warnings: []ir.TranslationWarning{
			{Message: "aws_cloudfront_distribution has no direct equivalent"},
		},
		RefGraph: ir.NewRefGraph(),
	}
	out := generate(t, "google", result)
	if !strings.Contains(out, "no direct equivalent") {
		t.Fatalf("warnings must reach the generated file:\n%s", out)
	}
}

func TestProviderSourceCoversEverySupportedProvider(t *testing.T) {
	cases := map[string]string{
		"aws":     "hashicorp/aws",
		"google":  "hashicorp/google",
		"azurerm": "hashicorp/azurerm",
	}
	for provider, want := range cases {
		if got := providerSource(provider); got != want {
			t.Errorf("providerSource(%q) = %q, want %q", provider, got, want)
		}
	}
}

func TestWriteHCLFailsOnAnUnwritableDestination(t *testing.T) {
	err := New("google").WriteHCL(
		filepath.Join(t.TempDir(), "missing-dir", "main.tf"),
		&translator.Result{SourceModule: &ir.Module{}},
	)
	if err == nil {
		t.Fatal("writing into a missing directory must return an error")
	}
}
