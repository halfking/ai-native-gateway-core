package streaming

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// 测试自适应响应格式转换器的完整场景

func TestResponseFormatPreference_SessionMemory(t *testing.T) {
	prefs := NewResponseFormatPreference()

	sessionID := "test-session-123"

	// 初始没有偏好
	if _, ok := prefs.Get(sessionID); ok {
		t.Error("should not have preference initially")
	}

	// 设置偏好
	prefs.Set(sessionID, "openai-chat")

	// 获取偏好
	if pref, ok := prefs.Get(sessionID); !ok || pref != "openai-chat" {
		t.Errorf("expected openai-chat, got %q", pref)
	}

	// 更新偏好
	prefs.Set(sessionID, "anthropic-messages")
	if pref, ok := prefs.Get(sessionID); !ok || pref != "anthropic-messages" {
		t.Errorf("expected anthropic-messages after update, got %q", pref)
	}

	// 删除偏好
	prefs.Delete(sessionID)
	if _, ok := prefs.Get(sessionID); ok {
		t.Error("should not have preference after delete")
	}
}

func TestAdaptiveResponseConverter_OpenAIToAnthropic(t *testing.T) {
	prefs := NewResponseFormatPreference()
	mockIR := &mockIRConverter{}
	arc := NewAdaptiveResponseConverter(prefs, mockIR)

	sessionID := "session-456"
	clientProtocol := "openai-chat"
	upstreamBody := []byte(`{"content":[{"type":"text","text":"Hello"}],"role":"assistant","stop_reason":"end_turn"}`)
	upstreamProtocol := "anthropic-messages"
	clientModel := "claude-sonnet-5"

	// 第一次请求 - 没有偏好,使用检测到的客户端协议
	body, format, err := arc.ConvertResponse(context.TODO(), sessionID, clientProtocol, upstreamBody, upstreamProtocol, clientModel)
	if err != nil {
		t.Fatalf("ConvertResponse failed: %v", err)
	}

	if format != "openai-chat" {
		t.Errorf("expected format openai-chat, got %q", format)
	}

	if len(body) == 0 {
		t.Error("response body is empty")
	}

	// 验证偏好已记录
	if pref, ok := prefs.Get(sessionID); !ok || pref != "openai-chat" {
		t.Errorf("expected preference to be set to openai-chat, got %q", pref)
	}

	// 第二次请求 - 使用记录的偏好
	body2, format2, err := arc.ConvertResponse(context.TODO(), sessionID, clientProtocol, upstreamBody, upstreamProtocol, clientModel)
	if err != nil {
		t.Fatalf("second ConvertResponse failed: %v", err)
	}

	if format2 != "openai-chat" {
		t.Errorf("expected format openai-chat on second call, got %q", format2)
	}

	if len(body2) == 0 {
		t.Error("second response body is empty")
	}
}

func TestAdaptiveResponseConverter_FormatFallback(t *testing.T) {
	prefs := NewResponseFormatPreference()
	mockIR := &mockIRConverter{
		failOpenAI:    true, // 模拟 OpenAI 格式失败
		failResponses: true, // 模拟 Responses 格式失败
	}
	arc := NewAdaptiveResponseConverter(prefs, mockIR)

	sessionID := "session-fallback"
	clientProtocol := "openai-chat"
	upstreamBody := []byte(`{"content":[{"type":"text","text":"Test"}],"role":"assistant","stop_reason":"end_turn"}`)
	upstreamProtocol := "anthropic-messages"
	clientModel := "claude-opus-4-8"

	// OpenAI 和 Responses 格式都失败,应该回退到 Anthropic
	body, format, err := arc.ConvertResponse(context.TODO(), sessionID, clientProtocol, upstreamBody, upstreamProtocol, clientModel)
	if err != nil {
		t.Fatalf("ConvertResponse failed: %v", err)
	}

	if format != "anthropic-messages" {
		t.Errorf("expected fallback to anthropic-messages, got %q", format)
	}

	if len(body) == 0 {
		t.Error("response body is empty")
	}

	// 验证记录了 anthropic-messages 偏好
	if pref, ok := prefs.Get(sessionID); !ok || pref != "anthropic-messages" {
		t.Errorf("expected preference anthropic-messages, got %q", pref)
	}
}

func TestAdaptiveResponseConverter_AllFormatsFail(t *testing.T) {
	prefs := NewResponseFormatPreference()
	mockIR := &mockIRConverter{
		failOpenAI:    true,
		failAnthropic: true,
		failResponses: true,
		failParse:     true,
	}
	arc := NewAdaptiveResponseConverter(prefs, mockIR)

	sessionID := "session-all-fail"
	clientProtocol := "openai-chat"
	upstreamBody := []byte(`{"content":[{"type":"text","text":"test"}]}`)
	upstreamProtocol := "openai-completions" // 改为 openai,这样就不会直接返回 upstreamBody
	clientModel := "claude-sonnet-5"

	_, _, err := arc.ConvertResponse(context.TODO(), sessionID, clientProtocol, upstreamBody, upstreamProtocol, clientModel)
	if err == nil {
		t.Error("expected error when all formats fail")
	} else {
		t.Logf("✓ Error as expected: %v", err)
	}

	// 不应该记录偏好
	if _, ok := prefs.Get(sessionID); ok {
		t.Error("should not set preference when all formats fail")
	}
}

func TestValidateResponseFormat(t *testing.T) {
	tests := []struct {
		name           string
		body           string
		expectedFormat string
		expectError    bool
	}{
		{
			name:           "valid OpenAI format",
			body:           `{"choices":[{"message":{"content":"test"}}]}`,
			expectedFormat: "openai-chat",
			expectError:    false,
		},
		{
			name:           "valid Anthropic format",
			body:           `{"content":[{"type":"text","text":"test"}]}`,
			expectedFormat: "anthropic-messages",
			expectError:    false,
		},
		{
			name:           "valid Responses format",
			body:           `{"output_text":{"delta":"test"}}`,
			expectedFormat: "openai-responses",
			expectError:    false,
		},
		{
			name:           "invalid OpenAI format - missing choices",
			body:           `{"message":"test"}`,
			expectedFormat: "openai-chat",
			expectError:    true,
		},
		{
			name:           "invalid Anthropic format - missing content",
			body:           `{"role":"assistant"}`,
			expectedFormat: "anthropic-messages",
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateResponseFormat([]byte(tt.body), tt.expectedFormat)
			if tt.expectError && err == nil {
				t.Error("expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// mockIRConverter 模拟 IR 转换器
type mockIRConverter struct {
	failOpenAI    bool
	failAnthropic bool
	failResponses bool
	failParse     bool
}

func (m *mockIRConverter) ParseOpenAI(body []byte) (*ir.InternalRequest, error) {
	return &ir.InternalRequest{}, nil
}

func (m *mockIRConverter) ParseAnthropic(body []byte) (*ir.InternalRequest, error) {
	return &ir.InternalRequest{}, nil
}

func (m *mockIRConverter) SerializeOpenAI(req *ir.InternalRequest) ([]byte, error) {
	return []byte(`{"model":"test","messages":[]}`), nil
}

func (m *mockIRConverter) SerializeAnthropic(req *ir.InternalRequest) ([]byte, error) {
	return []byte(`{"model":"test","messages":[],"max_tokens":100}`), nil
}

func (m *mockIRConverter) ParseAnthropicResponse(body []byte) (*ir.InternalResponse, error) {
	if m.failParse || m.failAnthropic {
		return nil, &mockError{msg: "parse anthropic failed"}
	}
	return &ir.InternalResponse{
		Role: "assistant",
		Content: []ir.ResponseContentBlock{
			{Type: "text", Text: "Hello from Anthropic"},
		},
	}, nil
}

func (m *mockIRConverter) ParseOpenAIResponse(body []byte) (*ir.InternalResponse, error) {
	if m.failParse {
		return nil, &mockError{msg: "parse openai failed"}
	}
	return &ir.InternalResponse{
		Role: "assistant",
		Content: []ir.ResponseContentBlock{
			{Type: "text", Text: "Hello from OpenAI"},
		},
	}, nil
}

func (m *mockIRConverter) SerializeOpenAIResponse(irResp *ir.InternalResponse, clientModel string) ([]byte, error) {
	if m.failOpenAI || irResp == nil {
		return nil, &mockError{msg: "openai serialization failed"}
	}
	return []byte(`{"choices":[{"message":{"role":"assistant","content":"test"}}]}`), nil
}

func (m *mockIRConverter) SerializeAnthropicResponse(irResp *ir.InternalResponse, clientModel string) ([]byte, error) {
	if m.failAnthropic {
		return nil, &mockError{msg: "anthropic serialization failed"}
	}
	if irResp == nil {
		return nil, &mockError{msg: "nil irResp"}
	}
	return []byte(`{"content":[{"type":"text","text":"test"}],"role":"assistant","stop_reason":"end_turn"}`), nil
}

func (m *mockIRConverter) SerializeResponsesResponse(irResp *ir.InternalResponse, clientModel string) ([]byte, error) {
	if m.failResponses || irResp == nil {
		return nil, &mockError{msg: "responses serialization failed"}
	}
	return []byte(`{"output_text":{"delta":"test"}}`), nil
}

type mockError struct {
	msg string
}

func (e *mockError) Error() string {
	return e.msg
}
