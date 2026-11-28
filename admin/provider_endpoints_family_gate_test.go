package admin

import (
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
)

// (2026-11-29 模型↔供应商匹配链审计 F2)：
// vendor_native 写侧校验——非空值必须落在 model_families ∪ models_canonical.family
// 词表内；空/NULL 放行；未知值 400 并带近似提示。
//
// 为什么这道门承重：端点选择器 Stage 1A/2/3 按【裸字符串相等】比对
// models_canonical.family（候选侧）与 provider_endpoint_protocols.vendor_native
// （端点侧）。词表外写一个 "glm" 就让家族匹配永不命中 ⇒ 静默回落 Stage 4 主端点，
// 运营无从察觉。写侧拦住词表外值 = 把「上线后从日志反推」变成「当场改对」。

// vendorFamilyRows 构造词表查询的返回行（mock 正则须逐字符对拍 SQL 本体）。
func vendorFamilyRows(ids ...string) *pgxmock.Rows {
	rows := pgxmock.NewRows([]string{"id"})
	for _, id := range ids {
		rows.AddRow(id)
	}
	return rows
}

// 词表 UNION 查询（checkVendorNativeFamily 的主查询；hint 兜底查询多一段 ORDER BY 1 LIMIT 8）。
const vendorFamilyUnionSQL = `SELECT id FROM model_families\s+UNION\s+SELECT DISTINCT family FROM models_canonical\s+WHERE family IS NOT NULL AND family <> ''`

const vendorFamilyHintSQL = `SELECT id FROM model_families\s+UNION\s+SELECT DISTINCT family FROM models_canonical\s+WHERE family IS NOT NULL AND family <> ''\s+ORDER BY 1 LIMIT 8`

func TestProviderEndpointPutVendorNativeAcceptsKnownFamily(t *testing.T) {
	h, mock, _ := newOrgsTestFixture(t)
	// handler 校验顺序：vendor 词表校验先于 provider kind 查询 ⇒ mock 注册序一致。
	mock.ExpectQuery(vendorFamilyUnionSQL).
		WillReturnRows(vendorFamilyRows("zhipu-glm", "anthropic-claude"))
	expectProviderKind(mock, 5, "cloud")
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM provider_endpoint_protocols\s+WHERE provider_id = \$1 AND protocol = \$2\s+FOR UPDATE`).
		WithArgs(5, "anthropic-messages").WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO provider_endpoint_protocols`).
		WithArgs(5, "anthropic-messages", "https://up.example/anthropic", false, "zhipu-glm", true, 100, nil).
		WillReturnRows(endpointRow(11, "anthropic-messages", "https://up.example/anthropic", false))
	mock.ExpectCommit()

	rec := doEndpoints(h, http.MethodPut, "/api/providers/5/endpoints/anthropic-messages",
		`{"base_url":"https://up.example/anthropic","vendor_native":"zhipu-glm"}`, 5, "anthropic-messages")
	if rec.Code != http.StatusCreated {
		t.Fatalf("known family must be accepted with 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

func TestProviderEndpointPutVendorNativeEmptyStaysAllowed(t *testing.T) {
	// 空/NULL 放行：存量 50/53 行 vendor_native 就是 NULL，未配 = 不参与家族匹配；
	// 校验只对非空值生效，不能把「没配」变成「必须配」。
	h, mock, _ := newOrgsTestFixture(t)
	expectProviderKind(mock, 5, "cloud")
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM provider_endpoint_protocols\s+WHERE provider_id = \$1 AND protocol = \$2\s+FOR UPDATE`).
		WithArgs(5, "openai-completions").WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO provider_endpoint_protocols`).
		WithArgs(5, "openai-completions", "https://up.example/v1", false, nil, true, 100, nil).
		WillReturnRows(endpointRow(12, "openai-completions", "https://up.example/v1", false))
	mock.ExpectCommit()

	rec := doEndpoints(h, http.MethodPut, "/api/providers/5/endpoints/openai-completions",
		`{"base_url":"https://up.example/v1"}`, 5, "openai-completions")
	if rec.Code != http.StatusCreated {
		t.Fatalf("no vendor_native must stay allowed, got %d body=%s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

func TestProviderEndpointPutVendorNativeUnknownFamilyRejected(t *testing.T) {
	// 词表外值（"glm"）必须 400，且错误体带近似提示（前缀命中 zhipu-glm）；
	// 拒绝必须发生在任何写 SQL 之前（只允许词表查询 + provider kind 查询）。
	h, mock, _ := newOrgsTestFixture(t)
	// 400 发生在 kind 查询与事务开启之前 ⇒ 不登记后续期望。
	mock.ExpectQuery(vendorFamilyUnionSQL).
		WillReturnRows(vendorFamilyRows("zhipu-glm", "anthropic-claude", "openai-gpt"))

	rec := doEndpoints(h, http.MethodPut, "/api/providers/5/endpoints/openai-completions",
		`{"base_url":"https://up.example/v1","vendor_native":"glm"}`, 5, "openai-completions")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown family must 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "zhipu-glm") {
		t.Fatalf("400 must carry the closest-match hint, body=%s", rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

func TestProviderEndpointPutVendorNativeUnknownNoHintListsVocabulary(t *testing.T) {
	// 完全无关的值（"carrier"）也 400，提示回落为词表样例列表。
	h, mock, _ := newOrgsTestFixture(t)
	mock.ExpectQuery(vendorFamilyUnionSQL).
		WillReturnRows(vendorFamilyRows("zhipu-glm", "anthropic-claude", "openai-gpt", "google-gemini"))
	// 无相近项 ⇒ hint 兜底查询（词表样例 LIMIT 8）也会执行。
	mock.ExpectQuery(vendorFamilyHintSQL).
		WillReturnRows(vendorFamilyRows("anthropic-claude", "google-gemini", "openai-gpt", "zhipu-glm"))
	// 400 发生在 kind 查询与事务开启之前 ⇒ 不登记后续期望。

	rec := doEndpoints(h, http.MethodPut, "/api/providers/5/endpoints/openai-completions",
		`{"base_url":"https://up.example/v1","vendor_native":"carrier"}`, 5, "openai-completions")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown family must 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "anthropic-claude") {
		t.Fatalf("hint must fall back to vocabulary samples, body=%s", rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}
