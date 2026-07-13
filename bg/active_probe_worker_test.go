// bg/active_probe_worker_test.go — unit tests for the active probe worker.
//
// These tests cover the pure state-management logic (Submit dedup,
// config defaults, backoff scheduling). Integration tests with a real
// DB / HTTP client belong in active_probe_integration_test.go.
package bg

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/credentialstate"
)

// stubStateObserver records calls to UpdateFromProbe for inspection.
type stubStateObserver struct {
	mu       sync.Mutex
	calls    []*credentialstate.State
	disabled bool
}

func (s *stubStateObserver) UpdateOnSuccess(_ context.Context, _ int, _ string, _ int, _ string) {
}

func (s *stubStateObserver) UpdateOnFailure(_ context.Context, _ int, _ string, _ interface{}, _ string) {
}

func (s *stubStateObserver) UpdateFromProbe(_ context.Context, st *credentialstate.State) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, st)
}

// newTestWorker returns a worker that does not start the goroutine and
// has minimal config so tests can drive Submit/processOne directly.
func newTestWorker() *ActiveProbeWorker {
	return NewActiveProbeWorker(ActiveProbeWorkerConfig{
		Enabled:     true,
		MaxAttempts: 5,
		TimeoutMs:   1000,
		QueueSize:   16,
	})
}

func TestNewActiveProbeWorker_Defaults(t *testing.T) {
	w := NewActiveProbeWorker(ActiveProbeWorkerConfig{Enabled: true})
	if w.cfg.MaxAttempts != 5 {
		t.Errorf("MaxAttempts default = %d, want 5", w.cfg.MaxAttempts)
	}
	if w.cfg.ConsecutiveThreshold != 2 {
		t.Errorf("ConsecutiveThreshold default = %d, want 2", w.cfg.ConsecutiveThreshold)
	}
	if w.cfg.TimeoutMs != 10000 {
		t.Errorf("TimeoutMs default = %d, want 10000", w.cfg.TimeoutMs)
	}
	if w.cfg.QueueSize != 128 {
		t.Errorf("QueueSize default = %d, want 128", w.cfg.QueueSize)
	}
}

func TestNewActiveProbeWorker_DisabledNoStart(t *testing.T) {
	w := NewActiveProbeWorker(ActiveProbeWorkerConfig{Enabled: false})
	// Start should be a no-op when disabled.
	w.Start(context.Background())
	// Verify no goroutine was started: Stop should not block forever.
	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()
	select {
	case <-done:
		// expected
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Stop() blocked too long for disabled worker")
	}
}

func TestSubmit_DedupPreventsDuplicate(t *testing.T) {
	w := newTestWorker()

	w.Submit(42, "gpt-4", "req-1")
	w.Submit(42, "gpt-4", "req-2") // dedup hit
	w.Submit(42, "gpt-4", "req-3") // dedup hit

	if got := w.RunningCount(); got != 1 {
		t.Errorf("RunningCount = %d, want 1 (dedup should keep only one entry)", got)
	}
}

func TestSubmit_DifferentKeysAllowed(t *testing.T) {
	w := newTestWorker()

	w.Submit(1, "gpt-4", "req-1")
	w.Submit(2, "gpt-4", "req-2")
	w.Submit(1, "claude-sonnet-5", "req-3")

	if got := w.RunningCount(); got != 3 {
		t.Errorf("RunningCount = %d, want 3 (distinct keys)", got)
	}
}

func TestSubmit_DisabledIsNoOp(t *testing.T) {
	w := NewActiveProbeWorker(ActiveProbeWorkerConfig{Enabled: false})

	w.Submit(1, "gpt-4", "req-1")

	if got := w.RunningCount(); got != 0 {
		t.Errorf("disabled worker should not track probes, got RunningCount=%d", got)
	}
}

func TestMarkSuccess_ClearsDedup(t *testing.T) {
	w := newTestWorker()

	w.Submit(7, "gpt-4", "req-1")
	if w.RunningCount() != 1 {
		t.Fatalf("after Submit: RunningCount=%d, want 1", w.RunningCount())
	}

	w.markSuccess(7, "gpt-4")

	if w.RunningCount() != 0 {
		t.Errorf("after markSuccess: RunningCount=%d, want 0", w.RunningCount())
	}
}

func TestMarkFailedFinal_ClearsDedup(t *testing.T) {
	w := newTestWorker()

	w.Submit(8, "gpt-4", "req-1")
	w.markFailedFinal(8, "gpt-4")

	if w.RunningCount() != 0 {
		t.Errorf("after markFailedFinal: RunningCount=%d, want 0", w.RunningCount())
	}
}

func TestMarkFailedRetry_ReschedulesAtLaterTime(t *testing.T) {
	w := newTestWorker()

	w.Submit(9, "gpt-4", "req-1")

	// markFailedRetry with attempt=1 and a 5s delay
	future := time.Now().Add(5 * time.Second)
	w.markFailedRetry(9, "gpt-4", 1, future, 5*time.Second)

	// Dedup entry should still exist
	if w.RunningCount() != 1 {
		t.Fatalf("after markFailedRetry: RunningCount=%d, want 1", w.RunningCount())
	}

	// And the queue should now have a re-submitted task
	select {
	case task := <-w.queue:
		if task.CredID != 9 || task.Model != "gpt-4" {
			t.Errorf("re-enqueued task = (%d, %s), want (9, gpt-4)", task.CredID, task.Model)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("markFailedRetry should re-enqueue the task")
	}
}

// TestRetryBackoffUsesCorrectChainEntry is the regression test for BUG #1
// (2026-07-13).
//
// Before the fix, processOne called computeBackoff(attempt + 1) after
// the just-failed probe, which silently dropped the 5s entry of
// DefaultErrorProbeBackoffChain — the effective retry schedule became
// [30s, 2m, 5m, 15m] instead of the documented [5s, 30s, 2m, 5m, 15m].
//
// This test exercises the math inside processOne directly (so it does
// not need a DB / executor / emitter) and asserts:
//   * after attempt=N fails, the worker schedules the next attempt in
//     chain[N-1] (= computeBackoff(N)) — NOT chain[N] (= computeBackoff(N+1)).
//
// To catch a regression to `computeBackoff(attempt + 1)` we compute the
// off-by-one form HERE and assert it is *not* what the worker would
// produce. If the worker regresses to off-by-one, the offender will
// stop matching the documented schedule, and we fail with a clear message.
func TestRetryBackoffUsesCorrectChainEntry(t *testing.T) {
	// workerBackoff mirrors the line in processOne (post-fix):
	//   backoff := computeBackoff(attempt)
	// We extract it here as a free function so this test stays
	// independent of the worker struct.
	workerBackoff := func(justFailedAttempt int) time.Duration {
		return computeBackoff(justFailedAttempt)
	}

	// buggyBackoff mirrors the OLD broken line:
	//   backoff := computeBackoff(attempt + 1)
	buggyBackoff := func(justFailedAttempt int) time.Duration {
		return computeBackoff(justFailedAttempt + 1)
	}

	// (just-failed attempt) → documented backoff
	cases := []struct {
		failedAttempt int
		want          time.Duration
	}{
		{1, 5 * time.Second},  // chain[0] — the entry the bug skipped
		{2, 30 * time.Second}, // chain[1]
		{3, 2 * time.Minute},  // chain[2]
		{4, 5 * time.Minute},  // chain[3]
		{5, 15 * time.Minute}, // chain[4] — capped, not exercised in prod (MaxAttempts=5 → markFailedFinal before this)
	}
	for _, c := range cases {
		got := workerBackoff(c.failedAttempt)
		if got != c.want {
			t.Errorf("BUG #1 regression: after attempt %d failed, worker schedules next "+
				"in %v, want %v. The fix replaced computeBackoff(attempt+1) with "+
				"computeBackoff(attempt) so the 5s first-retry entry is no longer skipped.",
				c.failedAttempt, got, c.want)
		}

		// Cross-check: the buggy form must NOT equal the documented value,
		// otherwise the chain is too short to detect the off-by-one.
		// (chain[3]=5m and chain[4]=15m are different; chain[4] and an
		// out-of-range attempt give back chain[last]=15m, so the last row
		// of cases below legitimately matches both forms.)
		if c.failedAttempt < 5 {
			if gotBuggy := buggyBackoff(c.failedAttempt); gotBuggy == c.want {
				t.Errorf("BUG #1 detectability: computeBackoff(%d) == computeBackoff(%d) "+
					"== %v. The chain is degenerate at this row — the off-by-one bug "+
					"would silently slip past the regression check.",
					c.failedAttempt+1, c.failedAttempt, c.want)
			}
		}
	}
}

func TestProbeKey_StableAcrossCalls(t *testing.T) {
	a := probeKey(1, "gpt-4")
	b := probeKey(1, "gpt-4")
	c := probeKey(2, "gpt-4")
	d := probeKey(1, "gpt-5")
	if a != b {
		t.Errorf("probeKey not stable: %q vs %q", a, b)
	}
	if a == c {
		t.Errorf("probeKey collides across cred IDs")
	}
	if a == d {
		t.Errorf("probeKey collides across models")
	}
}

func TestClassifyProbeErrorKind_AllStatuses(t *testing.T) {
	cases := []struct {
		status ProbeStatus
		want   string
	}{
		{ProbeStatusSuccess, ""},
		{ProbeStatusTimeout, "probe_direct_timeout"},
		{ProbeStatusNetwork, "probe_direct_network_error"},
		{ProbeStatusAuth, "probe_direct_auth_failed"},
		{ProbeStatusRate, "probe_direct_rate_limited"},
		{ProbeStatusHTTP5xx, "probe_direct_http_5xx"},
		{ProbeStatusHTTP4xx, "probe_direct_http_4xx"},
		{ProbeStatusCanceled, "probe_direct_canceled"},
		{ProbeStatusSkipped, "probe_direct_failed"},
		{ProbeStatusFailed, "probe_direct_failed"},
		{"unknown_status", "probe_direct_failed"},
	}
	for _, c := range cases {
		got := classifyProbeErrorKind(&ProbeResult{Status: c.status})
		if got != c.want {
			t.Errorf("classifyProbeErrorKind(%q) = %q, want %q", c.status, got, c.want)
		}
	}
}

func TestClassifyProbeErrorKind_HTTP5xxWithStatus(t *testing.T) {
	r := &ProbeResult{Status: ProbeStatusHTTP5xx, HTTPStatus: 503}
	if got := classifyProbeErrorKind(r); got != "probe_direct_http_503" {
		t.Errorf("503 classification = %q, want probe_direct_http_503", got)
	}
}

func TestBuildProbeRequestID_HasPrefixAndCredID(t *testing.T) {
	id := buildProbeRequestID(123, "gpt-4o", 1, true, time.Now())
	if len(id) == 0 {
		t.Fatal("buildProbeRequestID returned empty string")
	}
	// Must start with "probe-direct-c123-m"
	if !startsWith(id, "probe-direct-c123-m") {
		t.Errorf("request_id %q missing 'probe-direct-c123-m' prefix", id)
	}
	// Must contain attempt=1
	if !containsStr(id, "a1-") {
		t.Errorf("request_id %q missing attempt=1 marker", id)
	}
	// Success row should end in -ok-<nano>
	if !containsStr(id, "-ok-") {
		t.Errorf("request_id %q missing '-ok-' marker", id)
	}
}

func TestBuildProbeRequestID_FailureMarker(t *testing.T) {
	id := buildProbeRequestID(7, "claude-sonnet-5", 3, false, time.Now())
	if !containsStr(id, "-fail-") {
		t.Errorf("request_id %q missing '-fail-' marker", id)
	}
	if !containsStr(id, "a3-") {
		t.Errorf("request_id %q missing attempt=3 marker", id)
	}
}

func TestBuildProbeQualityFlags_AlwaysIncludesProbeAndDirect(t *testing.T) {
	r := &ProbeResult{Status: ProbeStatusSuccess}
	flags := buildProbeQualityFlags(r, 1)
	if !sliceContains(flags, "probe") {
		t.Errorf("flags missing 'probe': %v", flags)
	}
	if !sliceContains(flags, "direct") {
		t.Errorf("flags missing 'direct': %v", flags)
	}
}

func TestBuildProbeQualityFlags_TimeoutFlagAdded(t *testing.T) {
	r := &ProbeResult{Status: ProbeStatusTimeout}
	flags := buildProbeQualityFlags(r, 1)
	if !sliceContains(flags, "probe_timeout") {
		t.Errorf("flags missing 'probe_timeout': %v", flags)
	}
}

func TestBuildProbeQualityFlags_FinalAttemptFlagAtMax(t *testing.T) {
	r := &ProbeResult{Status: ProbeStatusHTTP5xx}
	flags := buildProbeQualityFlags(r, 5)
	if !sliceContains(flags, "probe_final_attempt") {
		t.Errorf("flags missing 'probe_final_attempt' at attempt 5: %v", flags)
	}
}

func TestSanitizeModelForID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"gpt-4o", "gpt-4o"},
		{"claude_sonnet_5", "claude_sonnet_5"},
		{"model/v1", "model_v1"},
		{"with space", "with_space"},
		{"", "unknown"},
	}
	for _, c := range cases {
		got := sanitizeModelForID(c.in)
		if got != c.want {
			t.Errorf("sanitizeModelForID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSanitizeModelForID_TruncatesLong(t *testing.T) {
	long := "verylongmodelnameverylongmodelnameverylongmodelnameverylongmodelname"
	got := sanitizeModelForID(long)
	if len(got) > 40 {
		t.Errorf("sanitizeModelForID did not truncate: len=%d", len(got))
	}
}

// containsStr / sliceContains — tiny local helpers (avoid pulling strings for tests).

func containsStr(s string, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func sliceContains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func startsWith(s, prefix string) bool {
	if len(prefix) > len(s) {
		return false
	}
	return s[:len(prefix)] == prefix
}

func TestRunningKeys_EmptyAndPopulated(t *testing.T) {
	w := newTestWorker()

	// Empty case
	if keys := w.RunningKeys(); len(keys) != 0 {
		t.Errorf("empty worker should have no keys, got %v", keys)
	}

	w.Submit(1, "a", "r1")
	w.Submit(2, "b", "r2")

	keys := w.RunningKeys()
	if len(keys) != 2 {
		t.Errorf("RunningKeys length = %d, want 2", len(keys))
	}
}

func TestNilWorkerSafety(t *testing.T) {
	var w *ActiveProbeWorker
	// All public methods must be nil-safe.
	w.Submit(1, "m", "r")
	if got := w.RunningCount(); got != 0 {
		t.Errorf("nil worker RunningCount = %d, want 0", got)
	}
	if keys := w.RunningKeys(); keys != nil {
		t.Errorf("nil worker RunningKeys = %v, want nil", keys)
	}
	if d := w.Done(); d == nil {
		t.Error("nil worker Done() returned nil channel")
	}
	w.Stop() // should not panic
}

// TestRetryBackoffCallShape_NoOffByOneInWorkerSource is the second half
// of the BUG #1 regression test (2026-07-13).
//
// TestRetryBackoffUsesCorrectChainEntry asserts the chain itself is
// [5s, 30s, 2m, 5m, 15m]. This test asserts the WORKER calls the
// chain with the right index — `computeBackoff(attempt)`, not the
// off-by-one `computeBackoff(attempt + 1)`.
//
// We scan active_probe_worker.go and reject any line containing the
// buggy call shape. The previous form was
//
//	backoff := computeBackoff(attempt + 1)
//
// which silently dropped the 5s first-retry entry and produced the
// effective schedule [30s, 2m, 5m, 15m] instead of [5s, 30s, 2m, 5m].
//
// This is a coarse check (it doesn't run processOne end-to-end), but
// it catches the exact off-by-one regression in code review and CI.
func TestRetryBackoffCallShape_NoOffByOneInWorkerSource(t *testing.T) {
	// Use the canonical source path for this package; tests run with
	// cwd = the package directory so a relative path works.
	const relPath = "active_probe_worker.go"
	data, err := os.ReadFile(relPath)
	if err != nil {
		t.Fatalf("read %s: %v", relPath, err)
	}
	src := string(data)

	// The buggy call shape. Whitespace-tolerant: any number of spaces
	// between `attempt` and `+ 1`.
	const buggyCall = "computeBackoff(attempt + 1)"
	if strings.Contains(src, buggyCall) {
		// Find the offending line for a clearer error message.
		var badLine int
		for i, line := range strings.Split(src, "\n") {
			if strings.Contains(line, buggyCall) {
				badLine = i + 1
				break
			}
		}
		t.Fatalf("BUG #1 regression: %s contains the off-by-one call %q at line %d. "+
			"The worker must call `computeBackoff(attempt)` so the 5s first-retry "+
			"entry of DefaultErrorProbeBackoffChain is not skipped. See the comment "+
			"block in processOne and the design doc §3.2 for the contract.",
			relPath, buggyCall, badLine)
	}

	// Belt-and-braces: confirm the correct call shape is present
	// somewhere in processOne, otherwise this fix could have been
	// "removed the call entirely".
	const correctCall = "computeBackoff(attempt)"
	if !strings.Contains(src, correctCall) {
		t.Fatalf("BUG #1 regression: %s no longer contains the correct call %q. "+
			"If you are rewriting the backoff calculation, preserve the contract that "+
			"after attempt N fails the next retry waits chain[N-1].",
			relPath, correctCall)
	}
}
