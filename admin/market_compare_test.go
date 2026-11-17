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

// TestMarketCompareToleratesMissingMarketPriceLateralRow 钉 F13（2026-11-17）。
//
// 缺陷形态：`mpr` 是 LEFT JOIN LATERAL ⇒ **未命中时整组 mpr.* 全 NULL**；
// 其中 overseas/domestic 的 platform_id（string）与 platform_count（int）
// 被扫进裸类型 ⇒ pgx 报 "cannot scan NULL into *string" ⇒
// `/api/admin/market/compare` 对**任何调用方**都 500。
// 生产实测（211）：前 200 行里 **194 行**没有 mpr 匹配；全量 4176 行里 2396 行
// （57%）没有 ⇒ 「没有行情行」是**常态**，不是异常。
//
// ★ 为什么既有测试抓不到：既有 happy-path 那一行把四个 mpr 列写成 `"" , 0`
//
//	（非 NULL）⇒ **从未走过「LATERAL 未命中」这条路** ⇒ 门是绿的、线上是 500。
//	⇒ 本用例专门造 NULL 行。
//
// ★ ★ **本用例的门的强度必须说清（量具纪律）**：pgxmock 的 Scan 语义与真 pgx
//
//	**不同** —— ① NULL 时它直接忽略目标、**不报错**（真 pgx 会报 NULL→*string）；
//	② 对 `**T` 目的地与真 pgx 行为也不同（这正是修法用 sql.NullString/
//	sql.NullInt64 而不是 `*string/*int` 的原因：前者两侧语义一致）。
//	⇒ **本用例守的是「响应语义」（缺失 ⇒ 空串/0、不编造），守不住「不再 500」**；
//	**有牙的是真库门** `admin/market_compare_realdb_test.go`（真 pgx + 真数据，
//	变异臂 M7 实测复现生产报错原文）。
//
// ★ 断言的语义（NULL≠0）：缺失 ⇒ 端点**照常 200**，四列保持零值（空串 / 0），
//
//	**不报错、也不编造别的模型的价**。
func TestMarketCompareToleratesMissingMarketPriceLateralRow(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()
	h := &Handler{marketPlatformDB: mock}

	mock.ExpectQuery(regexp.QuoteMeta("to_regclass('public.supplier_market_position')")).
		WillReturnRows(mock.NewRows([]string{"present"}).AddRow(true))

	cols := []string{
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
	computedAt := time.Date(2026, 11, 17, 3, 10, 0, 0, time.UTC)
	// ★ LATERAL 未命中 ⇒ 末尾 14 个 mpr 列**全 NULL**（含两个 platform_id 与两个 count）。
	//   按列组拼装 + 长度自校验（手数 35 vs 36 这类错自己会当场红）。
	fixtureRow := []any{
		int64(9), int64(41), "glm-5.3", "CNY",
		2.0, 8.0, "trusted",
		0.15, 2.35, 9.4, "CNY", nil,
		1.1, 1.9, 7.6, "zhipu", 4, "domestic",
		-2.5, 0.9, 1.1, computedAt,
	}
	fixtureRow = append(fixtureRow, nil, nil, nil, nil) // overseas 双地板 + platform_id + count
	fixtureRow = append(fixtureRow, nil, nil, nil, nil) // domestic 双地板 + platform_id + count
	fixtureRow = append(fixtureRow, nil)                // vendor_list_currency
	fixtureRow = append(fixtureRow, nil, nil, nil, nil) // vendor_list 原币入/出 + CNY 入/出
	fixtureRow = append(fixtureRow, nil)                // fx_rate
	if len(fixtureRow) != len(cols) {
		t.Fatalf("夹具行值数 %d ≠ 列数 %d（拼装漏了列）", len(fixtureRow), len(cols))
	}
	mock.ExpectQuery(regexp.QuoteMeta("FROM public.supplier_market_position s")).
		WithArgs("", "", 200).
		WillReturnRows(mock.NewRows(cols).AddRow(fixtureRow...))

	mock.ExpectQuery(regexp.QuoteMeta("GROUP BY price_confidence")).
		WillReturnRows(mock.NewRows([]string{"price_confidence", "count"}).
			AddRow("trusted", int64(27)))

	rr := httptest.NewRecorder()
	h.handleMarketCompare(rr, httptest.NewRequest(http.MethodGet, "/api/admin/market/compare", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("LATERAL 未命中（LATERAL 无行）应 200，得到 %d：%s", rr.Code, rr.Body.String())
	}
	var out struct {
		Available bool               `json:"available"`
		Items     []marketCompareRow `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("非法 JSON：%v\n%s", err, rr.Body.String())
	}
	if len(out.Items) != 1 {
		t.Fatalf("items=%d，期望 1", len(out.Items))
	}
	row := out.Items[0]
	// 缺失的四列保持零值：不报错，也不编造别的模型的价。
	if row.OverseasID != "" || row.DomesticID != "" {
		t.Fatalf("LATERAL 未命中时 platform_id 应为空串：overseas=%q domestic=%q", row.OverseasID, row.DomesticID)
	}
	if row.OverseasCount != 0 || row.DomesticCount != 0 {
		t.Fatalf("LATERAL 未命中时 platform_count 应为 0：overseas=%d domestic=%d", row.OverseasCount, row.DomesticCount)
	}
	// 行内自身的值不受影响（不能因为 mpr 缺失把评分行也抹掉）。
	if row.CanonicalName != "glm-5.3" || row.PriceConfidence != "trusted" {
		t.Fatalf("评分行本身应照常返回：%+v", row)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL 期望未满足：%v", err)
	}
}
