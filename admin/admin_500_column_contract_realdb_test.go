package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 2026-10-16 admin 500 修复轮的真实库判据。
//
// 覆盖两个 GET 端点：
//
//  1. GET /api/admin/sessions/inspector-stats
//     旧谓词引用 `session_dim.stop_reason`——该列**从未存在过**（权威 DDL
//     sql/migrations/startup/805_session_dim_reconcile.sql:30-49 只有 17 列；
//     权威写方 internal/sessionv2mirror/session_dim.go:72-83/:104-117 写的
//     正是这 17 列）。真库实测稳定 500：SQLSTATE 42703。
//     修复＝改查询（去掉无法满足的停机原因过滤），不是补列。
//
//  2. GET /api/admin/annotations/first-turn-samples
//     查询引用 `public.sessions.title`，而该列**应该存在但没被建出来**：
//     sql/migrations/startup/456_session_v2_display_columns.sql 有
//     ADD COLUMN IF NOT EXISTS title，但它不在 scripts/apply-db-revision-sequence.sh
//     的通道清单里（实测 grep 计数 0），所以 sessions 的 8 个 456 列一个都不存在
//     （真库 information_schema 实测 0 命中）。写入侧 internal/titlestore/store.go:268
//     与读出侧 admin/turns_sessions.go:152 都假定它在——所以 sessions 不是遗留表，
//     改查询指向别处是错的正解；正解是登记并应用 456。该动作不在本文件所有权内。
//     本测试按**真库实际形态**驱动 handler，把这两态都钉住（见下面的形状分流）。
//
// 判据纪律（照 LESSONS.md「判据纪律」）：
//   - 夹具用 `LIKE public.session_dim` 从真表克隆列，**不允许**凭空多出列；
//     老夹具 admin/session_retirement_http_test.go:86 自造了 `stop_reason text`，
//     正是这种假绿：它让被测谓词在夹具里存在，从而掩盖真库的 42703。
//   - 凡「允许侧」断言都配一条「前提仍在场」断言（stop_reason 必须不存在）。
//   - 断言真 200 且**非空壳**（回收计数 / 注解样本字段有值），不是只看状态码。
//   - 断言跨租户不可见，租户过滤一旦被摘掉即红。
//
// 用法：source /tmp/lgw-env.sh 后
//
//	go test ./admin/ -run 'Admin500|InspectorStatsRecycled|FirstTurnSamples' -count=1 -v
//
// 环境变量 TEST_ADMIN_500_DSN 优先，其次 LLM_GATEWAY_DATABASE_URL / DATABASE_URL。
// 隔离方式分两种，见两个 helper 的注释；本文件只建后即删的隔离 schema / 隔离
// database，**从不写 public、从不删业务行**。

func admin500DSN(t *testing.T) string {
	t.Helper()
	for _, key := range []string{"TEST_ADMIN_500_DSN", "LLM_GATEWAY_DATABASE_URL", "DATABASE_URL"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	t.Skip("set TEST_ADMIN_500_DSN (or `source /tmp/lgw-env.sh` and use LLM_GATEWAY_DATABASE_URL)")
	return ""
}

// newIsolatedSchemaPool 建一个唯一命名的 schema 并把 search_path 指过去。
// 适用于 SQL 里不带 `public.` 前缀的端点（inspector-stats 就是：它写的是裸
// `session_summaries` / `session_dim`）。
func newIsolatedSchemaPool(t *testing.T, dsn string) (*pgxpool.Pool, context.Context) {
	t.Helper()
	ctx := context.Background()
	root, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open root pool: %v", err)
	}
	schema := fmt.Sprintf("admin500_%d", time.Now().UnixNano())
	if _, err = root.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		root.Close()
		t.Fatalf("create schema %s: %v", schema, err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		root.Close()
		t.Fatalf("parse dsn: %v", err)
	}
	// ★★ search_path 必须**追加** public（不能只留隔离 schema）。
	//
	//   本库装了 citus_columnar，库级 EVENT TRIGGER `enforce_columnar_trigger`
	//   → `public.fn_enforce_columnar_event_trigger()` 的函数体里是**不加限定名**地
	//   调用 `columnar_insert_only_parents()`（它自己就在 public）。
	//   ⇒ 而 event trigger 的函数体按**会话当时的 search_path** 解析名字
	//   ⇒ ⇒ search_path 里没有 public ⇒ **每一次 CREATE TABLE 都 42883**
	//   ⇒ ⇒ 连带把 `LIKE public.session_dim INCLUDING DEFAULTS` 复制进来的
	//   DEFAULT 表达式也变成不可解析。
	//
	//   ★ 同款坑仓内已有两处明确记录（`domains/reportrollup/grainreport_e2e_test.go`、
	//   `tools/benchreport/main.go`：都是「追加原值」而不是覆盖）；
	//   公共样板见 `internal/testschema/testschema.go`：`name + ",public,pg_catalog"`。
	//   ★ 隔离性不受影响：public 排在隔离 schema **之后** ⇒ 裸名仍优先命中隔离 schema。
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public,pg_catalog"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		root.Close()
		t.Fatalf("open isolated pool: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		if _, err := root.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
		root.Close()
	})
	return pool, ctx
}

// newIsolatedDatabase 建一个唯一命名的 **database** 并连上去。
//
// 为什么 first-turn 端点不能用隔离 schema：firstTurnFromClause（admin/annotation_handler.go:680-713）
// 把表名写成了 `public.session_turns` / `public.sessions` 等**带 public 前缀**的形式，
// search_path 改不动它。要在完全不触碰业务库 public 的前提下驱动它，只能换一个
// 数据库——那时 `public.` 指向的是这个一次性数据库自己的 public。
//
// 返回的 pool 指向新库；清理＝先关连接池再 DROP DATABASE。业务库 public 全程只读。
func newIsolatedDatabase(t *testing.T, dsn string) (*pgxpool.Pool, context.Context) {
	t.Helper()
	ctx := context.Background()
	root, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open root pool: %v", err)
	}
	name := fmt.Sprintf("lgw_admin500_%d", time.Now().UnixNano())
	if _, err = root.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		root.Close()
		t.Fatalf("create database %s: %v", name, err)
	}
	// 把库名换成新库；其余连接参数（用户/密码/sslmode）沿用。
	idx := strings.LastIndex(dsn, "/")
	if idx < 0 {
		root.Close()
		t.Fatalf("cannot rewrite dsn %q", dsn)
	}
	rest := dsn[idx+1:]
	if q := strings.Index(rest, "?"); q >= 0 {
		rest = rest[q:]
	} else {
		rest = ""
	}
	pool, err := pgxpool.New(ctx, dsn[:idx+1]+name+rest)
	if err != nil {
		root.Close()
		t.Fatalf("open isolated db pool: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		if _, err := root.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
		root.Close()
	})
	return pool, ctx
}

// TestInspectorStatsRecycledTodayRealDB 是本轮 (b) 类修复的判据。
//
// 断言链：
//   - 前提：夹具 session_dim 由真表克隆，且**没有** stop_reason；
//   - 200 且非空壳：super_admin 拿到今日回收 2、活跃 1、关闭 3、token 315、均分 55；
//   - 口径：3 天前关闭的那条**不计入**今日回收；
//   - 跨租户：tenant_admin(acme) 只看到自己的 1 条回收 / 15 token / 均分 80，
//     看不到 other 的回收与 token（租户过滤被摘掉即红）。
func TestInspectorStatsRecycledTodayRealDB(t *testing.T) {
	dsn := admin500DSN(t)
	pool, ctx := newIsolatedSchemaPool(t, dsn)

	if _, err := pool.Exec(ctx, `
		-- 关键：从真表克隆列定义，夹具因此不可能凭空长出 stop_reason。
		CREATE TABLE session_dim (LIKE public.session_dim INCLUDING DEFAULTS);
		CREATE TABLE session_summaries (
			session_key text, tenant_id text,
			total_tokens bigint, health_score numeric
		);

		-- acme：1 活跃 + 1 今日关闭；other：1 今日关闭 + 1 三天前关闭。
		INSERT INTO session_dim (gw_session_id, session_key, tenant_id, status, created_at, closed_at) VALUES
			('s1','s1','acme', 'active', NOW() - INTERVAL '10 days', NULL),
			('s4','s4','acme', 'closed', NOW() - INTERVAL '10 days', NOW()),
			('s2','s2','other','closed', NOW() - INTERVAL '10 days', NOW()),
			('s3','s3','other','closed', NOW() - INTERVAL '10 days', NOW() - INTERVAL '3 days');

		INSERT INTO session_summaries VALUES
			('s1','acme', 10, 90), ('s4','acme', 5, 70),
			('s2','other',100, 50), ('s3','other',200, 10);
	`); err != nil {
		t.Fatalf("seed isolated schema: %v", err)
	}

	// 前提仍在场：stop_reason 必须缺席。真库没有它，所以夹具也不该有它。
	// 若哪天有人补了这一列，这条断言会红——那是**真的**口径变更，必须重读本测试，
	// 不能默默让它变绿。
	var hasStopReason bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = current_schema()
			  AND table_name = 'session_dim'
			  AND column_name = 'stop_reason'
		)`).Scan(&hasStopReason); err != nil {
		t.Fatalf("probe premise: %v", err)
	}
	if hasStopReason {
		t.Fatal("premise broken: fixture session_dim has stop_reason; " +
			"the production schema does not, so this fixture no longer reproduces the incident")
	}

	h := &Handler{db: pool}
	call := func(role, tenant string) SessionInspectorStatsResponse {
		t.Helper()
		req := SetAuthContext(
			httptest.NewRequest(http.MethodGet, "/api/admin/sessions/inspector-stats", nil),
			&AuthContext{Role: role, TenantID: tenant, Username: "probe"},
		)
		w := httptest.NewRecorder()
		h.HandleSessionInspectorStats(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("[%s/%s] HTTP %d body=%s", role, tenant, w.Code, w.Body.String())
		}
		var got SessionInspectorStatsResponse
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("[%s/%s] decode: %v body=%s", role, tenant, err, w.Body.String())
		}
		return got
	}

	// 平台视角（super_admin ⇒ 租户过滤关闭）：非空壳 + 当日口径。
	all := call("super_admin", "acme")
	if all.RecycledToday != 2 {
		t.Errorf("super_admin RecycledToday = %d, want 2 (two closures today)", all.RecycledToday)
	}
	if all.ActiveSessions != 1 || all.ClosedSessions != 3 {
		t.Errorf("super_admin status split = active %d / closed %d, want 1 / 3",
			all.ActiveSessions, all.ClosedSessions)
	}
	if all.TotalTokens != 315 {
		t.Errorf("super_admin TotalTokens = %d, want 315", all.TotalTokens)
	}
	if all.AvgHealthScore != 55 {
		t.Errorf("super_admin AvgHealthScore = %v, want 55", all.AvgHealthScore)
	}
	if all.GeneratedAt.IsZero() {
		t.Error("super_admin GeneratedAt is zero: response is an empty shell")
	}
	// 三天前关闭的 s3 绝不能算进「今日回收」。这条同时是口径钉子：
	// 去掉 closed_at 当日条件后 RecycledToday 会变成 3 → 红。
	if all.ClosedSessions != 3 || all.RecycledToday >= all.ClosedSessions {
		t.Errorf("today-only calibration lost: recycled %d vs closed %d",
			all.RecycledToday, all.ClosedSessions)
	}

	// 租户视角：只看得到自己的数据。租户过滤一旦失效，下面三行一起红。
	mine := call("tenant_admin", "acme")
	if mine.RecycledToday != 1 {
		t.Errorf("tenant acme RecycledToday = %d, want 1 (only s4 closed today)", mine.RecycledToday)
	}
	if mine.TotalTokens != 15 {
		t.Errorf("tenant acme TotalTokens = %d, want 15 (cross-tenant tokens leaked)", mine.TotalTokens)
	}
	if mine.AvgHealthScore != 80 {
		t.Errorf("tenant acme AvgHealthScore = %v, want 80 (cross-tenant rows leaked)", mine.AvgHealthScore)
	}
	if mine.ActiveSessions != 1 || mine.ClosedSessions != 1 {
		t.Errorf("tenant acme status split = active %d / closed %d, want 1 / 1 (cross-tenant rows leaked)",
			mine.ActiveSessions, mine.ClosedSessions)
	}
	// other 自己的视角必须看到它的 2 条今日回收（3 天前那条不含）。
	theirs := call("tenant_admin", "other")
	if theirs.RecycledToday != 1 {
		t.Errorf("tenant other RecycledToday = %d, want 1 (s2 today, s3 three days ago)", theirs.RecycledToday)
	}
}

// TestFirstTurnSamplesSessionsTitleShapeRealDB 钉住 (a) 类缺陷的两态。
//
// 端点 SQL 里表名带 `public.` 前缀，所以这里用一次性隔离 database（见
// newIsolatedDatabase 的注释），业务库 public 全程只读。
//
// 形状分流——按**真库实际的 public.sessions 是否有 title** 决定，这两条断言在
// 456 被登记并应用后会自动切换到「200 侧」，不需要改测试：
//
//   - 真库无 title（＝今天的状态）：端点必须**响亮地失败**（5xx 且正文点名该列），
//     绝不能返回 200 的空壳。半修（例如把查询悄悄改指别处而不同步写侧）会让这条
//     变红，属于预期——那种改法需要显式决策，不该悄悄通过。
//   - 真库有 title（456 登记后的状态）：端点必须 200 且样本字段非空，
//     并仍然按租户隔离（tenant_admin(acme) 看不到 other 的会话）。
func TestFirstTurnSamplesSessionsTitleShapeRealDB(t *testing.T) {
	dsn := admin500DSN(t)
	ctx := context.Background()

	// 读真库形态（只读）。
	root, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open root pool: %v", err)
	}
	defer root.Close()
	var realHasTitle bool
	if err := root.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema='public' AND table_name='sessions' AND column_name='title'
		)`).Scan(&realHasTitle); err != nil {
		t.Fatalf("probe public.sessions: %v", err)
	}
	t.Logf("public.sessions.title present = %v (mirrors sql/migrations/startup/456 registration state)", realHasTitle)

	pool, ctx := newIsolatedDatabase(t, dsn)

	// 列类型逐个照真库抄（session_turns / auto_route_selections_all 的类型见
	// information_schema 实测），避免夹具与生产形态漂移。
	// sessions 的 title 列按真库形态决定是否建——这正是本测试要覆盖的分流。
	sessionsDDL := `
		CREATE TABLE public.sessions (
			session_id text, tenant_id character varying, created_at timestamptz,
			closed_at timestamptz, status text, total_turns integer,
			last_request_summary text, client_type text`
	if realHasTitle {
		sessionsDDL += `, title text`
	}
	sessionsDDL += `);
		CREATE TABLE public.session_turns (
			session_id text, request_id text, tenant_id character varying,
			ts timestamptz, status_code integer, success boolean,
			latency_ms integer, turn_no integer, partition_date date);
		CREATE TABLE public.session_turns_hot (
			session_id text, request_id text, tenant_id character varying,
			ts timestamptz, status_code integer, success boolean,
			latency_ms integer, turn_no integer, partition_date date);
		CREATE TABLE public.auto_route_selections_all (
			id bigint, request_id text, tenant_id character varying, ts timestamptz,
			task_type text, confidence numeric, chosen_model text, partition_date date);
		CREATE TABLE public.training_human_annotations (
			request_id text, human_label text, is_correct boolean,
			annotation_reason text, annotator text, annotated_at timestamptz,
			annotation_metadata jsonb);

		-- 冷面（父表）放一条，_hot 放一条：验证端点真的两面都读。
		INSERT INTO public.session_turns VALUES
			('cold-sess','cold-req','acme', NOW(), 200, true, 11, 1, CURRENT_DATE);
		INSERT INTO public.session_turns_hot VALUES
			('hot-sess','hot-req','acme', NOW(), 200, true, 22, 1, CURRENT_DATE),
			('other-sess','other-req','other', NOW(), 500, false, 33, 1, CURRENT_DATE);

		INSERT INTO public.auto_route_selections_all VALUES
			(1,'cold-req','acme',NOW(),'code',0.90,'m-cold',CURRENT_DATE),
			(2,'hot-req','acme',NOW(),'chat',0.80,'m-hot',CURRENT_DATE),
			(3,'other-req','other',NOW(),'chat',0.70,'m-other',CURRENT_DATE);
	`
	if _, err := pool.Exec(ctx, sessionsDDL); err != nil {
		t.Fatalf("seed isolated database: %v", err)
	}

	// sessions 行按真库形态插入：存在时 title 有非空值（判「非空壳」用），
	// 不存在时该列与该值一起缺席——端点应当响亮失败而不是返回 200 空壳。
	baseCols := "session_id, tenant_id, created_at, status, total_turns, last_request_summary, client_type"
	titleCol, titleVal := "", ""
	if realHasTitle {
		baseCols += ", title"
		// 分隔符只由 titleCol 承担；titleVal 只给值。若两边都带逗号，
		// 拼接后会得到 ".. 'cli-cold', , 'Cold session title'"，
		// 报 SQLSTATE 42601 —— 这正是本判据 2026-10-16 首跑时的失败原因：
		// 列存在的分支此前从未被执行到过（真库当时没有 title 列）。
		titleCol, titleVal = ",", " 'Cold session title'"
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(`
		INSERT INTO public.sessions (%s)
		VALUES
			('cold-sess','acme',NOW(),'closed',7,'cold summary','cli-cold'%[2]s),
			('hot-sess','acme',NOW(),'closed',8,'hot summary','cli-hot'%[2]s),
			('other-sess','other',NOW(),'closed',9,'other summary','cli-other'%[2]s)`,
		baseCols, titleCol+titleVal)); err != nil {
		t.Fatalf("seed sessions: %v", err)
	}

	h := &Handler{db: pool}
	call := func(role, tenant, query string) *httptest.ResponseRecorder {
		t.Helper()
		req := SetAuthContext(
			httptest.NewRequest(http.MethodGet, "/api/admin/annotations/first-turn-samples"+query, nil),
			&AuthContext{Role: role, TenantID: tenant, Username: "probe"},
		)
		w := httptest.NewRecorder()
		h.handleAnnotationFirstTurnSamples(w, req)
		return w
	}

	if !realHasTitle {
		// 今天的状态：列缺失，端点必须响亮失败，且不能返回 200 空壳。
		// 真实 PG 错误是 `column sc.title does not exist` SQLSTATE 42703
		// （实测复现于隔离库，服务端日志里可见）；handler 用
		// writeInternalTextErr 只回泛化文案，不外泄内部细节，所以这里断言
		// 「5xx + 正文不含 schema 细节」而不是断言正文里有列名。
		w := call("super_admin", "acme", "")
		if w.Code == http.StatusOK {
			t.Fatalf("sessions.title missing but endpoint returned 200: body=%s", w.Body.String())
		}
		if w.Code < 500 {
			t.Errorf("want a 5xx for the missing-column failure, got %d body=%s", w.Code, w.Body.String())
		}
		if body := w.Body.String(); strings.Contains(body, "sc.title") || strings.Contains(body, "42703") {
			t.Errorf("error body leaks internal schema detail: %q", body)
		}
		t.Logf("documented blocker reproduced: HTTP %d on missing public.sessions.title (server log shows SQLSTATE 42703)", w.Code)
		return
	}

	// 456 登记后的状态：必须 200 且样本非空。
	w := call("super_admin", "acme", "")
	if w.Code != http.StatusOK {
		t.Fatalf("super_admin HTTP %d body=%s", w.Code, w.Body.String())
	}
	var all FirstTurnSamplesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &all); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if all.Total != 3 {
		t.Errorf("super_admin Total = %d, want 3 (cold + hot + other)", all.Total)
	}
	if len(all.Samples) != 3 {
		t.Fatalf("super_admin samples = %d, want 3", len(all.Samples))
	}
	// 非空壳：每个样本都要有可展示的标题/摘要与轮次。
	for _, s := range all.Samples {
		if s.Title == nil || strings.TrimSpace(*s.Title) == "" {
			t.Errorf("sample %s has empty title", s.RequestID)
		}
		if s.TotalTurns == nil || *s.TotalTurns == 0 {
			t.Errorf("sample %s has empty total_turns", s.RequestID)
		}
		if s.Client == nil || strings.TrimSpace(*s.Client) == "" {
			t.Errorf("sample %s has empty client", s.RequestID)
		}
		if s.ChosenModel == "" || s.TaskType == "" {
			t.Errorf("sample %s missing auto-route projection (%q/%q)", s.RequestID, s.TaskType, s.ChosenModel)
		}
	}

	// 跨租户隔离：acme 看得到冷热两条，看不到 other。
	w = call("tenant_admin", "acme", "")
	if w.Code != http.StatusOK {
		t.Fatalf("tenant acme HTTP %d body=%s", w.Code, w.Body.String())
	}
	var mine FirstTurnSamplesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &mine); err != nil {
		t.Fatalf("decode tenant: %v", err)
	}
	if mine.Total != 2 || len(mine.Samples) != 2 {
		t.Fatalf("tenant acme got %d samples (total %d), want 2", len(mine.Samples), mine.Total)
	}
	for _, s := range mine.Samples {
		if s.SessionID == "other-sess" {
			t.Fatal("cross-tenant session leaked into acme's first-turn samples")
		}
		if s.RequestID == "other-req" {
			t.Fatal("cross-tenant request leaked into acme's first-turn samples")
		}
	}
}
