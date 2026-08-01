// Package terraform provides optional Terraform CLI verification for generated modules.
package terraform

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2/hclparse"
)

// CheckResult records one verification command.
type CheckResult struct {
	Name   string
	Output string
	Err    error
}

// VerifySyntax checks generated HCL without requiring Terraform or providers.
func VerifySyntax(path string) error {
	parser := hclparse.NewParser()
	_, diagnostics := parser.ParseHCLFile(path)
	if diagnostics.HasErrors() {
		return fmt.Errorf("generated HCL is invalid: %s", diagnostics.Error())
	}
	return nil
}

// RunChecks optionally delegates formatting and semantic validation to Terraform.
// Validation does not run init, so it is intentionally opt-in and expects the
// output directory to already contain initialized providers.
func RunChecks(binary, outputDir, hclPath string, formatCheck, validate bool) []CheckResult {
	if binary == "" {
		binary = "terraform"
	}
	var results []CheckResult
	if formatCheck {
		results = append(results, run(binary, "fmt", "-check", "-diff", hclPath))
	}
	if validate {
		absoluteOutput, err := filepath.Abs(outputDir)
		if err != nil {
			results = append(results, CheckResult{Name: "terraform validate", Err: err})
		} else {
			results = append(results, run(binary, "-chdir="+absoluteOutput, "validate", "-no-color"))
		}
	}
	return results
}

func run(binary string, args ...string) CheckResult {
	cmd := exec.Command(binary, args...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	return CheckResult{
		Name:   strings.TrimSpace(binary + " " + strings.Join(args, " ")),
		Output: strings.TrimSpace(output.String()),
		Err:    err,
	}
}
