package autoroute

import (
	"strings"
	"testing"
)

// TestStructuredFeaturesNoContentLeakage verifies that ExtractStructuredFeatures
// does NOT include any prompt text, message content, keywords, or reversible
// content features in the returned StructuredFeatures.
//
// This is a critical privacy contract test enforcing the design principle from
// docs/auto-model-optimization/02-architecture-design.md:
// "不存储 prompt、messages、response、summary、keywords 或可逆内容特征"
func TestStructuredFeaturesNoContentLeakage(t *testing.T) {
	// Create test signals with sensitive content
	sensitivePrompt := "Please write a function to calculate fibonacci numbers"
	sensitiveSystem := "You are a helpful coding assistant specialized in algorithms"

	sigs := ClassificationSignals{
		SystemPrompt:    sensitiveSystem,
		LastUserPrompt:  sensitivePrompt,
		MessageCount:    3,
		EstimatedTokens: 1500,
		ToolCount:       1,
		HasImages:       false,
		Language:        "en",
		HasCodeBlock:    true,
		HasToolResults:  false,
		ClientType:      "vscode",
	}

	// Extract structured features
	features := ExtractStructuredFeatures(sigs, "smart")

	// Verify NO content leakage: check every field in StructuredFeatures
	// to ensure it does NOT contain any substring from the original prompts

	t.Run("DetectedLanguage contains no prompt content", func(t *testing.T) {
		assertNoContentLeakage(t, features.DetectedLanguage, sensitivePrompt, sensitiveSystem)
	})

	t.Run("PromptLengthBucket contains no prompt content", func(t *testing.T) {
		assertNoContentLeakage(t, features.PromptLengthBucket, sensitivePrompt, sensitiveSystem)
		// Also verify it's a valid bucket enum
		validBuckets := []string{"xs", "s", "m", "l", "xl", "xxl"}
		if !contains(validBuckets, features.PromptLengthBucket) {
			t.Errorf("PromptLengthBucket=%q is not a valid bucket", features.PromptLengthBucket)
		}
	})

	t.Run("ContextLengthBucket contains no prompt content", func(t *testing.T) {
		assertNoContentLeakage(t, features.ContextLengthBucket, sensitivePrompt, sensitiveSystem)
		validBuckets := []string{"xs", "s", "m", "l", "xl"}
		if !contains(validBuckets, features.ContextLengthBucket) {
			t.Errorf("ContextLengthBucket=%q is not a valid bucket", features.ContextLengthBucket)
		}
	})

	t.Run("TurnCountBucket contains no prompt content", func(t *testing.T) {
		assertNoContentLeakage(t, features.TurnCountBucket, sensitivePrompt, sensitiveSystem)
		validBuckets := []string{"single", "few", "many", "very_many"}
		if !contains(validBuckets, features.TurnCountBucket) {
			t.Errorf("TurnCountBucket=%q is not a valid bucket", features.TurnCountBucket)
		}
	})

	t.Run("IntentCategory contains no prompt content", func(t *testing.T) {
		assertNoContentLeakage(t, features.IntentCategory, sensitivePrompt, sensitiveSystem)
	})

	t.Run("DomainHint contains no prompt content", func(t *testing.T) {
		assertNoContentLeakage(t, features.DomainHint, sensitivePrompt, sensitiveSystem)
	})

	t.Run("ComplexityBucket contains no prompt content", func(t *testing.T) {
		assertNoContentLeakage(t, features.ComplexityBucket, sensitivePrompt, sensitiveSystem)
		validBuckets := []string{"trivial", "simple", "moderate", "complex", "very_complex"}
		if !contains(validBuckets, features.ComplexityBucket) {
			t.Errorf("ComplexityBucket=%q is not a valid bucket", features.ComplexityBucket)
		}
	})

	t.Run("ContentHash is non-reversible", func(t *testing.T) {
		// Content hash should be a hex string (64 chars for SHA256)
		if len(features.ContentHash) != 64 {
			t.Errorf("ContentHash length=%d, want 64 (SHA256 hex)", len(features.ContentHash))
		}
		// Verify it's hexadecimal
		for _, c := range features.ContentHash {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				t.Errorf("ContentHash contains non-hex character: %c", c)
			}
		}
		// Verify it does NOT contain any recognizable substring from the original prompts
		// (this would be a cryptographic hash collision, computationally infeasible)
		assertNoContentLeakage(t, features.ContentHash, sensitivePrompt, sensitiveSystem)
	})

	t.Run("Boolean flags contain no prompt content", func(t *testing.T) {
		// Boolean flags are by definition non-reversible (only true/false)
		// Just verify they are set based on signals, not content
		if !features.HasCodeIndicator {
			t.Error("HasCodeIndicator should be true when HasCodeBlock=true")
		}
		if features.HasMultimediaIndicator {
			t.Error("HasMultimediaIndicator should be false when HasImages=false")
		}
	})
}

// TestContentHashNonReversibility verifies that the content hash is a
// cryptographic hash that cannot be reversed to recover the original content.
func TestContentHashNonReversibility(t *testing.T) {
	testCases := []struct {
		name   string
		prompt string
		system string
	}{
		{
			name:   "short prompt",
			prompt: "hello world",
			system: "you are helpful",
		},
		{
			name:   "long prompt with sensitive data",
			prompt: strings.Repeat("secret data ", 100),
			system: "confidential system prompt",
		},
		{
			name:   "unicode content",
			prompt: "你好世界，请帮我写代码",
			system: "你是一个编程助手",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			sigs := ClassificationSignals{
				SystemPrompt:   tc.system,
				LastUserPrompt: tc.prompt,
			}
			features := ExtractStructuredFeatures(sigs, "smart")

			// Verify hash is 64 chars (SHA256 hex)
			if len(features.ContentHash) != 64 {
				t.Errorf("ContentHash length=%d, want 64", len(features.ContentHash))
			}

			// Verify hash does NOT contain any substring from original content
			// (for short content, this is a weak test, but for longer content
			// it would be cryptographically infeasible)
			assertNoContentLeakage(t, features.ContentHash, tc.prompt, tc.system)

			// Verify different content produces different hashes
			sigs2 := ClassificationSignals{
				SystemPrompt:   tc.system + " modified",
				LastUserPrompt: tc.prompt,
			}
			features2 := ExtractStructuredFeatures(sigs2, "smart")
			if features.ContentHash == features2.ContentHash {
				t.Error("Different content produced same hash (collision)")
			}
		})
	}
}

// TestSanitizeForAudit verifies that SanitizeForAudit removes all sensitive
// content and only returns non-reversible metadata.
func TestSanitizeForAudit(t *testing.T) {
	sensitivePrompt := "My credit card number is 1234-5678-9012-3456"
	sensitiveSystem := "Internal company secret: project codename alpha"

	sigs := ClassificationSignals{
		SystemPrompt:    sensitiveSystem,
		LastUserPrompt:  sensitivePrompt,
		MessageCount:    5,
		EstimatedTokens: 2000,
		ToolCount:       2,
		HasImages:       true,
		Language:        "en",
		HasCodeBlock:    false,
		HasToolResults:  true,
		ClientType:      "cursor",
	}

	sanitized := SanitizeForAudit(sigs)

	// Verify NO sensitive content in sanitized output
	for key, value := range sanitized {
		valueStr := ""
		switch v := value.(type) {
		case string:
			valueStr = v
		case int:
			// Integer values are safe (lengths, counts)
			continue
		case bool:
			// Boolean values are safe
			continue
		default:
			t.Errorf("Unexpected type for key=%s: %T", key, value)
		}

		// Check that string values don't contain sensitive content
		if strings.Contains(valueStr, "credit card") ||
			strings.Contains(valueStr, "1234-5678") ||
			strings.Contains(valueStr, "company secret") ||
			strings.Contains(valueStr, "codename") {
			t.Errorf("Sanitized output contains sensitive content: key=%s, value=%s", key, valueStr)
		}
	}

	// Verify expected keys are present (metadata only)
	expectedKeys := []string{
		"system_prompt_len", "last_user_len", "message_count",
		"estimated_tokens", "tool_count", "has_images", "language",
		"has_code_block", "has_tool_results", "client_type",
	}
	for _, key := range expectedKeys {
		if _, ok := sanitized[key]; !ok {
			t.Errorf("Expected key %q not found in sanitized output", key)
		}
	}

	// Verify prompt content is replaced with length
	if sanitized["system_prompt_len"] != len(sensitiveSystem) {
		t.Errorf("system_prompt_len=%v, want %d", sanitized["system_prompt_len"], len(sensitiveSystem))
	}
	if sanitized["last_user_len"] != len(sensitivePrompt) {
		t.Errorf("last_user_len=%v, want %d", sanitized["last_user_len"], len(sensitivePrompt))
	}
}

// TestFeatureVersioning verifies that feature version is always set correctly.
func TestFeatureVersioning(t *testing.T) {
	sigs := ClassificationSignals{
		LastUserPrompt: "test",
	}
	features := ExtractStructuredFeatures(sigs, "smart")

	if features.FeatureVersion != FeatureVersionV1 {
		t.Errorf("FeatureVersion=%q, want %q", features.FeatureVersion, FeatureVersionV1)
	}
}

// assertNoContentLeakage verifies that `value` does NOT contain any substring
// from `sensitivePrompt` or `sensitiveSystem`. This catches accidental content
// leakage into supposedly non-reversible features.
func assertNoContentLeakage(t *testing.T, value, sensitivePrompt, sensitiveSystem string) {
	t.Helper()

	// Check for any 4+ character substring from the sensitive content
	// (shorter substrings would have too many false positives)
	checkSubstrings := func(source string) {
		words := strings.Fields(source)
		for _, word := range words {
			if len(word) < 4 {
				continue
			}
			// Normalize for case-insensitive comparison
			if strings.Contains(strings.ToLower(value), strings.ToLower(word)) {
				t.Errorf("Feature value %q contains substring %q from sensitive content", value, word)
			}
		}
	}

	checkSubstrings(sensitivePrompt)
	checkSubstrings(sensitiveSystem)
}

// contains checks if a string slice contains a specific string.
func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
