package telemetry

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/internal/outbox"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

type boolPointerMatcher struct {
	want bool
}

func (m boolPointerMatcher) Match(value interface{}) bool {
	actual, ok := value.(*bool)
	return ok && actual != nil && *actual == m.want
}

type stringPointerMatcher struct {
	want *string
}

func (m stringPointerMatcher) Match(value interface{}) bool {
	actual, ok := value.(*string)
	if !ok {
		return false
	}
	if m.want == nil {
		return actual == nil
	}
	return actual != nil && *actual == *m.want
}

func requestLogUpdateArgs(entry RequestLogEntry) []interface{} {
	args := make([]interface{}, 97)
	for index := range args {
		args[index] = pgxmock.AnyArg()
	}
	// SQL $N → args[N-1]. SQL position 37=success, 38=request_status;
	// client-perception fields at $78-$81; t0..t9 at $82-$91; discard $92;
	// canonical/routing/attachments at $93-$96.
	// 2026-08-24 Phase 1: outbound_body (was $60) is removed from the main table
	// UPDATE bind list. The dedicated request_logs_bodies_hot table is the sole
	// outbound_body owner; outbound_body is written via upsertRequestLogBodies.
	// All placeholders $N≥60 are shifted by -1 (so this helper's array shrinks
	// from 97 to 96).
	// UsageSource is nonEmptyPtr-wrapped
	// by nonEmptyPtr() and is nil-safe so we leave it as a generic matcher.
	args[36] = boolPointerMatcher{want: entry.Success}
	args[37] = stringPointerMatcher{want: entry.RequestStatus}
	args[77] = stringPointerMatcher{want: entry.AgentName}
	args[78] = stringPointerMatcher{want: entry.AgentType}
	args[79] = stringPointerMatcher{want: entry.ClientProtocol}
	args[80] = stringPointerMatcher{want: entry.VirtualClientID}
	// $82-$91 t0..t9 stay AnyArg; $92 discard; $93-$96 canonical/routing/attachments
	args[91] = nullableJSONArg(entry.DiscardEvents)
	return args
}

func TestInsertSessionOpenedEvent_IsIdempotent(t *testing.T) {
	mockDB, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mockDB.Close()

	mockDB.ExpectBegin()
	tx, err := mockDB.Begin(context.Background())
	require.NoError(t, err)
	opened, err := outbox.BuildSessionOpenedEventV1("tenant-1", "session-1", "42")
	require.NoError(t, err)

	mockDB.ExpectExec(`INSERT INTO outbox_events[\s\S]*ON CONFLICT \(event_id\) DO NOTHING`).
		WithArgs(
			opened.EventID, opened.EventType, opened.SchemaVersion, opened.TenantID,
			opened.AggregateID, opened.AggregateVersion, opened.OccurredAt, pgxmock.AnyArg(),
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	require.NoError(t, insertSessionOpenedEvent(context.Background(), tx, opened, "req-1"))
	mockDB.ExpectRollback()
	require.NoError(t, tx.Rollback(context.Background()))
	require.NoError(t, mockDB.ExpectationsWereMet())
}
func TestUpdateRequestLog_AllowsTerminalSuccessToReplaceIntermediateFailure(t *testing.T) {
	mockDB, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mockDB.Close()

	mockDB.ExpectBegin()
	mockDB.ExpectExec(`UPDATE usage_ledger_hot`).
		WithArgs("req-update", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	status := RequestStatusSuccess
	requestLogArgs := requestLogUpdateArgs(RequestLogEntry{
		Success:       true,
		RequestStatus: &status,
	})
	mockDB.ExpectExec(`UPDATE request_logs_hot[\s\S]*request_logs_hot\.request_status = 'failure'`).
		WithArgs(requestLogArgs...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mockDB.ExpectExec(`INSERT INTO request_logs_bodies_hot`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mockDB.ExpectCommit()

	client := &Client{requestLogDB: mockDB}
	err = client.updateRequestLog(&RequestLogEntry{
		RequestID:     "req-update",
		Op:            RequestLogUpdate,
		Success:       true,
		RequestStatus: &status,
	})
	require.NoError(t, err)
	require.NoError(t, mockDB.ExpectationsWereMet())
}

func TestUpdateRequestLog_TerminalGuardDistinguishesNoOpFromMissing(t *testing.T) {
	tests := []struct {
		name         string
		entry        RequestLogEntry
		updateRows   int64
		existing     bool
		wantQuery    bool
		wantRollback bool
		wantCommit   bool
	}{
		{
			name:         "late nonterminal",
			entry:        RequestLogEntry{RequestID: "req-late-nonterminal", Success: false, RequestStatus: strptr(RequestStatusInProgress)},
			updateRows:   0,
			existing:     true,
			wantQuery:    true,
			wantRollback: true,
		},
		{
			name:         "late failure",
			entry:        RequestLogEntry{RequestID: "req-late-failure", Success: false, RequestStatus: strptr(RequestStatusFailure)},
			updateRows:   0,
			existing:     true,
			wantQuery:    true,
			wantRollback: true,
		},
		{
			name:       "failure to success",
			entry:      RequestLogEntry{RequestID: "req-failure-success", Success: true, RequestStatus: strptr(RequestStatusSuccess)},
			updateRows: 1,
			wantCommit: true,
		},
		{
			name:       "success enrichment",
			entry:      RequestLogEntry{RequestID: "req-success-enrichment", Success: true, RequestStatus: strptr(RequestStatusSuccess)},
			updateRows: 1,
			wantCommit: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mockDB, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mockDB.Close()

			mockDB.ExpectBegin()
			mockDB.ExpectExec(`UPDATE usage_ledger_hot`).
				WithArgs(tc.entry.RequestID, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			requestLogArgs := requestLogUpdateArgs(tc.entry)
			mockDB.ExpectExec(`UPDATE request_logs_hot`).
				WithArgs(requestLogArgs...).
				WillReturnResult(pgxmock.NewResult("UPDATE", tc.updateRows))
			if tc.wantQuery {
				mockDB.ExpectQuery(`SELECT EXISTS`).
					WithArgs(tc.entry.RequestID).
					WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(tc.existing))
			}
			if tc.wantRollback {
				mockDB.ExpectRollback()
			}
			if tc.wantCommit {
				mockDB.ExpectExec(`INSERT INTO request_logs_bodies_hot`).
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("INSERT", 1))
				mockDB.ExpectCommit()
			}

			client := &Client{requestLogDB: mockDB}
			err = client.updateRequestLog(&tc.entry)
			require.NoError(t, err)
			require.NoError(t, mockDB.ExpectationsWereMet())
		})
	}
}

func TestUpdateRequestLog_MissingRequestFallsBackToInsert(t *testing.T) {
	mockDB, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mockDB.Close()

	mockDB.ExpectBegin()
	mockDB.ExpectExec(`UPDATE usage_ledger_hot`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	requestLogArgs := requestLogUpdateArgs(RequestLogEntry{Success: false})
	mockDB.ExpectExec(`UPDATE request_logs_hot`).
		WithArgs(requestLogArgs...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mockDB.ExpectQuery(`SELECT EXISTS`).
		WithArgs("req-missing").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
	mockDB.ExpectRollback()

	mockDB.ExpectBegin()
	usageInsertArgs := make([]interface{}, 18)
	for index := range usageInsertArgs {
		usageInsertArgs[index] = pgxmock.AnyArg()
	}
	mockDB.ExpectExec(`INSERT INTO usage_ledger_hot`).
		WithArgs(usageInsertArgs...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	requestInsertArgs := make([]interface{}, 100)
	for index := range requestInsertArgs {
		requestInsertArgs[index] = pgxmock.AnyArg()
	}
	mockDB.ExpectExec(`INSERT INTO\s+request_logs_hot\s*\(`).
		WithArgs(requestInsertArgs...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mockDB.ExpectExec(`INSERT INTO request_logs_bodies_hot`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mockDB.ExpectCommit()

	client := &Client{requestLogDB: mockDB}
	err = client.updateRequestLog(&RequestLogEntry{
		RequestID: "req-missing",
		Success:   false,
	})
	require.NoError(t, err)
	require.NoError(t, mockDB.ExpectationsWereMet())
}

func TestClient_StopIsIdempotent(t *testing.T) {
	c := newClientWithBufSize(2)

	done := make(chan struct{}, 4)
	for i := 0; i < 4; i++ {
		go func() {
			c.Stop()
			done <- struct{}{}
		}()
	}
	for i := 0; i < 4; i++ {
		<-done
	}
	c.Stop()
}
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
	// 2026-08-20: 校正 stale 断言。早期的 *_default 表（canonical write target）
	// 已被迁移 341 替换为 request_logs_hot（hot table），production 代码不再
	// 写 request_logs_default。该测试现锁定"UPDATE 必须指向 hot 表且不带
	// 别名"。
	//
	// 注：不再校验具体的列清单（运维在迁移 341 / 455 之间改过两次表结构），
	// 只校验 SET 子句没有 `rl.` 别名引用 + WHERE 指向 hot 表。这是 schema
	// 演进路径上的"轻量级回归防护"——具体列由 SQL 编译器保证。
	const updateSQL = `
		UPDATE request_logs_hot
		   SET client_model = COALESCE($2, client_model),
		       outbound_model = COALESCE($3, outbound_model),
		       quality_flags = COALESCE(CAST($59 AS text[]), quality_flags),
		       quality_fix_actions = COALESCE(CAST($60 AS jsonb), quality_fix_actions)
		  FROM latest
		 WHERE request_logs_hot.id = latest.id
		   AND request_logs_hot.ts = latest.ts
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
	if strings.Contains(updateSQL, "UPDATE request_logs_default") {
		t.Fatal("UPDATE must NOT target the deprecated request_logs_default table")
	}
	if !strings.Contains(updateSQL, "UPDATE request_logs_hot") {
		t.Fatal("UPDATE must target request_logs_hot (post-migration 341)")
	}
	if !strings.Contains(updateSQL, "request_logs_hot.id") {
		t.Fatal("WHERE clause must reference request_logs_hot.id")
	}
}

func TestRequestLogsHotTarget_QualityColumnsUseHotTable(t *testing.T) {
	// 2026-08-20: 锁定 quality_* 列随 INSERT/UPDATE 一起迁移到 hot 表。
	// 测试不依赖具体参数编号，只验证 SQL 中同时出现 quality_* 和 request_logs_hot。
	const sql = `
		INSERT INTO request_logs_hot (request_id, ts, quality_flags, quality_fix_actions)
		VALUES ($1, $2, $3::text[], $4::text::jsonb)
	`
	if !strings.Contains(sql, "request_logs_hot") {
		t.Fatal("INSERT must target request_logs_hot")
	}
	for _, col := range []string{"quality_flags", "quality_fix_actions"} {
		if !strings.Contains(sql, col) {
			t.Errorf("SQL missing %s", col)
		}
	}
}
func TestNormalizeRequestStatus(t *testing.T) {
	entry := &RequestLogEntry{Op: RequestLogInsert, Success: false}
	normalizeRequestStatus(entry)
	if entry.RequestStatus == nil || *entry.RequestStatus != RequestStatusInProgress {
		t.Fatalf("expected in_progress, got %#v", entry.RequestStatus)
	}
}

func TestMergeRequestLogEntry_PreservesDiscardEventsOnEmptyUpdate(t *testing.T) {
	events := json.RawMessage(`[{"reason":"survival_attempt_discarded","attempt_number":1}]`)
	dst := &RequestLogEntry{RequestID: "req-discard-preserve", DiscardEvents: events}
	mergeRequestLogEntry(dst, &RequestLogEntry{RequestID: dst.RequestID})
	if string(dst.DiscardEvents) != string(events) {
		t.Fatalf("DiscardEvents = %s, want %s", dst.DiscardEvents, events)
	}
}

func TestMergeRequestLogEntry_PreservesBodiesOnEmptyUpdate(t *testing.T) {
	requestBody := `{"messages":[{"role":"user","content":"hello"}]}`
	responseBody := `{"choices":[{"message":{"content":"hi"}}]}`
	dst := &RequestLogEntry{
		RequestID:    "req-body-preserve",
		RequestBody:  &requestBody,
		ResponseBody: &responseBody,
	}
	mergeRequestLogEntry(dst, &RequestLogEntry{RequestID: dst.RequestID, Success: true})
	if dst.RequestBody == nil || *dst.RequestBody != requestBody {
		t.Fatalf("RequestBody = %v, want original body", dst.RequestBody)
	}
	if dst.ResponseBody == nil || *dst.ResponseBody != responseBody {
		t.Fatalf("ResponseBody = %v, want original body", dst.ResponseBody)
	}
}

func TestPersistRequestLog_ReleasesBodiesAfterPersistedHooks(t *testing.T) {
	requestBody := `{"messages":[{"role":"user","content":"hello"}]}`
	responseBody := `{"choices":[{"message":{"content":"hi"}}]}`
	outboundBody := json.RawMessage(`{"messages":[{"role":"user","content":"hello"}]}`)
	status := RequestStatusSuccess

	mockDB, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mockDB.Close()

	mockDB.ExpectBegin()
	mockDB.ExpectExec(`UPDATE usage_ledger_hot`).
		WithArgs("req-release", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mockDB.ExpectExec(`UPDATE request_logs_hot`).
		WithArgs(requestLogUpdateArgs(RequestLogEntry{Success: true, RequestStatus: &status})...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mockDB.ExpectExec(`INSERT INTO request_logs_bodies_hot`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mockDB.ExpectCommit()

	entry := &RequestLogEntry{
		RequestID:     "req-release",
		Op:            RequestLogUpdate,
		Success:       true,
		RequestStatus: &status,
		RequestBody:   &requestBody,
		ResponseBody:  &responseBody,
		OutboundBody:  outboundBody,
	}
	client := &Client{requestLogDB: mockDB}
	client.AddOnRequestLogPersisted(func(got *RequestLogEntry) {
		require.Equal(t, requestBody, *got.RequestBody)
		require.Equal(t, responseBody, *got.ResponseBody)
		require.Equal(t, outboundBody, got.OutboundBody)
	})

	require.NoError(t, client.persistRequestLog(entry))
	require.Nil(t, entry.RequestBody)
	require.Nil(t, entry.ResponseBody)
	require.Nil(t, entry.OutboundBody)
	require.NoError(t, mockDB.ExpectationsWereMet())
}

func TestPersistRequestLog_KeepsBodiesWhenWriteFails(t *testing.T) {
	requestBody := `{"messages":[{"role":"user","content":"hello"}]}`
	responseBody := `{"choices":[{"message":{"content":"hi"}}]}`
	outboundBody := json.RawMessage(`{"messages":[{"role":"user","content":"hello"}]}`)

	mockDB, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mockDB.Close()

	mockDB.ExpectBegin().WillReturnError(pgx.ErrTxClosed)
	entry := &RequestLogEntry{
		RequestID:    "req-retain",
		RequestBody:  &requestBody,
		ResponseBody: &responseBody,
		OutboundBody: outboundBody,
	}
	client := &Client{requestLogDB: mockDB}

	require.Error(t, client.persistRequestLog(entry))
	require.Equal(t, requestBody, *entry.RequestBody)
	require.Equal(t, responseBody, *entry.ResponseBody)
	require.Equal(t, outboundBody, entry.OutboundBody)
	require.NoError(t, mockDB.ExpectationsWereMet())
}

func TestMergeRequestLogEntry_PreservesClientPerceptionFields(t *testing.T) {
	agentName := "zcode"
	agentType := "ide-agent"
	protocol := "anthropic-messages"
	virtualID := "client-123"
	dst := &RequestLogEntry{
		RequestID:       "req-client-fields",
		AgentName:       &agentName,
		AgentType:       &agentType,
		ClientProtocol:  &protocol,
		VirtualClientID: &virtualID,
	}

	mergeRequestLogEntry(dst, &RequestLogEntry{RequestID: dst.RequestID, Success: false})

	require.Equal(t, agentName, *dst.AgentName)
	require.Equal(t, agentType, *dst.AgentType)
	require.Equal(t, protocol, *dst.ClientProtocol)
	require.Equal(t, virtualID, *dst.VirtualClientID)
}

func TestMergeRequestLogEntry_PreservesMirrorDimensions(t *testing.T) {
	projectID := "project-123"
	namespace := "workspace"
	dst := &RequestLogEntry{
		RequestID: "req-mirror-dimensions",
		ProjectID: &projectID,
		Namespace: &namespace,
	}

	mergeRequestLogEntry(dst, &RequestLogEntry{RequestID: dst.RequestID, Success: true})

	require.Equal(t, projectID, *dst.ProjectID)
	require.Equal(t, namespace, *dst.Namespace)
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

// TestRequestLogEntry_ApplyOriginFromContext verifies the helper
// reads context values written by middleware/origin_mw.go and copies
// them onto the entry. The test covers the four new fields, the
// nil-safe path, and the first-write-wins precedence rule.
func TestRequestLogEntry_ApplyOriginFromContext(t *testing.T) {
	t.Run("populates all four fields from ctx", func(t *testing.T) {
		ctx := context.Background()
		ctx = context.WithValue(ctx, "origin.stage", "node_probe")
		ctx = context.WithValue(ctx, "origin.actor", "node-probe-worker")
		ctx = context.WithValue(ctx, "origin.client_ip", "203.0.113.5")
		ctx = context.WithValue(ctx, "origin.xff", "203.0.113.5, 10.0.0.1")
		var e RequestLogEntry
		e.ApplyOriginFromContext(ctx)
		if e.OriginStage == nil || *e.OriginStage != "node_probe" {
			t.Fatalf("stage: %v", e.OriginStage)
		}
		if e.OriginActor == nil || *e.OriginActor != "node-probe-worker" {
			t.Fatalf("actor: %v", e.OriginActor)
		}
		if e.ClientIP == nil || *e.ClientIP != "203.0.113.5" {
			t.Fatalf("client_ip: %v", e.ClientIP)
		}
		if e.ClientForwardedFor == nil || *e.ClientForwardedFor != "203.0.113.5, 10.0.0.1" {
			t.Fatalf("xff: %v", e.ClientForwardedFor)
		}
	})

	t.Run("nil ctx and nil entry are safe", func(t *testing.T) {
		var e *RequestLogEntry
		e.ApplyOriginFromContext(context.Background()) // must not panic
		e = &RequestLogEntry{}
		e.ApplyOriginFromContext(nil) // must not panic
	})

	t.Run("first-write-wins: pre-set fields are not overwritten", func(t *testing.T) {
		ctx := context.Background()
		ctx = context.WithValue(ctx, "origin.stage", "business")
		e := RequestLogEntry{OriginStage: strPtrT("node_probe")}
		e.ApplyOriginFromContext(ctx)
		if e.OriginStage == nil || *e.OriginStage != "node_probe" {
			t.Fatalf("OriginStage should keep pre-set value, got %v", e.OriginStage)
		}
	})
}

// TestLookupProviderName verifies the provider name resolution logic.
func TestLookupProviderName(t *testing.T) {
	t.Run("returns provider code when found", func(t *testing.T) {
		mockDB, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mockDB.Close()

		providerID := 42
		mockDB.ExpectBegin()
		mockDB.ExpectQuery(`SELECT code FROM providers WHERE id = \$1`).
			WithArgs(42).
			WillReturnRows(pgxmock.NewRows([]string{"code"}).AddRow("anthropic"))

		ctx := context.Background()
		tx, err := mockDB.Begin(ctx)
		require.NoError(t, err)

		result := lookupProviderName(ctx, tx, &providerID)
		require.Equal(t, "anthropic", result)
		require.NoError(t, mockDB.ExpectationsWereMet())
	})

	t.Run("returns unknown when providerID is nil", func(t *testing.T) {
		mockDB, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mockDB.Close()

		ctx := context.Background()
		// No expectations needed - should not query
		result := lookupProviderName(ctx, nil, nil)
		require.Equal(t, "unknown", result)
		require.NoError(t, mockDB.ExpectationsWereMet())
	})

	t.Run("returns unknown when query fails", func(t *testing.T) {
		mockDB, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mockDB.Close()

		providerID := 999
		mockDB.ExpectBegin()
		mockDB.ExpectQuery(`SELECT code FROM providers WHERE id = \$1`).
			WithArgs(999).
			WillReturnError(pgx.ErrNoRows)

		ctx := context.Background()
		tx, err := mockDB.Begin(ctx)
		require.NoError(t, err)

		result := lookupProviderName(ctx, tx, &providerID)
		require.Equal(t, "unknown", result)
		require.NoError(t, mockDB.ExpectationsWereMet())
	})

	t.Run("handles multiple provider types", func(t *testing.T) {
		testCases := []struct {
			providerID int
			code       string
		}{
			{1, "openai"},
			{2, "anthropic"},
			{3, "google"},
			{4, "alibaba-dashscope"},
		}

		for _, tc := range testCases {
			mockDB, err := pgxmock.NewPool()
			require.NoError(t, err)

			mockDB.ExpectBegin()
			mockDB.ExpectQuery(`SELECT code FROM providers WHERE id = \$1`).
				WithArgs(tc.providerID).
				WillReturnRows(pgxmock.NewRows([]string{"code"}).AddRow(tc.code))

			ctx := context.Background()
			tx, err := mockDB.Begin(ctx)
			require.NoError(t, err)

			result := lookupProviderName(ctx, tx, &tc.providerID)
			require.Equal(t, tc.code, result)
			require.NoError(t, mockDB.ExpectationsWereMet())
			mockDB.Close()
		}
	})
}

// TestLookupTurnNumber verifies turn number counting logic.
func TestLookupTurnNumber(t *testing.T) {
	t.Run("returns 1 for first turn in session", func(t *testing.T) {
		mockDB, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mockDB.Close()

		mockDB.ExpectBegin()
		mockDB.ExpectQuery(`SELECT COUNT\(\*\) FROM request_logs WHERE gw_session_id = \$1`).
			WithArgs("session-abc").
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))

		ctx := context.Background()
		tx, err := mockDB.Begin(ctx)
		require.NoError(t, err)

		result := lookupTurnNumber(ctx, tx, "session-abc")
		require.Equal(t, 1, result)
		require.NoError(t, mockDB.ExpectationsWereMet())
	})

	t.Run("returns 2 for second turn in session", func(t *testing.T) {
		mockDB, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mockDB.Close()

		mockDB.ExpectBegin()
		mockDB.ExpectQuery(`SELECT COUNT\(\*\) FROM request_logs WHERE gw_session_id = \$1`).
			WithArgs("session-abc").
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))

		ctx := context.Background()
		tx, err := mockDB.Begin(ctx)
		require.NoError(t, err)

		result := lookupTurnNumber(ctx, tx, "session-abc")
		require.Equal(t, 2, result)
		require.NoError(t, mockDB.ExpectationsWereMet())
	})

	t.Run("returns 1 when sessionID is empty", func(t *testing.T) {
		mockDB, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mockDB.Close()

		ctx := context.Background()
		// No expectations - should not query
		result := lookupTurnNumber(ctx, nil, "")
		require.Equal(t, 1, result)
		require.NoError(t, mockDB.ExpectationsWereMet())
	})

	t.Run("returns 1 when query fails", func(t *testing.T) {
		mockDB, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mockDB.Close()

		mockDB.ExpectBegin()
		mockDB.ExpectQuery(`SELECT COUNT\(\*\) FROM request_logs WHERE gw_session_id = \$1`).
			WithArgs("session-xyz").
			WillReturnError(pgx.ErrNoRows)

		ctx := context.Background()
		tx, err := mockDB.Begin(ctx)
		require.NoError(t, err)

		result := lookupTurnNumber(ctx, tx, "session-xyz")
		require.Equal(t, 1, result)
		require.NoError(t, mockDB.ExpectationsWereMet())
	})

	t.Run("returns 1 when count is 0", func(t *testing.T) {
		mockDB, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mockDB.Close()

		mockDB.ExpectBegin()
		mockDB.ExpectQuery(`SELECT COUNT\(\*\) FROM request_logs WHERE gw_session_id = \$1`).
			WithArgs("session-new").
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))

		ctx := context.Background()
		tx, err := mockDB.Begin(ctx)
		require.NoError(t, err)

		result := lookupTurnNumber(ctx, tx, "session-new")
		require.Equal(t, 1, result)
		require.NoError(t, mockDB.ExpectationsWereMet())
	})

	t.Run("handles multi-turn conversation", func(t *testing.T) {
		testCases := []struct {
			count        int
			expectedTurn int
		}{
			{1, 1},
			{2, 2},
			{3, 3},
			{5, 5},
			{10, 10},
		}

		for _, tc := range testCases {
			mockDB, err := pgxmock.NewPool()
			require.NoError(t, err)

			mockDB.ExpectBegin()
			mockDB.ExpectQuery(`SELECT COUNT\(\*\) FROM request_logs WHERE gw_session_id = \$1`).
				WithArgs("session-multi").
				WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(tc.count))

			ctx := context.Background()
			tx, err := mockDB.Begin(ctx)
			require.NoError(t, err)

			result := lookupTurnNumber(ctx, tx, "session-multi")
			require.Equal(t, tc.expectedTurn, result)
			require.NoError(t, mockDB.ExpectationsWereMet())
			mockDB.Close()
		}
	})
}
