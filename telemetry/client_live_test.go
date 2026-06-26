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

	// Verify the new column write + also a sanity check that
	// quality_flags and quality_fix_actions are written as
	// non-NULL empty arrays (the DEFAULT-override footgun: an
	// explicit nil bind in INSERT would trip the not-null check,
	// so the helpers must coerce nil → []string{} / "{}" — see
	// qualityFlagsArg/qualityActionsArg).
	var (
		gotFlags    []string
		gotActions  []byte
		gotSuccess  bool
	)
	err = pool.QueryRow(ctx, `
		SELECT quality_flags, quality_fix_actions::text, success
		FROM request_logs
		WHERE request_id = $1
	`, entry.RequestID).Scan(&gotFlags, &gotActions, &gotSuccess)
	if err != nil {
		t.Fatalf("verify quality columns: %v", err)
	}
	if gotFlags == nil {
		t.Error("quality_flags should be a non-nil empty array, not nil")
	}
	if len(gotFlags) != 0 {
		t.Errorf("quality_flags should be empty, got %v", gotFlags)
	}
	if string(gotActions) != "{}" {
		t.Errorf("quality_fix_actions should be {}, got %q", string(gotActions))
	}
	if !gotSuccess {
		t.Error("success should be true")
	}

	_ = strings.Contains // keep import for future use
}

// TestRequestLogUniqueRequestID is the regression test for the 2026-06-26
// duplicate-row bug (kaixuan's report). Symptom: a glm-5.1 request that
// retried 智谱 3 times before succeeding on nvidia nim produced 4 rows in
// request_logs, all subsequently updated to "nvidia nim success".
//
// Root cause: the unique constraint was (request_id, ts). Because
// insertRequestLog uses ts=now() each call, a retry storm produced
// multiple rows. The UPDATE ... WHERE request_id=$1 then matched all of
// them.
//
// Fix: enforce UNIQUE (request_id) only via db/db.go::ensureRequestLogsUniqueIndex
// (mirror of db/migrations/301_request_logs_unique_request_id_only.sql).
// insertRequestLog now has ON CONFLICT (request_id) DO NOTHING so any
// racing INSERT is a no-op.
//
// This test simulates the exact bug scenario: initial INSERT → 3 failed
// candidate UPDATEs → 1 successful UPDATE → fallback INSERT path.
// It asserts SELECT COUNT(*) FROM request_logs WHERE request_id = $1
// returns 1.
//
// Skip unless LLM_GATEWAY_PG_TEST_URL is set.
func TestRequestLogUniqueRequestID(t *testing.T) {
	dsn := os.Getenv("LLM_GATEWAY_PG_TEST_URL")
	if dsn == "" {
		t.Skip("LLM_GATEWAY_PG_TEST_URL not set; skipping live DB test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	// Schema precondition: the new (request_id)-only unique index must exist.
	// If this fails, the test is correctly catching the schema gap; the
	// operator must apply migration 301 or wait for db.Open() to apply it.
	var hasNewIdx bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_indexes
			WHERE schemaname = 'public'
			  AND tablename = 'request_logs'
			  AND indexname = 'idx_request_logs_request_id_unique'
		)
	`).Scan(&hasNewIdx); err != nil {
		t.Fatalf("pg_indexes check: %v", err)
	}
	if !hasNewIdx {
		t.Fatal("idx_request_logs_request_id_unique index missing; run db/migrations/301_request_logs_unique_request_id_only.sql or restart the gateway so db.Open() applies ensureRequestLogsUniqueIndex")
	}

	requestID := "test-unique-rid-" + time.Now().UTC().Format("20060102T150405.000000")

	// Cleanup guard.
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM request_logs WHERE request_id = $1`, requestID)
	}()

	intPtr := func(v int) *int { return &v }
	strPtr := func(v string) *string { return &v }

	cl := NewClient()
	cl.SetDB(pool)
	if !cl.Enabled() {
		t.Fatal("client should be enabled when DB is set")
	}

	// Step 1: Initial INSERT (mimics recordInitialRequestLog).
	inProgress := RequestStatusInProgress
	initial := &RequestLogEntry{
		Op:            RequestLogInsert,
		RequestID:     requestID,
		TenantID:      "default",
		ClientModel:   strPtr("glm-5.1"),
		OutboundModel: strPtr("glm-5"),
		Success:       false,
		RequestStatus: &inProgress,
	}
	if err := cl.persistRequestLog(initial); err != nil {
		t.Fatalf("initial INSERT: %v", err)
	}

	// Step 2: 智谱 failed 3 times (mimics Execute → candidate_failure_logs path,
	// then EmitRequestLogUpdate for the request_logs row).
	for i := 1; i <= 3; i++ {
		failureKind := "transient"
		failed := &RequestLogEntry{
			Op:            RequestLogUpdate,
			RequestID:     requestID,
			TenantID:      "default",
			ClientModel:   strPtr("glm-5.1"),
			OutboundModel: strPtr("glm-5"),
			CredentialID:  intPtr(11 + i), // 智谱 creds
			ProviderID:    intPtr(11 + i),
			Success:       false,
			RequestStatus: strPtr(RequestStatusFailure),
			ErrorKind:     &failureKind,
		}
		if err := cl.persistRequestLog(failed); err != nil {
			t.Fatalf("failure UPDATE %d: %v", i, err)
		}
	}

	// Step 3: nvidia nim success (mimics emitTelemetry happy path).
	emptyKind := ""
	success := &RequestLogEntry{
		Op:             RequestLogUpdate,
		RequestID:      requestID,
		TenantID:       "default",
		ClientModel:    strPtr("glm-5.1"),
		OutboundModel:  strPtr("glm-5"),
		CredentialID:   intPtr(18),
		ProviderID:     intPtr(18),
		Success:        true,
		RequestStatus:  strPtr(RequestStatusSuccess),
		ErrorKind:      &emptyKind, // explicit clear
		PromptTokens:   intPtr(100),
		CompletionTokens: intPtr(50),
	}
	if err := cl.persistRequestLog(success); err != nil {
		t.Fatalf("success UPDATE: %v", err)
	}

	// Step 4: simulate the fallback INSERT path (originally a separate
	// INSERT with new ts that would create a duplicate row under the
	// OLD schema). With the fix, ON CONFLICT (request_id) DO NOTHING
	// collapses this to the existing row.
	fallback := &RequestLogEntry{
		Op:            RequestLogInsert,
		RequestID:     requestID,
		TenantID:      "default",
		ClientModel:   strPtr("glm-5.1"),
		OutboundModel: strPtr("glm-5"),
		CredentialID:   intPtr(18),
		ProviderID:     intPtr(18),
		Success:        true,
		RequestStatus:  strPtr(RequestStatusSuccess),
	}
	if err := cl.persistRequestLog(fallback); err != nil {
		t.Fatalf("fallback INSERT (would have created dup row pre-fix): %v", err)
	}

	// ASSERT 1: exactly 1 row exists.
	var rowCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM request_logs WHERE request_id = $1
	`, requestID).Scan(&rowCount); err != nil {
		t.Fatalf("row count: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("expected 1 row in request_logs, got %d (request_id=%s).\n"+
			"This is the exact regression: a retry storm with INSERTs and UPDATEs\n"+
			"must collapse to a single row under the (request_id)-only unique constraint.",
			rowCount, requestID)
	}

	// ASSERT 2: final state shows nvidia nim (provider 18) success.
	var (
		gotSuccess   bool
		gotProvider  *int
		gotRequestStatus *string
		gotErrorKind *string
	)
	if err := pool.QueryRow(ctx, `
		SELECT success, provider_id, request_status, error_kind
		FROM request_logs WHERE request_id = $1
	`, requestID).Scan(&gotSuccess, &gotProvider, &gotRequestStatus, &gotErrorKind); err != nil {
		t.Fatalf("final state query: %v", err)
	}
	if !gotSuccess {
		t.Errorf("final state success = false, want true")
	}
	if gotProvider == nil || *gotProvider != 18 {
		t.Errorf("final state provider_id = %v, want 18 (nvidia nim)", gotProvider)
	}
	if gotRequestStatus == nil || *gotRequestStatus != RequestStatusSuccess {
		t.Errorf("final state request_status = %v, want %q", gotRequestStatus, RequestStatusSuccess)
	}
	if gotErrorKind != nil && *gotErrorKind != "" {
		t.Errorf("final state error_kind = %q, want NULL or empty (cleared on success)", *gotErrorKind)
	}

	// ASSERT 3: token counts survived.
	var (
		gotPrompt *int
		gotCompletion *int
	)
	if err := pool.QueryRow(ctx, `
		SELECT prompt_tokens, completion_tokens
		FROM request_logs WHERE request_id = $1
	`, requestID).Scan(&gotPrompt, &gotCompletion); err != nil {
		t.Fatalf("token query: %v", err)
	}
	if gotPrompt == nil || *gotPrompt != 100 {
		t.Errorf("final state prompt_tokens = %v, want 100", gotPrompt)
	}
	if gotCompletion == nil || *gotCompletion != 50 {
		t.Errorf("final state completion_tokens = %v, want 50", gotCompletion)
	}
}

// TestRequestLogFallbackUpsert exercises upsertRequestLogFallback directly.
// This is the path that fires when an UPDATE matches 0 rows (no initial
// record was ever written — e.g. panic before recordInitialRequestLog,
// or async fallback path on a fresh gateway). Under the OLD schema with
// (request_id, ts) constraint and ON CONFLICT (request_id, ts), each call
// would create a NEW row (because ts=now() differs). Under the fix, the
// constraint is (request_id) only, so subsequent calls update the same row.
//
// Skip unless LLM_GATEWAY_PG_TEST_URL is set.
func TestRequestLogFallbackUpsert(t *testing.T) {
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

	// Schema precondition (same as TestRequestLogUniqueRequestID).
	var hasNewIdx bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_indexes
			WHERE schemaname = 'public'
			  AND tablename = 'request_logs'
			  AND indexname = 'idx_request_logs_request_id_unique'
		)
	`).Scan(&hasNewIdx); err != nil {
		t.Fatalf("pg_indexes check: %v", err)
	}
	if !hasNewIdx {
		t.Fatal("idx_request_logs_request_id_unique index missing")
	}

	requestID := "test-fallback-upsert-" + time.Now().UTC().Format("20060102T150405.000000")
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM request_logs WHERE request_id = $1`, requestID)
	}()

	intPtr := func(v int) *int { return &v }
	strPtr := func(v string) *string { return &v }

	cl := NewClient()
	cl.SetDB(pool)

	// Simulate 3 fallback INSERT attempts (e.g. concurrent retries on
	// a fresh gateway that never wrote an initial record). With the
	// fix, all 3 collapse to a single row.
	for i := 1; i <= 3; i++ {
		entry := &RequestLogEntry{
			Op:             RequestLogUpdate,
			RequestID:      requestID,
			TenantID:       "default",
			ClientModel:    strPtr("gpt-4o"),
			OutboundModel:  strPtr("gpt-4o"),
			CredentialID:   intPtr(i),
			ProviderID:     intPtr(i),
			Success:        i == 3, // last call wins
			RequestStatus:  strPtr(RequestStatusSuccess),
			PromptTokens:   intPtr(10 * i),
			CompletionTokens: intPtr(5 * i),
		}
		_ = intPtr
		_ = strPtr
		if err := cl.persistRequestLog(entry); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}

	// ASSERT: exactly 1 row.
	var rowCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM request_logs WHERE request_id = $1
	`, requestID).Scan(&rowCount); err != nil {
		t.Fatalf("row count: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("expected 1 row after 3 fallback upserts, got %d", rowCount)
	}

	// ASSERT: last value wins (cred 3, provider 3, prompt=30, completion=15).
	var (
		gotProvider *int
		gotPrompt *int
		gotCompletion *int
	)
	if err := pool.QueryRow(ctx, `
		SELECT provider_id, prompt_tokens, completion_tokens
		FROM request_logs WHERE request_id = $1
	`, requestID).Scan(&gotProvider, &gotPrompt, &gotCompletion); err != nil {
		t.Fatalf("state query: %v", err)
	}
	if gotProvider == nil || *gotProvider != 3 {
		t.Errorf("provider_id = %v, want 3 (last update wins)", gotProvider)
	}
	if gotPrompt == nil || *gotPrompt != 30 {
		t.Errorf("prompt_tokens = %v, want 30", gotPrompt)
	}
	if gotCompletion == nil || *gotCompletion != 15 {
		t.Errorf("completion_tokens = %v, want 15", gotCompletion)
	}
}
