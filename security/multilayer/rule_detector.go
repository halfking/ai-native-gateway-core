package security

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// RuleDetector implements Layer 1: rule-based security detection.
//
// Characteristics:
//  - Latency: <1ms (compiled regex, no I/O)
//  - Cost: Zero (pure computation)
//  - Precision: 80-90% (high false positive rate without context)
//
// Strategy: Fast pattern matching + structural analysis + known threat signatures
type RuleDetector struct {
	patterns       map[ThreatType][]*compiledPattern
	credibilityChecks map[ThreatType]credibilityChecker
	config         RuleDetectorConfig
}

// RuleDetectorConfig contains configuration for rule-based detection.
type RuleDetectorConfig struct {
	// EnableCredibilityChecks applies additional validation to reduce false positives
	EnableCredibilityChecks bool
	
	// MinMatchLength is the minimum length for a match to be considered
	MinMatchLength int
	
	// MaxMatchesPerType limits how many matches of each type to return
	MaxMatchesPerType int
}

// DefaultRuleDetectorConfig returns sensible defaults.
func DefaultRuleDetectorConfig() RuleDetectorConfig {
	return RuleDetectorConfig{
		EnableCredibilityChecks: true,
		MinMatchLength:          3,
		MaxMatchesPerType:       10,
	}
}

type compiledPattern struct {
	regex       *regexp.Regexp
	severity    int
	ruleID      string
	description string
	action      string // "block", "redact", "warn"
}

type credibilityChecker func(match string, ctx DetectionContext) bool

// NewRuleDetector creates a Layer 1 rule-based detector.
func NewRuleDetector(config RuleDetectorConfig) *RuleDetector {
	detector := &RuleDetector{
		patterns:          make(map[ThreatType][]*compiledPattern),
		credibilityChecks: make(map[ThreatType]credibilityChecker),
		config:            config,
	}
	
	detector.initializePatterns()
	detector.initializeCredibilityChecks()
	
	return detector
}

// Detect implements SecurityDetector for Layer 1.
func (r *RuleDetector) Detect(ctx context.Context, content string, detectionCtx DetectionContext) (*SecurityResult, error) {
	start := time.Now()
	
	var allThreats []ThreatMatch
	
	// Run all pattern matchers
	for threatType, patterns := range r.patterns {
		matches := r.matchPatterns(content, threatType, patterns, detectionCtx)
		allThreats = append(allThreats, matches...)
	}
	
	// Compute overall confidence
	confidence := r.computeConfidence(allThreats, detectionCtx)
	
	// Determine actions
	shouldBlock := r.shouldBlock(allThreats)
	shouldRedact := r.shouldRedact(allThreats)
	redactedText := ""
	if shouldRedact {
		redactedText = r.redactContent(content, allThreats)
	}
	
	result := &SecurityResult{
		IsSafe:       len(allThreats) == 0,
		Confidence:   confidence,
		Threats:      allThreats,
		Layer:        "rule",
		Latency:      time.Since(start),
		ShouldBlock:  shouldBlock,
		ShouldRedact: shouldRedact,
		RedactedText: redactedText,
		Reason:       r.buildReason(allThreats),
		Metadata: map[string]interface{}{
			"pattern_count": len(r.patterns),
			"match_count":   len(allThreats),
		},
	}
	
	return result, nil
}

// matchPatterns applies all patterns for a threat type.
func (r *RuleDetector) matchPatterns(content string, threatType ThreatType, patterns []*compiledPattern, ctx DetectionContext) []ThreatMatch {
	var matches []ThreatMatch
	
	for _, pattern := range patterns {
		regexMatches := pattern.regex.FindAllStringIndex(content, -1)
		
		for _, loc := range regexMatches {
			if len(loc) < 2 {
				continue
			}
			
			start, end := loc[0], loc[1]
			value := content[start:end]
			
			// Skip if too short
			if len(value) < r.config.MinMatchLength {
				continue
			}
			
			// Apply credibility check if enabled
			if r.config.EnableCredibilityChecks {
				if checker, ok := r.credibilityChecks[threatType]; ok {
					if !checker(value, ctx) {
						continue // Skip non-credible match
					}
				}
			}
			
			// Extract context (50 chars before/after)
			contextStr := r.extractContext(content, start, end, 50)
			
			match := ThreatMatch{
				Type:        threatType,
				Severity:    pattern.severity,
				Start:       start,
				End:         end,
				Value:       value,
				RuleID:      pattern.ruleID,
				Pattern:     pattern.regex.String(),
				Confidence:  r.computeMatchConfidence(value, threatType, ctx),
				Context:     contextStr,
				Reason:      pattern.description,
				Action:      pattern.action,
				Replacement: r.generateReplacement(threatType, value),
			}
			
			matches = append(matches, match)
			
			// Limit matches per type
			if len(matches) >= r.config.MaxMatchesPerType {
				return matches
			}
		}
	}
	
	return matches
}

// initializePatterns loads built-in detection patterns.
func (r *RuleDetector) initializePatterns() {
	// Credit card patterns
	r.patterns[ThreatCreditCard] = []*compiledPattern{
		{
			regex:       regexp.MustCompile(`\b\d{4}[\s-]?\d{4}[\s-]?\d{4}[\s-]?\d{4}\b`),
			severity:    9,
			ruleID:      "rule_credit_card_001",
			description: "Credit card number pattern",
			action:      "redact",
		},
	}
	
	// Email patterns
	r.patterns[ThreatEmail] = []*compiledPattern{
		{
			regex:       regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Z|a-z]{2,}\b`),
			severity:    5,
			ruleID:      "rule_email_001",
			description: "Email address pattern",
			action:      "redact",
		},
	}
	
	// Phone patterns (multiple formats)
	r.patterns[ThreatPhone] = []*compiledPattern{
		{
			regex:       regexp.MustCompile(`\b1[3-9]\d{9}\b`),
			severity:    6,
			ruleID:      "rule_phone_cn_001",
			description: "Chinese mobile phone number",
			action:      "redact",
		},
		{
			regex:       regexp.MustCompile(`\b\d{3}[-.\s]?\d{3}[-.\s]?\d{4}\b`),
			severity:    6,
			ruleID:      "rule_phone_us_001",
			description: "US phone number format",
			action:      "redact",
		},
	}
	
	// API Key patterns
	r.patterns[ThreatAPIKey] = []*compiledPattern{
		{
			regex:       regexp.MustCompile(`\b(sk-|ak-|pk-|Bearer\s+)[A-Za-z0-9_-]{20,}\b`),
			severity:    10,
			ruleID:      "rule_apikey_001",
			description: "API key pattern (sk-/ak-/pk-/Bearer prefix)",
			action:      "block",
		},
		{
			regex:       regexp.MustCompile(`\b[A-Za-z0-9]{32,}\b`),
			severity:    7,
			ruleID:      "rule_apikey_002",
			description: "Generic token pattern",
			action:      "warn",
		},
	}
	
	// Password patterns in text
	r.patterns[ThreatPassword] = []*compiledPattern{
		{
			regex:       regexp.MustCompile(`(?i)(password|passwd|pwd|密码)[\s:=]+[^\s]{8,}`),
			severity:    9,
			ruleID:      "rule_password_001",
			description: "Password declaration in text",
			action:      "redact",
		},
	}
	
	// Chinese ID card
	r.patterns[ThreatIDCard] = []*compiledPattern{
		{
			regex:       regexp.MustCompile(`\b\d{17}[\dXx]\b`),
			severity:    8,
			ruleID:      "rule_idcard_cn_001",
			description: "Chinese ID card (18 digits)",
			action:      "redact",
		},
	}
	
	// Internal IP addresses
	r.patterns[ThreatInternalIP] = []*compiledPattern{
		{
			regex:       regexp.MustCompile(`\b(?:10|172\.(?:1[6-9]|2[0-9]|3[01])|192\.168)\.\d{1,3}\.\d{1,3}\b`),
			severity:    4,
			ruleID:      "rule_internal_ip_001",
			description: "Internal IP address (RFC1918)",
			action:      "warn",
		},
	}
	
	// Prompt injection patterns
	r.patterns[ThreatPromptInjection] = []*compiledPattern{
		{
			regex:       regexp.MustCompile(`(?i)(ignore\s+(previous|above|all)\s+(instructions?|prompts?|rules?)|disregard|forget\s+what)`),
			severity:    8,
			ruleID:      "rule_injection_001",
			description: "Instruction override attempt",
			action:      "block",
		},
	}
	
	// System prompt leak attempts
	r.patterns[ThreatSystemPromptLeak] = []*compiledPattern{
		{
			regex:       regexp.MustCompile(`(?i)(show|tell|reveal|print|output|display|repeat)\s+(me\s+)?(your|the)\s+(system\s+)?(prompt|instructions?|rules?)`),
			severity:    7,
			ruleID:      "rule_system_leak_001",
			description: "System prompt extraction attempt",
			action:      "block",
		},
	}
	
	// Role manipulation
	r.patterns[ThreatRoleManipulation] = []*compiledPattern{
		{
			regex:       regexp.MustCompile(`(?i)(you\s+are\s+now|act\s+as|pretend\s+(to\s+be|you\s+are)|roleplay\s+as)\s+(a\s+)?\w+`),
			severity:    6,
			ruleID:      "rule_role_manip_001",
			description: "Role manipulation attempt",
			action:      "warn",
		},
	}
}

// initializeCredibilityChecks loads credibility validators.
func (r *RuleDetector) initializeCredibilityChecks() {
	// Credit card: Luhn algorithm validation
	r.credibilityChecks[ThreatCreditCard] = func(match string, ctx DetectionContext) bool {
		digits := strings.ReplaceAll(strings.ReplaceAll(match, " ", ""), "-", "")
		return luhnCheck(digits)
	}
	
	// Chinese ID card: Province code + date validation
	r.credibilityChecks[ThreatIDCard] = func(match string, ctx DetectionContext) bool {
		if len(match) != 18 {
			return false
		}
		// Check if first 2 digits are valid province code (simplified)
		provinceCode := match[0:2]
		validProvinceCodes := map[string]bool{
			"11": true, "12": true, "13": true, "14": true, "15": true,
			"21": true, "22": true, "23": true, "31": true, "32": true,
			"33": true, "34": true, "35": true, "36": true, "37": true,
			"41": true, "42": true, "43": true, "44": true, "45": true,
			"46": true, "50": true, "51": true, "52": true, "53": true,
			"54": true, "61": true, "62": true, "63": true, "64": true,
			"65": true,
		}
		return validProvinceCodes[provinceCode]
	}
	
	// Phone: Not in common false positive patterns
	r.credibilityChecks[ThreatPhone] = func(match string, ctx DetectionContext) bool {
		// Filter out sequential numbers (123456789, 111111111)
		if isSequential(match) || isRepeating(match) {
			return false
		}
		return true
	}
	
	// API Key: Check if surrounded by code context markers
	r.credibilityChecks[ThreatAPIKey] = func(match string, ctx DetectionContext) bool {
		// If in tool output or code block, likely legitimate example
		if ctx.IsToolOutput {
			// Check if it's a placeholder or example
			if strings.Contains(strings.ToLower(match), "example") ||
			   strings.Contains(strings.ToLower(match), "placeholder") ||
			   strings.Contains(strings.ToLower(match), "your") {
				return false
			}
		}
		return true
	}
}

// Helper functions

func (r *RuleDetector) computeMatchConfidence(value string, threatType ThreatType, ctx DetectionContext) float64 {
	// Base confidence from pattern match
	confidence := 0.7
	
	// Boost if credibility check passed
	if r.config.EnableCredibilityChecks {
		if checker, ok := r.credibilityChecks[threatType]; ok {
			if checker(value, ctx) {
				confidence += 0.15
			}
		}
	}
	
	// Reduce confidence for ambiguous contexts
	if ctx.IsToolOutput {
		confidence -= 0.1 // Tool outputs often contain examples
	}
	
	return min(0.95, max(0.5, confidence))
}

func (r *RuleDetector) computeConfidence(threats []ThreatMatch, ctx DetectionContext) float64 {
	if len(threats) == 0 {
		return 0.95 // High confidence it's safe
	}
	
	// Average confidence of individual matches
	sum := 0.0
	for _, t := range threats {
		sum += t.Confidence
	}
	return sum / float64(len(threats))
}

func (r *RuleDetector) shouldBlock(threats []ThreatMatch) bool {
	for _, t := range threats {
		if t.Severity >= 8 && t.Action == "block" {
			return true
		}
	}
	return false
}

func (r *RuleDetector) shouldRedact(threats []ThreatMatch) bool {
	for _, t := range threats {
		if t.Severity >= 6 && (t.Action == "redact" || t.Action == "block") {
			return true
		}
	}
	return false
}

func (r *RuleDetector) redactContent(content string, threats []ThreatMatch) string {
	// Sort by position (descending) to avoid offset issues
	sorted := make([]ThreatMatch, len(threats))
	copy(sorted, threats)
	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].Start > sorted[i].Start {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	
	result := content
	for _, threat := range sorted {
		if threat.Action == "redact" || threat.Action == "block" {
			before := result[:threat.Start]
			after := result[threat.End:]
			result = before + threat.Replacement + after
		}
	}
	
	return result
}

func (r *RuleDetector) generateReplacement(threatType ThreatType, value string) string {
	replacements := map[ThreatType]string{
		ThreatCreditCard: "[REDACTED:CREDIT_CARD]",
		ThreatEmail:      "[REDACTED:EMAIL]",
		ThreatPhone:      "[REDACTED:PHONE]",
		ThreatAPIKey:     "[REDACTED:API_KEY]",
		ThreatPassword:   "[REDACTED:PASSWORD]",
		ThreatIDCard:     "[REDACTED:ID_CARD]",
		ThreatInternalIP: "[REDACTED:IP]",
	}
	
	if replacement, ok := replacements[threatType]; ok {
		return replacement
	}
	return "[REDACTED]"
}

func (r *RuleDetector) extractContext(content string, start, end, contextLen int) string {
	contextStart := maxInt(0, start-contextLen)
	contextEnd := minInt(len(content), end+contextLen)
	return content[contextStart:contextEnd]
}

func (r *RuleDetector) buildReason(threats []ThreatMatch) string {
	if len(threats) == 0 {
		return "No threats detected"
	}
	
	threatCounts := make(map[ThreatType]int)
	for _, t := range threats {
		threatCounts[t.Type]++
	}
	
	var parts []string
	for threatType, count := range threatCounts {
		parts = append(parts, fmt.Sprintf("%s(%d)", threatType, count))
	}
	
	return "Detected threats: " + strings.Join(parts, ", ")
}

func (r *RuleDetector) Name() string { return "rule" }
func (r *RuleDetector) Layer() int   { return 1 }

// Utility functions

func luhnCheck(number string) bool {
	sum := 0
	isSecond := false
	
	for i := len(number) - 1; i >= 0; i-- {
		digit := int(number[i] - '0')
		if digit < 0 || digit > 9 {
			return false
		}
		
		if isSecond {
			digit *= 2
			if digit > 9 {
				digit -= 9
			}
		}
		
		sum += digit
		isSecond = !isSecond
	}
	
	return sum%10 == 0
}

func isSequential(s string) bool {
	if len(s) < 3 {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] != s[i-1]+1 {
			return false
		}
	}
	return true
}

func isRepeating(s string) bool {
	if len(s) < 3 {
		return false
	}
	first := s[0]
	for i := 1; i < len(s); i++ {
		if s[i] != first {
			return false
		}
	}
	return true
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
