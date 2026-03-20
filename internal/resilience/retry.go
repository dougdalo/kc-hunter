// Package resilience provides retry and backoff utilities for degraded environments.
package resilience

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// RetryOpts configures retry behavior.
type RetryOpts struct {
	MaxAttempts int           // total attempts (1 = no retry)
	BaseDelay   time.Duration // delay before first retry; doubles each attempt
	StepName    string        // for structured warning messages
}

// DefaultRetryOpts returns production-reasonable defaults:
// 3 attempts, 1s base delay.
func DefaultRetryOpts(step string) RetryOpts {
	return RetryOpts{
		MaxAttempts: 3,
		BaseDelay:   1 * time.Second,
		StepName:    step,
	}
}

// RetryResult captures what happened during retried execution.
type RetryResult struct {
	Attempts int      // how many attempts were made
	Warnings []string // per-attempt error messages (empty on first-attempt success)
}

// Do retries fn with exponential backoff until it succeeds, context expires,
// or MaxAttempts is exhausted. Returns the last error on exhaustion.
func Do(ctx context.Context, opts RetryOpts, fn func(ctx context.Context) error) (RetryResult, error) {
	if opts.MaxAttempts < 1 {
		opts.MaxAttempts = 1
	}

	var result RetryResult
	delay := opts.BaseDelay

	for attempt := 1; attempt <= opts.MaxAttempts; attempt++ {
		result.Attempts = attempt

		err := fn(ctx)
		if err == nil {
			return result, nil
		}

		// Record warning for this attempt.
		msg := fmt.Sprintf("%s: attempt %d/%d failed: %v", opts.StepName, attempt, opts.MaxAttempts, err)
		result.Warnings = append(result.Warnings, msg)

		// Don't sleep after the last attempt.
		if attempt == opts.MaxAttempts {
			return result, err
		}

		// Respect context cancellation during backoff.
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(delay):
		}

		delay *= 2 // exponential backoff
	}

	// Unreachable, but be explicit.
	return result, fmt.Errorf("%s: exhausted %d attempts", opts.StepName, opts.MaxAttempts)
}

// StepTimeout returns a sub-context with a fraction of the parent's remaining deadline.
// If the parent has no deadline, falls back to the given default.
// This prevents a single slow step from consuming the entire timeout budget.
func StepTimeout(parent context.Context, fraction float64, fallback time.Duration) (context.Context, context.CancelFunc) {
	deadline, ok := parent.Deadline()
	if !ok {
		return context.WithTimeout(parent, fallback)
	}

	remaining := time.Until(deadline)
	stepDuration := time.Duration(float64(remaining) * fraction)
	stepDuration = max(stepDuration, 5*time.Second)
	stepDuration = min(stepDuration, remaining)

	return context.WithTimeout(parent, stepDuration)
}

// FormatWarnings renders a list of warnings as a compact block.
func FormatWarnings(warnings []string) string {
	if len(warnings) == 0 {
		return ""
	}
	return strings.Join(warnings, "\n")
}
