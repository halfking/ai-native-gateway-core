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
//   - C ★双喂防护门（本门重点）：v1 写入开启（S4 未翻转）时必须整体
//     no-op —— 否则生产翻转前与触发器双计数，request_count 翻倍。
//     判据是行为侧：同一条 entry，门开时行数不变、门关时 +1。
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

	orig := v1WritesEnabled
	v1WritesEnabled = func() bool { return false }
	t.Cleanup(func() { v1WritesEnabled = orig })

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

	// C ★双喂防护门：v1 写入开启时 no-op（行数与聚合都不得变化）。
	v1WritesEnabled = func() bool { return true }
	if err := w.UpsertSessionDim(ctx, &telemetry.RequestLogEntry{
		RequestID: "r1013-sum-c", GwSessionID: sess, TenantID: "default",
		EventAt: &ts2, Success: true, CostUSD: floatPtr(9.9), LatencyMs: intPtr(9999),
	}); err != nil {
		t.Fatalf("gated upsert: %v", err)
	}
	row = pool.QueryRow(ctx, `SELECT request_count FROM session_summaries WHERE session_key = $1`, summaryTestSession)
	if err := row.Scan(&count); err != nil {
		t.Fatalf("gate row scan: %v", err)
	}
	if count != 2 {
		t.Fatalf("C: v1 写入开启时必须整体 no-op，request_count = %d, want 2（双喂即翻倍）", count)
	}
}
