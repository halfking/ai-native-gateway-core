package v2

// ExtractProviderExtensions extracts provider-specific extensions from various sources
//
// This function should be called in the main request processing pipeline
// to capture vendor-specific fields that need to be preserved in session storage.
//
// Usage in main pipeline:
//   processedReq := &ProcessedRequest{
//       // ... standard fields
//       ProviderExtensions: ExtractProviderExtensions(transportCtx, providerID),
//   }
func ExtractProviderExtensions(providerID string, rawExtensions map[string]interface{}) map[string]interface{} {
	if len(rawExtensions) == 0 {
		return nil
	}
	
	// Filter and preserve only provider-specific fields
	extensions := make(map[string]interface{})
	
	// Common provider-specific field patterns
	providerPrefixes := getProviderFieldPrefixes(providerID)
	
	for key, value := range rawExtensions {
		// Check if this field belongs to the current provider
		for _, prefix := range providerPrefixes {
			if len(key) > len(prefix) && key[:len(prefix)] == prefix {
				extensions[key] = value
				break
			}
		}
	}
	
	return extensions
}

// getProviderFieldPrefixes returns known field prefixes for each provider
func getProviderFieldPrefixes(providerID string) []string {
	switch providerID {
	case "openai":
		return []string{"openai_", "reasoning_", "modalities"}
		
	case "anthropic":
		return []string{"anthropic_", "thinking", "extended_thinking"}
		
	case "gemini", "google":
		return []string{"gemini_", "google_", "search_grounding"}
		
	case "deepseek":
		return []string{"deepseek_", "reasoning_content"}
		
	case "glm", "zhipu":
		return []string{"glm_", "zhipu_", "web_search", "retrieval"}
		
	case "minimax":
		return []string{"minimax_", "bot_setting", "plugins", "reply_constraints"}
		
	case "qwen", "dashscope":
		return []string{"qwen_", "dashscope_", "enable_search"}
		
	case "ollama":
		return []string{"ollama_", "options", "format", "keep_alive"}
		
	case "doubao", "volcengine":
		return []string{"doubao_", "volcengine_", "plugins", "bot_id"}
		
	default:
		// For unknown providers, capture any non-standard fields
		return []string{providerID + "_"}
	}
}

// MergeProviderExtensions merges extensions from multiple sources
//
// Priority: explicit > inferred > defaults
func MergeProviderExtensions(explicit, inferred, defaults map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})
	
	// Start with defaults
	for k, v := range defaults {
		result[k] = v
	}
	
	// Override with inferred
	for k, v := range inferred {
		result[k] = v
	}
	
	// Override with explicit
	for k, v := range explicit {
		result[k] = v
	}
	
	return result
}

// SanitizeProviderExtensions removes sensitive fields from extensions
//
// This should be called before storing extensions to avoid leaking credentials
func SanitizeProviderExtensions(extensions map[string]interface{}) map[string]interface{} {
	if len(extensions) == 0 {
		return nil
	}
	
	sanitized := make(map[string]interface{})
	
	// Sensitive field patterns to exclude
	sensitivePatterns := []string{
		"api_key", "secret", "token", "password", "credential",
		"auth", "bearer", "signature", "private_key",
	}
	
	for key, value := range extensions {
		// Check if key contains sensitive pattern
		isSensitive := false
		keyLower := key
		for _, pattern := range sensitivePatterns {
			if containsIgnoreCase(keyLower, pattern) {
				isSensitive = true
				break
			}
		}
		
		if !isSensitive {
			sanitized[key] = value
		}
	}
	
	return sanitized
}

// containsIgnoreCase checks if s contains substr (case-insensitive)
func containsIgnoreCase(s, substr string) bool {
	// Simple case-insensitive check
	sLower := ""
	substrLower := ""
	
	for _, c := range s {
		if c >= 'A' && c <= 'Z' {
			sLower += string(c + 32)
		} else {
			sLower += string(c)
		}
	}
	
	for _, c := range substr {
		if c >= 'A' && c <= 'Z' {
			substrLower += string(c + 32)
		} else {
			substrLower += string(c)
		}
	}
	
	for i := 0; i <= len(sLower)-len(substrLower); i++ {
		if sLower[i:i+len(substrLower)] == substrLower {
			return true
		}
	}
	
	return false
}

// ValidateProviderExtensions validates that extensions are safe to store
//
// Returns error if extensions contain disallowed content
func ValidateProviderExtensions(extensions map[string]interface{}) error {
	if len(extensions) == 0 {
		return nil
	}
	
	// Check size limits
	const maxExtensionsSize = 100 * 1024 // 100KB
	estimatedSize := estimateJSONSize(extensions)
	
	if estimatedSize > maxExtensionsSize {
		return &ExtensionsValidationError{
			Reason: "extensions too large",
			Size:   estimatedSize,
			Limit:  maxExtensionsSize,
		}
	}
	
	return nil
}

// estimateJSONSize estimates the JSON serialized size
func estimateJSONSize(data map[string]interface{}) int {
	// Rough estimation: key length + value estimation + overhead
	size := 2 // {} brackets
	
	for key, value := range data {
		size += len(key) + 4 // "key":
		size += estimateValueSize(value)
		size += 1 // comma
	}
	
	return size
}

// estimateValueSize estimates the size of a value
func estimateValueSize(value interface{}) int {
	switch v := value.(type) {
	case string:
		return len(v) + 2 // quotes
	case int, int64, float64, bool:
		return 16 // approximate
	case map[string]interface{}:
		return estimateJSONSize(v)
	case []interface{}:
		size := 2 // []
		for _, item := range v {
			size += estimateValueSize(item) + 1 // comma
		}
		return size
	default:
		return 32 // default estimate
	}
}

// ExtensionsValidationError represents an extensions validation failure
type ExtensionsValidationError struct {
	Reason string
	Size   int
	Limit  int
}

func (e *ExtensionsValidationError) Error() string {
	return "provider extensions validation failed: " + e.Reason
}
