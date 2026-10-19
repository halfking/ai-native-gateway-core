// Top-N 大表端点快照缓存的判据（2026-10-16 第三轮性能修正）。
//
// 背景：GET /api/admin/data-lifecycle/storage/tables 在 /storage 上了快照之后
// 成了本仓唯一还 >2s 的 admin GET（实测 1286 / 2500 / 1734ms）。慢因与总览
// 不同：这里的 ORDER BY 必须对**全部 440 张表**逐个算 pg_total_relation_size
// 才能排序，LIMIT 20 省不掉这笔钱；把 TOAST 的相关子查询改写成
// LEFT JOIN pg_class tc ON tc.oid = c.reltoastrelid 实测**没有收益**
// （1170/1197/3463ms vs 1696/1254/1455ms），故沿用总览同构的短 TTL 快照。
//
// 本轮新增的真实风险不是"缓存"，而是"一次采满 200 行、再按 limit 切片"：
// 切片时若忘了重算 totalBytes / PercentOfDB，limit=20 就会显示"对 200 行
// 求和的占比"——数字看着合理，无任何报错，但与优化前口径不同。
// 因此 TestDataLifecycleTableSizesSliceRecomputesTotals 是本文件的核心判据。
//
// 判据纪律（对齐 LESSONS）：
//   - 每条"命中"断言都配一条"前提仍在场"的断言，避免"看不见"被当成"没发生"。
//   - TTL 用例先钉生产默认值的上界，再临时压到毫秒级——否则"把 TTL 改成
//     365 天"这种改法会让显式失效用例照样全绿。
//   - 占比断言用**响应自身的** TotalBytes 复算，不用绝对常量。
//
// 运行：
//
//	source /tmp/lgw-env.sh   # 导出 LLM_GATEWAY_DATABASE_URL（真库判据）
//	go test ./admin -run TestDataLifecycleTableSizes -count=1 -v
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// callTableSizes 走真实 handler，返回状态码、解码响应与耗时。
func callTableSizes(t *testing.T, h *Handler, query string) (int, tableSizesResponse, time.Duration) {
	t.Helper()
	url := "/api/admin/data-lifecycle/storage/tables"
	if query != "" {
		url += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()

	start := time.Now()
	h.handleDataLifecycleTableSizes(rec, req)
	elapsed := time.Since(start)

	var resp tableSizesResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode tableSizes: %v (body=%.200s)", err, rec.Body.String())
		}
	}
	return rec.Code, resp, elapsed
}

// TestDataLifecycleTableSizesFieldsAreComplete 端点返回 200、字段齐全、数据是真的。
//
// 「前提仍在场」的一半：必须证明这张表单来自真实 PG 而不是空壳，所以拿响应里
// 的第一张表去问 PG 的实时字节数对账。
func TestDataLifecycleTableSizesFieldsAreComplete(t *testing.T) {
	h, pool, _ := newStoragePerfHandler(t)

	// 这一条走原始 recorder：契约破坏最常见的形式是**字段名消失**，
	// 而字段名只存在于序列化后的 raw JSON 里，解码成结构体就看不见了。
	req := httptest.NewRequest(http.MethodGet, "/api/admin/data-lifecycle/storage/tables", nil)
	rec := httptest.NewRecorder()
	h.handleDataLifecycleTableSizes(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%.300s)", rec.Code, rec.Body.String())
	}
	raw := rec.Body.String()
	for _, key := range []string{
		`"table"`, `"schema"`, `"rows"`, `"total_bytes"`, `"total_human"`,
		`"index_bytes"`, `"toast_bytes"`, `"toast_human"`,
		`"percent_of_db"`, `"is_partitioned"`,
		`"tables"`, `"collected_at"`,
	} {
		if !strings.Contains(raw, key) {
			t.Errorf("response missing key %s (body=%.400s)", key, raw)
		}
	}

	var resp tableSizesResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Tables) == 0 {
		t.Fatal("Tables 为空：本库 440 张表，Top-N 不可能为空")
	}
	if resp.CollectedAt.IsZero() {
		t.Error("CollectedAt is zero")
	}
	if resp.TotalBytes <= 0 {
		t.Errorf("TotalBytes = %d, want > 0", resp.TotalBytes)
	}

	// 与 PG 实时值对账：响应里的字节数必须是**真实读数**而不是壳子。
	//
	// 为什么不能逐字节精确相等（2026-10-20 复核轮；判据红了先怀疑自己的前提）：
	//  - 端点口径就是 pg_total_relation_size(c.oid)，而本判据的 live 查询用的是
	//    **同一个函数** —— 两者相等的前提是「两次读之间那张表没变」。
	//  - 首行是全库最大的表。本机实测首行为 ursm_node_snapshot_min（231MB），
	//    网关的 URSM worker 正在批量写它（n_tup_ins 4.9 万，实测 5 秒涨 64KB）：
	//    快照采集与随后的 live 查询之间只要落进一次写入，两者就不相等。
	//  - 更糟的是首行可能是**某个用例自己建的临时 schema 里的表**：用例一结束
	//    就 DROP CASCADE，live 查询直接 42P01。
	//  ⇒ 精确相等在**活库**上间歇性红（同一份代码偶发 PASS 偶发 FAIL），
	//    而端点本身是对的（它返回的就是真实读数）。变的是判据的前提，不是产品行为。
	//
	// 现在的口径：**按本文件上方注释早已写明的「±1%」比对**（原实现写成了精确
	// 相等 —— 判据没做到它自己声明的事）；关系已被删时改判下一行；并保留
	// 「至少有一行成功对账」的前提断言。
	//
	// 判据强度：真错了会差几十个百分点（只回 index_bytes、只回 toast、回 0），
	// 而活库的写入漂移实测在 KB 级 —— ±1% 足以把「读数不对」与「读数在变」分开。
	first := resp.Tables[0]
	reconciled := 0
	for i := range resp.Tables {
		row := resp.Tables[i]
		var liveBytes int64
		if err := pool.QueryRow(context.Background(),
			"SELECT pg_total_relation_size($1::regclass)", row.Schema+"."+row.Table).Scan(&liveBytes); err != nil {
			// 关系可能已被删（临时 schema 的表在用例结束时 DROP CASCADE）→ 换下一行
			continue
		}
		if liveBytes <= 0 {
			continue
		}
		reconciled++
		diff := row.TotalBytes - liveBytes
		if diff < 0 {
			diff = -diff
		}
		// 容差 = max(1%, 64KB)：小表上 1% 可能小于一个页（8KB），要给页级漂移留余量
		allowed := liveBytes / 100
		if allowed < 64*1024 {
			allowed = 64 * 1024
		}
		if diff > allowed {
			t.Errorf("第 %d 行 %s.%s 的 TotalBytes = %d，PG 实时 = %d（偏差 %d 字节 > 容差 %d）—— 响应不是真实读数",
				i, row.Schema, row.Table, row.TotalBytes, liveBytes, diff, allowed)
		}
		break
	}
	if reconciled == 0 {
		t.Fatalf("前提不在场：没有任何一行能与 PG 实时值对账（首行 %s.%s）", first.Schema, first.Table)
	}
	if first.TotalHuman != humanBytes(first.TotalBytes) {
		t.Errorf("TotalHuman = %q，与 TotalBytes=%d 不一致", first.TotalHuman, first.TotalBytes)
	}

	// 首调必须真打一次 PG
	if got := tableSizesCollectCount(); got != 1 {
		t.Errorf("collect count = %d, want 1（首调必须真查一次库）", got)
	}

	// 排序必须是降序（页面把它当"最大的表"榜）
	for i := 1; i < len(resp.Tables); i++ {
		if resp.Tables[i].TotalBytes > resp.Tables[i-1].TotalBytes {
			t.Errorf("未按大小降序：第 %d 行 %d > 第 %d 行 %d",
				i, resp.Tables[i].TotalBytes, i-1, resp.Tables[i-1].TotalBytes)
		}
	}
}

// TestDataLifecycleTableSizesSecondCallHitsCache 第二次调用命中快照。
func TestDataLifecycleTableSizesSecondCallHitsCache(t *testing.T) {
	h, _, _ := newStoragePerfHandler(t)

	code1, r1, d1 := callTableSizes(t, h, "")
	if code1 != http.StatusOK {
		t.Fatalf("first status = %d", code1)
	}
	if got := tableSizesCollectCount(); got != 1 {
		t.Fatalf("after first call collect count = %d, want 1", got)
	}

	code2, r2, d2 := callTableSizes(t, h, "")
	if code2 != http.StatusOK {
		t.Fatalf("second status = %d", code2)
	}

	if got := tableSizesCollectCount(); got != 1 {
		t.Errorf("after second call collect count = %d, want 1 —— 查询被重复执行了", got)
	}
	if got := tableSizesHitCount(); got != 1 {
		t.Errorf("cache hit count = %d, want 1", got)
	}
	if d2 >= 300*time.Millisecond {
		t.Errorf("second call = %v, want < 300ms（热调用目标）", d2)
	}
	if d2 >= d1 {
		t.Errorf("second call %v not faster than first %v", d2, d1)
	}

	// 命中快照不得改变响应内容
	if len(r2.Tables) != len(r1.Tables) {
		t.Fatalf("table count changed on cache hit: %d → %d", len(r1.Tables), len(r2.Tables))
	}
	for i := range r1.Tables {
		if r1.Tables[i].Table != r2.Tables[i].Table || r1.Tables[i].TotalBytes != r2.Tables[i].TotalBytes {
			t.Fatalf("call[%d] differs on cache hit: %+v vs %+v", i, r1.Tables[i], r2.Tables[i])
		}
	}
	if !r2.CollectedAt.Equal(r1.CollectedAt) {
		t.Errorf("CollectedAt changed on cache hit: %v → %v", r1.CollectedAt, r2.CollectedAt)
	}

	t.Logf("cold=%v hot=%v speedup=%.1fx rows=%d", d1, d2, float64(d1)/float64(d2), len(r1.Tables))
}

// TestDataLifecycleTableSizesSliceRecomputesTotals **本文件的核心判据**。
//
// 快照一次采满 200 行，请求按 limit 切片。若切片时忘了重算，占比会变成
// "对 200 行求和"的百分比——数字合法、页面不报错，但与优化前"对 Top-limit
// 求和"的口径不符，属于静默的口径漂移。
//
// 三段断言：
//  1. limit=5 的行集合 == limit=200 的前 5 行（切片正确）
//  2. limit=5 的 TotalBytes == 前 5 行之和（总量按 Top-limit 重算）
//  3. 每个 PercentOfDB == TotalBytes*100/本响应 TotalBytes（占比按 Top-limit 重算）
//     ——并先断言 5 行之和确实小于 200 行之和，否则第 3 条没有区分力。
func TestDataLifecycleTableSizesSliceRecomputesTotals(t *testing.T) {
	h, _, _ := newStoragePerfHandler(t)

	_, full, _ := callTableSizes(t, h, "limit=200")
	if len(full.Tables) == 0 {
		t.Fatal("limit=200 返回空")
	}
	if len(full.Tables) < 10 {
		t.Fatalf("limit=200 只回了 %d 行，无法做前缀断言", len(full.Tables))
	}

	const n = 5
	_, small, _ := callTableSizes(t, h, fmt.Sprintf("limit=%d", n))

	if len(small.Tables) != n {
		t.Fatalf("limit=%d 返回 %d 行，want %d", n, len(small.Tables), n)
	}

	// ① 前缀性质：Top-5 必须是 Top-200 的前 5 行
	for i := 0; i < n; i++ {
		if small.Tables[i].Table != full.Tables[i].Table {
			t.Errorf("第 %d 行不是 Top-200 的前缀：%s vs %s", i, small.Tables[i].Table, full.Tables[i].Table)
		}
	}

	// 前提仍在场：Top-5 的和必须**小于** Top-200 的和，否则下面的占比断言
	// 区分不出"按 Top-5 重算"和"沿用 Top-200 的总量"。
	var sum5, sum200 int64
	for i := 0; i < n; i++ {
		sum5 += small.Tables[i].TotalBytes
	}
	for i := range full.Tables {
		sum200 += full.Tables[i].TotalBytes
	}
	if sum5 >= sum200 {
		t.Fatalf("前提不在场：Top-5(%d) 未小于 Top-200(%d)，占比断言无区分力", sum5, sum200)
	}
	if small.TotalBytes != sum5 {
		t.Errorf("limit=%d 的 TotalBytes = %d，want Top-%d 之和 %d", n, small.TotalBytes, n, sum5)
	}
	if small.TotalBytes == full.TotalBytes {
		t.Errorf("前提不在场：limit=%d 与 limit=200 的 TotalBytes 相同(%d)，未按 Top-limit 重算", n, small.TotalBytes)
	}

	// ③ 占比必须用**本响应**的 TotalBytes 复算
	for i := range small.Tables {
		want := int(small.Tables[i].TotalBytes * 100 / small.TotalBytes)
		if small.Tables[i].PercentOfDB != want {
			t.Errorf("limit=%d 第 %d 行 PercentOfDB = %d，want %d（= TotalBytes*100/%d）",
				n, i, small.Tables[i].PercentOfDB, want, small.TotalBytes)
		}
	}

	// 上界自洽：占比之和应接近 100（整除误差 ≤ n）
	totalPct := 0
	for i := range small.Tables {
		totalPct += small.Tables[i].PercentOfDB
	}
	if totalPct > 100 || totalPct < 100-n {
		t.Errorf("limit=%d 的 PercentOfDB 之和 = %d，want ∈ [%d, 100]", n, totalPct, 100-n)
	}
}

// TestDataLifecycleTableSizesInvalidateRefetches 真建表 + 真字节数，证明失效有效。
func TestDataLifecycleTableSizesInvalidateRefetches(t *testing.T) {
	h, pool, schema := newStoragePerfHandler(t)

	// 建一张足够大的表，确保它能挤进 Top-N（否则断言无从观察）
	if _, err := pool.Exec(context.Background(),
		fmt.Sprintf("CREATE TABLE %s.t_inv (id bigint, pad text)", schema)); err != nil {
		t.Fatalf("create t_inv: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		fmt.Sprintf("INSERT INTO %s.t_inv (pad) SELECT repeat('x', 8192) FROM generate_series(1, 40000)", schema)); err != nil {
		t.Fatalf("fill t_inv: %v", err)
	}

	code1, r1, _ := callTableSizes(t, h, "")
	if code1 != http.StatusOK {
		t.Fatalf("first status = %d", code1)
	}
	t1 := r1.TotalBytes
	if got := tableSizesCollectCount(); got != 1 {
		t.Fatalf("collect count = %d, want 1", got)
	}
	var sawInv bool
	for _, tb := range r1.Tables {
		if tb.Table == "t_inv" {
			sawInv = true
		}
	}
	if !sawInv {
		t.Fatalf("前提不在场：刚建的 t_inv 不在 Top-N 里（它有 %d 字节），本用例无从观察", t1)
	}

	// 再建一张更大的表，快照期内总量**不应**变化
	if _, err := pool.Exec(context.Background(),
		fmt.Sprintf("CREATE TABLE %s.t_inv2 (id bigint, pad text)", schema)); err != nil {
		t.Fatalf("create t_inv2: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		fmt.Sprintf("INSERT INTO %s.t_inv2 (pad) SELECT repeat('y', 8192) FROM generate_series(1, 120000)", schema)); err != nil {
		t.Fatalf("fill t_inv2: %v", err)
	}

	// 直接问 PG，确认总量确实涨了（否则③的断言没有意义）
	var liveTotal int64
	if err := pool.QueryRow(context.Background(), `
		SELECT COALESCE(SUM(pg_total_relation_size(c.oid)),0)::bigint
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname NOT IN ('pg_catalog','information_schema')
		  AND c.relkind IN ('r','p')`).Scan(&liveTotal); err != nil {
		t.Fatalf("probe live total: %v", err)
	}
	if liveTotal <= t1 {
		t.Fatalf("前提不在场：t_inv2 未让 PG 真实总量增长（live=%d ≤ t1=%d）", liveTotal, t1)
	}

	code2, r2, _ := callTableSizes(t, h, "")
	if code2 != http.StatusOK {
		t.Fatalf("second status = %d", code2)
	}
	if r2.TotalBytes != t1 {
		t.Errorf("snapshot not in effect: TotalBytes=%d, want %d（TTL 内不该变）", r2.TotalBytes, t1)
	}
	if got := tableSizesCollectCount(); got != 1 {
		t.Fatalf("collect count = %d, want 1 before invalidation", got)
	}

	// 失效 → 必须重新取数
	InvalidateDataLifecycleStorageCache()

	code3, r3, _ := callTableSizes(t, h, "")
	if code3 != http.StatusOK {
		t.Fatalf("third status = %d", code3)
	}
	if got := tableSizesCollectCount(); got != 2 {
		t.Errorf("collect count after invalidate = %d, want 2（失效后必须重查）", got)
	}
	if r3.TotalBytes <= t1 {
		t.Errorf("TotalBytes after invalidate = %d, want > %d —— 失效后没有重新取数", r3.TotalBytes, t1)
	}
	t.Logf("t1=%d t3=%d live=%d", t1, r3.TotalBytes, liveTotal)
}

// TestDataLifecycleTableSizesTTLExpiryRefetches TTL 到期必须重查，
// 并先钉住生产默认值的上界。
//
// 2026-10-17 契约变更（stale-while-revalidate）：到期后**不再同步重算**，
// 而是立即返回旧读数 + 后台单飞刷新。所以"重查发生了"不能靠本次调用的
// 返回值证明——必须等后台刷新落定后再取一次。故本用例分三段：
//
//	③ 到期后的那次调用：**不得阻塞**（这正是本次优化的目的）
//	④ 轮询等后台刷新落定，断言 collect 计数确实增长（刷新真的发生了）
//	⑤ 再取一次，断言拿到的是刷新后的新读数（刷新出来的数据是有效的）
func TestDataLifecycleTableSizesTTLExpiryRefetches(t *testing.T) {
	h, pool, schema := newStoragePerfHandler(t)

	// ① 生产默认值上界：这条不能被下面的临时覆盖掩盖
	if tableSizesTTL <= 0 || tableSizesTTL > 60*time.Second {
		t.Fatalf("tableSizesTTL = %v, want (0, 60s] —— TTL 过大会让数据永久陈旧", tableSizesTTL)
	}

	orig := tableSizesTTL
	tableSizesTTL = 60 * time.Millisecond
	t.Cleanup(func() { tableSizesTTL = orig })

	if _, err := pool.Exec(context.Background(),
		fmt.Sprintf("CREATE TABLE %s.t_ttl (id bigint, pad text)", schema)); err != nil {
		t.Fatalf("create t_ttl: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		fmt.Sprintf("INSERT INTO %s.t_ttl (pad) SELECT repeat('z', 4096) FROM generate_series(1, 30000)", schema)); err != nil {
		t.Fatalf("fill t_ttl: %v", err)
	}

	code1, r1, _ := callTableSizes(t, h, "")
	if code1 != http.StatusOK {
		t.Fatalf("first status = %d", code1)
	}
	t1 := r1.TotalBytes
	if got := tableSizesCollectCount(); got != 1 {
		t.Fatalf("collect count = %d, want 1", got)
	}

	// TTL 期内：不应重查
	if _, _, _ = callTableSizes(t, h, ""); tableSizesCollectCount() != 1 {
		t.Fatalf("within TTL collect count = %d, want 1", tableSizesCollectCount())
	}

	// 制造可观测变化，再等到期
	if _, err := pool.Exec(context.Background(),
		fmt.Sprintf("INSERT INTO %s.t_ttl (pad) SELECT repeat('w', 4096) FROM generate_series(1, 90000)", schema)); err != nil {
		t.Fatalf("grow t_ttl: %v", err)
	}
	time.Sleep(150 * time.Millisecond) // > TTL

	// ③ 到期后的调用：返回的仍是旧读数，且**不得**为它阻塞
	code2, r2, d2 := callTableSizes(t, h, "")
	if code2 != http.StatusOK {
		t.Fatalf("second status = %d", code2)
	}
	if d2 >= 300*time.Millisecond {
		t.Errorf("过期后的调用 = %v，want < 300ms —— 过期后必须走旧读数而不是同步重算", d2)
	}
	if r2.TotalBytes != t1 {
		t.Errorf("过期调用应返回旧读数 %d，实际 %d", t1, r2.TotalBytes)
	}

	// ④⑤ 轮询到新读数真的可见。
	//
	// 这里**不能**只等 collect 计数：collect 在查询开始前就自增，等���它变成 2
	// 时刷新可能还没把快照写回去（第一版判据就栽在这里，读数仍是旧值）。
	// 契约是"刷新最终让新读数可见"，所以直接按结果轮询。
	deadline := time.Now().Add(25 * time.Second)
	var last int64
	for {
		code, r, _ := callTableSizes(t, h, "")
		if code != http.StatusOK {
			t.Fatalf("polling status = %d", code)
		}
		last = r.TotalBytes
		if last > t1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("后台刷新后仍未拿到新读数（始终 = %d）—— 刷新 goroutine 断了", last)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Logf("ttl=60ms t1=%d 过期响应=%d(%v) 刷新后=%d", t1, r2.TotalBytes, d2, last)
}

// TestDataLifecycleTableSizesConcurrentCallsAggregateOnce 并发 N 次只查一次库。
func TestDataLifecycleTableSizesConcurrentCallsAggregateOnce(t *testing.T) {
	h, _, _ := newStoragePerfHandler(t)

	const n = 8
	var wg sync.WaitGroup
	codes := make([]int, n)
	resps := make([]tableSizesResponse, n)
	start := make(chan struct{})

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			codes[i], resps[i], _ = callTableSizes(t, h, "")
		}(i)
	}
	close(start)
	wg.Wait()

	if got := tableSizesCollectCount(); got != 1 {
		t.Errorf("concurrent collect count = %d, want 1 —— %d 个并发请求重复打了库", got, n)
	}
	for i := 0; i < n; i++ {
		if codes[i] != http.StatusOK {
			t.Errorf("call[%d] status = %d, want 200", i, codes[i])
		}
		if len(resps[i].Tables) == 0 {
			t.Errorf("call[%d] degraded: 无数据", i)
		}
		if !resps[i].CollectedAt.Equal(resps[0].CollectedAt) {
			t.Errorf("call[%d] 拿到不同快照（%v vs %v）—— 单飞没有合并", i, resps[i].CollectedAt, resps[0].CollectedAt)
		}
	}
}

// TestDataLifecycleTableSizesNonGetStillRejected 方法契约不能被缓存改动波及。
func TestDataLifecycleTableSizesNonGetStillRejected(t *testing.T) {
	h, _, _ := newStoragePerfHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/data-lifecycle/storage/tables", nil)
	rec := httptest.NewRecorder()
	h.handleDataLifecycleTableSizes(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", rec.Code)
	}
}
