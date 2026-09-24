package db

// ddl_definition_guard_contract_test.go —— R60 修正轮（S6-1）真库契约测试。
//
// 验证三件事（纯函数单测覆盖不了的）：
//  1. 守卫解析器与真实 PostgreSQL（PG17 ruleutils 渲染）逐字对齐：全新
//     scratch 库跑完 ensure 链后，每个守卫组必须报告"当前"（跳过路径生效）；
//  2. SKIP 与执行的最终库态等价：第二遍 ensure 前后 pg_policies/pg_trigger
//     目录快照逐字节不变；
//  3. 定义漂移必执行：手改策略 qual / 触发器 WHEN 后守卫必须报告"不当前"，
//     重跑 ensure 后定义恢复为期望值（策略演进能落到存量安装）。
//
// 无 TEST_PG_URL 时整组 SKIP（CI 与本地默认路径）：
//	TEST_PG_URL=postgres://user:pass@127.0.0.1:5432/postgres?sslmode=disable \
//	  go test ./db/ -run TestDDLSkipGuard -v
// 测试在目标实例上创建/销毁独立 scratch 数据库，不触碰既有库。

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ddlRecordingTracer 记录经过 pgx 执行的全部 POLICY/TRIGGER DDL 语句，
// 供契约 2b 断言第二遍 ensure 零重放。
type ddlRecordingTracer struct {
	stmts []string
}

func (t *ddlRecordingTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	upper := strings.ToUpper(data.SQL)
	for _, banned := range []string{"CREATE POLICY", "DROP POLICY", "CREATE TRIGGER", "DROP TRIGGER"} {
		if strings.Contains(upper, banned) {
			t.stmts = append(t.stmts, data.SQL)
			break
		}
	}
	return ctx
}

func (t *ddlRecordingTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestDDLSkipGuardsAgainstRealPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL not set: real-Postgres DDL guard contract test skipped (pure-function unit tests still run)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect TEST_PG_URL: %v", err)
	}
	defer admin.Close()
	if err := admin.Ping(ctx); err != nil {
		t.Fatalf("ping TEST_PG_URL: %v", err)
	}

	scratch := fmt.Sprintf("r60_ddl_guard_%d", time.Now().UnixNano()%1e9)
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+scratch); err != nil {
		t.Fatalf("drop stale scratch db: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+scratch); err != nil {
		t.Fatalf("create scratch db: %v", err)
	}
	scratchCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("ParseConfig(TEST_PG_URL): %v", err)
	}
	scratchCfg.ConnConfig.Database = scratch
	scratchPool0, err := pgxpool.NewWithConfig(ctx, scratchCfg)
	if err != nil {
		t.Fatalf("connect scratch db: %v", err)
	}
	scratchPool0.Close()
	// 校验确实连上了 scratch 库（防止回连 admin 库后误判）。
	var dbname string
	probe, err := pgxpool.NewWithConfig(ctx, scratchCfg)
	if err != nil {
		t.Fatalf("probe scratch db: %v", err)
	}
	if err := probe.QueryRow(ctx, "SELECT current_database()").Scan(&dbname); err != nil || dbname != scratch {
		probe.Close()
		t.Fatalf("expected to be on scratch db %s, got %q (err=%v)", scratch, dbname, err)
	}
	probe.Close()
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanupCtx, "DROP DATABASE IF EXISTS "+scratch); err != nil {
			t.Logf("cleanup: drop scratch db %s: %v", scratch, err)
		}
	})

	pool, err := pgxpool.NewWithConfig(ctx, scratchCfg)
	if err != nil {
		t.Fatalf("connect scratch db: %v", err)
	}
	defer pool.Close()
	d := &DB{pool: pool}

	// 依赖 stub：ensure 链假定前置迁移已建好这些表（scratch 库只有守卫链）。
	for _, stub := range []string{
		`CREATE FUNCTION public.get_current_tenant() RETURNS text LANGUAGE sql STABLE AS $$ SELECT COALESCE(NULLIF(current_setting('app.current_tenant', true), ''), 'default'); $$`,
		// governor 的 auto-route 触发器引用的 notify 函数由真实链路更早的
		// ensure/迁移创建；scratch 库用同签名 stub。
		`CREATE FUNCTION public.notify_auto_route_refresh() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END; $$`,
		`CREATE TABLE tenants (code TEXT PRIMARY KEY)`,
		`CREATE TABLE credentials (
			id BIGSERIAL PRIMARY KEY,
			tenant_id TEXT,
			provider_id BIGINT,
			revision BIGINT NOT NULL DEFAULT 0,
			concurrency_limit INT, concurrency_mode TEXT, rpm_limit INT,
			tpm_limit INT, fp_slot_limit INT, max_queue_depth INT,
			max_queue_wait_ms INT, status TEXT, availability_state TEXT,
			quota_state TEXT, circuit_state TEXT, lifecycle_status TEXT,
			manual_disabled BOOLEAN)`,
		`CREATE TABLE tenant_settings_kv (tenant_id TEXT)`,
		`CREATE TABLE settings_audit (tenant_id TEXT)`,
		`CREATE TABLE tenant_tool_policies (tenant_id TEXT)`,
		`CREATE TABLE tool_call_events (tenant_id TEXT)`,
		`CREATE TABLE tool_usage_stats (tenant_id TEXT)`,
		`CREATE TABLE tool_registry (tenant_id TEXT)`,
		`CREATE TABLE analysis_events (tenant_id TEXT)`,
		`CREATE TABLE intent_aggregates (tenant_id TEXT)`,
		`CREATE TABLE routing_overrides (id BIGSERIAL PRIMARY KEY)`,
	} {
		if _, err := pool.Exec(ctx, stub); err != nil {
			t.Fatalf("stub: %v", err)
		}
	}

	ensures := []struct {
		name string
		fn   func(d *DB) func(context.Context) error
	}{
		{"request_journey_observation", func(d *DB) func(context.Context) error { return d.ensureRequestJourneyObservationSchema }},
		{"journal_snapshot_receipt", func(d *DB) func(context.Context) error { return d.ensureJournalSnapshotReceiptSchema }},
		{"users", func(d *DB) func(context.Context) error { return d.EnsureUsersTable }},
		{"vibe_coding", func(d *DB) func(context.Context) error { return d.ensureVibeCodingSchema }},
		{"webcookie_sessions", func(d *DB) func(context.Context) error { return d.ensureWebCookieSessionsSchema }},
		{"route_incident", func(d *DB) func(context.Context) error { return d.ensureRouteIncidentSchema }},
		{"orchestration_runtime_instances", func(d *DB) func(context.Context) error { return d.ensureOrchestrationRuntimeInstancesSchema }},
		{"response_format_anomalies", func(d *DB) func(context.Context) error { return d.ensureResponseFormatAnomaliesSchema }},
		{"model_integrity_events", func(d *DB) func(context.Context) error { return d.ensureModelIntegrityEventsSchema }},
		{"supplemental_rls", func(d *DB) func(context.Context) error { return d.ensureSupplementalRLS }},
		{"analysis_events_rls", func(d *DB) func(context.Context) error { return d.ensureAnalysisEventsRLS }},
		{"tenant_model_policies", func(d *DB) func(context.Context) error { return d.ensureTenantModelPoliciesSchema }},
		{"routing_overrides_audit", func(d *DB) func(context.Context) error { return d.ensureRoutingOverridesAudit }},
		{"credential_keys", func(d *DB) func(context.Context) error { return d.ensureCredentialKeysSchema }},
		{"credential_client_quota", func(d *DB) func(context.Context) error { return d.ensureCredentialClientQuotaSchema }},
		{"credential_governor_revision", func(d *DB) func(context.Context) error { return d.ensureCredentialGovernorRevision }},
	}
	runEnsures := func(d *DB, stage string) {
		for _, e := range ensures {
			if err := e.fn(d)(ctx); err != nil {
				t.Fatalf("%s ensure %s: %v", stage, e.name, err)
			}
		}
	}
	runEnsures(d, "first")

	// ── 契约 1：每个守卫组在真库渲染下必须报告"当前"（跳过路径生效）──
	outboxDDLs := []string{`
		CREATE POLICY request_journey_observation_outbox_tenant_isolation
			ON request_journey_observation_outbox
			USING (tenant_id = current_setting('app.current_tenant', true)::TEXT)
			WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::TEXT)`, `
		CREATE POLICY request_journey_observation_outbox_super_admin_bypass
			ON request_journey_observation_outbox
			USING (current_setting('app.current_role', true) = 'super_admin'
				OR current_setting('app.bypass_rls', true) = 'true')
			WITH CHECK (current_setting('app.current_role', true) = 'super_admin'
				OR current_setting('app.bypass_rls', true) = 'true')`}
	groups := []struct {
		note string
		ok   bool
	}{
		{"outbox rls+policies", d.rlsPoliciesCurrent(ctx, "request_journey_observation_outbox", true, outboxDDLs)},
		{"users policy", d.policiesCurrent(ctx, "users", []string{usersPolicyDDL})},
		{"tenant_model_policies policy", d.policiesCurrent(ctx, "tenant_model_policies", []string{`
		CREATE POLICY tenant_isolation_tmp ON public.tenant_model_policies
		    USING ((tenant_id)::text = (public.get_current_tenant())::text)`})},
		{"tenant_model_policies audit trigger", d.triggersCurrent(ctx, "tenant_model_policies", []string{`
		CREATE TRIGGER tenant_model_policies_audit_trg
		    AFTER INSERT OR UPDATE OR DELETE ON tenant_model_policies
		    FOR EACH ROW EXECUTE FUNCTION tenant_model_policies_audit_fn()`})},
		{"routing_overrides trigger", d.triggersCurrent(ctx, "routing_overrides", []string{`
		CREATE TRIGGER routing_overrides_audit_trg
			AFTER INSERT OR UPDATE OR DELETE ON routing_overrides
			FOR EACH ROW EXECUTE FUNCTION routing_overrides_audit_fn()`})},
		{"governor bump trigger", d.triggersCurrent(ctx, "credentials", []string{`
		CREATE TRIGGER trg_bump_credentials_governor_revision
		BEFORE INSERT OR UPDATE OF concurrency_limit, concurrency_mode, rpm_limit,
			tpm_limit, fp_slot_limit, max_queue_depth, max_queue_wait_ms
		ON public.credentials FOR EACH ROW
		EXECUTE FUNCTION public.bump_credentials_governor_revision()`})},
		{"governor auto-route trigger (WHEN OLD.* )", d.triggersCurrent(ctx, "credentials", []string{`
		CREATE TRIGGER trg_notify_auto_route_creds
		AFTER UPDATE OF status, availability_state, quota_state, circuit_state,
			concurrency_limit, concurrency_mode, rpm_limit, tpm_limit, fp_slot_limit,
			max_queue_depth, max_queue_wait_ms, lifecycle_status, manual_disabled
		ON public.credentials FOR EACH ROW
		WHEN (OLD.* IS DISTINCT FROM NEW.*)
		EXECUTE FUNCTION public.notify_auto_route_refresh()`})},
		{"credential_keys triggers", d.triggersCurrent(ctx, "credential_keys", []string{`
		CREATE TRIGGER trg_credential_keys_enforce_parent_tenant
		    BEFORE INSERT OR UPDATE OF credential_id, tenant_id ON public.credential_keys
		    FOR EACH ROW EXECUTE FUNCTION public.credential_keys_enforce_parent_tenant()`, `
		CREATE TRIGGER trg_credential_keys_touch_updated_at
		    BEFORE UPDATE ON public.credential_keys
		    FOR EACH ROW EXECUTE FUNCTION public.credential_keys_touch_updated_at()`})},
	}
	for _, g := range groups {
		if !g.ok {
			t.Errorf("guard group %q should report current after first ensure (parse vs real PG rendering mismatch)", g.note)
		}
	}

	// ── 契约 2：第二遍 ensure 前后 catalog 快照逐字节不变（SKIP 等价执行）──
	snapshot := func() string {
		var sb strings.Builder
		rows, err := pool.Query(ctx, `
			SELECT tablename, policyname, cmd, permissive, roles::text,
			       coalesce(qual,''), coalesce(with_check,'')
			FROM pg_policies WHERE schemaname='public' ORDER BY 1,2`)
		if err != nil {
			t.Fatalf("snapshot policies: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var f [7]string
			if err := rows.Scan(&f[0], &f[1], &f[2], &f[3], &f[4], &f[5], &f[6]); err != nil {
				t.Fatalf("snapshot scan: %v", err)
			}
			sb.WriteString(strings.Join(f[:], "\x00") + "\n")
		}
		trows, err := pool.Query(ctx, `
			SELECT tgrelid::regclass::text, tgname, pg_get_triggerdef(oid)
			FROM pg_trigger WHERE NOT tgisinternal ORDER BY 1,2`)
		if err != nil {
			t.Fatalf("snapshot triggers: %v", err)
		}
		defer trows.Close()
		for trows.Next() {
			var f [3]string
			if err := trows.Scan(&f[0], &f[1], &f[2]); err != nil {
				t.Fatalf("snapshot scan: %v", err)
			}
			sb.WriteString(strings.Join(f[:], "\x00") + "\n")
		}
		return sb.String()
	}
	before := snapshot()

	// ── 契约 2b（R61 S1-P2-1）：第二遍 ensure 全程零 POLICY/TRIGGER DDL ──
	// 契约 2 的快照比对在"守卫全失效（fail-open 每语句重放）"时依然通过
	// （DROP+CREATE 同定义还原出逐字节相同的目录态）；本契约用 pgx
	// QueryTracer 直接断言守卫命中路径真的跳过了执行——这是烧点收口的
	// 直接目标，不允许只靠 8 组采样间接背书。第二遍 ensure 即本 traced
	// pass，快照对比包住同一次执行。
	rec := &ddlRecordingTracer{}
	tracedCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("ParseConfig for traced pool: %v", err)
	}
	tracedCfg.ConnConfig.Database = scratch
	tracedCfg.ConnConfig.Tracer = rec
	tracedPool, err := pgxpool.NewWithConfig(ctx, tracedCfg)
	if err != nil {
		t.Fatalf("connect traced pool: %v", err)
	}
	defer tracedPool.Close()
	runEnsures(&DB{pool: tracedPool}, "second")
	if len(rec.stmts) > 0 {
		t.Errorf("second ensure must execute zero POLICY/TRIGGER DDL (guards must short-circuit); got %d statements:\n%s",
			len(rec.stmts), strings.Join(rec.stmts, "\n---\n"))
	}

	if after := snapshot(); after != before {
		t.Errorf("second ensure changed catalog state:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}

	// ── 契约 3：定义漂移必执行（策略演进落到存量安装）──
	// 3a. 策略 qual 漂移
	if _, err := pool.Exec(ctx, `
		DROP POLICY request_journey_observation_outbox_tenant_isolation ON request_journey_observation_outbox;
		CREATE POLICY request_journey_observation_outbox_tenant_isolation ON request_journey_observation_outbox
			USING (tenant_id = 'drifted') WITH CHECK (tenant_id = 'drifted')`); err != nil {
		t.Fatalf("inject policy drift: %v", err)
	}
	if d.rlsPoliciesCurrent(ctx, "request_journey_observation_outbox", true, outboxDDLs) {
		t.Errorf("drifted policy qual must NOT be treated as current")
	}
	if err := d.ensureRequestJourneyObservationSchema(ctx); err != nil {
		t.Fatalf("re-ensure after policy drift: %v", err)
	}
	if !d.rlsPoliciesCurrent(ctx, "request_journey_observation_outbox", true, outboxDDLs) {
		t.Errorf("policy definition must be restored to expected after re-ensure")
	}
	// 3b. 触发器 WHEN 漂移
	if _, err := pool.Exec(ctx, `
		DROP TRIGGER route_incidents_touch ON route_incidents;
		CREATE TRIGGER route_incidents_touch AFTER UPDATE ON route_incidents
			FOR EACH ROW WHEN (true) EXECUTE FUNCTION touch_route_incidents_updated_at()`); err != nil {
		t.Fatalf("inject trigger drift: %v", err)
	}
	if d.triggersCurrent(ctx, "route_incidents", []string{`
		CREATE TRIGGER route_incidents_touch
			BEFORE UPDATE ON route_incidents
			FOR EACH ROW EXECUTE FUNCTION touch_route_incidents_updated_at()`}) {
		t.Errorf("drifted trigger definition must NOT be treated as current")
	}
	if err := d.ensureRouteIncidentSchema(ctx); err != nil {
		t.Fatalf("re-ensure after trigger drift: %v", err)
	}
	if !d.triggersCurrent(ctx, "route_incidents", []string{`
		CREATE TRIGGER route_incidents_touch
			BEFORE UPDATE ON route_incidents
			FOR EACH ROW EXECUTE FUNCTION touch_route_incidents_updated_at()`}) {
		t.Errorf("trigger definition must be restored to expected after re-ensure")
	}

	// ── omnifree（放最后：表最多）──
	if err := d.ensureOmniFreeSchema(ctx); err != nil {
		t.Fatalf("ensureOmniFreeSchema: %v", err)
	}
	omniDDL := fmt.Sprintf(`CREATE POLICY tenant_isolation_%[1]s ON public.%[1]s USING (tenant_id = public.get_current_tenant() OR current_setting('app.current_role', true) = 'super_admin' OR current_setting('app.bypass_rls', true) = 'true') WITH CHECK (tenant_id = public.get_current_tenant() OR current_setting('app.current_role', true) = 'super_admin' OR current_setting('app.bypass_rls', true) = 'true')`, "free_resource_catalog")
	if !d.rlsPoliciesCurrent(ctx, "free_resource_catalog", false, []string{omniDDL}, "tenant_isolation_policy") {
		t.Errorf("omnifree policy guard should report current after ensure")
	}
	if !d.triggersCurrent(ctx, "free_resource_catalog", []string{`CREATE TRIGGER update_free_resource_catalog_updated_at
			BEFORE UPDATE ON free_resource_catalog
			FOR EACH ROW EXECUTE FUNCTION update_updated_at_column()`}) {
		t.Errorf("omnifree trigger guard should report current after ensure")
	}
	// omnifree 全表守卫：再跑一遍 ensure 必须整体幂等（守卫命中路径）。
	if err := d.ensureOmniFreeSchema(ctx); err != nil {
		t.Fatalf("repeat ensureOmniFreeSchema: %v", err)
	}
}
