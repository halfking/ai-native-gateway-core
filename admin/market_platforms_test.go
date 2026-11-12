package admin

// market_platforms_test.go — M1-T3 handler 测试（pgxmock 全查询路径）。
//
// 覆盖面：
//	① GET 清单：行扫描 + SQL 形状 + 空表 = items:[]（不是 null）；
//	② PUT happy path：部分更新语义（只提交的列变）+ RETURNING 落库时刻；
//	③ PUT 校验（与 958 CHECK 同形的前置闸）：region/currency 取值域、
//	   配对约束、**只提交一半时按库内现值配对**；
//	④ PUT 404（平台不存在）；
//	⑤ 审计载荷形状（before/after 全行）+ 审计行登记。
//
// ★ 这些测试同时是 api_handler_coverage 门的「有覆盖」证据：两个 handler
// 的函数体都被真实执行 ⇒ 不进零覆盖台账。
//
// ★ pgxmock 默认 QueryMatcherRegexp = 非锚定的子串匹配 ⇒ 期望串用
// regexp.QuoteMeta 后按「SQL 里的独特片段」写即可（不必逐字全文）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	pgxmock "github.com/pashagolub/pgxmock/v4"
)

const marketPlatformsListSQLFragment = "FROM public.market_platform_registry"
const marketPlatformSelectFragment = "SELECT platform_id, platform_name, region, native_currency"
const marketPlatformUpdateFragment = "UPDATE public.market_platform_registry"
const marketPlatformAuditFragment = "INSERT INTO routing_audit_log"

func marketPlatformMockRows(mock pgxmock.PgxPoolIface) *pgxmock.Rows {
	return mock.NewRows([]string{
		"platform_id", "platform_name", "region", "native_currency",
		"is_ours", "enabled", "notes", "updated_at",
	})
}

// routeMarketPlatformPut 让请求走**真实的 ServeMux 路由**（与 handler.go 里
// 的注册形态逐字同款）——直接调 handler 拿不到 PathValue，且注册形态
// （方法前缀 + {platformId} 通配）本身也该被钉住。
func routeMarketPlatformPut(t *testing.T, h *Handler, rr *httptest.ResponseRecorder, req *http.Request) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/admin/market/platforms/{platformId}", h.handleMarketPlatformUpdate)
	mux.ServeHTTP(rr, req)
}
func TestMarketPlatformsListScansRows(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()
	h := &Handler{marketPlatformDB: mock}

	updatedAt := time.Date(2026, 11, 13, 3, 10, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta(marketPlatformsListSQLFragment)).
		WillReturnRows(marketPlatformMockRows(mock).
			AddRow("anthropic", "Anthropic", "overseas", "USD", false, true, "", updatedAt).
			AddRow("zhipu", "智谱AI", "domestic", "CNY", true, true, "", updatedAt))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/market/platforms", nil)
	h.handleMarketPlatformsList(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("GET 应 200，得到 %d：%s", rr.Code, rr.Body.String())
	}
	var out struct {
		Items []marketPlatformRow `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是合法 JSON：%v\n%s", err, rr.Body.String())
	}
	if len(out.Items) != 2 {
		t.Fatalf("应 2 行，得到 %d", len(out.Items))
	}
	first := out.Items[0]
	if first.PlatformID != "anthropic" || first.Region != "overseas" ||
		first.NativeCurrency != "USD" || !first.Enabled {
		t.Fatalf("第一行形状不对：%+v", first)
	}
	if first.UpdatedAt != "2026-11-13T03:10:00Z" {
		t.Fatalf("updated_at 应 RFC3339 UTC，得到 %q", first.UpdatedAt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL 期望未满足：%v", err)
	}
}

func TestMarketPlatformsListEmptyTableIsItemsEmptyArray(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()
	h := &Handler{marketPlatformDB: mock}

	mock.ExpectQuery(regexp.QuoteMeta(marketPlatformsListSQLFragment)).
		WillReturnRows(marketPlatformMockRows(mock))

	rr := httptest.NewRecorder()
	h.handleMarketPlatformsList(rr, httptest.NewRequest(http.MethodGet, "/api/admin/market/platforms", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET 应 200，得到 %d", rr.Code)
	}
	// 空表必须序列化成 [] 而不是 null（前端 empty 判定按长度，null 会炸 .length）。
	if !strings.Contains(rr.Body.String(), `"items":[]`) {
		t.Fatalf("空表应返回 items:[]，得到：%s", rr.Body.String())
	}
}

func TestMarketPlatformsListNilPoolIs503(t *testing.T) {
	h := &Handler{}
	rr := httptest.NewRecorder()
	h.handleMarketPlatformsList(rr, httptest.NewRequest(http.MethodGet, "/api/admin/market/platforms", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil pool 应 503，得到 %d", rr.Code)
	}
}

func TestMarketPlatformsListRejectsNonGET(t *testing.T) {
	h := &Handler{}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rr := httptest.NewRecorder()
		h.handleMarketPlatformsList(rr, httptest.NewRequest(m, "/api/admin/market/platforms", nil))
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s 应 405，得到 %d", m, rr.Code)
		}
	}
}

func TestMarketPlatformUpdatePartialFields(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()
	h := &Handler{marketPlatformDB: mock}

	before := time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)
	after := time.Date(2026, 11, 13, 3, 10, 0, 0, time.UTC)

	// ① SELECT 现值（审计 before + 配对基准）。
	mock.ExpectQuery(regexp.QuoteMeta(marketPlatformSelectFragment)).
		WithArgs("zhipu").
		WillReturnRows(marketPlatformMockRows(mock).
			AddRow("zhipu", "智谱AI", "domestic", "CNY", false, true, "", before))

	// ② UPDATE：只提交 is_ours ⇒ 其余列按现值写回（不变）。
	mock.ExpectQuery(regexp.QuoteMeta(marketPlatformUpdateFragment)).
		WithArgs("zhipu", "智谱AI", "domestic", "CNY", true, true, "").
		WillReturnRows(mock.NewRows([]string{"updated_at"}).AddRow(after))

	// ③ 审计行（best-effort，但登记必须发生）。
	mock.ExpectExec(regexp.QuoteMeta(marketPlatformAuditFragment)).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/admin/market/platforms/zhipu",
		strings.NewReader(`{"is_ours": true}`))
	routeMarketPlatformPut(t, h, rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("PUT 应 200，得到 %d：%s", rr.Code, rr.Body.String())
	}
	var out struct {
		Item marketPlatformRow `json:"item"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	if !out.Item.IsOurs {
		t.Fatalf("is_ours 应为 true：%+v", out.Item)
	}
	if out.Item.Region != "domestic" || out.Item.NativeCurrency != "CNY" || out.Item.PlatformName != "智谱AI" {
		t.Fatalf("未提交字段必须保持现值：%+v", out.Item)
	}
	if out.Item.UpdatedAt != "2026-11-13T03:10:00Z" {
		t.Fatalf("updated_at 应来自 RETURNING，得到 %q", out.Item.UpdatedAt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL 期望未满足：%v", err)
	}
}

func TestMarketPlatformUpdateAuditPayloadShape(t *testing.T) {
	before := marketPlatformRow{PlatformID: "openai", PlatformName: "OpenAI",
		Region: "overseas", NativeCurrency: "USD", IsOurs: false, Enabled: true,
		UpdatedAt: "2026-11-01T00:00:00Z"}
	after := before
	after.IsOurs = true

	payload := marketPlatformAuditPayload(before, after)
	var shape map[string]marketPlatformRow
	if err := json.Unmarshal(payload, &shape); err != nil {
		t.Fatalf("audit payload 不是 before/after 形状：%v", err)
	}
	b, ok := shape["before"]
	if !ok || b.IsOurs {
		t.Fatalf("before 缺失或布尔不对：%s", payload)
	}
	a, ok := shape["after"]
	if !ok || !a.IsOurs {
		t.Fatalf("after 缺失或布尔不对：%s", payload)
	}
	if a.PlatformID != "openai" {
		t.Fatalf("after 缺平台标识：%s", payload)
	}
}

func TestMarketPlatformUpdateValidationFailures(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		existing marketPlatformRow
		wantSub  string
	}{
		{
			name:     "region 越域",
			body:     `{"region": "mars"}`,
			existing: marketPlatformRow{Region: "domestic", NativeCurrency: "CNY"},
			wantSub:  "overseas|domestic",
		},
		{
			name:     "currency 越域",
			body:     `{"native_currency": "EUR"}`,
			existing: marketPlatformRow{Region: "domestic", NativeCurrency: "CNY"},
			wantSub:  "USD|CNY",
		},
		{
			name:     "配对被拆",
			body:     `{"region": "overseas"}`,
			existing: marketPlatformRow{Region: "domestic", NativeCurrency: "CNY"},
			wantSub:  "配对",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatalf("pgxmock.NewPool: %v", err)
			}
			defer mock.Close()
			h := &Handler{marketPlatformDB: mock}

			mock.ExpectQuery(regexp.QuoteMeta(marketPlatformSelectFragment)).
				WithArgs("zhipu").
				WillReturnRows(marketPlatformMockRows(mock).
					AddRow("zhipu", "智谱AI", tc.existing.Region, tc.existing.NativeCurrency,
						false, true, "", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)))
			// 校验失败 ⇒ 不应有 UPDATE（pgxmock 对未期望的查询直接报错）。

			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPut, "/api/admin/market/platforms/zhipu",
				strings.NewReader(tc.body))
			routeMarketPlatformPut(t, h, rr, req)

			if rr.Code != http.StatusBadRequest {
				t.Fatalf("应 400，得到 %d：%s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), tc.wantSub) {
				t.Fatalf("错误信息应含 %q：%s", tc.wantSub, rr.Body.String())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("校验失败路径不应有额外 SQL：%v", err)
			}
		})
	}
}

func TestMarketPlatformUpdateHalfPairAgainstExistingRow(t *testing.T) {
	// 只提交 notes ⇒ region/currency 用库内现值 overseas/USD ⇒ 配对成立 ⇒ 放行。
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()
	h := &Handler{marketPlatformDB: mock}

	before := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta(marketPlatformSelectFragment)).
		WithArgs("openai").
		WillReturnRows(marketPlatformMockRows(mock).
			AddRow("openai", "OpenAI", "overseas", "USD", false, true, "", before))
	mock.ExpectQuery(regexp.QuoteMeta(marketPlatformUpdateFragment)).
		WithArgs("openai", "OpenAI", "overseas", "USD", false, true, "对拍过").
		WillReturnRows(mock.NewRows([]string{"updated_at"}).AddRow(before))
	mock.ExpectExec(regexp.QuoteMeta(marketPlatformAuditFragment)).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/admin/market/platforms/openai",
		strings.NewReader(`{"notes": "对拍过"}`))
	routeMarketPlatformPut(t, h, rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("半提交合法配对应 200，得到 %d：%s", rr.Code, rr.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL 期望未满足：%v", err)
	}
}

func TestMarketPlatformUpdateNotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()
	h := &Handler{marketPlatformDB: mock}

	mock.ExpectQuery(regexp.QuoteMeta(marketPlatformSelectFragment)).
		WithArgs("nope").
		WillReturnError(pgx.ErrNoRows)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/admin/market/platforms/nope",
		strings.NewReader(`{"is_ours": true}`))
	routeMarketPlatformPut(t, h, rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("未知平台应 404，得到 %d：%s", rr.Code, rr.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL 期望未满足：%v", err)
	}
}

func TestMarketRegistryValidatePairTable(t *testing.T) {
	valid := [][2]string{
		{"overseas", "USD"}, {"domestic", "CNY"},
	}
	for _, p := range valid {
		if err := marketRegistryValidatePair(p[0], p[1]); err != nil {
			t.Fatalf("合法配对 %v 不应报错：%v", p, err)
		}
	}
	invalid := [][2]string{
		{"overseas", "CNY"}, {"domestic", "USD"},
		{"mars", "USD"}, {"overseas", "EUR"}, {"", ""}, {"overseas", ""},
	}
	for _, p := range invalid {
		if err := marketRegistryValidatePair(p[0], p[1]); err == nil {
			t.Fatalf("非法配对 %v 应报错", p)
		}
	}
}
