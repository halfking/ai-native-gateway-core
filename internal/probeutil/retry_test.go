package probeutil

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestIsProbeRetryableStatus_NonRetryable(t *testing.T) {
	// Clear business failures — must fail fast.
	for _, code := range []int{400, 401, 402, 403, 404, 422} {
		if IsProbeRetryableStatus(code) {
			t.Errorf("status %d: got retryable=true, want false (clear business failure)", code)
		}
	}
}

func TestIsProbeRetryableStatus_Retryable(t *testing.T) {
	// Transient conditions — must retry.
	for _, code := range []int{0, 408, 425, 429, 500, 502, 503, 504, 599} {
		if !IsProbeRetryableStatus(code) {
			t.Errorf("status %d: got retryable=false, want true (transient)", code)
		}
	}
}

func TestIsProbeRetryableStatus_OutOfRange(t *testing.T) {
	// Non-failure status codes (1xx/2xx/3xx) are never produced by a probe
	// as a failure outcome, but the classifier must keep them non-retryable
	// defensively — a future caller bug surfacing 200 here should not spin
	// the retry loop.
	nonRetryable := []int{100, 200, 204, 301, 304, 600, 999}
	for _, code := range nonRetryable {
		if IsProbeRetryableStatus(code) {
			t.Errorf("status %d: should be non-retryable (not a failure outcome)", code)
		}
	}

	// Other 4xx not in the fail-fast set (405/409/410/418/428/451…) retry.
	// Policy: err on the side of retrying — a single probe must not declare
	// a credential dead.
	retryable4xx := []int{405, 406, 407, 409, 410, 411, 413, 414, 417, 418, 421, 423, 424, 426, 428, 431, 451}
	for _, code := range retryable4xx {
		if !IsProbeRetryableStatus(code) {
			t.Errorf("status %d: should be retryable (4xx not in fail-fast set)", code)
		}
	}
}

func TestIsProbeCtxCancel_Nil(t *testing.T) {
	if IsProbeCtxCancel(nil) {
		t.Error("nil error: should not be treated as ctx cancel")
	}
}

func TestIsProbeCtxCancel_Canceled(t *testing.T) {
	if !IsProbeCtxCancel(context.Canceled) {
		t.Error("context.Canceled: should be detected as ctx cancel")
	}
}

func TestIsProbeCtxCancel_DeadlineExceeded(t *testing.T) {
	if !IsProbeCtxCancel(context.DeadlineExceeded) {
		t.Error("context.DeadlineExceeded: should be detected as ctx cancel")
	}
}

func TestIsProbeCtxCancel_Wrapped(t *testing.T) {
	// errors.Is unwraps; verify our function relies on errors.Is.
	wrapped := errors.Join(errors.New("network dial: connection refused"), context.Canceled)
	if !IsProbeCtxCancel(wrapped) {
		t.Error("wrapped context.Canceled: should still be detected as ctx cancel")
	}
}

func TestIsProbeCtxCancel_UnrelatedError(t *testing.T) {
	if IsProbeCtxCancel(errors.New("connection refused")) {
		t.Error("plain network error: should NOT be treated as ctx cancel")
	}
}

func TestProbeRetryDelays_Schedule(t *testing.T) {
	// Lock the schedule: 0s, 2s, 5s. Any change to this slice is a
	// deliberate policy change and must be reflected in CHANGELOG.
	want := []time.Duration{0, 2 * time.Second, 5 * time.Second}
	if len(ProbeRetryDelays) != len(want) {
		t.Fatalf("ProbeRetryDelays length: got %d want %d", len(ProbeRetryDelays), len(want))
	}
	for i := range want {
		if ProbeRetryDelays[i] != want[i] {
			t.Errorf("ProbeRetryDelays[%d]: got %v want %v", i, ProbeRetryDelays[i], want[i])
		}
	}
}

func TestSleepWithCtx_Completes(t *testing.T) {
	ctx := context.Background()
	// Use a small but nonzero delay so the timer is exercised.
	start := time.Now()
	ok := SleepWithCtx(ctx, 10*time.Millisecond)
	elapsed := time.Since(start)
	if !ok {
		t.Error("background ctx: SleepWithCtx should return true on normal completion")
	}
	if elapsed < 5*time.Millisecond {
		t.Errorf("expected ~10ms sleep, got %v", elapsed)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("sleep too long: %v", elapsed)
	}
}

func TestSleepWithCtx_ZeroReturnsImmediately(t *testing.T) {
	ctx := context.Background()
	start := time.Now()
	ok := SleepWithCtx(ctx, 0)
	if !ok {
		t.Error("zero delay: SleepWithCtx should return true on normal completion")
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Errorf("zero delay took too long: %v", time.Since(start))
	}
}

func TestSleepWithCtx_CanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after a short wait — SleepWithCtx must return early with false.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	ok := SleepWithCtx(ctx, 5*time.Second)
	elapsed := time.Since(start)
	if ok {
		t.Error("canceled ctx: SleepWithCtx should return false")
	}
	if elapsed > 1*time.Second {
		t.Errorf("canceled ctx: SleepWithCtx should return promptly, took %v", elapsed)
	}
}

func TestSleepWithCtx_DeadlineExceeded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	ok := SleepWithCtx(ctx, 5*time.Second)
	elapsed := time.Since(start)
	if ok {
		t.Error("deadline-exceeded ctx: SleepWithCtx should return false")
	}
	if elapsed > 1*time.Second {
		t.Errorf("deadline-exceeded ctx: SleepWithCtx should return promptly, took %v", elapsed)
	}
}
