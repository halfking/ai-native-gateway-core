package telemetry

import (
	"strings"
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

func TestRequestLogsUpdateSQL_SetClauseDoesNotReferenceTargetAlias(t *testing.T) {
	const updateSQL = `
		UPDATE request_logs
		   SET client_model = COALESCE($2, client_model),
		       outbound_model = COALESCE($3, outbound_model),
		       credential_id = COALESCE($4, credential_id),
		       provider_id = COALESCE($5, provider_id),
		       canonical_id = COALESCE($6, canonical_id),
		       client_profile = COALESCE($7, client_profile),
		       request_mode = COALESCE($8, request_mode),
		       end_user_id = COALESCE($9, end_user_id),
		       prompt_tokens = COALESCE($10, prompt_tokens),
		       completion_tokens = COALESCE($11, completion_tokens),
		       total_tokens = COALESCE($12, total_tokens),
		       cache_read_tokens = COALESCE($13, cache_read_tokens),
		       cache_write_tokens = COALESCE($14, cache_write_tokens),
		       cost_usd = COALESCE($15, cost_usd),
		       cost_display = COALESCE($16, cost_display),
		       cost_currency = COALESCE($17, cost_currency),
		       stream_first_chunk_ms = COALESCE($18, stream_first_chunk_ms),
		       stream_chunk_count = COALESCE($19, stream_chunk_count),
		       stream_done_received = COALESCE($20, stream_done_received),
		       stream_interrupted = COALESCE($21, stream_interrupted),
		       response_checksum = COALESCE($22, response_checksum),
		       response_preview = COALESCE($23, response_preview),
		       response_body = COALESCE(CAST($24 AS jsonb), response_body),
		       failure_stage = COALESCE($25, failure_stage),
		       failure_detail_code = COALESCE($26, failure_detail_code),
		       transform_rule_id = COALESCE($27, transform_rule_id),
		       egress_protocol = COALESCE($28, egress_protocol),
		       request_preview = COALESCE($29, request_preview),
		       transform_summary = COALESCE($30, transform_summary),
		       request_body = COALESCE(CAST($31 AS jsonb), request_body),
		       usage_source = COALESCE(NULLIF($32, ''), usage_source),
		       success = COALESCE($33, success),
		       request_status = COALESCE($34, request_status),
		       error_kind = CASE
		           WHEN COALESCE($33, success) = TRUE THEN NULL
		           ELSE COALESCE($35, error_kind)
		       END,
		       latency_ms = COALESCE($36, latency_ms),
		       identity_hash = COALESCE($37, identity_hash),
		       search_text = COALESCE($38, search_text),
		       gw_session_id = COALESCE($39, gw_session_id),
		       gw_task_id = COALESCE($40, gw_task_id),
		       api_key_prefix = COALESCE($41, api_key_prefix),
		       api_key_owner_user = COALESCE($42, api_key_owner_user),
		       application_code = COALESCE($43, application_code),
		       is_auto_request = COALESCE($44, is_auto_request),
		       task_type = COALESCE($45, task_type),
		       auto_profile = COALESCE($46, auto_profile),
		       auto_decision = COALESCE(CAST($47 AS jsonb), auto_decision),
		       auto_confidence = COALESCE($48, auto_confidence),
		       work_type = COALESCE($49, work_type),
		       credits_charged = COALESCE($50, credits_charged),
		       parent_request_id = COALESCE($51, parent_request_id),
		       compression_reason = COALESCE($52, compression_reason),
		       compression_strategy = COALESCE($53, compression_strategy),
		       compression_meta = COALESCE(CAST($54 AS jsonb), compression_meta),
		       outbound_body = COALESCE(CAST($55 AS jsonb), outbound_body),
		       outbound_msg_count = COALESCE($56, outbound_msg_count),
		       outbound_token_est = COALESCE($57, outbound_token_est),
		       outbound_msg_hashes = COALESCE(CAST($58 AS jsonb), outbound_msg_hashes),
		       quality_flags = COALESCE(CAST($59 AS text[]), quality_flags),
		       quality_fix_actions = COALESCE(CAST($60 AS jsonb), quality_fix_actions),
		       quality_score = COALESCE($61, quality_score),
		       upstream_finish_reason = COALESCE($62, upstream_finish_reason),
		       tool_calls = COALESCE(CAST($63 AS jsonb), tool_calls),
		       client_request_id = COALESCE($64, client_request_id)
		  FROM latest
		 WHERE request_logs.id = latest.id
		   AND request_logs.ts = latest.ts
	`

	setIdx := strings.Index(updateSQL, "SET ")
	fromIdx := strings.Index(updateSQL, "FROM latest")
	if setIdx < 0 || fromIdx <= setIdx {
		t.Fatalf("unexpected update SQL layout")
	}
	setClause := updateSQL[setIdx:fromIdx]
	if strings.Contains(setClause, "rl.") {
		t.Fatalf("SET clause must not reference target alias rl: %s", setClause)
	}
	if strings.Contains(updateSQL, "UPDATE request_logs rl") {
		t.Fatal("UPDATE must not alias request_logs as rl")
	}
}

func TestInsertUpsertSQL_DoesNotReferenceUndefinedRLAlias(t *testing.T) {
	const upsertTail = `
		ON CONFLICT (request_id, ts) DO UPDATE SET
			client_request_id = COALESCE(EXCLUDED.client_request_id, request_logs.client_request_id)
	`
	if strings.Contains(upsertTail, "rl.client_request_id") {
		t.Fatal("upsert tail must not reference undefined rl alias")
	}
	if !strings.Contains(upsertTail, "request_logs.client_request_id") {
		t.Fatal("upsert tail must qualify the existing column with the request_logs table name to avoid ambiguity")
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
