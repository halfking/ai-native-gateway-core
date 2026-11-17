package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func TestMarketModelPlatformsTableMissing(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery(regexp.QuoteMeta("to_regclass('public.market_price_platform_quotes')")).
		WillReturnRows(mock.NewRows([]string{"ok"}).AddRow(false))

	h := &Handler{marketPlatformDB: mock}
	rr := httptest.NewRecorder()
	h.handleMarketModelPlatforms(rr, httptest.NewRequest(http.MethodGet, "/api/admin/market/model-platforms?model=glm", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["available"] != false {
		t.Fatalf("want available:false, got %#v", body)
	}
}

func TestMarketModelPlatformsScansNullPrices(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery(regexp.QuoteMeta("to_regclass('public.market_price_platform_quotes')")).
		WillReturnRows(mock.NewRows([]string{"ok"}).AddRow(true))
	mock.ExpectQuery(`FROM market_price_platform_quotes`).
		WithArgs("standard", "glm-4", 200).
		WillReturnRows(
			mock.NewRows([]string{
				"canonical_name", "platform_id", "platform_name", "region", "native_currency",
				"is_vendor", "vendor", "currency",
				"input_per_1m", "output_per_1m", "cny_in_per_1m", "cny_out_per_1m",
				"cache_hit_per_1m", "cny_cache_hit_per_1m", "tier", "model_kind",
				"run_date", "source_url",
			}).AddRow(
				"glm-4", "zhipu", "Zhipu", "domestic", "CNY",
				true, "zhipu", "CNY",
				nil, 2.0, nil, 14.0,
				nil, 0.5, "输入32K+", "文本/对话",
				"2026-11-17", "https://example.test",
			),
		)

	h := &Handler{marketPlatformDB: mock}
	rr := httptest.NewRecorder()
	h.handleMarketModelPlatforms(rr, httptest.NewRequest(http.MethodGet, "/api/admin/market/model-platforms?model=glm-4", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Available bool `json:"available"`
		IsDir     bool `json:"is_dir"`
		Items     []struct {
			InputPer1M     *float64 `json:"input_per_1m"`
			OutputPer1M    *float64 `json:"output_per_1m"`
			IsVendor       bool     `json:"is_vendor"`
			CacheHitPer1M  *float64 `json:"cache_hit_per_1m"`
			Tier           string   `json:"tier"`
			ModelKind      string   `json:"model_kind"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Available || len(body.Items) != 1 {
		t.Fatalf("unexpected body %#v", body)
	}
	if body.IsDir {
		t.Fatal("指定了 model ⇒ is_dir 必须为 false")
	}
	if body.Items[0].InputPer1M != nil {
		t.Fatalf("NULL input must stay nil, got %v", body.Items[0].InputPer1M)
	}
	if body.Items[0].OutputPer1M == nil || *body.Items[0].OutputPer1M != 2.0 {
		t.Fatalf("output=%v", body.Items[0].OutputPer1M)
	}
	if !body.Items[0].IsVendor {
		t.Fatal("is_vendor expected true")
	}
	// ★ 2026-11-17：缓存命中价（NULL≠0）与阶梯/类别随行落地。
	if body.Items[0].CacheHitPer1M != nil {
		t.Fatalf("NULL cache hit must stay nil, got %v", body.Items[0].CacheHitPer1M)
	}
	if body.Items[0].Tier != "输入32K+" || body.Items[0].ModelKind != "文本/对话" {
		t.Fatalf("tier/model_kind 未落地：%q/%q", body.Items[0].Tier, body.Items[0].ModelKind)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// ★ 2026-11-17：没指定 model ⇒ 返回**模型目录**（而不是全量明细），
//   并把 is_dir 置 true ⇒ 前端据此渲染模型选择器（不再要求手打 canonical 名）。
func TestMarketModelPlatformsDirectoryMode(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery(regexp.QuoteMeta("to_regclass('public.market_price_platform_quotes')")).
		WillReturnRows(mock.NewRows([]string{"ok"}).AddRow(true))
	mock.ExpectQuery(`FROM market_price_platform_quotes`).
		WithArgs("standard", "", 200).
		WillReturnRows(mock.NewRows([]string{
			"canonical_name", "platform_id", "platform_name", "region", "native_currency",
			"is_vendor", "vendor", "currency",
			"input_per_1m", "output_per_1m", "cny_in_per_1m", "cny_out_per_1m",
			"cache_hit_per_1m", "cny_cache_hit_per_1m", "tier", "model_kind",
			"run_date", "source_url",
		}).AddRow(
			"glm-4", "zhipu", "Zhipu", "domestic", "CNY",
			true, "zhipu", "CNY",
			nil, 2.0, nil, 14.0,
			nil, nil, "", "文本/对话",
			"2026-11-17", "https://example.test",
		))
	mock.ExpectQuery(`count\(DISTINCT q\.platform_id\)`).
		WithArgs("standard", 200).
		WillReturnRows(mock.NewRows([]string{
			"canonical_name", "display_name", "platforms", "vendor_rows",
			"min_cny_in", "min_cny_out", "run_date",
		}).AddRow("glm-4", "GLM-4", 3, 1, 1.5, 9.0, "2026-11-17"))

	h := &Handler{marketPlatformDB: mock}
	rr := httptest.NewRecorder()
	h.handleMarketModelPlatforms(rr, httptest.NewRequest(http.MethodGet, "/api/admin/market/model-platforms", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		IsDir     bool `json:"is_dir"`
		Directory []struct {
			CanonicalName string   `json:"canonical_name"`
			DisplayName   string   `json:"display_name"`
			Platforms     int      `json:"platforms"`
			MinCNYOut     *float64 `json:"min_cny_out_per_1m"`
		} `json:"directory"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.IsDir {
		t.Fatal("未指定 model ⇒ is_dir 必须为 true")
	}
	if len(body.Directory) != 1 || body.Directory[0].CanonicalName != "glm-4" {
		t.Fatalf("目录读数错：%#v", body.Directory)
	}
	if body.Directory[0].Platforms != 3 {
		t.Fatalf("可比平台数错：%d", body.Directory[0].Platforms)
	}
	if body.Directory[0].MinCNYOut == nil || *body.Directory[0].MinCNYOut != 9.0 {
		t.Fatalf("最低价错：%v", body.Directory[0].MinCNYOut)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMarketModelPlatformsMethodGate(t *testing.T) {
	h := &Handler{}
	rr := httptest.NewRecorder()
	h.handleMarketModelPlatforms(rr, httptest.NewRequest(http.MethodPost, "/api/admin/market/model-platforms", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", rr.Code)
	}
}
