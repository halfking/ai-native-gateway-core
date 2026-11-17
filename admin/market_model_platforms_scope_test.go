package admin

// market_model_platforms_scope_test.go —— F11 行为门（2026-11-17 复核轮）。
//
// ★ 这个门守的是一个**已在生产露过脸的缺陷**：
//   明细模式的 SQL 带一条 `canonical_name ILIKE '%' || $2 || '%'` 的「同族便利匹配」
//   ⇒ 把**别的模型**的价格当成选中模型的行情展示。
//   实测（211 线上，`model=glm-5.3`）：43 行里只有 17 行是 glm-5.3，
//   26 行是 glm-5.3-flash / zhipu/glm-5.3 / glm-5.3-prime / glm-5.3-fast /
//   甚至带**零宽空格**的 glm-5.3-flash ⇒ 最便宜标记挂到了别的模型的行上。
//
// ★ 为什么同时有「契约断言」和「行为断言」：
//   · 行为断言（pgxmock）证明**路由与参数**按 scope 走对了；
//   · 契约断言证明**默认 SQL 里没有子串匹配**（pgxmock 无法断言「不存在某子句」，
//     但可以断言「存在精确匹配子句」+ 断言常量里没有 ILIKE ⇒ 变异臂能红）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

// TestMarketPlatformQuotesSQLIsExactByDefault —— 默认明细 SQL 必须精确匹配。
func TestMarketPlatformQuotesSQLIsExactByDefault(t *testing.T) {
	if !strings.Contains(marketPlatformQuotesSQL, "lower(canonical_name) = lower($2)") {
		t.Fatalf("默认明细 SQL 缺少精确匹配子句：\n%s", marketPlatformQuotesSQL)
	}
	// ★ 核心守卫：默认路径**不得**含子串/模糊匹配。
	for _, bad := range []string{"ILIKE", "LIKE", "~", "starts_with"} {
		if strings.Contains(marketPlatformQuotesSQL, bad) {
			t.Fatalf("默认明细 SQL 含模糊匹配 %q ⇒ 会把别的模型的价格混进来：\n%s", bad, marketPlatformQuotesSQL)
		}
	}
	// 同族 SQL 必须**显式**存在，且带精确匹配 + 排序（本模型行在前）。
	if !strings.Contains(marketPlatformQuotesFamilySQL, "ILIKE") {
		t.Fatal("同族 SQL 应含 ILIKE（scope=family 的显式入口）")
	}
	if !strings.Contains(marketPlatformQuotesFamilySQL, "(lower(canonical_name) = lower($2)) DESC") {
		t.Fatalf("同族 SQL 必须把本模型行排在前面：\n%s", marketPlatformQuotesFamilySQL)
	}
}

// TestMarketModelPlatformsDefaultScopeIsExact —— 默认（无 scope）走精确匹配 SQL。
func TestMarketModelPlatformsDefaultScopeIsExact(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery(regexp.QuoteMeta("to_regclass('public.market_price_platform_quotes')")).
		WillReturnRows(mock.NewRows([]string{"ok"}).AddRow(true))
	// ★ 期望的查询文本必须含「精确匹配」子句（pgxmock 用正则匹配实际 SQL）。
	mock.ExpectQuery(`lower\(canonical_name\) = lower\(\$2\)`).
		WithArgs("standard", "glm-5.3", 200).
		WillReturnRows(mock.NewRows([]string{
			"canonical_name", "platform_id", "platform_name", "region", "native_currency",
			"is_vendor", "vendor", "currency",
			"input_per_1m", "output_per_1m", "cny_in_per_1m", "cny_out_per_1m",
			"cache_hit_per_1m", "cny_cache_hit_per_1m", "tier", "model_kind",
			"run_date", "source_url",
		}).AddRow(
			"glm-5.3", "openrouter", "OpenRouter", "overseas", "USD",
			false, "", "USD",
			0.0399, 0.0784, 0.2665, 0.5236,
			0.2004, nil, "", "文本/对话",
			"2026-11-17", "https://example.test",
		))

	h := &Handler{marketPlatformDB: mock}
	rr := httptest.NewRecorder()
	h.handleMarketModelPlatforms(rr, httptest.NewRequest(http.MethodGet,
		"/api/admin/market/model-platforms?model=glm-5.3", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Scope string `json:"scope"`
		Items []struct {
			CanonicalName string `json:"canonical_name"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Scope != "" {
		t.Fatalf("默认 scope 应为空（精确匹配），实得 %q", body.Scope)
	}
	if len(body.Items) != 1 || body.Items[0].CanonicalName != "glm-5.3" {
		t.Fatalf("明细行错：%#v", body.Items)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestMarketModelPlatformsFamilyScopeExplicit —— scope=family 走同族 SQL 且回带 scope。
func TestMarketModelPlatformsFamilyScopeExplicit(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery(regexp.QuoteMeta("to_regclass('public.market_price_platform_quotes')")).
		WillReturnRows(mock.NewRows([]string{"ok"}).AddRow(true))
	mock.ExpectQuery(`ILIKE`).
		WithArgs("standard", "glm-5.3", 200).
		WillReturnRows(mock.NewRows([]string{
			"canonical_name", "platform_id", "platform_name", "region", "native_currency",
			"is_vendor", "vendor", "currency",
			"input_per_1m", "output_per_1m", "cny_in_per_1m", "cny_out_per_1m",
			"cache_hit_per_1m", "cny_cache_hit_per_1m", "tier", "model_kind",
			"run_date", "source_url",
		}).AddRow(
			"glm-5.3", "openrouter", "OpenRouter", "overseas", "USD",
			false, "", "USD",
			0.0399, 0.0784, 0.2665, 0.5236,
			0.2004, nil, "", "文本/对话",
			"2026-11-17", "https://example.test",
		).AddRow(
			"glm-5.3-flash", "baidu-qianfan", "百度智能云千帆", "domestic", "CNY",
			false, "", "CNY",
			0.8, 2.8, 0.8, 2.8,
			nil, nil, "", "文本/对话",
			"2026-10-31", "https://example.test",
		))

	h := &Handler{marketPlatformDB: mock}
	rr := httptest.NewRecorder()
	h.handleMarketModelPlatforms(rr, httptest.NewRequest(http.MethodGet,
		"/api/admin/market/model-platforms?model=glm-5.3&scope=family", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Scope string `json:"scope"`
		Items []struct {
			CanonicalName string `json:"canonical_name"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Scope != "family" {
		t.Fatalf("scope 必须回带 family（前端据此显示模型名列），实得 %q", body.Scope)
	}
	if len(body.Items) != 2 {
		t.Fatalf("同族模式应返回 2 行，实得 %d", len(body.Items))
	}
	// ★ 同族行必须**自带 canonical_name** ⇒ 前端才分得清哪行是哪个模型。
	if body.Items[1].CanonicalName != "glm-5.3-flash" {
		t.Fatalf("同族行必须带自己的 canonical_name，实得 %q", body.Items[1].CanonicalName)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestMarketModelPlatformsUnknownScopeFallsBackToExact —— 未知 scope ⇒ 回落精确匹配。
func TestMarketModelPlatformsUnknownScopeFallsBackToExact(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery(regexp.QuoteMeta("to_regclass('public.market_price_platform_quotes')")).
		WillReturnRows(mock.NewRows([]string{"ok"}).AddRow(true))
	mock.ExpectQuery(`lower\(canonical_name\) = lower\(\$2\)`).
		WithArgs("standard", "glm-5.3", 200).
		WillReturnRows(mock.NewRows([]string{
			"canonical_name", "platform_id", "platform_name", "region", "native_currency",
			"is_vendor", "vendor", "currency",
			"input_per_1m", "output_per_1m", "cny_in_per_1m", "cny_out_per_1m",
			"cache_hit_per_1m", "cny_cache_hit_per_1m", "tier", "model_kind",
			"run_date", "source_url",
		}))
	h := &Handler{marketPlatformDB: mock}
	rr := httptest.NewRecorder()
	h.handleMarketModelPlatforms(rr, httptest.NewRequest(http.MethodGet,
		"/api/admin/market/model-platforms?model=glm-5.3&scope=whatever", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Scope string `json:"scope"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Scope != "" {
		t.Fatalf("未知 scope 必须回落精确匹配（scope 回带空），实得 %q", body.Scope)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
