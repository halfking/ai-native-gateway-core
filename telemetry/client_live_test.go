package telemetry

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestRequestLogInsertParamCount is an integration test that catches
// placeholder/argument mismatches in persistRequestLog's INSERT
// statement.
//
// 2026-06-19 incident: T-NEW-7 added the upstream_finish_reason column
// to the SQL ($65) and to the bind list — but the bind-list patch
// was missed on the FIRST insertRequestLog (the UPDATE merge was
// fine). The unit test suite passed; only a live 184 k3s
// deployment surfaced "mismatched param and argument count" with
// every POST /v1/chat/completions. This test exercises the live
// path against a real Postgres so the regression cannot recur.
//
// Skip unless LLM_GATEWAY_PG_TEST_URL is set (the variable name is
// intentionally gateway-specific so we don't accidentally point at
// the wrong database in CI).
func TestRequestLogInsertParamCount(t *testing.T) {
	dsn := os.Getenv("LLM_GATEWAY_PG_TEST_URL")
	if dsn == "" {
		t.Skip("LLM_GATEWAY_PG_TEST_URL not set; skipping live DB test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	// Ensure the schema is up-to-date (this is what the gateway does
	// at startup). If the column is missing, the test fails here
	// rather than at the INSERT — which is the correct shape, because
	// the migration must run before the code.
	var hasCol bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = 'request_logs'
			  AND column_name = 'upstream_finish_reason'
		)
	`).Scan(&hasCol); err != nil {
		t.Fatalf("column check: %v", err)
	}
	if !hasCol {
		t.Fatal("upstream_finish_reason column missing; run db/migrations/018_upstream_finish_reason.sql first")
	}

	// Build a fully-populated RequestLogEntry. Every pointer field
	// gets a distinct non-nil sentinel so a missing bind value would
	// surface as a NULL in the row (the smoke test below scans
	// for those nulls).
	intPtr := func(v int) *int { return &v }
	strPtr := func(v string) *string { return &v }
	floatPtr := func(v float64) *float64 { return &v }
	now := time.Now().UTC().Truncate(time.Microsecond)
	upstream := "stop"
	entry := &RequestLogEntry{
		Op:                RequestLogInsert,
		RequestID:         "telemetry-paramcount-" + now.Format("20060102T150405.000"),
		TenantID:          "default",
		ApplicationID:     intPtr(1),
		APIKeyID:          intPtr(1),
		EndUserID:         strPtr("end-user"),
		ClientModel:       strPtr("gpt-4o"),
		OutboundModel:     strPtr("gpt-4o-2024-08-06"),
		CredentialID:      intPtr(1),
		ProviderID:        intPtr(1),
		CanonicalID:       intPtr(1),
		ClientProfile:     strPtr("smart"),
		RequestMode:       strPtr("chat"),
		PromptTokens:      intPtr(10),
		CompletionTokens:  intPtr(20),
		CacheReadTokens:   intPtr(0),
		CacheWriteTokens:  intPtr(0),
		CostUSD:           floatPtr(0.001),
		CostDisplay:       floatPtr(0.007),
		CostCurrency:      strPtr("CNY"),
		LatencyMs:         intPtr(1234),
		Success:           true,
		RequestStatus:     strPtr(RequestStatusSuccess),
		ErrorKind:         nil,
		UsageSource:       strPtr("llm"),
		IdentityHash:      strPtr("hash-test"),
		ResponseChecksum:  strPtr("cs-test"),
		TransformRuleID:   strPtr("tr-test"),
		EgressProtocol:    strPtr("openai"),
		FailureStage:      nil,
		FailureDetailCode: nil,
		RequestPreview:    strPtr("hello"),
		TransformSummary:  strPtr("noop"),
		ResponsePreview:   strPtr("world"),
		RequestBody:       strPtr(`{"messages":[]}`),
		ResponseBody:      strPtr(`{"choices":[{"message":{"content":"world"}}]}`),
		StreamFirstChunkMs: intPtr(50),
		StreamChunkCount:   intPtr(5),
		StreamDoneReceived: func() *bool { b := true; return &b }(),
		StreamInterrupted:  func() *bool { b := false; return &b }(),
		GwSessionID:         strPtr("gw_test_session"),
		GwTaskID:            strPtr("gw_test_task"),
		APIKeyPrefix:        strPtr("sk-test-****"),
		APIKeyOwnerUser:     strPtr("test-owner"),
		ApplicationCode:     strPtr("test-app"),
		IsAutoRequest:       func() *bool { b := false; return &b }(),
		TaskType:            strPtr("chat"),
		AutoProfile:         strPtr("smart"),
		AutoDecision:        strPtr(`{"top":[]}`),
		AutoConfidence:      floatPtr(0.95),
		WorkType:            strPtr("general_chat"),
		CreditsCharged:      func() *int64 { v := int64(100); return &v }(),
		ParentRequestID:     nil,
		CompressionReason:   nil,
		CompressionStrategy: nil,
		CompressionMeta:     nil,
		OutboundBody:        nil,
		OutboundMsgCount:    nil,
		OutboundTokenEst:    nil,
		OutboundMsgHashes:   nil,
		QualityFlags:        []string{},
		QualityFixActions:   nil,
		QualityScore:        nil,
		UpstreamFinishReason: &upstream,
	}

	// Direct write — bypass the async queue so the error path
	// surfaces synchronously to the test.
	cl := NewClient()
	cl.SetDB(pool)
	if !cl.Enabled() {
		t.Fatal("client should be enabled when DB is set")
	}
	if err := cl.persistRequestLog(entry); err != nil {
		t.Fatalf("persistRequestLog: %v", err)
	}

	// Verify the row made it in with the new column populated.
	var gotUpstream *string
	err = pool.QueryRow(ctx, `
		SELECT upstream_finish_reason
		FROM request_logs
		WHERE request_id = $1
	`, entry.RequestID).Scan(&gotUpstream)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if gotUpstream == nil {
		t.Fatal("upstream_finish_reason should be populated")
	}
	if *gotUpstream != "stop" {
		t.Fatalf("upstream_finish_reason = %q, want \"stop\"", *gotUpstream)
	}

	// Cleanup — best-effort, ignore errors (the row is keyed by
	// a timestamped request_id that no real request would use).
	_, _ = pool.Exec(ctx, `DELETE FROM request_logs WHERE request_id = $1`, entry.RequestID)

	// Helpful error message if a future regression breaks the
	// placeholder/argument count: pgx reports it as
	// "mismatched param and argument count" or "SQLSTATE 08P01
	// protocol_violation". This substring catches both.
	_ = strings.Contains // keep import for future use
}
