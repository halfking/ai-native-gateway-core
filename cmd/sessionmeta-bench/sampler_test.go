package main

import (
	"strings"
	"testing"
)

func TestAnonymizeText(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "email",
			input: "Contact me at user@example.com for details",
			want:  "Contact me at [EMAIL] for details",
		},
		{
			name:  "phone_cn",
			input: "我的手机号是 13812345678",
			want:  "我的手机号是 [PHONE]",
		},
		{
			name:  "phone_intl",
			input: "Call +1-555-123-4567",
			want:  "Call [PHONE]",
		},
		{
			name:  "ip_address",
			input: "Server at 192.168.1.100",
			want:  "Server at [IP]",
		},
		{
			name:  "url_query",
			input: "https://example.com?token=abc123&user=john",
			want:  "https://example.com?[QUERY]?[QUERY]",
		},
		{
			name:  "bearer_token",
			input: "Authorization: Bearer sk-abc123def456",
			want:  "Authorization: Bearer [TOKEN]",
		},
		{
			name:  "file_path",
			input: "Read /Users/john/projects/secret/config.yaml",
			want:  "Read [FILE_PATH].yaml",
		},
		{
			name:  "connection_string",
			input: "postgres://user:pass@host:5432/db?sslmode=disable",
			want:  "[CONNECTION_STRING]",
		},
		{
			name:  "mixed",
			input: "Deploy to 10.0.1.5 using admin@corp.com with token Bearer xyz789",
			want:  "Deploy to [IP] using [EMAIL] with token Bearer [TOKEN]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := anonymizeText(tt.input)
			if got != tt.want {
				t.Errorf("anonymizeText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAnonymizeClientType(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Cursor IDE 0.45.0", "ide"},
		{"vscode-extension", "ide"},
		{"Web Browser/Chrome", "web"},
		{"API Client Go/1.21", "api"},
		{"unknown-client", "unknown"},
	}

	for _, tt := range tests {
		got := anonymizeClientType(tt.input)
		if got != tt.want {
			t.Errorf("anonymizeClientType(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestContainsCodeBlock(t *testing.T) {
	tests := []struct {
		user      string
		assistant string
		want      bool
	}{
		{
			user:      "Write a function:\n```go\nfunc main() {}\n```",
			assistant: "Here it is",
			want:      true,
		},
		{
			user:      "No code here",
			assistant: "Just plain text",
			want:      false,
		},
		{
			user:      "Check this:\n    func foo() {\n    }",
			assistant: "Indented code",
			want:      true,
		},
	}

	for _, tt := range tests {
		got := containsCodeBlock(tt.user, tt.assistant)
		if got != tt.want {
			t.Errorf("containsCodeBlock(%q, %q) = %v, want %v", tt.user, tt.assistant, got, tt.want)
		}
	}
}

func TestDetectLanguage(t *testing.T) {
	tests := []struct {
		name  string
		turns []TurnSample
		want  string
	}{
		{
			name: "chinese",
			turns: []TurnSample{
				{UserMessage: "你好，帮我写一个函数", AssistantMessage: "好的，这是代码"},
			},
			want: "zh",
		},
		{
			name: "english",
			turns: []TurnSample{
				{UserMessage: "Write me a function", AssistantMessage: "Here is the code"},
			},
			want: "en",
		},
		{
			name: "mixed",
			turns: []TurnSample{
				{UserMessage: "Write a function to handle 中文输入并返回结果", AssistantMessage: "Here is the implementation with proper 中文支持和边界检查"},
			},
			want: "mixed",
		},
		{
			name: "unknown",
			turns: []TurnSample{
				{UserMessage: "```code```", AssistantMessage: "123"},
			},
			want: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectLanguage(tt.turns)
			if got != tt.want {
				t.Errorf("detectLanguage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractUserMessage(t *testing.T) {
	reqJSON := `{
		"messages": [
			{"role": "system", "content": "You are helpful"},
			{"role": "user", "content": "Hello"},
			{"role": "assistant", "content": "Hi"},
			{"role": "user", "content": "How are you?"}
		]
	}`
	got := extractUserMessage(reqJSON)
	want := "How are you?"
	if got != want {
		t.Errorf("extractUserMessage() = %q, want %q", got, want)
	}
}

func TestExtractAssistantMessage(t *testing.T) {
	respJSON := `{
		"choices": [
			{"message": {"content": "I am fine, thank you!"}}
		]
	}`
	got := extractAssistantMessage(respJSON)
	want := "I am fine, thank you!"
	if got != want {
		t.Errorf("extractAssistantMessage() = %q, want %q", got, want)
	}
}

func TestAnonymizeText_NoSystemPromptLeak(t *testing.T) {
	input := "You are a helpful assistant. Bearer sk-12345"
	output := anonymizeText(input)
	if !strings.Contains(output, "[TOKEN]") {
		t.Errorf("Bearer token not anonymized: %q", output)
	}
	// System prompt text itself is not leaked by anonymization,
	// but extraction logic must filter it (tested separately)
}
