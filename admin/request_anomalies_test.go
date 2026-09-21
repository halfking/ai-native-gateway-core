package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/reqprobe"
)

// newRequestAnomalyTestHandler 构造带内存 reqprobe 存储的 handler、
// super_admin JWT 与注册了全部路由的 mux（走真实 superAdmin 鉴权链）。
func newRequestAnomalyTestServer(t *testing.T) (*Handler, *http.ServeMux, string) {
	t.Helper()
	h := NewHandler(nil, "test-secret", nil)
	coord := reqprobe.NewCoordinator(reqprobe.NewMemoryStore())
	h.SetRequestAnomalyStore(coord)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	token, _, err := SignToken(1, "default", "root", "super_admin", "test-secret", false)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return h, mux, token
}

func doAdminJSON(t *testing.T, mux *http.ServeMux, method, url, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, url, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, url, nil)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func TestRequestAnomaliesListCountResolveFlow(t *testing.T) {
	h, mux, token := newRequestAnomalyTestServer(t)
	defer h.requestAnomalies.Close()

	// 造两条数据（param_rejected 已恢复 2 次、mode_mismatch 未恢复）。
	store := h.requestAnomalies.Store()
	now := time.Now()
	mk := func(trigger reqprobe.Trigger, param, mode string, recovered int) reqprobe.Record {
		return reqprobe.Record{
			ProviderCode: "xai", OutboundModel: "grok-4.6", ClientModel: "grok-4.6",
			Protocol: "openai-chat", Trigger: trigger, Param: param, SuggestMode: mode,
			HTTPStatus: 400, ErrorKind: "client_bug", ErrorSample: "Invalid value for 'reasoning_effort'",
			Occurrences: 3, RecoveredCount: recovered,
			FirstSeen: now, LastSeen: now, Day: reqprobe.Today(now),
		}
	}
	if _, err := store.Upsert(context.Background(), mk(reqprobe.TriggerParamRejected, "reasoning_effort", "", 2)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Upsert(context.Background(), mk(reqprobe.TriggerModeMismatch, "", "chat", 0)); err != nil {
		t.Fatal(err)
	}

	// 未鉴权 → 401。
	req := httptest.NewRequest(http.MethodGet, "/api/admin/request-anomalies", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated code = %d", w.Code)
	}

	// 列表（带 trigger 过滤）。
	w = doAdminJSON(t, mux, http.MethodGet, "/api/admin/request-anomalies?trigger=param_rejected&unresolved_only=true", token, "")
	if w.Code != http.StatusOK {
		t.Fatalf("list code = %d body=%s", w.Code, w.Body.String())
	}
	var listResp struct {
		Anomalies []reqprobe.Record `json:"anomalies"`
		Count     int               `json:"count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listResp); err != nil {
		t.Fatal(err)
	}
	if listResp.Count != 1 || len(listResp.Anomalies) != 1 {
		t.Fatalf("count = %d items = %d", listResp.Count, len(listResp.Anomalies))
	}
	rec := listResp.Anomalies[0]
	if rec.Param != "reasoning_effort" || rec.Occurrences != 3 || rec.RecoveredCount != 2 {
		t.Fatalf("record = %+v", rec)
	}
	// 模式过滤另一条。
	w = doAdminJSON(t, mux, http.MethodGet, "/api/admin/request-anomalies?trigger=mode_mismatch", token, "")
	if w.Code != http.StatusOK {
		t.Fatalf("list code = %d", w.Code)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listResp); err != nil {
		t.Fatal(err)
	}
	if listResp.Count != 1 || listResp.Anomalies[0].SuggestMode != "chat" {
		t.Fatalf("mode filter count = %d", listResp.Count)
	}

	// 计数。
	w = doAdminJSON(t, mux, http.MethodGet, "/api/admin/request-anomalies/count", token, "")
	if w.Code != http.StatusOK {
		t.Fatalf("count code = %d", w.Code)
	}
	var counts reqprobe.Counts
	if err := json.Unmarshal(w.Body.Bytes(), &counts); err != nil {
		t.Fatal(err)
	}
	if counts.Unresolved != 2 || counts.NewToday != 2 {
		t.Fatalf("counts = %+v", counts)
	}

	// 单条解决。
	w = doAdminJSON(t, mux, http.MethodPost, "/api/admin/request-anomalies/1/resolve", token, `{"resolution_notes":"已加白名单"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("resolve code = %d body=%s", w.Code, w.Body.String())
	}
	counts, _ = store.Counts(context.Background())
	if counts.Unresolved != 1 {
		t.Fatalf("unresolved after resolve = %d", counts.Unresolved)
	}

	// 批量解决剩余全部。
	w = doAdminJSON(t, mux, http.MethodPost, "/api/admin/request-anomalies/batch-resolve", token, `{"all_unresolved":true,"resolution_notes":"批量处理"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("batch resolve code = %d body=%s", w.Code, w.Body.String())
	}
	var batchResp struct {
		Resolved int `json:"resolved"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &batchResp); err != nil {
		t.Fatal(err)
	}
	if batchResp.Resolved != 1 {
		t.Fatalf("batch resolved = %d", batchResp.Resolved)
	}
	counts, _ = store.Counts(context.Background())
	if counts.Unresolved != 0 {
		t.Fatalf("unresolved after batch = %d", counts.Unresolved)
	}

	// 按 ids 批量（空集报 400）。
	w = doAdminJSON(t, mux, http.MethodPost, "/api/admin/request-anomalies/batch-resolve", token, `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty batch code = %d", w.Code)
	}
}

func TestRequestAnomaliesWithoutStoreReturns503(t *testing.T) {
	h := NewHandler(nil, "test-secret", nil)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	token, _, err := SignToken(1, "default", "root", "super_admin", "test-secret", false)
	if err != nil {
		t.Fatal(err)
	}
	w := doAdminJSON(t, mux, http.MethodGet, "/api/admin/request-anomalies", token, "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", w.Code)
	}
}
