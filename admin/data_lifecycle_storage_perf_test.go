// 存储总览快照缓存的判据（2026-10-16 性能轮）。
//
// 背景：GET /api/admin/data-lifecycle/storage 是本机 416 个 admin GET 里
// 唯一 >2s 的路径（首次 2.327s，复测 1.44–1.76s）。逐段 \timing 分解见
// data_lifecycle_storage.go 的 handler 注释：SUM(pg_total_relation_size)
// 全用户 schema（3362 关系）独占 1.62–1.72s（85%），并发化最多只能把
// 2.24s 压到 ~1.93s，仍远超 300ms 目标，故走短 TTL 快照。
//
// 判据纪律（对齐 LESSONS）：
//   - 每条"命中"断言都配一条"前提仍在场"的断言：既证明缓存不再重复聚合，
//     也证明数据源本身没坏（真建表、真比对字节数），避免"看不见"被当成
//     "没发生"。
//   - 不只断言 status==200：并发用例断言聚合**实打实只跑一次**。
//
// 运行：
//
//	TEST_DATABASE_URL="postgres://…@127.0.0.1:54318/llm_gateway?sslmode=disable" \
//	go test ./admin -run TestDataLifecycleStorage -count=1 -v
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// callStorageOverview 走真实 handler，返回状态码、解码后的响应与耗时。
func callStorageOverview(t *testing.T, h *Handler) (int, storageOverview, time.Duration) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/data-lifecycle/storage", nil)
	rec := httptest.NewRecorder()

	start := time.Now()
	h.handleDataLifecycleStorage(rec, req)
	elapsed := time.Since(start)

	var ov storageOverview
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &ov); err != nil {
			t.Fatalf("decode overview: %v (body=%.200s)", err, rec.Body.String())
		}
	}
	return rec.Code, ov, elapsed
}

// newStoragePerfHandler 连真实 PG 并返回隔离好的 handler。
//
// 每个用例独立 schema（dlperf_<test>_<pid>_<nanos>），退出时 DROP CASCADE。
// 快照按 *pgxpool.Pool 分桶，池一换就是一份全新缓存，用例之间天然隔离。
func newStoragePerfHandler(t *testing.T) (*Handler, *pgxpool.Pool, string) {
	t.Helper()
	// DSN 回退链必须与 admin500DSN 同构。此前只认 TEST_DATABASE_URL，
	// 而本仓真库判据的实际来源是 LLM_GATEWAY_DATABASE_URL
	// （见 /tmp/lgw-env.sh 与 scripts/audit/run-integration-gate.sh），
	// 于是这些用例在**每一个**真实环境里都静默 SKIP —— 报 PASS 却一测未测，
	// 快照缓存/singleflight 优化等于没有判据守护。
	dbURL := ""
	for _, key := range []string{"TEST_DATABASE_URL", "LLM_GATEWAY_DATABASE_URL", "DATABASE_URL"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			dbURL = v
			break
		}
	}
	if dbURL == "" {
		t.Skip("set TEST_DATABASE_URL (or LLM_GATEWAY_DATABASE_URL); skipping real-DB perf test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping: %v", err)
	}

	schema := fmt.Sprintf("dlperf_%s_%d_%d",
		sanitizeSchemaToken(t.Name()), os.Getpid(), time.Now().UnixNano())
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		pool.Close()
		t.Fatalf("create schema %s: %v", schema, err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer dropCancel()
		if _, err := pool.Exec(dropCtx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
			t.Errorf("cleanup: drop schema %s: %v", schema, err)
		}
		// 残留=0 是硬要求，落成断言而不是"清理没报错就算过"
		var n int
		if err := pool.QueryRow(dropCtx,
			"SELECT count(*) FROM pg_namespace WHERE nspname LIKE 'dlperf\\_%'").Scan(&n); err != nil {
			t.Errorf("cleanup: residue check: %v", err)
		} else if n != 0 {
			t.Errorf("cleanup: %d isolated schema(s) left behind", n)
		}
		pool.Close()
	})

	resetDataLifecycleStorageCacheForTest()
	t.Cleanup(resetDataLifecycleStorageCacheForTest)
	return &Handler{db: pool}, pool, schema
}

func sanitizeSchemaToken(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + 32)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// TestDataLifecycleStorageOverviewFieldsAreComplete 端点返回 200 且字段齐全。
//
// 除了解码后的具体值，还逐个核对 raw JSON 的键名 —— 前端
// StorageOverview.vue / tuning.ts 的 TS 类型依赖这些键，缺一个就是契约破坏。
func TestDataLifecycleStorageOverviewFieldsAreComplete(t *testing.T) {
	h, pool, _ := newStoragePerfHandler(t)
	ctx := context.Background()

	req := httptest.NewRequest(http.MethodGet, "/api/admin/data-lifecycle/storage", nil)
	rec := httptest.NewRecorder()
	h.handleDataLifecycleStorage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%.300s)", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	// 前端消费方实际读取的键（web/src/api/tuning.ts:399-443）
	for _, key := range []string{
		`"database_bytes"`, `"database_human"`, `"total_bytes"`, `"total_human"`,
		`"tables_bytes"`, `"indexes_bytes"`, `"toast_bytes"`,
		`"free_bytes"`, `"free_human"`, `"server_version"`,
		`"available"`, `"table_count"`, `"total_columns"`,
		`"path"`, `"used_percent"`, `"used_human"`, `"free_human"`,
		`"local_logs"`, `"warnings"`, `"collected_at"`,
	} {
		if !strings.Contains(body, key) {
			t.Errorf("response missing key %s (body=%.400s)", key, body)
		}
	}

	var ov storageOverview
	if err := json.Unmarshal([]byte(body), &ov); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// 具体值断言，不是"非空"
	if ov.Database.DatabaseBytes <= 0 {
		t.Errorf("DatabaseBytes = %d, want > 0", ov.Database.DatabaseBytes)
	}
	if ov.Database.TotalBytes <= 0 {
		t.Errorf("TotalBytes = %d, want > 0", ov.Database.TotalBytes)
	}
	// ★ 2026-10-21（R67 / C6 改判）：**跨口径**的不变式是错的，撤掉。
	//
	//   TotalBytes   = 用户 schema 的 SUM(pg_total_relation_size)（**含** TOAST）
	//   DatabaseBytes = pg_database_size()（**整库**，含 pg_catalog / information_schema）
	//   ⇒ 两者根本不是同一个口径，「TotalBytes ≥ DatabaseBytes」在
	//     「系统目录占比大」的库上**必然不成立**（新装库/空库正是这种）。
	//   实测（一次性库 itgate_r59_shape）：
	//     pg_database_size = 44,996,275 / 用户 schema 合计 = 42,360,832
	//     pg_catalog 单独 = 43,950,080 ⇒ 差值就是系统目录。
	//   ⇒ R59 记的「总量漏了 TOAST」被**证伪**：pg_total_relation_size 本就含 TOAST。
	//   ⇒ 换成两条**同口径**判据：
	//   ① 总量 ≥ 它自己的三段拆分之和（环境无关）；
	//   ② 总量与 PG **实时同口径**求和一致（±1%）—— 这才是「总量没漏」的真判据。
	if split := ov.Database.TablesBytes + ov.Database.IndexesBytes + ov.Database.ToastBytes; ov.Database.TotalBytes < split {
		t.Errorf("TotalBytes(%d) < 表+索引+TOAST(%d)：同口径下总量不该小于它的拆分之和",
			ov.Database.TotalBytes, split)
	}
	var liveTotal int64
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(pg_total_relation_size(c.oid)), 0)::bigint
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname NOT IN ('pg_catalog','information_schema')
	`).Scan(&liveTotal); err != nil {
		t.Fatalf("读 PG 实时同口径总量失败：%v", err)
	}
	if liveTotal > 0 {
		diff := ov.Database.TotalBytes - liveTotal
		if diff < 0 {
			diff = -diff
		}
		if allowed := liveTotal / 100; diff > allowed {
			t.Errorf("TotalBytes(%d) 与 PG 实时同口径求和(%d) 差 %d 字节 > 容差 %d —— 总量口径漏了东西",
				ov.Database.TotalBytes, liveTotal, diff, allowed)
		}
	}
	if ov.Database.TablesBytes <= 0 {
		t.Errorf("TablesBytes = %d, want > 0", ov.Database.TablesBytes)
	}
	if ov.Database.IndexesBytes <= 0 {
		t.Errorf("IndexesBytes = %d, want > 0 (本库有索引)", ov.Database.IndexesBytes)
	}
	if ov.Database.ServerVersion == "" {
		t.Error("ServerVersion empty")
	}
	if ov.Database.TotalHuman != humanBytes(ov.Database.TotalBytes) {
		t.Errorf("TotalHuman = %q, inconsistent with TotalBytes=%d",
			ov.Database.TotalHuman, ov.Database.TotalBytes)
	}
	if ov.Filesystem.TotalBytes <= 0 || ov.Filesystem.UsedPercent < 0 || ov.Filesystem.UsedPercent > 100 {
		t.Errorf("filesystem implausible: %+v", ov.Filesystem)
	}
	if ov.CollectedAt.IsZero() {
		t.Error("CollectedAt is zero")
	}
	if ov.Warnings == nil {
		t.Error("Warnings nil（前端 TS 类型是 string[]，必须序列化为 [] 而非 null）")
	}
	// local_logs：resolveLogDir 永远返回路径，Exists 可为 false，但对象必须在
	if ov.LocalLogs == nil {
		t.Error("LocalLogs nil；StorageOverview.vue:116 之后整块字段都会被读到")
	}

	// 前提仍在场：这份响应必须真的来自 PG，不是零值壳
	if got := dataLifecycleStorageCollectCount(); got != 1 {
		t.Errorf("collect count = %d, want 1（首调必须真打一次 PG）", got)
	}
}

// TestDataLifecycleStorageSecondCallSkipsAggregation 第二次调用命中快照。
//
// 三重断言：① 聚合次数不再增长（不是"快了一下"，是"没再聚合"）；
// ② 耗时跨过 300ms 目标线；③ 响应内容与首调逐字段一致（契约不变）。
func TestDataLifecycleStorageSecondCallSkipsAggregation(t *testing.T) {
	h, _, _ := newStoragePerfHandler(t)

	code1, ov1, d1 := callStorageOverview(t, h)
	if code1 != http.StatusOK {
		t.Fatalf("first call status = %d", code1)
	}
	if got := dataLifecycleStorageCollectCount(); got != 1 {
		t.Fatalf("after first call collect count = %d, want 1", got)
	}

	code2, ov2, d2 := callStorageOverview(t, h)
	if code2 != http.StatusOK {
		t.Fatalf("second call status = %d", code2)
	}

	if got := dataLifecycleStorageCollectCount(); got != 1 {
		t.Errorf("after second call collect count = %d, want 1 —— 聚合被重复执行了", got)
	}
	if got := dataLifecycleStorageCacheHitCount(); got != 1 {
		t.Errorf("cache hit count = %d, want 1", got)
	}
	if d2 >= 300*time.Millisecond {
		t.Errorf("second call = %v, want < 300ms（热调用目标）", d2)
	}
	if d2 >= d1 {
		t.Errorf("second call %v not faster than first %v", d2, d1)
	}

	// 命中快照不得改变响应内容
	if ov2.Database != ov1.Database {
		t.Errorf("database block differs on cache hit:\n1: %+v\n2: %+v", ov1.Database, ov2.Database)
	}
	if !ov2.CollectedAt.Equal(ov1.CollectedAt) {
		t.Errorf("CollectedAt changed on cache hit: %v → %v", ov1.CollectedAt, ov2.CollectedAt)
	}
	if len(ov2.Warnings) != len(ov1.Warnings) {
		t.Errorf("warnings changed on cache hit: %v → %v", ov1.Warnings, ov2.Warnings)
	}

	t.Logf("cold=%v hot=%v speedup=%.1fx", d1, d2, float64(d1)/float64(d2))
}

// TestDataLifecycleStorageInvalidateRefetches 是本文件最硬的一条判据：
// 真的在隔离 schema 里建表、用真实字节数证明"失效后确实重新取数"。
//
// 三段：
//
//	① 建表 A → 首调 T1（快照）
//	② 建表 B（更大）→ 再调仍为 T1 —— 证明快照确实在生效（而非每次重查）
//	③ 失效 → 再调 T3 > T1 —— 证明失效真的重新打了 PG
//
// 第②步是"前提仍在场"的那一半：没有它，第③步的 T3 > T1 也可能只是
// 每步都在重查而已。
func TestDataLifecycleStorageInvalidateRefetches(t *testing.T) {
	h, pool, schema := newStoragePerfHandler(t)

	// 建表 A（灌 ~2MB，让字节数变化大于噪声）
	if _, err := pool.Exec(context.Background(),
		fmt.Sprintf("CREATE TABLE %s.t_a (id bigserial PRIMARY KEY, pad text)", schema)); err != nil {
		t.Fatalf("create t_a: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		fmt.Sprintf("INSERT INTO %s.t_a (pad) SELECT repeat('x', 1024) FROM generate_series(1, 2000)",
			schema)); err != nil {
		t.Fatalf("fill t_a: %v", err)
	}

	code1, ov1, _ := callStorageOverview(t, h)
	if code1 != http.StatusOK {
		t.Fatalf("first status = %d", code1)
	}
	t1 := ov1.Database.TotalBytes
	if t1 <= 0 {
		t.Fatalf("TotalBytes after t_a = %d, want > 0", t1)
	}
	if got := dataLifecycleStorageCollectCount(); got != 1 {
		t.Fatalf("collect count = %d, want 1", got)
	}

	// ② 再建一张更大的表 B，快照期内总字节数**不应**变化
	if _, err := pool.Exec(context.Background(),
		fmt.Sprintf("CREATE TABLE %s.t_b (id bigserial PRIMARY KEY, pad text)", schema)); err != nil {
		t.Fatalf("create t_b: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		fmt.Sprintf("INSERT INTO %s.t_b (pad) SELECT repeat('y', 4096) FROM generate_series(1, 6000)",
			schema)); err != nil {
		t.Fatalf("fill t_b: %v", err)
	}
	// 直接问 PG，确认 t_b 确实让真实总量涨了（否则③的断言没有意义）
	var liveTotal int64
	if err := pool.QueryRow(context.Background(), `
		SELECT COALESCE(SUM(pg_total_relation_size(c.oid)),0)::bigint
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname NOT IN ('pg_catalog','information_schema')`).Scan(&liveTotal); err != nil {
		t.Fatalf("probe live total: %v", err)
	}
	if liveTotal <= t1 {
		t.Fatalf("前提不在场：t_b 未让 PG 真实总量增长（live=%d ≤ t1=%d）", liveTotal, t1)
	}
	t.Logf("t1=%d liveAfterB=%d", t1, liveTotal)

	code2, ov2, _ := callStorageOverview(t, h)
	if code2 != http.StatusOK {
		t.Fatalf("second status = %d", code2)
	}
	if got := ov2.Database.TotalBytes; got != t1 {
		t.Errorf("snapshot not in effect: TotalBytes=%d, want %d (unchanged within TTL)", got, t1)
	}
	if got := dataLifecycleStorageCollectCount(); got != 1 {
		t.Fatalf("collect count = %d, want 1 before invalidation", got)
	}

	// ③ 失效 → 必须重新取数，且应贴近 PG 的实时值
	InvalidateDataLifecycleStorageCache()

	code3, ov3, _ := callStorageOverview(t, h)
	if code3 != http.StatusOK {
		t.Fatalf("third status = %d", code3)
	}
	if got := dataLifecycleStorageCollectCount(); got != 2 {
		t.Errorf("collect count after invalidate = %d, want 2（失效后必须重新聚合）", got)
	}
	t3 := ov3.Database.TotalBytes
	if t3 <= t1 {
		t.Errorf("TotalBytes after invalidate = %d, want > %d —— 失效后没有重新取数", t3, t1)
	}
	// 允许 1% 抖动（聚合期间其它连接也在写库），但必须真的接近实时值
	if t3 < liveTotal*99/100 || t3 > liveTotal*101/100 {
		t.Errorf("TotalBytes after invalidate = %d, want ≈ live %d (±1%%)", t3, liveTotal)
	}
	t.Logf("t1=%d t3=%d live=%d", t1, t3, liveTotal)
}

// TestDataLifecycleStorageConcurrentCallsAggregateOnce 并发 N 次只聚合一次。
func TestDataLifecycleStorageConcurrentCallsAggregateOnce(t *testing.T) {
	h, _, _ := newStoragePerfHandler(t)

	const n = 8
	var wg sync.WaitGroup
	codes := make([]int, n)
	ovs := make([]storageOverview, n)
	start := make(chan struct{})

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // 同时起跑，制造真实的并发 miss
			codes[i], ovs[i], _ = callStorageOverview(t, h)
		}(i)
	}
	close(start)
	wg.Wait()

	if got := dataLifecycleStorageCollectCount(); got != 1 {
		t.Errorf("concurrent collect count = %d, want 1 —— %d 个并发 miss 重复打了库", got, n)
	}
	for i := 0; i < n; i++ {
		if codes[i] != http.StatusOK {
			t.Errorf("call[%d] status = %d, want 200", i, codes[i])
		}
		if ovs[i].Database.TotalBytes <= 0 {
			t.Errorf("call[%d] degraded: TotalBytes=%d", i, ovs[i].Database.TotalBytes)
		}
		if !ovs[i].CollectedAt.Equal(ovs[0].CollectedAt) {
			t.Errorf("call[%d] got a different snapshot (%v vs %v) —— 单飞没有合并",
				i, ovs[i].CollectedAt, ovs[0].CollectedAt)
		}
	}
}

// TestDataLifecycleStorageTTLExpiryRefetches TTL 到期必须重查，
// 并先钉住生产默认值的上界。
//
// 2026-10-17 契约变更（stale-while-revalidate）：到期后**不再同步重算**，
// 而是立即返回旧读数 + 后台单飞聚合。所以"重查发生了"不能靠本次调用的
// 返回值证明——必须按结果轮询（collect 计数在查询*开始前*就自增，
// 拿它当"完成"信号会读到尚未写回的旧快照）。
//
// 三段：③ 到期后的调用**不得阻塞**且返回旧读数；
//
//	④⑤ 轮询到新读数真的可见（同时覆盖"刷新没发生"与"刷新太慢"）。
func TestDataLifecycleStorageTTLExpiryRefetches(t *testing.T) {
	h, pool, schema := newStoragePerfHandler(t)

	// ① 先钉住**生产默认值**的上界。这一条不能被下面的临时覆盖掩盖：
	// 把 TTL 调成 365 天时数据会永久陈旧，而显式失效用例照样全绿
	// （显式失效不看 TTL），只有这条能红。
	if storageOverviewTTL <= 0 || storageOverviewTTL > 60*time.Second {
		t.Fatalf("storageOverviewTTL = %v, want (0, 60s] —— TTL 过大会让数据永久陈旧", storageOverviewTTL)
	}

	orig := storageOverviewTTL
	storageOverviewTTL = 60 * time.Millisecond
	t.Cleanup(func() { storageOverviewTTL = orig })

	if _, err := pool.Exec(context.Background(),
		fmt.Sprintf("CREATE TABLE %s.t_ttl (id bigint)", schema)); err != nil {
		t.Fatalf("create t_ttl: %v", err)
	}

	code1, ov1, d1 := callStorageOverview(t, h)
	if code1 != http.StatusOK {
		t.Fatalf("first status = %d", code1)
	}
	t1 := ov1.Database.TotalBytes
	if got := dataLifecycleStorageCollectCount(); got != 1 {
		t.Fatalf("collect count = %d, want 1", got)
	}

	// TTL 期内：不应重查
	callStorageOverview(t, h)
	if got := dataLifecycleStorageCollectCount(); got != 1 {
		t.Fatalf("within TTL collect count = %d, want 1", got)
	}

	// 制造可观测变化，再等 TTL 到期
	if _, err := pool.Exec(context.Background(),
		fmt.Sprintf("INSERT INTO %s.t_ttl SELECT generate_series(1, 200000)", schema)); err != nil {
		t.Fatalf("fill t_ttl: %v", err)
	}
	time.Sleep(150 * time.Millisecond) // > TTL

	// ③ 到期后的调用：给旧读数，且**不得**为它阻塞
	code2, ov2, d2 := callStorageOverview(t, h)
	if code2 != http.StatusOK {
		t.Fatalf("second status = %d", code2)
	}
	if d2 >= 300*time.Millisecond {
		t.Errorf("过期后的调用 = %v，want < 300ms —— 过期后必须走旧读数而不是同步重算", d2)
	}
	if ov2.Database.TotalBytes != t1 {
		t.Errorf("过期调用应返回旧读数 %d，实际 %d", t1, ov2.Database.TotalBytes)
	}

	// ④⑤ 轮询到新读数真的可见（不能只等 collect 计数，见函数注释）
	deadline := time.Now().Add(25 * time.Second)
	var last int64
	for {
		code, ov, _ := callStorageOverview(t, h)
		if code != http.StatusOK {
			t.Fatalf("polling status = %d", code)
		}
		last = ov.Database.TotalBytes
		if last > t1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("后台刷新后仍未拿到新读数（始终 = %d）—— 刷新 goroutine 断了", last)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Logf("ttl=60ms t1=%d 首调=%v 过期响应=%d(%v) 刷新后=%d", t1, d1, ov2.Database.TotalBytes, d2, last)
}

// TestDataLifecycleStorageNonGetStillRejected 方法契约不能被缓存改动波及。
func TestDataLifecycleStorageNonGetStillRejected(t *testing.T) {
	h, _, _ := newStoragePerfHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/data-lifecycle/storage", nil)
	rec := httptest.NewRecorder()
	h.handleDataLifecycleStorage(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", rec.Code)
	}
}
