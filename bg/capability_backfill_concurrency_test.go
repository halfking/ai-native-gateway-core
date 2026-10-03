// capability_backfill_concurrency_test.go — 多实例并发 upsert 实测（2026-10-03）。
//
// handoff 遗留 5 原文：「**多实例并发 upsert 未实测**（ON CONFLICT 最后写者胜，
// 结论不撕裂，但没测）」。本文件把那半句「但没测」补上。
//
// 为什么值得单独测而不是读代码就签字：蓝绿部署下两个实例会在同一个
// dist-lock 交接窗口里同时跑一轮（锁 TTL 25min，而一轮最坏 ~17min），
// 所以「同一行被两个实例同时 upsert」不是理论场景。`ON CONFLICT DO UPDATE`
// 在并发下会不会报 unique violation、会不会留下两行、结论会不会撕裂
// （supported 变成两值之间谁都说不清的第三态），都只能实测。
//
// 需要真实 PG：TEST_DATABASE_URL 未设置则跳过（与 bg 包既有约定一致）。
package bg

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// concurrencyPool 起一个测试库连接，并在 t.Cleanup 里清掉本测试造的行。
func concurrencyPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping PG-backed concurrency test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	var ok bool
	if err := pool.QueryRow(ctx,
		`SELECT to_regclass('public.credential_model_capabilities') IS NOT NULL`).Scan(&ok); err != nil {
		t.Fatalf("probe schema: %v", err)
	}
	if !ok {
		t.Skip("credential_model_capabilities missing — run migrations 612/613 first")
	}
	return pool
}

// bindingID 取一个不与既有数据冲突的 binding id。
func bindingID(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	ctx := context.Background()
	// 不新建 binding（那要满足一堆外键）：直接借一个已存在的 id，
	// 并在 cleanup 里把本测试写的行删掉，避免污染真实库。
	var id int64
	err := pool.QueryRow(ctx,
		`SELECT id FROM credential_model_bindings ORDER BY id LIMIT 1`).Scan(&id)
	if err != nil {
		t.Skipf("no credential_model_bindings row available: %v", err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cctx,
			`DELETE FROM credential_model_capabilities
			  WHERE credential_model_binding_id = $1 AND capability = $2`,
			id, CapabilityNonstream)
	})
	return id
}

// TestUpsert_ConcurrentInstancesDoNotTear is the claim under test.
//
// 8 个 goroutine 各写一次同一行，结论一半 true 一半 false。
// 断言三件事，任一不成立就是「结论撕裂」：
//  1. 没有任何一次 upsert 报 unique violation（并发 INSERT 撞车）；
//  2. 最终**只有一行**（不是两行）；
//  3. 最终 supported 是 true 或 false 之一 —— 不是第三态。
func TestUpsert_ConcurrentInstancesDoNotTear(t *testing.T) {
	pool := concurrencyPool(t)
	id := bindingID(t, pool)

	const workers = 8
	b := &CapabilityBackfill{db: pool}
	ctx := context.Background()

	// 先清干净，确保起点是「无行」。
	if _, err := pool.Exec(ctx,
		`DELETE FROM credential_model_capabilities
		  WHERE credential_model_binding_id = $1 AND capability = $2`,
		id, CapabilityNonstream); err != nil {
		t.Fatalf("pre-clean: %v", err)
	}

	var wg sync.WaitGroup
	errs := make([]error, workers)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // 让 8 个 goroutine 尽量同时发起，逼出真正的并发窗口
			supported := i%2 == 0
			errs[i] = b.persistRow(ctx, dueBinding{
				BindingID: id, CredentialID: 1, RawModel: "concurrency-probe",
			}, supported, []byte(fmt.Sprintf(`{"worker":%d}`, i)))
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("worker %d upsert failed: %v", i, err)
		}
	}
	if t.Failed() {
		t.FailNow()
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM credential_model_capabilities
		  WHERE credential_model_binding_id = $1 AND capability = $2`,
		id, CapabilityNonstream).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if n != 1 {
		t.Fatalf("同一 (binding, capability) 落了 %d 行，want 1：并发 upsert 撕裂了行数。", n)
	}

	var supported bool
	var evidence string
	if err := pool.QueryRow(ctx,
		`SELECT supported, evidence_json::text FROM credential_model_capabilities
		  WHERE credential_model_binding_id = $1 AND capability = $2`,
		id, CapabilityNonstream).Scan(&supported, &evidence); err != nil {
		t.Fatalf("read final: %v", err)
	}
	t.Logf("最终 supported=%v evidence=%s（最后写者胜）", supported, evidence)
	// supported 是 NOT NULL BOOLEAN，Scan 成功即证明没有第三态。
	// 这里再断言一次「结论非空」是为了让意图显式：读到 NULL 会在这里失败。
	if !supported && evidence == "" {
		t.Fatal("最终行既不支持也无证据：结论撕裂成了空态")
	}
}

// TestUpsert_ConcurrentDifferentCapabilitiesDoNotCollide 覆盖另一个轴：
// 同一 binding 的**不同 capability** 行必须互不覆盖。
//
// 613 把 stream 拆成独立键之后，一个 binding 会有两行。这一条保证
// 并发写两条不同 capability 时不会互相踩。
//
// ⚠️ 2026-10-03：第一版这条判据是**假绿**的，而且是被它自己的日志抓到的——
// 日志打「该 binding 现有 1 行」，而两个 upsert 都报成功。根因：
// `persistRow` 把 capability 键**写死**成 CapabilityNonstream（这是它的
// 生产行为：本任务只写非流式），而我传的 `cap` 变量根本没进 SQL。
// 于是两个 goroutine 写的是**同一行**，「互不覆盖」压根没被测。
//
// ⇒ 这正是「判据的场景形状没对上被测对象」的又一例：被测函数根本不接受
// capability 参数时，用例必须在**更低的层**（真实 SQL）上写两行，
// 而不是调两次同一个函数然后数行数。
func TestUpsert_ConcurrentDifferentCapabilitiesDoNotCollide(t *testing.T) {
	pool := concurrencyPool(t)
	id := bindingID(t, pool)
	ctx := context.Background()
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cctx,
			`DELETE FROM credential_model_capabilities WHERE credential_model_binding_id = $1`, id)
	})
	if _, err := pool.Exec(ctx,
		`DELETE FROM credential_model_capabilities WHERE credential_model_binding_id = $1`, id); err != nil {
		t.Fatalf("pre-clean: %v", err)
	}

	// 关键：persistRow 写死 capability，所以这里**直接发 SQL**，
	// 才能真正造出「同一 binding、两个不同 capability」并发写入。
	const upsertSQL = `
		INSERT INTO credential_model_capabilities
		       (credential_model_binding_id, capability, supported, last_tested_at, evidence_json, updated_at)
		VALUES ($1, $2, $3, now(), $4, now())
		ON CONFLICT (credential_model_binding_id, capability)
		DO UPDATE SET supported = EXCLUDED.supported, last_tested_at = now(),
		              evidence_json = EXCLUDED.evidence_json, updated_at = now()`
	caps := []string{CapabilityNonstream, "native_responses_stream"}
	// errs 按 index 索引，**不按 append 顺序**——上一版用 append 到 slice，
	// 报错时会张冠李戴。
	errs := make([]error, len(caps))
	var wg sync.WaitGroup
	for i, c := range caps {
		wg.Add(1)
		go func(i int, c string) {
			defer wg.Done()
			_, errs[i] = pool.Exec(ctx, upsertSQL, id, c, i == 0,
				[]byte(fmt.Sprintf(`{"cap":%q}`, c)))
		}(i, c)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("%s upsert 失败：%v", caps[i], err)
		}
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM credential_model_capabilities
		  WHERE credential_model_binding_id = $1`, id).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	t.Logf("该 binding 现有 %d 行能力位", n)
	if n != len(caps) {
		t.Fatalf("落了 %d 行，want %d：两个不同 capability 并发写必须各占一行，"+
			"互相覆盖说明唯一键/冲突目标用错了", n, len(caps))
	}

	// 两行的结论必须各自独立（不是一个把另一个盖掉了）。
	rows, err := pool.Query(ctx,
		`SELECT capability, supported FROM credential_model_capabilities
		  WHERE credential_model_binding_id = $1 ORDER BY capability`, id)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var c string
		var sup bool
		if err := rows.Scan(&c, &sup); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[c] = sup
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	// nonstream 写 true、stream 写 false；被覆盖就会两行同值或只剩一行。
	if !got[CapabilityNonstream] {
		t.Fatalf("nonstream 结论不是 true（实际 %v）：被另一行盖掉了", got[CapabilityNonstream])
	}
	if got["native_responses_stream"] {
		t.Fatal("stream 结论不是 false（实际 true）：被另一行盖掉了")
	}
}

// TestUpsert_RowsAffectedIsNotZero pins the 0-rows guard.
//
// persistRow 把 RowsAffected()==0 当错误。并发下这条断言是有意义的：
// ON CONFLICT DO UPDATE 命中既有行时，PostgreSQL 返回 1 而不是 0，
// 所以若哪天有人把 DO UPDATE 改成 DO NOTHING，这条会立刻抓住
// ——而 DO NOTHING 在并发下正好会**静默丢弃**后写者的结论。
func TestUpsert_RowsAffectedIsNotZero(t *testing.T) {
	pool := concurrencyPool(t)
	id := bindingID(t, pool)
	b := &CapabilityBackfill{db: pool}
	ctx := context.Background()
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cctx,
			`DELETE FROM credential_model_capabilities
			  WHERE credential_model_binding_id = $1 AND capability = $2`,
			id, CapabilityNonstream)
	})

	// 第一次：INSERT 路径。
	if err := b.persistRow(ctx, dueBinding{BindingID: id, RawModel: "affected"},
		true, []byte(`{"n":1}`)); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// 第二次：DO UPDATE 路径。DO NOTHING 会让这里返回 0 rows ⇒ 报错。
	if err := b.persistRow(ctx, dueBinding{BindingID: id, RawModel: "affected"},
		false, []byte(`{"n":2}`)); err != nil {
		t.Fatalf("second upsert (DO UPDATE path): %v", err)
	}
	var supported bool
	if err := pool.QueryRow(ctx,
		`SELECT supported FROM credential_model_capabilities
		  WHERE credential_model_binding_id = $1 AND capability = $2`,
		id, CapabilityNonstream).Scan(&supported); err != nil {
		t.Fatalf("read: %v", err)
	}
	if supported {
		t.Fatal("supported=true：第二次 upsert 没生效，DO UPDATE 退化成 DO NOTHING 了？")
	}
}
