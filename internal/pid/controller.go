// Package pid implements the discrete PID controller used to drive translation
// accuracy toward the setpoint (1.0 = 100% attribute coverage).
//
// In the translation context:
//
//	Error     = setpoint − accuracy        (unmapped fraction)
//	P output  = react to current unmapped ratio
//	I output  = accumulate residual error  (builds fallback mapping table)
//	D output  = dampen when accuracy is improving fast (avoid overshooting)
//	Output    = "effort" in [0,1] sent to the translator each iteration
package pid

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Controller is a discrete PID controller with anti-windup clamping.
type Controller struct {
	Kp       float64 // Proportional gain
	Ki       float64 // Integral gain
	Kd       float64 // Derivative gain
	Setpoint float64 // Desired accuracy (typically 1.0)

	integral    float64
	prevError   float64
	prevTime    time.Time
	initialized bool
	lastOutput  float64

	// Anti-windup: clamp the integral term to this range.
	integralMin float64
	integralMax float64

	// History for diagnostics and reporting.
	History []Sample
}

// Sample is a single PID computation record.
type Sample struct {
	Iteration int       `json:"iteration"`
	Time      time.Time `json:"time"`
	Accuracy  float64   `json:"accuracy"`
	Error     float64   `json:"error"`
	POutput   float64   `json:"p_output"`
	IOutput   float64   `json:"i_output"`
	DOutput   float64   `json:"d_output"`
	Output    float64   `json:"output"`
}

// New creates a PID controller with sensible anti-windup bounds.
func New(kp, ki, kd, setpoint float64) *Controller {
	return &Controller{
		Kp:          kp,
		Ki:          ki,
		Kd:          kd,
		Setpoint:    setpoint,
		integralMin: -2.0,
		integralMax: 2.0,
	}
}

// Compute takes the current accuracy measurement [0,1] and returns the effort
// value [0,1] the translator should apply on the next iteration.
func (c *Controller) Compute(accuracy float64) float64 {
	now := time.Now()
	err := c.Setpoint - accuracy

	dt := 1.0
	if c.initialized {
		dt = now.Sub(c.prevTime).Seconds()
		if dt < 1e-6 {
			dt = 1e-6
		}
	}

	// Proportional
	p := c.Kp * err

	// Integral with anti-windup clamping
	c.integral += err * dt
	c.integral = math.Max(c.integralMin, math.Min(c.integralMax, c.integral))
	i := c.Ki * c.integral

	// Derivative (zero on first call)
	d := 0.0
	if c.initialized {
		d = c.Kd * (err - c.prevError) / dt
	}

	// Total output, clamped to [0, 1]
	raw := p + i + d
	output := math.Max(0.0, math.Min(1.0, raw))

	c.History = append(c.History, Sample{
		Iteration: len(c.History) + 1,
		Time:      now,
		Accuracy:  accuracy,
		Error:     err,
		POutput:   p,
		IOutput:   i,
		DOutput:   d,
		Output:    output,
	})

	c.prevError = err
	c.prevTime = now
	c.initialized = true
	c.lastOutput = output
	return output
}

// LastOutput returns the most recent output without recomputing.
func (c *Controller) LastOutput() float64 {
	return c.lastOutput
}

// IsConverged reports whether the error has fallen below the given tolerance.
func (c *Controller) IsConverged(tolerance float64) bool {
	if len(c.History) == 0 {
		return false
	}
	return math.Abs(c.History[len(c.History)-1].Error) <= tolerance
}

// AccuracyTrend returns the per-iteration change in accuracy (positive = improving).
func (c *Controller) AccuracyTrend() float64 {
	n := len(c.History)
	if n < 2 {
		return 0
	}
	return c.History[n-1].Accuracy - c.History[n-2].Accuracy
}

// Reset clears all state so the controller can be reused.
func (c *Controller) Reset() {
	c.integral = 0
	c.prevError = 0
	c.initialized = false
	c.lastOutput = 0
	c.History = nil
}

// Report returns a human-readable string of the full PID history.
func (c *Controller) Report() string {
	if len(c.History) == 0 {
		return "no PID history recorded"
	}
	var sb strings.Builder
	sb.WriteString("PID Feedback History\n")
	sb.WriteString(fmt.Sprintf("%-6s %-10s %-10s %-10s %-10s %-10s %-10s\n",
		"Iter", "Accuracy", "Error", "P", "I", "D", "Output"))
	sb.WriteString(strings.Repeat("─", 68) + "\n")
	for _, s := range c.History {
		sb.WriteString(fmt.Sprintf("%-6d %-10.4f %-10.4f %-10.4f %-10.4f %-10.4f %-10.4f\n",
			s.Iteration, s.Accuracy, s.Error, s.POutput, s.IOutput, s.DOutput, s.Output))
	}
	return sb.String()
}
