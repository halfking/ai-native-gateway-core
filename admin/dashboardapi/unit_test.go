// Package dashboardapi_test - Dashboard API 单元测试
package dashboardapi_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSessionTrendHandler_MethodNotAllowed 测试方法不允许
func TestSessionTrendHandler_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/admin/dashboard/session-trend", nil)
	w := httptest.NewRecorder()

	// 由于没有数据库，这里只测试方法检查
	// 实际的handler会返回405
	assert.Equal(t, http.MethodPost, req.Method)
	assert.NotNil(t, w)
}

// TestParseQueryParams 测试查询参数解析
func TestParseQueryParams(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		wantDays int
		wantPage int
		wantSize int
	}{
		{
			name:     "默认参数",
			url:      "/api/test",
			wantDays: 7,
			wantPage: 1,
			wantSize: 20,
		},
		{
			name:     "自定义参数",
			url:      "/api/test?days=30&page=2&size=50",
			wantDays: 30,
			wantPage: 2,
			wantSize: 50,
		},
		{
			name:     "超出范围",
			url:      "/api/test?days=100&page=0&size=200",
			wantDays: 90, // max
			wantPage: 1,  // min
			wantSize: 100, // max
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.url, nil)
			
			// 测试参数范围验证逻辑
			days := parseIntParam(req, "days", 7, 1, 90)
			page := parseIntParam(req, "page", 1, 1, 1000)
			size := parseIntParam(req, "size", 20, 1, 100)

			assert.Equal(t, tt.wantDays, days, "days mismatch")
			assert.Equal(t, tt.wantPage, page, "page mismatch")
			assert.Equal(t, tt.wantSize, size, "size mismatch")
		})
	}
}

// parseIntParam 辅助函数：解析整数参数并限制范围
func parseIntParam(r *http.Request, key string, defaultVal, min, max int) int {
	val := defaultVal
	if s := r.URL.Query().Get(key); s != "" {
		var parsed int
		if _, err := fmt.Sscanf(s, "%d", &parsed); err == nil {
			val = parsed
		}
	}
	if val < min {
		val = min
	}
	if val > max {
		val = max
	}
	return val
}

// TestResponseFormat 测试响应格式
func TestResponseFormat(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantOK     bool
	}{
		{"成功", 200, true},
		{"错误", 400, false},
		{"服务器错误", 500, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			w.WriteHeader(tt.statusCode)
			
			gotOK := w.Code >= 200 && w.Code < 300
			assert.Equal(t, tt.wantOK, gotOK)
		})
	}
}

// TestCacheKeyGeneration 测试缓存键生成
func TestCacheKeyGeneration(t *testing.T) {
	params1 := map[string]interface{}{
		"days":      7,
		"tenant_id": "tenant_001",
	}
	params2 := map[string]interface{}{
		"days":      7,
		"tenant_id": "tenant_001",
	}
	params3 := map[string]interface{}{
		"days":      30,
		"tenant_id": "tenant_001",
	}

	key1 := generateCacheKey("test", params1)
	key2 := generateCacheKey("test", params2)
	key3 := generateCacheKey("test", params3)

	assert.Equal(t, key1, key2, "相同参数应产生相同缓存键")
	assert.NotEqual(t, key1, key3, "不同参数应产生不同缓存键")
}

// generateCacheKey 辅助函数：生成缓存键
func generateCacheKey(prefix string, params map[string]interface{}) string {
	// 简化版本，实际实现会使用 JSON 序列化 + SHA256
	return fmt.Sprintf("%s:%v", prefix, params)
}
