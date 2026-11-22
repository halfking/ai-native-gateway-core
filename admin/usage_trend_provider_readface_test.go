package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	dbpkg "github.com/kaixuan/llm-gateway-go/db"
)

// ─── 背景（2026-11-23，provider 过滤读面整体退化） ───
//
// 用量趋势页按供应商过滤时，读面原为 request_stats_minute（自带
// provider_id × canonical_id）JOIN provider_models 取展示名。该读面在生产
// **已整体退化**：
//
//	211 真库实测：请求日志近 3 天按 provider 分组，canonical_id 非空行数
//	**恒为 0**（provider 14 / 587 / 18 / 32 … 无一例外）⇒ rollup 写面
//	COALESCE(r.canonical_id, 0) ⇒ request_stats_minute.canonical_id **恒为 0**
//	⇒ provider_models 的 canonical_id 映射永远 JOIN 不上 ⇒ 兜底标签
//	'model#' || canonical_id ⇒ **所有行塌成一行 `model#0`**。
//
// 即：那一档不是「更快的同一答案」，是「更快的错误答案」——不报错、不空表，
// 只是给出一份看起来正常的错答案。修法＝provider 过滤并入明细档（模型名直接
// 来自 outbound_model/client_model），并顺带让图表与同屏排行卡片口径一致。

// usageTrendSourceCode 剥掉 // 注释行后的源码文本。
//
// ★ 断言必须**先去注释**：本文件的修复说明、以及 usage_trend_series.go 的文件头
// 注释里都**故意引用了旧缺陷写法**（"FROM request_stats_minute" / "'model#'"）
// ——那是说明文字，不去注释就会把「文档写清了缺陷」误判成「缺陷复活」。
func usageTrendSourceCode(t *testing.T, file string) string {
	t.Helper()
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	var codeLines []string
	for _, line := range strings.Split(string(src), "\n") {
		if !strings.Contains(line, "//") {
			codeLines = append(codeLines, line)
		}
	}
	return strings.Join(codeLines, "\n")
}

// TestUsageTrendProviderReadFaceNotMinuteTier —— 源码级守卫：已退化的 provider
// 汇总档不得复活（它一复活，供应商过滤就又只剩一行 `model#0`）。
func TestUsageTrendProviderReadFaceNotMinuteTier(t *testing.T) {
	code := usageTrendSourceCode(t, "usage_trend_series.go")
	for _, marker := range []string{
		"FROM request_stats_minute",
		"providerModelsNameExpr",
		"providerModelsNameJoin",
		"'model#'",
	} {
		if strings.Contains(code, marker) {
			t.Fatalf("usage_trend_series.go must not use the degraded provider read face (%q) — "+
				"request_stats_minute carries no model name and its canonical_id is 0 in production "+
				"(every provider collapses into one 'model#0' series)", marker)
		}
	}
}

// TestUsageTrendProviderUsesDetailReadFace —— 分档断言：provider / api_key
// 过滤都走明细档；无过滤仍走 dim 汇总档（那条路在生产是好的）。
func TestUsageTrendProviderUsesDetailReadFace(t *testing.T) {
	cases := []struct {
		name string
		f    usageTrendFilters
		want string
	}{
		{"none", usageTrendFilters{}, "request_stats_dim_minute"},
		{"tenant-only", usageTrendFilters{tenantID: "t1"}, "request_stats_dim_minute"},
		{"provider", usageTrendFilters{providerID: 3}, "request_logs_with_current_month"},
		{"provider+tenant", usageTrendFilters{providerID: 3, tenantID: "t1"}, "request_logs_with_current_month"},
		{"provider+model", usageTrendFilters{providerID: 3, models: []string{"m"}}, "request_logs_with_current_month"},
		{"apikey", usageTrendFilters{apiKeyID: 7}, "request_logs_with_current_month"},
	}
	for _, c := range cases {
		if got := usageTrendSource(c.f); got != c.want {
			t.Errorf("%s: usageTrendSource = %s, want %s", c.name, got, c.want)
		}
	}
}

// usageTrendPickRealProvider 选一个「近 7 天有 2..20 个模型」的供应商
// （模型数落在 top 上限内 ⇒ 不会被折叠成 __others__ ⇒ 断言可逐项对齐）。
func usageTrendPickRealProvider(t *testing.T, db *pgxpool.Pool, start, end time.Time) (int64, int, int64) {
	t.Helper()
	var pid int64
	var models int
	var reqs int64
	err := db.QueryRow(context.Background(), `
		SELECT r.provider_id,
			COUNT(DISTINCT COALESCE(NULLIF(r.outbound_model, ''), NULLIF(r.client_model, ''), '__unknown__')),
			COUNT(*)
		FROM `+dbpkg.SessionFamilyTurnsSourceSQL()+` r
		WHERE r.request_status IN ('success', 'failure', 'rate_limited')
		  AND r.ts >= $1 AND r.ts < $2
		  AND r.provider_id IS NOT NULL
		GROUP BY r.provider_id
		HAVING COUNT(DISTINCT COALESCE(NULLIF(r.outbound_model, ''), NULLIF(r.client_model, ''), '__unknown__')) BETWEEN 2 AND 20
		ORDER BY COUNT(*) DESC
		LIMIT 1
	`, start, end).Scan(&pid, &models, &reqs)
	if err != nil {
		t.Fatalf("pick provider with 2..20 models: %v", err)
	}
	return pid, models, reqs
}

// TestUsageTrendProviderSeriesRealDB —— 真库口径钉测（需 TEST_DATABASE_URL）：
//
//	① 供应商过滤返回的序列**不得**出现 `model#N` 哨兵标签；
//	② 模型数必须等于该供应商在同窗口的真实模型数（不能塌成一行）；
//	③ 序列总请求数必须等于该供应商在同窗口的真实请求数（口径一致，不是子集）。
//
// ③ 是关键：只断言「有多个模型」会被「少算了请求」蒙混过关。
// 参考计数与端点**同源**（同一 session 族 SQL、同一时间窗、同一状态集）⇒
// 数字必须逐项相等，而不是「差不多」。
func TestUsageTrendProviderSeriesRealDB(t *testing.T) {
	db := testDB(t)
	if db == nil {
		t.Skip("database not available (TEST_DATABASE_URL not set)")
	}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/usage/trend-series?days=7&top=20", nil)
	tr, err := boardTimeRangeFromRequest(req)
	if err != nil {
		t.Fatalf("resolve time range: %v", err)
	}
	providerID, realModels, realRequests := usageTrendPickRealProvider(t, db, tr.Start, tr.End)

	// 参考计数：与端点同源（同一 SQL 形状、同一窗口、同一状态集）。
	var refModels int
	var refRequests int64
	if err := db.QueryRow(context.Background(), `
		SELECT COUNT(DISTINCT `+usageTrendModelExpr+`), COUNT(*)
		FROM `+dbpkg.SessionFamilyTurnsSourceSQL()+` r
		WHERE r.request_status IN ('success', 'failure', 'rate_limited')
		  AND r.ts >= $1 AND r.ts < $2 AND r.provider_id = $3
	`, tr.Start, tr.End, providerID).Scan(&refModels, &refRequests); err != nil {
		t.Fatalf("reference count: %v", err)
	}
	if refModels != realModels || refRequests != realRequests {
		t.Fatalf("reference mismatch (models %d vs %d, requests %d vs %d)", refModels, realModels, refRequests, realRequests)
	}

	h := &Handler{db: db}
	seriesReq := httptest.NewRequest(http.MethodGet,
		"/api/admin/usage/trend-series?provider_id="+strconv.FormatInt(providerID, 10)+"&days=7&top=20", nil)
	w := httptest.NewRecorder()
	h.usageTrendSeries(w, seriesReq)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var resp usageTrendSeriesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Source != "request_logs_with_current_month" {
		t.Fatalf("provider filter must use the detail read face, got source=%s", resp.Source)
	}
	distinct := map[string]bool{}
	var total int64
	for _, s := range resp.Series {
		if strings.HasPrefix(s.Model, "model#") {
			t.Fatalf("series label %q is the degraded sentinel — provider read face still collapses", s.Model)
		}
		distinct[s.Model] = true
		total += s.TotalRequests
	}
	if len(distinct) != realModels {
		t.Fatalf("provider %d: series has %d distinct models, real data has %d", providerID, len(distinct), realModels)
	}
	if total != realRequests {
		t.Fatalf("provider %d: series total requests %d != real %d (口径不一致)", providerID, total, realRequests)
	}
	t.Logf("provider %d: %d models / %d requests / series=%d", providerID, realModels, realRequests, len(resp.Series))
}

// TestUsageTrendProviderSeriesLatency —— 真库耗时基线（需 TEST_DATABASE_URL）：
// 供应商过滤走明细档后必须仍能在页面可接受的时间内返回（端点 ctx 45s，
// 前端 15s 起提示「缩短时间范围」）。上界取 15s。
func TestUsageTrendProviderSeriesLatency(t *testing.T) {
	db := testDB(t)
	if db == nil {
		t.Skip("database not available (TEST_DATABASE_URL not set)")
	}
	tr, err := boardTimeRangeFromRequest(httptest.NewRequest(http.MethodGet, "/api/admin/usage/trend-series?days=7", nil))
	if err != nil {
		t.Fatalf("resolve time range: %v", err)
	}
	providerID, _, _ := usageTrendPickRealProvider(t, db, tr.Start, tr.End)

	h := &Handler{db: db}
	seriesReq := httptest.NewRequest(http.MethodGet,
		"/api/admin/usage/trend-series?provider_id="+strconv.FormatInt(providerID, 10)+"&days=7&top=20", nil)
	w := httptest.NewRecorder()
	start := time.Now()
	h.usageTrendSeries(w, seriesReq)
	elapsed := time.Since(start)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	t.Logf("provider trend-series (7d, provider=%d) took %s", providerID, elapsed)
	if elapsed > 15*time.Second {
		t.Fatalf("provider trend-series took %s — too slow for the page (45s ctx, frontend warns at 15s)", elapsed)
	}
}

// TestUsageTrendProviderDetailSQLHasProviderPredicate —— 源码级钉：明细档的
// provider 过滤谓词必须在（漏了它就是「按供应商过滤但没过滤」）。
func TestUsageTrendProviderDetailSQLHasProviderPredicate(t *testing.T) {
	code := usageTrendSourceCode(t, "usage_trend_series.go")
	if !strings.Contains(code, "r.provider_id = $") {
		t.Fatal("detail read face must push the provider filter down (r.provider_id = $N)")
	}
	if !strings.Contains(code, "r.api_key_id = $") {
		t.Fatal("detail read face must push the api_key filter down (r.api_key_id = $N)")
	}
}

// ─── 明细档 SQL 形状守卫（钉「format 参数错位」这一类缺陷） ───
//
// 病根：明细档 SQL 由 4 类片段拼成（模型口径 / 时间桶 / credits / FROM 源），
// 之前用 fmt 的**位置参数**对齐 ⇒ 错位一位后 `FROM` 子句变成 credits 表达式
// ⇒ 42P01「missing FROM-clause entry for table r」⇒ 端点**静默降级成空序列**
// ⇒ 页面显示「这段时间没有用量」，而真相是「查询本身写错了」。
// ★ 通用式：**降级分支会吃掉真错误** ⇒ SQL 形状必须有源码级钉测，否则
// 「静默空」会被当成「没数据」。

func TestUsageTrendDetailSeriesSQLShape(t *testing.T) {
	tr := boardTimeRange{
		Start: time.Date(2026, 11, 16, 16, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 11, 23, 16, 0, 0, 0, time.UTC),
	}
	f := usageTrendFilters{providerID: 587}
	sql, args := usageTrendDetailSeriesSQL(tr, f)

	// FROM 必须是会话族源，且带别名 r。
	wantFrom := "FROM " + dbpkg.SessionFamilyTurnsSourceSQL() + " r"
	if !strings.Contains(sql, wantFrom) {
		t.Fatalf("detail series SQL must read FROM the session-family source with alias r\nwant substring: %s\ngot:\n%s", wantFrom, sql)
	}
	// SELECT 的第一个表达式必须是模型口径（不是 FROM 源、不是 credits）。
	if !strings.Contains(sql, "SELECT "+usageTrendModelExpr+" AS model") {
		t.Fatalf("detail series SQL must select the model expression first\n got:\n%s", sql)
	}
	// credits 只能出现在 SUM() 里，绝不能落进 FROM（错位时的症状：
	// 外层 FROM 变成 `FROM COALESCE(r.credits_charged, CASE …) r`）。
	if !strings.Contains(sql, "FROM (SELECT") {
		t.Fatalf("outer FROM must be the session-family source (starts with '('), not another expression\n got:\n%s", sql)
	}
	if n := strings.Count(sql, "FROM maas_settings ms"); n != 1 {
		t.Fatalf("credits inner FROM count = %d, want 1 (credits expression misplaced)", n)
	}
	// 参数个数与占位符一致：$1/$2 窗口 + $3 provider。
	if len(args) != 3 {
		t.Fatalf("detail series args = %d, want 3 (start, end, provider)", len(args))
	}
	if !strings.Contains(sql, "r.provider_id = $3") {
		t.Fatalf("provider predicate must be pushed down with the right placeholder\n got:\n%s", sql)
	}
}

func TestUsageTrendDetailModelsSQLShape(t *testing.T) {
	tr := boardTimeRange{
		Start: time.Date(2026, 11, 16, 16, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 11, 23, 16, 0, 0, 0, time.UTC),
	}
	f := usageTrendFilters{providerID: 587}
	sql, args := usageTrendDetailModelsSQL(tr, f)

	wantFrom := "FROM " + dbpkg.SessionFamilyTurnsSourceSQL() + " r"
	if !strings.Contains(sql, wantFrom) {
		t.Fatalf("detail models SQL must read FROM the session-family source with alias r\n got:\n%s", sql)
	}
	if !strings.Contains(sql, "SELECT "+usageTrendModelExpr+",") {
		t.Fatalf("detail models SQL must select the model expression first\n got:\n%s", sql)
	}
	if !strings.Contains(sql, "FROM (SELECT") {
		t.Fatalf("outer FROM must be the session-family source (starts with '('), not another expression\n got:\n%s", sql)
	}
	if n := strings.Count(sql, "FROM maas_settings ms"); n != 1 {
		t.Fatalf("credits inner FROM count = %d, want 1 (credits expression misplaced)", n)
	}
	if len(args) != 3 {
		t.Fatalf("detail models args = %d, want 3", len(args))
	}
}
