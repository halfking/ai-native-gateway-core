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
				"run_date", "source_url",
			}).AddRow(
				"glm-4", "zhipu", "Zhipu", "domestic", "CNY",
				true, "zhipu", "CNY",
				nil, 2.0, nil, 14.0,
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
		Items     []struct {
			InputPer1M  *float64 `json:"input_per_1m"`
			OutputPer1M *float64 `json:"output_per_1m"`
			IsVendor    bool     `json:"is_vendor"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Available || len(body.Items) != 1 {
		t.Fatalf("unexpected body %#v", body)
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
