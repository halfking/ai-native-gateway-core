package bg

// project_backfill_worker_realdb_test.go — 迁移 762 + 回填 worker 的真库纵切
// （审计三十三轮 Track C）。与 hot_ts_column_realdb_test.go 同门控：
// 无 TEST_DATABASE_URL / TEST_DB_URL 时跳过。
//
// 注意：本测试会执行 762 迁移（DDL，幂等）并写入 r33test_ 前缀的夹具行、
// 结束时清理。只应指向一次性/scratch 库（CI 与本地演练均为 tmp 库），
// 不要指向生产或共享库。

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func r33TestDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过真库纵切")
	}
	return dsn
}

// requireScratchSessionDB guards the DDL-bearing tests in this file.
//
// ★ 2026-10-06: 762 的 down 脚本会 DROP TRIGGER / FUNCTION / INDEX
// （见 762_session_project_backfill_chain.down.sql）。指向共享库或生产库时，
// 「测试跑通」等于当场拆掉那条回填链。文件头注释一直写着「只应指向一次性/scratch
// 库」，但那是注释——没有代码拦着，TEST_DATABASE_URL 指错一次就出事。
// 这里把要求变成判据：库里有本测试未创建的存量行就跳过。
//
// 代价要说清楚：本文件在「有数据的库」上从此只会 SKIP 而不再 FAIL。这是
// 刻意的——SKIP 与 FAIL 的取舍在这里是「不拿生产的触发器换一条测试信号」。
func requireScratchSessionDB(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	var preexisting int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM session_summaries`).Scan(&preexisting); err != nil {
		t.Fatalf("count pre-existing session_summaries: %v", err)
	}
	if preexisting > 0 {
		t.Skipf("session_summaries already holds %d rows: this test runs 762 up AND down "+
			"(down drops triggers/functions) and would dismantle a shared database's backfill "+
			"chain. Point TEST_DATABASE_URL at a disposable/scratch database", preexisting)
	}
}

// TestMigration762ProjectBackfillChain_RealDB 端到端验证：
//  1. 762 迁移在真库可执行且幂等（连跑两遍）；
//  2. bg worker 批量语句把存量 NULL 行按两级口径回填（app: 前缀）；
//  3. session_dim UPSERT 触发器自动同步新会话（写链）；
//  4. 权威 project_id 后到可升级 app:*，反向不覆盖；
//  5. project_dim / session_project_attribution 维度行正确落库；
//  6. 762 down 可执行，链路拆除后数据保留。
func TestMigration762ProjectBackfillChain_RealDB(t *testing.T) {
	dsn := r33TestDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Skipf("connect real db: %v", err)
	}
	defer conn.Close(ctx)

	upBytes, err := os.ReadFile(filepath.Join("..", "sql", "migrations", "startup", "762_session_project_backfill_chain.sql"))
	if err != nil {
		t.Fatalf("read 762 up: %v", err)
	}
	downBytes, err := os.ReadFile(filepath.Join("..", "sql", "migrations", "startup", "762_session_project_backfill_chain.down.sql"))
	if err != nil {
		t.Fatalf("read 762 down: %v", err)
	}

	// 前置：依赖表存在（fresh scratch 库需先有 547/655 形态）。
	for _, tbl := range []string{"session_summaries", "session_dim", "project_dim", "session_project_attribution"} {
		var exists bool
		if err := conn.QueryRow(ctx,
			`SELECT to_regclass('public.`+tbl+`') IS NOT NULL`).Scan(&exists); err != nil {
			t.Fatalf("probe table %s: %v", tbl, err)
		}
		if !exists {
			t.Skipf("table %s missing; scratch db not provisioned with 547/655 shape", tbl)
		}
	}

	// 2026-10-06: 护栏必须在任何 DDL 之前。见 requireScratchSessionDB。
	requireScratchSessionDB(t, ctx, conn)

	// 1. 迁移两遍（幂等）。
	for i := 1; i <= 2; i++ {
		if _, err := conn.Exec(ctx, string(upBytes)); err != nil {
			t.Fatalf("apply 762 up (pass %d): %v", i, err)
		}
	}

	const fixtureTenant = "r33test-tenant"
	// session_summaries.tenant_id references public.tenants(code)
	// (fk_session_tenant) and the gate database is empty, so the child rows
	// below cannot be inserted before the parent exists — measured 2026-10-02
	// as 23503 "violates foreign key constraint fk_session_tenant".
	//
	// The tenant is seeded AFTER the initial cleanup() call on purpose. cleanup
	// is invoked immediately after it is defined (to clear anything a previous
	// run left behind) and it reads createdTenant through the closure. Seeding
	// first would make that immediate call delete the row that was just
	// inserted, and the test would still fail 23503 — which is exactly what the
	// first version of this fix did.
	var createdTenant bool
	cleanup := func() {
		//nolint:errcheck // best-effort fixture cleanup
		conn.Exec(ctx, `DELETE FROM session_summaries WHERE session_key LIKE 'r33test_%'`)
		//nolint:errcheck
		conn.Exec(ctx, `DELETE FROM session_dim WHERE gw_session_id LIKE 'r33test_%'`)
		//nolint:errcheck
		conn.Exec(ctx, `DELETE FROM session_project_attribution WHERE gw_session_id LIKE 'r33test_%'`)
		//nolint:errcheck
		conn.Exec(ctx, `DELETE FROM project_dim WHERE project_ref LIKE 'app:r33app%'`)
		if createdTenant {
			// Children are already gone above, so this cannot cascade into
			// anything the test did not create.
			//nolint:errcheck
			conn.Exec(ctx, `DELETE FROM public.tenants WHERE code=$1`, fixtureTenant)
		}
	}
	cleanup()
	defer cleanup()

	if err := conn.QueryRow(ctx,
		`INSERT INTO public.tenants (code, name) VALUES ($1, $2)
		 ON CONFLICT (code) DO NOTHING RETURNING true`,
		fixtureTenant, "fixture-"+fixtureTenant).Scan(&createdTenant); err != nil {
		t.Fatalf("seed tenant %q: %v", fixtureTenant, err)
	}

	// 存量夹具：两条 NULL 项目会话（app 口径）+ 一条无信号会话（保持 NULL）。
	// s4 预留给写链验证：生产序是 request_logs_hot 触发器先建 ss 行、
	// sessionv2mirror 请求终态后 UPSERT session_dim，所以这里也先建 ss 行。
	seed := `
INSERT INTO session_summaries (session_key, tenant_id, first_request_at, last_request_at, request_count)
VALUES ('r33test_s1', $1, NOW(), NOW(), 1),
       ('r33test_s2', $1, NOW(), NOW(), 1),
       ('r33test_s3', $1, NOW(), NOW(), 1),
       ('r33test_s4', $1, NOW(), NOW(), 1)`
	if _, err := conn.Exec(ctx, seed, fixtureTenant); err != nil {
		t.Fatalf("seed session_summaries: %v", err)
	}
	// status and created_at are written literally because both are NOT NULL with
	// NO default in the installed schema: migration 350 declares
	// DEFAULT 'active' but 350 is not in the installer's StartupFiles, and
	// 805_session_dim_reconcile — which IS registered — creates the columns
	// without defaults. Measured with information_schema.columns on a gate
	// database, 2026-10-02.
	dim := `
INSERT INTO session_dim (gw_session_id, session_key, tenant_id, status, created_at, first_request_at, last_active_at, application_code)
VALUES ('r33test_s1', 'r33test_s1', $1, 'active', NOW(), NOW(), NOW(), 'r33app'),
       ('r33test_s2', 'r33test_s2', $1, 'active', NOW(), NOW(), NOW(), NULL)`
	if _, err := conn.Exec(ctx, dim, fixtureTenant); err != nil {
		t.Fatalf("seed session_dim: %v", err)
	}

	// 注意：seed session_dim 走 INSERT 已触发 762 触发器（s1 应已被填
	// app:r33app）。为了让 worker 路径也被真库验证，先清掉触发器填的值
	// 再跑批量语句——两个入口最终收敛到同一 sync 函数。
	if _, err := conn.Exec(ctx,
		`UPDATE session_summaries SET gw_project_id = NULL WHERE session_key LIKE 'r33test_%'`); err != nil {
		t.Fatalf("reset gw_project_id: %v", err)
	}

	// 2. worker 批量语句（与 BackfillOnce 同一条 SQL）。
	rows, err := conn.Query(ctx, sessionProjectBackfillSQL, projectBackfillBatchSize)
	if err != nil {
		t.Fatalf("backfill batch query: %v", err)
	}
	var total int64
	for rows.Next() {
		var affected int
		if err := rows.Scan(&affected); err != nil {
			rows.Close()
			t.Fatalf("scan backfill result: %v", err)
		}
		total += int64(affected)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("backfill rows: %v", err)
	}
	if total < 1 {
		t.Fatalf("expected ≥1 backfilled row, got %d", total)
	}

	var proj1, proj3 *string
	if err := conn.QueryRow(ctx,
		`SELECT gw_project_id FROM session_summaries WHERE session_key = 'r33test_s1'`).Scan(&proj1); err != nil {
		t.Fatalf("read s1: %v", err)
	}
	if proj1 == nil || *proj1 != "app:r33app" {
		t.Errorf("s1 gw_project_id = %v, want app:r33app", proj1)
	}
	if err := conn.QueryRow(ctx,
		`SELECT gw_project_id FROM session_summaries WHERE session_key = 'r33test_s3'`).Scan(&proj3); err != nil {
		t.Fatalf("read s3: %v", err)
	}
	if proj3 != nil {
		t.Errorf("s3 (no session_dim, no signals) gw_project_id = %v, want NULL", *proj3)
	}

	// 3. 维度行：project_dim 本地 app 行 + attribution rule 留痕。
	var dimName string
	var dimSynced bool
	if err := conn.QueryRow(ctx,
		`SELECT name, synced_from_acc_at IS NOT NULL FROM project_dim WHERE project_ref = 'app:r33app'`).Scan(&dimName, &dimSynced); err != nil {
		t.Fatalf("read project_dim: %v", err)
	}
	if dimName != "r33app" || dimSynced {
		t.Errorf("project_dim row name=%q acc_synced=%v, want name=r33app acc_synced=false", dimName, dimSynced)
	}
	var attrMethod, attrStatus string
	if err := conn.QueryRow(ctx,
		`SELECT method, status FROM session_project_attribution WHERE gw_session_id = 'r33test_s1'`).Scan(&attrMethod, &attrStatus); err != nil {
		t.Fatalf("read attribution: %v", err)
	}
	if attrMethod != "rule" || attrStatus != "confirmed" {
		t.Errorf("attribution method=%s status=%s, want rule/confirmed", attrMethod, attrStatus)
	}

	// 4. 写链：新会话 UPSERT session_dim（模拟 sessionv2mirror）自动回填。
	if _, err := conn.Exec(ctx, `
INSERT INTO session_dim (gw_session_id, session_key, tenant_id, status, created_at, first_request_at, last_active_at, application_code)
VALUES ('r33test_s4', 'r33test_s4', $1, 'active', NOW(), NOW(), NOW(), 'r33app')
ON CONFLICT (gw_session_id) DO UPDATE SET last_active_at = NOW()`,
		fixtureTenant); err != nil {
		t.Fatalf("upsert session_dim s4: %v", err)
	}
	if err := conn.QueryRow(ctx,
		`SELECT ss.gw_project_id FROM session_summaries ss WHERE ss.session_key = 'r33test_s4'`).Scan(&proj3); err != nil {
		t.Fatalf("read s4: %v", err)
	}
	if proj3 == nil || *proj3 != "app:r33app" {
		t.Errorf("s4 gw_project_id = %v, want app:r33app via session_dim trigger", proj3)
	}

	// 5. 权威升级：session_dim 后到 project_id 升级 app:*；反向不覆盖。
	if _, err := conn.Exec(ctx,
		`UPDATE session_dim SET project_id = 'r33-acc-project' WHERE gw_session_id = 'r33test_s1'`); err != nil {
		t.Fatalf("authoritative upgrade: %v", err)
	}
	var upgraded *string
	if err := conn.QueryRow(ctx,
		`SELECT gw_project_id FROM session_summaries WHERE session_key = 'r33test_s1'`).Scan(&upgraded); err != nil {
		t.Fatalf("read upgraded s1: %v", err)
	}
	if upgraded == nil || *upgraded != "r33-acc-project" {
		t.Errorf("s1 after authoritative update = %v, want r33-acc-project", upgraded)
	}
	// 反向：权威值在场时 app 变化不得覆盖。
	if _, err := conn.Exec(ctx,
		`UPDATE session_dim SET application_code = 'r33other' WHERE gw_session_id = 'r33test_s1'`); err != nil {
		t.Fatalf("app change on authoritative session: %v", err)
	}
	if err := conn.QueryRow(ctx,
		`SELECT gw_project_id FROM session_summaries WHERE session_key = 'r33test_s1'`).Scan(&upgraded); err != nil {
		t.Fatalf("re-read s1: %v", err)
	}
	if upgraded == nil || *upgraded != "r33-acc-project" {
		t.Errorf("s1 must keep authoritative ref, got %v", upgraded)
	}

	// 6. down 可执行；数据保留（观测列不抹）。
	if _, err := conn.Exec(ctx, string(downBytes)); err != nil {
		t.Fatalf("apply 762 down: %v", err)
	}
	var remain *string
	if err := conn.QueryRow(ctx,
		`SELECT gw_project_id FROM session_summaries WHERE session_key = 'r33test_s1'`).Scan(&remain); err != nil {
		t.Fatalf("read s1 post-down: %v", err)
	}
	if remain == nil || *remain != "r33-acc-project" {
		t.Errorf("down must keep backfilled data, got %v", remain)
	}
}

// TestBackfillBatchSurvivesUnresolvableWindowSaturation_RealDB 复现并钉住
// 2026-10-06 实测到的 worker 停摆（runbook §10.38）。
//
// 停摆机理：「能 join 上 session_dim」不等于「能回填」。project_id 与
// application_code 双 NULL 的行照样进窗口，sync_session_project_attr 对它们
// 返回 0，行保持 NULL；下一批 ORDER BY session_key 又把它们选回来。只要这类行
// 的条数 ≥ LIMIT，窗口就被永久占死，worker 每轮 backfilled=0，而窗口外压着
// 全部可回填行。生产实测 first_resolvable_rank(5746) > LIMIT(2000)。
//
// ★ 为什么这个 bug 原本测不出来：原测试只有 4 行夹具，窗口永远装得下，陷阱不
// 成立。所以本测试必须自己把陷阱造出来——塞 projectBackfillBatchSize+500 行
// 不可解析行，并且让它们排在目标行之前。缺了任何一半，本测试都会假绿。
//
// 与 TestMigration762ProjectBackfillChain_RealDB 不同，本测试只跑 762 up
// （幂等），不跑 down。
func TestBackfillBatchSurvivesUnresolvableWindowSaturation_RealDB(t *testing.T) {
	dsn := r33TestDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Skipf("connect real db: %v", err)
	}
	defer conn.Close(ctx)

	for _, tbl := range []string{"session_summaries", "session_dim", "project_dim", "session_project_attribution"} {
		var exists bool
		if err := conn.QueryRow(ctx,
			`SELECT to_regclass('public.`+tbl+`') IS NOT NULL`).Scan(&exists); err != nil {
			t.Fatalf("probe table %s: %v", tbl, err)
		}
		if !exists {
			t.Skipf("table %s missing; scratch db not provisioned with 547/655 shape", tbl)
		}
	}
	requireScratchSessionDB(t, ctx, conn)

	upBytes, err := os.ReadFile(filepath.Join("..", "sql", "migrations", "startup", "762_session_project_backfill_chain.sql"))
	if err != nil {
		t.Fatalf("read 762 up: %v", err)
	}
	if _, err := conn.Exec(ctx, string(upBytes)); err != nil {
		t.Fatalf("apply 762 up: %v", err)
	}

	const (
		trapTenant = "trap33-tenant"
		trapPrefix = "0000trap_"
		targetKey  = "9999zzz_backfill_target"
		targetApp  = "trap33app"
		trapRows   = projectBackfillBatchSize + 500 // 必须 > LIMIT，否则窗口装不下陷阱
	)
	cleanup := func() {
		//nolint:errcheck // best-effort fixture cleanup
		conn.Exec(ctx, `DELETE FROM session_summaries WHERE session_key LIKE '0000trap_%' OR session_key = $1`, targetKey)
		//nolint:errcheck
		conn.Exec(ctx, `DELETE FROM session_dim WHERE gw_session_id LIKE '0000trap_%' OR gw_session_id = $1`, targetKey)
		//nolint:errcheck
		conn.Exec(ctx, `DELETE FROM session_project_attribution WHERE gw_session_id LIKE '0000trap_%' OR gw_session_id = $1`, targetKey)
		//nolint:errcheck
		conn.Exec(ctx, `DELETE FROM project_dim WHERE project_ref LIKE 'app:trap33%'`)
		//nolint:errcheck
		conn.Exec(ctx, `DELETE FROM public.tenants WHERE code = $1`, trapTenant)
	}
	cleanup()
	defer cleanup()

	if _, err := conn.Exec(ctx,
		`INSERT INTO public.tenants (code, name) VALUES ($1, $2) ON CONFLICT (code) DO NOTHING`,
		trapTenant, "fixture-"+trapTenant); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}

	// 陷阱行：session_dim 有行（能 join 上），但 project_id 与 application_code
	// 双 NULL ⇒ gw_resolve_project_ref 返回 NULL ⇒ sync 返回 0、行不变 NULL。
	if _, err := conn.Exec(ctx, `
INSERT INTO session_summaries (session_key, tenant_id, first_request_at, last_request_at, request_count)
SELECT '0000trap_' || lpad(i::text, 6, '0'), $1, NOW(), NOW(), 1
FROM generate_series(1, $2) i`, trapTenant, trapRows); err != nil {
		t.Fatalf("seed trap session_summaries: %v", err)
	}
	if _, err := conn.Exec(ctx, `
INSERT INTO session_dim (gw_session_id, session_key, tenant_id, status, created_at, first_request_at, last_active_at, application_code)
SELECT '0000trap_' || lpad(i::text, 6, '0'), '0000trap_' || lpad(i::text, 6, '0'),
       $1, 'active', NOW(), NOW(), NOW(), NULL
FROM generate_series(1, $2) i`, trapTenant, trapRows); err != nil {
		t.Fatalf("seed trap session_dim: %v", err)
	}
	// 目标行：可解析，排在全部陷阱之后（'9' > '0'，C.UTF-8 字节序）。
	if _, err := conn.Exec(ctx, `
INSERT INTO session_summaries (session_key, tenant_id, first_request_at, last_request_at, request_count)
VALUES ($1, $2, NOW(), NOW(), 1)`, targetKey, trapTenant); err != nil {
		t.Fatalf("seed target session_summaries: %v", err)
	}
	if _, err := conn.Exec(ctx, `
INSERT INTO session_dim (gw_session_id, session_key, tenant_id, status, created_at, first_request_at, last_active_at, application_code)
VALUES ($1, $1, $2, 'active', NOW(), NOW(), NOW(), $3)`, targetKey, trapTenant, targetApp); err != nil {
		t.Fatalf("seed target session_dim: %v", err)
	}

	// 隔离 worker 路径：上面 seed session_dim 时，762 的 AFTER INSERT 触发器
	// （trg_session_dim_project_attr_ins）已经把目标行填好了——它与 worker 是同一
	// 函数的两个入口（迁移自称「口径单一」）。不清掉的话目标行根本不在 NULL 集合里，
	// 窗口里一条可回填行都没有，本测试会因错误的原因通过或失败。
	// （与 TestMigration762ProjectBackfillChain_RealDB 同一手法。）
	if _, err := conn.Exec(ctx,
		`UPDATE session_summaries SET gw_project_id = NULL WHERE session_key = $1`, targetKey); err != nil {
		t.Fatalf("reset target gw_project_id: %v", err)
	}
	// 该重置本身也可能被别的触发器回填，先确认目标确实处于 NULL 状态。
	var targetPrefill *string
	if err := conn.QueryRow(ctx,
		`SELECT gw_project_id FROM session_summaries WHERE session_key = $1`, targetKey).Scan(&targetPrefill); err != nil {
		t.Fatalf("read target pre-state: %v", err)
	}
	if targetPrefill != nil {
		t.Fatalf("target gw_project_id = %v before the batch; the write-chain trigger refilled it, "+
			"so the worker path is not isolated", *targetPrefill)
	}

	// 陷阱成立性前置断言：目标行必须排在全部陷阱行之后，否则窗口装得下陷阱，
	// 本测试就退化成「窗口没满」的普通用例，对停摆零检出。
	var trapsBefore int
	if err := conn.QueryRow(ctx,
		`SELECT count(*) FROM session_summaries WHERE session_key LIKE $1 AND session_key < $2`,
		trapPrefix+"%", targetKey).Scan(&trapsBefore); err != nil {
		t.Fatalf("verify trap ordering: %v", err)
	}
	if trapsBefore < trapRows {
		t.Fatalf("trap not established: only %d of %d unresolvable rows sort before the target; "+
			"this test would not exercise window saturation", trapsBefore, trapRows)
	}

	// 跑生产语句本体，一个批次。
	rows, err := conn.Query(ctx, sessionProjectBackfillSQL, projectBackfillBatchSize)
	if err != nil {
		t.Fatalf("backfill batch query: %v", err)
	}
	var total int64
	for rows.Next() {
		var affected int
		if err := rows.Scan(&affected); err != nil {
			rows.Close()
			t.Fatalf("scan backfill result: %v", err)
		}
		total += int64(affected)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("backfill rows: %v", err)
	}
	if total < 1 {
		t.Fatalf("expected ≥1 backfilled row, got %d — the %d unresolvable rows ahead of the "+
			"target saturated the %d-row window and stalled the batch", total, trapsBefore, projectBackfillBatchSize)
	}

	var got *string
	if err := conn.QueryRow(ctx,
		`SELECT gw_project_id FROM session_summaries WHERE session_key = $1`, targetKey).Scan(&got); err != nil {
		t.Fatalf("read target: %v", err)
	}
	if want := "app:" + targetApp; got == nil || *got != want {
		t.Errorf("target gw_project_id = %v, want %s", got, want)
	}

	// 陷阱行必须仍为 NULL：它们确实无从回填，不该被"顺手填上"假装成功。
	var filledTraps int
	if err := conn.QueryRow(ctx,
		`SELECT count(*) FROM session_summaries WHERE session_key LIKE $1 AND gw_project_id IS NOT NULL`,
		trapPrefix+"%").Scan(&filledTraps); err != nil {
		t.Fatalf("count filled traps: %v", err)
	}
	if filledTraps != 0 {
		t.Errorf("%d unresolvable trap rows got a project ref; they have neither "+
			"project_id nor application_code, so any value is a bug", filledTraps)
	}
}
