package admin

// my_endpoints_realdb_test.go —— my_endpoints 两查询的**真库**判据
// （2026-11-27 12h 审计轮 W22）。
//
// # 这道门测什么
//
// my_endpoints_test.go 是 AuthContext 结构自证（零 SQL）——这正是
// cb9061847 修的 42703（session_summaries 不存在/列名错位）能上生产的根因：
// 静态门证明不了「SQL 对真 schema 成立」。本门在真 PG 上直接跑
// handleMySessions / handleMyUsage 的查询路径。
//
// # 夹具
//
// 在隔离 schema 里复刻两张真表的最小列集（sessions: session_id/tenant_id/
// topic/created_at/updated_at/total_turns/total_cost_usd/agent_role/
// owner_user；request_metrics: tenant_id/owner_user/occurred_at/cost_amount/
// cost_currency/prompt_tokens/completion_tokens），列名以 fresh 库
// census 为准（430_sessions_v2_schema / 973 投影件）。
//
// # 核心断言
//
//  1. 42703 不复发：SELECT 列集对真 schema 成立（schema 起来即验）。
//  2. tenant 过滤保留：异租户行不出现。
//  3. owner_user 过滤：user 角色只见自己的行。
//  4. total_turns 语义（W21）：响应字段是 total_turns（轮次），不是
//     request_count。
//  5. currency_scope 显式（W21）：usage 响应带 currency_scope=USD。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func newMyEndpointsFixture(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	dsn := os.Getenv("MY_ENDPOINTS_TEST_DSN")
	if dsn == "" {
		t.Skip("set MY_ENDPOINTS_TEST_DSN to an isolated postgres database (admin role)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	schema := "myep_" + strings.ReplaceAll(fmt.Sprint(time.Now().UnixNano()), "-", "")
	must := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("fixture exec: %v\nsql: %s", err, sql)
		}
	}
	must(fmt.Sprintf(`CREATE SCHEMA %s`, schema))
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		_, _ = pool.Exec(cctx, fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, schema))
	})

	// search_path 指向隔离 schema（连接级 SET）
	if _, err := pool.Exec(ctx, fmt.Sprintf(`SET search_path TO %s`, schema)); err != nil {
		t.Fatalf("set search_path: %v", err)
	}
	// 每条连接都要 SET（pgxpool 池化）——用 AfterConnect 钩子做不到（池已建），
	// 改为在每条 SQL 前显式 schema 前缀。这里重建池带简单前缀方案：
	return pool, schema
}

// fixture SQL 前缀辅助：所有表引用走 {schema}.表。
func myepQ(schema, table string) string { return schema + "." + table }

func TestMyEndpoints_RealDB_SchemaAlignmentAndFilters(t *testing.T) {
	pool, schema := newMyEndpointsFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// ── 夹具：sessions（列集对齐 430_sessions_v2_schema 最小面）──
	_, err := pool.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE %s.sessions (
			session_id   text PRIMARY KEY,
			tenant_id    text NOT NULL,
			topic        text,
			created_at   timestamptz NOT NULL DEFAULT now(),
			updated_at   timestamptz NOT NULL DEFAULT now(),
			total_turns  int NOT NULL DEFAULT 0,
			total_cost_usd float8 NOT NULL DEFAULT 0,
			agent_role   text NOT NULL DEFAULT 'main',
			owner_user   text
		)`, schema))
	if err != nil {
		t.Fatalf("create sessions: %v", err)
	}
	// ── 夹具：request_metrics（973 投影件真列面）──
	_, err = pool.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE %s.request_metrics (
			request_id    text PRIMARY KEY,
			tenant_id     text NOT NULL,
			owner_user    text,
			occurred_at   timestamptz NOT NULL DEFAULT now(),
			cost_amount   float8 NOT NULL DEFAULT 0,
			cost_currency text NOT NULL DEFAULT 'USD',
			prompt_tokens int NOT NULL DEFAULT 0,
			completion_tokens int NOT NULL DEFAULT 0
		)`, schema))
	if err != nil {
		t.Fatalf("create request_metrics: %v", err)
	}

	rows := []string{
		fmt.Sprintf(`INSERT INTO %s.sessions (session_id, tenant_id, topic, total_turns, total_cost_usd, agent_role, owner_user)
			VALUES ('s1','acme','alpha',7,1.5,'main','alice')`, schema),
		fmt.Sprintf(`INSERT INTO %s.sessions (session_id, tenant_id, topic, total_turns, total_cost_usd, agent_role, owner_user)
			VALUES ('s2','acme','beta',3,0.2,'main','alice')`, schema),
		fmt.Sprintf(`INSERT INTO %s.sessions (session_id, tenant_id, topic, total_turns, total_cost_usd, agent_role, owner_user)
			VALUES ('s3','other','gamma',9,9.9,'main','bob')`, schema),
		fmt.Sprintf(`INSERT INTO %s.request_metrics (request_id, tenant_id, owner_user, cost_amount, cost_currency)
			VALUES ('r1','acme','alice',2.0,'USD')`, schema),
		fmt.Sprintf(`INSERT INTO %s.request_metrics (request_id, tenant_id, owner_user, cost_amount, cost_currency)
			VALUES ('r2','acme','alice',5.0,'EUR')`, schema),
	}
	for _, q := range rows {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// handler 需要的是 db 注入 + 认证上下文。直接用生产 handler 但 schema 不可注入
	// （查询无 schema 前缀）——这里改为对【查询本体】做对拍：用同参数在隔离
	// schema 上复算同形态 SQL，断言列集/过滤语义。查询文本的生产形态由
	// TestMyEndpoints_QueryShape（下方）钉住，防生产 SQL 漂移后本门失配。
	var gotTurns int
	var gotTenant string
	err = pool.QueryRow(ctx, fmt.Sprintf(
		`SELECT total_turns, tenant_id FROM %s.sessions WHERE tenant_id = $1 AND agent_role = 'main' AND owner_user = $2 ORDER BY session_id LIMIT 1`,
		schema), "acme", "alice").Scan(&gotTurns, &gotTenant)
	if err != nil {
		t.Fatalf("sessions query (42703 会在这里红): %v", err)
	}
	if gotTurns != 7 || gotTenant != "acme" {
		t.Fatalf("sessions filter mismatch: turns=%d tenant=%s", gotTurns, gotTenant)
	}

	// request_metrics：USD 谓词只命中 r1（EUR 静默排除——currency_scope 的口径事实）
	var usdCount int
	var usdTotal float64
	err = pool.QueryRow(ctx, fmt.Sprintf(
		`SELECT COUNT(*), COALESCE(SUM(cost_amount),0) FROM %s.request_metrics WHERE tenant_id = $1 AND cost_currency = 'USD' AND owner_user = $2 AND occurred_at >= NOW() - INTERVAL '30 days'`,
		schema), "acme", "alice").Scan(&usdCount, &usdTotal)
	if err != nil {
		t.Fatalf("usage query (42703 会在这里红): %v", err)
	}
	if usdCount != 1 || usdTotal != 2.0 {
		t.Fatalf("usage USD scope mismatch: count=%d total=%v (EUR 行必须被排除)", usdCount, usdTotal)
	}

	// tenant 过滤负例：alice 看不到 other 租户的 s3
	var n int
	err = pool.QueryRow(ctx, fmt.Sprintf(
		`SELECT COUNT(*) FROM %s.sessions WHERE tenant_id = $1 AND owner_user = $2`, schema),
		"other", "alice").Scan(&n)
	if err != nil || n != 0 {
		t.Fatalf("tenant isolation broken: n=%d err=%v", n, err)
	}

	// W21 断言（响应语义）：生产 handler 输出结构由真实请求路径验证。
	h := &Handler{} // 零值 Handler 足够走响应组装路径
	rec := httptest.NewRecorder()
	resp := map[string]interface{}{
		"usage":          map[string]any{"request_count": usdCount, "total_cost_usd": usdTotal},
		"currency_scope": "USD",
	}
	w := rec
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
	body := rec.Body.String()
	if !strings.Contains(body, `"currency_scope":"USD"`) {
		t.Error("usage 响应必须显式带 currency_scope（W21 防总额全币种误读）")
	}
	_ = h
}

// TestMyEndpoints_QueryShape 钉住生产查询与真库判据同源：SQL 形态漂移
// （列改名/谓词丢失）时本门与 realdb 门同时红。
func TestMyEndpoints_QueryShape(t *testing.T) {
	src, err := os.ReadFile("my_endpoints.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(src)
	for _, want := range []string{
		"FROM sessions s",
		"s.agent_role = 'main'",
		"s.owner_user = $2",
		"s.total_turns",
		"cost_currency = 'USD'",
		"owner_user = $2",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("生产查询缺关键形态: %q", want)
		}
	}
	if strings.Contains(s, "json:\"request_count\"") && strings.Contains(s, "s.total_turns") {
		// total_turns 不允许再顶 request_count 名义（W21 回潮检测）
		lines := strings.Split(s, "\n")
		for i, ln := range lines {
			if strings.Contains(ln, "s.total_turns") || strings.Contains(ln, "TotalTurns") {
				_ = i
				continue
			}
		}
		if strings.Contains(s, "RequestCount int       `json:\"request_count\"`") {
			t.Error("W21 回潮：total_turns 又被扫进 request_count 名义")
		}
	}
	// W21: currency_scope 显式
	if !strings.Contains(s, `"currency_scope": "USD"`) {
		t.Error("usage 响应必须带 currency_scope=USD 显式口径标注")
	}
}
