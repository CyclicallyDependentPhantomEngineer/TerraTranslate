// Package app orchestrates the provider-neutral translation pipeline.
package app

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/terra-translate/internal/codegen"
	"github.com/terra-translate/internal/feedback"
	"github.com/terra-translate/internal/ir"
	"github.com/terra-translate/internal/parser"
	"github.com/terra-translate/internal/pid"
	"github.com/terra-translate/internal/translator"
)

// Config describes one Terraform module translation.
type Config struct {
	InputPath   string
	OutputDir   string
	From        string
	To          string
	SchemaPath  string
	CatalogDir  string
	Kp          float64
	Ki          float64
	Kd          float64
	MaxIter     int
	Verbose     bool
	MinAccuracy float64
}

// Outcome contains the generated artifacts and feedback-loop state.
type Outcome struct {
	From       string
	To         string
	HCLPath    string
	ReportPath string
	Loop       *feedback.LoopResult
	Controller *pid.Controller
}

// ExitCode returns the documented process status for this outcome.
func (o *Outcome) ExitCode(minAccuracy float64) int {
	if o.Loop.Accuracy < minAccuracy {
		return 2
	}
	return 0
}

// Translate runs parse -> classify -> translate/PID -> codegen for one module.
func Translate(cfg Config) (*Outcome, error) {
	setDefaults(&cfg)
	if cfg.MinAccuracy < 0 || cfg.MinAccuracy > 1 {
		return nil, fmt.Errorf("minimum accuracy must be between 0 and 1")
	}

	p := parser.New()
	module, err := p.ParsePath(cfg.InputPath)
	if err != nil {
		return nil, fmt.Errorf("parse source: %w", err)
	}
	if len(module.Resources) == 0 {
		return nil, fmt.Errorf("no resources found in %q", cfg.InputPath)
	}

	from := normaliseProvider(cfg.From)
	if from == "auto" {
		from, err = InferProvider(module)
		if err != nil {
			return nil, err
		}
	}
	to := normaliseProvider(cfg.To)
	if from == to {
		return nil, fmt.Errorf("source and target provider are both %q", from)
	}
	module.SourceProvider = from

	mappings, err := translator.LoadMappingsWithCatalog(from, to, cfg.SchemaPath, cfg.CatalogDir)
	if err != nil {
		return nil, fmt.Errorf("load mappings: %w", err)
	}
	if len(mappings.Resources) == 0 {
		return nil, fmt.Errorf("provider pair %s -> %s is not supported", from, to)
	}

	ctrl := pid.New(cfg.Kp, cfg.Ki, cfg.Kd, 1.0)
	trans := translator.New(mappings)
	translationLoop := feedback.NewLoop(ctrl, trans, cfg.MaxIter, cfg.Verbose)
	loopResult, err := translationLoop.Run(module)
	if err != nil {
		return nil, fmt.Errorf("translation loop: %w", err)
	}

	if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
		return nil, fmt.Errorf("create output directory %q: %w", cfg.OutputDir, err)
	}

	hclPath := filepath.Join(cfg.OutputDir, "main.tf")
	gen := codegen.New(to)
	if err := gen.WriteHCL(hclPath, loopResult.BestResult); err != nil {
		return nil, fmt.Errorf("write translated HCL: %w", err)
	}

	reportPath := filepath.Join(cfg.OutputDir, "translation_report.json")
	if err := feedback.WriteReport(reportPath, loopResult, ctrl, from, to); err != nil {
		return nil, fmt.Errorf("write translation report: %w", err)
	}

	return &Outcome{
		From:       from,
		To:         to,
		HCLPath:    hclPath,
		ReportPath: reportPath,
		Loop:       loopResult,
		Controller: ctrl,
	}, nil
}

func setDefaults(cfg *Config) {
	if cfg.InputPath == "" {
		cfg.InputPath = "."
	}
	if cfg.OutputDir == "" {
		cfg.OutputDir = "./terra-translate-output"
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

func normaliseProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "gcp":
		return "google"
	case "azure":
		return "azurerm"
	default:
		return strings.ToLower(strings.TrimSpace(provider))
	}
}

// InferProvider identifies a module's dominant provider from resource types.
func InferProvider(module *ir.Module) (string, error) {
	counts := map[string]int{}
	for _, resource := range module.Resources {
		provider := providerForResource(resource.OriginalType)
		if provider != "" {
			counts[provider]++
		}
	}
	if len(counts) == 0 {
		return "", fmt.Errorf("cannot infer source provider; pass -from explicitly")
	}

	type providerCount struct {
		name  string
		count int
	}
	ordered := make([]providerCount, 0, len(counts))
	for name, count := range counts {
		ordered = append(ordered, providerCount{name: name, count: count})
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].count == ordered[j].count {
			return ordered[i].name < ordered[j].name
		}
		return ordered[i].count > ordered[j].count
	})
	if len(ordered) > 1 && ordered[0].count == ordered[1].count {
		return "", fmt.Errorf("cannot infer a dominant source provider; pass -from explicitly")
	}
	return ordered[0].name, nil
}

func providerForResource(resourceType string) string {
	switch {
	case strings.HasPrefix(resourceType, "aws_"):
		return "aws"
	case strings.HasPrefix(resourceType, "google_"):
		return "google"
	case strings.HasPrefix(resourceType, "azurerm_"):
		return "azurerm"
	default:
		return ""
	}
}
