package promptinjection

import (
	"testing"
)

// TestSSNValidator_ContextAwareness tests that SSN pattern matching with context validation
// reduces false positives from version numbers, product codes, etc.
func TestSSNValidator_ContextAwareness(t *testing.T) {
	validator := NewSSNValidator()

	tests := []struct {
		name      string
		input     string
		wantMatch bool
		matchText string
	}{
		// True positives: actual SSN contexts
		{
			name:      "SSN with explicit label",
			input:     "Employee SSN: 123-45-6789 for tax purposes",
			wantMatch: true,
			matchText: "123-45-6789",
		},
		{
			name:      "Social Security Number context",
			input:     "The social security number is 987-65-4321",
			wantMatch: true,
			matchText: "987-65-4321",
		},
		{
			name:      "Tax ID context",
			input:     "Tax ID: 111-22-3333",
			wantMatch: true,
			matchText: "111-22-3333",
		},
		{
			name:      "Employee ID context",
			input:     "Employee ID 555-55-5555 is assigned",
			wantMatch: true,
			matchText: "555-55-5555",
		},
		// False positives: should NOT be flagged
		{
			name:      "Version number",
			input:     "Version 1-2-3456 is available",
			wantMatch: false,
			matchText: "",
		},
		{
			name:      "Product code with model prefix",
			input:     "Model 123-45-6789 SKU",
			wantMatch: false,
			matchText: "",
		},
		{
			name:      "Release build number",
			input:     "Release build 100-10-1234",
			wantMatch: false,
			matchText: "",
		},
		{
			name:      "Phone number context",
			input:     "Call phone 999-88-7777 for support",
			wantMatch: false,
			matchText: "",
		},
		{
			name:      "Catalog part number",
			input:     "Part 222-33-4444 from catalog",
			wantMatch: false,
			matchText: "",
		},
		// Edge cases
		{
			name:      "Isolated SSN without context",
			input:     "123-45-6789",
			wantMatch: false,
			matchText: "",
		},
		{
			name:      "SSN preceded by line break and label",
			input:     "Employee info:\nSSN: 123-45-6789\nDeactivated",
			wantMatch: true,
			matchText: "123-45-6789",
		},
		{
			name:      "Word boundary check - part of larger string",
			input:     "v123-45-6789x build tag",
			wantMatch: false,
			matchText: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := validator.FindSSNs(tt.input)

			if tt.wantMatch {
				if len(results) == 0 {
					t.Errorf("Expected SSN match for %q, got none", tt.input)
				} else if results[0].match != tt.matchText {
					t.Errorf("Expected match %q, got %q", tt.matchText, results[0].match)
				}
			} else {
				if len(results) > 0 {
					t.Errorf("Expected no SSN match for %q, but found: %v", tt.input, results[0].match)
				}
			}
		})
	}
}

// TestSSNValidator_IsLikelySSN tests the context validation logic directly
func TestSSNValidator_IsLikelySSN(t *testing.T) {
	validator := NewSSNValidator()

	tests := []struct {
		name      string
		input     string
		matchPos  int // position where the SSN-like pattern starts
		wantLikely bool
	}{
		{
			name:       "SSN after 'SSN:' label",
			input:      "SSN: 123-45-6789",
			matchPos:   5,  // position of "1" in "123", adjusted
			wantLikely: true,
		},
		{
			name:       "Version prefix context",
			input:      "Version 1-2-3456",
			matchPos:   8,
			wantLikely: false,
		},
		{
			name:       "Model prefix context",
			input:      "Model 123-45-6789",
			matchPos:   6,
			wantLikely: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Find the actual match end position
			matchEnd := tt.matchPos + len("123-45-6789") // Standard SSN length
			if matchEnd > len(tt.input) {
				matchEnd = len(tt.input)
			}

			result := validator.IsLikelySSN(tt.input, tt.matchPos, matchEnd)
			if result != tt.wantLikely {
				t.Errorf("IsLikelySSN(%q, %d, %d) = %v, want %v",
					tt.input, tt.matchPos, matchEnd, result, tt.wantLikely)
			}
		})
	}
}

// TestSSNValidator_MultipleMatches tests handling of multiple patterns in one string
func TestSSNValidator_MultipleMatches(t *testing.T) {
	validator := NewSSNValidator()

	input := "Employee records: SSN: 111-11-1111, Tax ID: 222-22-2222, Version 333-33-3333, Product SKU 444-44-4444, Social Security Number: 555-55-5555"

	results := validator.FindSSNs(input)

	// Debug: print what we actually found
	t.Logf("Found %d SSN matches in: %s", len(results), input)
	for _, r := range results {
		t.Logf("  - %s at position %d-%d", r.match, r.start, r.end)
	}

	// Should find SSNs with explicit context indicators, ignore version/product patterns
	expectedMatches := map[string]bool{
		"111-11-1111": true, // after SSN:
		"222-22-2222": true, // after Tax ID:
		"555-55-5555": true, // after Social Security Number:
	}

	for _, r := range results {
		if !expectedMatches[r.match] {
			t.Errorf("Unexpected match: %s", r.match)
		}
		delete(expectedMatches, r.match)
	}

	for unmatchedSSN := range expectedMatches {
		t.Errorf("Expected to find: %s", unmatchedSSN)
	}
}
