package main

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestRunTerraformCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	status := run([]string{
		"terraform",
		"-input", filepath.Join("example", "terragrunt", "modules", "storage"),
		"-output", t.TempDir(),
		"-to", "google",
		"-fmt-check=false",
		"-min-accuracy", "0.5",
	}, &stdout, &stderr)
	if status != 0 {
		t.Fatalf("status = %d\nstdout:\n%s\nstderr:\n%s", status, stdout.String(), stderr.String())
	}
}

func TestRunHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if status := run([]string{"help"}, &stdout, &stderr); status != 0 {
		t.Fatalf("help status = %d", status)
	}
}

func TestRunRealtimeExampleProducesValidHCL(t *testing.T) {
	var stdout, stderr bytes.Buffer
	status := run([]string{
		"terraform",
		"-input", filepath.Join("example", "realtime", "aws-web-platform"),
		"-output", t.TempDir(),
		"-from", "aws",
		"-to", "azurerm",
		"-fmt-check=false",
		"-min-accuracy", "0",
	}, &stdout, &stderr)
	if status != 0 {
		t.Fatalf("status = %d\nstdout:\n%s\nstderr:\n%s", status, stdout.String(), stderr.String())
	}
}
