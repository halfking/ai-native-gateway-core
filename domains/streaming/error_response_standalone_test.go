package streaming_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/streaming"
)

// TestEnhancedErrorResponse_BasicError 测试基本错误响应
func TestEnhancedErrorResponse_BasicError(t *testing.T) {
	w := httptest.NewRecorder()
	
	streaming.WriteErrorJSONEnhanced(
		w,
		http.StatusServiceUnavailable,
		"req-123",
		"Service temporarily unavailable",
		"server_error",
		"service_unavailable",
		"gateway",
		nil,
		nil,
		"",
		nil,
	)
	
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", w.Code)
	}
	
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	
	errorObj, ok := resp["error"].(map[string]interface{})
	if !ok {
		t.Fatal("expected error object")
	}
	
	if errorObj["source"] != "gateway" {
		t.Errorf("expected source=gateway, got %v", errorObj["source"])
	}
	
	if resp["request_id"] != "req-123" {
		t.Errorf("expected request_id=req-123, got %v", resp["request_id"])
	}
}

// TestEnhancedErrorResponse_WithRetryInfo 测试包含重试信息的响应
func TestEnhancedErrorResponse_WithRetryInfo(t *testing.T) {
	w := httptest.NewRecorder()
	
	providerID := 7
	retryInfo := &streaming.RetryInfo{
		Retryable:    true,
		RetryAfter:   60,
		AttemptsMade: 2,
		MaxAttempts:  3,
	}
	
	streaming.WriteErrorJSONEnhanced(
		w,
		http.StatusTooManyRequests,
		"req-456",
		"Rate limit exceeded",
		"rate_limit_error",
		"rate_limit",
		"provider",
		nil,
		&providerID,
		"openai",
		retryInfo,
	)
	
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("expected status 429, got %d", w.Code)
	}
	
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	
	errorObj := resp["error"].(map[string]interface{})
	
	// 验证source
	if errorObj["source"] != "provider" {
		t.Errorf("expected source=provider, got %v", errorObj["source"])
	}
	
	// 验证供应商信息
	if errorObj["provider_id"].(float64) != 7 {
		t.Errorf("expected provider_id=7, got %v", errorObj["provider_id"])
	}
	
	if errorObj["provider_name"] != "openai" {
		t.Errorf("expected provider_name=openai, got %v", errorObj["provider_name"])
	}
	
	// 验证重试信息
	retryInfoObj := errorObj["retry_info"].(map[string]interface{})
	if !retryInfoObj["retryable"].(bool) {
		t.Error("expected retryable=true")
	}
	if retryInfoObj["retry_after"].(float64) != 60 {
		t.Errorf("expected retry_after=60, got %v", retryInfoObj["retry_after"])
	}
	if retryInfoObj["attempts_made"].(float64) != 2 {
		t.Errorf("expected attempts_made=2, got %v", retryInfoObj["attempts_made"])
	}
	
	// 验证响应头
	if w.Header().Get("X-Gateway-Failover") != "true" {
		t.Error("expected X-Gateway-Failover=true")
	}
	if w.Header().Get("X-Gateway-Attempts") != "2" {
		t.Errorf("expected X-Gateway-Attempts=2, got %s", w.Header().Get("X-Gateway-Attempts"))
	}
}

// TestExtractUpstreamErrorInfo_JSON 测试JSON格式上游错误提取
func TestExtractUpstreamErrorInfo_JSON(t *testing.T) {
	body := []byte(`{
		"error": {
			"type": "rate_limit_error",
			"message": "Rate limit exceeded",
			"code": "rate_limit_exceeded"
		}
	}`)
	
	info := streaming.ExtractUpstreamErrorInfo(nil, body, 429)
	
	if info == nil {
		t.Fatal("expected non-nil info")
	}
	
	if info.StatusCode != 429 {
		t.Errorf("status code mismatch: got %d, want 429", info.StatusCode)
	}
	
	if info.Type != "rate_limit_error" {
		t.Errorf("type mismatch: got %s, want rate_limit_error", info.Type)
	}
	
	if info.Message != "Rate limit exceeded" {
		t.Errorf("message mismatch: got %s", info.Message)
	}
	
	if info.Code != "rate_limit_exceeded" {
		t.Errorf("code mismatch: got %s", info.Code)
	}
}

// TestBackwardCompatibility 测试向后兼容性
func TestBackwardCompatibility(t *testing.T) {
	w := httptest.NewRecorder()
	
	streaming.WriteErrorJSONEnhanced(
		w,
		http.StatusBadRequest,
		"req-789",
		"Invalid request",
		"invalid_request_error",
		"invalid_parameter",
		"gateway",
		nil,
		nil,
		"",
		nil,
	)
	
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	
	errorObj := resp["error"].(map[string]interface{})
	
	// 验证基本字段存在（旧客户端依赖这些）
	requiredFields := []string{"type", "message", "code"}
	for _, field := range requiredFields {
		if _, exists := errorObj[field]; !exists {
			t.Errorf("missing required field: %s", field)
		}
	}
	
	// 新字段也应该存在
	if _, exists := errorObj["source"]; !exists {
		t.Error("missing source field")
	}
}
