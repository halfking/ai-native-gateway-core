package db

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Contract test: db.go must wire ensureRequestLogsCurrentMonthView into the
// startup migration chain, and the ensure must pin the **session-only** view
// contract — the shape whose loss broke every /api/logs request in the
// 2026-09-07 42P01 incident.
//
// ★ 2026-10-20（迁移 890，v1 族最终删除）：本门的契约表**整体反转**。
//
// 原契约表钉的是 v1 时代的形状：v1 宽表的两条包装链
// （`…_without_customer_id` / `…_without_request_class_due_at`）、
// v1 基表字面量（`public._request_logs` / `public._request_logs_hot`）、
// 以及为**冻结链**准备的 696/700 条件 lateral
// （`baseHasFingerprint` / `hotHasRawModelName` / `LEFT JOIN LATERAL` …）。
//
// v1 族被 DROP 之后，那些形状**一个字都不该再出现**：canonical 视图只剩
// 两臂（`session_turns_hot ∪ session_turns` + 734 details LEFT JOIN），
// 包装链没有对象（那两张视图被 890 一起删了），条件 lateral 也没有对象
// （它们是为「基线链是冻结的」这件事准备的）。
//
// ⇒ 反转后的契约表有两类条目，缺一不可：
//
//	① **必须在场**（正向）：canonical 视图名、投影列、两条会话臂、
//	   details 家族、幂等探针的三个量（canonicalExists / bodyIsSessionOnly /
//	   bodyHasV1Arm）、快路径短路。
//	② **必须不在场**（负向）：v1 族的任何字面量与包装链视图名。
//	   这一类必须有**阳性对照**（见下）——否则「负向断言恒绿」与
//	   「它真的在查」不可区分。
func TestApplyMigrationsIncludesRequestLogsViewEnsure(t *testing.T) {
	source, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "ensureRequestLogsCurrentMonthView(migCtx)") {
		t.Error("db.go must call ensureRequestLogsCurrentMonthView in applyMigrationsOnce")
	}
	ensureSrc, err := os.ReadFile("request_logs_view_schema.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(ensureSrc)

	// ---- ① 必须在场的正向契约 ----
	for _, want := range []string{
		// The three probe quantities the idempotent fast path is built on.
		// bodyHasV1Arm is the **negative** one: it is what lets the composer
		// notice that a pre-890 view (with a v1 arm) is still installed and
		// replace it. Without it, a v1-arm view would survive the boot and
		// every read of it would 42P01 on the dropped family.
		"canonicalExists",
		"bodyIsSessionOnly",
		"bodyHasV1Arm",
		// The shared projection builders (the two arms must project the same
		// columns, in the same order, or the UNION's positional typing breaks).
		"sessionFamilyProjection",
		"SessionFamilyTurnsForSessionSQL",
		// The v4 view comment (the operator-visible statement of which
		// generation is installed).
		"canonicalSessionOnlyComment",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("request_logs_view_schema.go missing contract %q", want)
		}
	}

	// ---- ①' 必须在场的正向契约（SQL 字面量层）----
	//
	// 这一层查的是**字符串字面量**里的 SQL：视图名、投影列、两条会话臂、
	// details 家族、UNION ALL。
	//
	// ⚠ 为什么把「标识符层」与「SQL 层」分开：注释里也会出现这些名字
	//（本仓的注记文化是**大量**在注释里讲历史），而 `stripSQLComments`
	//（同包已有）剥的是 **SQL 注释**（`--` / `/* */`），剥不掉 **Go 注释**
	//（`//`）⇒ 用它当量具会把注记判成活代码。
	// ⇒ 正确量具是**只取字符串字面量**（go/ast 精确到字面量）。
	lits := goStringLiterals(t, "request_logs_view_schema.go")
	for _, want := range []string{
		"request_logs_with_current_month",
		"customer_id",
		"request_class",
		"due_at",
		// The session family's two arms + the 734 details feature layer.
		"session_turns_hot",
		"session_turns",
		"session_turn_details",
		"UNION ALL",
	} {
		if !containsIdentifierIn(lits, want) {
			t.Errorf("request_logs_view_schema.go 的 SQL 字面量里缺少 %q", want)
		}
	}

	// ---- ② 必须不在场的负向契约 ----
	//
	// ⚠ 为什么这一类必须有**阳性对照**：负向断言（"文件里不该有 X"）在
	// 判据写坏时（例如误写成检查一个永远不出现的串）会**恒绿**，
	// 而「检查没跑成」与「检查通过」必须长得不一样。
	//
	// ⚠ 为什么用 `containsSQLIdentifier` 而不是 `strings.Contains`：
	// 朴素包含会把 `public.request_logs_with_current_month`（**必须在场**的
	// canonical 视图名）判成「出现了 public.request_logs」——那是假阳性，
	// 而且是那种「改对代码后门反而更红」的方向。⇒ 判定必须按**标识符边界**。
	//
	// ⚠ 为什么按**标识符边界**判：`public.request_logs` 是
	// `public.request_logs_with_current_month` 的**前缀**，朴素包含会把
	// 「必须在场」的 canonical 视图名判成「必须不在场」——那是「改对代码后
	// 门反而更红」的方向。
	for _, unwanted := range []string{
		// v1 族基表（两代物理名都算：部署通道会词法改写）。
		"public._request_logs",
		"public.request_logs",
		"_request_logs_hot",
		"request_logs_hot",
		"_request_logs_bodies",
		// 890 一起删掉的那两张包装链视图：composer 不该再重建它们。
		"request_logs_with_current_month_without_customer_id",
		"request_logs_with_current_month_without_request_class_due_at",
		// 为冻结链准备的 696/700 条件 lateral（其对象已不存在）。
		"baseHasFingerprint",
		"baseHasRawModelName",
		"hotHasFingerprint",
		"hotHasRawModelName",
		"source.raw_model_name",
		"NOT IN ('customer_id', 'request_class', 'due_at')",
		"LEFT JOIN LATERAL",
	} {
		if containsIdentifierIn(lits, unwanted) {
			t.Errorf("request_logs_view_schema.go 的 SQL 字面量里仍出现 %q —— v1 族已被迁移 890 整体 DROP，"+
				"composer 里任何提到它（或为冻结链准备的包装/条件 lateral）的语句都会 42P01，"+
				"而 CREATE OR REPLACE VIEW 是**即时**校验的 ⇒ 整个 boot 失败。",
				unwanted)
		}
	}

	// ---- 阳性对照：证明上面那条负向判据**分得清** ----
	//
	// 喂一段确实含 v1 族引用的文本，断言它**会**被同一条判据抓到。
	// 没有它，上面那个循环若把判据写坏（例如比较了一个永远不出现的串），
	// 整段会恒绿。
	//
	// ⚠ 对照里**必须包含** `public.request_logs_with_current_month`
	//（canonical 视图名）—— 否则这条对照就测不到「边界判定」这件事本身：
	// 一个只做朴素包含的判据也会通过它，而它恰恰是这里要防的失效。
	sample := []string{
		"SELECT * FROM public._request_logs_hot rl UNION ALL SELECT * FROM public.session_turns t",
		"-- canonical view: public.request_logs_with_current_month",
	}
	if !containsIdentifierIn(sample, "public._request_logs_hot") {
		t.Error("阳性对照失效：含 v1 族引用的样本没有被负向判据抓到 —— " +
			"上面那个负向断言此时恒绿，测不到任何东西")
	}
	if containsIdentifierIn(sample, "public.request_logs") {
		t.Error("阳性对照失效：canonical 视图名 public.request_logs_with_current_month " +
			"被当成了 v1 基表 public.request_logs —— 判据没有按标识符边界判定，" +
			"它会把「必须在场」的 canonical 视图名判成「必须不在场」")
	}
	// 反向对照：v1 **母表**与 v1 **hot 表**是两个不同的关系，
	// 边界判定必须分得开（`public._request_logs` 不该匹配 `public._request_logs_hot`）。
	if containsIdentifierIn(sample, "public._request_logs") {
		t.Error("边界判定过宽：public._request_logs_hot 被当成了 public._request_logs —— " +
			"两者是不同的关系（母表 vs 热表），判据必须按标识符边界区分")
	}
}

// goStringLiterals 解析一个 Go 文件，返回它**全部字符串字面量**的值
// （含解释型字符串与 raw string）。
//
// 为什么需要它：判定「composer 的 SQL 里有没有提到 v1 族」时，量具必须是
// **可执行的那部分**——Go 注释里讲历史时也会提到这些名字，而本仓的注记文化
// 是**大量**讲历史。用整份源码当量具会把注记判成活代码（假阳性，
// 且方向是「改对代码后门反而更红」）；用「剥 SQL 注释」的量具也不行
// （`stripSQLComments` 剥 `--` / `/* */`，剥不掉 Go 的 `//`）。
//
// ⇒ 精确量具 = **字符串字面量集合**。go/ast 给出它，不需要正则。
func goStringLiterals(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		v, err := strconv.Unquote(lit.Value)
		if err != nil {
			// raw string 用 strconv.Unquote 也能解；解不了就退回字面量原文。
			v = lit.Value
		}
		out = append(out, v)
		return true
	})
	return out
}

// containsIdentifierIn 报告 lits 里是否出现了**独立**的标识符 ident。
//
// 「独立」= 前后两侧都不是 [A-Za-z0-9_]。这条边界是必须的：
// `public.request_logs` 是 `public.request_logs_with_current_month` 的**前缀**，
// 朴素包含会把「必须在场」的 canonical 视图名判成「必须不在场」。
func containsIdentifierIn(lits []string, ident string) bool {
	isWord := func(b byte) bool {
		return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
	}
	for _, s := range lits {
		for i := 0; i+len(ident) <= len(s); i++ {
			if s[i:i+len(ident)] != ident {
				continue
			}
			if i > 0 && isWord(s[i-1]) {
				continue
			}
			end := i + len(ident)
			if end < len(s) && isWord(s[end]) {
				continue
			}
			return true
		}
	}
	return false
}

// Live round-trip: rebuild the whole wrapper chain from scratch hot/parent
// tables inside a dedicated scratch database, then verify the canonical view
// matches the production column contract (intersection base + customer_id +
// request_class + due_at, with system_fingerprint/raw_model_name either
// inherited via the dynamic base intersection or appended by the 696/700
// conditional laterals on frozen chains) and that a HOT_ONLY hot column never
// breaks the UNION.
//
// Gated on TEST_PG_DSN so CI stays offline-green; run locally:
//
//	TEST_PG_DSN='postgres://llm_gateway:…@127.0.0.1:5432/llm_gateway?sslmode=disable' \
//	  go test ./db/ -run TestRequestLogsCurrentMonthViewEnsureRoundTrip -count=1 -v
func TestRequestLogsCurrentMonthViewEnsureRoundTrip(t *testing.T) {
	dsn := resolveTestDSN()
	if dsn == "" {
		t.Skip("TEST_PG_DSN (or LLM_GATEWAY_TEST_PG_DSN) not set — offline mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Scratch database on the same server: the ensure hardcodes the public
	// schema, so a throwaway database (not schema) is the only way to test
	// the full chain rebuild without touching the shared instance's views.
	scratchName := "llmgw_view_ensure_test"

	// NOTE: pgx.ConnConfig.ConnString() does not reflect a mutated Database
	// field (it re-serializes the original conn string), so re-parsing a
	// serialized string would silently reconnect to the original database.
	// Parse into pool configs once and override the embedded ConnConfig.
	adminPoolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse admin pool config: %v", err)
	}
	adminPoolCfg.ConnConfig.Database = "postgres"
	adminPool, err := pgxpool.NewWithConfig(ctx, adminPoolCfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	if _, err := adminPool.Exec(ctx,
		`DROP DATABASE IF EXISTS `+scratchName); err != nil {
		t.Fatalf("drop stale scratch db: %v", err)
	}
	if _, err := adminPool.Exec(ctx,
		`CREATE DATABASE `+scratchName); err != nil {
		t.Fatalf("create scratch db: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		adminPool.Exec(dropCtx, `DROP DATABASE IF EXISTS `+scratchName)
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

	// Minimal hot/parent pair: a shared base column set, the later-appended
	// columns (customer_id/request_class/due_at/system_fingerprint(487/603)/
	// raw_model_name(485/603)) on both sides, and a HOT_ONLY column that
	// exists only on hot — the exact shape that made the 341-style SELECT *
	// UNION replay fail and drop the view. The fingerprint/model-name columns
	// must exist on the tables or the 696/700 lateral shape probes have
	// nothing real to probe (this schema predated 696 and made the live
	// round-trip fail with 42703 on h.system_fingerprint).
	schema := `
		CREATE TABLE public._request_logs (
			id bigserial PRIMARY KEY,
			request_id text NOT NULL,
			ts timestamptz NOT NULL DEFAULT now(),
			tenant_id text NOT NULL DEFAULT '',
			prompt_tokens bigint NOT NULL DEFAULT 0,
			customer_id bigint,
			request_class text NOT NULL DEFAULT 'immediate',
			due_at timestamptz,
			system_fingerprint text,
			raw_model_name text,
			CONSTRAINT request_logs_request_class_due_at_check
				CHECK ((request_class = 'immediate' AND due_at IS NULL)
					OR (request_class = 'scheduled' AND due_at IS NOT NULL))
		);
		CREATE TABLE public._request_logs_hot (
			id bigserial PRIMARY KEY,
			request_id text NOT NULL,
			ts timestamptz NOT NULL DEFAULT now(),
			tenant_id text NOT NULL DEFAULT '',
			prompt_tokens bigint NOT NULL DEFAULT 0,
			caller_id text, -- HOT_ONLY: must never leak into the UNION
			customer_id bigint,
			request_class text NOT NULL DEFAULT 'immediate',
			due_at timestamptz,
			system_fingerprint text,
			raw_model_name text,
			CONSTRAINT request_logs_hot_request_class_due_at_check
				CHECK ((request_class = 'immediate' AND due_at IS NULL)
					OR (request_class = 'scheduled' AND due_at IS NOT NULL))
		);
		INSERT INTO public._request_logs_hot (request_id, ts, caller_id, customer_id, request_class, due_at, system_fingerprint, raw_model_name)
		VALUES ('req-hot', now(), 'hot-only', 42, 'scheduled', now() + interval '1 hour', 'fp-hot-a', 'model-alpha');
		INSERT INTO public._request_logs (request_id, ts, customer_id, request_class, due_at, system_fingerprint, raw_model_name)
		VALUES ('req-parent', now(), 7, 'immediate', NULL, 'fp-parent-b', 'model-beta');
	`
	if _, err := pool.Exec(ctx, schema); err != nil {
		t.Fatalf("seed scratch schema: %v", err)
	}

	db := &DB{pool: pool}
	if err := db.ensureRequestLogsCurrentMonthView(ctx); err != nil {
		t.Fatalf("ensure (cold chain rebuild): %v", err)
	}
	// Idempotency: a second pass on a healthy chain must be a no-op.
	if err := db.ensureRequestLogsCurrentMonthView(ctx); err != nil {
		t.Fatalf("ensure (idempotent second pass): %v", err)
	}

	var colCount, baseCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = 'request_logs_with_current_month'
	`).Scan(&colCount); err != nil {
		t.Fatalf("count view columns: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(DISTINCT h.attname) FROM pg_attribute h
		JOIN pg_attribute p ON p.attrelid = 'public._request_logs'::regclass AND p.attname = h.attname
		WHERE h.attrelid = 'public._request_logs_hot'::regclass
		  AND h.attnum > 0 AND NOT h.attisdropped
		  AND p.attnum > 0 AND NOT p.attisdropped
		  AND h.attname NOT IN ('customer_id', 'request_class', 'due_at')
	`).Scan(&baseCount); err != nil {
		t.Fatalf("count base columns: %v", err)
	}
	// Dynamic-rebuild shape (b): the base intersection already carries
	// system_fingerprint/raw_model_name (both tables have them), so the
	// canonical only appends customer_id + request_class + due_at via the
	// 577/610 stages — the fingerprint/model-name columns ride v.*.
	if colCount != baseCount+3 { // base(incl. fp+raw) + customer_id + request_class + due_at
		t.Fatalf("canonical view column count = %d, want %d (base %d + customer_id + request_class + due_at)",
			colCount, baseCount+3, baseCount)
	}

	// HOT_ONLY column must be absent from the view; appended columns present.
	var hotOnly, hasClass, hasDueAt, hasCustomer, hasFingerprint, hasRawModelName bool
	if err := pool.QueryRow(ctx, `
		SELECT
			bool_or(column_name = 'caller_id'),
			bool_or(column_name = 'request_class'),
			bool_or(column_name = 'due_at'),
			bool_or(column_name = 'customer_id'),
			bool_or(column_name = 'system_fingerprint'),
			bool_or(column_name = 'raw_model_name')
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = 'request_logs_with_current_month'
	`).Scan(&hotOnly, &hasClass, &hasDueAt, &hasCustomer, &hasFingerprint, &hasRawModelName); err != nil {
		t.Fatalf("probe view column contract: %v", err)
	}
	if hotOnly {
		t.Error("HOT_ONLY column caller_id leaked into the canonical view")
	}
	if !hasClass || !hasDueAt || !hasCustomer || !hasFingerprint || !hasRawModelName {
		t.Errorf("appended columns missing (request_class=%v due_at=%v customer_id=%v system_fingerprint=%v raw_model_name=%v)",
			hasClass, hasDueAt, hasCustomer, hasFingerprint, hasRawModelName)
	}

	// Data round-trip through the view from both sides of the UNION.
	var hotCustomer, parentCustomer *int64
	var hotClass, hotRaw, hotFP, parentRaw string
	if err := pool.QueryRow(ctx, `
		SELECT customer_id, request_class, raw_model_name, system_fingerprint FROM request_logs_with_current_month
		WHERE request_id = 'req-hot'
	`).Scan(&hotCustomer, &hotClass, &hotRaw, &hotFP); err != nil {
		t.Fatalf("read hot row via view: %v", err)
	}
	if hotCustomer == nil || *hotCustomer != 42 || hotClass != "scheduled" || hotRaw != "model-alpha" || hotFP != "fp-hot-a" {
		t.Errorf("hot row via view = (%v, %q, %q, %q), want (42, scheduled, model-alpha, fp-hot-a)", hotCustomer, hotClass, hotRaw, hotFP)
	}
	if err := pool.QueryRow(ctx, `
		SELECT customer_id, raw_model_name FROM request_logs_with_current_month
		WHERE request_id = 'req-parent'
	`).Scan(&parentCustomer, &parentRaw); err != nil {
		t.Fatalf("read parent row via view: %v", err)
	}
	if parentCustomer == nil || *parentCustomer != 7 || parentRaw != "model-beta" {
		t.Errorf("parent row via view = (%v, %q), want (7, model-beta)", parentCustomer, parentRaw)
	}

	// Frozen-chain replay (migration 700's reason to exist): wrappers created
	// before 485/603 — the base intersection carries neither
	// system_fingerprint nor raw_model_name — plus the canonical dropped
	// out-of-band (680-incident style). The ensure must reuse the stale
	// wrappers and append fingerprint + model name on the conditional lateral
	// instead of crashing or silently omitting them. This is the exact shape
	// found on the local deploy DB and the shared 252 production PG, where
	// the drift scanner's SELECT hit 42703 on every cycle.
	if _, err := pool.Exec(ctx, `
		DROP VIEW public.request_logs_with_current_month;
		DROP VIEW public.request_logs_with_current_month_without_request_class_due_at;
		DROP VIEW public.request_logs_with_current_month_without_customer_id;
	`); err != nil {
		t.Fatalf("drop dynamic chain: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE VIEW public.request_logs_with_current_month_without_customer_id AS
		SELECT id, request_id, ts, tenant_id, prompt_tokens FROM public._request_logs_hot
		UNION ALL
		SELECT id, request_id, ts, tenant_id, prompt_tokens FROM public._request_logs
	`); err != nil {
		t.Fatalf("create stale pre-485 base wrapper: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE VIEW public.request_logs_with_current_month_without_request_class_due_at AS
		SELECT v.*, m.customer_id
		FROM public.request_logs_with_current_month_without_customer_id v
		LEFT JOIN LATERAL (
			SELECT customer_id FROM public._request_logs_hot
			WHERE request_id = v.request_id AND ts = v.ts
			UNION ALL
			SELECT customer_id FROM public._request_logs
			WHERE request_id = v.request_id AND ts = v.ts
			LIMIT 1
		) m ON true
	`); err != nil {
		t.Fatalf("create stale 577 wrapper: %v", err)
	}
	if err := db.ensureRequestLogsCurrentMonthView(ctx); err != nil {
		t.Fatalf("ensure (frozen-chain rebuild): %v", err)
	}
	// Idempotency: a second pass on the rebuilt chain must be a no-op.
	if err := db.ensureRequestLogsCurrentMonthView(ctx); err != nil {
		t.Fatalf("ensure (frozen-chain idempotent second pass): %v", err)
	}

	// 5 stale base + customer_id + request_class + due_at + system_fingerprint (696) + raw_model_name (700).
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = 'request_logs_with_current_month'
	`).Scan(&colCount); err != nil {
		t.Fatalf("count frozen-rebuild view columns: %v", err)
	}
	if colCount != 10 {
		t.Fatalf("frozen-rebuild canonical column count = %d, want 10 (5 stale base + customer_id + request_class + due_at + system_fingerprint + raw_model_name)", colCount)
	}
	// The scanner's exact projection must now parse and return data.
	var scanFP, scanRaw string
	if err := pool.QueryRow(ctx, `
		SELECT raw_model_name, system_fingerprint FROM request_logs_with_current_month
		WHERE request_id = 'req-hot'
	`).Scan(&scanRaw, &scanFP); err != nil {
		t.Fatalf("scanner-shape SELECT on frozen rebuild: %v", err)
	}
	if scanRaw != "model-alpha" || scanFP != "fp-hot-a" {
		t.Errorf("scanner-shape SELECT = (%q, %q), want (model-alpha, fp-hot-a)", scanRaw, scanFP)
	}
}

// TestSessionFamilyTurnsForSessionSQLPushesPredicate 钉住「会话谓词下推」这一
// 性能不变量。
//
// 视图与本 helper 投影同一份列契约，但 gw_session_id 在两边都是
// (CASE WHEN t.session_id LIKE 'sys:%' THEN NULL ELSE t.session_id END) 表达式：
// 按投影名过滤就用不上 idx_session_turns_session。下推成 `t.session_id = $1`
// 后走原始列，本机同一会话 EXPLAIN ANALYZE 实测 188.5ms → 1.9ms。
//
// 一旦有人把 WHERE 挪回外层、或只加到一条腿上，这里必须变红——否则退化是
// 静默的（SQL 仍然正确，只是慢 100 倍，没有任何测试会失败）。
func TestSessionFamilyTurnsForSessionSQLPushesPredicate(t *testing.T) {
	sql := SessionFamilyTurnsForSessionSQL()

	if n := strings.Count(sql, "WHERE t.session_id = $1"); n != 2 {
		t.Fatalf("session predicate must be pushed into BOTH legs (hot + parent), found %d: %s", n, sql)
	}
	// 外层调用方若再加 gw_session_id 谓词会退回表达式过滤；这里确认投影名
	// 本身仍是表达式（契约未被动过）。
	if !strings.Contains(sql, "AS gw_session_id") {
		t.Fatal("frozen column contract must still project gw_session_id")
	}
	// 与波1 的免下推版本并存：那个是给 ts 窗口日志查询用的，不能被顺手改掉。
	if strings.Contains(SessionFamilyTurnsSourceSQL(), "WHERE t.session_id = $1") {
		t.Fatal("SessionFamilyTurnsSourceSQL is the ts-window variant; it must stay predicate-free")
	}
	if strings.Contains(sql, "_request_logs") {
		t.Fatal("session-native source must not reference the retired _request_logs family")
	}
}

// resolveTestDSN 见 view_schema_v2_contract_test.go（同包唯一定义）。
