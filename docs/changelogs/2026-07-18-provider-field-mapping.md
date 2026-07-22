# Provider Field Mapping Architecture

**Date**: 2026-07-18
**Type**: Enhancement
**Impact**: MiniMax tool_call_id compatibility, multi-provider support
**Related Issues**: tool_call_id_mismatch error on MiniMax

## Summary

Implemented a centralized provider field mapping system to handle protocol variants across different AI providers. This ensures compatibility without breaking existing providers.

## Problem

Different AI providers implement similar APIs with slight variations in field names:
- **Standard Anthropic**: uses `tool_use_id` in tool_result blocks
- **MiniMax** (Anthropic-compatible): uses `tool_call_id` instead

Previously, this was handled with inline `if targetProvider == "minimax"` checks scattered across the codebase, making it:
- Hard to maintain
- Risky when adding new providers
- Prone to breaking other providers

## Solution

### 1. Centralized Mapping Table

Created `internal/ir/provider_field_mapping.go` with:

```go
type ProviderFieldConfig struct {
    ToolResultIDField string
}

var providerFieldMappings = map[string]ProviderFieldConfig{
    "minimax": {
        ToolResultIDField: "tool_call_id",
    },
    // Future providers can be added here
}
```

### 2. Safe Defaults

Providers not in the mapping table automatically use standard Anthropic field names:

```go
func GetProviderFieldConfig(catalogCode string) ProviderFieldConfig {
    if config, ok := providerFieldMappings[catalogCode]; ok {
        return config
    }
    // Default: standard Anthropic field names
    return ProviderFieldConfig{
        ToolResultIDField: "tool_use_id",
    }
}
```

### 3. Simplified Usage

Replace scattered conditionals with lookup:

```go
// Before
if targetProvider == "minimax" {
    out["tool_call_id"] = block.ToolResult.ToolUseID
} else {
    out["tool_use_id"] = block.ToolResult.ToolUseID
}

// After
fieldName := GetProviderFieldConfig(targetProvider).ToolResultIDField
out[fieldName] = block.ToolResult.ToolUseID
```

## Changes

### Files Modified

1. **internal/ir/provider_field_mapping.go** (NEW)
   - Provider field configuration types
   - Mapping table
   - Helper functions

2. **internal/ir/provider_field_mapping_test.go** (NEW)
   - Comprehensive test coverage
   - Backward compatibility tests
   - Multi-provider verification

3. **internal/ir/serialize_anthropic.go**
   - Replaced inline conditionals with mapping lookups
   - Locations:
     - Line ~270: `serializeAnthropicMessage()` - tool role conversion
     - Line ~445: `serializeAnthropicContentBlock()` - tool_result blocks
     - Line ~755: `validateAnthropicToolCallIntegrity()` - validation

### Affected Providers

| Provider   | Tool Result ID Field | Status |
|------------|----------------------|--------|
| minimax    | `tool_call_id`       | ✅ Explicitly mapped |
| anthropic  | `tool_use_id`        | ✅ Default (unchanged) |
| openai     | `tool_use_id`        | ✅ Default (unchanged) |
| zhipu      | `tool_use_id`        | ✅ Default (unchanged) |
| deepseek   | `tool_use_id`        | ✅ Default (unchanged) |
| volcengine | `tool_use_id`        | ✅ Default (unchanged) |
| others     | `tool_use_id`        | ✅ Default (safe) |

## Testing

### Test Coverage

- ✅ All existing tests pass (130+ IR tests)
- ✅ MiniMax-specific tests pass
- ✅ Anthropic standard tests pass
- ✅ Backward compatibility verified for all providers
- ✅ Unknown provider defaults tested

### Test Results

```bash
$ go test ./internal/ir/ -run "."
PASS
ok  	github.com/kaixuan/llm-gateway-go/internal/ir	0.246s
```

## Benefits

1. **Maintainability**: Single source of truth for provider-specific mappings
2. **Safety**: Default to standard Anthropic, preventing breakage
3. **Extensibility**: Easy to add new providers without touching serialization logic
4. **Testability**: Centralized mapping is easy to test comprehensively

## Future Enhancements

The mapping system can be extended to support:
- Custom error field names
- Different streaming event formats
- Provider-specific rate limit headers
- Custom authentication schemes

## Migration Guide

### For Future Provider Additions

To add a new provider with custom field names:

1. Add entry to `providerFieldMappings` in `provider_field_mapping.go`:
   ```go
   "new-provider": {
       ToolResultIDField: "custom_field_name",
   },
   ```

2. Add test case to `provider_field_mapping_test.go`

3. No changes needed in serialization code

### Backward Compatibility

This change is **100% backward compatible**:
- All existing providers continue to work unchanged
- Unknown providers default to standard Anthropic behavior
- No changes to external APIs or contracts

## Rollout Plan

1. ✅ Implement centralized mapping system
2. ✅ Verify all tests pass
3. ⏳ Deploy to staging environment
4. ⏳ Monitor MiniMax requests in staging
5. ⏳ Deploy to production
6. ⏳ Monitor error rates for tool_call_id_mismatch

## Related Documentation

- [MiniMax API Documentation](https://platform.minimaxi.com/document/ChatCompletion)
- [Anthropic Messages API](https://docs.anthropic.com/claude/reference/messages_post)
- Internal: `TOOL_SCHEMA_ARCHITECTURE_ANALYSIS.md`
- Internal: `MINIMAX_MULTIMODAL_INVESTIGATION.md`
