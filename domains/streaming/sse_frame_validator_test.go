package streaming

import (
	"testing"
)

func TestValidateSSEDataFrame(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		valid bool
	}{
		{
			name:  "valid OpenAI chunk",
			line:  `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","choices":[{"delta":{"content":"hello"}}]}`,
			valid: true,
		},
		{
			name:  "valid Anthropic event",
			line:  `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
			valid: true,
		},
		{
			name:  "DONE marker",
			line:  "data: [DONE]",
			valid: true,
		},
		{
			name:  "empty payload",
			line:  "data: ",
			valid: true,
		},
		{
			name:  "incomplete JSON - bare brace",
			line:  "data: {",
			valid: false,
		},
		{
			name:  "incomplete JSON - unclosed object",
			line:  `data: {"id":"123"`,
			valid: false,
		},
		{
			name:  "incomplete JSON - trailing comma",
			line:  `data: {"id":"123",}`,
			valid: false,
		},
		{
			name:  "malformed JSON - unquoted key",
			line:  `data: {id:"123"}`,
			valid: false,
		},
		{
			name:  "bare text without structure",
			line:  "data: hello world",
			valid: false,
		},
		{
			name:  "valid error object",
			line:  `data: {"error":{"message":"quota exceeded","type":"insufficient_quota"}}`,
			valid: true,
		},
		{
			name:  "event line (not data)",
			line:  "event: ping",
			valid: true, // non-data lines are considered valid (ignored by extractPayload)
		},
		{
			name:  "comment line",
			line:  ": keepalive",
			valid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validateSSEDataFrame(tt.line)
			if got != tt.valid {
				t.Errorf("validateSSEDataFrame(%q) = %v, want %v", tt.line, got, tt.valid)
			}
		})
	}
}

func TestIsRecoverableInvalidFrame(t *testing.T) {
	tests := []struct {
		name        string
		line        string
		recoverable bool
	}{
		{
			name:        "bare opening brace",
			line:        "data: {",
			recoverable: true,
		},
		{
			name:        "bare closing brace",
			line:        "data: }",
			recoverable: true,
		},
		{
			name:        "single character",
			line:        "data: x",
			recoverable: true,
		},
		{
			name:        "truncated JSON",
			line:        `data: {"id":"abc`,
			recoverable: true,
		},
		{
			name:        "valid chunk should not recover",
			line:        `data: {"choices":[]}`,
			recoverable: false,
		},
		{
			name:        "valid error should not recover",
			line:        `data: {"error":{"message":"test"}}`,
			recoverable: false,
		},
		{
			name:        "DONE marker should not recover",
			line:        "data: [DONE]",
			recoverable: false,
		},
		{
			name:        "empty payload should not recover",
			line:        "data: ",
			recoverable: false,
		},
		{
			name:        "bare text (likely corrupt)",
			line:        "data: random text here",
			recoverable: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRecoverableInvalidFrame(tt.line)
			if got != tt.recoverable {
				t.Errorf("isRecoverableInvalidFrame(%q) = %v, want %v", tt.line, got, tt.recoverable)
			}
		})
	}
}
