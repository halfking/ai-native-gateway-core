package admin

// market_compare_test.go — M3-T2 compare 端点测试（pgxmock 全查询路径）。
//
// 覆盖面：
//	① 表缺席 ⇒ available:false + 空态语义（不是 500）；
//	② happy path：评分行 + mpr LATERAL 对拍扫描（NULL 价格列按指针透传）+ 覆盖率摘要；
//	③ 方法闸 / nil pool。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v4"
)

func TestMarketCompareTableAbsentIsAvailableFalse(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()
	h := &Handler{marketPlatformDB: mock}

	mock.ExpectQuery(regexp.QuoteMeta("to_regclass('public.supplier_market_position')")).
		WillReturnRows(mock.NewRows([]string{"present"}).AddRow(false))

	rr := httptest.NewRecorder()
	h.handleMarketCompare(rr, httptest.NewRequest(http.MethodGet, "/api/admin/market/compare", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("表缺席应 200（空态语义），得到 %d：%s", rr.Code, rr.Body.String())
	}
	var out struct {
		Available bool   `json:"available"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("非法 JSON：%v", err)
	}
	if out.Available {
		t.Fatal("表缺席必须 available:false")
	}
	if !strings.Contains(out.Reason, "959") {
		t.Fatalf("reason 要给「为什么空」的指针：%s", out.Reason)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL 期望未满足：%v", err)
	}
}

func TestMarketCompareScansRowsAndSummary(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()
	h := &Handler{marketPlatformDB: mock}

	mock.ExpectQuery(regexp.QuoteMeta("to_regclass('public.supplier_market_position')")).
		WillReturnRows(mock.NewRows([]string{"present"}).AddRow(true))

	mainCols := []string{
		"credential_id", "provider_model_id", "canonical_name", "currency",
		"supplier_in_per_1m", "supplier_out_per_1m", "price_confidence",
		"discount_vs_vendor", "vendor_ref_in_per_1m", "vendor_ref_out_per_1m",
		"vendor_ref_currency", "fx_used",
		"market_position", "market_ref_in_per_1m", "market_ref_out_per_1m",
		"market_ref_platform_id", "market_ref_platform_count", "market_ref_used",
		"market_advantage_score", "market_position_vs_overseas",
		"market_position_vs_domestic", "computed_at",
		"overseas_min_in_per_1m", "overseas_min_out_per_1m",
		"overseas_min_platform_id", "overseas_min_platform_count",
		"domestic_min_in_per_1m", "domestic_min_out_per_1m",
		"domestic_min_platform_id", "domestic_min_platform_count",
		"vendor_list_currency",
		"vendor_list_in_per_1m", "vendor_list_out_per_1m",
		"vendor_list_cny_in_per_1m", "vendor_list_cny_out_per_1m",
		"mpr_fx_rate",
	}
	computedAt := time.Date(2026, 11, 13, 3, 10, 0, 0, time.UTC)
	// 行 1：trusted 全值行；行 2：empty 行（供应商价 NULL ⇒ C2 全 NULL 透传，
	// mpr 地板有值 —— NULL≠0 红线的对偶：有地板就不能被行内 NULL 吞掉）。
	mock.ExpectQuery(regexp.QuoteMeta("FROM public.supplier_market_position s")).
		WithArgs("", "", 200).
		WillReturnRows(mock.NewRows(mainCols).
			AddRow(int64(7), int64(33), "glm-5.3", "CNY",
				2.0, 8.0, "trusted",
				0.15, 2.35, 9.4, "CNY", nil,
				1.1, 1.9, 7.6, "zhipu", 4, "domestic",
				-2.5, 0.9, 1.1, computedAt,
				1.9, 7.6, "zhipu", 4,
				3.5, 14.0, "minimax", 2,
				"CNY", 2.35, 9.4, 2.35, 9.4, 6.687755).
			AddRow(int64(8), int64(34), "whisper-large-v3-turbo", "USD",
				nil, nil, "empty",
				nil, nil, nil, "", nil,
				nil, nil, nil, "", 0, "",
				nil, nil, nil, computedAt,
				0.5, 2.0, "openai", 5,
				nil, nil, "", 0,
				"USD", 0.5, 2.0, 3.34, 13.36, 6.687755))

	mock.ExpectQuery(regexp.QuoteMeta("GROUP BY price_confidence")).
		WillReturnRows(mock.NewRows([]string{"price_confidence", "count"}).
			AddRow("trusted", int64(27)).
			AddRow("empty", int64(4099)))

	rr := httptest.NewRecorder()
	h.handleMarketCompare(rr, httptest.NewRequest(http.MethodGet, "/api/admin/market/compare", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("应 200，得到 %d：%s", rr.Code, rr.Body.String())
	}
	var out struct {
		Available bool               `json:"available"`
		Items     []marketCompareRow `json:"items"`
		Summary   map[string]int64   `json:"summary"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("非法 JSON：%v\n%s", err, rr.Body.String())
	}
	if !out.Available || len(out.Items) != 2 {
		t.Fatalf("available=%v items=%d", out.Available, len(out.Items))
	}
	row1 := out.Items[0]
	if row1.CanonicalName != "glm-5.3" || row1.PriceConfidence != "trusted" ||
		row1.MarketRefUsed != "domestic" || row1.MarketRefCount != 4 {
		t.Fatalf("行 1 形状不对：%+v", row1)
	}
	if row1.ComputedAt != "2026-11-13T03:10:00Z" {
		t.Fatalf("computed_at 应 RFC3339：%q", row1.ComputedAt)
	}
	// 行 2：NULL 价格列必须保持 NULL（nil），不被落 0。
	row2 := out.Items[1]
	if row2.SupplierIn != nil || row2.MarketPosition != nil || row2.MarketRefIn != nil {
		t.Fatalf("NULL 列被落了值（NULL≠0 红线）：%+v", row2)
	}
	if row2.DomesticIn != nil && *row2.DomesticIn != 0 {
		t.Fatalf("domestic 无地板应 NULL：%+v", row2.DomesticIn)
	}
	if row2.OverseasIn == nil || *row2.OverseasIn != 0.5 {
		t.Fatalf("overseas 地板应透传 0.5：%+v", row2.OverseasIn)
	}
	if out.Summary["trusted"] != 27 || out.Summary["empty"] != 4099 {
		t.Fatalf("覆盖率摘要不对：%v", out.Summary)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL 期望未满足：%v", err)
	}
}

func TestMarketCompareGuards(t *testing.T) {
	h := &Handler{}
	rr := httptest.NewRecorder()
	h.handleMarketCompare(rr, httptest.NewRequest(http.MethodGet, "/api/admin/market/compare", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil pool 应 503，得到 %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	h.handleMarketCompare(rr, httptest.NewRequest(http.MethodPost, "/api/admin/market/compare", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST 应 405，得到 %d", rr.Code)
	}
}
