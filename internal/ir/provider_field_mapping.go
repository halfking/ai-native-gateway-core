package ir

import "strings"

// ProviderFieldMapping defines provider-specific field name mappings for
// protocol variants. This ensures compatibility across different providers
// that implement similar but not identical APIs.
//
// Background: Some providers (e.g., MiniMax) implement Anthropic-compatible
// APIs but use slightly different field names. For example:
//   - Standard Anthropic: uses "tool_use_id" in tool_result blocks
//   - MiniMax (Anthropic-compatible): uses "tool_call_id" instead
//
// Special case: NVIDIA NIM acts as a relay for various providers including
// MiniMax. When NVIDIA forwards requests to MiniMax models (identified by
// model name patterns like "minimaxai/*"), we need to use MiniMax's protocol.
//
// This mapping allows the IR serializer to adapt field names based on both
// the target provider's catalog code AND the model name pattern.

// ProviderFieldConfig holds provider-specific field name preferences.
type ProviderFieldConfig struct {
	// ToolResultIDField is the field name for tool result IDs.
	// Standard: "tool_use_id" (Anthropic, OpenAI via Anthropic)
	// MiniMax: "tool_call_id"
	ToolResultIDField string
}

// providerFieldMappings defines field name mappings for known providers.
var providerFieldMappings = map[string]ProviderFieldConfig{
	"minimax": {
		ToolResultIDField: "tool_call_id",
	},
	// Add more provider-specific mappings here as needed
	// "another-provider": {
	//     ToolResultIDField: "custom_field_name",
	// },
}

// GetProviderFieldConfig returns the field configuration for a given provider
// and optional model name. It handles both direct providers and relay providers
// that forward to different upstream services.
//
// Parameters:
//   - catalogCode: the provider's catalog code (e.g., "minimax", "nvidia")
//   - modelName: optional model name for model-specific routing (e.g., "minimaxai/minimax-m3")
//
// Examples:
//   - GetProviderFieldConfig("minimax", "") → uses tool_call_id (direct MiniMax)
//   - GetProviderFieldConfig("nvidia", "minimaxai/minimax-m3") → uses tool_call_id (NVIDIA → MiniMax)
//   - GetProviderFieldConfig("nvidia", "meta/llama-3") → uses tool_use_id (NVIDIA → Meta)
func GetProviderFieldConfig(catalogCode string, modelName string) ProviderFieldConfig {
	// Direct provider mapping
	if config, ok := providerFieldMappings[catalogCode]; ok {
		return config
	}

	// Model-specific routing detection for relay providers
	// NVIDIA NIM relays to various upstream providers based on model name patterns
	if catalogCode == "nvidia" {
		// NVIDIA forwards minimaxai/* models to MiniMax → use MiniMax protocol
		if strings.Contains(strings.ToLower(modelName), "minimaxai/") ||
			strings.Contains(strings.ToLower(modelName), "minimax") {
			return ProviderFieldConfig{
				ToolResultIDField: "tool_call_id",
			}
		}
	}

	// Default: standard Anthropic field names
	return ProviderFieldConfig{
		ToolResultIDField: "tool_use_id",
	}
}

// UsesToolCallID returns true if the provider uses "tool_call_id" instead of
// "tool_use_id" for tool result blocks. This is a convenience helper for the
// most common case.
func UsesToolCallID(catalogCode string, modelName string) bool {
	return GetProviderFieldConfig(catalogCode, modelName).ToolResultIDField == "tool_call_id"
}
