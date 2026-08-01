// Package feedback implements the PID-driven translation loop.
package feedback

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/terra-translate/internal/ir"
	"github.com/terra-translate/internal/pid"
	"github.com/terra-translate/internal/translator"
)

// LoopResult is the final output of the feedback loop.
type LoopResult struct {
	BestResult      *translator.Result
	AccuracyHistory []float64
	Warnings        []ir.TranslationWarning
	Iterations      int
	Converged       bool
	Accuracy        float64
	MappedAttrs     int
	TotalAttrs      int
}

// Loop orchestrates iterative translation refinement using a PID controller.
type Loop struct {
	ctrl      *pid.Controller
	trans     *translator.Translator
	maxIter   int
	tolerance float64
	verbose   bool
}

// NewLoop creates a feedback loop.
func NewLoop(ctrl *pid.Controller, trans *translator.Translator, maxIter int, verbose bool) *Loop {
	return &Loop{
		ctrl:      ctrl,
		trans:     trans,
		maxIter:   maxIter,
		tolerance: 0.01,
		verbose:   verbose,
	}
}

// Run executes the PID feedback loop and returns the best result.
func (l *Loop) Run(module *ir.Module) (*LoopResult, error) {
	loopResult := &LoopResult{}
	warningSet := make(map[string]struct{})
	effort := 0.5
	var lastAccuracy float64

	for iter := 0; iter < l.maxIter; iter++ {
		result := l.trans.Translate(module, effort)
		// Use composite score as the PID process variable.
		accuracy := result.Score.Composite
		if accuracy == 0 {
			accuracy = result.Accuracy // fallback to coverage
		}
		loopResult.AccuracyHistory = append(loopResult.AccuracyHistory, accuracy)

		if l.verbose {
			fmt.Printf("  [iter %2d] composite=%.1f%%  coverage=%.1f%%  validity=%.1f%%  effort=%.3f  mapped=%d/%d\n",
				iter+1, accuracy*100,
				result.Score.CoverageRatio*100,
				result.Score.ValidityRatio*100,
				effort, result.MappedAttrs, result.TotalAttrs)
		}

		if loopResult.BestResult == nil || accuracy > loopResult.BestResult.Score.Composite {
			loopResult.BestResult = result
		}

		for _, w := range result.Warnings {
			key := w.Resource + "::" + w.Attribute
			if _, seen := warningSet[key]; !seen {
				warningSet[key] = struct{}{}
				loopResult.Warnings = append(loopResult.Warnings, w)
			}
		}

		effort = l.ctrl.Compute(accuracy)

		var missed []translator.UnmappedAttr
		for _, tr := range result.TargetResources {
			for _, attr := range tr.UnmappedAttrs {
				missed = append(missed, translator.UnmappedAttr{
					SourceType: tr.OriginalResource.OriginalType,
					SourceAttr: attr,
					Value:      tr.OriginalResource.Properties[attr],
				})
			}
		}
		l.trans.LearnFromMissed(missed, effort)

		loopResult.Iterations = iter + 1

		if accuracy >= 1.0-l.tolerance {
			loopResult.Converged = true
			break
		}
		if iter > 1 && math.Abs(accuracy-lastAccuracy) < 0.001 {
			break
		}
		lastAccuracy = accuracy
	}

	best := loopResult.BestResult
	if best == nil {
		return nil, fmt.Errorf("no translation result produced")
	}
	loopResult.Accuracy = best.Accuracy
	loopResult.MappedAttrs = best.MappedAttrs
	loopResult.TotalAttrs = best.TotalAttrs

	return loopResult, nil
}

// Report is the JSON-serialisable report.
type Report struct {
	GeneratedAt     time.Time               `json:"generated_at"`
	SourceProvider  string                  `json:"source_provider"`
	TargetProvider  string                  `json:"target_provider"`
	Iterations      int                     `json:"iterations"`
	Converged       bool                    `json:"converged"`
	FinalAccuracy   float64                 `json:"final_accuracy_pct"`
	MappedAttrs     int                     `json:"mapped_attrs"`
	TotalAttrs      int                     `json:"total_attrs"`
	AccuracyHistory []float64               `json:"accuracy_history"`
	PIDHistory      []pid.Sample            `json:"pid_history"`
	Warnings        []ir.TranslationWarning `json:"warnings,omitempty"`
}

// WriteReport serialises the result and PID history to a JSON file.
func WriteReport(path string, lr *LoopResult, ctrl *pid.Controller, src, tgt string) error {
	history := make([]float64, len(lr.AccuracyHistory))
	for i, a := range lr.AccuracyHistory {
		history[i] = math.Round(a*10000) / 100
	}
	r := Report{
		GeneratedAt:     time.Now(),
		SourceProvider:  src,
		TargetProvider:  tgt,
		Iterations:      lr.Iterations,
		Converged:       lr.Converged,
		FinalAccuracy:   math.Round(lr.Accuracy*10000) / 100,
		MappedAttrs:     lr.MappedAttrs,
		TotalAttrs:      lr.TotalAttrs,
		AccuracyHistory: history,
		PIDHistory:      ctrl.History,
		Warnings:        lr.Warnings,
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
