package ir

import "testing"

func TestGetProviderFieldConfig(t *testing.T) {
	tests := []struct {
		name           string
		catalogCode    string
		modelName      string
		expectedField  string
		description    string
	}{
		{
			name:          "MiniMax uses tool_call_id",
			catalogCode:   "minimax",
			modelName:     "",
			expectedField: "tool_call_id",
			description:   "MiniMax implements Anthropic-compatible API with tool_call_id",
		},
		{
			name:          "Standard Anthropic uses tool_use_id",
			catalogCode:   "anthropic",
			modelName:     "",
			expectedField: "tool_use_id",
			description:   "Standard Anthropic API uses tool_use_id",
		},
		{
			name:          "OpenAI defaults to tool_use_id",
			catalogCode:   "openai",
			modelName:     "",
			expectedField: "tool_use_id",
			description:   "OpenAI → Anthropic conversion uses standard tool_use_id",
		},
		{
			name:          "Zhipu defaults to tool_use_id",
			catalogCode:   "zhipu",
			modelName:     "",
			expectedField: "tool_use_id",
			description:   "Zhipu GLM uses standard tool_use_id",
		},
		{
			name:          "DeepSeek defaults to tool_use_id",
			catalogCode:   "deepseek",
			modelName:     "",
			expectedField: "tool_use_id",
			description:   "DeepSeek uses standard tool_use_id",
		},
		{
			name:          "Empty string defaults to tool_use_id",
			catalogCode:   "",
			modelName:     "",
			expectedField: "tool_use_id",
			description:   "Empty catalog code defaults to standard Anthropic",
		},
		{
			name:          "Unknown provider defaults to tool_use_id",
			catalogCode:   "unknown-provider",
			modelName:     "",
			expectedField: "tool_use_id",
			description:   "Unknown providers default to standard Anthropic",
		},
		// NVIDIA relay scenarios
		{
			name:          "NVIDIA with minimax model uses tool_call_id",
			catalogCode:   "nvidia",
			modelName:     "minimaxai/minimax-m3",
			expectedField: "tool_call_id",
			description:   "NVIDIA relaying to MiniMax should use tool_call_id",
		},
		{
			name:          "NVIDIA with MiniMax-M3 uses tool_call_id",
			catalogCode:   "nvidia",
			modelName:     "MiniMax-M3",
			expectedField: "tool_call_id",
			description:   "NVIDIA with MiniMax model name should use tool_call_id",
		},
		{
			name:          "NVIDIA with uppercase MINIMAXAI uses tool_call_id",
			catalogCode:   "nvidia",
			modelName:     "MINIMAXAI/MINIMAX-M3",
			expectedField: "tool_call_id",
			description:   "Case-insensitive matching for MiniMax models",
		},
		{
			name:          "NVIDIA with exact 'minimax' uses tool_call_id",
			catalogCode:   "nvidia",
			modelName:     "minimax",
			expectedField: "tool_call_id",
			description:   "Exact 'minimax' model name should match",
		},
		{
			name:          "NVIDIA with model containing minimax substring uses standard",
			catalogCode:   "nvidia",
			modelName:     "my-minimax-fork",
			expectedField: "tool_use_id",
			description:   "Substring match should not trigger, avoid false positives",
		},
		{
			name:          "NVIDIA with non-minimax model uses tool_use_id",
			catalogCode:   "nvidia",
			modelName:     "meta/llama-3",
			expectedField: "tool_use_id",
			description:   "NVIDIA relaying to other providers uses standard tool_use_id",
		},
		{
			name:          "NVIDIA with empty model defaults to tool_use_id",
			catalogCode:   "nvidia",
			modelName:     "",
			expectedField: "tool_use_id",
			description:   "NVIDIA without model name defaults to standard",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := GetProviderFieldConfig(tt.catalogCode, tt.modelName)
			if config.ToolResultIDField != tt.expectedField {
				t.Errorf("GetProviderFieldConfig(%q, %q).ToolResultIDField = %q, want %q\n  %s",
					tt.catalogCode, tt.modelName, config.ToolResultIDField, tt.expectedField, tt.description)
			}
		})
	}
}

func TestUsesToolCallID(t *testing.T) {
	tests := []struct {
		name        string
		catalogCode string
		modelName   string
		expected    bool
	}{
		{"minimax direct", "minimax", "", true},
		{"anthropic", "anthropic", "", false},
		{"openai", "openai", "", false},
		{"zhipu", "zhipu", "", false},
		{"deepseek", "deepseek", "", false},
		{"empty", "", "", false},
		{"unknown", "unknown", "", false},
		{"nvidia minimax relay", "nvidia", "minimaxai/minimax-m3", true},
		{"nvidia llama", "nvidia", "meta/llama-3", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := UsesToolCallID(tt.catalogCode, tt.modelName)
			if result != tt.expected {
				t.Errorf("UsesToolCallID(%q, %q) = %v, want %v", tt.catalogCode, tt.modelName, result, tt.expected)
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
		config := GetProviderFieldConfig(provider, "")
		if config.ToolResultIDField != "tool_use_id" {
			t.Errorf("BACKWARD COMPATIBILITY BROKEN: provider %q should use 'tool_use_id', got %q",
				provider, config.ToolResultIDField)
		}
	}
}

// TestProviderFieldMapping_MiniMaxSpecific ensures MiniMax mapping is correct.
func TestProviderFieldMapping_MiniMaxSpecific(t *testing.T) {
	config := GetProviderFieldConfig("minimax", "")
	if config.ToolResultIDField != "tool_call_id" {
		t.Errorf("MiniMax should use 'tool_call_id', got %q", config.ToolResultIDField)
	}

	if !UsesToolCallID("minimax", "") {
		t.Error("UsesToolCallID('minimax', '') should return true")
	}
}

// TestProviderFieldMapping_NVIDIARelay ensures NVIDIA relay detection works.
func TestProviderFieldMapping_NVIDIARelay(t *testing.T) {
	tests := []struct {
		modelName     string
		expectedField string
		description   string
	}{
		{"minimaxai/minimax-m3", "tool_call_id", "NVIDIA → MiniMax relay"},
		{"minimaxai/minimax-m2.7", "tool_call_id", "NVIDIA → MiniMax M2.7"},
		{"MiniMax-M3", "tool_call_id", "Model name starts with MiniMax"},
		{"MINIMAXAI/MINIMAX-M3", "tool_call_id", "Case-insensitive matching"},
		{"minimax", "tool_call_id", "Exact 'minimax' model name"},
		{"MiniMax", "tool_call_id", "Case-insensitive exact match"},
		{"my-minimax-fork", "tool_use_id", "Substring in middle (false positive avoided)"},
		{"minimaxlite", "tool_use_id", "Prefix without separator (false positive avoided)"},
		{"meta/llama-3", "tool_use_id", "NVIDIA → Meta (standard)"},
		{"anthropic/claude-3", "tool_use_id", "NVIDIA → Anthropic (standard)"},
		{"", "tool_use_id", "Empty model name (default)"},
	}

	for _, tt := range tests {
		t.Run(tt.modelName, func(t *testing.T) {
			config := GetProviderFieldConfig("nvidia", tt.modelName)
			if config.ToolResultIDField != tt.expectedField {
				t.Errorf("NVIDIA with model %q: expected %q, got %q (%s)",
					tt.modelName, tt.expectedField, config.ToolResultIDField, tt.description)
			}
		})
	}
}
