package apihub

// 批量 upsert 的真库回归（2026-10-05，runbook §10.30）
//
// 无 TEST_DATABASE_URL / TEST_DB_URL 时跳过（同 db/db_750_ensure_realdb_test.go
// 与 bg/ursm_825_realdb_test.go 的门控纪律）。
//
// ★ 这个文件存在的理由：apihub/batch_upsert_test.go 里的 7 条门**全是结构门**
//   （占位符编号、参数上限、批量与单行的 WHERE 逐字一致、memStore 上的行为）。
//   它们**没有一个真的让 PostgreSQL 执行过那条多值 SQL**。
//
//   而这条 SQL 的风险恰恰集中在「PG 会不会接受」上：
//   多值 INSERT 里每行的 $N 编号、末尾那个共用窗口参数、
//   以及 ON CONFLICT 的 WHERE 在「一次冲突多行」时的语义 ——
//   任何解析器层面的错误都不可能从源码门里看出来。
//
//   §10.29 那次手工 BEGIN…ROLLBACK 验证证明它能跑，但那是一次性的、
//   不可重复的。把它固化成门，才叫「可验证」而不是「我跑过一次」。

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func connectBatch(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过批量 upsert 真库回归")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("connect real db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// batchTestRefBase 是本测试占用的 ref_id 段。
// ★ 必须是**别人不会用的段**且必须在 cleanup 里删干净 —— 这个测试
//   打的是**真实的 public.assets 表**，不是临时表（因为 SQL 里表名是硬编码的，
//   换成临时表就等于改了被测物）。
const batchTestRefBase = 99000000

func cleanupBatchRows(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	_, err := pool.Exec(ctx,
		`DELETE FROM public.assets WHERE ref_id >= $1 AND ref_id < $2`,
		batchTestRefBase, batchTestRefBase+1000)
	if err != nil {
		t.Fatalf("清理测试行失败: %v", err)
	}
}

func mkBatchTestAsset(i int) Asset {
	return Asset{
		Kind:        KindLLMEndpoint,
		RefID:       int64(batchTestRefBase + i),
		TenantID:    "batchtest",
		Name:        "batch-" + itoa(int64(i)),
		Tags:        map[string]string{"i": itoa(int64(i))},
		HealthState: HealthUnknown,
		Version:     "0.0.0",
		Metadata:    map[string]any{"idx": i},
	}
}

// ── 门 A：多行 SQL 必须在真 PG 上跑通并正确落库 ────────────────────────
//
// 覆盖：每行的 $N 编号是否正确（错位会让 name/ref_id 串行）、
// 末尾共用窗口参数的位置、共用参数在多行冲突时是否只求值一次。
func TestBatchRealDBWritesAllRows(t *testing.T) {
	pool := connectBatch(t)
	cleanupBatchRows(t, pool)
	t.Cleanup(func() { cleanupBatchRows(t, pool) })

	store := NewPGStore(pool)
	const n = 25
	assets := make([]Asset, 0, n)
	for i := 1; i <= n; i++ {
		assets = append(assets, mkBatchTestAsset(i))
	}
	if err := store.UpsertBatch(context.Background(), assets); err != nil {
		t.Fatalf("UpsertBatch: %v", err)
	}

	ctx := context.Background()
	var got int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM public.assets
		 WHERE ref_id >= $1 AND ref_id < $2 AND tenant_id = 'batchtest'`,
		batchTestRefBase, batchTestRefBase+1000).Scan(&got); err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != n {
		t.Fatalf("落库 %d 行，期望 %d", got, n)
	}

	// 逐行核对 name / tags / metadata —— 错位最常见的表现是这些字段串行。
	rows, err := pool.Query(ctx,
		`SELECT ref_id, name, tags->>'i', metadata->>'idx'
		 FROM public.assets
		 WHERE ref_id >= $1 AND ref_id < $2 ORDER BY ref_id`,
		batchTestRefBase, batchTestRefBase+1000)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	checked := 0
	for rows.Next() {
		var refID int64
		var name, tagI, metaIdx string
		if err := rows.Scan(&refID, &name, &tagI, &metaIdx); err != nil {
			t.Fatalf("scan: %v", err)
		}
		want := itoa(refID - int64(batchTestRefBase))
		if name != "batch-"+want || tagI != want || metaIdx != want {
			t.Errorf("ref_id=%d 字段错位：name=%q tags.i=%q metadata.idx=%q，期望后缀都是 %q",
				refID, name, tagI, metaIdx, want)
		}
		checked++
	}
	if checked != n {
		t.Errorf("核对 %d 行，期望 %d", checked, n)
	}
}

// ── 门 B（本文件最要紧的一条）：重复跑同一批，门控必须让 last_seen_at 不动 ──
//
// ★ 这是 §10.29 整件事的**存在理由**：门控（§10.24 的 A 方案）让 93.7% 的
//   upsert 什么都不做。它在**单行**形态下被验证过（A 方案上线时看过
//   n_tup_upd 降幅），但**多行**形态下 ON CONFLICT 的 WHERE 是否同样生效，
//   只有真库能回答。
//
// 假如多行下 WHERE 失效，现象是：每 tick 把 1306 行全刷一遍 last_seen_at
// ⇒ 写放大回到改动前，而 n_tup_upd 只会显示「比预期高一点」，
// **没有任何告警会响**。这与门 3（批量与单行 WHERE 逐字一致）守的是同一个风险，
// 但那条是源码比较，这条是行为实测 —— 两者不可互相替代。
func TestBatchRealDBGateBlocksSecondIdenticalRun(t *testing.T) {
	pool := connectBatch(t)
	cleanupBatchRows(t, pool)
	t.Cleanup(func() { cleanupBatchRows(t, pool) })

	store := NewPGStore(pool)
	const n = 20
	assets := make([]Asset, 0, n)
	for i := 1; i <= n; i++ {
		assets = append(assets, mkBatchTestAsset(i))
	}
	ctx := context.Background()

	if err := store.UpsertBatch(ctx, assets); err != nil {
		t.Fatalf("首轮 UpsertBatch: %v", err)
	}
	// 等一帧，确保 now() 有可观测的推进（否则两次 last_seen_at 可能相同）
	time.Sleep(1100 * time.Millisecond)

	before := map[int64]time.Time{}
	rows, err := pool.Query(ctx,
		`SELECT ref_id, last_seen_at FROM public.assets
		 WHERE ref_id >= $1 AND ref_id < $2`, batchTestRefBase, batchTestRefBase+1000)
	if err != nil {
		t.Fatalf("query before: %v", err)
	}
	for rows.Next() {
		var id int64
		var ts time.Time
		if err := rows.Scan(&id, &ts); err != nil {
			t.Fatalf("scan before: %v", err)
		}
		before[id] = ts
	}
	rows.Close()
	if len(before) != n {
		t.Fatalf("首轮落库 %d 行，期望 %d", len(before), n)
	}

	// 完全相同的一批再跑一次
	if err := store.UpsertBatch(ctx, assets); err != nil {
		t.Fatalf("次轮 UpsertBatch: %v", err)
	}

	changed := 0
	rows2, err := pool.Query(ctx,
		`SELECT ref_id, last_seen_at FROM public.assets
		 WHERE ref_id >= $1 AND ref_id < $2`, batchTestRefBase, batchTestRefBase+1000)
	if err != nil {
		t.Fatalf("query after: %v", err)
	}
	defer rows2.Close()
	for rows2.Next() {
		var id int64
		var ts time.Time
		if err := rows2.Scan(&id, &ts); err != nil {
			t.Fatalf("scan after: %v", err)
		}
		if !ts.Equal(before[id]) {
			changed++
		}
	}
	if changed != 0 {
		t.Errorf("第二轮有 %d/%d 行的 last_seen_at 被改写，期望 0。\n"+
			"  ⇒ 多值 INSERT 的 ON CONFLICT ... WHERE 没有生效。\n"+
			"  后果是每 tick 全量刷新 1306 行 ⇒ 写放大回到 §10.29 的 546 万次/天，\n"+
			"  而 n_tup_upd 只会显示「略高」，不会有任何告警。", changed, n)
	}
}

// ── 门 C：业务字段真变了时，**必须**立刻落库（不能被心跳窗口挡住）─────
//
// 与门 B 相反方向。若批量版只有心跳那一半而漏了 IS DISTINCT FROM 那一半，
// 「改了名字要等 5 分钟才可见」。这个方向同样在数据上不可见。
func TestBatchRealDBWritesWhenBusinessFieldChanges(t *testing.T) {
	pool := connectBatch(t)
	cleanupBatchRows(t, pool)
	t.Cleanup(func() { cleanupBatchRows(t, pool) })

	store := NewPGStore(pool)
	ctx := context.Background()
	assets := []Asset{mkBatchTestAsset(1)}
	if err := store.UpsertBatch(ctx, assets); err != nil {
		t.Fatalf("首轮: %v", err)
	}

	// 只改 name，其余完全相同
	changed := mkBatchTestAsset(1)
	changed.Name = "batch-1-renamed"
	if err := store.UpsertBatch(ctx, []Asset{changed}); err != nil {
		t.Fatalf("次轮: %v", err)
	}

	var name string
	var lastSeen time.Time
	if err := pool.QueryRow(ctx,
		`SELECT name, last_seen_at FROM public.assets WHERE ref_id = $1`,
		batchTestRefBase+1).Scan(&name, &lastSeen); err != nil {
		t.Fatalf("query: %v", err)
	}
	if name != "batch-1-renamed" {
		t.Errorf("name = %q，期望 %q ⇒ 批量版漏了 IS DISTINCT FROM 那一半，"+
			"业务变更被心跳窗口挡住了（改动要等最多 5 分钟才可见）", name, "batch-1-renamed")
	}
}
