// storage_metrics_endpoint_test.go 校验 GET /metrics/storage：
// 用真实注册函数 / handler 挂到最小 ServeMux 上以 httptest 驱动，
// 覆盖 lite 模式（file_cache 有值）、full 模式（file_cache 为 null）
// 与 admin token 鉴权接线，不启动整个网关。
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	v2 "github.com/kaixuan/llm-gateway-go/domains/session/v2"
	"github.com/kaixuan/llm-gateway-go/monitoring"
)

const testStorageAdminToken = "test-storage-admin-token"

// storageEndpointBody 端点响应契约（file_cache 用指针区分 null 与空对象）。
type storageEndpointBody struct {
	Storage   map[string]interface{}  `json:"storage"`
	FileCache *map[string]interface{} `json:"file_cache"`
	Mode      string                  `json:"mode"`
}

// serveStorageMetrics 用指定 runtime 与 token 注册到最小 mux 并发起 GET。
func serveStorageMetrics(t *testing.T, rt *storageRuntime, token, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	registerStorageMetricsHandler(mux, rt, token)
	req := httptest.NewRequest(http.MethodGet, "/metrics/storage", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestStorageMetricsEndpoint_LiteMode(t *testing.T) {
	// 真实 FileCache（lite 模式的 L1.5），Stats() 应出现在 file_cache。
	fc, err := v2.NewFileCache(t.TempDir(), time.Hour, 1<<30)
	if err != nil {
		t.Fatalf("创建 FileCache: %v", err)
	}
	rt := &storageRuntime{fileCache: fc, mode: "lite"}

	// 直接构造 handler 注入独立 StorageMetrics，断言精确数值
	// （注册函数走 Default() 单例，跨测试状态会串扰，故数值断言放此处）。
	m := monitoring.NewStorageMetrics()
	m.RecordL1Hit()
	m.RecordL1Hit()
	m.RecordL1Miss()

	handler := newStorageMetricsHandler(rt, m)
	req := httptest.NewRequest(http.MethodGet, "/metrics/storage", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	var body storageEndpointBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v; body: %s", err, rec.Body.String())
	}
	if body.Mode != "lite" {
		t.Errorf("mode = %q, want lite", body.Mode)
	}
	// storage 快照结构与数值（数值来自注入实例）。
	l1, ok := body.Storage["l1"].(map[string]interface{})
	if !ok {
		t.Fatalf("storage.l1 缺失或类型错误: %v", body.Storage["l1"])
	}
	// 注意：JSON 反序列化后数值统一为 float64。
	if got := l1["hits"].(float64); got != 2 {
		t.Errorf("storage.l1.hits = %v, want 2", l1["hits"])
	}
	if got := l1["hit_rate"].(float64); got < 0.66-1e-9 || got > 0.667+1e-9 {
		t.Errorf("storage.l1.hit_rate = %v, want 2/3", got)
	}
	if _, ok := body.Storage["l1_5"]; !ok {
		t.Error("storage.l1_5 缺失")
	}
	if _, ok := body.Storage["l2"]; !ok {
		t.Error("storage.l2 缺失")
	}
	if _, ok := body.Storage["l3"]; !ok {
		t.Error("storage.l3 缺失")
	}
	if _, ok := body.Storage["writes"]; !ok {
		t.Error("storage.writes 缺失")
	}
	// file_cache：lite 模式非 null，含 FileCache.Stats() 的字段。
	if body.FileCache == nil {
		t.Fatal("file_cache = null, want FileCache.Stats()（lite 模式）")
	}
	if _, ok := (*body.FileCache)["size_used_bytes"]; !ok {
		t.Errorf("file_cache.size_used_bytes 缺失: %v", *body.FileCache)
	}
	if _, ok := (*body.FileCache)["max_size_bytes"]; !ok {
		t.Errorf("file_cache.max_size_bytes 缺失: %v", *body.FileCache)
	}
}

func TestStorageMetricsEndpoint_FullModeNullFileCache(t *testing.T) {
	// rt == nil（full / 未启用双模式）：端点仍可用，mode=full、file_cache=null。
	rec := serveStorageMetrics(t, nil, testStorageAdminToken, "Bearer "+testStorageAdminToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body storageEndpointBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v; body: %s", err, rec.Body.String())
	}
	if body.Mode != "full" {
		t.Errorf("mode = %q, want full", body.Mode)
	}
	if body.FileCache != nil {
		t.Errorf("file_cache = %v, want null（full 模式无 L1.5 文件缓存）", *body.FileCache)
	}
	if body.Storage == nil {
		t.Fatal("storage 缺失, want 零值快照")
	}
	l3, ok := body.Storage["l3"].(map[string]interface{})
	if !ok {
		t.Fatalf("storage.l3 缺失或类型错误: %v", body.Storage["l3"])
	}
	if got := l3["avg_latency_ms"].(float64); got != 0 {
		t.Errorf("storage.l3.avg_latency_ms = %v, want 0（零值快照）", got)
	}
}

func TestStorageMetricsEndpoint_AdminTokenAuth(t *testing.T) {
	rt := &storageRuntime{mode: "lite"} // 无 fileCache 也应安全（file_cache=null）

	// 无 token → 401（与 /metrics 相同的 AdminTokenMiddleware 接线）。
	rec := serveStorageMetrics(t, rt, testStorageAdminToken, "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("无 token status = %d, want %d; body: %s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}

	// 错误 token → 401。
	rec = serveStorageMetrics(t, rt, testStorageAdminToken, "Bearer wrong-token")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("错误 token status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	// 正确 token → 200；runtime.mode 为空时兜底上报 "full"。
	rec = serveStorageMetrics(t, rt, testStorageAdminToken, "Bearer "+testStorageAdminToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("正确 token status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body storageEndpointBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v; body: %s", err, rec.Body.String())
	}
	if body.Mode != "lite" {
		t.Errorf("mode = %q, want lite（runtime.mode 显式设置）", body.Mode)
	}
	if body.FileCache != nil {
		t.Errorf("file_cache = %v, want null（runtime 无 fileCache）", *body.FileCache)
	}
}

func TestStorageMetricsEndpoint_MethodNotAllowed(t *testing.T) {
	mux := http.NewServeMux()
	registerStorageMetricsHandler(mux, nil, testStorageAdminToken)

	// "GET /metrics/storage" 模式路由：非 GET 由 ServeMux 返回 405。
	req := httptest.NewRequest(http.MethodPost, "/metrics/storage", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}
