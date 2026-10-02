package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Contract test (offline, no DB): the Go-side session projection must carry
// the same expression sequence as the migration file's `proj` block (734:
// details-joined canonical), and the Go DDL composer must mirror the
// migration's shape-conditional lateral composition plus the hasDetails
// fallback (no d.* references on 733-less databases). This is the cheap first
// line of defense; the full viewdef equivalence runs live in
// TestRequestLogsViewV2EnsureMatchesMigration.
func TestViewV2ProjectionContractSync(t *testing.T) {
	// Locate the migration file relative to the db package.
	sqlPath := filepath.Join("..", "sql", "migrations", "startup",
		"734_request_logs_view_details_join.sql")
	raw, err := os.ReadFile(sqlPath)
	if err != nil {
		t.Fatalf("read migration file: %v", err)
	}
	src := string(raw)

	projStart := strings.Index(src, "proj := $proj$")
	projEnd := strings.Index(src, "$proj$;")
	if projStart < 0 || projEnd < 0 || projEnd <= projStart {
		t.Fatal("migration file no longer carries the $proj$ block; update this contract test")
	}
	sqlProj := src[projStart+len("proj := $proj$") : projEnd]

	// Normalize both sides: strip line comments, collapse whitespace.
	normalize := func(s string) string {
		lineComment := regexp.MustCompile(`--[^\n]*`)
		spaceRun := regexp.MustCompile(`\s+`)
		return spaceRun.ReplaceAllString(lineComment.ReplaceAllString(s, " "), " ")
	}
	// The migration proj uses comma-prefix continuation; the Go const uses
	// comma-suffix. Split both into expressions on top-level commas after
	// normalization.
	splitExprs := func(s string) []string {
		var parts []string
		depth := 0
		cur := strings.Builder{}
		for _, r := range s {
			switch r {
			case '(':
				depth++
			case ')':
				depth--
			case ',':
				if depth == 0 {
					parts = append(parts, strings.TrimSpace(cur.String()))
					cur.Reset()
					continue
				}
			}
			cur.WriteRune(r)
		}
		if strings.TrimSpace(cur.String()) != "" {
			parts = append(parts, strings.TrimSpace(cur.String()))
		}
		return parts
	}

	goExprs := splitExprs(normalize(sessionFamilyProjection(true)))
	sqlExprs := splitExprs(normalize(sqlProj))
	if len(goExprs) == 0 || len(sqlExprs) == 0 {
		t.Fatalf("empty projection (go=%d sql=%d)", len(goExprs), len(sqlExprs))
	}
	// 734 冻结块（113 列）必须是 Go 投影的前缀；其后仅允许**登记过**的追加
	// 尾列——再增列必须先落迁移，再往 registeredProjectionAppends 加一行。
	//
	// 登记成表而不是四段手写断言：原形态是「计数 +2」加两条逐位比对，下一次
	// 追加要把三处魔数一起改，而少改一处的表现是门在**正确代码**上报
	// 「count drifted」——一个读不懂自己失败原因的门。表驱动后，追加是一行，
	// 失败信息直接是「第 N 个追加列漂移：期望 X，实得 Y」。
	if len(goExprs) != len(sqlExprs)+len(registeredProjectionAppends) {
		t.Fatalf("projection expression count drifted: go=%d sql(734)=%d registered appends=%d",
			len(goExprs), len(sqlExprs), len(registeredProjectionAppends))
	}
	for i := range sqlExprs {
		if goExprs[i] != sqlExprs[i] {
			t.Fatalf("projection expr %d drifted:\n  go  = %s\n  sql = %s", i+1, goExprs[i], sqlExprs[i])
		}
	}
	for i, want := range registeredProjectionAppends {
		if got := goExprs[len(sqlExprs)+i]; got != want {
			t.Fatalf("registered append #%d drifted:\n  want = %s\n  got  = %s", i+1, want, got)
		}
	}

	// 734 details overlay: the canonical projection must reference the details
	// layer on the 30 contract columns; the legacy fallback must reference
	// none (733-less databases keep the 710 NULL placeholders).
	canonical := sessionFamilyProjection(true)
	legacy := sessionFamilyProjection(false)
	if !strings.Contains(canonical, "d.client_model") ||
		!strings.Contains(canonical, "d.quality_flags") ||
		!strings.Contains(canonical, "d.request_class") {
		t.Error("canonical (734) projection must carry d.* details references")
	}
	if strings.Contains(legacy, "d.") {
		t.Error("legacy (710-fallback) projection must not reference the details alias")
	}

	// Shape-conditional lateral composition: frozen chain laterally appends
	// fp/raw only — credits (738) and client_ip (740) are referenced as
	// v.<col> at the inner-select tail (the 738 regexp insert position),
	// never laterally appended and never carried by a v.* star (star
	// expansion is creation-time-frozen and can never byte-match the
	// migration product). 815 的三列**恒**走 lateral（会话侧 t.<col> 直映、
	// v1 侧无包装链可依），所以它们不属于「条件化」那一类，钉在下一条断言里。
	testMiddleCols := "id, request_id, ts, tenant_id"
	frozenDDL := canonicalV2DDL(testMiddleCols, false, false, false, false, true)
	if !strings.Contains(frozenDDL, "source.system_fingerprint") ||
		!strings.Contains(frozenDDL, "source.raw_model_name") ||
		strings.Contains(frozenDDL, "source.credits_rate_multiplier") ||
		strings.Contains(frozenDDL, "source.client_ip") {
		t.Error("frozen-chain v2 DDL must laterally append fp/raw only — never credits/client_ip")
	}
	// 815 三列在**每一种基座形态**下都必须经 lateral 出现在 v1 分支内层。
	// 少一条的后果不是编译错或 DDL 错，而是 v1 分支那一臂拿到 NULL——
	// 而 UNION ALL 只按位置匹型，列数不变，所以门只能在这里拦。
	for _, shape := range []struct {
		name                       string
		fp, raw, credits, cip, det bool
	}{
		{"frozen", false, false, false, false, true},
		{"dynamic", true, true, true, true, true},
		{"pre-736", false, false, false, false, true},
	} {
		ddl := canonicalV2DDL(testMiddleCols, shape.fp, shape.raw, shape.credits, shape.cip, shape.det)
		for _, col := range []string{"origin_stage", "token_band", "client_forwarded_for"} {
			if !strings.Contains(ddl, "source."+col) {
				t.Errorf("%s base shape: v1 branch must lateral-append source.%s", shape.name, col)
			}
			if !strings.Contains(ddl, "h."+col) || !strings.Contains(ddl, "p."+col) {
				t.Errorf("%s base shape: lateral must select both h.%s and p.%s", shape.name, col, col)
			}
			if !strings.Contains(ddl, "t."+col+" AS "+col) {
				t.Errorf("%s base shape: session branch must project t.%s", shape.name, col)
			}
		}
	}
	// Inner-select tail order (post-738/740 wrapper): middleCols, laterals,
	// then v.credits/v.client_ip; the all-frozen probe composes the
	// contract with no v-refs at all.
	if !strings.Contains(canonicalV2DDL(testMiddleCols, false, false, true, true, true),
		testMiddleCols+", source.request_class, source.due_at, source.origin_stage, source.token_band, source.client_forwarded_for, source.system_fingerprint, source.raw_model_name, v.credits_rate_multiplier, v.client_ip") {
		t.Error("inner select must be middleCols + laterals (815 three, then fp/raw) + v.credits/v.client_ip (738 insert position)")
	}
	if strings.Contains(canonicalV2DDL(testMiddleCols, false, false, false, false, true), "v.credits_rate_multiplier") {
		t.Error("pre-736 wrapper must not reference v.credits_rate_multiplier")
	}
	if dynamicDDL := canonicalV2DDL(testMiddleCols, true, true, true, true, true); strings.Contains(dynamicDDL, "source.system_fingerprint") ||
		strings.Contains(dynamicDDL, "source.raw_model_name") {
		t.Error("dynamic-chain v2 DDL must not re-append columns the base wrapper already carries")
	}
	// Composition width: wrapper with credits+client_ip composes the
	// 118-name contract; missing either falls back to 113 + the 815 three.
	// client_ip 的表达式 2026-10-02 起是有源投影（816），不再是 NULL 补位——
	// 本断言随投影同体更新，钉的是「全宽形态里这一列**不是**补位」。
	if full := canonicalV2DDL(testMiddleCols, true, true, true, true, true); strings.Contains(full, "NULL::inet AS client_ip") ||
		!strings.Contains(full, "THEN t.client_ip::inet END) AS client_ip") ||
		!strings.Contains(full, "NULL::double precision AS credits_rate_multiplier") {
		t.Error("post-738/740 base must compose the full body (credits 补位 + client_ip 有源投影 + 815 three)")
	}
	if stale := canonicalV2DDL(testMiddleCols, true, true, true, false, true); strings.Contains(stale, "AS client_ip") ||
		strings.Contains(stale, "AS credits_rate_multiplier") {
		t.Error("pre-740 base must fall back to the pre-rate contract (113 + 815 three)")
	}
	// preRateColumnOrder 的存在理由：815 的三列排在 credits/client_ip **之后**，
	// 所以「冻结 113 + 三列」不是 canonicalColumnOrderV2 的前缀。若哪天有人
	// 把回退实现改回前缀切片，这三列会连同 credits/client_ip 一起消失。
	if n := len(preRateColumnOrder()); n != len(canonicalColumnOrderV2)-2 {
		t.Fatalf("preRateColumnOrder must drop exactly credits_rate_multiplier+client_ip: %d vs %d",
			n, len(canonicalColumnOrderV2))
	}
	for _, col := range registeredColumnAppends[2:] {
		found := false
		for _, n := range preRateColumnOrder() {
			if n == col {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("preRateColumnOrder dropped the 815 column %q — the pre-736 fallback would then "+
				"compose a body whose v1 branch has no source for it", col)
		}
	}
	// hasDetails=false fallback: no details JOIN, no d.* references.
	if legacyDDL := canonicalV2DDL(testMiddleCols, false, false, false, false, false); strings.Contains(legacyDDL, "session_turn_details") {
		t.Error("hasDetails=false DDL must not join the details family")
	}
	if detailsDDL := canonicalV2DDL(testMiddleCols, false, false, false, false, true); !strings.Contains(detailsDDL, "LEFT JOIN public.session_turn_details_hot") ||
		!strings.Contains(detailsDDL, "LEFT JOIN public.session_turn_details ") {
		t.Error("hasDetails=true DDL must LEFT JOIN both details hot and parent")
	}

	// Name-order contract: the migration file's $names$ block (734 frozen
	// 113) must be a prefix of the Go canonicalColumnOrderV2, followed by the
	// registered 738/740 appends (UNION ALL matches types positionally).
	namesStart := strings.Index(src, "names := $names$")
	namesEnd := strings.Index(src, "$names$;")
	if namesStart < 0 || namesEnd < 0 || namesEnd <= namesStart {
		t.Fatal("migration file no longer carries the $names$ block; update this contract test")
	}
	sqlNamesRaw := src[namesStart+len("names := $names$") : namesEnd]
	sqlNames := strings.Split(normalize(sqlNamesRaw), ",")
	for i := range sqlNames {
		sqlNames[i] = strings.TrimSpace(sqlNames[i])
	}
	if len(canonicalColumnOrderV2) != len(sqlNames)+len(registeredColumnAppends) {
		t.Fatalf("name count drifted: go=%d sql(734)=%d registered appends=%d",
			len(canonicalColumnOrderV2), len(sqlNames), len(registeredColumnAppends))
	}
	for i := range sqlNames {
		if sqlNames[i] != canonicalColumnOrderV2[i] {
			t.Fatalf("column order drifted at position %d: go=%s sql=%s", i+1, canonicalColumnOrderV2[i], sqlNames[i])
		}
	}
	for i, want := range registeredColumnAppends {
		if got := canonicalColumnOrderV2[len(sqlNames)+i]; got != want {
			t.Fatalf("registered name append #%d drifted:\n  want = %s\n  got  = %s", i+1, want, got)
		}
	}
}

// registeredProjectionAppends / registeredColumnAppends 是 734 冻结 113 列
// 之后的**尾列登记表**，两侧必须逐项一致（表达式文本 + 列名）。
//
// 为什么是表而不是散落的断言：每加一列，原来那套「len == sql+2」加两条
// 手写比对的形态要同步改三处魔数，而漏改一处的表现是门在**正确代码**上报
// count drifted——一个读不出自己失败原因的门比没有门更坏。表驱动之后，
// 追加一列 = 迁移文件 + 两张表各一行；漂移时失败信息直接指出是第几个、
// 期望什么、实得什么。
//
// 登记纪律：先落迁移（815 及后续），再登记；未登记的追加一律视为漂移。
// 追加列的语义裁决见 docs/audit/2026-09-30-session-request-data-re-audit.md
// §9.22（815 的三列 + id/trace_events 被拒的实测依据）。
var registeredProjectionAppends = []string{
	"NULL::double precision AS credits_rate_multiplier", // 738
	// 816：740 落地时是 NULL 补位（理由「session 侧未回填」），该理由已被 252
	// 生产库复测证伪——session 侧 85% 有值、且与 v1 配对 826/826 同义
	// （审计 §9.60.6.1）。改为有源投影。守卫 CASE 与本投影既有的
	// application_id / api_key_id / credential_id 转换同款：text→inet 没有类型
	// 约束，一个畸形值会让整条 canonical 视图的每个读方报错。
	"(CASE WHEN t.client_ip ~ '^[0-9a-fA-F:.]+$' THEN t.client_ip::inet END) AS client_ip", // 816
	"t.origin_stage AS origin_stage",                    // 815
	"t.token_band AS token_band",                        // 815
	"t.client_forwarded_for AS client_forwarded_for",    // 815
}

var registeredColumnAppends = []string{
	"credits_rate_multiplier", // 738
	"client_ip",               // 740
	"origin_stage",            // 815
	"token_band",              // 815
	"client_forwarded_for",    // 815
}

// Live round-trip (TEST_PG_DSN): on a scratch database holding
// the full 113-column dynamic-contract shape, the Go ensure and the 710
// migration file must produce byte-identical canonical view definitions, the
// anti-join must dedup dual-written request_ids, and the D4 'sys:%' NULL
// gw_session_id semantics must hold.
//
//	TEST_PG_DSN='postgres://llm_gateway:…@127.0.0.1:5432/llm_gateway?sslmode=disable' \
//	  go test ./db/ -run TestRequestLogsViewV2EnsureMatchesMigration -count=1 -v
func TestRequestLogsViewV2EnsureMatchesMigration(t *testing.T) {
	dsn := resolveTestDSN()
	if dsn == "" {
		t.Skip("TEST_PG_DSN (or LLM_GATEWAY_TEST_PG_DSN) not set — offline mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	scratchName := "llmgw_view_v2_contract_test"
	adminPoolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse admin pool config: %v", err)
	}
	adminPoolCfg.ConnConfig.Database = "postgres"
	adminPool, err := pgxpool.NewWithConfig(ctx, adminPoolCfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	if _, err := adminPool.Exec(ctx, `DROP DATABASE IF EXISTS `+scratchName); err != nil {
		t.Fatalf("drop stale scratch db: %v", err)
	}
	// template0 + 显式编码：裸 CREATE DATABASE 继承 postmaster locale，与本机
	// template1（C.UTF-8）不一致时直接 22023（2026-09-23 dev 实例 locale 漂移
	// 实证）。template0 不携带 locale 校验对，任何实例态都能建库；测试查询
	// 不依赖 collation 序。
	if _, err := adminPool.Exec(ctx,
		`CREATE DATABASE `+scratchName+` TEMPLATE template0 ENCODING 'UTF8'`); err != nil {
		t.Fatalf("create scratch db: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		adminPool.Exec(dropCtx, `DROP DATABASE IF EXISTS `+scratchName) //nolint:errcheck
		adminPool.Close()
	})

	scratchPoolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse scratch pool config: %v", err)
	}
	scratchPoolCfg.ConnConfig.Database = scratchName
	pool, err := pgxpool.NewWithConfig(ctx, scratchPoolCfg)
	if err != nil {
		t.Fatalf("connect scratch: %v", err)
	}
	defer pool.Close()

	// Clone the REAL frozen contract: the live canonical view's column list
	// minus the five appended names IS the frozen 108-column base contract;
	// both request_logs tables keep exactly those shared columns (plus the
	// appended five and hot-only extras), so the scratch is a faithful
	// frozen-chain shape — the shape production (local + 252) runs today.
	frozen, err := frozenContractColumnList(ctx, pool, dsn)
	if err != nil {
		t.Fatalf("derive frozen contract columns: %v", err)
	}
	ddl, err := cloneTablesFrozenDDL(ctx, dsn, frozen)
	if err != nil {
		t.Fatalf("derive clone DDL from live catalogs: %v", err)
	}
	if _, err := pool.Exec(ctx, ddl+`
		CREATE TABLE public.schema_migrations (version text PRIMARY KEY, description text, applied_at timestamptz DEFAULT now());

		-- Pre-create the frozen-shape wrapper chain (no fp/raw in the base —
		-- the ensure must reuse it and compose the 4-lateral v1 branch).
		CREATE VIEW public.request_logs_with_current_month_without_customer_id AS
		SELECT `+strings.Join(frozen, ", ")+` FROM public.request_logs_hot
		UNION ALL
		SELECT `+strings.Join(frozen, ", ")+` FROM public.request_logs;
		CREATE VIEW public.request_logs_with_current_month_without_request_class_due_at AS
		SELECT v.*, m.customer_id
		FROM public.request_logs_with_current_month_without_customer_id v
		LEFT JOIN LATERAL (
			SELECT customer_id FROM public.request_logs_hot h
			WHERE h.request_id = v.request_id AND h.ts = v.ts
			UNION ALL
			SELECT customer_id FROM public.request_logs p
			WHERE p.request_id = v.request_id AND p.ts = v.ts
			LIMIT 1
		) m ON true;
	`); err != nil {
		t.Fatalf("seed scratch schema: %v", err)
	}

	db := &DB{pool: pool}
	var dbName string
	_ = pool.QueryRow(ctx, `SELECT current_database()`).Scan(&dbName)
	t.Logf("scratch db = %q", dbName)

	// Replay the production migration chain (733 family prerequisite → 710
	// v2 body → 734 details join → 738 credits column → 740 client_ip) so
	// the base wrappers carry the final column contract. The ensure below
	// then composes from the same base shape the migrations produced — the
	// viewdef-equality contract must compare like against like (a frozen
	// pre-738 base would make the ensure compose extra laterals the
	// regexp-append migrations never emit).
	applyMigration := func(name string) {
		t.Helper()
		sqlBytes, err := os.ReadFile(filepath.Join("..", "sql", "migrations", "startup", name))
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		if _, err := pool.Exec(ctx, string(sqlBytes)); err != nil {
			t.Fatalf("apply %s on scratch: %v", name, err)
		}
	}
	applyMigration("733_session_turn_details.sql")
	applyMigration("710_request_logs_view_session_family_v2.sql")
	applyMigration("734_request_logs_view_details_join.sql")
	applyMigration("738_view_chain_credits_rate_multiplier.sql")
	applyMigration("740_view_chain_client_ip.sql")
	applyMigration("815_request_logs_view_stage_band_cff.sql")

	// Cold-build equivalence: drop the canonical and let the ensure rebuild
	// it from the post-740 base shape.
	if _, err := pool.Exec(ctx, `DROP VIEW public.request_logs_with_current_month`); err != nil {
		t.Fatalf("drop canonical before cold-build ensure: %v", err)
	}
	if err := db.ensureRequestLogsCurrentMonthView(ctx); err != nil {
		t.Fatalf("ensure (v2 cold build): %v", err)
	}
	var views int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM pg_views WHERE schemaname='public' AND viewname LIKE 'request_logs%'`).Scan(&views)
	t.Logf("request_logs%% views after ensure = %d", views)
	ensureViewdef := viewDefinition(t, ctx, pool)
	if !strings.Contains(ensureViewdef, "session_turns") {
		t.Fatal("ensure produced a non-v2 body on a session-family database")
	}
	if !strings.Contains(ensureViewdef, "session_turn_details") {
		t.Fatal("ensure must produce the 734 details-joined body when 733 tables exist")
	}
	if !strings.Contains(ensureViewdef, "client_ip") {
		t.Fatal("ensure must compose the 740 client_ip column into the rebuilt body")
	}
	// 816 起 client_ip 必须是**有源**投影且**带守卫**（审计 §9.61）：
	// `t.client_ip::inet` 无守卫时，一个畸形 text 值会打挂整条视图链的每个读方。
	// pg_get_viewdef 会把 `CASE WHEN c THEN x END` 重排成 `WHEN c THEN x` + `END`
	//（外层 CASE 字样消失），所以判据用渲染后仍存在的片段。
	if !strings.Contains(ensureViewdef, "t.client_ip::inet") {
		t.Error("ensure must project t.client_ip on the session branch (816); " +
			"falling back to NULL padding re-creates the __unknown__ client_ip dimension")
	}
	if !strings.Contains(ensureViewdef, "WHEN t.client_ip ~ ") {
		t.Error("ensure's client_ip projection lost its CASE guard (816); " +
			"an unguarded text->inet cast breaks every reader of the canonical view")
	}
	if !strings.Contains(ensureViewdef, "credits_rate_multiplier") {
		t.Fatal("ensure must compose the 738 credits_rate_multiplier column into the rebuilt body")
	}
	for _, col := range []string{"origin_stage", "token_band", "client_forwarded_for"} {
		if !strings.Contains(ensureViewdef, "t."+col) {
			t.Errorf("ensure must compose the 815 session-branch column t.%s into the rebuilt body", col)
		}
		if !strings.Contains(ensureViewdef, "h."+col) {
			t.Errorf("ensure must compose the 815 v1-branch lateral column h.%s into the rebuilt body", col)
		}
	}
	// Idempotency: second pass must not change the definition.
	if err := db.ensureRequestLogsCurrentMonthView(ctx); err != nil {
		t.Fatalf("ensure (idempotent second pass): %v", err)
	}
	if again := viewDefinition(t, ctx, pool); again != ensureViewdef {
		t.Fatal("second ensure pass changed the view definition")
	}

	// Migration equivalence: reset the wrapper chain to the pristine frozen
	// shape (the state production migrations ran against), drop the
	// canonical, replay the production migration order (710 → 734 → 738 →
	// 740), and require the exact same view definition as the ensure. The
	// reset is load-bearing: replaying 710/734 over the already-shaped
	// wrappers re-expands v.* with credits/client_ip and 738's textual
	// insert then duplicates the column (42702).
	if _, err := pool.Exec(ctx, `DROP VIEW public.request_logs_with_current_month CASCADE`); err != nil {
		t.Fatalf("drop canonical before migration replay: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		DROP VIEW IF EXISTS public.request_logs_with_current_month_without_request_class_due_at CASCADE;
		DROP VIEW IF EXISTS public.request_logs_with_current_month_without_customer_id CASCADE;
		CREATE VIEW public.request_logs_with_current_month_without_customer_id AS
		SELECT `+strings.Join(frozen, ", ")+` FROM public.request_logs_hot
		UNION ALL
		SELECT `+strings.Join(frozen, ", ")+` FROM public.request_logs;
		CREATE VIEW public.request_logs_with_current_month_without_request_class_due_at AS
		SELECT v.*, m.customer_id
		FROM public.request_logs_with_current_month_without_customer_id v
		LEFT JOIN LATERAL (
			SELECT customer_id FROM public.request_logs_hot h
			WHERE h.request_id = v.request_id AND h.ts = v.ts
			UNION ALL
			SELECT customer_id FROM public.request_logs p
			WHERE p.request_id = v.request_id AND p.ts = v.ts
			LIMIT 1
		) m ON true;
	`); err != nil {
		t.Fatalf("reset frozen wrappers before migration replay: %v", err)
	}
	migrationSQL, err := os.ReadFile(filepath.Join("..", "sql", "migrations", "startup",
		"710_request_logs_view_session_family_v2.sql"))
	if err != nil {
		t.Fatalf("read migration file: %v", err)
	}
	if _, err := pool.Exec(ctx, string(migrationSQL)); err != nil {
		t.Fatalf("apply 710 migration on scratch: %v", err)
	}
	applyMigration("734_request_logs_view_details_join.sql")
	applyMigration("738_view_chain_credits_rate_multiplier.sql")
	applyMigration("740_view_chain_client_ip.sql")
	applyMigration("815_request_logs_view_stage_band_cff.sql")
	// 816（审计 §9.61）：client_ip 由 NULL 补位改为 session 侧有源投影。
	// **这道门第一次跑就红了**，报「ensure 和 710+734+738+740+815 产出不同
	// viewdef」——因为重放链里没有 816，而 Go ensure 已经是 816 的形态。
	// 这正是它存在的意义：Go 镜像体（生产启动走的那条）与迁移链必须同体，
	// 漏掉一个迁移就会在这里现形，而不是等到某台机器启动时把视图重建歪。
	applyMigration("816_request_logs_view_client_ip_projection.sql")
	migrationViewdef := viewDefinition(t, ctx, pool)
	if migrationViewdef != ensureViewdef {
		t.Fatalf("ensure and migrations 710+734+738+740+815+816 produce different view definitions:\n--- ensure ---\n%s\n--- migration ---\n%s",
			ensureViewdef, migrationViewdef)
	}

	// Data semantics: dedup + D4 NULL passthrough + details overlay + 740
	// client_ip passthrough.
	seed := `
		-- ts 必须给：真库 request_logs.ts 是 NOT NULL，而 v1 分支的 lateral
		-- 以 h.ts = v.ts 关联。旧夹具不写 ts ⇒ lateral 恒不命中 ⇒ v1 分支的
		-- request_class/due_at/fp/raw 全是 NULL，而**没有任何断言碰过这条腿**
		-- （client_ip 走 v.client_ip 绕过了 lateral），所以这个缺陷一直藏着。
		-- 815 的第一批断言是第一个读 lateral 的断言，当场把它挖了出来。
		INSERT INTO public.request_logs_hot (id, request_id, gw_session_id, ts, prompt_tokens, completion_tokens, customer_id, request_class, raw_model_name, client_model, quality_flags, client_ip, origin_stage, token_band, client_forwarded_for)
		VALUES (900001, 'req-v1-only', 'sess-legacy', now(), 10, 5, 1, 'immediate', 'm-alpha', 'cli-alpha', '{}', '203.0.113.7', 'business', 'band-mid', '203.0.113.9')
		     , (901001, 'req-dual', 'sess-dual', now(), 20, 8, 2, 'immediate', 'm-beta', 'cli-beta', '{}', NULL, 'business', 'band-dual-v1', '198.51.100.1');
		INSERT INTO public.request_logs (request_id, gw_session_id, ts, prompt_tokens, completion_tokens, customer_id, request_class, raw_model_name)
		VALUES ('req-parent-only', NULL, now(), 1, 2, 3, 'immediate', 'm-gamma');
		INSERT INTO public.session_turns_hot (session_id, tenant_id, request_id, ts, turn_no, model, success, status_code, credits_charged, partition_date)
		VALUES ('sess-dual', 'default', 'req-dual', now(), 1, 'm-beta-live', true, 200, 7, CURRENT_DATE)
		     , ('sys:probe:cred9:20260914', 'default', 'req-synthetic', now(), 1, 'm-probe', true, 200, 0, CURRENT_DATE);
		INSERT INTO public.session_turns (session_id, tenant_id, request_id, ts, turn_no, model, success, status_code, credits_charged, partition_date)
		VALUES ('sess-live', 'default', 'req-turn-parent', now(), 1, 'm-live', true, 200, 3, CURRENT_DATE);
		-- 733 特征层：req-dual 的 hot 行带特征；req-turn-parent 走 parent 分支
		INSERT INTO public.session_turn_details_hot (session_id, tenant_id, request_id, turn_no, ts, partition_date, client_model, quality_flags, request_class)
		VALUES ('sess-dual', 'default', 'req-dual', 1, now(), CURRENT_DATE, 'cli-beta-client', '{empty_tool_name}', 'immediate');
		INSERT INTO public.session_turn_details (session_id, tenant_id, request_id, turn_no, ts, partition_date, client_model, request_class)
		VALUES ('sess-live', 'default', 'req-turn-parent', 1, now(), CURRENT_DATE, 'cli-live-client', 'scheduled');
	`
	if _, err := pool.Exec(ctx, seed); err != nil {
		t.Fatalf("seed scratch rows: %v", err)
	}

	var dualCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM public.request_logs_with_current_month WHERE request_id = 'req-dual'
	`).Scan(&dualCount); err != nil {
		t.Fatalf("dedup probe failed: %v", err)
	}
	if dualCount != 1 {
		t.Fatalf("anti-join dedup: request_id=req-dual appears %d times, want exactly 1", dualCount)
	}

	// 740 client_ip passthrough: the real source column must surface through
	// the canonical view's v1 branch (B7 data-source fix).
	var v1ClientIP *string
	if err := pool.QueryRow(ctx, `
		SELECT HOST(client_ip) FROM public.request_logs_with_current_month WHERE request_id = 'req-v1-only'
	`).Scan(&v1ClientIP); err != nil {
		t.Fatalf("client_ip passthrough probe failed: %v", err)
	}
	if v1ClientIP == nil || *v1ClientIP != "203.0.113.7" {
		t.Fatalf("client_ip passthrough: got %v, want 203.0.113.7", v1ClientIP)
	}

	var syntheticGW *string
	if err := pool.QueryRow(ctx, `
		SELECT gw_session_id FROM public.request_logs_with_current_month WHERE request_id = 'req-synthetic'
	`).Scan(&syntheticGW); err != nil {
		t.Fatalf("synthetic row probe failed: %v", err)
	}
	if syntheticGW != nil {
		t.Fatalf("D4 synthetic session must surface as NULL gw_session_id on the compat view, got %q", *syntheticGW)
	}

	// Details overlay: hot-branch join surfaces client_model/quality_flags;
	// parent-branch join surfaces request_class; a turn without a details row
	// still yields NULL (LEFT semantics, 710-compatible).
	var dualClientModel string
	var dualQualityFlags []string
	if err := pool.QueryRow(ctx, `
		SELECT client_model::text, quality_flags FROM public.request_logs_with_current_month
		WHERE request_id = 'req-dual'
	`).Scan(&dualClientModel, &dualQualityFlags); err != nil {
		t.Fatalf("details hot overlay probe failed: %v", err)
	}
	if dualClientModel != "cli-beta-client" {
		t.Fatalf("details hot overlay: client_model = %q, want %q", dualClientModel, "cli-beta-client")
	}
	if len(dualQualityFlags) != 1 || dualQualityFlags[0] != "empty_tool_name" {
		t.Fatalf("details hot overlay: quality_flags = %v, want [empty_tool_name]", dualQualityFlags)
	}
	var liveClass string
	if err := pool.QueryRow(ctx, `
		SELECT request_class FROM public.request_logs_with_current_month WHERE request_id = 'req-turn-parent'
	`).Scan(&liveClass); err != nil {
		t.Fatalf("details parent overlay probe failed: %v", err)
	}
	if liveClass != "scheduled" {
		t.Fatalf("details parent overlay: request_class = %q, want %q", liveClass, "scheduled")
	}
	var syntheticClientModel *string
	if err := pool.QueryRow(ctx, `
		SELECT client_model FROM public.request_logs_with_current_month WHERE request_id = 'req-synthetic'
	`).Scan(&syntheticClientModel); err != nil {
		t.Fatalf("missing-details probe failed: %v", err)
	}
	if syntheticClientModel != nil {
		t.Fatalf("turn without details row must surface NULL client_model, got %q", *syntheticClientModel)
	}

	var rows int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM public.request_logs_with_current_month
		WHERE request_id IN ('req-v1-only','req-parent-only','req-turn-parent','req-dual','req-synthetic')
	`).Scan(&rows); err != nil {
		t.Fatalf("coverage probe failed: %v", err)
	}
	if rows != 5 {
		t.Fatalf("view coverage = %d rows, want 5 (both branches visible)", rows)
	}

	// ── 815 三列的数据语义（两侧各一条腿都要有源）─────────────────────────
	// 这三条不是「列存在」而是「值真的出来了」：UNION ALL 只按位置匹型，
	// 少给 v1 分支 lateral 那一列，列数照样相等、查询照样 200，而 v1-only 行
	// 从这三列拿到 NULL——正是 §9.18/§9.22 反复出现的「修好了但变全盲」。
	var stage, band, cff *string
	if err := pool.QueryRow(ctx, `
		SELECT origin_stage, token_band, client_forwarded_for
		FROM public.request_logs_with_current_month WHERE request_id = 'req-v1-only'
	`).Scan(&stage, &band, &cff); err != nil {
		t.Fatalf("815 v1-branch passthrough probe failed: %v", err)
	}
	if stage == nil || *stage != "business" {
		t.Fatalf("815 v1-branch lateral origin_stage = %v, want \"business\" "+
			"(lateral 匹配条件是 h.request_id = v.request_id AND h.ts = v.ts；"+
			"夹具的 v1 行若仍缺 ts，这条腿恒不命中，而症状是 NULL 而非报错)", stage)
	}
	if band == nil || *band != "band-mid" {
		t.Fatalf("815 v1-branch lateral token_band = %v, want \"band-mid\"", band)
	}
	if cff == nil || *cff != "203.0.113.9" {
		t.Fatalf("815 v1-branch lateral client_forwarded_for = %v, want \"203.0.113.9\"", cff)
	}
	// 反连接那条更隐蔽的腿：req-dual 在 v1 与 session_turns 都有行，视图只
	// 输出 session 臂那一行。session 侧缺该列时读方拿 NULL 是**契约内的**
	// 行为（覆盖缺口），但必须只出现一次——若反连接与追加列的组合让 v1 那条
	// 也漏了行/多了一行，dualCount 断言会先变红。
	if _, err := pool.Exec(ctx, `
		UPDATE public.session_turns_hot SET origin_stage = 'node_probe', token_band = 'band-live'
		WHERE request_id = 'req-dual'
	`); err != nil {
		t.Fatalf("stage 815 session-branch values: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT origin_stage, token_band FROM public.request_logs_with_current_month
		WHERE request_id = 'req-dual'
	`).Scan(&stage, &band); err != nil {
		t.Fatalf("815 session-branch passthrough probe failed: %v", err)
	}
	if stage == nil || *stage != "node_probe" {
		t.Fatalf("815 session-branch origin_stage = %v, want \"node_probe\"", stage)
	}
	if band == nil || *band != "band-live" {
		t.Fatalf("815 session-branch token_band = %v, want \"band-live\"", band)
	}

	// ── `id` 的值层契约（§9.27.3）：会话两臂补 NULL，v1 臂透传真值 ─────────────
	//
	// 活库 viewdef 的三条臂（2026-10-02 按 pg_get_viewdef 行号实测）：
	//   L1   SELECT NULL::bigint AS id  ← 臂 FROM session_turns_hot t（会话 hot）
	//   L146 SELECT NULL::bigint AS id  ← 臂 FROM session_turns t     （会话 cold）
	//   L291 SELECT rl.id,              ← v1 臂（rl = request_logs_hot）
	//
	// 所以「`id` 永不投影」只对**会话侧**成立。会话侧不能给 id，因为 v1 的
	// request_logs.id 是请求行 id、session 侧的 id 是 turn id，1,515,984 组同
	// request_id 配对里两者相等 0 次。而 v1-only 的行**必须**带着它真实的
	// request_logs.id 出去，否则只存在于 v1 的历史行会在视图里彻底失去主键。
	// （我曾把这写成「两条臂都补 NULL、决策在两条臂上一致落地」——那是把 v1 臂
	//   当成了会话臂；实测 L291 是真值透传。）
	//
	// 为什么必须值层断言：现有门读的是 DDL 文本层，而读方消费的是查询结果。
	// 文本层说「不投影」而值层给出真 turn id，读方拿不到任何报警——id 一旦有值，
	// 按它排序/去重的读方会正常算出一个错的数。
	//
	// 两条会话臂都要查：req-dual 命中 hot 臂、req-turn-parent 命中 cold 臂。
	// 而 req-dual 的 v1 孪生行带着真 id（901001）却**不能**出现在视图里
	// （反连接去重只留会话臂那一行）——这才是会话臂必须 NULL 的真正含义。
	for _, tc := range []struct {
		requestID string
		arm       string
		wantNil   bool
	}{
		{"req-dual", "会话 hot 臂（反连接后由 session_turns_hot 供给；其 v1 孪生 id=901001 不应外泄）", true},
		{"req-turn-parent", "会话 cold 臂（由 session_turns 供给）", true},
		{"req-v1-only", "v1 臂（必须透传真实 request_logs.id=900001）", false},
	} {
		var id *int64
		if err := pool.QueryRow(ctx,
			`SELECT id FROM public.request_logs_with_current_month WHERE request_id = $1`,
			tc.requestID).Scan(&id); err != nil {
			t.Fatalf("id 值层探针失败 %s: %v", tc.requestID, err)
		}
		if tc.wantNil {
			if id != nil {
				t.Fatalf("%s（%s）的视图 id = %d，必须是 NULL。\n"+
					"会话侧的 id 是 turn id，与 v1 的 request_logs.id 不是同一个东西"+
					"（1,515,984 组同 request_id 配对里相等 0 次）。给一个有值的 id 比给 NULL "+
					"更坏——读方不会报错，只会算出一个错的数。",
					tc.requestID, tc.arm, *id)
			}
			continue
		}
		// v1 臂：不只是「非空」，而是必须**等于源表里那个真值**。只断言非空
		// 太弱——turn id 也是非空的，而那正是本条要防的回归。
		if id == nil {
			t.Fatalf("%s（%s）的视图 id 是 NULL，但它必须透传源表的真值 900001。\n"+
				"v1-only 的行只存在于 request_logs，视图若在这里也补 NULL，"+
				"这批历史行会在视图里彻底失去主键。", tc.requestID, tc.arm)
		}
		if *id != 900001 {
			t.Fatalf("%s（%s）的视图 id = %d，必须是源表的真值 900001。\n"+
				"非空但不是源表值 ⇒ 某条臂在拿 turn id 冒充 v1 的请求行 id。",
				tc.requestID, tc.arm, *id)
		}
	}

	// Down chain in reverse numeric order (740 down → 738 down → 734 down →
	// 733 down → 710 down). 734 down must rebuild the 710 body — its
	// "already v2" probe must NOT mistake the 734 details-joined body for the
	// 710 shape, or the family drop below hits 2BP01 (view dependency).
	applyDown := func(name string) {
		t.Helper()
		sqlBytes, err := os.ReadFile(filepath.Join("..", "sql", "migrations", "startup", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if _, err := pool.Exec(ctx, string(sqlBytes)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	// 816（审计 §9.61）在 815 之上，所以**必须先 down 816**：它的 proj 是
	// 完整的 118 列契约（含 815 那三列），815 down 单独执行会把 816 建的
	// 视图直接拆成 115 列而不报错——「能跑完但结果不是你以为的形态」这一类
	// 半吊子状态，正是本门存在的理由。
	applyDown("816_request_logs_view_client_ip_projection.down.sql")
	postDown816 := viewDefinition(t, ctx, pool)
	if strings.Contains(postDown816, "t.client_ip::inet") {
		t.Fatalf("816 down must remove the client_ip cast from the canonical view; got:\n%s", postDown816)
	}
	if !strings.Contains(postDown816, "client_ip") {
		t.Fatalf("816 down must KEEP the 740 client_ip column (it only replaces an expression); got:\n%s", postDown816)
	}
	applyDown("815_request_logs_view_stage_band_cff.down.sql")
	postDown815 := viewDefinition(t, ctx, pool)
	for _, col := range []string{"origin_stage", "token_band", "client_forwarded_for"} {
		if strings.Contains(postDown815, col) {
			t.Fatalf("815 down must strip %s from the canonical view; got:\n%s", col, postDown815)
		}
	}
	if !strings.Contains(postDown815, "client_ip") {
		t.Fatal("815 down must keep the 740 client_ip column (it strips only its own three)")
	}

	// ── 815 幂等：down 掉自己再 up 一次，viewdef 必须逐字节回到同一份 ──────────
	//
	// 这三段（up → down → up）此前只活在那次「动活库」的手跑里，不可复核：验收时
	// 谁也不敢在活库上重跑 down + up，于是「幂等」这条结论就只存在于一次运行的
	// 记忆里。放进 scratch 库后它变成一道**可重跑**的门。
	//
	// 顺序上必须先清 bookkeeping 行：815.down 按 append-only 惯例（710/734/738 同款）
	// 保留自己的 schema_migrations 行，而该表是 PRIMARY KEY(version)，于是朴素重跑
	// 会在文件末尾的 INSERT 处冲突、整笔事务回滚，视图停在 115 列。响亮地失败可以
	// 接受，但操作者必须知道有这一步——否则「回滚后再前滚」会变成一次静默的空操作。
	clear815Bookkeeping := func(what string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `DELETE FROM public.schema_migrations WHERE version = '815'`); err != nil {
			t.Fatalf("clear 815 bookkeeping row (%s): %v", what, err)
		}
	}
	clear815Bookkeeping("before re-up")
	applyMigration("815_request_logs_view_stage_band_cff.sql")
	// 816 的 down 会删掉自己的 bookkeeping 行（append-only 下它本来也不会冲突），
	// 但显式清理一次，让「重跑 up」不依赖「down 恰好删干净了」这个隐含前提——
	// 幂等门要能在 down 实现变化后仍然成立。
	if _, err := pool.Exec(ctx, `DELETE FROM public.schema_migrations WHERE version = '816'`); err != nil {
		t.Fatalf("clear 816 bookkeeping row: %v", err)
	}
	applyMigration("816_request_logs_view_client_ip_projection.sql")
	reUp815 := viewDefinition(t, ctx, pool)
	if reUp815 != ensureViewdef {
		t.Fatalf("815+816 down 之后再 up，必须逐字节回到同一份 viewdef。\n"+
			"不一致说明迁移依赖了它自己没有建立的前置状态——线上表现是"+
			"「回滚后再前滚，视图少列、多列或表达式被悄悄换掉」，而每次都会被当成偶发。\n"+
			"post-816 viewdef:\n%s\n\nre-up viewdef:\n%s", ensureViewdef, reUp815)
	}
	// 幂等的第二面：三列必须**真的有值**，而不是「列名回来了」。上面 607-624 行把
	// session 分臂夹具改成了 node_probe/band-live，重建后仍要读到它们。列回来了而
	// 值是 NULL，等于把「缺源」伪装成「已迁移」——正是这套视图反复出问题的形状。
	if err := pool.QueryRow(ctx, `
		SELECT origin_stage, token_band FROM public.request_logs_with_current_month
		WHERE request_id = 'req-dual'
	`).Scan(&stage, &band); err != nil {
		t.Fatalf("815 re-up passthrough probe failed: %v", err)
	}
	if stage == nil || *stage != "node_probe" || band == nil || *band != "band-live" {
		t.Fatalf("815 re-up 后三列必须仍有值；got origin_stage=%v token_band=%v。"+
			"（列回来了但值是 NULL = 缺源被伪装成已迁移）", stage, band)
	}
	// 沙箱纪律：探针做完要复原，并把 down 的确定性一并钉住
	//（同一个 down 跑两次结果不同 = 它偷偷读了活库状态，那正是本文件最不敢
	// 依赖的一类输入）。
	//
	// **两次必须从同一状态出发**（2026-10-02 修正）。第一版把「第二次 down」
	// 接在 815+816 的 re-up 之后，而 postDown815 采自 816-down 之后 ——
	// 于是这条断言在比两个**不同起点**的产物，测的是一个我没打算测的量：
	// 815 的 down 对「816 产出的 viewdef」和对「815 产出的 viewdef」做正则
	// 手术，结果本就可能不同。修正后的结构把两件事分开：
	//   ① postDown815 == postDown815From816 —— 816 的存在不扰动 815 的 down 结果；
	//   ② 连做两次同样的 down 序列，产物逐字节相同 —— 真正的确定性。
	downChain := func(what string) string {
		t.Helper()
		if _, err := pool.Exec(ctx, `DELETE FROM public.schema_migrations WHERE version = '816'`); err != nil {
			t.Fatalf("clear 816 bookkeeping row (%s): %v", what, err)
		}
		applyDown("816_request_logs_view_client_ip_projection.down.sql")
		clear815Bookkeeping(what)
		applyDown("815_request_logs_view_stage_band_cff.down.sql")
		return viewDefinition(t, ctx, pool)
	}
	postDown815From816 := downChain("run 1")
	if postDown815From816 != postDown815 {
		t.Fatalf("816 的存在扰动了 815 down 的结果：816-down+815-down 走出来的形态 "+
			"必须与纯 815-down 逐字节相同，否则回滚链的结果取决于「816 有没有跑过」。\n"+
			"pure-815:\n%s\n\nvia-816:\n%s", postDown815, postDown815From816)
	}
	if secondDown := downChain("run 2"); secondDown != postDown815From816 {
		t.Fatalf("815 down 必须确定性：第二次 down 的 viewdef 与第一次不同。\n"+
			"first:\n%s\n\nsecond:\n%s", postDown815From816, secondDown)
	}

	applyDown("740_view_chain_client_ip.down.sql")
	postDown740 := viewDefinition(t, ctx, pool)
	if strings.Contains(postDown740, "client_ip") || !strings.Contains(postDown740, "credits_rate_multiplier") {
		t.Fatal("740 down must strip client_ip while keeping the 738 credits column")
	}
	applyDown("738_view_chain_credits_rate_multiplier.down.sql")
	postDown738 := viewDefinition(t, ctx, pool)
	if strings.Contains(postDown738, "credits_rate_multiplier") {
		t.Fatal("738 down must strip credits_rate_multiplier (pre-736 shape)")
	}
	applyDown("734_request_logs_view_details_join.down.sql")
	postDown732 := viewDefinition(t, ctx, pool)
	if !strings.Contains(postDown732, "session_turns") || strings.Contains(postDown732, "session_turn_details") {
		t.Fatal("734 down must restore the 710 body (v2, no details join)")
	}
	// 733 down then drops the details family cleanly (no view dependency left).
	down733, err := os.ReadFile(filepath.Join("..", "sql", "migrations", "startup",
		"733_session_turn_details.down.sql"))
	if err != nil {
		t.Fatalf("read 733 down: %v", err)
	}
	if _, err := pool.Exec(ctx, string(down733)); err != nil {
		t.Fatalf("apply 733 down: %v", err)
	}
	// 710 down then restores a v1 (request_logs-only) body.
	downSQL, err := os.ReadFile(filepath.Join("..", "sql", "migrations", "startup",
		"710_request_logs_view_session_family_v2.down.sql"))
	if err != nil {
		t.Fatalf("read down migration: %v", err)
	}
	if _, err := pool.Exec(ctx, string(downSQL)); err != nil {
		t.Fatalf("apply 710 down: %v", err)
	}
	if v1Viewdef := viewDefinition(t, ctx, pool); strings.Contains(v1Viewdef, "session_turns") {
		t.Fatal("710 down must restore a v1 (non-session) view body")
	}
}

func viewDefinition(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	var def string
	if err := pool.QueryRow(ctx,
		`SELECT pg_get_viewdef('public.request_logs_with_current_month'::regclass, true)`,
	).Scan(&def); err != nil {
		t.Fatalf("read view definition: %v", err)
	}
	return def
}

// frozenContractColumnList returns the frozen base contract: the live
// canonical view's columns minus the appended names
// (customer_id/request_class/due_at/system_fingerprint/raw_model_name/
// credits_rate_multiplier/client_ip) = the 108 columns the pre-485 base
// wrapper carries, in canonical order.
func frozenContractColumnList(ctx context.Context, scratch *pgxpool.Pool, liveDSN string) ([]string, error) {
	liveCfg, err := pgxpool.ParseConfig(liveDSN)
	if err != nil {
		return nil, fmt.Errorf("parse live DSN: %w", err)
	}
	live, err := pgxpool.NewWithConfig(ctx, liveCfg)
	if err != nil {
		return nil, fmt.Errorf("connect live catalogs: %w", err)
	}
	defer live.Close()

	// 减去**全部**追加列，来源是登记表而不是硬编码清单：硬编码那份在 815
	// 之后会少减三列，于是 frozen 从 108 变 111，报错信息是「frozen base
	// contract = 111 columns, want 108」——一个把「清单没跟上」说成「契约
	// 漂移」的消息，排查方向被直接带偏。
	appended := map[string]bool{
		"customer_id": true, "request_class": true, "due_at": true,
		"system_fingerprint": true, "raw_model_name": true,
	}
	for _, c := range registeredColumnAppends {
		appended[c] = true
	}
	rows, err := live.Query(ctx, `
		SELECT column_name FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'request_logs_with_current_month'
		ORDER BY ordinal_position`)
	if err != nil {
		return nil, fmt.Errorf("canonical column query: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if !appended[name] {
			names = append(names, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(names) != 108 {
		return nil, fmt.Errorf("frozen base contract = %d columns, want 108", len(names))
	}
	return names, nil
}

// cloneTablesFrozenDDL derives CREATE TABLE statements for the request_logs
// pair restricted to the frozen contract (the shared post-485 columns —
// among them five hot/parent type-conflicting ones — are dropped from BOTH
// sides), plus the session_turns pair cloned in full. The result is a
// type-consistent frozen-chain shape matching production.
func cloneTablesFrozenDDL(ctx context.Context, liveDSN string, frozen []string) (string, error) {
	liveCfg, err := pgxpool.ParseConfig(liveDSN)
	if err != nil {
		return "", fmt.Errorf("parse live DSN: %w", err)
	}
	live, err := pgxpool.NewWithConfig(ctx, liveCfg)
	if err != nil {
		return "", fmt.Errorf("connect live catalogs: %w", err)
	}
	defer live.Close()

	keep := map[string]bool{}
	for _, name := range frozen {
		keep[name] = true
	}
	// 815 的三列必须留在克隆里：迁移 815 对物理 request_logs(_hot) 有源守卫，
	// 缺列即 notice 后 RETURN（不重建），于是列数停在 115 而末尾的 fail-closed
	// 对账会报「rebuild did not converge」——一个指向「重建逻辑坏了」而真因是
	// 「夹具少了一列」的失败信息。夹具要忠实反映 428/485 之后的物理表。
	//
	// ⚠️ 同一段代码**只发「列名 + 类型」**：不带 NOT NULL，也不带 DEFAULT（生产形态
	// 是 `id bigint NOT NULL DEFAULT nextval('request_logs_id_seq')`）。
	//
	// 后果不是「夹具宽松一点」，而是**任何依赖某列真实取值的断言都会平凡通过**：
	// 夹具不写 id ⇒ 它在 scratch 里恒为 NULL ⇒ 「id 必须是 NULL」是 NULL 对 NULL。
	// 这个坑已经踩过一次——`id` 的值层门在 v1 臂上恒绿，而 v1 臂其实应当透传真值。
	//
	// 现在的做法是在 seed 里**显式给 id**（900001 / 901001），而不是给克隆补
	// DEFAULT：补 DEFAULT 需要连带在 scratch 里建序列（顺序依赖、且会让所有
	// 未显式给 id 的夹具行都拿到自动 id），收益只是让夹具更「像生产」。
	// **凡给这个夹具加断言，先问：我要断言的那一列在夹具里有没有真实取值？**
	for _, name := range []string{
		"customer_id", "request_class", "due_at", "system_fingerprint",
		"raw_model_name", "credits_rate_multiplier", "client_ip",
		"origin_stage", "token_band", "client_forwarded_for",
	} {
		keep[name] = true
	}

	var b strings.Builder
	for _, table := range []string{"request_logs", "request_logs_hot"} {
		cols, err := liveColumns(ctx, live, table)
		if err != nil {
			return "", err
		}
		var defs []string
		for _, col := range cols {
			if keep[col.name] {
				defs = append(defs, col.name+" "+col.typ)
			}
		}
		fmt.Fprintf(&b, "CREATE TABLE public.%s (\n%s\n);\n", table, strings.Join(defs, ",\n"))
	}
	for _, table := range []string{"session_turns", "session_turns_hot"} {
		cols, err := liveColumns(ctx, live, table)
		if err != nil {
			return "", err
		}
		defs := make([]string, 0, len(cols))
		for _, col := range cols {
			defs = append(defs, col.name+" "+col.typ)
		}
		fmt.Fprintf(&b, "CREATE TABLE public.%s (\n%s\n);\n", table, strings.Join(defs, ",\n"))
	}
	return b.String(), nil
}

type liveColumn struct {
	name string
	typ  string
}

func liveColumns(ctx context.Context, live *pgxpool.Pool, table string) ([]liveColumn, error) {
	rows, err := live.Query(ctx, `
		SELECT a.attname, format_type(a.atttypid, a.atttypmod)
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relname = $1
		  AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attnum`, table)
	if err != nil {
		return nil, fmt.Errorf("catalog query for %s: %w", table, err)
	}
	defer rows.Close()
	var cols []liveColumn
	for rows.Next() {
		var col liveColumn
		if err := rows.Scan(&col.name, &col.typ); err != nil {
			return nil, err
		}
		cols = append(cols, col)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("table public.%s not found in live catalogs", table)
	}
	return cols, nil
}

// 变量名按仓内契约取：scripts/audit/run-integration-gate.sh 只注入
// TEST_PG_DSN / *_DATABASE_URL / *_DB_URL / *_PG_URL 这几种后缀（注入清单见
// 该脚本的 dsn_runner 段），而 sql/schema/integration_gate_test.go 的
// TestGateInjectsEveryDBCredentialName 正是按这些后缀从仓内**推导**该清单的。
// 用 _PG_DSN 这类不在契约内的名字，harness 永远不会注入 ⇒ 这道门在 CI 上
// 结构性沉睡，却仍以 "ok" 的形式出现在报告里。第二变量名保留给手工运行，
// 但**新代码请直接用 TEST_PG_DSN**，否则重蹈「门禁对沉睡文件不作证」。

func resolveTestDSN() string {
	if v := os.Getenv("TEST_PG_DSN"); v != "" {
		return v
	}
	return os.Getenv("LLM_GATEWAY_TEST_PG_DSN")
}
