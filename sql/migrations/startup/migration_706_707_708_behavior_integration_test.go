//go:build integration

// migration_706_707_708_behavior_integration_test.go — behavioral fixture for
// the storage-optimization-plan v2 S1a migrations, run against a real planner
// (postgres:16-alpine via testcontainers, or TEST_PG_URL):
//
//	706 — session_memora/session_censors/session_tools 三新表族（父表 + hot +
//	      ensure_session_family_partitions + promote）；sessions 访问维度补列。
//	707 — session_turns 宽表补采列（父表 + hot 有序列契约全等）；部分唯一索引
//	      每会话至多一条 is_final_success；promote 重写为「有序契约 + SELECT *」
//	      后新列（credits_charged）随行搬入月分区不丢。
//	708 — session_bodies kind 列 + 旧行回填 turn_delta + final_full 部分唯一
//	      索引；unified 视图 13 列含 kind；sessions.last_full_* 死列 DROP；
//	      promote 重写后 kind 随行搬入。
//
// Run:
//
//	go test -tags integration ./sql/migrations/startup -run TestMigrationS1A -count=1
//
// All DDL runs inside one transaction that is rolled back, so TEST_PG_URL
// instances are left clean. Fixture chain: schema_migrations + real 430/526/614
// files (BEGIN/COMMIT + psql meta stripped) — the minimal ancestor set the
// three migrations assume.
package startup

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestMigrationS1ASessionFamily(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	conn, cleanup := partitionBehaviorContainer(t, ctx)
	defer cleanup()

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin fixture transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	exec := func(sql string) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql); err != nil {
			t.Fatalf("exec failed: %v\nSQL: %s", err, sql)
		}
	}
	queryInt := func(sql string) int {
		t.Helper()
		var n int
		if err := tx.QueryRow(ctx, sql).Scan(&n); err != nil {
			t.Fatalf("query %q: %v", sql, err)
		}
		return n
	}
	execFile := func(name string) {
		t.Helper()
		exec(stripTxControl(t, name))
	}

	// ── fixture: minimal ancestor chain ───────────────────────────────
	exec(`CREATE TABLE IF NOT EXISTS public.schema_migrations (
		version text PRIMARY KEY,
		description text
	)`)
	execFile("430_sessions_v2_schema.sql")
	// 526 全文件断言面太宽（request_logs 双表 RLS 策略/统一视图）；fixture
	// 只取 706-708 实际依赖的最小集：hot 表（列序与父表全等——707 有序契约
	// 的前提，用 LIKE 保证）+ advisory lock key 函数（promote 内部调用）。
	exec(`CREATE TABLE public.session_turns_hot (LIKE public.session_turns INCLUDING ALL)`)
	exec(`CREATE OR REPLACE FUNCTION public.session_turns_advisory_lock_key(
		p_tenant_id TEXT, p_session_id TEXT)
		RETURNS BIGINT LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
		AS $fn$ SELECT hashtextextended(p_tenant_id || ':' || p_session_id, 0) $fn$`)
	execFile("614_session_bodies_hot.sql")
	// 708 前置：sessions.last_full_* 死列（456 遗产）
	exec(`ALTER TABLE public.sessions
		ADD COLUMN IF NOT EXISTS last_full_request JSONB,
		ADD COLUMN IF NOT EXISTS last_full_response JSONB,
		ADD COLUMN IF NOT EXISTS last_full_payload_at TIMESTAMPTZ`)
	// 708 回填验证的前置：预置一行"历史逐轮 delta"（无 kind 列——
	// 708 ADD COLUMN 后它会带上 DEFAULT 'final_full'，再被回填为 turn_delta）
	exec(`INSERT INTO public.session_bodies_hot
		(session_id, turn_no, tenant_id, request_id, ts, partition_date)
		VALUES ('sess-b','1','tenant-b','req-b1','2027-05-02 08:00+08'::timestamptz, '2027-05-02'::date)`)

	// ── apply 706 → 707 → 708 ─────────────────────────────────────────
	execFile("706_session_family_s1a.sql")
	execFile("707_session_turns_s1a.sql")
	execFile("708_session_bodies_s1a.sql")

	// ── 706 assertions ────────────────────────────────────────────────
	for _, table := range []string{
		"session_memora", "session_censors", "session_tools",
		"session_memora_hot", "session_censors_hot", "session_tools_hot",
	} {
		if got := queryInt(`SELECT count(*) FROM pg_class WHERE relname = '` + table + `'`); got != 1 {
			t.Errorf("706: table %s missing (pg_class count=%d)", table, got)
		}
	}
	// ensure 函数为任意月份建分区（2027-05 保证不存在 → 真建而非 IF EXISTS 跳过）
	exec(`SELECT public.ensure_session_family_partitions('2027-05-01'::date)`)
	for _, part := range []string{
		"session_memora_2027_05", "session_censors_2027_05", "session_tools_2027_05",
	} {
		if !partitionAttached(t, tx, ctx, strings.TrimSuffix(part, "_2027_05"), part) {
			t.Errorf("706: partition %s not attached after ensure", part)
		}
	}
	// promote：hot 行搬入月分区（638 守卫拒非正 interval，用微秒级正区间）
	exec(`INSERT INTO public.session_tools_hot
		(execution_id, session_id, request_id, tenant_id, tool_name, status, started_at, created_at, partition_date)
		VALUES ('exec-1','sess-t','req-t','tenant-t','read_file','success',
		        '2027-05-02 10:00+08'::timestamptz, now() - interval '1 hour', '2027-05-02'::date)`)
	var moved int
	if err := tx.QueryRow(ctx, `SELECT public.promote_session_tools_hot_to_partition('1 microsecond'::interval, 100)`).Scan(&moved); err != nil {
		t.Fatalf("706: promote session_tools: %v", err)
	}
	if moved != 1 {
		t.Errorf("706: session_tools promote moved %d rows, want 1", moved)
	}
	if got := queryInt(`SELECT count(*) FROM public.session_tools WHERE execution_id='exec-1'`); got != 1 {
		t.Errorf("706: promoted session_tools row count=%d, want 1", got)
	}
	if got := queryInt(`SELECT count(*) FROM public.session_tools_hot`); got != 0 {
		t.Errorf("706: session_tools_hot drain count=%d, want 0", got)
	}
	// sessions 访问维度补列
	if got := queryInt(`SELECT count(*) FROM information_schema.columns
		WHERE table_schema='public' AND table_name='sessions'
		  AND column_name IN ('project_id','api_key_id','application_id','end_user_id',
		                      'owner_user','client_ip','agent_name','duration_ms')`); got != 8 {
		t.Errorf("706: sessions access-dim columns = %d, want 8", got)
	}

	// ── 707 assertions ────────────────────────────────────────────────
	for _, table := range []string{"session_turns", "session_turns_hot"} {
		if got := queryInt(`SELECT count(*) FROM information_schema.columns
			WHERE table_schema='public' AND table_name='` + table + `'
			  AND column_name IN ('request_delta','response_delta','is_final_success',
			      'api_key_id','application_id','end_user_id','customer_id','credits_charged',
			      'cost_display','cost_currency','work_type','token_band','usage_source',
			      'is_auto_request','auto_decision','auto_confidence','task_type_chosen',
			      'routing_attempts','routing_summary','canonical_id','canonical_model','raw_model_name',
			      'trace_events','failure_stage','failure_detail_code','upstream_status_code',
			      'upstream_finish_reason','stream_first_chunk_ms','stream_chunk_count',
			      'stream_interrupted','stream_done_sent','client_request_id','client_endpoint',
			      'client_timeout','egress_protocol','search_text','request_preview',
			      'response_preview','transform_summary','identity_hash','request_checksum',
			      'response_checksum','system_fingerprint','origin_stage','origin_actor',
			      'client_ip','client_forwarded_for','agent_name','agent_type','virtual_client_id')`); got != 50 {
			t.Errorf("707: %s backfill columns = %d, want 50", table, got)
		}
	}
	// is_final_success 部分唯一索引：同会话第二条唯一成功必须 23505。
	// 预期失败用 SAVEPOINT 包裹，避免毒化外层 fixture 事务。
	exec(`INSERT INTO public.session_turns_hot
		(session_id, turn_no, tenant_id, request_id, ts, success, is_final_success, partition_date)
		VALUES ('sess-claim','1','tenant-c','req-c1','2027-05-02 10:00+08'::timestamptz, true, true, '2027-05-02'::date)`)
	exec(`SAVEPOINT sp_claim`)
	if _, err := tx.Exec(ctx, `INSERT INTO public.session_turns_hot
		(session_id, turn_no, tenant_id, request_id, ts, success, is_final_success, partition_date)
		VALUES ('sess-claim','2','tenant-c','req-c2','2027-05-02 11:00+08'::timestamptz, true, true, '2027-05-02'::date)`); err == nil || !strings.Contains(err.Error(), "23505") {
		t.Fatalf("707: second is_final_success must raise 23505, got %v", err)
	}
	exec(`ROLLBACK TO SAVEPOINT sp_claim`)
	// promote：新列随 SELECT * 搬入分区（credits_charged 守恒）。
	// 708/638 体的 bodies promote 不自 ensure，先为目标月建分区。
	exec(`SELECT public.ensure_sessions_v2_partitions('2027-05-01'::date)`)
	exec(`INSERT INTO public.session_turns_hot
		(session_id, turn_no, tenant_id, request_id, ts, success, credits_charged, partition_date)
		VALUES ('sess-prom','1','tenant-p','req-p1', now() - interval '1 hour', true, 42, CURRENT_DATE)`)
	if err := tx.QueryRow(ctx, `SELECT public.promote_session_turns_hot_to_partition('1 microsecond'::interval, 100)`).Scan(&moved); err != nil {
		t.Fatalf("707: promote session_turns: %v", err)
	}
	if got := queryInt(`SELECT COALESCE(sum(credits_charged),0) FROM public.session_turns
		WHERE tenant_id='tenant-p' AND request_id='req-p1'`); got != 42 {
		t.Errorf("707: credits_charged after promote = %d, want 42 (SELECT * must carry new columns)", got)
	}

	// ── 708 assertions ────────────────────────────────────────────────
	// 旧行回填：迁移前插入的无 kind 行 → ADD COLUMN DEFAULT 'final_full' →
	// 回填 turn_delta
	if got := queryInt(`SELECT count(*) FROM public.session_bodies_hot
		WHERE tenant_id='tenant-b' AND request_id='req-b1' AND kind='turn_delta'`); got != 1 {
		t.Errorf("708: legacy delta row not backfilled to turn_delta (count=%d)", got)
	}
	// unified 视图 13 列且暴露 kind
	if got := queryInt(`SELECT count(*) FROM information_schema.columns
		WHERE table_schema='public' AND table_name='session_bodies_unified'`); got != 13 {
		t.Errorf("708: session_bodies_unified columns = %d, want 13", got)
	}
	// final_full 部分唯一索引：同会话第二条 final_full 必须 23505
	exec(`INSERT INTO public.session_bodies_hot
		(session_id, turn_no, tenant_id, request_id, ts, kind, partition_date)
		VALUES ('sess-ff','0','tenant-f','final_full:sess-ff','2027-05-02 08:00+08'::timestamptz, 'final_full', '2027-05-02'::date)`)
	exec(`SAVEPOINT sp_ff`)
	if _, err := tx.Exec(ctx, `INSERT INTO public.session_bodies_hot
		(session_id, turn_no, tenant_id, request_id, ts, kind, partition_date)
		VALUES ('sess-ff','0','tenant-f','other-req','2027-05-02 09:00+08'::timestamptz, 'final_full', '2027-05-02'::date)`); err == nil || !strings.Contains(err.Error(), "23505") {
		t.Fatalf("708: second final_full row must raise 23505, got %v", err)
	}
	exec(`ROLLBACK TO SAVEPOINT sp_ff`)
	// sessions.last_full_* 死列已 DROP
	if got := queryInt(`SELECT count(*) FROM information_schema.columns
		WHERE table_schema='public' AND table_name='sessions'
		  AND column_name IN ('last_full_request','last_full_response','last_full_payload_at')`); got != 0 {
		t.Errorf("708: sessions.last_full_* still present (%d columns)", got)
	}
	// promote：kind 随 SELECT * 搬入分区（bodies promote 按 ts 过滤且不自
	// ensure——目标分区为 fixture 已有的当前月）
	exec(`INSERT INTO public.session_bodies_hot
		(session_id, turn_no, tenant_id, request_id, ts, kind, partition_date)
		VALUES ('sess-bp','1','tenant-bp','req-bp1', now() - interval '1 hour', 'turn_delta', CURRENT_DATE)`)
	if err := tx.QueryRow(ctx, `SELECT public.promote_session_bodies_hot_to_partition('1 microsecond'::interval, 100)`).Scan(&moved); err != nil {
		t.Fatalf("708: promote session_bodies: %v", err)
	}
	if got := queryInt(`SELECT count(*) FROM public.session_bodies
		WHERE tenant_id='tenant-bp' AND request_id='req-bp1' AND kind='turn_delta'`); got != 1 {
		t.Errorf("708: promoted kind='turn_delta' row count=%d, want 1", got)
	}

	// 双账本自登记
	for _, version := range []string{"706", "707", "708"} {
		if got := queryInt(`SELECT count(*) FROM public.schema_migrations WHERE version='` + version + `'`); got != 1 {
			t.Errorf("ledger: schema_migrations version %s count=%d, want 1", version, got)
		}
	}
}
