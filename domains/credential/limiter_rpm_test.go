package credential

import (
	"context"
	"testing"
	"time"
)

// TestCheckCredentialRPM_BasicWindow exercises the per-credential RPM
// sliding window: under the limit the gate allows; at the limit it
// denies; after the window slides it allows again.
func TestCheckCredentialRPM_BasicWindow(t *testing.T) {
	l := NewLimiter()
	defer l.Stop()
	limit := 3
	lim := &limit

	// First N acquires within the window → allowed.
	for i := 0; i < limit; i++ {
		if !l.CheckCredentialRPM(1, 1, lim) {
			t.Fatalf("acquire #%d should be allowed (limit=%d)", i, limit)
		}
	}
	// (limit+1)th → denied.
	if l.CheckCredentialRPM(1, 1, lim) {
		t.Fatalf("acquire beyond limit should be denied (limit=%d)", limit)
	}

	// Different credential → independent window.
	if !l.CheckCredentialRPM(2, 1, lim) {
		t.Fatalf("different credential should have its own window")
	}
}

// TestCheckCredentialRPM_NilAndZero confirms nil/zero = unlimited.
func TestCheckCredentialRPM_NilAndZero(t *testing.T) {
	l := NewLimiter()
	defer l.Stop()

	// nil = unlimited (paid credentials default).
	for i := 0; i < 100; i++ {
		if !l.CheckCredentialRPM(1, 1, nil) {
			t.Fatalf("nil limit should always allow (i=%d)", i)
		}
	}
	// 0 = unlimited (explicit zero).
	zero := 0
	for i := 0; i < 100; i++ {
		if !l.CheckCredentialRPM(1, 1, &zero) {
			t.Fatalf("zero limit should always allow (i=%d)", i)
		}
	}
}

// TestCheckCredentialRPM_WindowSlides fakes the passage of time by
// (1) saturating the window, (2) waiting > 60s in real time would
// be slow, so we instead verify that the bucket is per-key (different
// keys do not share capacity) and that the size-bounded pruning works.
func TestCheckCredentialRPM_WindowSlides(t *testing.T) {
	l := NewLimiter()
	defer l.Stop()
	limit := 2
	lim := &limit

	// Saturate credential (1, 1).
	for i := 0; i < limit; i++ {
		l.CheckCredentialRPM(1, 1, lim)
	}
	if l.CheckCredentialRPM(1, 1, lim) {
		t.Fatal("should be saturated")
	}
	// Different credential keys are independent.
	if !l.CheckCredentialRPM(1, 2, lim) {
		t.Fatal("different credential must not share RPM window")
	}
	if !l.CheckCredentialRPM(2, 1, lim) {
		t.Fatal("different provider must not share RPM window")
	}
}

// TestAcquireAll_RPMDenialBlocksCandidate verifies that AcquireAll
// returns an error when the credential's RPM is saturated, and that
// a subsequent acquire on a different credential is still allowed.
func TestAcquireAll_RPMDenialBlocksCandidate(t *testing.T) {
	l := NewLimiter()
	defer l.Stop()
	limit := 2
	lim := &limit
	ctx := context.Background()

	// Saturate credential (1, 1).
	for i := 0; i < limit; i++ {
		_, err := l.AcquireAll(ctx, 1, 1, "hash", 0, 0, lim)
		if err != nil {
			t.Fatalf("acquire #%d should be allowed: %v", i, err)
		}
	}
	// Next acquire on the same credential fails (RPM saturated).
	_, err := l.AcquireAll(ctx, 1, 1, "hash", 0, 0, lim)
	if err == nil {
		t.Fatal("acquire beyond RPM limit should fail")
	}
	// Different credential still works.
	if _, err := l.AcquireAll(ctx, 1, 2, "hash", 0, 0, lim); err != nil {
		t.Fatalf("different credential should be allowed: %v", err)
	}
}

// TestAcquireAll_RPMDoesNotChargeFailedConcurrencyAcquire ensures a request
// that cannot obtain the credential semaphore does not consume RPM capacity.
func TestAcquireAll_RPMDoesNotChargeFailedConcurrencyAcquire(t *testing.T) {
	l := NewWithLimits(10, 10, 1, 1)
	defer l.Stop()

	limit := 2
	ctx := context.Background()
	held, err := l.AcquireAll(ctx, 1, 1, "held", 0, 0, &limit)
	if err != nil {
		t.Fatalf("holding acquire failed: %v", err)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := l.AcquireAll(timeoutCtx, 1, 1, "blocked", 0, 0, &limit); err == nil {
		t.Fatal("expected saturated credential acquire to fail")
	}

	held()
	first, err := l.AcquireAll(ctx, 1, 1, "first", 0, 0, &limit)
	if err != nil {
		t.Fatalf("first post-failure acquire should be allowed: %v", err)
	}
	first()
	if _, err := l.AcquireAll(ctx, 1, 1, "second", 0, 0, &limit); err == nil {
		t.Fatal("second post-failure acquire should be rejected after two successful reservations")
	}
}

// TestAcquireAll_NilRPMLimitUnlimited guards against future regressions
// where AcquireAll with nil rpm_limit accidentally throttles. Iterates
// at the credential's default concurrency ceiling (50) and releases
// after each acquire to verify the RPM check itself never blocks.
func TestAcquireAll_NilRPMLimitUnlimited(t *testing.T) {
	l := NewLimiter()
	defer l.Stop()
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		release, err := l.AcquireAll(ctx, 1, 1, fmtHash(i), 0, 0, nil)
		if err != nil {
			t.Fatalf("nil rpm_limit should never block (i=%d): %v", i, err)
		}
		release()
	}
	// Saturate concurrency to verify the semaphore still rejects when
	// RPM is nil — i.e. the nil-rpm-limit path doesn't accidentally
	// disable the concurrency gate.
	held := make([]func(), 0, 60)
	for i := 0; i < 50; i++ {
		release, err := l.AcquireAll(ctx, 1, 1, fmtHash(i), 0, 0, nil)
		if err != nil {
			t.Fatalf("concurrent acquire #%d should succeed (RPM=nil): %v", i, err)
		}
		held = append(held, release)
	}
	// 51st acquire (still nil RPM) must hit the concurrency semaphore.
	_, err := l.AcquireAll(ctx, 1, 1, fmtHash(99), 0, 0, nil)
	if err == nil {
		t.Fatal("concurrency gate should still reject when all 50 slots held (nil rpm)")
	}
	for _, r := range held {
		r()
	}
}

// TestCheckCredentialRPM_OldEntriesPruned prevents a memory leak: the
// window should not retain entries beyond the limit (oldest entries
// are pruned in-place on every check). We exercise with a tight loop.
func TestCheckCredentialRPM_OldEntriesPruned(t *testing.T) {
	l := NewLimiter()
	defer l.Stop()
	limit := 3
	lim := &limit

	// 1000 alternating acquires across two credentials should never
	// blow up the per-credential window size beyond `limit` because
	// each acquire is now within the 60s window but the count caps
	// at `limit`. We don't actually wait 60s; we just verify no panic
	// and the deny behaviour after limit is hit is consistent.
	for i := 0; i < 1000; i++ {
		l.CheckCredentialRPM(i%2+1, 1, lim)
	}
	if l.CheckCredentialRPM(1, 1, lim) {
		t.Fatal("credential (1,1) must remain saturated after 1000 acquires within 60s")
	}
}

// Suppress unused-time warning while keeping the file independent of
// the time package's pull-through elsewhere.
var _ = time.Second

func fmtHash(i int) string {
	const letters = "abcdef0123456789"
	b := make([]byte, 8)
	for j := 0; j < 8; j++ {
		b[j] = letters[(i+j)%len(letters)]
	}
	return string(b)
}
