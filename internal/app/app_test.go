package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/terra-translate/internal/parser"
)

func TestInferProvider(t *testing.T) {
	module, err := parser.New().ParsePath(filepath.Join("..", "..", "example", "realtime", "aws-web-platform"))
	if err != nil {
		t.Fatal(err)
	}
	provider, err := InferProvider(module)
	if err != nil {
		t.Fatal(err)
	}
	if provider != "aws" {
		t.Fatalf("provider = %q, want aws", provider)
	}
}

func TestTranslatePreservesModuleInterface(t *testing.T) {
	output := t.TempDir()
	outcome, err := Translate(Config{
		InputPath:   filepath.Join("..", "..", "example", "terragrunt", "modules", "storage"),
		OutputDir:   output,
		From:        "auto",
		To:          "google",
		Kp:          0.8,
		Ki:          0.1,
		Kd:          0.05,
		MaxIter:     8,
		MinAccuracy: 0.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.From != "aws" {
		t.Fatalf("source provider = %q, want aws", outcome.From)
	}
	generated, err := os.ReadFile(outcome.HCLPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(generated)
	for _, expected := range []string{
		`variable "bucket_name"`,
		`resource "google_storage_bucket" "assets"`,
		`output "bucket_id"`,
		`value = google_storage_bucket.assets.self_link`,
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("generated HCL does not contain %q\n%s", expected, text)
		}
	}
}
