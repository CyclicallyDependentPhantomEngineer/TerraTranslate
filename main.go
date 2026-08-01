// terra-translate translates Terraform modules between cloud providers using an
// Intermediate Representation (IR) and a PID-controlled feedback loop to
// maximise attribute mapping accuracy.
//
// Usage:
//
//	terra-translate -from aws -to google -input ./my-aws-module -output ./gcp-output
//	terra-translate -from aws -to azurerm -input main.tf -output ./azure-output -v
//	terra-translate -from google -to aws -input ./gcp -output ./aws-out -kp 0.9 -ki 0.2
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/terra-translate/internal/codegen"
	"github.com/terra-translate/internal/feedback"
	"github.com/terra-translate/internal/parser"
	"github.com/terra-translate/internal/pid"
	"github.com/terra-translate/internal/translator"
)

func main() {
	var (
		inputPath  = flag.String("input", ".", "Input Terraform file or directory")
		outputDir  = flag.String("output", "./terra-translate-output", "Output directory")
		from       = flag.String("from", "aws", "Source cloud provider (aws, google, azurerm)")
		to         = flag.String("to", "google", "Target cloud provider (aws, google, azurerm)")
		schemaPath = flag.String("schema", "", "Optional path to 'terraform providers schema -json' output")
		kp         = flag.Float64("kp", 0.8, "PID proportional gain")
		ki         = flag.Float64("ki", 0.1, "PID integral gain")
		kd         = flag.Float64("kd", 0.05, "PID derivative gain")
		maxIter    = flag.Int("max-iter", 8, "Maximum PID feedback iterations")
		verbose    = flag.Bool("v", false, "Verbose output (show per-iteration PID data)")
		printPID   = flag.Bool("pid-report", false, "Print full PID history table after translation")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `terra-translate — Terraform module cloud translator with PID feedback

Usage:
  terra-translate [flags]

Flags:
`)
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
Examples:
  terra-translate -from aws -to google -input ./aws-infra -output ./gcp-infra
  terra-translate -from aws -to azurerm -input main.tf -output ./azure -v -pid-report
  terra-translate -from google -to aws -input ./gcp -output ./aws -kp 0.9 -ki 0.2 -kd 0.1
`)
	}
	flag.Parse()

	log.SetFlags(0)
	log.SetPrefix("[terra-translate] ")

	fmt.Printf("\n╔══════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║  terra-translate  %-8s → %-8s                    ║\n", *from, *to)
	fmt.Printf("║  PID: Kp=%-5.2f Ki=%-5.2f Kd=%-5.2f  MaxIter=%-3d         ║\n",
		*kp, *ki, *kd, *maxIter)
	fmt.Printf("╚══════════════════════════════════════════════════════════╝\n\n")

	// 1. Parse source Terraform files.
	log.Printf("Parsing source: %s", *inputPath)
	p := parser.New()
	module, err := p.ParsePath(*inputPath)
	if err != nil {
		log.Fatalf("Parse error: %v", err)
	}
	module.SourceProvider = *from
	log.Printf("Found %d resource(s), %d variable(s), %d output(s), %d data source(s)",
		len(module.Resources), len(module.Variables), len(module.Outputs), len(module.DataSources))

	if len(module.Resources) == 0 {
		log.Fatal("No resources found — nothing to translate.")
	}

	// 2. Load mapping tables.
	log.Printf("Loading mappings: %s → %s", *from, *to)
	mappings, err := translator.LoadMappings(*from, *to, *schemaPath)
	if err != nil {
		log.Fatalf("Failed to load mappings: %v", err)
	}

	// 3. PID controller + feedback loop.
	ctrl := pid.New(*kp, *ki, *kd, 1.0)
	trans := translator.New(mappings)
	loop := feedback.NewLoop(ctrl, trans, *maxIter, *verbose)

	log.Printf("Starting PID feedback loop (max %d iterations)…", *maxIter)
	loopResult, err := loop.Run(module)
	if err != nil {
		log.Fatalf("Translation failed: %v", err)
	}

	// 4. Report.
	fmt.Printf("\n┌─ Results ────────────────────────────────────────────────\n")
	fmt.Printf("│  Iterations : %d", loopResult.Iterations)
	if loopResult.Converged {
		fmt.Printf(" (converged ✓)\n")
	} else {
		fmt.Printf(" (max reached)\n")
	}
	fmt.Printf("│  Accuracy   : %.1f%% (%d / %d attributes mapped)\n",
		loopResult.Accuracy*100, loopResult.MappedAttrs, loopResult.TotalAttrs)
	fmt.Printf("│  Accuracy Δ : %s\n", sparkline(loopResult.AccuracyHistory))

	for _, ra := range loopResult.BestResult.ResourceAccuracies {
		status := "✓"
		if ra.Score < 1.0 {
			status = "~"
		}
		fmt.Printf("│    %s %-40s  %.0f%%\n", status,
			ra.OriginalType+"."+ra.Name, ra.Score*100)
	}

	if len(loopResult.Warnings) > 0 {
		fmt.Printf("│  Warnings (%d):\n", len(loopResult.Warnings))
		for _, w := range loopResult.Warnings {
			fmt.Printf("│    ⚠  [%s] %s — %s\n", w.Resource, w.Attribute, w.Message)
		}
	}
	fmt.Printf("└──────────────────────────────────────────────────────────\n\n")

	if *printPID {
		fmt.Println(ctrl.Report())
	}

	// 5. Write output.
	if err := os.MkdirAll(*outputDir, 0755); err != nil {
		log.Fatalf("Cannot create output dir %q: %v", *outputDir, err)
	}

	hclPath := filepath.Join(*outputDir, "main.tf")
	gen := codegen.New(*to)
	if err := gen.WriteHCL(hclPath, loopResult.BestResult); err != nil {
		log.Fatalf("Failed to write HCL: %v", err)
	}
	log.Printf("Translated HCL written to: %s", hclPath)

	reportPath := filepath.Join(*outputDir, "translation_report.json")
	if err := feedback.WriteReport(reportPath, loopResult, ctrl, *from, *to); err != nil {
		log.Printf("Warning: could not write report: %v", err)
	} else {
		log.Printf("PID report written to:       %s", reportPath)
	}

	if loopResult.Accuracy < 0.90 {
		fmt.Printf("\n⚠  Translation accuracy %.1f%% is below 90%%. Review unmapped attributes manually.\n",
			loopResult.Accuracy*100)
		os.Exit(2)
	}
	fmt.Println("\n✓ Translation complete.")
}

func sparkline(history []float64) string {
	blocks := []string{"▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}
	if len(history) == 0 {
		return "—"
	}
	var sb string
	for _, v := range history {
		idx := int(v * float64(len(blocks)-1))
		if idx >= len(blocks) {
			idx = len(blocks) - 1
		}
		sb += blocks[idx]
	}
	return sb
}
