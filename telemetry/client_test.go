package telemetry

import (
	"testing"
	"time"
)

func TestClient_Disabled(t *testing.T) {
	c := NewClient()
	if c.Enabled() {
		t.Fatal("no DB should be disabled")
	}
	c.EmitDecisionLog(&DecisionLogEntry{RequestID: "test"})
	c.EmitRequestLog(&RequestLogEntry{RequestID: "test"})
	c.Stop()
}

func TestClient_QueueFull(t *testing.T) {
	c := newClientWithBufSize(2)

	for i := 0; i < 10; i++ {
		c.EmitDecisionLog(&DecisionLogEntry{
			RequestID: "overflow",
			Model:     "test",
			Success:   true,
		})
	}

	c.Stop()
}

func TestClient_QueueFull_SyncFallback(t *testing.T) {
	c := newClientWithBufSize(1)

	// Fill the queue so the next Emit hits the default (sync) path.
	// Worker doesn't drain during test, so buffer fills after 1 item.
	c.EmitDecisionLog(&DecisionLogEntry{RequestID: "fill", Model: "test", Success: true})

	// This emit should hit the default case (sync insert) without blocking.
	c.EmitDecisionLog(&DecisionLogEntry{
		RequestID: "sync",
		Model:     "test",
		Success:   true,
	})

	c.Stop()
}

func TestClient_EmitDoesNotBlock(t *testing.T) {
	c := NewClient()
	start := time.Now()
	for i := 0; i < 100; i++ {
		c.EmitDecisionLog(&DecisionLogEntry{RequestID: "bench", Model: "test", Success: true})
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Error("Emit should not block")
	}
	c.Stop()
}

func TestResolveRequestStatus(t *testing.T) {
	t.Parallel()
	errKind := "timeout"
	cases := []struct {
		name      string
		success   bool
		errorKind *string
		initial   bool
		want      string
	}{
		{name: "success", success: true, want: RequestStatusSuccess},
		{name: "failure", success: false, errorKind: &errKind, want: RequestStatusFailure},
		{name: "initial in progress", success: false, initial: true, want: RequestStatusInProgress},
		{name: "update without error still in progress", success: false, initial: false, want: RequestStatusInProgress},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ResolveRequestStatus(tc.success, tc.errorKind, tc.initial); got != tc.want {
				t.Fatalf("ResolveRequestStatus() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeRequestStatus(t *testing.T) {
	entry := &RequestLogEntry{Op: RequestLogInsert, Success: false}
	normalizeRequestStatus(entry)
	if entry.RequestStatus == nil || *entry.RequestStatus != RequestStatusInProgress {
		t.Fatalf("expected in_progress, got %#v", entry.RequestStatus)
	}
}

func TestMergeRequestLogEntry_ClearsErrorKindOnSuccess(t *testing.T) {
	// 2026-06-20 audit fix: when a failure entry is merged with
	// a success entry, the merged entry's ErrorKind should be
	// nil (matching the SQL CASE that clears it in the DB).
	// This prevents stale error_kind from being logged by any
	// pre-write observability path.
	rateLimit := "rate_limit"
	emptyKind := ""

	// Start with a failure entry (the dst of the first merge).
	failure := &RequestLogEntry{
		RequestID:     "req-1",
		Op:            RequestLogUpdate,
		Success:       false,
		ErrorKind:     &rateLimit,
		RequestStatus: strPtrT("failure"),
	}

	// Then a success update arrives.
	success := &RequestLogEntry{
		RequestID:     "req-1",
		Op:            RequestLogUpdate,
		Success:       true,
		ErrorKind:     &emptyKind, // empty string: "clear it"
		RequestStatus: strPtrT("success"),
	}

	mergeRequestLogEntry(failure, success)

	if !failure.Success {
		t.Error("merged Success should be true")
	}
	if failure.ErrorKind != nil {
		t.Errorf("merged ErrorKind should be nil when Success=true, got %v", *failure.ErrorKind)
	}
	if failure.RequestStatus == nil || *failure.RequestStatus != "success" {
		t.Errorf("RequestStatus should be 'success', got %v", failure.RequestStatus)
	}
}

func TestMergeRequestLogEntry_KeepsErrorKindOnFailure(t *testing.T) {
	// If both entries are failures, error_kind from the latest
	// update wins. This is intentional: each new entry fully
	// overwrites dst via mergeStringPtr's *dst = &v pattern, so
	// the most recent information is preserved. In practice the
	// async-retry goroutine emits at most one failure update per
	// request_id, so this is more of a defensive guarantee than
	// a frequent code path.
	rateLimit := "rate_limit"
	upstreamDown := "upstream_down"

	dst := &RequestLogEntry{
		RequestID: "req-2",
		Op:        RequestLogUpdate,
		Success:   false,
		ErrorKind: &rateLimit,
	}
	src := &RequestLogEntry{
		RequestID: "req-2",
		Op:        RequestLogUpdate,
		Success:   false,
		ErrorKind: &upstreamDown,
	}

	mergeRequestLogEntry(dst, src)

	if dst.Success {
		t.Error("Success should remain false")
	}
	if dst.ErrorKind == nil {
		t.Fatal("ErrorKind should be preserved when both are failures")
	}
	// Last non-empty wins: upstream_down replaces rate_limit.
	if *dst.ErrorKind != "upstream_down" {
		t.Errorf("ErrorKind = %q, want upstream_down (last non-empty wins)", *dst.ErrorKind)
	}
}

func TestMergeRequestLogBatch_MultipleUpdatesCoalesce(t *testing.T) {
	// Two UPDATE entries for the same request_id should coalesce
	// into one entry with the merged fields.
	reqID := "req-batch-1"
	rateLimit := "rate_limit"
	emptyKind := ""

	batch := []any{
		&RequestLogEntry{
			RequestID: reqID,
			Op:        RequestLogUpdate,
			Success:   false,
			ErrorKind: &rateLimit,
		},
		&RequestLogEntry{
			RequestID: reqID,
			Op:        RequestLogUpdate,
			Success:   true,
			ErrorKind: &emptyKind,
		},
		// Different request_id — should NOT be merged.
		&RequestLogEntry{
			RequestID: "req-other",
			Op:        RequestLogUpdate,
			Success:   true,
		},
	}

	merged := mergeRequestLogBatch(batch)

	// Expect 2 entries: the merged one for reqID + the other one.
	if len(merged) != 2 {
		t.Fatalf("len(merged) = %d, want 2", len(merged))
	}
	// The merged entry for req-batch-1 should have Success=true
	// and ErrorKind=nil.
	var found *RequestLogEntry
	for _, item := range merged {
		if e, ok := item.(*RequestLogEntry); ok && e.RequestID == reqID {
			found = e
			break
		}
	}
	if found == nil {
		t.Fatal("merged entry for req-batch-1 not found")
	}
	if !found.Success {
		t.Error("merged Success should be true")
	}
	if found.ErrorKind != nil {
		t.Errorf("merged ErrorKind should be nil after success merge, got %v", *found.ErrorKind)
	}
}

// strPtrT is a small helper to avoid pulling strings package
// indirection into this section.
func strPtrT(s string) *string { return &s }

// SQL contract test is intentionally deferred to client_live_test.go
// (TestRequestLogUniqueRequestID) because dbPool is a concrete
// *pgxpool.Pool rather than an interface, and the test depends on
// the actual SQL hitting the (request_id)-only unique constraint
// added by migration 301 / db/db.go ensureRequestLogsUniqueIndex.
// See CHANGELOG_request_logs_unique_id.md for context.
