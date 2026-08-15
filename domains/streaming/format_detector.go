package streaming

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// FormatPattern defines a client request format pattern.
// It describes the structure, known issues, and fix strategies for a specific client type.
type FormatPattern struct {
	ID          string         // Unique identifier, e.g., "opencode-v1"
	Name        string         // Display name, e.g., "OpenCode CLI"
	ClientHint  []string       // User-Agent hints, e.g., ["opencode", "openai-python"]
	Version     string         // Version, e.g., "1.0.0"
	Features    FormatFeatures // Structure features
	KnownIssues []FormatIssue  // Known issues
	Fixes       []FormatFix    // Fix strategies
}

// FormatFeatures describes the structural features of a format.
type FormatFeatures struct {
	RequiredFields []string          // Required fields, e.g., ["model", "messages"]
	OptionalFields []string          // Optional fields
	FieldTypes     map[string]string // Field type signatures, e.g., {"messages": "array"}
	Markers        []string          // Special markers for identification
}

// FormatIssue describes a known issue with a format.
type FormatIssue struct {
	Type        string // Issue type, e.g., "empty_object", "missing_field"
	Field       string // Problem field
	Description string // Human-readable description
	Frequency   int    // Occurrence frequency (for prioritization)
}

// FormatFix describes a fix strategy.
type FormatFix struct {
	IssueType   string                                                  // Issue type to fix
	FixFunc     func(body map[string]any) (map[string]any, bool, error) // Fix function, returns (fixed, changed, error)
	Description string                                                  // Human-readable description
}

// DetectResult represents the result of format detection.
type DetectResult struct {
	Pattern     *FormatPattern // Matched pattern (nil if no match)
	Confidence  float64        // Confidence score 0.0-1.0
	Issues      []FormatIssue  // Detected issues
	CanFix      bool           // Whether issues can be fixed
	Suggestions []string       // Fix suggestions
}

// FixResult represents the result of format fixing.
type FixResult struct {
	Original []byte   // Original request body
	Fixed    []byte   // Fixed request body
	Applied  []string // Applied fixes
	Changed  bool     // Whether any changes were made
	Pattern  string   // Pattern ID used
}

// FormatDetector detects request format patterns.
type FormatDetector struct {
	registry *FormatRegistry
}

// NewFormatDetector creates a new format detector.
func NewFormatDetector(registry *FormatRegistry) *FormatDetector {
	return &FormatDetector{registry: registry}
}

// Detect detects the format of a request.
func (d *FormatDetector) Detect(body []byte, headers http.Header) DetectResult {
	var bodyMap map[string]any
	if err := json.Unmarshal(body, &bodyMap); err != nil {
		return DetectResult{
			Confidence: 0.0,
			Issues: []FormatIssue{{
				Type:        "invalid_json",
				Description: "Request body is not valid JSON",
			}},
			CanFix: false,
		}
	}

	// 1. Try fast match by User-Agent
	userAgent := headers.Get("User-Agent")
	if pattern := d.matchByUserAgent(userAgent); pattern != nil {
		score := d.scorePattern(bodyMap, pattern)
		if score > 0.5 { // Lowered threshold - User-Agent hint is strong
			issues := d.detectIssues(bodyMap, pattern)
			return DetectResult{
				Pattern:     pattern,
				Confidence:  score,
				Issues:      issues,
				CanFix:      len(issues) > 0 && len(pattern.Fixes) > 0,
				Suggestions: d.generateSuggestions(issues, pattern),
			}
		}
	}

	// 2. Structure-based matching (iterate all known patterns)
	return d.findBestMatch(bodyMap)
}

// matchByUserAgent tries to match a pattern by User-Agent header.
func (d *FormatDetector) matchByUserAgent(userAgent string) *FormatPattern {
	userAgent = strings.ToLower(userAgent)
	patterns := d.registry.List()

	for _, pattern := range patterns {
		for _, hint := range pattern.ClientHint {
			if strings.Contains(userAgent, strings.ToLower(hint)) {
				return pattern
			}
		}
	}

	return nil
}

// findBestMatch finds the best matching pattern by structure.
func (d *FormatDetector) findBestMatch(body map[string]any) DetectResult {
	patterns := d.registry.List()
	var bestPattern *FormatPattern
	var bestScore float64

	for _, pattern := range patterns {
		score := d.scorePattern(body, pattern)
		if score > bestScore {
			bestScore = score
			bestPattern = pattern
		}
	}

	if bestPattern == nil || bestScore < 0.6 {
		return DetectResult{
			Confidence:  bestScore,
			CanFix:      false,
			Suggestions: []string{"Request format does not match any known patterns"},
		}
	}

	issues := d.detectIssues(body, bestPattern)
	return DetectResult{
		Pattern:     bestPattern,
		Confidence:  bestScore,
		Issues:      issues,
		CanFix:      len(issues) > 0 && len(bestPattern.Fixes) > 0,
		Suggestions: d.generateSuggestions(issues, bestPattern),
	}
}

// scorePattern calculates a confidence score for a pattern match.
func (d *FormatDetector) scorePattern(body map[string]any, pattern *FormatPattern) float64 {
	score := 0.0
	maxScore := 0.0

	// Check required fields (weight 0.5)
	for _, field := range pattern.Features.RequiredFields {
		maxScore += 0.5
		if _, ok := body[field]; ok {
			score += 0.5
		}
	}

	// Check field types (weight 0.3)
	for field, expectedType := range pattern.Features.FieldTypes {
		maxScore += 0.3
		if val, ok := body[field]; ok {
			actualType := getJSONType(val)
			if actualType == expectedType {
				score += 0.3
			} else if isCompatibleType(actualType, expectedType) {
				score += 0.15 // Compatible types get half score
			}
		}
	}

	// Check special markers (weight 0.2)
	for _, marker := range pattern.Features.Markers {
		maxScore += 0.2
		if hasMarker(body, marker) {
			score += 0.2
		}
	}

	if maxScore == 0 {
		return 0.0
	}
	return score / maxScore
}

// detectIssues detects known issues in the request body.
func (d *FormatDetector) detectIssues(body map[string]any, pattern *FormatPattern) []FormatIssue {
	var detected []FormatIssue

	for _, issue := range pattern.KnownIssues {
		if d.hasIssue(body, issue) {
			detected = append(detected, issue)
		}
	}

	return detected
}

// hasIssue checks if a specific issue exists in the body.
func (d *FormatDetector) hasIssue(body map[string]any, issue FormatIssue) bool {
	switch issue.Type {
	case "empty_object_messages":
		if msg, ok := body["messages"]; ok {
			if msgMap, isMap := msg.(map[string]any); isMap && len(msgMap) == 0 {
				return true
			}
		}
	case "string_messages":
		if msg, ok := body["messages"]; ok {
			if _, isStr := msg.(string); isStr {
				return true
			}
		}
	case "missing_field":
		if _, ok := body[issue.Field]; !ok {
			return true
		}
	case "wrong_type":
		if val, ok := body[issue.Field]; ok {
			expectedType := issue.Description // Stored in description for simplicity
			actualType := getJSONType(val)
			return actualType != expectedType
		}
	}
	return false
}

// generateSuggestions generates fix suggestions based on issues.
func (d *FormatDetector) generateSuggestions(issues []FormatIssue, pattern *FormatPattern) []string {
	var suggestions []string
	for _, issue := range issues {
		// Find applicable fix
		for _, fix := range pattern.Fixes {
			if fix.IssueType == issue.Type {
				suggestions = append(suggestions, fix.Description)
				break
			}
		}
	}
	return suggestions
}

// Helper functions

func getJSONType(val any) string {
	switch val.(type) {
	case string:
		return "string"
	case float64, int, int64:
		return "number"
	case bool:
		return "boolean"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	case nil:
		return "null"
	default:
		return "unknown"
	}
}

func isCompatibleType(actual, expected string) bool {
	// Define compatible type pairs
	compatible := map[string][]string{
		"number": {"integer", "float"},
		"string": {"text"},
		"array":  {"list"},
	}

	if compatibles, ok := compatible[expected]; ok {
		for _, c := range compatibles {
			if c == actual {
				return true
			}
		}
	}
	return false
}

func hasMarker(body map[string]any, marker string) bool {
	// Check if body contains a special marker field or pattern
	_, ok := body[marker]
	return ok
}

// FormatFixer fixes request format issues.
type FormatFixer struct{}

// NewFormatFixer creates a new format fixer.
func NewFormatFixer() *FormatFixer {
	return &FormatFixer{}
}

// Fix attempts to fix format issues in the request body.
func (f *FormatFixer) Fix(body []byte, pattern *FormatPattern) (FixResult, error) {
	var bodyMap map[string]any
	if err := json.Unmarshal(body, &bodyMap); err != nil {
		return FixResult{}, err
	}

	result := FixResult{
		Original: body,
		Pattern:  pattern.ID,
	}

	// Apply all fix strategies
	for _, fix := range pattern.Fixes {
		fixed, changed, err := fix.FixFunc(bodyMap)
		if err != nil {
			continue // Skip failed fixes
		}
		if changed {
			bodyMap = fixed
			result.Applied = append(result.Applied, fix.IssueType)
			result.Changed = true
		}
	}

	if result.Changed {
		fixedBytes, err := json.Marshal(bodyMap)
		if err != nil {
			return FixResult{}, err
		}
		result.Fixed = fixedBytes
	} else {
		result.Fixed = body
	}

	return result, nil
}

// FormatRegistry manages known format patterns.
type FormatRegistry struct {
	mu       sync.RWMutex
	patterns map[string]*FormatPattern
}

// NewFormatRegistry creates a new format registry with predefined patterns.
func NewFormatRegistry() *FormatRegistry {
	r := &FormatRegistry{
		patterns: make(map[string]*FormatPattern),
	}

	// Register predefined patterns
	r.Register(createOpenAIPattern())
	r.Register(createOpenCodePattern())
	r.Register(createAnthropicPattern())

	return r
}

// Register registers a format pattern.
func (r *FormatRegistry) Register(pattern *FormatPattern) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.patterns[pattern.ID] = pattern
}

// Get retrieves a format pattern by ID.
func (r *FormatRegistry) Get(id string) *FormatPattern {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.patterns[id]
}

// List returns all registered patterns.
func (r *FormatRegistry) List() []*FormatPattern {
	r.mu.RLock()
	defer r.mu.RUnlock()

	patterns := make([]*FormatPattern, 0, len(r.patterns))
	for _, p := range r.patterns {
		patterns = append(patterns, p)
	}
	return patterns
}

// FormatCache caches format information in Redis (interface for now).
type FormatCache interface {
	Get(ctx context.Context, sessionID string) (*CachedFormat, error)
	Set(ctx context.Context, sessionID string, format *CachedFormat) error
	Delete(ctx context.Context, sessionID string) error
}

// CachedFormat represents cached format information.
type CachedFormat struct {
	PatternID   string    `json:"pattern_id"`
	PatternName string    `json:"pattern_name"`
	Confidence  float64   `json:"confidence"`
	CachedAt    time.Time `json:"cached_at"`
	UseCount    int       `json:"use_count"`
	LastUsed    time.Time `json:"last_used"`
}
