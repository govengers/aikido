package retry

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

var errTransient = errors.New("transient")

func retryAll(error) bool { return true }

func fastPolicy(maximumAttempts int) Policy {
	return Policy{
		MaximumAttempts: maximumAttempts,
		BaseDelay:       time.Millisecond,
		MaximumDelay:    5 * time.Millisecond,
		Multiplier:      2,
		ShouldRetry:     retryAll,
	}
}

func TestDo_SucceedsImmediately(t *testing.T) {
	t.Parallel()
	calls := 0
	err := Do(t.Context(), DefaultPolicy(), func(int) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestDo_RetriesUntilSuccess(t *testing.T) {
	t.Parallel()
	var attempts []int
	err := Do(t.Context(), fastPolicy(3), func(attempt int) error {
		attempts = append(attempts, attempt)
		if attempt < 3 {
			return errTransient
		}
		return nil
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if fmt.Sprint(attempts) != "[1 2 3]" {
		t.Errorf("attempts = %v, want [1 2 3]", attempts)
	}
}

func TestDo_StopsOnNonRetryableError(t *testing.T) {
	t.Parallel()
	permanent := errors.New("permanent")
	policy := fastPolicy(5)
	policy.ShouldRetry = func(error) bool { return false }
	calls := 0
	err := Do(t.Context(), policy, func(int) error {
		calls++
		return permanent
	})
	if !errors.Is(err, permanent) {
		t.Fatalf("err = %v, want the permanent error", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry on non-retryable)", calls)
	}
}

func TestDo_StopsAfterMaximumAttempts(t *testing.T) {
	t.Parallel()
	calls := 0
	err := Do(t.Context(), fastPolicy(3), func(int) error {
		calls++
		return errTransient
	})
	if !errors.Is(err, errTransient) {
		t.Fatalf("err = %v, want the transient error", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3 (MaximumAttempts)", calls)
	}
}

func TestDo_ZeroPolicyCallsOnce(t *testing.T) {
	t.Parallel()
	calls := 0
	err := Do(t.Context(), Policy{}, func(int) error {
		calls++
		return errTransient
	})
	if !errors.Is(err, errTransient) || calls != 1 {
		t.Errorf("err = %v, calls = %d; want the transient error after 1 call", err, calls)
	}
}

func TestDo_NilShouldRetryRetriesEveryError(t *testing.T) {
	t.Parallel()
	policy := fastPolicy(2)
	policy.ShouldRetry = nil
	calls := 0
	_ = Do(t.Context(), policy, func(int) error { // the error is the transient one, calls is what is asserted
		calls++
		return errTransient
	})
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

func TestDo_NeverRetriesContextErrors(t *testing.T) {
	t.Parallel()
	for _, contextError := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(contextError.Error(), func(t *testing.T) {
			t.Parallel()
			calls := 0
			err := Do(t.Context(), fastPolicy(3), func(int) error {
				calls++
				return fmt.Errorf("call provider: %w", contextError)
			})
			if !errors.Is(err, contextError) || calls != 1 {
				t.Errorf("err = %v, calls = %d; want %v after 1 call", err, calls, contextError)
			}
		})
	}
}

func TestDo_HonorsRetryAfter(t *testing.T) {
	t.Parallel()
	policy := Policy{
		MaximumAttempts: 3,
		BaseDelay:       time.Hour, // the test would time out without the Retry-After override
		MaximumDelay:    time.Hour,
		ShouldRetry:     retryAll,
	}
	calls := 0
	err := Do(t.Context(), policy, func(int) error {
		calls++
		if calls < 2 {
			return &AfterError{Cause: errors.New("rate limited"), After: time.Millisecond}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

func TestDo_ReturnsContextErrorWhenCancelledBeforeFirstAttempt(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	calls := 0
	err := Do(ctx, fastPolicy(3), func(int) error {
		calls++
		return nil
	})
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Errorf("err = %v, calls = %d; want context.Canceled before any call", err, calls)
	}
}

func TestDo_ReturnsContextErrorWhenCancelledDuringWait(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	policy := Policy{MaximumAttempts: 3, BaseDelay: time.Hour, MaximumDelay: time.Hour, ShouldRetry: retryAll}
	calls := 0
	err := Do(ctx, policy, func(int) error {
		calls++
		cancel() // the wait after this attempt is the one that sees the cancel
		return errTransient
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Errorf("err = %v, calls = %d; want context.Canceled after 1 call", err, calls)
	}
}

func TestDo_SkipsWaitPastDeadline(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	policy := Policy{MaximumAttempts: 3, BaseDelay: time.Hour, MaximumDelay: time.Hour, ShouldRetry: retryAll}
	calls := 0
	err := Do(ctx, policy, func(int) error {
		calls++
		return errTransient
	})
	if !errors.Is(err, errTransient) || calls != 1 {
		t.Errorf("err = %v, calls = %d; want the transient error after 1 call, without waiting", err, calls)
	}
}

func TestPolicy_Backoff(t *testing.T) {
	t.Parallel()
	base := Policy{BaseDelay: time.Second, MaximumDelay: time.Minute, Multiplier: 2}
	withJitter := base
	withJitter.Jitter = 0.2
	excessiveJitter := base
	excessiveJitter.Jitter = 5
	noBase := base
	noBase.BaseDelay = 0

	testCases := []struct {
		name    string
		policy  Policy
		attempt int
		random  float64
		want    time.Duration
	}{
		{name: "first wait is the base delay", policy: base, attempt: 1, random: 0.5, want: time.Second},
		{name: "grows by the multiplier", policy: base, attempt: 3, random: 0.5, want: 4 * time.Second},
		{name: "capped at the maximum", policy: base, attempt: 10, random: 0.5, want: time.Minute},
		{name: "huge attempt counts saturate instead of overflowing", policy: base, attempt: 5000, random: 0.5, want: time.Minute},
		{name: "lowest jitter sample shortens by the jitter fraction", policy: withJitter, attempt: 1, random: 0, want: 800 * time.Millisecond},
		{name: "middle jitter sample keeps the wait", policy: withJitter, attempt: 1, random: 0.5, want: time.Second},
		{name: "jitter above 1 counts as 1", policy: excessiveJitter, attempt: 1, random: 0, want: 0},
		{name: "no base delay means no wait", policy: noBase, attempt: 4, random: 0.9, want: 0},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := testCase.policy.withDefaults().backoff(testCase.attempt, testCase.random)
			if got != testCase.want {
				t.Errorf("backoff = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestPolicy_Delay(t *testing.T) {
	t.Parallel()
	policy := Policy{BaseDelay: time.Second, MaximumDelay: 10 * time.Second}.withDefaults()
	testCases := []struct {
		name string
		err  error
		want time.Duration
	}{
		{name: "plain error uses the backoff", err: errTransient, want: 2 * time.Second},
		{name: "retry-after hint replaces the backoff", err: &AfterError{Cause: errTransient, After: 3 * time.Second}, want: 3 * time.Second},
		{name: "wrapped retry-after hint still counts", err: fmt.Errorf("call: %w", &AfterError{After: 3 * time.Second}), want: 3 * time.Second},
		{name: "retry-after hint is capped", err: &AfterError{After: time.Hour}, want: 10 * time.Second},
		{name: "empty retry-after hint falls back to the backoff", err: &AfterError{Cause: errTransient}, want: 2 * time.Second},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := policy.delay(2, testCase.err, 0.5); got != testCase.want {
				t.Errorf("delay = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestPolicy_WithDefaults(t *testing.T) {
	t.Parallel()
	got := Policy{MaximumAttempts: -3, MaximumDelay: -1, Multiplier: 0.5}.withDefaults()
	if got.MaximumAttempts != 1 || got.MaximumDelay != defaultMaximumDelay || got.Multiplier != defaultMultiplier || got.ShouldRetry == nil {
		t.Errorf("withDefaults = %+v", got)
	}
}

func TestAfterError(t *testing.T) {
	t.Parallel()
	wrapped := &AfterError{Cause: errTransient, After: time.Second}
	if wrapped.Error() != "transient" || !errors.Is(wrapped, errTransient) {
		t.Errorf("AfterError with cause: Error() = %q, Is(cause) = %v", wrapped.Error(), errors.Is(wrapped, errTransient))
	}
	if bare := (&AfterError{}); bare.Error() != "retry after" || bare.Unwrap() != nil {
		t.Errorf("AfterError without cause: Error() = %q, Unwrap() = %v", bare.Error(), bare.Unwrap())
	}
}

func TestDefaultPolicy_Sane(t *testing.T) {
	t.Parallel()
	policy := DefaultPolicy()
	if policy.MaximumAttempts < 2 {
		t.Errorf("MaximumAttempts = %d, want >= 2", policy.MaximumAttempts)
	}
	if policy.BaseDelay <= 0 {
		t.Errorf("BaseDelay = %v, want > 0", policy.BaseDelay)
	}
	if policy.MaximumDelay < policy.BaseDelay {
		t.Errorf("MaximumDelay (%v) < BaseDelay (%v)", policy.MaximumDelay, policy.BaseDelay)
	}
	if policy.Multiplier < 1 {
		t.Errorf("Multiplier = %v, want >= 1", policy.Multiplier)
	}
	if policy.ShouldRetry == nil || !policy.ShouldRetry(errTransient) {
		t.Error("ShouldRetry must retry a plain error")
	}
}
