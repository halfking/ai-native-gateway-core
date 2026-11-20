package streaming

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestWriteErrorJSONEnhanced_BasicError 测试基本错误响应
func TestWriteErrorJSONEnhanced_BasicError(t *testing.T) {
	w := httptest.NewRecorder()

	WriteErrorJSONEnhanced(
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

	var resp EnhancedErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp.Error.Source != "gateway" {
		t.Errorf("expected source=gateway, got %s", resp.Error.Source)
	}

	if resp.Error.UpstreamError != nil {
		t.Error("expected no upstream error")
	}

	if resp.RequestID != "req-123" {
		t.Errorf("expected request_id=req-123, got %s", resp.RequestID)
	}
}

// TestWriteErrorJSONEnhanced_WithUpstreamError 测试包含上游错误的响应
func TestWriteErrorJSONEnhanced_WithUpstreamError(t *testing.T) {
	w := httptest.NewRecorder()

	providerID := 7
	upstreamErr := &UpstreamErrorInfo{
		StatusCode: 429,
		Type:       "rate_limit_error",
		Message:    "Rate limit exceeded for model gpt-4",
		Code:       "rate_limit_exceeded",
		Body:       `{"error":{"message":"Rate limit exceeded","type":"rate_limit_error"}}`,
	}

	WriteErrorJSONEnhanced(
		w,
		http.StatusTooManyRequests,
		"req-456",
		"Rate limit exceeded",
		"rate_limit_error",
		"rate_limit",
		"provider",
		upstreamErr,
		&providerID,
		"openai",
		&RetryInfo{
			Retryable:    true,
			RetryAfter:   60,
			AttemptsMade: 2,
			MaxAttempts:  3,
		},
	)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("expected status 429, got %d", w.Code)
	}

	var resp EnhancedErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// 验证source
	if resp.Error.Source != "provider" {
		t.Errorf("expected source=provider, got %s", resp.Error.Source)
	}

	// 验证上游错误
	if resp.Error.UpstreamError == nil {
		t.Fatal("expected upstream error")
	}
	if resp.Error.UpstreamError.StatusCode != 429 {
		t.Errorf("expected upstream status 429, got %d", resp.Error.UpstreamError.StatusCode)
	}
	if resp.Error.UpstreamError.Message != "Rate limit exceeded for model gpt-4" {
		t.Errorf("upstream message mismatch: %s", resp.Error.UpstreamError.Message)
	}

	// 验证供应商信息
	if resp.Error.ProviderID == nil || *resp.Error.ProviderID != 7 {
		t.Errorf("expected provider_id=7, got %v", resp.Error.ProviderID)
	}
	if resp.Error.ProviderName != "openai" {
		t.Errorf("expected provider_name=openai, got %s", resp.Error.ProviderName)
	}

	// 验证重试信息
	if resp.Error.RetryInfo == nil {
		t.Fatal("expected retry info")
	}
	if !resp.Error.RetryInfo.Retryable {
		t.Error("expected retryable=true")
	}
	if resp.Error.RetryInfo.RetryAfter != 60 {
		t.Errorf("expected retry_after=60, got %d", resp.Error.RetryInfo.RetryAfter)
	}
	if resp.Error.RetryInfo.AttemptsMade != 2 {
		t.Errorf("expected attempts_made=2, got %d", resp.Error.RetryInfo.AttemptsMade)
	}

	// 验证响应头
	if w.Header().Get("X-Gateway-Failover") != "true" {
		t.Error("expected X-Gateway-Failover=true")
	}
	if w.Header().Get("X-Gateway-Attempts") != "2" {
		t.Errorf("expected X-Gateway-Attempts=2, got %s", w.Header().Get("X-Gateway-Attempts"))
	}
}

// TestExtractUpstreamErrorInfo 测试上游错误信息提取
func TestExtractUpstreamErrorInfo(t *testing.T) {
	tests := []struct {
		name       string
		body       []byte
		statusCode int
		wantType   string
		wantMsg    string
		wantCode   string
	}{
		{
			name: "standard error format",
			body: []byte(`{
				"error": {
					"type": "rate_limit_error",
					"message": "Rate limit exceeded",
					"code": "rate_limit_exceeded"
				}
			}`),
			statusCode: 429,
			wantType:   "rate_limit_error",
			wantMsg:    "Rate limit exceeded",
			wantCode:   "rate_limit_exceeded",
		},
		{
			name: "flat error format",
			body: []byte(`{
				"type": "auth_error",
				"message": "Invalid API key",
				"code": "invalid_key"
			}`),
			statusCode: 401,
			wantType:   "auth_error",
			wantMsg:    "Invalid API key",
			wantCode:   "invalid_key",
		},
		{
			name:       "empty body",
			body:       []byte{},
			statusCode: 500,
			wantType:   "",
			wantMsg:    "",
			wantCode:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := ExtractUpstreamErrorInfo(nil, tt.body, tt.statusCode)

			if len(tt.body) == 0 {
				if info != nil {
					t.Error("expected nil for empty body")
				}
				return
			}

			if info == nil {
				t.Fatal("expected non-nil info")
			}

			if info.StatusCode != tt.statusCode {
				t.Errorf("status code mismatch: got %d, want %d", info.StatusCode, tt.statusCode)
			}

			if info.Type != tt.wantType {
				t.Errorf("type mismatch: got %s, want %s", info.Type, tt.wantType)
			}

			if info.Message != tt.wantMsg {
				t.Errorf("message mismatch: got %s, want %s", info.Message, tt.wantMsg)
			}

			if info.Code != tt.wantCode {
				t.Errorf("code mismatch: got %s, want %s", info.Code, tt.wantCode)
			}
		})
	}
}

// TestExtractUpstreamErrorInfo_BodyTruncation 测试响应体截断
func TestExtractUpstreamErrorInfo_BodyTruncation(t *testing.T) {
	// 创建一个超过4KB的响应体
	largeBody := make([]byte, 5000)
	for i := range largeBody {
		largeBody[i] = 'x'
	}

	info := ExtractUpstreamErrorInfo(nil, largeBody, 500)

	if info == nil {
		t.Fatal("expected non-nil info")
	}

	// 验证被截断
	if len(info.Body) != 4096+len("... (truncated)") {
		t.Errorf("expected truncated body length %d, got %d", 4096+len("... (truncated)"), len(info.Body))
	}

	if !strings.HasSuffix(info.Body, "... (truncated)") {
		t.Error("expected body to end with truncation marker")
	}
}

// TestDetermineErrorSource 测试错误来源判断
func TestDetermineErrorSource(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantSource string
	}{
		{
			name:       "nil error",
			err:        nil,
			wantSource: "gateway",
		},
		{
			name:       "non-execute error",
			err:        errors.New("some error"),
			wantSource: "gateway",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := determineErrorSource(tt.err)
			if source != tt.wantSource {
				t.Errorf("expected source=%s, got %s", tt.wantSource, source)
			}
		})
	}
}

// TestResponseFormat_BackwardCompatibility 测试响应格式向后兼容性
func TestResponseFormat_BackwardCompatibility(t *testing.T) {
	w := httptest.NewRecorder()

	// 旧格式客户端只需要基本字段
	WriteErrorJSONEnhanced(
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

	// 验证基本字段存在
	errorObj, ok := resp["error"].(map[string]interface{})
	if !ok {
		t.Fatal("expected error object")
	}

	requiredFields := []string{"type", "message", "code"}
	for _, field := range requiredFields {
		if _, exists := errorObj[field]; !exists {
			t.Errorf("missing required field: %s", field)
		}
	}

	// 新字段应该存在但可选
	if _, exists := errorObj["source"]; !exists {
		t.Error("missing source field")
	}

	// 当没有上游错误时，这些字段应该不存在或为null
	if val, exists := errorObj["upstream_error"]; exists && val != nil {
		t.Error("unexpected upstream_error field")
	}
}
