package admin

// ★ 这道门钉的是**不变量**，不是节点名、不是具体行数：
//
//	**auto-route 成本仪表盘的两个「活指标」必须从会话族取值，
//	不得读 v1 冻结族（`_request_logs`）。**
//
// ─────────────────────────────────────────────────────────────────────────────
// 为什么需要这道门（2026-10-20 复核轮，
//   docs/audit/2026-10-20-request-logs-session-turns-复核轮.md §5.12）
//
// `customer_cost_view.active_concurrent` 与 `model_cost_per_task_view.avg_latency_ms`
// 是两个**活指标**（实时并发数、7 天平均延迟），它们的定义体原先各有一个
// 相关子查询读 v1 母表。860 把 v1 母表改名为 `_request_logs`，而 v1 自 S4 起
// **停写** ⇒ 该族冻结 ⇒
//
//	· active_concurrent 是 **5 分钟窗** ⇒ 冻结族里没有「最近 5 分钟」的行
//	  ⇒ 这个计数**在任何环境都恒 0**（活指标恒 0，**且无任何错误信号**）；
//	· avg_latency_ms 的 7 天窗只能读到「停写那一刻」的快照值。
//
// 迁移 883 把两个子查询换到会话族。这道门用**真库 + 造夹具**证明换过之后
// 真的取到了会话族的数据，而不是「视图能建出来就算数」。
//
// ─────────────────────────────────────────────────────────────────────────────
// 判别动作（为什么要造夹具，而不是只读现网数据）
//
// 这两个子查询是**相关子查询**：外层聚合表 `api_key_model_cost` 零行时，
// 子查询**根本不会执行** ⇒ 只看端点返回 `[]` 无法区分「读源已切」与
// 「压根没数据」。所以本门自己造外层行（`api_key_model_cost`）与会话族行，
// 再断言指标**非零/非空**。
//
// 前提断言（防「因看不见而假绿」）：断言 `_request_logs` 当前**确实是空的**
// ⇒ 于是「指标非零」这件事**只可能**由会话族贡献 ⇒ 指标若为 0，就一定是
// 读源没切（或谓词被改坏），而不是「v1 那边恰好有数据」。
//
// 阴性侧也钉住：is_auto_request=false / success=false 的会话行**不得**被计入
// active_concurrent ⇒ 防止「为了让门过，把谓词删掉」这种改法。
//
// 变异（改回旧写法会红在哪）：
//   · 把视图体改回读 `_request_logs` ⇒ active_concurrent 变 0 ⇒
//     "active_concurrent must be > 0" 失败；
//   · 把 `st.raw_model_name = mcp.raw_model` 改掉 ⇒ avg_latency_ms 变 NULL ⇒
//     "avg_latency_ms must not be NULL" 失败；
//   · 把 `st.is_auto_request = true` 删掉 ⇒ 计数变 2 ⇒ 阴性侧断言失败。

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// costViewsRealDBPool 连真库；没有 TEST_PG_URL 就跳过。
// ⚠ 跳过**不构成**证据（门禁纪律：t.Skip 的门在没配环境变量时永远绿）。
func costViewsRealDBPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence that the cost views read the session family")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("cannot connect to TEST_PG_URL: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("cannot ping TEST_PG_URL: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestCostViewMetrics_ReadSessionFamilyNotFrozenV1 用真库 + 造夹具断言：
// customer_cost_view.active_concurrent 与 model_cost_per_task_view.avg_latency_ms
// 的取值来自**会话族**。
func TestCostViewMetrics_ReadSessionFamilyNotFrozenV1(t *testing.T) {
	pool := costViewsRealDBPool(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // 夹具不留在库里

	// ── 前提断言 1：v1 族**不存在**（不是「空」）─────────────────────────
	// ⇒ 「指标非零」这件事只可能由会话族贡献。
	// ★ R68 改瞄：v1 族已随迁移 890 真删 ⇒ 原来的
	//   `SELECT count(*) FROM public._request_logs` 必然 42P01
	//   ⇒ 前提升级成「关系**不存在**」—— 更强，且归因前提不变。
	var v1Present bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('public._request_logs') IS NOT NULL`).Scan(&v1Present); err != nil {
		t.Fatalf("probe _request_logs: %v", err)
	}
	if v1Present {
		t.Fatalf("premise broken: public._request_logs 又存在了 —— " +
			"「指标非零」这件事不再能归因给会话族（v1 族可能重新长出行）")
	}

	// ── 夹具 0：一个**真实存在**的 api_key（视图 JOIN api_keys）──────────
	// ★ R68 改瞄：原来写的是「从库里挑一个」（`SELECT id FROM api_keys LIMIT 1`）。
	//   在**全新安装**的库里 api_keys 本来就可以是空的 ⇒ 那是**环境状态**，
	//   不是被测行为 ⇒ 门会随机红（实测：一次性库 0 行 ⇒
	//   "pick api_key: no rows in result set"）。
	//   ⇒ 改为**在事务内种一个**（随 ROLLBACK 消失，不留痕）。
	//   必需列：application_id / tenant_id / key_hash / key_prefix；
	//   key_hash 上有唯一索引 ⇒ 用带门标识的值避免撞已有行。
	var apiKeyID int
	if err := tx.QueryRow(ctx, `
		INSERT INTO public.api_keys
			(application_id, tenant_id, key_hash, key_prefix, key_alias, enabled, status)
		VALUES (1, 'default', 'gw_cost_view_gate_key_hash', 'gw_cost_v', 'gw-cost-view-gate', true, 'active')
		RETURNING id`).Scan(&apiKeyID); err != nil {
		t.Fatalf("seed api_key fixture: %v", err)
	}

	const rawModel = "gate-upstream-raw-model"
	// ⚠ 客户端模型必须与上游原始模型**故意不同**：夹具若把两者设成同一个值，
	//   门就分不出「匹配键用的是 raw_model_name（上游原始模型，对应 v1 的
	//   outbound_model）还是 model（客户端模型）」——那是把指标接到**错误
	//   的键空间**上的改法，必须能被抓到（变异 M2：把匹配键换成 model）。
	const clientModel = "gate-client-model-DIFFERENT"
	const latency = 4321
	now := time.Now().UTC()

	// ── 夹具 1：外层聚合行（没有它，相关子查询不会执行）────────────────
	if _, err := tx.Exec(ctx, `
		INSERT INTO public.api_key_model_cost
			(bucket, api_key_id, canonical_id, raw_model, billing_mode,
			 requests_total, requests_success, tokens_input, tokens_output, cost_usd,
			 active_concurrent, concurrency_limit, pressure_ratio, last_request_at, updated_at)
		VALUES ($1, $2, NULL, $3, 'token', 1, 1, 10, 5, 0.01, 0, 10, 0.1, $1, now())`,
		now, apiKeyID, rawModel); err != nil {
		t.Fatalf("insert api_key_model_cost fixture: %v", err)
	}

	// ── 夹具 2：会话族行（auto + success）⇒ 应当被两个指标看见 ─────────
	// ⚠ `session_turns_hot` 上有唯一键 (tenant_id, session_id, turn_no)
	//   ⇒ 同一 session 下的三行必须给不同 turn_no。
	//
	// ⚠ 谓词语义（第一版夹具把这里写错过，被门当场抓出来）：
	//   active_concurrent 的谓词是 `success IS NOT NULL`，**不是** `success = true`。
	//   语义是「**有结果的**请求」——成功与失败都算，因为并发达标的是
	//   「有多少个请求在跑」，失败的请求也占着并发额度。
	//   ⇒ 阴性侧要钉的是「success IS NULL（没跑完）不计入」与
	//     「非 auto 请求不计入」，**不是**「失败请求不计入」。
	insertSessionFixture := func(reqID string, turnNo int, isAuto bool, success *bool, lat int) {
		t.Helper()
		if _, err := tx.Exec(ctx, `
			INSERT INTO public.session_turns_hot
				(session_id, turn_no, tenant_id, request_id, ts,
				 api_key_id, is_auto_request, success, latency_ms,
				 model, raw_model_name, prompt_tokens, completion_tokens, cost_usd)
			VALUES ('gw_cost_view_gate', $1, 'default', $2, $3,
			        $4, $5, $6, $7,
			        $8, $9, 10, 5, 0.01)`,
			turnNo, reqID, now, fmt.Sprintf("%d", apiKeyID), isAuto, success, lat, clientModel, rawModel); err != nil {
			t.Fatalf("insert session_turns_hot fixture %s: %v", reqID, err)
		}
	}
	boolPtr := func(b bool) *bool { return &b }
	// 计入的那一条
	insertSessionFixture("gate-req-auto-ok", 1, true, boolPtr(true), latency)
	// 阴性侧 1：非 auto 请求 ⇒ 不计入
	insertSessionFixture("gate-req-not-auto", 2, false, boolPtr(true), latency)
	// 阴性侧 2：auto 但没有结果（success IS NULL）⇒ 不计入
	insertSessionFixture("gate-req-auto-null-success", 3, true, nil, latency)

	// ── 前提断言 2：夹具行确实在会话族里（否则「指标非零」无法归因）────
	var premiseRows int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM public.session_turns_hot
		 WHERE session_id = 'gw_cost_view_gate'`).Scan(&premiseRows); err != nil {
		t.Fatalf("premise probe: %v", err)
	}
	if premiseRows != 3 {
		t.Fatalf("premise broken: expected 3 fixture rows in session_turns_hot, got %d", premiseRows)
	}

	// ── 判据 1：active_concurrent 必须为 1 ─────────────────────────────
	// 计入的只有那条「auto + 有结果」的会话行；非 auto 与 success IS NULL
	// 的两条**不得**被计入。
	var activeConcurrent int
	if err := tx.QueryRow(ctx, `
		SELECT active_concurrent FROM public.customer_cost_view
		 WHERE api_key_id = $1`, apiKeyID).Scan(&activeConcurrent); err != nil {
		t.Fatalf("read customer_cost_view: %v", err)
	}
	if activeConcurrent != 1 {
		t.Fatalf("active_concurrent must be 1 (only the auto + non-NULL-success session turn counts), got %d — "+
			"the metric is not reading the session family, or its is_auto_request / success IS NOT NULL predicates were lost", activeConcurrent)
	}

	// ── 判据 2：avg_latency_ms 必须非空且等于夹具延迟 ──────────────────
	var avgLatency *float64
	if err := tx.QueryRow(ctx, `
		SELECT avg_latency_ms FROM public.model_cost_per_task_view
		 WHERE raw_model = $1`, rawModel).Scan(&avgLatency); err != nil {
		t.Fatalf("read model_cost_per_task_view: %v", err)
	}
	if avgLatency == nil {
		t.Fatalf("avg_latency_ms must not be NULL — the metric is not reading the session family " +
			"(or the raw_model_name join key drifted)")
	}
	if *avgLatency != float64(latency) {
		t.Fatalf("avg_latency_ms = %v, want %d (the fixture's latency)", *avgLatency, latency)
	}
}
