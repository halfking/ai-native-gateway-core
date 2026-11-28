package executors

import (
	"testing"

	providercatalog "github.com/kaixuan/llm-gateway-go/provider/catalog"
)

// TestIsProtocolUnsupportedError 验证协议不支持错误的判定逻辑。
func TestIsProtocolUnsupportedError(t *testing.T) {
	tests := []struct {
		name             string
		candidateProtocol string
		httpStatus       int
		body             string
		want             bool
	}{
		{
			name:             "anthropic-messages 404 with unsupported keyword",
			candidateProtocol: providercatalog.ProtocolAnthropicMessages,
			httpStatus:       404,
			body:             `{"error": "unsupported endpoint"}`,
			want:             true,
		},
		{
			name:             "gemini-generate 400 with not found",
			candidateProtocol: providercatalog.ProtocolGeminiGenerate,
			httpStatus:       400,
			body:             `{"error": {"message": "Model not found"}}`,
			want:             true,
		},
		{
			name:             "ollama-native 405 with method not allowed",
			candidateProtocol: providercatalog.ProtocolOllamaNative,
			httpStatus:       405,
			body:             `Method Not Allowed`,
			want:             true,
		},
		{
			name:             "base protocol should never trigger fallback",
			candidateProtocol: providercatalog.ProtocolOpenAICompletions,
			httpStatus:       404,
			body:             `{"error": "unsupported"}`,
			want:             false,
		},
		{
			name:             "wrong status code (500)",
			candidateProtocol: providercatalog.ProtocolAnthropicMessages,
			httpStatus:       500,
			body:             `{"error": "unsupported"}`,
			want:             false,
		},
		{
			name:             "parameter error should not trigger fallback",
			candidateProtocol: providercatalog.ProtocolAnthropicMessages,
			httpStatus:       400,
			body:             `{"error": "unsupported parameter: 'messages'"}`,
			want:             false,
		},
		{
			name:             "Chinese parameter error",
			candidateProtocol: providercatalog.ProtocolAnthropicMessages,
			httpStatus:       400,
			body:             `{"error": "不支持该参数 'stream_options'"}`,
			want:             false,
		},
		{
			name:             "empty body",
			candidateProtocol: providercatalog.ProtocolAnthropicMessages,
			httpStatus:       404,
			body:             "",
			want:             false,
		},
		{
			name:             "rate limit should not trigger fallback",
			candidateProtocol: providercatalog.ProtocolAnthropicMessages,
			httpStatus:       429,
			body:             `{"error": "rate limit exceeded"}`,
			want:             false,
		},
		{
			name:             "auth error should not trigger fallback",
			candidateProtocol: providercatalog.ProtocolAnthropicMessages,
			httpStatus:       401,
			body:             `{"error": "unauthorized"}`,
			want:             false,
		},
		{
			name:             "openai-responses 404 with unsupported (should trigger)",
			candidateProtocol: providercatalog.ProtocolOpenAIResponses,
			httpStatus:       404,
			body:             `{"error": "endpoint does not exist"}`,
			want:             true,
		},
		{
			name:             "case insensitive matching",
			candidateProtocol: providercatalog.ProtocolAnthropicMessages,
			httpStatus:       400,
			body:             `{"ERROR": "UNSUPPORTED OPERATION"}`,
			want:             true,
		},
		{
			name:             "route not found",
			candidateProtocol: providercatalog.ProtocolGeminiGenerate,
			httpStatus:       404,
			body:             `route not found`,
			want:             true,
		},
		{
			name:             "unknown model at endpoint",
			candidateProtocol: providercatalog.ProtocolAnthropicMessages,
			httpStatus:       400,
			body:             `{"error": "unknown model for this endpoint"}`,
			want:             true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isProtocolUnsupportedError(tt.candidateProtocol, tt.httpStatus, tt.body)
			if got != tt.want {
				t.Errorf("isProtocolUnsupportedError(%q, %d, %q) = %v, want %v",
					tt.candidateProtocol, tt.httpStatus, tt.body, got, tt.want)
			}
		})
	}
}

// TestIsProtocolUnsupportedError_AllProtocols 验证所有协议的基本行为。
func TestIsProtocolUnsupportedError_AllProtocols(t *testing.T) {
	protocols := []string{
		providercatalog.ProtocolOpenAICompletions,
		providercatalog.ProtocolOpenAIResponses,
		providercatalog.ProtocolAnthropicMessages,
		providercatalog.ProtocolGeminiGenerate,
		providercatalog.ProtocolOllamaNative,
	}

	body := `{"error": "unsupported"}`
	status := 404

	for _, proto := range protocols {
		got := isProtocolUnsupportedError(proto, status, body)
		// 只有基协议应该返回 false
		shouldBeFalse := proto == providercatalog.ProtocolOpenAICompletions
		if got == shouldBeFalse {
			t.Errorf("protocol %q: expected %v, got %v", proto, !shouldBeFalse, got)
		}
	}
}
