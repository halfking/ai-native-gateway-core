package telemetry

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
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
// the wrong database in CI). Falls back to TEST_DATABASE_URL — the
// KEY actually registered in the envs loader — so a local run does
// not require duplicating credentials under a second name.
func TestRequestLogInsertParamCount(t *testing.T) {
	dsn := os.Getenv("LLM_GATEWAY_PG_TEST_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("LLM_GATEWAY_PG_TEST_URL / TEST_DATABASE_URL not set; skipping live DB test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	// Ensure the active hot-table schema is up-to-date before issuing the write.
	var hasCol bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = 'request_logs_hot'
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
	// ⚠ persistRequestLog 成功后调 releaseBodies()，把 entry 上的三件套正文
	// 置为 nil（client.go:1221）。所以下面的断言必须对照**写入前**取下的副本，
	// 绝不能解引用 entry.RequestBody —— 那是空指针 panic。
	//
	// 这个 panic 之前被更早的失败挡住了（那时 SELECT request_body 先报 42703），
	// 所以这条测试**从来没有跑到过这一行**。修好上一层才暴露出来：
	// 一个被上层错误掩盖的下层缺陷，只有真的把它跑通才会现形。
	wantRequestBody := `{"messages":[]}`
	wantResponseBody := `{"choices":[{"message":{"content":"world"}}]}`
	entry := &RequestLogEntry{
		Op:                   RequestLogInsert,
		RequestID:            "telemetry-paramcount-" + now.Format("20060102T150405.000"),
		TenantID:             "default",
		ApplicationID:        intPtr(1),
		APIKeyID:             intPtr(1),
		EndUserID:            strPtr("end-user"),
		ClientModel:          strPtr("gpt-4o"),
		OutboundModel:        strPtr("gpt-4o-2024-08-06"),
		CredentialID:         intPtr(1),
		ProviderID:           intPtr(1),
		CanonicalID:          intPtr(1),
		ClientProfile:        strPtr("smart"),
		RequestMode:          strPtr("chat"),
		PromptTokens:         intPtr(10),
		CompletionTokens:     intPtr(20),
		CacheReadTokens:      intPtr(0),
		CacheWriteTokens:     intPtr(0),
		CostUSD:              floatPtr(0.001),
		CostDisplay:          floatPtr(0.007),
		CostCurrency:         strPtr("CNY"),
		LatencyMs:            intPtr(1234),
		Success:              true,
		RequestStatus:        strPtr(RequestStatusSuccess),
		ErrorKind:            nil,
		UsageSource:          strPtr("llm"),
		IdentityHash:         strPtr("hash-test"),
		ResponseChecksum:     strPtr("cs-test"),
		TransformRuleID:      strPtr("tr-test"),
		EgressProtocol:       strPtr("openai"),
		FailureStage:         nil,
		FailureDetailCode:    nil,
		RequestPreview:       strPtr("hello"),
		TransformSummary:     strPtr("noop"),
		ResponsePreview:      strPtr("world"),
		RequestBody:          strPtr(wantRequestBody),
		ResponseBody:         strPtr(wantResponseBody),
		StreamFirstChunkMs:   intPtr(50),
		StreamChunkCount:     intPtr(5),
		StreamDoneReceived:   func() *bool { b := true; return &b }(),
		StreamInterrupted:    func() *bool { b := false; return &b }(),
		GwSessionID:          strPtr("gw_test_session"),
		GwTaskID:             strPtr("gw_test_task"),
		APIKeyPrefix:         strPtr("sk-test-****"),
		APIKeyOwnerUser:      strPtr("test-owner"),
		ApplicationCode:      strPtr("test-app"),
		IsAutoRequest:        func() *bool { b := false; return &b }(),
		TaskType:             strPtr("chat"),
		AutoProfile:          strPtr("smart"),
		AutoDecision:         strPtr(`{"top":[]}`),
		AutoConfidence:       floatPtr(0.95),
		WorkType:             strPtr("general_chat"),
		CreditsCharged:       func() *int64 { v := int64(100); return &v }(),
		ParentRequestID:      nil,
		CompressionReason:    nil,
		CompressionStrategy:  nil,
		CompressionMeta:      nil,
		OutboundBody:         nil,
		OutboundMsgCount:     nil,
		OutboundTokenEst:     nil,
		OutboundMsgHashes:    nil,
		QualityFlags:         []string{},
		QualityFixActions:    nil,
		QualityScore:         nil,
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

	// R42 修正：t.Cleanup 晚于 defer cancel()/pool.Close() 执行，原写法 ctx
	// 与池双死、三表静默漏行——独立连接 + 失败变红（e83fb6211 同形态）。
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		conn, cerr := pgx.Connect(cctx, dsn)
		if cerr != nil {
			t.Errorf("cleanup: connect to delete %s: %v", entry.RequestID, cerr)
			return
		}
		defer conn.Close(cctx)
		for _, table := range []string{"request_logs_bodies_hot", "request_logs_hot", "usage_ledger_hot"} {
			if _, derr := conn.Exec(cctx, `DELETE FROM `+table+` WHERE request_id = $1`, entry.RequestID); derr != nil {
				t.Errorf("cleanup: delete %s: %v", table, derr)
			}
		}
	})

	// 「主表不得保留完整 body」这条不变式，在 bodies 面拆分（request_logs_bodies）
	// 之后**已经变成结构保证**：`request_body` / `response_body` 两列
	// 根本不在 request_logs_hot 上。所以不能再 SELECT 它们来「断言为 NULL」——
	// 那样这条不变式只是碰巧成立（列没了），一旦有人把列加回来就静默失效。
	// 这里把不变式本身显式钉住：两列必须**不存在**于主表。
	var bodyColsOnMain int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'request_logs_hot'
		  AND column_name IN ('request_body', 'response_body')
	`).Scan(&bodyColsOnMain); err != nil {
		t.Fatalf("body-column presence check: %v", err)
	}
	if bodyColsOnMain != 0 {
		t.Fatalf("request_logs_hot 上有 %d 个完整 body 列（request_body/response_body）—— "+
			"bodies 面拆分被回退了；完整正文必须留在 request_logs_bodies_hot", bodyColsOnMain)
	}

	// 主表在拆分后承载的是**截断 preview**，不是完整正文。entry 里的
	// RequestPreview/ResponsePreview 哨兵值与完整 body 不同，所以这里能真正
	// 分辨「写进去的是 preview」而不是「body 被顺手搬回了主表」。
	var (
		gotUpstream        *string
		gotRequestPreview  *string
		gotResponsePreview *string
	)
	err = pool.QueryRow(ctx, `
		SELECT upstream_finish_reason, request_preview, response_preview
		FROM request_logs_hot
		WHERE request_id = $1
	`, entry.RequestID).Scan(&gotUpstream, &gotRequestPreview, &gotResponsePreview)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if gotUpstream == nil {
		t.Fatal("upstream_finish_reason should be populated")
	}
	if *gotUpstream != "stop" {
		t.Fatalf("upstream_finish_reason = %q, want \"stop\"", *gotUpstream)
	}
	// ⚠ 失败信息必须打**值**。直接 `%v` 一个 *string 只会印出指针地址
	// （实测：`request_preview = 0x62fddf94b980`），对下一个人零信息量 ——
	// 而「信息量为零的失败信息」等于让人重新查一遍。
	deref := func(p *string) string {
		if p == nil {
			return "<nil>"
		}
		return *p
	}
	if gotRequestPreview == nil || *gotRequestPreview != "hello" {
		t.Fatalf("request_preview = %q, want %q（主表只应承载截断 preview）",
			deref(gotRequestPreview), "hello")
	}
	if gotResponsePreview == nil || *gotResponsePreview != "world" {
		t.Fatalf("response_preview = %q, want %q（主表只应承载截断 preview）",
			deref(gotResponsePreview), "world")
	}

	var gotBodies struct {
		RequestBody  string
		ResponseBody string
	}
	err = pool.QueryRow(ctx, `
		SELECT request_body::text, response_body::text
		FROM request_logs_bodies_hot
		WHERE request_id = $1
	`, entry.RequestID).Scan(&gotBodies.RequestBody, &gotBodies.ResponseBody)
	if err != nil {
		t.Fatalf("verify bodies: %v", err)
	}
	require.JSONEq(t, wantRequestBody, gotBodies.RequestBody)
	require.JSONEq(t, wantResponseBody, gotBodies.ResponseBody)

	var joinedRequestBody string
	err = pool.QueryRow(ctx, `
		SELECT rb.request_body::text
		FROM request_logs_hot rl
		JOIN request_logs_bodies_hot rb
		  ON rb.request_id = rl.request_id
		WHERE rl.request_id = $1
	`, entry.RequestID).Scan(&joinedRequestBody)
	if err != nil {
		t.Fatalf("verify metadata/body join: %v", err)
	}
	require.JSONEq(t, wantRequestBody, joinedRequestBody)

	updatedRequestBody := `{"messages":[{"role":"user","content":"updated"}]}`
	updatedResponseBody := `{"choices":[{"message":{"content":"updated"}}]}`
	if err := cl.persistRequestLog(&RequestLogEntry{
		Op:           RequestLogUpdate,
		RequestID:    entry.RequestID,
		RequestBody:  &updatedRequestBody,
		ResponseBody: &updatedResponseBody,
		Success:      true,
	}); err != nil {
		t.Fatalf("persistRequestLog update: %v", err)
	}
	err = pool.QueryRow(ctx, `
		SELECT request_body::text, response_body::text
		FROM request_logs_bodies_hot
		WHERE request_id = $1
	`, entry.RequestID).Scan(&gotBodies.RequestBody, &gotBodies.ResponseBody)
	if err != nil {
		t.Fatalf("verify updated bodies: %v", err)
	}
	require.JSONEq(t, updatedRequestBody, gotBodies.RequestBody)
	require.JSONEq(t, updatedResponseBody, gotBodies.ResponseBody)

	// A metadata-only update must not erase bodies captured by an earlier write.
	if err := cl.persistRequestLog(&RequestLogEntry{
		Op:        RequestLogUpdate,
		RequestID: entry.RequestID,
		Success:   true,
	}); err != nil {
		t.Fatalf("persistRequestLog metadata-only update: %v", err)
	}
	err = pool.QueryRow(ctx, `
		SELECT request_body::text, response_body::text
		FROM request_logs_bodies_hot
		WHERE request_id = $1
	`, entry.RequestID).Scan(&gotBodies.RequestBody, &gotBodies.ResponseBody)
	if err != nil {
		t.Fatalf("verify preserved bodies: %v", err)
	}
	require.JSONEq(t, updatedRequestBody, gotBodies.RequestBody)
	require.JSONEq(t, updatedResponseBody, gotBodies.ResponseBody)

	// Verify the new column write and also a sanity check that
	// quality_flags and quality_fix_actions are written as
	// non-NULL empty arrays (the DEFAULT-override footgun: an
	// explicit nil bind in INSERT would trip the not-null check,
	// so the helpers must coerce nil → []string{} / "{}" — see
	// qualityFlagsArg/qualityActionsArg).
	var (
		gotFlags   []string
		gotActions []byte
		gotSuccess bool
	)
	err = pool.QueryRow(ctx, `
		SELECT quality_flags, quality_fix_actions::text, success
		FROM request_logs_hot
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

}
