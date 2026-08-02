// Package terragrunt adapts Terragrunt units to the Terraform module translator.
package terragrunt

import (
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// Unit is a discovered terragrunt.hcl and its Terraform module source.
type Unit struct {
	ConfigPath      string `json:"config_path"`
	Directory       string `json:"directory"`
	Source          string `json:"source,omitempty"`
	ModulePath      string `json:"module_path,omitempty"`
	Remote          bool   `json:"remote"`
	Unresolved      bool   `json:"unresolved"`
	ResolutionError string `json:"resolution_error,omitempty"`
}

// DiscoverUnits recursively finds Terragrunt units without entering cache or
// generated output directories. Source expressions are deliberately resolved
// statically: discovery never downloads remote modules or reads remote state.
func DiscoverUnits(root, outputRoot string) ([]Unit, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve Terragrunt root: %w", err)
	}
	info, err := os.Stat(absoluteRoot)
	if err != nil {
		return nil, fmt.Errorf("stat Terragrunt root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("Terragrunt root %q is not a directory", absoluteRoot)
	}

	absoluteOutput := ""
	if outputRoot != "" {
		absoluteOutput, _ = filepath.Abs(outputRoot)
	}

	var configs []string
	err = filepath.WalkDir(absoluteRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != absoluteRoot && shouldSkipDirectory(path, entry.Name(), absoluteOutput) {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() == "terragrunt.hcl" {
			configs = append(configs, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discover Terragrunt units: %w", err)
	}
	sort.Strings(configs)

	units := make([]Unit, 0, len(configs))
	for _, configPath := range configs {
		units = append(units, inspectUnit(configPath))
	}
	return units, nil
}

func shouldSkipDirectory(path, name, outputRoot string) bool {
	if outputRoot != "" && sameOrChildPath(path, outputRoot) {
		return true
	}
	if name == ".terragrunt-cache" || name == ".git" || name == ".terraform" {
		return true
	}
	return strings.HasPrefix(name, ".")
}

func sameOrChildPath(path, parent string) bool {
	relative, err := filepath.Rel(parent, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func inspectUnit(configPath string) Unit {
	directory := filepath.Dir(configPath)
	unit := Unit{ConfigPath: configPath, Directory: directory, ModulePath: directory}

	parser := hclparse.NewParser()
	file, diagnostics := parser.ParseHCLFile(configPath)
	if diagnostics.HasErrors() {
		unit.Unresolved = true
		unit.ResolutionError = diagnostics.Error()
		return unit
	}
	body, ok := file.Body.(*hclsyntax.Body)
	if !ok {
		unit.Unresolved = true
		unit.ResolutionError = "unexpected Terragrunt HCL body"
		return unit
	}

	for _, block := range body.Blocks {
		if block.Type != "terraform" {
			continue
		}
		attribute, exists := block.Body.Attributes["source"]
		if !exists {
			return unit
		}
		value, valueDiagnostics := attribute.Expr.Value(nil)
		if valueDiagnostics.HasErrors() || value == cty.NilVal || !value.IsKnown() || value.Type() != cty.String {
			unit.Unresolved = true
			unit.ResolutionError = "terraform.source is dynamic; use a literal local path or translate the materialized cache directory"
			return unit
		}
		unit.Source = value.AsString()
		unit.Remote = isRemoteSource(unit.Source)
		if !unit.Remote {
			modulePath, resolveErr := resolveLocalSource(directory, unit.Source)
			if resolveErr != nil {
				unit.Unresolved = true
				unit.ResolutionError = resolveErr.Error()
				return unit
			}
			unit.ModulePath = modulePath
		}
		return unit
	}
	return unit
}

func isRemoteSource(source string) bool {
	lower := strings.ToLower(strings.TrimSpace(source))
	remotePrefixes := []string{
		"git::", "hg::", "s3::", "gcs::", "tfr://", "http://", "https://", "github.com/", "bitbucket.org/",
	}
	for _, prefix := range remotePrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func resolveLocalSource(unitDirectory, source string) (string, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return unitDirectory, nil
	}
	if parsed, err := url.Parse(source); err == nil && parsed.Scheme == "file" {
		source = parsed.Path
	}
	if query := strings.IndexByte(source, '?'); query >= 0 {
		source = source[:query]
	}
	source = collapsePackageSubdir(source)
	if source == "" {
		return "", fmt.Errorf("empty local terraform.source")
	}
	if !filepath.IsAbs(source) {
		source = filepath.Join(unitDirectory, filepath.FromSlash(source))
	}
	absolute, err := filepath.Abs(filepath.Clean(source))
	if err != nil {
		return "", fmt.Errorf("resolve local terraform.source: %w", err)
	}
	return absolute, nil
}

// Terragrunt uses a double slash to separate a package root from a subdirectory.
func collapsePackageSubdir(source string) string {
	start := 0
	if strings.HasPrefix(source, "//") {
		start = 2
	}
	if index := strings.Index(source[start:], "//"); index >= 0 {
		index += start
		return filepath.Join(source[:index], source[index+2:])
	}
	return source
}
