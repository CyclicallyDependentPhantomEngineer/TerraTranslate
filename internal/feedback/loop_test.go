package feedback

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/terra-translate/internal/ir"
	"github.com/terra-translate/internal/pid"
	"github.com/terra-translate/internal/translator"
)

func newModule() *ir.Module {
	return &ir.Module{
		SourceProvider: "aws",
		Variables:      map[string]*ir.Variable{},
		Outputs:        map[string]*ir.Output{},
		Locals:         map[string]interface{}{},
		Resources: []*ir.Resource{
			{
				OriginalType: "aws_s3_bucket",
				Name:         "assets",
				LogicalClass: ir.StorageBucket,
				Properties: map[string]*ir.Property{
					"bucket": {Name: "bucket", Value: "example-assets", Type: ir.TypeString},
				},
			},
		},
	}
}

func newTranslator(t *testing.T) *translator.Translator {
	t.Helper()
	mappings, err := translator.LoadMappings("aws", "google", "")
	if err != nil {
		t.Fatal(err)
	}
	return translator.New(mappings)
}

func newLoop(t *testing.T, maxIter int) *Loop {
	t.Helper()
	return NewLoop(pid.New(0.8, 0.1, 0.05, 1.0), newTranslator(t), maxIter, false)
}

func TestRunProducesAResultAndRecordsHistory(t *testing.T) {
	result, err := newLoop(t, 5).Run(newModule())
	if err != nil {
		t.Fatal(err)
	}
	if result.BestResult == nil {
		t.Fatal("the loop must return the best translation it found")
	}
	if result.Iterations < 1 {
		t.Fatalf("iterations = %d, want at least 1", result.Iterations)
	}
	if len(result.AccuracyHistory) != result.Iterations {
		t.Fatalf("history has %d entries for %d iterations",
			len(result.AccuracyHistory), result.Iterations)
	}
	if result.Accuracy < 0 || result.Accuracy > 1 {
		t.Fatalf("accuracy %v escaped [0,1]", result.Accuracy)
	}
	if result.TotalAttrs > 0 && result.MappedAttrs > result.TotalAttrs {
		t.Fatalf("mapped %d of %d attributes", result.MappedAttrs, result.TotalAttrs)
	}
}

func TestRunNeverExceedsMaxIterations(t *testing.T) {
	for _, maxIter := range []int{1, 2, 3} {
		result, err := newLoop(t, maxIter).Run(newModule())
		if err != nil {
			t.Fatal(err)
		}
		if result.Iterations > maxIter {
			t.Fatalf("ran %d iterations with -max-iter %d", result.Iterations, maxIter)
		}
	}
}

func TestRunOnAModuleWithNoResources(t *testing.T) {
	// An empty module has nothing to map; the loop must terminate cleanly
	// rather than dividing by a zero attribute count.
	empty := &ir.Module{
		Variables: map[string]*ir.Variable{},
		Outputs:   map[string]*ir.Output{},
		Locals:    map[string]interface{}{},
	}
	result, err := newLoop(t, 3).Run(empty)
	if err != nil {
		t.Fatal(err)
	}
	if result.BestResult == nil {
		t.Fatal("expected a result even for an empty module")
	}
}

func TestWriteReportEmitsReadableJSON(t *testing.T) {
	controller := pid.New(0.8, 0.1, 0.05, 1.0)
	loop := NewLoop(controller, newTranslator(t), 3, false)
	result, err := loop.Run(newModule())
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "translation_report.json")
	if err := WriteReport(path, result, controller, "aws", "google"); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var report Report
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("report is not valid JSON: %v", err)
	}
	if report.SourceProvider != "aws" || report.TargetProvider != "google" {
		t.Fatalf("providers were not recorded: %+v", report)
	}
	if len(report.PIDHistory) == 0 {
		t.Fatal("the report documents PID history and must include it")
	}
}

func TestWriteReportFailsOnAnUnwritablePath(t *testing.T) {
	controller := pid.New(0.8, 0.1, 0.05, 1.0)
	result := &LoopResult{}
	err := WriteReport(filepath.Join(t.TempDir(), "missing", "report.json"), result, controller, "aws", "google")
	if err == nil {
		t.Fatal("writing into a missing directory must return an error")
	}
}
