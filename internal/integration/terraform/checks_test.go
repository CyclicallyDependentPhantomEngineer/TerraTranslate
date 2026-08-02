package terraform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVerifySyntax(t *testing.T) {
	directory := t.TempDir()
	valid := filepath.Join(directory, "valid.tf")
	if err := os.WriteFile(valid, []byte(`resource "aws_s3_bucket" "test" {}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifySyntax(valid); err != nil {
		t.Fatalf("valid HCL rejected: %v", err)
	}

	invalid := filepath.Join(directory, "invalid.tf")
	if err := os.WriteFile(invalid, []byte(`resource "broken" {`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifySyntax(invalid); err == nil {
		t.Fatal("invalid HCL was accepted")
	}
}
