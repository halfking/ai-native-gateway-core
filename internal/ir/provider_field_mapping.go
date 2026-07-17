package ir

// ProviderFieldMapping defines provider-specific field name mappings for
// protocol variants. This ensures compatibility across different providers
// that implement similar but not identical APIs.
//
// Background: Some providers (e.g., MiniMax) implement Anthropic-compatible
// APIs but use slightly different field names. For example:
//   - Standard Anthropic: uses "tool_use_id" in tool_result blocks
//   - MiniMax (Anthropic-compatible): uses "tool_call_id" instead
//
// This mapping allows the IR serializer to adapt field names based on the
// target provider's catalog code without breaking compatibility with other
// providers.

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

// GetProviderFieldConfig returns the field configuration for a given provider.
// If the provider is not in the mapping, returns the standard Anthropic config.
func GetProviderFieldConfig(catalogCode string) ProviderFieldConfig {
	if config, ok := providerFieldMappings[catalogCode]; ok {
		return config
	}
	// Default: standard Anthropic field names
	return ProviderFieldConfig{
		ToolResultIDField: "tool_use_id",
	}
}

// UsesToolCallID returns true if the provider uses "tool_call_id" instead of
// "tool_use_id" for tool result blocks. This is a convenience helper for the
// most common case.
func UsesToolCallID(catalogCode string) bool {
	return GetProviderFieldConfig(catalogCode).ToolResultIDField == "tool_call_id"
}
