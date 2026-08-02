package pid

import (
	"math"
	"strings"
	"testing"
)

func TestComputeClampsOutputToUnitInterval(t *testing.T) {
	// Large gains with a large error would drive the raw output well past 1.
	controller := New(10, 5, 1, 1.0)
	output := controller.Compute(0.0)
	if output < 0 || output > 1 {
		t.Fatalf("output %v escaped [0,1]", output)
	}
	if output != 1 {
		t.Fatalf("a maximal error should saturate the output, got %v", output)
	}

	// An accuracy above the setpoint produces a negative error and must clamp low.
	settled := New(10, 5, 1, 0.5)
	if got := settled.Compute(1.0); got != 0 {
		t.Fatalf("negative error should clamp to 0, got %v", got)
	}
}

func TestDerivativeIsZeroOnTheFirstSample(t *testing.T) {
	controller := New(1, 0, 1, 1.0)
	controller.Compute(0.4)
	if got := controller.History[0].DOutput; got != 0 {
		t.Fatalf("first sample has no previous error, want D=0, got %v", got)
	}
}

func TestIntegralIsClampedAgainstWindup(t *testing.T) {
	// Sustained error must not let the integral term grow without bound.
	controller := New(0, 1, 0, 1.0)
	for i := 0; i < 200; i++ {
		controller.Compute(0.0)
	}
	if math.Abs(controller.integral) > 2.0+1e-9 {
		t.Fatalf("integral %v exceeded the anti-windup clamp", controller.integral)
	}
}

func TestIsConvergedUsesTheLatestError(t *testing.T) {
	controller := New(0.8, 0.1, 0.05, 1.0)
	if controller.IsConverged(0.01) {
		t.Fatal("a controller with no history has not converged")
	}
	controller.Compute(0.5)
	if controller.IsConverged(0.01) {
		t.Fatal("error 0.5 is not within tolerance 0.01")
	}
	controller.Compute(0.999)
	if !controller.IsConverged(0.01) {
		t.Fatal("error 0.001 is within tolerance 0.01")
	}
}

func TestAccuracyTrendReportsPerIterationChange(t *testing.T) {
	controller := New(0.8, 0.1, 0.05, 1.0)
	if got := controller.AccuracyTrend(); got != 0 {
		t.Fatalf("trend needs two samples, got %v", got)
	}
	controller.Compute(0.40)
	if got := controller.AccuracyTrend(); got != 0 {
		t.Fatalf("trend needs two samples, got %v", got)
	}
	controller.Compute(0.55)
	if got := controller.AccuracyTrend(); math.Abs(got-0.15) > 1e-9 {
		t.Fatalf("trend = %v, want 0.15", got)
	}
	controller.Compute(0.50)
	if got := controller.AccuracyTrend(); got >= 0 {
		t.Fatalf("a regression must report a negative trend, got %v", got)
	}
}

func TestResetClearsEveryAccumulator(t *testing.T) {
	controller := New(0.8, 0.1, 0.05, 1.0)
	controller.Compute(0.2)
	controller.Compute(0.3)
	controller.Reset()

	if len(controller.History) != 0 {
		t.Fatalf("history survived reset: %d samples", len(controller.History))
	}
	if controller.LastOutput() != 0 || controller.integral != 0 || controller.initialized {
		t.Fatal("controller state survived reset")
	}
	// After a reset the next sample must again behave like a first sample.
	controller.Compute(0.2)
	if controller.History[0].DOutput != 0 {
		t.Fatal("derivative was carried across a reset")
	}
}

func TestReportListsEveryIteration(t *testing.T) {
	controller := New(0.8, 0.1, 0.05, 1.0)
	controller.Compute(0.30)
	controller.Compute(0.60)
	report := controller.Report()

	for _, want := range []string{"0.300", "0.600"} {
		if !strings.Contains(report, want) {
			t.Fatalf("report is missing %q:\n%s", want, report)
		}
	}
	if lines := strings.Count(report, "\n"); lines < 2 {
		t.Fatalf("report should have one line per iteration:\n%s", report)
	}
}
