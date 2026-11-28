// provider_endpoints_test.go — provider_endpoint_protocols admin 管理面判据
// （协议自适应方案 A4）。
//
// mock 正则须逐字符对拍 SQL 本体（L144 教训：mock 失配的表现是「业务路径拿到了
// 错误的失败」，与真实 404/500 同形难辨）——所以每条期望都带着对应 SQL 的特征片段。
package admin

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
)

func endpointCols() []string {
	return []string{"id", "protocol", "base_url", "is_primary", "vendor_native", "enabled", "weight", "notes",
		"health_status", "health_checked_at", "health_latency_ms", "health_error", "consecutive_failures",
		"created_at", "updated_at"}
}

func endpointRow(id int64, protocol, baseURL string, primary bool) *pgxmock.Rows {
	return pgxmock.NewRows(endpointCols()).AddRow(id, protocol, baseURL, primary, nil, true, int16(100), nil,
		"unknown", nil, nil, nil, 0, nowPtr(), nowPtr())
}

func expectProviderKind(mock pgxmock.PgxPoolIface, id int, kind string) {
	mock.ExpectQuery(`SELECT COALESCE\(kind,''\) FROM providers WHERE id = \$1`).
		WithArgs(id).
		WillReturnRows(pgxmock.NewRows([]string{"kind"}).AddRow(kind))
}

func doEndpoints(h *Handler, method, path, body string, providerID int, sub string) *httptest.ResponseRecorder {
	req := superAdminReq(method, path, body)
	rec := httptest.NewRecorder()
	h.handleProviderEndpoints(rec, req, providerID, sub)
	return rec
}

// 新建非主端点：先查供应商 → 事务内查无行 → INSERT → COMMIT，返回 201。
func TestProviderEndpointPutCreates(t *testing.T) {
	h, mock, _ := newOrgsTestFixture(t)
	expectProviderKind(mock, 5, "cloud")
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM provider_endpoint_protocols\s+WHERE provider_id = \$1 AND protocol = \$2\s+FOR UPDATE`).
		WithArgs(5, "anthropic-messages").WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO provider_endpoint_protocols`).
		WithArgs(5, "anthropic-messages", "https://up.example/anthropic", false, nil, true, 100, nil).
		WillReturnRows(endpointRow(11, "anthropic-messages", "https://up.example/anthropic", false))
	mock.ExpectCommit()

	rec := doEndpoints(h, http.MethodPut, "/api/providers/5/endpoints/anthropic-messages",
		`{"base_url":" https://up.example/anthropic "}`, 5, "anthropic-messages")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

// 提升为主端点：必须先摘掉旧主端点（部分唯一索引），再 UPDATE 目标行。
func TestProviderEndpointPutPromotesDemotesOldPrimary(t *testing.T) {
	h, mock, _ := newOrgsTestFixture(t)
	expectProviderKind(mock, 5, "cloud")
	mock.ExpectBegin()
	mock.ExpectQuery(`FOR UPDATE`).WithArgs(5, "openai-responses").
		WillReturnRows(pgxmock.NewRows([]string{"id", "is_primary", "base_url"}).AddRow(int64(3), false, "https://up.example/v1"))
	mock.ExpectExec(`UPDATE provider_endpoint_protocols\s+SET is_primary = false, updated_at = now\(\)\s+WHERE provider_id = \$1 AND is_primary = true AND protocol <> \$2`).
		WithArgs(5, "openai-responses").WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
	mock.ExpectQuery(`UPDATE provider_endpoint_protocols SET`).
		WithArgs(5, "openai-responses", "https://up.example/v1", pgxmock.AnyArg(), pgxmock.AnyArg(), nil,
			false, nil, false, nil).
		WillReturnRows(endpointRow(3, "openai-responses", "https://up.example/v1", true))
	mock.ExpectCommit()

	rec := doEndpoints(h, http.MethodPut, "/api/providers/5/endpoints/openai-responses",
		`{"is_primary":true}`, 5, "openai-responses")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

// 不允许把当前主端点直接降级/停用——否则供应商落到「零主端点」。
func TestProviderEndpointPutRefusesDemotingOrDisablingPrimary(t *testing.T) {
	for name, body := range map[string]string{
		"demote":  `{"is_primary":false}`,
		"disable": `{"enabled":false}`,
	} {
		t.Run(name, func(t *testing.T) {
			h, mock, _ := newOrgsTestFixture(t)
			expectProviderKind(mock, 5, "cloud")
			mock.ExpectBegin()
			mock.ExpectQuery(`FOR UPDATE`).WithArgs(5, "openai-completions").
				WillReturnRows(pgxmock.NewRows([]string{"id", "is_primary", "base_url"}).AddRow(int64(1), true, "https://up.example/v1"))
			mock.ExpectRollback()
			rec := doEndpoints(h, http.MethodPut, "/x", body, 5, "openai-completions")
			if rec.Code != http.StatusConflict {
				t.Fatalf("want 409, got %d body=%s", rec.Code, rec.Body.String())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet: %v", err)
			}
		})
	}
}

// 输入校验：全部在落库前拒绝（零 SQL 或仅供应商查询）。
func TestProviderEndpointPutRejectsBadInput(t *testing.T) {
	cases := []struct{ name, sub, body string }{
		{"adaptive 选择器不是线格式", "adaptive", `{"base_url":"https://a.example"}`},
		{"未知协议", "carrier-pigeon", `{"base_url":"https://a.example"}`},
		{"weight 越下界", "openai-completions", `{"base_url":"https://a.example","weight":0}`},
		{"weight 越上界", "openai-completions", `{"base_url":"https://a.example","weight":1000}`},
		{"notes 过长", "openai-completions", `{"base_url":"https://a.example","notes":"` + strings.Repeat("x", 501) + `"}`},
		{"非 JSON", "openai-completions", `not json`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, mock, _ := newOrgsTestFixture(t)
			rec := doEndpoints(h, http.MethodPut, "/x", tc.body, 5, tc.sub)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d body=%s", rec.Code, rec.Body.String())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("校验失败必须发生在任何 SQL 之前: %v", err)
			}
		})
	}
}

func TestProviderEndpointPutBaseURLValidation(t *testing.T) {
	cases := []struct{ name, kind, url string }{
		{"空串", "cloud", ` `},
		{"非 http 协议", "cloud", `ftp://a.example`},
		{"无主机", "cloud", `https://`},
		{"本地供应商指向公网", LocalProviderKind, `https://api.openai.com/v1`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, mock, _ := newOrgsTestFixture(t)
			expectProviderKind(mock, 5, tc.kind)
			mock.ExpectBegin()
			mock.ExpectQuery(`FOR UPDATE`).WithArgs(5, "openai-completions").WillReturnError(pgx.ErrNoRows)
			mock.ExpectRollback()
			rec := doEndpoints(h, http.MethodPut, "/x", `{"base_url":"`+tc.url+`"}`, 5, "openai-completions")
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d body=%s", rec.Code, rec.Body.String())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet: %v", err)
			}
		})
	}
}

func TestProviderEndpointPutProvider404(t *testing.T) {
	h, mock, _ := newOrgsTestFixture(t)
	mock.ExpectQuery(`SELECT COALESCE\(kind,''\) FROM providers`).WithArgs(9).WillReturnError(pgx.ErrNoRows)
	rec := doEndpoints(h, http.MethodPut, "/x", `{"base_url":"https://a.example"}`, 9, "openai-completions")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestProviderEndpointDelete(t *testing.T) {
	t.Run("非主端点可删", func(t *testing.T) {
		h, mock, _ := newOrgsTestFixture(t)
		mock.ExpectQuery(`SELECT is_primary FROM provider_endpoint_protocols`).WithArgs(5, "gemini-generate").
			WillReturnRows(pgxmock.NewRows([]string{"is_primary"}).AddRow(false))
		mock.ExpectExec(`DELETE FROM provider_endpoint_protocols\s+WHERE provider_id = \$1 AND protocol = \$2 AND is_primary = false`).
			WithArgs(5, "gemini-generate").WillReturnResult(pgconn.NewCommandTag("DELETE 1"))
		rec := doEndpoints(h, http.MethodDelete, "/x", ``, 5, "gemini-generate")
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet: %v", err)
		}
	})
	t.Run("主端点不可删", func(t *testing.T) {
		h, mock, _ := newOrgsTestFixture(t)
		mock.ExpectQuery(`SELECT is_primary FROM provider_endpoint_protocols`).WithArgs(5, "openai-completions").
			WillReturnRows(pgxmock.NewRows([]string{"is_primary"}).AddRow(true))
		rec := doEndpoints(h, http.MethodDelete, "/x", ``, 5, "openai-completions")
		if rec.Code != http.StatusConflict {
			t.Fatalf("want 409, got %d", rec.Code)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("主端点路径不得发出 DELETE: %v", err)
		}
	})
	t.Run("并发提升后命中 0 行必须报冲突而非成功", func(t *testing.T) {
		// 读到的 is_primary=false 已过期：并发 PUT 把该行提升为主端点，
		// `AND is_primary = false` 于是命中 0 行。报 200 就是假的成功。
		h, mock, _ := newOrgsTestFixture(t)
		mock.ExpectQuery(`SELECT is_primary FROM provider_endpoint_protocols`).WithArgs(5, "gemini-generate").
			WillReturnRows(pgxmock.NewRows([]string{"is_primary"}).AddRow(false))
		mock.ExpectExec(`DELETE FROM provider_endpoint_protocols`).
			WithArgs(5, "gemini-generate").WillReturnResult(pgconn.NewCommandTag("DELETE 0"))
		rec := doEndpoints(h, http.MethodDelete, "/x", ``, 5, "gemini-generate")
		if rec.Code != http.StatusConflict {
			t.Fatalf("want 409, got %d body=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("不存在", func(t *testing.T) {
		h, mock, _ := newOrgsTestFixture(t)
		mock.ExpectQuery(`SELECT is_primary FROM provider_endpoint_protocols`).WithArgs(5, "openai-completions").
			WillReturnError(pgx.ErrNoRows)
		rec := doEndpoints(h, http.MethodDelete, "/x", ``, 5, "openai-completions")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("want 404, got %d", rec.Code)
		}
	})
}

func TestProviderEndpointList(t *testing.T) {
	h, mock, _ := newOrgsTestFixture(t)
	mock.ExpectQuery(`FROM provider_endpoint_protocols\s+WHERE provider_id = \$1\s+ORDER BY is_primary DESC, weight DESC, id ASC`).
		WithArgs(5).
		WillReturnRows(endpointRow(1, "openai-completions", "https://up.example/v1", true).
			AddRow(int64(2), "anthropic-messages", "https://up.example/a", false, nil, true, int16(50), nil,
				"unknown", nil, nil, nil, 0, nowPtr(), nowPtr()))
	rec := doEndpoints(h, http.MethodGet, "/x", ``, 5, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"anthropic-messages"`) || !strings.Contains(body, `"weight":50`) {
		t.Errorf("列表缺字段: %s", body)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

func TestProviderEndpointRequiresSuperAdmin(t *testing.T) {
	h, _, _ := newOrgsTestFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req = SetAuthContext(req, &AuthContext{UserID: 2, Username: "t", Role: RoleTenantAdmin, IsJWT: true})
	rec := httptest.NewRecorder()
	h.handleProviderEndpoints(rec, req, 5, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rec.Code)
	}
}

// 协议集合必须与表 CHECK 逐字一致——漂了就是「后端放行、库侧 23514」或
// 「库侧允许、后端拒收」的半条链。
func TestProviderEndpointProtocolsMatchTableCheck(t *testing.T) {
	raw, err := os.ReadFile("../sql/objects/tables/provider_endpoint_protocols.sql")
	if err != nil {
		t.Fatalf("read table DDL: %v", err)
	}
	m := regexp.MustCompile(`(?s)provider_endpoint_protocols_protocol_check\s+CHECK \(protocol = ANY \(ARRAY\[(.*?)\]\)\)`).FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatal("DDL 里找不到 protocol CHECK")
	}
	var inDDL []string
	for _, v := range regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(m[1], -1) {
		inDDL = append(inDDL, v[1])
	}
	if strings.Join(inDDL, ",") != strings.Join(providerEndpointProtocols, ",") {
		t.Fatalf("协议集合漂移:\n DDL=%v\n Go =%v", inDDL, providerEndpointProtocols)
	}
}

// 接线门（源码锚点）：分发器必须把 endpoints 子路径交给 handleProviderEndpoints，
// 写路径必须失效候选缓存。写了处理器没接分发、或写了不失效，都是「门不承重」。
func TestProviderEndpointWiringAnchors(t *testing.T) {
	disp, err := os.ReadFile("providers.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(disp), "h.handleProviderEndpoints("); n < 2 {
		t.Errorf("providers.go 分发器里 handleProviderEndpoints 调用点 %d 处，应 ≥2（精确 \"endpoints\" 与前缀 \"endpoints/\"）", n)
	}
	src, err := os.ReadFile("provider_endpoints.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(src), "provider.InvalidateAllCandidateCache()"); n < 2 {
		t.Errorf("provider_endpoints.go 写路径（PUT/DELETE）都必须失效候选缓存，现有 %d 处", n)
	}
}
