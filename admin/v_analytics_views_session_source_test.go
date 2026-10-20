package admin

// ★ 这道门钉的是**不变量**，不是节点名、不是具体行数：
//
//	**v_continuation_effectiveness / v_node_switch_analysis /
//	v_timeout_effectiveness 三张分析视图必须从会话族取值，
//	不得读 v1 冻结族（`_request_logs`）。**
//
// ─────────────────────────────────────────────────────────────────────────────
// 为什么需要这道门（2026-10-20 复核轮，
//   docs/audit/2026-10-20-request-logs-session-turns-复核轮.md §5.13）
//
// 这 3 张视图原先读的是 v1 **裸母表** `_request_logs`。v1 自 2026-10-13 起停写、
// 历史数据已清空 ⇒ 真库实测三张视图**恒 0 行**，且**不报任何错**。
// 同时它们是「后期删除 `_request_logs`」这一步的**依赖阻塞项**
// （pg_depend 实测：`_request_logs` 被 7 个视图依赖，这 3 张是其中
// **唯一没有任何 API/Go 代码消费**的）。
//
// 迁移 884 把它们换到会话族。本门用**真库 + 造夹具**证明换过之后真的
// 取到了会话族的数据，而不是「视图能建出来就算数」。
//
// ─────────────────────────────────────────────────────────────────────────────
// 夹具的两个桶怎么取（这是本门最需要小心的地方，两版都踩过）
//
// 这 3 张视图都是「按小时分桶」的聚合视图，桶里会混进**真实流量**。
// 若夹具落在有真实流量的桶里，断言就只能写成「≥ 某个数」——那正是
// 「没有正向下限」的弱判据（fitRow 那条教训：只断言「没坏」的判据，
// 在「干脆没做」面前永远是绿的）。
//
// ⇒ 两个桶都取「**独占**」的桶：
//
//	① inWindow 桶 = **未来**的整点（now + 3h）。
//	   视图的窗口是 `ts > now() - interval '24 hours'`，**没有上界**
//	   ⇒ 未来行在窗内；而真实流量**不可能**有未来时间戳 ⇒ 该桶天然独占。
//	   （代价写在这里：若将来视图加上界 `ts <= now()`，夹具会掉出窗口、
//	   门会红 ⇒ 那时要改成「当前小时 + 按 request_id 精确过滤」，
//	   而不是把断言改松。）
//
//	② outWindow 桶 = **动态找**一个「证明为空」的、且在 24h 窗之外的小时桶
//	   （从 now-28h 往后逐小时找，最远找 200 小时）。
//	   ⚠ **不能**随便取「now-30h」：本门第一版就是这么写的，结果那个桶里
//	   **恰好有 1 行真实数据** ⇒ 「该桶为空」的前提不成立 ⇒ 阴性侧断言
//	   分不清是「窗口条件被删坏」还是「那个桶本来就有数据」。
//	   ⇒ 前提必须**动态证明**，不能假设。
//
// ─────────────────────────────────────────────────────────────────────────────
// 前提断言（防「因看不见而假绿」）
//
// ① `public._request_logs` 当前**确实是空的** ⇒ 视图能取到的数据
//    **只可能**来自会话族 ⇒ 断言失败就一定是读源没切（或谓词被改坏）。
// ② 真库定义体里**没有** request_logs 族引用（用 strpos 精确匹配；
//    ⚠ 不要用 `LIKE '%_request_logs%'`——SQL 的 LIKE 里 `_` 是**单字符
//    通配符**，会把 canonical 视图名 request_logs_with_current_month 也判成
//    命中；上一轮就是因此多算了 2 张视图）。
// ③ 夹具自己的 session **不在 parent 腿**（否则 UNION ALL 会数两次）。
// ④ 夹具的每一行都**有** details 行（details 是 LEFT JOIN 的右表，
//    缺了就取不到那 6 个细节语义列）。
// ⑤ 两个桶里**除夹具外**没有别的行 ⇒ 桶内聚合计数可以被**精确**断言。
//
// 阴性侧也钉住：窗口外（>24h）的夹具行**不得**出现在任何一张视图里
// ⇒ 防止「为了让门过，把窗口条件删掉」这种改法。
//
// 变异（改回旧写法会红在哪）：
//   · 把视图体改回读 `_request_logs` ⇒ 三张视图全空 ⇒ 所有断言失败；
//   · 把 24 小时窗口删掉 ⇒ 窗口外的夹具行被计入 ⇒ 阴性侧断言失败；
//   · 把 details 的 join 键写错 ⇒ 6 个细节列取不到 ⇒ 指标变 NULL/0；
//   · 把两条腿写成 hot 单腿（漏 parent 腿）⇒ 本机 parent 腿有 877 行
//     ⇒ 计数会偏大 ⇒ 精确值断言失败。

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// vAnalyticsGateSessionID 每次运行用不同的 session_id，避免与上一次
// 崩溃留下的行撞唯一键（tenant_id, session_id, turn_no, partition_date）。
func vAnalyticsGateSessionID() string {
	return fmt.Sprintf("gw_v_analytics_gate_%d", time.Now().UnixNano())
}

// vAnalyticsFindEmptyBucket 动态找一个「证明为空」且在 24h 窗之外的小时桶。
//
// 为什么必须动态找（而不是写死「now-30h」）：本门第一版写死了那个桶，
// 结果那个桶里**恰好有 1 行真实数据** ⇒「该桶为空」的前提不成立 ⇒
// 阴性侧断言分不清是「窗口条件被删坏」还是「那个桶本来就有数据」。
// ⇒ 前提必须**被证明**，不能被假设。
//
// 找法：从 now-28h 往后逐小时试（-28h 起，保证在 24h 窗之外），
// 找第一个在两条腿里都**没有**行的小时桶；最远试 200 小时。
func vAnalyticsFindEmptyBucket(ctx context.Context, tx pgx.Tx) (time.Time, bool) {
	base := time.Now().UTC().Truncate(time.Hour)
	for i := 28; i <= 200; i++ {
		cand := base.Add(-time.Duration(i) * time.Hour)
		var n int
		if err := tx.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM public.session_turns
			         WHERE ts >= $1 AND ts < $1 + interval '1 hour')
			     + (SELECT count(*) FROM public.session_turns_hot
			         WHERE ts >= $1 AND ts < $1 + interval '1 hour')`, cand).Scan(&n); err != nil {
			// 探测失败就当这个桶不可用，继续找下一个
			continue
		}
		if n == 0 {
			return cand, true
		}
	}
	return time.Time{}, false
}

// TestVAnalyticsViews_ReadSessionFamilyNotFrozenV1 用真库 + 造夹具断言：
// 3 张 v_* 分析视图的取值来自**会话族**（而不是已冻结清空的 v1 族）。
func TestVAnalyticsViews_ReadSessionFamilyNotFrozenV1(t *testing.T) {
	pool := costViewsRealDBPool(t) // 同包复用：没配 TEST_PG_URL 就跳过（跳过≠证据）
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // 夹具不留在库里

	// ── 前提断言 1：v1 族**不存在**（不是「空」）─────────────────────────
	// ★ R68 改瞄：v1 族已随迁移 890 真删 ⇒ 原来的
	//   `SELECT count(*) FROM public._request_logs` 必然 42P01。
	//   ⇒ 前提从「**空的**」升级成「**不存在的**」—— 更强，而且
	//     「视图取到的数据只可能来自会话族」这个**归因前提**依然成立
	//     （v1 族一旦复活，这道门会立刻红，而不是默默读到一个空集）。
	var v1Present bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('public._request_logs') IS NOT NULL`).Scan(&v1Present); err != nil {
		t.Fatalf("probe _request_logs: %v", err)
	}
	if v1Present {
		t.Fatalf("premise broken: public._request_logs 又存在了 —— " +
			"「视图取到会话族数据」这件事不再能归因给会话族（v1 族可能重新长出行）")
	}

	// ── 前提断言 2：真库定义体里**没有** request_logs 族引用（精确仪器）──
	for _, v := range []string{
		"v_continuation_effectiveness", "v_node_switch_analysis", "v_timeout_effectiveness",
	} {
		var hits int
		if err := tx.QueryRow(ctx,
			`SELECT CASE WHEN strpos(pg_get_viewdef($1::regclass, true), 'request_logs') > 0 THEN 1 ELSE 0 END`,
			v).Scan(&hits); err != nil {
			t.Fatalf("probe viewdef %s: %v", v, err)
		}
		if hits != 0 {
			t.Fatalf("premise broken: %s 的定义体仍引用 request_logs 族 —— "+
				"「视图取到会话族数据」这件事就不可能是本门的结论", v)
		}
	}

	// ── 桶 ①：未来整点（天然独占）────────────────────────────────────
	sess := vAnalyticsGateSessionID()
	inBucket := time.Now().UTC().Truncate(time.Hour).Add(3 * time.Hour) // now + 3h
	partition := inBucket.Truncate(24 * time.Hour)                      // partition_date（两腿一致）

	// ── 桶 ②：动态找「证明为空」的窗口外小时桶 ─────────────────────────
	outBucket, ok := vAnalyticsFindEmptyBucket(ctx, tx)
	if !ok {
		t.Fatalf("premise broken: 找不到一个「证明为空」且在 24h 窗之外的小时桶（找了两百个小时都没有）—— " +
			"阴性侧断言（窗口外的行不得被计入）无法被精确判定")
	}
	outWindow := outBucket

	// ── 夹具 ──────────────────────────────────────────────────────────
	const (
		reqAuto    = "gate-req-auto"
		reqFail    = "gate-req-fail"
		reqNoCache = "gate-req-nocache"
		reqOld     = "gate-req-old"
	)
	insertTurn := func(reqID string, turnNo int, ts time.Time, success bool, latency int, errKind *string) {
		t.Helper()
		if _, err := tx.Exec(ctx, `
			INSERT INTO public.session_turns_hot
				(session_id, turn_no, tenant_id, request_id, ts, partition_date,
				 api_key_id, is_auto_request, success, latency_ms, error_kind,
				 model, raw_model_name, prompt_tokens, completion_tokens, cost_usd)
			VALUES ($1, $2, 'default', $3, $4, $5, '2', false, $6, $7, $8,
			        'gate-client-model', 'gate-upstream-raw-model', 10, 5, 0.01)`,
			sess, turnNo, reqID, ts, partition, success, latency, errKind); err != nil {
			t.Fatalf("insert session_turns_hot %s: %v", reqID, err)
		}
	}
	insertDetails := func(reqID string, turnNo int, ts time.Time, nodeSwitch *int, isCont *bool, cacheID *int64, ctxTokens *int, timeoutMode *string, effTimeout *int) {
		t.Helper()
		if _, err := tx.Exec(ctx, `
			INSERT INTO public.session_turn_details_hot
				(session_id, turn_no, tenant_id, request_id, ts, partition_date,
				 node_switch_count, is_continuation, cached_response_id, context_size_tokens,
				 timeout_mode, effective_timeout_seconds)
			VALUES ($1, $2, 'default', $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			sess, turnNo, reqID, ts, partition, nodeSwitch, isCont, cacheID, ctxTokens, timeoutMode, effTimeout); err != nil {
			t.Fatalf("insert session_turn_details_hot %s: %v", reqID, err)
		}
	}
	intPtr := func(v int) *int { return &v }
	boolPtr := func(v bool) *bool { return &v }
	int64Ptr := func(v int64) *int64 { return &v }
	strPtr := func(v string) *string { return &v }

	// ① 成功 + 有缓存命中 + node_switch=999
	insertTurn(reqAuto, 1, inBucket, true, 1234, nil)
	insertDetails(reqAuto, 1, inBucket, intPtr(999), boolPtr(true), int64Ptr(999999), intPtr(500), nil, nil)
	// ② 失败 + timeout 语义 + node_switch=998
	insertTurn(reqFail, 2, inBucket, false, 5678, strPtr("upstream_timeout"))
	insertDetails(reqFail, 2, inBucket, intPtr(998), boolPtr(false), nil, nil, strPtr("fixed"), intPtr(30))
	// ③ 成功 + 是续轮但**没**缓存命中（计入 cache_misses）
	insertTurn(reqNoCache, 3, inBucket, true, 111, nil)
	insertDetails(reqNoCache, 3, inBucket, intPtr(996), boolPtr(true), nil, nil, nil, nil)
	// ④ 窗口外（阴性侧：>24h ⇒ 三张视图都不得计入）
	insertTurn(reqOld, 4, outWindow, true, 222, nil)
	insertDetails(reqOld, 4, outWindow, intPtr(995), boolPtr(true), int64Ptr(111111), intPtr(999), nil, nil)

	// ⑤ **parent 腿**夹具（turn_no=5, switches=994）
	//    为什么必须有它：只往 hot 腿插夹具的话，**「把两条腿写成 hot 单腿」**
	//    这种变异**抓不到**（夹具在 hot，hot 单腿照样能取到它）⇒
	//    门必须有一条**只在 parent 腿**才成立的行。
	//    ⚠ partition_date 用一个**固定的、确定存在的月份分区**（2026-10）
	//    （`session_turns` 是按月分区的表，hot 表不是 ⇒ 两腿的写法不同）。
	const parentPartition = "2026-10-15"
	if _, err := tx.Exec(ctx, `
		INSERT INTO public.session_turns
			(session_id, turn_no, tenant_id, request_id, ts, partition_date,
			 api_key_id, is_auto_request, success, latency_ms, error_kind,
			 model, raw_model_name, prompt_tokens, completion_tokens, cost_usd)
		VALUES ($1, 5, 'default', 'gate-req-parent', $2, $3, '2', false, true, 2222, NULL,
		        'gate-client-model', 'gate-upstream-raw-model', 10, 5, 0.01)`,
		sess, inBucket, parentPartition); err != nil {
		t.Fatalf("insert session_turns (parent leg): %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO public.session_turn_details
			(session_id, turn_no, tenant_id, request_id, ts, partition_date,
			 node_switch_count, is_continuation, cached_response_id, context_size_tokens,
			 timeout_mode, effective_timeout_seconds)
		VALUES ($1, 5, 'default', 'gate-req-parent', $2, $3, 994, false, NULL, NULL, NULL, NULL)`,
		sess, inBucket, parentPartition); err != nil {
		t.Fatalf("insert session_turn_details (parent leg): %v", err)
	}

	// ── 前提断言 3：夹具不在 parent 腿（UNION ALL 不能数两次）──────────
	var overlap int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM public.session_turns_hot h
		  JOIN public.session_turns p USING (session_id, turn_no)
		 WHERE h.session_id = $1`, sess).Scan(&overlap); err != nil {
		t.Fatalf("probe overlap: %v", err)
	}
	if overlap != 0 {
		t.Fatalf("premise broken: 夹具同时出现在 hot 与 parent 腿 ⇒ UNION ALL 会把同一行数两次")
	}

	// ── 前提断言 4：夹具每一行都有 details 行 ───────────────────────────
	var missingDetails int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM public.session_turns_hot h
		  LEFT JOIN public.session_turn_details_hot d
		    ON d.tenant_id::text = h.tenant_id::text
		   AND d.request_id = h.request_id
		   AND d.partition_date = h.partition_date
		 WHERE h.session_id = $1 AND d.session_id IS NULL`, sess).Scan(&missingDetails); err != nil {
		t.Fatalf("probe details coverage: %v", err)
	}
	if missingDetails != 0 {
		t.Fatalf("premise broken: 夹具有 %d 行没有对应 details ⇒ 细节语义列取不到，断言不可归因", missingDetails)
	}

	// ── 前提断言 5：两个桶里**除夹具外**没有别的行 ⇒ 计数可精确断言 ────
	for _, spec := range []struct {
		name   string
		bucket time.Time
	}{
		{"inWindow", inBucket},
		{"outWindow", outBucket},
	} {
		var other int
		if err := tx.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM public.session_turns
			         WHERE ts >= $1 AND ts < $1 + interval '1 hour' AND session_id <> $2)
			     + (SELECT count(*) FROM public.session_turns_hot
			         WHERE ts >= $1 AND ts < $1 + interval '1 hour' AND session_id <> $2)`,
			spec.bucket, sess).Scan(&other); err != nil {
			t.Fatalf("probe %s bucket isolation: %v", spec.name, err)
		}
		if other != 0 {
			t.Fatalf("premise broken: %s 桶里还有 %d 行**非夹具**数据 ⇒ 该桶的聚合计数不能被精确断言", spec.name, other)
		}
	}

	// ══════════════════════════════════════════════════════════════════
	// 判据 1：v_node_switch_analysis —— 按 (小时, node_switch_count) 分桶
	// ══════════════════════════════════════════════════════════════════
	type switchRow struct {
		Switches   int
		ReqCount   int
		SuccCount  int
		SuccRate   float64
		AvgLatency float64
	}
	got := map[int]switchRow{}
	qr, err := tx.Query(ctx, `
		SELECT switches, request_count, success_count, success_rate, avg_latency_seconds
		  FROM public.v_node_switch_analysis
		 WHERE time_bucket = $1
		 ORDER BY switches`, inBucket)
	if err != nil {
		t.Fatalf("read v_node_switch_analysis: %v", err)
	}
	for qr.Next() {
		var r switchRow
		if err := qr.Scan(&r.Switches, &r.ReqCount, &r.SuccCount, &r.SuccRate, &r.AvgLatency); err != nil {
			qr.Close()
			t.Fatalf("scan v_node_switch_analysis: %v", err)
		}
		got[r.Switches] = r
	}
	qr.Close()
	if err := qr.Err(); err != nil {
		t.Fatalf("iterate v_node_switch_analysis: %v", err)
	}

	// 999 = 成功那行；998 = 失败那行；996 = 第三行
	if r, ok := got[999]; !ok {
		t.Fatalf("v_node_switch_analysis 在夹具小时桶里没有 switches=999 的行（实际 %v）—— "+
			"读源没切到会话族，或 node_switch_count 取不到", got)
	} else {
		if r.ReqCount != 1 || r.SuccCount != 1 || r.SuccRate != 100 {
			t.Errorf("switches=999 行应为 request=1 success=1 rate=100，实际 %+v", r)
		}
		if r.AvgLatency < 1.23 || r.AvgLatency > 1.24 {
			t.Errorf("switches=999 行的 avg_latency_seconds 应为 1.234（夹具延迟 1234ms），实际 %v", r.AvgLatency)
		}
	}
	if r, ok := got[998]; !ok {
		t.Errorf("v_node_switch_analysis 缺少 switches=998 的行（失败那行）")
	} else {
		if r.ReqCount != 1 || r.SuccCount != 0 || r.SuccRate != 0 {
			t.Errorf("switches=998 行应为 request=1 success=0 rate=0，实际 %+v", r)
		}
	}
	if _, ok := got[996]; !ok {
		t.Errorf("v_node_switch_analysis 缺少 switches=996 的行（第三行）")
	}
	// ★ parent 腿：这条夹具**只在** `session_turns`（parent）里 ⇒
	//   「把两条腿写成 hot 单腿」的变异会让它消失 ⇒ 这条断言专门钉它。
	if r, ok := got[994]; !ok {
		t.Errorf("v_node_switch_analysis 缺少 switches=994 的行（**parent 腿**那条夹具）—— " +
			"parent 腿没被读进去（视图被改成 hot 单腿？）")
	} else {
		if r.ReqCount != 1 || r.SuccCount != 1 {
			t.Errorf("switches=994 行应为 request=1 success=1，实际 %+v", r)
		}
	}

	// ══════════════════════════════════════════════════════════════════
	// 判据 2：v_continuation_effectiveness —— 按小时分桶
	// ══════════════════════════════════════════════════════════════════
	var contReq, cacheHits, cacheMisses int
	var cacheRate float64
	var tokensSaved int64
	if err := tx.QueryRow(ctx, `
		SELECT continuation_requests, cache_hits, cache_misses, cache_hit_rate, tokens_saved
		  FROM public.v_continuation_effectiveness
		 WHERE time_bucket = $1`, inBucket).Scan(&contReq, &cacheHits, &cacheMisses, &cacheRate, &tokensSaved); err != nil {
		t.Fatalf("read v_continuation_effectiveness: %v", err)
	}
	// 夹具：turn1（续轮+有缓存）+ turn3（续轮+无缓存）⇒ 2 个续轮、1 次命中、1 次未命中
	if contReq != 2 {
		t.Errorf("continuation_requests 应为 2（夹具两条续轮行），实际 %d", contReq)
	}
	if cacheHits != 1 {
		t.Errorf("cache_hits 应为 1，实际 %d", cacheHits)
	}
	if cacheMisses != 1 {
		t.Errorf("cache_misses 应为 1（续轮但没缓存命中那条），实际 %d", cacheMisses)
	}
	if cacheRate != 50 {
		t.Errorf("cache_hit_rate 应为 50（1/2），实际 %v", cacheRate)
	}
	if tokensSaved != 500 {
		t.Errorf("tokens_saved 应为 500（夹具 context_size_tokens），实际 %d", tokensSaved)
	}

	// ══════════════════════════════════════════════════════════════════
	// 判据 3：v_timeout_effectiveness —— 按 (小时, timeout_mode) 分桶
	// ══════════════════════════════════════════════════════════════════
	var totalReq, succCnt, timeoutCnt int
	var timeoutRate, avgEffTimeout, avgLatency float64
	if err := tx.QueryRow(ctx, `
		SELECT total_requests, success_count, timeout_count, timeout_rate, avg_effective_timeout, avg_latency_seconds
		  FROM public.v_timeout_effectiveness
		 WHERE time_bucket = $1 AND timeout_mode = 'fixed'`, inBucket).Scan(
		&totalReq, &succCnt, &timeoutCnt, &timeoutRate, &avgEffTimeout, &avgLatency); err != nil {
		t.Fatalf("read v_timeout_effectiveness: %v", err)
	}
	if totalReq != 1 {
		t.Errorf("total_requests 应为 1（只有失败那行有 effective_timeout_seconds），实际 %d", totalReq)
	}
	if succCnt != 0 {
		t.Errorf("success_count 应为 0，实际 %d", succCnt)
	}
	if timeoutCnt != 1 {
		t.Errorf("timeout_count 应为 1（success=false 且 error_kind 含 timeout），实际 %d", timeoutCnt)
	}
	if timeoutRate != 100 {
		t.Errorf("timeout_rate 应为 100，实际 %v", timeoutRate)
	}
	if avgEffTimeout != 30 {
		t.Errorf("avg_effective_timeout 应为 30（夹具值），实际 %v", avgEffTimeout)
	}
	if avgLatency < 5.67 || avgLatency > 5.68 {
		t.Errorf("avg_latency_seconds 应为 5.678（夹具延迟 5678ms），实际 %v", avgLatency)
	}

	// ══════════════════════════════════════════════════════════════════
	// 判据 4（阴性侧）：窗口外（>24h）的夹具行**不得**出现在任何一张视图里
	// （该桶已由前提断言 5 证明「除夹具外无别的行」⇒ 视图里出现它，
	//   就只可能是那条窗口外的夹具行被计入了）
	// ══════════════════════════════════════════════════════════════════
	for _, v := range []string{
		"v_continuation_effectiveness", "v_node_switch_analysis", "v_timeout_effectiveness",
	} {
		var n int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM public.`+v+` WHERE time_bucket = $1`, outBucket).Scan(&n); err != nil {
			t.Fatalf("probe out-of-window bucket in %s: %v", v, err)
		}
		if n != 0 {
			t.Errorf("%s 把**窗口外**（>24h）的行计入了 —— 24 小时窗口条件被删坏了", v)
		}
	}
	// 行级标记（不依赖桶独占性）：switches=995 与 timeout_mode='gate-ooo'
	// 都不该出现（后者我没插，改用 switches 这条哨兵）
	var oooSwitches int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM public.v_node_switch_analysis WHERE switches = 995`).Scan(&oooSwitches); err != nil {
		t.Fatalf("probe ooo switches: %v", err)
	}
	if oooSwitches != 0 {
		t.Errorf("v_node_switch_analysis 把**窗口外**（>24h）的行计入了（switches=995 出现了）—— 24 小时窗口条件被删坏了")
	}
}
