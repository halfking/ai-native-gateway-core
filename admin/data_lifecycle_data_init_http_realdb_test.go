package admin

// 数据初始化（data-init）的**HTTP 层**真库验证。
//
// # ★ 与 realdb 测试的分工
//
//   · admin/data_lifecycle_data_init_realdb_test.go —— 验**数据层**（统计口径、
//     每条 DELETE 的 EXPLAIN 计划）。
//   · 本文件 —— 验**HTTP 层**（请求解析 / 类别白名单 / 令牌校验 / 响应形状）。
//
// # ★ 为什么 execute 不在真库上真跑
//
// execute 会**真的删数据**（能删掉全部用户/租户/凭据）⇒ 真库上跑一遍等于
// 生产事故 ⇒ 本文件只验它的**拒绝路径**：
//
//   ① 没带令牌 → 400；
//   ② 令牌属于别的类别 → 400；
//   ③ 令牌被篡改 → 400。
//
// ⇒ 「令牌这道闸门在真库上确实拦得住」被证明，而真删除只发生在隔离库里。
//
// # 环境变量：TEST_DATA_INIT_DSN → LLM_GATEWAY_DATABASE_URL → DATABASE_URL。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// dataInitHTTPPool 连真库（与 realdb 测试共用同一套环境变量口径）。
func dataInitHTTPPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), dataInitTestDSN(t))
	if err != nil {
		t.Fatalf("connect failed: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// dataInitJSON 解析响应体。
func dataInitJSON(t *testing.T, rec *httptest.ResponseRecorder, out any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("decode failed: %v\nbody=%s", err, rec.Body.String())
	}
}

func TestDataInitCatalogHandler_OnRealDB(t *testing.T) {
	pool := dataInitHTTPPool(t)
	h := NewHandler(pool, "", nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/data-lifecycle/data-init/catalog", nil)
	rec := httptest.NewRecorder()
	h.handleDataInitCatalog(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp dataInitCatalogResponse
	dataInitJSON(t, rec, &resp)

	if len(resp.Categories) != len(dataInitCategories) {
		t.Fatalf("got %d categories, want %d", len(resp.Categories), len(dataInitCategories))
	}
	for _, cat := range resp.Categories {
		if cat.Key == "" || cat.LabelKey == "" {
			t.Errorf("category missing key/label: %+v", cat)
		}
		if cat.Level != dataInitLevelOperational && cat.Level != dataInitLevelConfig {
			t.Errorf("category %s: unexpected level %q", cat.Key, cat.Level)
		}
		if len(cat.Tables) == 0 {
			t.Errorf("category %s: no table stats", cat.Key)
		}
	}
	t.Logf("catalog over HTTP: %d categories, generated_at=%s", len(resp.Categories), resp.GeneratedAt)
}

func TestDataInitPreviewHandler_OnRealDB(t *testing.T) {
	pool := dataInitHTTPPool(t)
	h := NewHandler(pool, "", nil, nil)

	body := strings.NewReader(`{"categories":["request_records","users","tenants","api_keys"]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/data-lifecycle/data-init/preview", body)
	rec := httptest.NewRecorder()
	h.handleDataInitPreview(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp dataInitPreviewResponse
	dataInitJSON(t, rec, &resp)

	if len(resp.Categories) != 4 {
		t.Fatalf("expected 4 preview categories, got %d", len(resp.Categories))
	}
	if resp.ConfirmToken == "" {
		t.Fatal("preview must issue a confirm token")
	}
	if resp.TokenExpiresAt.Before(time.Now()) {
		t.Error("confirm token must expire in the future")
	}
	if resp.TotalTargetRows < 0 {
		t.Errorf("negative target rows: %d", resp.TotalTargetRows)
	}
	// ★ 受保护类别：users / tenants / api_keys 的保留行数必须 > 0
	//   （真库上确实有超级管理员 / default 租户 / 系统 key）。
	for _, pc := range resp.Categories {
		switch pc.Key {
		case "users", "tenants", "api_keys":
			if pc.ProtectedRows <= 0 {
				t.Errorf("category %s: expected protected rows > 0, got %d", pc.Key, pc.ProtectedRows)
			}
		}
	}
	t.Logf("preview over HTTP: target=%d protected=%d", resp.TotalTargetRows, resp.TotalProtected)
}

func TestDataInitPreviewHandler_RejectsUnknownCategory(t *testing.T) {
	pool := dataInitHTTPPool(t)
	h := NewHandler(pool, "", nil, nil)

	body := strings.NewReader(`{"categories":["not_a_category"]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/data-lifecycle/data-init/preview", body)
	rec := httptest.NewRecorder()
	h.handleDataInitPreview(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown category, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDataInitExecuteHandler_RejectsMissingToken(t *testing.T) {
	pool := dataInitHTTPPool(t)
	h := NewHandler(pool, "", nil, nil)

	body := strings.NewReader(`{"categories":["request_records"]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/data-lifecycle/data-init/execute", body)
	rec := httptest.NewRecorder()
	h.handleDataInitExecute(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without confirm token, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "DELETE") {
		t.Fatal("response must not leak SQL")
	}
}

func TestDataInitExecuteHandler_RejectsTokenOfOtherCategorySet(t *testing.T) {
	pool := dataInitHTTPPool(t)
	h := NewHandler(pool, "", nil, nil)

	// 先给 request_records 签一个令牌。
	plan, err := buildDataInitPlan([]string{"request_records"}, dataInitActor{Username: "tester", TenantID: "default"})
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	token, _, err := issueDataInitToken(plan, []string{"request_records"})
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	// 却拿它去执行 users ⇒ 必须被拒（且**不能真的删**）。
	payload, _ := json.Marshal(dataInitExecuteRequest{
		Categories:   []string{"users"},
		ConfirmToken: token,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/data-lifecycle/data-init/execute", strings.NewReader(string(payload)))
	rec := httptest.NewRecorder()
	h.handleDataInitExecute(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for cross-category token, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDataInitExecuteHandler_RejectsTamperedToken(t *testing.T) {
	pool := dataInitHTTPPool(t)
	h := NewHandler(pool, "", nil, nil)

	plan, err := buildDataInitPlan([]string{"request_records"}, dataInitActor{Username: "tester", TenantID: "default"})
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	token, _, err := issueDataInitToken(plan, []string{"request_records"})
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	rawPart, macPart, _ := strings.Cut(token, ".")
	tampered := "00" + rawPart[2:] + "." + macPart

	payload, _ := json.Marshal(dataInitExecuteRequest{
		Categories:   []string{"request_records"},
		ConfirmToken: tampered,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/data-lifecycle/data-init/execute", strings.NewReader(string(payload)))
	rec := httptest.NewRecorder()
	h.handleDataInitExecute(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for tampered token, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDataInitExecuteHandler_RejectsNonPostMethod(t *testing.T) {
	pool := dataInitHTTPPool(t)
	h := NewHandler(pool, "", nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/data-lifecycle/data-init/execute", nil)
	rec := httptest.NewRecorder()
	h.handleDataInitExecute(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}
