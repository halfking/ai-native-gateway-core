package admin

import (
	"testing"
)

func TestDetectIDESource(t *testing.T) {
	gen := &AutoTitleGenerator{}

	tests := []struct {
		name     string
		preview  string
		expected string
	}{
		{
			name:     "ZCode IDE",
			preview:  "[system]\nYou are ZCode, an interactive coding agent",
			expected: "[ZCode]",
		},
		{
			name:     "ZooCode IDE",
			preview:  "[system]\nYou are Zoo, a helpful assistant",
			expected: "[ZooCode]",
		},
		{
			name:     "Cursor IDE",
			preview:  "You are Cursor, an AI coding assistant",
			expected: "[Cursor]",
		},
		{
			name:     "OpenCode IDE",
			preview:  "You are OpenCode, the best coding agent",
			expected: "[OpenCode]",
		},
		{
			name:     "No IDE detected",
			preview:  "Write a function to reverse a string",
			expected: "",
		},
		{
			name:     "Case insensitive",
			preview:  "YOU ARE ZCODE, AN INTERACTIVE CODING AGENT",
			expected: "[ZCode]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := gen.detectIDESource(tt.preview)
			if result != tt.expected {
				t.Errorf("detectIDESource(%q) = %q, want %q", tt.preview, result, tt.expected)
			}
		})
	}
}

func TestExtractUserPrompt(t *testing.T) {
	gen := &AutoTitleGenerator{}

	tests := []struct {
		name     string
		preview  string
		expected string
	}{
		{
			name: "With system and user messages",
			preview: `[system]
You are ZCode, an interactive coding agent
[user]
Write a function to calculate fibonacci numbers`,
			expected: "Write a function to calculate fibonacci numbers",
		},
		{
			name: "User prefix with colon",
			preview: `system: You are a helpful assistant
user: Explain how binary search works`,
			expected: "Explain how binary search works",
		},
		{
			name: "No prefixes",
			preview: `You are ZCode
Write unit tests for a login function`,
			expected: "Write unit tests for a login function",
		},
		{
			name:     "Only system messages",
			preview:  "[system]\nYou are a helpful assistant\nYour task is to help users",
			expected: "",
		},
		{
			name:     "Short line skipped",
			preview:  "You are ZCode\nOK\nImplement a REST API for user management",
			expected: "Implement a REST API for user management",
		},
		{
			name: "Long prompt truncated",
			preview: `[user]
This is a very long user prompt that exceeds sixty characters and should be truncated properly`,
			expected: "This is a very long user prompt that exceeds sixty…",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := gen.extractUserPrompt(tt.preview)
			if result != tt.expected {
				t.Errorf("extractUserPrompt() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestExtractTitleFromPreview(t *testing.T) {
	gen := &AutoTitleGenerator{}

	tests := []struct {
		name    string
		preview string
		wantIDE bool // Should contain IDE prefix
		minLen  int  // Minimum expected length
		maxLen  int  // Maximum expected length
	}{
		{
			name: "ZCode with user prompt",
			preview: `[system]
You are ZCode, an interactive coding agent
[user]
Write a function to reverse a string`,
			wantIDE: true,
			minLen:  20,
			maxLen:  80,
		},
		{
			name: "Cursor with long prompt",
			preview: `You are Cursor, an AI coding assistant.
[user]
I need help implementing a complex authentication system with OAuth2, JWT tokens, and refresh token rotation`,
			wantIDE: true,
			minLen:  20,
			maxLen:  80,
		},
		{
			name:    "No IDE, direct prompt",
			preview: `Create a React component for a user profile page`,
			wantIDE: false,
			minLen:  20,
			maxLen:  80,
		},
		{
			name:    "Empty preview",
			preview: "",
			wantIDE: false,
			minLen:  0,
			maxLen:  0,
		},
		{
			name: "ZooCode with Chinese prompt",
			preview: `[system]
You are Zoo, a helpful assistant
[user]
帮我写一个计算斐波那契数列的函数`,
			wantIDE: true,
			minLen:  15,
			maxLen:  80,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := gen.extractTitleFromPreview(tt.preview)

			if tt.maxLen == 0 {
				if result != "" {
					t.Errorf("extractTitleFromPreview() = %q, want empty string", result)
				}
				return
			}

			if len(result) < tt.minLen {
				t.Errorf("extractTitleFromPreview() length = %d, want >= %d (result: %q)",
					len(result), tt.minLen, result)
			}
			if len(result) > tt.maxLen {
				t.Errorf("extractTitleFromPreview() length = %d, want <= %d (result: %q)",
					len(result), tt.maxLen, result)
			}

			if tt.wantIDE {
				hasIDEPrefix := false
				idePrefixes := []string{"[ZCode]", "[Cursor]", "[ZooCode]", "[OpenCode]"}
				for _, prefix := range idePrefixes {
					if len(result) >= len(prefix) && result[:len(prefix)] == prefix {
						hasIDEPrefix = true
						break
					}
				}
				if !hasIDEPrefix {
					t.Errorf("extractTitleFromPreview() = %q, expected IDE prefix", result)
				}
			}
		})
	}
}
