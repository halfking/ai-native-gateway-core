// bg/node_probe_submit_gate_test.go — 2026-09-05 node_probe_worker 降噪.
//
// Regression coverage for the per-cycle ERROR spam
// "node_probe_worker: enqueue via queue exhausted retries" with
// "automatic probe task is not eligible: credential_id=N"
// (docs/2026-09-05-pg-error-audit-and-environment.md §5 P1):
//
//  1. the pump candidate query (pumpDueStatesSQL) must pre-filter rows with
//     the SAME automatic-probe eligibility gate the queue enforces at
//     Enqueue, so permanently ineligible credentials never enter the
//     submit retry loop;
//  2. submitViaQueueSource must treat deterministic gate rejections
//     (ErrProbeAutomaticIneligible / ErrProbeOutOfScope) as skip-once:
//     exactly ONE Info log, no retry attempts, no ERROR, and a nil error
//     so no caller re-arms or re-logs the row;
//  3. transient enqueue failures must keep the P1.3 semantics untouched:
//     bounded retries, WARN per attempt, ERROR on exhaustion, non-nil error.
package bg

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// sliceLogHandler captures slog records so tests can assert emitted levels
// without touching any sink infrastructure.
type sliceLogHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *sliceLogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *sliceLogHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}

func (h *sliceLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *sliceLogHandler) WithGroup(string) slog.Handler      { return h }

func (h *sliceLogHandler) snapshot() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]slog.Record, len(h.records))
	copy(out, h.records)
	return out
}

// captureNodeProbeLogs swaps the default slog logger for a capturing one and
// returns a restore func plus a reader for the captured records.
func captureNodeProbeLogs(t *testing.T) (func(), func() []slog.Record) {
	t.Helper()
	h := &sliceLogHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	return func() { slog.SetDefault(prev) }, h.snapshot
}

// messages filters the captured records down to the node_probe enqueue
// lifecycle messages this file asserts on, so concurrently-running noisy
// tests cannot break the level assertions.
func enqueueMessages(records []slog.Record) []struct {
	level   slog.Level
	message string
} {
	var out []struct {
		level   slog.Level
		message string
	}
	for _, r := range records {
		msg := r.Message
		if strings.Contains(msg, "enqueue") {
			out = append(out, struct {
				level   slog.Level
				message string
			}{r.Level, msg})
		}
	}
	return out
}

func newGateTestWorker(behavior func(call int) (int64, bool, error)) (*NodeProbeWorker, *int) {
	calls := 0
	w := &NodeProbeWorker{probeQueue: &ProbeQueue{}}
	w.enqueueFn = func(_ context.Context, _ ProbeQueueTask) (int64, bool, error) {
		calls++
		return behavior(calls)
	}
	return w, &calls
}

func TestSubmitViaQueueSourceSkipsIneligibleGateWithoutRetry(t *testing.T) {
	restore, snapshot := captureNodeProbeLogs(t)
	defer restore()
	w, calls := newGateTestWorker(func(int) (int64, bool, error) {
		return 0, false, fmt.Errorf("%w: credential_id=%d", ErrProbeAutomaticIneligible, 7)
	})

	inserted, err := w.submitViaQueueSource(7, "gpt-x", "", "", "periodic")
	if err != nil {
		t.Fatalf("deterministic ineligible rejection must not surface as error, got %v", err)
	}
	if inserted {
		t.Fatal("inserted must be false on gate skip")
	}
	if *calls != 1 {
		t.Fatalf("deterministic rejection must not consume the retry budget: got %d enqueue calls, want 1", *calls)
	}

	msgs := enqueueMessages(snapshot())
	for _, m := range msgs {
		if m.level >= slog.LevelWarn {
			t.Fatalf("gate skip must not emit WARN/ERROR logs, got level=%s msg=%q", m.level, m.message)
		}
	}
	if len(msgs) != 1 {
		dumped := ""
		for _, m := range msgs {
			dumped += fmt.Sprintf("\n  level=%s msg=%q", m.level, m.message)
		}
		t.Fatalf("gate skip must be logged exactly once (per-credential dedup), got %d enqueue logs:%s", len(msgs), dumped)
	}
	if !strings.Contains(msgs[0].message, "deterministic gate") {
		t.Fatalf("single skip log should identify the deterministic gate, got %q", msgs[0].message)
	}
}

func TestSubmitViaQueueSourceSkipsOutOfScopeGateWithoutRetry(t *testing.T) {
	restore, snapshot := captureNodeProbeLogs(t)
	defer restore()
	w, calls := newGateTestWorker(func(int) (int64, bool, error) {
		return 0, false, fmt.Errorf("%w: tenant=%q credential_id=%d model=%q", ErrProbeOutOfScope, "default", 9, "m")
	})

	inserted, err := w.submitViaQueueSource(9, "m", "default", "", "request_failure")
	if err != nil {
		t.Fatalf("deterministic out-of-scope rejection must not surface as error, got %v", err)
	}
	if inserted {
		t.Fatal("inserted must be false on gate skip")
	}
	if *calls != 1 {
		t.Fatalf("out-of-scope rejection must not be retried: got %d enqueue calls, want 1", *calls)
	}
	for _, m := range enqueueMessages(snapshot()) {
		if m.level >= slog.LevelWarn {
			t.Fatalf("out-of-scope skip must not emit WARN/ERROR logs, got level=%s msg=%q", m.level, m.message)
		}
	}
}

func TestSubmitViaQueueSourceStillRetriesTransientErrors(t *testing.T) {
	restore, snapshot := captureNodeProbeLogs(t)
	defer restore()
	w, calls := newGateTestWorker(func(int) (int64, bool, error) {
		return 0, false, errors.New("connect: connection refused")
	})

	_, err := w.submitViaQueueSource(7, "gpt-x", "", "", "periodic")
	if err == nil {
		t.Fatal("transient enqueue failure must still surface as error after retries are exhausted")
	}
	if *calls != nodeProbeQueueSubmitMaxAttempts {
		t.Fatalf("transient failure must keep the P1.3 retry budget: got %d enqueue calls, want %d",
			*calls, nodeProbeQueueSubmitMaxAttempts)
	}
	var sawWarn, sawError bool
	for _, m := range enqueueMessages(snapshot()) {
		switch {
		case m.level == slog.LevelWarn && strings.Contains(m.message, "failed, retrying"):
			sawWarn = true
		case m.level == slog.LevelError && strings.Contains(m.message, "exhausted retries"):
			sawError = true
		}
	}
	if !sawWarn || !sawError {
		t.Fatalf("transient failure must keep WARN-per-attempt + final ERROR logs (sawWarn=%v sawError=%v)", sawWarn, sawError)
	}
}

func TestSubmitViaQueueSourceRecoversAfterTransientFailure(t *testing.T) {
	restore, _ := captureNodeProbeLogs(t)
	defer restore()
	w, calls := newGateTestWorker(func(call int) (int64, bool, error) {
		if call == 1 {
			return 0, false, errors.New("connect: connection refused")
		}
		return 42, true, nil
	})

	inserted, err := w.submitViaQueueSource(7, "gpt-x", "", "", "periodic")
	if err != nil {
		t.Fatalf("transient failure followed by success must not surface an error, got %v", err)
	}
	if !inserted {
		t.Fatal("inserted must be true when the retry succeeds")
	}
	if *calls != 2 {
		t.Fatalf("want exactly 2 enqueue calls (fail then succeed), got %d", *calls)
	}
}

// TestPumpDueStatesSQLAppliesAutomaticEligibilityGate pins the root-cause
// fix: the pump candidate query must carry the shared eligibility gate so
// permanently ineligible rows are filtered out BEFORE the batch LIMIT — they
// never reach submitViaQueueSource, so they can neither burn retry attempts
// nor emit the per-cycle ERROR spam. All pre-existing candidate semantics
// (paused/due filters, tenant projection, batch cap) must stay intact.
func TestPumpDueStatesSQLAppliesAutomaticEligibilityGate(t *testing.T) {
	sql := pumpDueStatesSQL()
	fragment := automaticProbeEligibilityExistsSQL("nps.credential_id")
	if !strings.Contains(sql, fragment) {
		t.Fatalf("pump SQL must embed the shared automatic eligibility gate verbatim:\n%s", sql)
	}
	// The gate itself must stay complete (same lifecycle gates the queue
	// enforces at Enqueue — contract shared with probe_automatic_gating_test).
	for _, want := range []string{
		"COALESCE(c.status, 'active') = 'active'",
		"COALESCE(c.lifecycle_status, 'active') = 'active'",
		"COALESCE(c.manual_disabled, FALSE) = FALSE",
		"COALESCE(p.enabled, FALSE) = TRUE",
		"COALESCE(p.manual_disabled, FALSE) = FALSE",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("pump eligibility gate missing %q", want)
		}
	}
	// Gate must sit in WHERE (before ORDER BY/LIMIT) so ineligible rows do
	// not consume nodeProbeQueuePumpBatch slots.
	gatePos := strings.Index(sql, fragment)
	orderByPos := strings.Index(sql, "ORDER BY")
	limitPos := strings.Index(sql, "LIMIT $1")
	if gatePos < 0 || orderByPos < 0 || limitPos < 0 || gatePos > orderByPos || orderByPos > limitPos {
		t.Fatalf("eligibility gate must be applied before ORDER BY/LIMIT (gate=%d order=%d limit=%d)", gatePos, orderByPos, limitPos)
	}
	// Pre-existing candidate semantics must be preserved.
	for _, want := range []string{
		"SELECT nps.credential_id, nps.raw_model_name",
		"COALESCE(cred.tenant_id, 'default')",
		"FROM node_probe_state nps",
		"JOIN credentials cred ON cred.id = nps.credential_id",
		"nps.paused = FALSE",
		"nps.next_retry_at <= now()",
		"LIMIT $1",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("pump SQL lost pre-existing candidate clause %q:\n%s", want, sql)
		}
	}
}
