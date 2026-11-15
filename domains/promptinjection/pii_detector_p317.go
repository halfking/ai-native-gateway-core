package promptinjection

import (
	"regexp"
	"strings"
	"unicode"
)

// P3-17: Enhanced PII detection with context validation to reduce false positives
// Original issue: Pattern `\d{3}-\d{2}-\d{4}` matches SSN format but has high false positive rate
// Examples of false positives:
// - "Version 1-2-3" (software versioning)
// - "Model 123-45-6789" (product codes)
// - "Phone 123-456-7890" (phone numbers, though similar format)
//
// Solution: Add context validation to confirm SSN context before flagging as PII

// SSNValidator provides context-aware validation for SSN patterns
type SSNValidator struct {
	// Pattern to match SSN format
	ssnPattern *regexp.Regexp
	// Context indicators that suggest this is actually an SSN
	contextIndicators []string
	// Context indicators that suggest this is NOT an SSN (false positive)
	falsePositiveIndicators []string
}

// NewSSNValidator creates a new SSN validator with context awareness
func NewSSNValidator() *SSNValidator {
	return &SSNValidator{
		ssnPattern: regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`),
		contextIndicators: []string{
			"ssn", "social security", "security number",
			"employee id", "employee-id", "employee number",
			"tax id", "tax-id", "tin", "taxpayer",
			"identification", "person id", "individual id",
		},
		falsePositiveIndicators: []string{
			"version", "v1", "v2", "release", "build",
			"model", "sku", "product", "item",
			"code", "number:", "catalog", "part",
			"phone", "call", "mobile", "telephone",
			"port", "address", "ip",
		},
	}
}

// IsLikelySSN checks if a matched number is likely an actual SSN
// based on surrounding context, not just the pattern match.
// This reduces false positives from version numbers, product codes, etc.
func (v *SSNValidator) IsLikelySSN(input string, matchStart, matchEnd int) bool {
	// Check if SSN format appears isolated (not part of longer number sequences)
	// If surrounded by text immediately (e.g., "v123-45-6789x"), likely false positive
	if matchStart > 0 {
		charBefore := rune(input[matchStart-1])
		if unicode.IsLetter(charBefore) || unicode.IsDigit(charBefore) {
			return false // Part of larger string
		}
	}
	if matchEnd < len(input) {
		charAfter := rune(input[matchEnd])
		if unicode.IsLetter(charAfter) || unicode.IsDigit(charAfter) {
			return false // Part of larger string
		}
	}
	
	// Look at text immediately before the match to find the strongest signal
	// Check different window sizes and take the closest, most relevant indicator
	
	// Window 1: Very close (15 chars back) - strongest signal
	closeStart := matchStart - 15
	if closeStart < 0 {
		closeStart = 0
	}
	closeContext := strings.ToLower(input[closeStart:matchStart])
	
	// Check for false positive indicators in close context
	falsePositiveClose := []string{
		"v.", "version", "release", "build",
		"model", "sku", "product", "code",
	}
	for _, pattern := range falsePositiveClose {
		if strings.Contains(closeContext, pattern) {
			return false
		}
	}
	
	// Check for true positive indicators in close context
	for _, indicator := range v.contextIndicators {
		if strings.Contains(closeContext, indicator) {
			return true
		}
	}
	
	// Window 2: Medium range (50 chars back) - weaker signal
	beforeStart := matchStart - 50
	if beforeStart < 0 {
		beforeStart = 0
	}
	beforeMatch := strings.ToLower(input[beforeStart:matchStart])
	
	// Check for true positive indicators in medium context
	for _, indicator := range v.contextIndicators {
		if strings.Contains(beforeMatch, indicator) {
			return true
		}
	}
	
	// Check for false positive indicators only if no true positive found
	for _, pattern := range falsePositiveClose {
		if strings.Contains(beforeMatch, pattern) {
			return false
		}
	}
	
	// Default: without clear indicators, assume it's a false positive
	// (conservative approach to minimize over-flagging)
	return false
}

// FindSSNs finds SSN-like patterns in input with context validation
// Returns matches only if context suggests they're actual SSNs
func (v *SSNValidator) FindSSNs(input string) []struct {
	match string
	start int
	end   int
} {
	var results []struct {
		match string
		start int
		end   int
	}
	
	matches := v.ssnPattern.FindAllStringSubmatchIndex(input, -1)
	for _, match := range matches {
		matchStart := match[0]
		matchEnd := match[1]
		
		// P3-17: Add context validation
		if v.IsLikelySSN(input, matchStart, matchEnd) {
			results = append(results, struct {
				match string
				start int
				end   int
			}{
				match: input[matchStart:matchEnd],
				start: matchStart,
				end:   matchEnd,
			})
		}
	}
	
	return results
}
