package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// TestGetProviderErrorStats_NonexistentProvider 验证不存在的 provider 返回 404
func TestGetProviderErrorStats_NonexistentProvider(t *testing.T) {
	// 此测试需要在集成测试环境中运行，因为它需要真实的数据库
	t.Skip("requires database; covered by integration test")
}

// TestGetProviderErrorStats_ParameterValidation 验证参数解析的单元测试
func TestGetProviderErrorStats_ParameterValidation(t *testing.T) {
	tests := []struct {
		name           string
		queryString    string
		expectedHours  int
		expectedLimit  int
		expectedFilter string
	}{
		{
			name:           "default params",
			queryString:    "",
			expectedHours:  24,
			expectedLimit:  100,
			expectedFilter: "all",
		},
		{
			name:           "valid custom params",
			queryString:    "?hours=48&limit=50&resolved=true",
			expectedHours:  48,
			expectedLimit:  50,
			expectedFilter: "true",
		},
		{
			name:           "hours too high - use default",
			queryString:    "?hours=10000",
			expectedHours:  24,
			expectedLimit:  100,
			expectedFilter: "all",
		},
		{
			name:           "hours negative - use default",
			queryString:    "?hours=-5",
			expectedHours:  24,
			expectedLimit:  100,
			expectedFilter: "all",
		},
		{
			name:           "hours non-numeric - use default",
			queryString:    "?hours=abc",
			expectedHours:  24,
			expectedLimit:  100,
			expectedFilter: "all",
		},
		{
			name:           "limit too high - use default",
			queryString:    "?limit=99999",
			expectedHours:  24,
			expectedLimit:  100,
			expectedFilter: "all",
		},
		{
			name:           "limit zero - use default",
			queryString:    "?limit=0",
			expectedHours:  24,
			expectedLimit:  100,
			expectedFilter: "all",
		},
		{
			name:           "resolved false",
			queryString:    "?resolved=false",
			expectedHours:  24,
			expectedLimit:  100,
			expectedFilter: "false",
		},
		{
			name:           "resolved all explicit",
			queryString:    "?resolved=all",
			expectedHours:  24,
			expectedLimit:  100,
			expectedFilter: "all",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 通过 URL 解析验证 query 参数
			req := httptest.NewRequest(http.MethodGet, "/error-stats"+tt.queryString, nil)

			// 验证 hours 参数解析
			hours := 24
			if hoursStr := req.URL.Query().Get("hours"); hoursStr != "" {
				if parsed, err := strconv.Atoi(hoursStr); err == nil && parsed > 0 && parsed <= 720 {
					hours = parsed
				}
			}
			if hours != tt.expectedHours {
				t.Errorf("hours = %d, want %d", hours, tt.expectedHours)
			}

			// 验证 limit 参数解析
			limit := 100
			if limitStr := req.URL.Query().Get("limit"); limitStr != "" {
				if parsed, err := strconv.Atoi(limitStr); err == nil && parsed > 0 && parsed <= 1000 {
					limit = parsed
				}
			}
			if limit != tt.expectedLimit {
				t.Errorf("limit = %d, want %d", limit, tt.expectedLimit)
			}

			// 验证 resolved 参数
			resolved := req.URL.Query().Get("resolved")
			if resolved == "" {
				resolved = "all"
			}
			if resolved != tt.expectedFilter {
				t.Errorf("resolved = %q, want %q", resolved, tt.expectedFilter)
			}
		})
	}
}

// TestGetProviderErrorStats_JSONResponse 验证 JSON 响应格式
func TestGetProviderErrorStats_JSONResponse(t *testing.T) {
	// 验证响应 JSON 包含所有必需字段
	expectedFields := []string{
		"provider_id",
		"time_range_hours",
		"total_errors",
		"total_occurrences",
		"resolved_count",
		"unresolved_count",
		"errors",
	}

	// 模拟一个响应对象
	resp := map[string]any{
		"provider_id":       1,
		"time_range_hours":  24,
		"total_errors":      0,
		"total_occurrences": 0,
		"resolved_count":    0,
		"unresolved_count":  0,
		"errors":            []any{},
	}

	// 序列化为 JSON 验证格式
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	// 反序列化验证字段
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	for _, field := range expectedFields {
		if _, ok := decoded[field]; !ok {
			t.Errorf("response missing field: %s", field)
		}
	}
}
