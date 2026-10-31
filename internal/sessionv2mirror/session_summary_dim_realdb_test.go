//go:build !integration

package sessionv2mirror

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// session_summary_dim_realdb_test.go — S4 停写交接判据（2026-10-13）。
//
// 为什么必须是真库门：本写入器的全部风险都在 SQL 语义里（增量均值、
// 首值优先、数组去重追加、双载体参数在 INSERT/DO UPDATE 两处的引用），
// mock 只能证明「语句发了」，证明不了「值落进了行」。同族先例见
// turn_writer_double_fire_realdb_test.go 的文件头论证。
//
// 用例组（互为对照，缺一不可）：
//   - A 首行：INSERT 分支落全部聚合列；
//   - B 第二条（成功+失败混合、不同模型/provider/task）：增量语义 ——
//     count 2/1/1、avg=(a*n+b)/(n+1)、min/max、数组去重追加、
//     primary_model 与 gw_task_id 首值不被第二条覆盖；
//   - C ★退役开关不再支配聚合（2026-10-29）：第三条必须照写（count 3）。
//     原断言是「v1 写入开启时 no-op」（双喂防护门），门的前提随 v1 族退役
//     消失 ⇒ 断言翻向。见下方 NoV1TriggerPremise 用例：那是删门的**前提**，
//     前提消失必须**当场可见**，否则删门等于把双喂风险转成静默停摆。
const summaryTestSession = "sess-r1013-summary-selftest"

func summaryTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping real-database session summary proof")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Skipf("TEST_DATABASE_URL unreachable, skipping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// strPtr/intPtr/floatPtr 助手沿用 hook_test.go 的既有声明（同包测试共享）。

func TestSessionSummaryDimWriter_S4HandoffSemantics(t *testing.T) {
	pool := summaryTestPool(t)
	ctx := context.Background()

	cleanup := func() {
		// 双保险：用例自己先 DELETE，失败路径残留也能被下一轮清掉。
		_, _ = pool.Exec(ctx, `DELETE FROM session_summaries WHERE session_key = $1`, summaryTestSession)
	}
	cleanup()
	t.Cleanup(cleanup)

	w := NewSessionSummaryDimWriter(pool)
	ts1 := time.Date(2026, 10, 13, 7, 0, 0, 0, time.Local)
	ts2 := ts1.Add(2 * time.Minute)
	sess := strPtr(summaryTestSession)
	provider := 36 // 本地库在场：vapeur

	// A 首行（成功）。
	if err := w.UpsertSessionDim(ctx, &telemetry.RequestLogEntry{
		RequestID: "r1013-sum-a", GwSessionID: sess, TenantID: "default",
		EventAt: &ts1, Success: true,
		PromptTokens: intPtr(3000), CompletionTokens: intPtr(1000),
		CostUSD: floatPtr(1.0), LatencyMs: intPtr(1000),
		ClientModel: strPtr("gpt-5.6-sol"), OutboundModel: strPtr("gpt-5.6-sol"),
		WorkType: strPtr("coding"), ProviderID: &provider, GwTaskID: strPtr("task-first"),
	}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	var (
		count, ok, fail, avg, minLat, maxLat int
		inCost                               float64
		primary, taskID                      *string
		models                               []string
	)
	row := pool.QueryRow(ctx, `
		SELECT request_count, success_count, error_count, avg_latency_ms,
		       min_latency_ms, max_latency_ms, input_cost_usd::float8,
		       primary_model, gw_task_id, models_used
		FROM session_summaries WHERE session_key = $1`, summaryTestSession)
	if err := row.Scan(&count, &ok, &fail, &avg, &minLat, &maxLat, &inCost, &primary, &taskID, &models); err != nil {
		t.Fatalf("first row scan (insert branch must land): %v", err)
	}
	if count != 1 || ok != 1 || fail != 0 || avg != 1000 || minLat != 1000 || maxLat != 1000 {
		t.Fatalf("A: counts/latency = %d/%d/%d avg=%d min=%d max=%d, want 1/1/0 1000", count, ok, fail, avg, minLat, maxLat)
	}
	// 3000:1000 → ratio 0.75 → input 0.75。
	if inCost < 0.74 || inCost > 0.76 {
		t.Fatalf("A: input_cost = %v, want ~0.75 (token ratio split)", inCost)
	}
	if primary == nil || *primary != "gpt-5.6-sol" || taskID == nil || *taskID != "task-first" {
		t.Fatalf("A: primary/task first-value = %v/%v", primary, taskID)
	}
	if len(models) != 1 || models[0] != "gpt-5.6-sol" {
		t.Fatalf("A: models_used = %v", models)
	}

	// B 第二条（失败、不同模型、不同 task——首值不得被覆盖；avg 增量）。
	if err := w.UpsertSessionDim(ctx, &telemetry.RequestLogEntry{
		RequestID: "r1013-sum-b", GwSessionID: sess, TenantID: "default",
		EventAt: &ts2, Success: false,
		PromptTokens: intPtr(1000), CompletionTokens: intPtr(1000),
		CostUSD: floatPtr(0.5), LatencyMs: intPtr(3000),
		ClientModel: strPtr("minimax-m3"), OutboundModel: strPtr("MiniMax-M3"),
		WorkType: strPtr("coding"), ProviderID: &provider, GwTaskID: strPtr("task-second"),
	}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	row = pool.QueryRow(ctx, `
		SELECT request_count, success_count, error_count, avg_latency_ms,
		       min_latency_ms, max_latency_ms, primary_model, gw_task_id, models_used
		FROM session_summaries WHERE session_key = $1`, summaryTestSession)
	if err := row.Scan(&count, &ok, &fail, &avg, &minLat, &maxLat, &primary, &taskID, &models); err != nil {
		t.Fatalf("second row scan: %v", err)
	}
	if count != 2 || ok != 1 || fail != 1 {
		t.Fatalf("B: counts = %d/%d/%d, want 2/1/1", count, ok, fail)
	}
	// avg = (1000*1 + 3000)/2 = 2000；min=1000 max=3000。
	if avg != 2000 || minLat != 1000 || maxLat != 3000 {
		t.Fatalf("B: latency avg/min/max = %d/%d/%d, want 2000/1000/3000", avg, minLat, maxLat)
	}
	// 首值优先：primary_model 与 gw_task_id 不被第二条覆盖。
	if *primary != "gpt-5.6-sol" || *taskID != "task-first" {
		t.Fatalf("B: first-value overwrite = %v/%v", *primary, *taskID)
	}
	// 数组去重追加：两个模型都在。
	if len(models) != 2 {
		t.Fatalf("B: models_used = %v, want 2 distinct", models)
	}

	// C ★退役开关不再支配聚合（2026-10-29）：第三次写入**必须落库**。
	//
	// 原实现在这里断言「v1 写入开启时整体 no-op」（双喂防护门）。该门的前提
	// 是「v1 写入方 + trg_update_session_summary 触发器同时存在」——两者都已
	// 随 v1 族退役消失 ⇒ 门剩下的唯一效果就是「退役开关一开，聚合静默停摆」。
	// 本写入器是聚合列的唯一写方 ⇒ 停摆 = 用户面会话列表/分析对新会话永远
	// 不出现，且无日志无错误。
	//
	// 断言因此从「不写」翻成「照写」：退役语义**不得**支配活行为。
	// 这里的「v1 开关为 true」由静态门
	// TestSessionSummaryDimWriter_IgnoresRetiredV1Switch 兜住（源文件不得
	// 再读该开关 ⇒ 该开关在物理上无法支配本写入器），动态侧由本段与
	// TestSessionSummaryDimWriter_NoV1TriggerPremise 兜住。
	if err := w.UpsertSessionDim(ctx, &telemetry.RequestLogEntry{
		RequestID: "r1013-sum-c", GwSessionID: sess, TenantID: "default",
		EventAt: &ts2, Success: true, CostUSD: floatPtr(9.9), LatencyMs: intPtr(9999),
	}); err != nil {
		t.Fatalf("third upsert: %v", err)
	}
	row = pool.QueryRow(ctx, `SELECT request_count FROM session_summaries WHERE session_key = $1`, summaryTestSession)
	if err := row.Scan(&count); err != nil {
		t.Fatalf("third row scan: %v", err)
	}
	if count != 3 {
		t.Fatalf("C: 退役开关不得支配聚合写入，request_count = %d, want 3（停摆=用户面新会话永不出现）", count)
	}
}

// TestSessionSummaryDimWriter_NoV1TriggerPremise 钉住「删门的安全前提」：
// session_summaries 上**不得**存在任何触发器，且 v1 族关系与
// update_session_summary() 函数必须已不存在。
//
// 为什么这是本轮的**核心守卫**而不是顺带检查：删掉「v1 写入开启 ⇒ no-op」
// 这道门之所以安全，前提是「没有第二个写方在喂同一批聚合列」。本测试把
// 这个前提变成**持续可见的事实**：任何将来重建 v1 族 + 触发器的迁移都会让
// 本测试当场报红，逼迁移作者重新决定要不要加回双喂门——而不是让一道早该
// 退休的开关一直悄悄支配聚合。
//
// 反向自证：若有人在 session_summaries 上挂一个触发器（例如为别的目的），
// 本测试必须报红 ⇒ 断言不是恒真。
func TestSessionSummaryDimWriter_NoV1TriggerPremise(t *testing.T) {
	pool := summaryTestPool(t)
	ctx := context.Background()

	// ① session_summaries 上不得有任何非内部触发器。
	var triggerCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)::int
		FROM pg_trigger t
		JOIN pg_class c ON c.oid = t.tgrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relname = 'session_summaries' AND NOT t.tgisinternal`,
	).Scan(&triggerCount); err != nil {
		t.Fatalf("probe session_summaries triggers: %v", err)
	}
	if triggerCount != 0 {
		t.Fatalf("session_summaries 上存在 %d 个触发器：聚合列有了第二个写方，"+
			"删掉双喂防护门会双计数 —— 必须先决定双喂仲裁再继续", triggerCount)
	}

	// ② v1 族关系必须已不存在（触发器的宿主表）。
	var v1Relations int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)::int
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public'
		  AND c.relname IN ('_request_logs_hot', '_request_logs', '_request_logs_bodies_hot')`,
	).Scan(&v1Relations); err != nil {
		t.Fatalf("probe v1 relations: %v", err)
	}
	if v1Relations != 0 {
		t.Fatalf("v1 族关系仍存在 %d 个：v1 写入方若复活会与本写入器双喂", v1Relations)
	}

	// ③ 触发器调用的函数必须已不存在（即便有人重建了函数体）。
	var fnCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)::int FROM pg_proc WHERE proname = 'update_session_summary'`,
	).Scan(&fnCount); err != nil {
		t.Fatalf("probe update_session_summary(): %v", err)
	}
	if fnCount != 0 {
		t.Fatalf("update_session_summary() 仍存在：触发器函数被重建，双喂前提可能复活")
	}
}
