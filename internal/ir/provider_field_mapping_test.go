package ir

import "testing"

func TestGetProviderFieldConfig(t *testing.T) {
	tests := []struct {
		name           string
		catalogCode    string
		expectedField  string
		description    string
	}{
		{
			name:          "MiniMax uses tool_call_id",
			catalogCode:   "minimax",
			expectedField: "tool_call_id",
			description:   "MiniMax implements Anthropic-compatible API with tool_call_id",
		},
		{
			name:          "Standard Anthropic uses tool_use_id",
			catalogCode:   "anthropic",
			expectedField: "tool_use_id",
			description:   "Standard Anthropic API uses tool_use_id",
		},
		{
			name:          "OpenAI defaults to tool_use_id",
			catalogCode:   "openai",
			expectedField: "tool_use_id",
			description:   "OpenAI → Anthropic conversion uses standard tool_use_id",
		},
		{
			name:          "Zhipu defaults to tool_use_id",
			catalogCode:   "zhipu",
			expectedField: "tool_use_id",
			description:   "Zhipu GLM uses standard tool_use_id",
		},
		{
			name:          "DeepSeek defaults to tool_use_id",
			catalogCode:   "deepseek",
			expectedField: "tool_use_id",
			description:   "DeepSeek uses standard tool_use_id",
		},
		{
			name:          "Empty string defaults to tool_use_id",
			catalogCode:   "",
			expectedField: "tool_use_id",
			description:   "Empty catalog code defaults to standard Anthropic",
		},
		{
			name:          "Unknown provider defaults to tool_use_id",
			catalogCode:   "unknown-provider",
			expectedField: "tool_use_id",
			description:   "Unknown providers default to standard Anthropic",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := GetProviderFieldConfig(tt.catalogCode)
			if config.ToolResultIDField != tt.expectedField {
				t.Errorf("GetProviderFieldConfig(%q).ToolResultIDField = %q, want %q\n  %s",
					tt.catalogCode, config.ToolResultIDField, tt.expectedField, tt.description)
			}
		})
	}
}

func TestUsesToolCallID(t *testing.T) {
	tests := []struct {
		catalogCode string
		expected    bool
	}{
		{"minimax", true},
		{"anthropic", false},
		{"openai", false},
		{"zhipu", false},
		{"deepseek", false},
		{"", false},
		{"unknown", false},
	}

	for _, tt := range tests {
		t.Run(tt.catalogCode, func(t *testing.T) {
			result := UsesToolCallID(tt.catalogCode)
			if result != tt.expected {
				t.Errorf("UsesToolCallID(%q) = %v, want %v", tt.catalogCode, result, tt.expected)
			}
		})
	}
}

// TestProviderFieldMapping_BackwardCompatibility ensures that adding new
// provider mappings doesn't break existing providers.
func TestProviderFieldMapping_BackwardCompatibility(t *testing.T) {
	// These providers must always use standard tool_use_id
	standardProviders := []string{
		"anthropic",
		"openai",
		"zhipu",
		"deepseek",
		"volcengine",
		"baidu",
		"alibaba",
		"tencent",
		"", // empty = default
	}

	for _, provider := range standardProviders {
		config := GetProviderFieldConfig(provider)
		if config.ToolResultIDField != "tool_use_id" {
			t.Errorf("BACKWARD COMPATIBILITY BROKEN: provider %q should use 'tool_use_id', got %q",
				provider, config.ToolResultIDField)
		}
	}
}

// TestProviderFieldMapping_MiniMaxSpecific ensures MiniMax mapping is correct.
func TestProviderFieldMapping_MiniMaxSpecific(t *testing.T) {
	config := GetProviderFieldConfig("minimax")
	if config.ToolResultIDField != "tool_call_id" {
		t.Errorf("MiniMax should use 'tool_call_id', got %q", config.ToolResultIDField)
	}

	if !UsesToolCallID("minimax") {
		t.Error("UsesToolCallID('minimax') should return true")
	}
}
