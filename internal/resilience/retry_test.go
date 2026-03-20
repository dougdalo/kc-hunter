package resilience

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDo_SuccessFirstAttempt(t *testing.T) {
	calls := 0
	result, err := Do(context.Background(), RetryOpts{
		MaxAttempts: 3,
		BaseDelay:   1 * time.Millisecond,
		StepName:    "test",
	}, func(ctx context.Context) error {
		calls++
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Errorf("calls=%d, want 1", calls)
	}
	if result.Attempts != 1 {
		t.Errorf("attempts=%d, want 1", result.Attempts)
	}
	if len(result.Warnings) != 0 {
		t.Errorf("warnings=%d, want 0", len(result.Warnings))
	}
}

func TestDo_SuccessAfterRetry(t *testing.T) {
	calls := 0
	result, err := Do(context.Background(), RetryOpts{
		MaxAttempts: 3,
		BaseDelay:   1 * time.Millisecond,
		StepName:    "test",
	}, func(ctx context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("transient")
		}
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 3 {
		t.Errorf("calls=%d, want 3", calls)
	}
	if result.Attempts != 3 {
		t.Errorf("attempts=%d, want 3", result.Attempts)
	}
	if len(result.Warnings) != 2 {
		t.Errorf("warnings=%d, want 2 (one per failed attempt)", len(result.Warnings))
	}
}

func TestDo_ExhaustsAttempts(t *testing.T) {
	calls := 0
	result, err := Do(context.Background(), RetryOpts{
		MaxAttempts: 2,
		BaseDelay:   1 * time.Millisecond,
		StepName:    "connect-rest",
	}, func(ctx context.Context) error {
		calls++
		return errors.New("permanent")
	})

	if err == nil {
		t.Fatal("expected error after exhausting attempts")
	}
	if calls != 2 {
		t.Errorf("calls=%d, want 2", calls)
	}
	if result.Attempts != 2 {
		t.Errorf("attempts=%d, want 2", result.Attempts)
	}
	if len(result.Warnings) != 2 {
		t.Errorf("warnings=%d, want 2", len(result.Warnings))
	}
}

func TestDo_RespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0

	_, err := Do(ctx, RetryOpts{
		MaxAttempts: 5,
		BaseDelay:   100 * time.Millisecond,
		StepName:    "test",
	}, func(ctx context.Context) error {
		calls++
		if calls == 1 {
			cancel() // cancel during backoff
		}
		return errors.New("fail")
	})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("err=%v, want context.Canceled", err)
	}
	if calls > 2 {
		t.Errorf("calls=%d, should stop early due to cancellation", calls)
	}
}

func TestDo_SingleAttempt(t *testing.T) {
	result, err := Do(context.Background(), RetryOpts{
		MaxAttempts: 1,
		BaseDelay:   1 * time.Millisecond,
		StepName:    "test",
	}, func(ctx context.Context) error {
		return errors.New("fail")
	})

	if err == nil {
		t.Fatal("expected error")
	}
	if result.Attempts != 1 {
		t.Errorf("attempts=%d, want 1", result.Attempts)
	}
}

func TestDo_ZeroAttemptsDefaultsToOne(t *testing.T) {
	calls := 0
	_, err := Do(context.Background(), RetryOpts{
		MaxAttempts: 0,
		StepName:    "test",
	}, func(ctx context.Context) error {
		calls++
		return errors.New("fail")
	})

	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("calls=%d, want 1", calls)
	}
}

func TestDo_ExponentialBackoff(t *testing.T) {
	var timestamps []time.Time

	_, _ = Do(context.Background(), RetryOpts{
		MaxAttempts: 3,
		BaseDelay:   50 * time.Millisecond,
		StepName:    "test",
	}, func(ctx context.Context) error {
		timestamps = append(timestamps, time.Now())
		return errors.New("fail")
	})

	if len(timestamps) != 3 {
		t.Fatalf("timestamps=%d, want 3", len(timestamps))
	}

	// First retry delay should be ~50ms, second ~100ms.
	d1 := timestamps[1].Sub(timestamps[0])
	d2 := timestamps[2].Sub(timestamps[1])

	if d1 < 40*time.Millisecond {
		t.Errorf("first delay=%v, want >= 40ms", d1)
	}
	if d2 < 80*time.Millisecond {
		t.Errorf("second delay=%v, want >= 80ms (2x first)", d2)
	}
}

func TestStepTimeout_WithDeadline(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	child, childCancel := StepTimeout(parent, 0.3, 5*time.Second)
	defer childCancel()

	deadline, ok := child.Deadline()
	if !ok {
		t.Fatal("child should have deadline")
	}

	remaining := time.Until(deadline)
	// 30% of 10s = 3s, but minimum is 5s. Since 3s < 5s, should be clamped to 5s.
	// But wait — the minimum is 5s only if fraction result < 5s.
	// 0.3 * 10s = 3s < 5s, so we clamp to 5s.
	if remaining < 4500*time.Millisecond || remaining > 5500*time.Millisecond {
		t.Errorf("remaining=%v, want ~5s (clamped from 3s)", remaining)
	}
}

func TestStepTimeout_WithoutDeadline(t *testing.T) {
	child, cancel := StepTimeout(context.Background(), 0.5, 7*time.Second)
	defer cancel()

	deadline, ok := child.Deadline()
	if !ok {
		t.Fatal("child should have deadline (from fallback)")
	}

	remaining := time.Until(deadline)
	if remaining < 6*time.Second || remaining > 8*time.Second {
		t.Errorf("remaining=%v, want ~7s", remaining)
	}
}

func TestDefaultRetryOpts(t *testing.T) {
	opts := DefaultRetryOpts("test-step")
	if opts.MaxAttempts != 3 {
		t.Errorf("maxAttempts=%d, want 3", opts.MaxAttempts)
	}
	if opts.BaseDelay != 1*time.Second {
		t.Errorf("baseDelay=%v, want 1s", opts.BaseDelay)
	}
	if opts.StepName != "test-step" {
		t.Errorf("stepName=%q, want test-step", opts.StepName)
	}
}
