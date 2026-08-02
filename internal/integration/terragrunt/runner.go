package terragrunt

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/terra-translate/internal/app"
	"github.com/terra-translate/internal/ir"
)

// StackConfig controls a Terragrunt repository translation.
type StackConfig struct {
	Root          string
	OutputRoot    string
	From          string
	To            string
	SchemaPath    string
	CatalogDir    string
	Kp            float64
	Ki            float64
	Kd            float64
	MaxIter       int
	Verbose       bool
	MinAccuracy   float64
	FailOnSkipped bool
}

// UnitResult records how one Terragrunt unit was handled.
type UnitResult struct {
	Unit
	Status          string                  `json:"status"`
	OutputPath      string                  `json:"output_path,omitempty"`
	AccuracyPercent float64                 `json:"accuracy_percent,omitempty"`
	MappedAttrs     int                     `json:"mapped_attrs,omitempty"`
	TotalAttrs      int                     `json:"total_attrs,omitempty"`
	Warnings        []ir.TranslationWarning `json:"warnings,omitempty"`
	Error           string                  `json:"error,omitempty"`
}

// SourceMapping describes how a local Terragrunt source maps to generated code.
type SourceMapping struct {
	Source           string `json:"source"`
	ResolvedSource   string `json:"resolved_source"`
	TranslatedSource string `json:"translated_source"`
}

// StackSummary contains aggregate migration counts.
type StackSummary struct {
	Units             int `json:"units"`
	UniqueModules     int `json:"unique_modules"`
	TranslatedModules int `json:"translated_modules"`
	PartialModules    int `json:"partial_modules"`
	SkippedUnits      int `json:"skipped_units"`
	FailedUnits       int `json:"failed_units"`
}

// StackReport is written after every stack run, including partial runs.
type StackReport struct {
	GeneratedAt    time.Time       `json:"generated_at"`
	Root           string          `json:"root"`
	OutputRoot     string          `json:"output_root"`
	SourceProvider string          `json:"source_provider"`
	TargetProvider string          `json:"target_provider"`
	Summary        StackSummary    `json:"summary"`
	SourceMappings []SourceMapping `json:"source_mappings,omitempty"`
	Units          []UnitResult    `json:"units"`
}

type cachedModule struct {
	status   string
	output   string
	accuracy float64
	mapped   int
	total    int
	warnings []ir.TranslationWarning
	err      string
}

// ExitCode follows the CLI contract: fatal module errors are 1, partial or
// skipped migrations are 2, and a complete stack translation is 0.
func (r *StackReport) ExitCode(failOnSkipped bool) int {
	if r.Summary.FailedUnits > 0 {
		return 1
	}
	if r.Summary.PartialModules > 0 || (failOnSkipped && r.Summary.SkippedUnits > 0) {
		return 2
	}
	return 0
}

// TranslateStack discovers and translates every unique local Terraform module.
func TranslateStack(cfg StackConfig) (*StackReport, string, error) {
	setStackDefaults(&cfg)
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return nil, "", fmt.Errorf("resolve root: %w", err)
	}
	outputRoot, err := filepath.Abs(cfg.OutputRoot)
	if err != nil {
		return nil, "", fmt.Errorf("resolve output root: %w", err)
	}
	if sameOrChildPath(root, outputRoot) && root == outputRoot {
		return nil, "", fmt.Errorf("output root must differ from the Terragrunt root")
	}
	if err := os.MkdirAll(outputRoot, 0o755); err != nil {
		return nil, "", fmt.Errorf("create stack output: %w", err)
	}

	units, err := DiscoverUnits(root, outputRoot)
	if err != nil {
		return nil, "", err
	}
	if len(units) == 0 {
		return nil, "", fmt.Errorf("no terragrunt.hcl files found under %q", root)
	}
	report := &StackReport{
		GeneratedAt:    time.Now(),
		Root:           root,
		OutputRoot:     outputRoot,
		SourceProvider: cfg.From,
		TargetProvider: cfg.To,
		Summary:        StackSummary{Units: len(units)},
	}

	modules := make(map[string]cachedModule)
	for _, unit := range units {
		unitResult := UnitResult{Unit: unit}
		switch {
		case unit.Unresolved:
			unitResult.Status = "skipped"
			unitResult.Error = unit.ResolutionError
			report.Summary.SkippedUnits++
		case unit.Remote:
			unitResult.Status = "skipped"
			unitResult.Error = "remote module source is not downloaded automatically; point -root at a materialized Terragrunt cache or a local source checkout"
			report.Summary.SkippedUnits++
		default:
			modulePath := filepath.Clean(unit.ModulePath)
			cached, exists := modules[modulePath]
			if !exists {
				report.Summary.UniqueModules++
				moduleOutput := moduleOutputPath(root, outputRoot, modulePath)
				outcome, translationErr := app.Translate(app.Config{
					InputPath:   modulePath,
					OutputDir:   moduleOutput,
					From:        cfg.From,
					To:          cfg.To,
					SchemaPath:  cfg.SchemaPath,
					CatalogDir:  cfg.CatalogDir,
					Kp:          cfg.Kp,
					Ki:          cfg.Ki,
					Kd:          cfg.Kd,
					MaxIter:     cfg.MaxIter,
					Verbose:     cfg.Verbose,
					MinAccuracy: cfg.MinAccuracy,
				})
				if translationErr != nil {
					cached = cachedModule{status: "failed", output: moduleOutput, err: translationErr.Error()}
					report.Summary.FailedUnits++
				} else {
					status := "translated"
					if outcome.ExitCode(cfg.MinAccuracy) == 2 {
						status = "partial"
						report.Summary.PartialModules++
					} else {
						report.Summary.TranslatedModules++
					}
					cached = cachedModule{
						status:   status,
						output:   moduleOutput,
						accuracy: outcome.Loop.Accuracy * 100,
						mapped:   outcome.Loop.MappedAttrs,
						total:    outcome.Loop.TotalAttrs,
						warnings: outcome.Loop.Warnings,
					}
					report.SourceMappings = append(report.SourceMappings, SourceMapping{
						Source:           unit.Source,
						ResolvedSource:   modulePath,
						TranslatedSource: moduleOutput,
					})
				}
				modules[modulePath] = cached
			} else if cached.status == "failed" {
				report.Summary.FailedUnits++
			}

			unitResult.Status = cached.status
			if exists && (cached.status == "translated" || cached.status == "partial") {
				unitResult.Status = "reused"
			}
			unitResult.OutputPath = cached.output
			unitResult.AccuracyPercent = cached.accuracy
			unitResult.MappedAttrs = cached.mapped
			unitResult.TotalAttrs = cached.total
			unitResult.Warnings = cached.warnings
			unitResult.Error = cached.err
		}
		report.Units = append(report.Units, unitResult)
	}

	reportPath := filepath.Join(outputRoot, "terragrunt_translation_report.json")
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, "", fmt.Errorf("encode stack report: %w", err)
	}
	if err := os.WriteFile(reportPath, data, 0o644); err != nil {
		return nil, "", fmt.Errorf("write stack report: %w", err)
	}
	return report, reportPath, nil
}

func setStackDefaults(cfg *StackConfig) {
	if cfg.Root == "" {
		cfg.Root = "."
	}
	if cfg.OutputRoot == "" {
		cfg.OutputRoot = "./terra-translate-terragrunt-output"
	}
	if cfg.From == "" {
		cfg.From = "auto"
	}
	if cfg.To == "" {
		cfg.To = "google"
	}
	if cfg.MaxIter <= 0 {
		cfg.MaxIter = 8
	}
}

func moduleOutputPath(root, outputRoot, modulePath string) string {
	relative, err := filepath.Rel(root, modulePath)
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		if relative == "." {
			relative = "root"
		}
		return filepath.Join(outputRoot, relative)
	}
	hash := sha256.Sum256([]byte(modulePath))
	suffix := hex.EncodeToString(hash[:4])
	return filepath.Join(outputRoot, "external", filepath.Base(modulePath)+"-"+suffix)
}
