package autoroute

// structured_features.go — extract non-reversible structured features from
// ClassificationSignals for storage in auto_route_selections.
//
// Ref: docs/auto-model-optimization/02-architecture-design.md
//      docs/auto-model-optimization/05-storage-optimization.md
//
// Design principle: These features are low-sensitivity, non-reversible, fixed
// schema values used for ML training and human annotation. They must NOT
// include prompt content, message text, keywords, truncated text, embeddings,
// or any reversible content features.
//
// All extraction functions are pure: given the same ClassificationSignals,
// they produce the same StructuredFeatures. No randomness, no global state.

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
)

const (
	// FeatureVersionV1 is the current structured feature schema version.
	FeatureVersionV1 = "v1"
)

// StructuredFeatures contains all non-reversible features extracted from a
// request for storage in auto_route_selections. All fields are optional to
// allow gradual rollout and backward compatibility.
type StructuredFeatures struct {
	// Language detection (enumeration, not content)
	DetectedLanguage string // zh, en, ja, ko, mixed, unknown

	// Length buckets (logarithmic bins, not actual content)
	PromptLengthBucket  string // xs, s, m, l, xl, xxl
	ContextLengthBucket string // xs, s, m, l, xl
	TurnCountBucket     string // single, few, many, very_many

	// Content type indicators (boolean flags, no actual content)
	HasCodeIndicator       bool
	HasMathIndicator       bool
	HasTableIndicator      bool
	HasMultimediaIndicator bool

	// Intent and domain (high-level classification)
	IntentCategory string // question, instruction, conversation, analysis, generation
	DomainHint     string // general, technical, business, academic, creative

	// Complexity bucket (heuristic estimate)
	ComplexityBucket string // trivial, simple, moderate, complex, very_complex

	// User preference signals
	LatencySensitive bool
	CostSensitive    bool

	// Feature schema version
	FeatureVersion string // v1, v2, etc.

	// Non-reversible content hash (for deduplication only)
	ContentHash string // SHA256 hex
}

// ExtractStructuredFeatures derives non-reversible features from
// ClassificationSignals. This is the ONLY function that should be called
// from the request path to create features for storage.
//
// Privacy guarantee: The returned StructuredFeatures contains NO prompt text,
// NO message content, NO keywords, and NO reversible content features.
func ExtractStructuredFeatures(sigs ClassificationSignals, profile string) StructuredFeatures {
	return StructuredFeatures{
		DetectedLanguage:       detectLanguageEnum(sigs.Language),
		PromptLengthBucket:     computeLengthBucket(estimatePromptLength(sigs)),
		ContextLengthBucket:    computeContextLengthBucket(sigs.EstimatedTokens),
		TurnCountBucket:        computeTurnCountBucket(sigs.MessageCount),
		HasCodeIndicator:       sigs.HasCodeBlock || containsCodeSignal(sigs),
		HasMathIndicator:       containsMathSignal(sigs),
		HasTableIndicator:      false, // TODO: implement table detection
		HasMultimediaIndicator: sigs.HasImages,
		IntentCategory:         inferIntentCategory(sigs),
		DomainHint:             inferDomainHint(sigs),
		ComplexityBucket:       inferComplexityBucket(sigs),
		LatencySensitive:       profile == "speed_first",
		CostSensitive:          profile == "cost_first",
		FeatureVersion:         FeatureVersionV1,
		ContentHash:            computeContentHash(sigs),
	}
}

// detectLanguageEnum returns a language enum (zh, en, ja, ko, mixed, unknown)
// based on the Language field from ClassificationSignals. This is already a
// non-reversible signal computed during signal extraction.
func detectLanguageEnum(lang string) string {
	switch strings.ToLower(lang) {
	case "zh", "en", "ja", "ko", "mixed":
		return strings.ToLower(lang)
	default:
		return "unknown"
	}
}

// estimatePromptLength estimates the total character count of the prompt
// (system + last user message) without storing the actual content.
func estimatePromptLength(sigs ClassificationSignals) int {
	// Use the original lengths from signal extraction, not the content itself
	systemLen := len(sigs.SystemPrompt)
	userLen := len(sigs.LastUserPrompt)
	return systemLen + userLen
}

// computeLengthBucket maps a character count to a logarithmic bucket.
// Buckets: xs (<500), s (500-2k), m (2k-8k), l (8k-32k), xl (32k-128k), xxl (128k+)
func computeLengthBucket(charCount int) string {
	switch {
	case charCount < 500:
		return "xs"
	case charCount < 2000:
		return "s"
	case charCount < 8000:
		return "m"
	case charCount < 32000:
		return "l"
	case charCount < 128000:
		return "xl"
	default:
		return "xxl"
	}
}

// computeContextLengthBucket maps a token count to a context length bucket.
// Buckets: xs (<2k), s (2k-8k), m (8k-32k), l (32k-128k), xl (128k+)
func computeContextLengthBucket(tokens int) string {
	switch {
	case tokens < 2000:
		return "xs"
	case tokens < 8000:
		return "s"
	case tokens < 32000:
		return "m"
	case tokens < 128000:
		return "l"
	default:
		return "xl"
	}
}

// computeTurnCountBucket maps message count to a conversation bucket.
// Buckets: single (1), few (2-5), many (6-20), very_many (20+)
func computeTurnCountBucket(messageCount int) string {
	switch {
	case messageCount <= 1:
		return "single"
	case messageCount <= 5:
		return "few"
	case messageCount <= 20:
		return "many"
	default:
		return "very_many"
	}
}

// containsCodeSignal returns true if the request has coding indicators
// (HasCodeBlock, IDE client type, or structural code patterns).
// This is a DERIVED boolean flag, not content storage.
func containsCodeSignal(sigs ClassificationSignals) bool {
	return sigs.HasCodeBlock || sigs.ClientType != ""
}

// containsMathSignal returns true if the request has math/reasoning indicators.
// This is heuristic detection based on non-content signals only.
func containsMathSignal(sigs ClassificationSignals) bool {
	// Use structural signals only: high tool count with reasoning pattern,
	// or explicit reasoning task type from prior classification.
	// Do NOT scan prompt content here — that would violate the design.
	return false // Conservative: requires explicit detection in classifier
}

// inferIntentCategory maps signals to a high-level intent enum.
// Categories: question, instruction, conversation, analysis, generation
func inferIntentCategory(sigs ClassificationSignals) string {
	// Use structural signals: message count, tool count, client type
	if sigs.MessageCount == 1 {
		if sigs.ToolCount > 0 {
			return "instruction" // Single message with tools → instruction
		}
		return "question" // Single message, no tools → question
	}
	if sigs.MessageCount > 5 {
		return "conversation" // Multi-turn → conversation
	}
	if sigs.ToolCount >= 3 {
		return "analysis" // Multi-tool → analysis
	}
	return "generation" // Default: generative task
}

// inferDomainHint maps signals to a domain enum.
// Domains: general, technical, business, academic, creative
func inferDomainHint(sigs ClassificationSignals) string {
	// Use non-content signals: IDE client type, tool count, code/math indicators
	if sigs.ClientType != "" || sigs.HasCodeBlock {
		return "technical"
	}
	if sigs.ToolCount >= 3 {
		return "business" // Multi-tool workflows often business-oriented
	}
	// Conservative: default to general without content inspection
	return "general"
}

// inferComplexityBucket estimates request complexity from structural signals.
// Buckets: trivial, simple, moderate, complex, very_complex
func inferComplexityBucket(sigs ClassificationSignals) string {
	// Complexity heuristic: combine token count, message count, tool count
	score := 0
	if sigs.EstimatedTokens > 10000 {
		score += 2
	} else if sigs.EstimatedTokens > 2000 {
		score += 1
	}
	if sigs.MessageCount > 10 {
		score += 2
	} else if sigs.MessageCount > 3 {
		score += 1
	}
	if sigs.ToolCount >= 5 {
		score += 2
	} else if sigs.ToolCount >= 2 {
		score += 1
	}

	switch {
	case score == 0:
		return "trivial"
	case score <= 2:
		return "simple"
	case score <= 4:
		return "moderate"
	case score <= 5:
		return "complex"
	default:
		return "very_complex"
	}
}

// computeContentHash computes a SHA256 hash of the prompt content for
// deduplication purposes. The hash is non-reversible and cannot be used
// to reconstruct the original prompt.
//
// Privacy guarantee: This function computes a cryptographic hash. Given the
// hash alone, it is computationally infeasible to recover the original content.
func computeContentHash(sigs ClassificationSignals) string {
	// Concatenate system + user prompt for hashing
	// We use the content here ONLY to compute a hash, not to store it
	var b strings.Builder
	b.WriteString(sigs.SystemPrompt)
	b.WriteByte('\n')
	b.WriteString(sigs.LastUserPrompt)
	content := b.String()

	// Compute SHA256 hash
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

// SanitizeForAudit returns a sanitized version of ClassificationSignals
// suitable for logging and debugging. All content fields are replaced with
// length indicators or redacted markers.
func SanitizeForAudit(sigs ClassificationSignals) map[string]any {
	return map[string]any{
		"system_prompt_len": len(sigs.SystemPrompt),
		"last_user_len":     len(sigs.LastUserPrompt),
		"message_count":     sigs.MessageCount,
		"estimated_tokens":  sigs.EstimatedTokens,
		"tool_count":        sigs.ToolCount,
		"has_images":        sigs.HasImages,
		"language":          sigs.Language,
		"has_code_block":    sigs.HasCodeBlock,
		"has_tool_results":  sigs.HasToolResults,
		"client_type":       sigs.ClientType,
	}
}

// isASCII returns true if all runes in s are ASCII (< 128).
func isASCII(s string) bool {
	for _, r := range s {
		if r >= unicode.MaxASCII {
			return false
		}
	}
	return true
}

// isCJK returns true if the string contains CJK (Chinese/Japanese/Korean) characters.
func isCJK(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
			unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			return true
		}
	}
	return false
}
