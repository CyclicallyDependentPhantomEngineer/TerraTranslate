// terra-translate translates Terraform modules between cloud providers and
// provides adapters for Terraform CLI and Terragrunt repositories.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/terra-translate/internal/app"
	"github.com/terra-translate/internal/catalog"
	terraformext "github.com/terra-translate/internal/integration/terraform"
	terragruntext "github.com/terra-translate/internal/integration/terragrunt"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		// Backward compatibility with the original flag-only CLI.
		return runTerraform(args, false, stdout, stderr)
	}

	switch args[0] {
	case "terraform", "translate":
		return runTerraform(args[1:], true, stdout, stderr)
	case "terragrunt":
		return runTerragrunt(args[1:], stdout, stderr)
	case "catalog":
		return runCatalog(args[1:], stdout, stderr)
	case "help", "-help", "--help":
		printRootUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		printRootUsage(stderr)
		return 1
	}
}

func printRootUsage(w io.Writer) {
	fmt.Fprint(w, `terra-translate — cross-cloud Terraform and Terragrunt migration assistant

Usage:
  terra-translate terraform [flags]   Translate one Terraform module
  terra-translate terragrunt [flags]  Translate local modules used by Terragrunt units
  terra-translate catalog refresh     Refresh provider/module data and mappings
  terra-translate catalog remap       Regenerate mappings from stored indexes
  terra-translate catalog status      Show the current catalog snapshot
  terra-translate [flags]             Legacy alias for "terraform"

Run "terra-translate <command> -help" for command-specific flags.
`)
}

func runTerraform(args []string, extensionMode bool, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("terraform", flag.ContinueOnError)
	flags.SetOutput(stderr)
	defaultFrom := "aws"
	if extensionMode {
		defaultFrom = "auto"
	}

	inputPath := flags.String("input", ".", "Input Terraform file or module directory")
	outputDir := flags.String("output", "./terra-translate-output", "Output directory")
	from := flags.String("from", defaultFrom, "Source provider (auto, aws, google, azurerm)")
	to := flags.String("to", "google", "Target provider (aws, google, azurerm)")
	schemaPath := flags.String("schema", "", "Optional provider schema JSON path")
	catalogDir := flags.String("catalog", "", "Refreshed catalog directory used for generated mappings")
	kp := flags.Float64("kp", 0.8, "PID proportional gain")
	ki := flags.Float64("ki", 0.1, "PID integral gain")
	kd := flags.Float64("kd", 0.05, "PID derivative gain")
	maxIter := flags.Int("max-iter", 8, "Maximum feedback iterations")
	minAccuracy := flags.Float64("min-accuracy", 0.90, "Minimum mapped-attribute ratio for exit code 0")
	verbose := flags.Bool("v", false, "Show per-iteration PID data")
	printPID := flags.Bool("pid-report", false, "Print the full PID history")
	terraformBinary := flags.String("terraform-bin", "terraform", "Terraform CLI binary used for checks")
	fmtCheck := flags.Bool("fmt-check", extensionMode, "Run terraform fmt -check on generated HCL")
	validate := flags.Bool("validate", false, "Run terraform validate (output must already be initialized)")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: terra-translate terraform [flags]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected positional arguments: %s\n", strings.Join(flags.Args(), " "))
		return 1
	}

	fmt.Fprintf(stdout, "terra-translate: Terraform module %s -> %s\n", *from, *to)
	fmt.Fprintf(stdout, "Parsing source: %s\n", *inputPath)
	outcome, err := app.Translate(app.Config{
		InputPath:   *inputPath,
		OutputDir:   *outputDir,
		From:        *from,
		To:          *to,
		SchemaPath:  *schemaPath,
		CatalogDir:  *catalogDir,
		Kp:          *kp,
		Ki:          *ki,
		Kd:          *kd,
		MaxIter:     *maxIter,
		Verbose:     *verbose,
		MinAccuracy: *minAccuracy,
	})
	if err != nil {
		fmt.Fprintf(stderr, "translation failed: %v\n", err)
		return 1
	}

	if err := terraformext.VerifySyntax(outcome.HCLPath); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	checks := terraformext.RunChecks(*terraformBinary, *outputDir, outcome.HCLPath, *fmtCheck, *validate)
	for _, check := range checks {
		if check.Err != nil {
			fmt.Fprintf(stderr, "check failed: %s: %v\n", check.Name, check.Err)
			if check.Output != "" {
				fmt.Fprintln(stderr, check.Output)
			}
			return 1
		}
		fmt.Fprintf(stdout, "Check passed: %s\n", check.Name)
	}

	printTranslationResult(stdout, outcome)
	if *printPID {
		fmt.Fprintln(stdout, outcome.Controller.Report())
	}
	fmt.Fprintf(stdout, "Translated HCL: %s\n", outcome.HCLPath)
	fmt.Fprintf(stdout, "Translation report: %s\n", outcome.ReportPath)

	status := outcome.ExitCode(*minAccuracy)
	if status == 2 {
		fmt.Fprintf(stderr, "translation coverage %.1f%% is below the %.1f%% threshold; manual review is required\n",
			outcome.Loop.Accuracy*100, *minAccuracy*100)
	}
	return status
}

func runTerragrunt(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("terragrunt", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "Terragrunt repository or stack root")
	outputRoot := flags.String("output", "./terra-translate-terragrunt-output", "Stack translation output directory")
	from := flags.String("from", "auto", "Source provider (auto, aws, google, azurerm)")
	to := flags.String("to", "google", "Target provider (aws, google, azurerm)")
	schemaPath := flags.String("schema", "", "Optional provider schema JSON path")
	catalogDir := flags.String("catalog", "", "Refreshed catalog directory used for generated mappings")
	kp := flags.Float64("kp", 0.8, "PID proportional gain")
	ki := flags.Float64("ki", 0.1, "PID integral gain")
	kd := flags.Float64("kd", 0.05, "PID derivative gain")
	maxIter := flags.Int("max-iter", 8, "Maximum feedback iterations per module")
	minAccuracy := flags.Float64("min-accuracy", 0.90, "Minimum mapped-attribute ratio")
	verbose := flags.Bool("v", false, "Show per-iteration PID data")
	failOnSkipped := flags.Bool("fail-on-skipped", true, "Return exit code 2 when remote or dynamic units are skipped")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: terra-translate terragrunt [flags]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected positional arguments: %s\n", strings.Join(flags.Args(), " "))
		return 1
	}

	fmt.Fprintf(stdout, "terra-translate: Terragrunt stack %s -> %s\n", *from, *to)
	report, reportPath, err := terragruntext.TranslateStack(terragruntext.StackConfig{
		Root:          *root,
		OutputRoot:    *outputRoot,
		From:          *from,
		To:            *to,
		SchemaPath:    *schemaPath,
		CatalogDir:    *catalogDir,
		Kp:            *kp,
		Ki:            *ki,
		Kd:            *kd,
		MaxIter:       *maxIter,
		Verbose:       *verbose,
		MinAccuracy:   *minAccuracy,
		FailOnSkipped: *failOnSkipped,
	})
	if err != nil {
		fmt.Fprintf(stderr, "Terragrunt translation failed: %v\n", err)
		return 1
	}

	for _, unit := range report.Units {
		configPath, _ := filepath.Rel(report.Root, unit.ConfigPath)
		fmt.Fprintf(stdout, "  %-10s %-40s", unit.Status, configPath)
		if unit.TotalAttrs > 0 {
			fmt.Fprintf(stdout, " %.1f%% (%d/%d)", unit.AccuracyPercent, unit.MappedAttrs, unit.TotalAttrs)
		}
		if unit.Error != "" {
			fmt.Fprintf(stdout, " — %s", unit.Error)
		}
		fmt.Fprintln(stdout)
	}
	summary := report.Summary
	fmt.Fprintf(stdout, "Units: %d; unique modules: %d; translated: %d; partial: %d; skipped: %d; failed: %d\n",
		summary.Units, summary.UniqueModules, summary.TranslatedModules, summary.PartialModules, summary.SkippedUnits, summary.FailedUnits)
	fmt.Fprintf(stdout, "Stack report: %s\n", reportPath)
	return report.ExitCode(*failOnSkipped)
}

func runCatalog(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "Usage: terra-translate catalog <refresh|remap|status> [flags]")
		return 1
	}
	switch args[0] {
	case "refresh":
		return runCatalogRefresh(args[1:], stdout, stderr)
	case "remap":
		return runCatalogRemap(args[1:], stdout, stderr)
	case "status":
		return runCatalogStatus(args[1:], stdout, stderr)
	case "help", "-help", "--help":
		fmt.Fprintln(stdout, "Usage: terra-translate catalog <refresh|remap|status> [flags]")
		return 0
	default:
		fmt.Fprintf(stderr, "unknown catalog command %q\n", args[0])
		return 1
	}
}

func runCatalogRemap(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("catalog remap", flag.ContinueOnError)
	flags.SetOutput(stderr)
	catalogDir := flags.String("catalog", "./catalog", "Catalog directory")
	overridesPath := flags.String("overrides", "./catalog-overrides.json", "Manual mapping override JSON file")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	manifest, manifestPath, err := catalog.Remap(*catalogDir, *overridesPath)
	if err != nil {
		fmt.Fprintf(stderr, "catalog remap failed: %v\n", err)
		return 1
	}
	printCatalogSummary(stdout, manifest)
	fmt.Fprintf(stdout, "Manifest: %s\n", manifestPath)
	return 0
}

func runCatalogRefresh(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("catalog refresh", flag.ContinueOnError)
	flags.SetOutput(stderr)
	outputDir := flags.String("output", "./catalog", "Versioned catalog directory")
	terraformBinary := flags.String("terraform-bin", "terraform", "Terraform CLI binary")
	registryURL := flags.String("registry-url", "https://registry.terraform.io", "Terraform Registry base URL")
	awsVersion := flags.String("aws-version", "latest", "AWS provider version or latest")
	googleVersion := flags.String("google-version", "latest", "Google provider version or latest")
	azurermVersion := flags.String("azurerm-version", "latest", "AzureRM provider version or latest")
	refreshModules := flags.Bool("modules", true, "Fetch paginated Registry module metadata")
	moduleLimit := flags.Int("module-limit", 0, "Maximum modules per provider; 0 fetches all")
	moduleDetails := flags.Bool("module-details", false, "Fetch detailed inputs/outputs/resources for each selected module")
	detailWorkers := flags.Int("detail-workers", 6, "Concurrent Registry module-detail requests")
	detailRPS := flags.Int("detail-rps", 10, "Maximum Registry module-detail requests started per second")
	requestTimeout := flags.Duration("request-timeout", 45*time.Second, "Timeout for each Registry request")
	commandTimeout := flags.Duration("command-timeout", 20*time.Minute, "Timeout for Terraform init and schema extraction")
	overridesPath := flags.String("overrides", "./catalog-overrides.json", "Manual mapping override JSON file")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: terra-translate catalog refresh [flags]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if *moduleLimit < 0 {
		fmt.Fprintln(stderr, "module-limit cannot be negative")
		return 1
	}
	if *detailRPS <= 0 {
		fmt.Fprintln(stderr, "detail-rps must be positive")
		return 1
	}

	manifest, manifestPath, err := catalog.Refresh(catalog.Config{
		OutputDir:       *outputDir,
		TerraformBinary: *terraformBinary,
		RegistryBaseURL: *registryURL,
		Providers: []catalog.ProviderSpec{
			{Name: "aws", Source: "hashicorp/aws", Version: *awsVersion},
			{Name: "google", Source: "hashicorp/google", Version: *googleVersion},
			{Name: "azurerm", Source: "hashicorp/azurerm", Version: *azurermVersion},
		},
		RefreshModules: *refreshModules,
		ModuleLimit:    *moduleLimit,
		ModuleDetails:  *moduleDetails,
		DetailWorkers:  *detailWorkers,
		DetailRPS:      *detailRPS,
		RequestTimeout: *requestTimeout,
		CommandTimeout: *commandTimeout,
		OverridesPath:  *overridesPath,
		Progress: func(format string, values ...interface{}) {
			fmt.Fprintf(stdout, format+"\n", values...)
		},
	})
	if err != nil {
		fmt.Fprintf(stderr, "catalog refresh failed: %v\n", err)
		return 1
	}
	printCatalogSummary(stdout, manifest)
	fmt.Fprintf(stdout, "Manifest: %s\n", manifestPath)
	return 0
}

func runCatalogStatus(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("catalog status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	catalogDir := flags.String("catalog", "./catalog", "Catalog directory")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	manifest, err := catalog.LoadLatest(*catalogDir)
	if err != nil {
		fmt.Fprintf(stderr, "catalog status failed: %v\n", err)
		return 1
	}
	printCatalogSummary(stdout, manifest)
	return 0
}

func printCatalogSummary(w io.Writer, manifest *catalog.Manifest) {
	fmt.Fprintf(w, "Catalog snapshot: %s (%s)\n", manifest.SnapshotID, manifest.RefreshedAt.Format(time.RFC3339))
	fmt.Fprintf(w, "Terraform: %s\n", manifest.TerraformVersion)
	providerNames := make([]string, 0, len(manifest.Providers))
	for name := range manifest.Providers {
		providerNames = append(providerNames, name)
	}
	slices.Sort(providerNames)
	for _, name := range providerNames {
		provider := manifest.Providers[name]
		modules := manifest.Modules[name]
		fmt.Fprintf(w, "  %-8s v%-12s resources=%d data_sources=%d ephemeral=%d functions=%d modules=%d detailed=%d\n",
			name, provider.Version, provider.Resources, provider.DataSources,
			provider.EphemeralResources, provider.Functions, modules.Modules, modules.Detailed)
	}
	fmt.Fprintf(w, "Generated mappings: %d provider pairs\n", len(manifest.Mappings))
}

func printTranslationResult(w io.Writer, outcome *app.Outcome) {
	result := outcome.Loop
	convergence := "stalled/max iterations"
	if result.Converged {
		convergence = "converged"
	}
	fmt.Fprintf(w, "Result: %.1f%% coverage (%d/%d attributes), %d iterations, %s\n",
		result.Accuracy*100, result.MappedAttrs, result.TotalAttrs, result.Iterations, convergence)
	fmt.Fprintf(w, "Accuracy trend: %s\n", sparkline(result.AccuracyHistory))
	if len(result.Warnings) > 0 {
		fmt.Fprintf(w, "Warnings: %d\n", len(result.Warnings))
	}
}

func sparkline(history []float64) string {
	blocks := []string{"▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}
	if len(history) == 0 {
		return "—"
	}
	var builder strings.Builder
	for _, value := range history {
		index := int(value * float64(len(blocks)-1))
		if index < 0 {
			index = 0
		}
		if index >= len(blocks) {
			index = len(blocks) - 1
		}
		builder.WriteString(blocks[index])
	}
	return builder.String()
}
