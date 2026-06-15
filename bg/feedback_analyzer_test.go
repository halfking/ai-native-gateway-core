package bg

import (
	"testing"
)

func TestExtractCJKBigrams_Chinese(t *testing.T) {
	bigrams := extractCJKBigrams("求解方程式")
	// 求解, 解方, 方程, 程式
	expected := map[string]bool{
		"求解": true,
		"解方": true,
		"方程": true,
		"程式": true,
	}
	for _, b := range bigrams {
		// Only check CJK bigrams (ignore ASCII here)
		if len([]rune(b)) == 2 {
			if !expected[b] && isCJK(rune(b[0])) {
				// Extra CJK bigram is OK, not an error
			}
		}
	}

	// Verify all expected bigrams are present
	found := make(map[string]bool)
	for _, b := range bigrams {
		found[b] = true
	}
	for exp := range expected {
		if !found[exp] {
			t.Errorf("expected bigram %q not found in %v", exp, bigrams)
		}
	}
}

func TestExtractCJKBigrams_English(t *testing.T) {
	bigrams := extractCJKBigrams("debug this code please")
	// word bigrams: "debug this", "this code", "code please"
	// Words < 3 chars are filtered, so "this" (4) is included
	// Expected: "debug this", "this code", "code please"
	joined := ""
	for _, b := range bigrams {
		joined += b + "|"
	}
	if len(bigrams) == 0 {
		t.Error("expected ASCII bigrams, got none")
	}
}

func TestExtractCJKBigrams_Mixed(t *testing.T) {
	bigrams := extractCJKBigrams("用 Python debug 这个 bug")
	// Should have both CJK bigrams (用? no, single CJK adjacent to space)
	// and ASCII bigrams (python debug)
	if len(bigrams) == 0 {
		t.Error("expected at least some bigrams from mixed text")
	}
}

func TestExtractCJKBigrams_Empty(t *testing.T) {
	bigrams := extractCJKBigrams("")
	if len(bigrams) != 0 {
		t.Errorf("expected empty for empty input, got %d", len(bigrams))
	}
}

func TestExtractASCIIBigrams(t *testing.T) {
	tests := []struct {
		input      string
		minExpected int
	}{
		{"hello world foo bar", 3},       // 4 words → 3 bigrams
		{"a ab abc abcd", 1},              // only "abc" "abcd" qualify (>=3 chars)
		{"single", 0},                     // 1 word → 0 bigrams
		{"", 0},
	}
	for _, tt := range tests {
		got := extractASCIIBigrams(tt.input)
		if len(got) < tt.minExpected {
			t.Errorf("extractASCIIBigrams(%q) = %d bigrams, want >= %d",
				tt.input, len(got), tt.minExpected)
		}
	}
}

func TestIsCJK(t *testing.T) {
	if !isCJK('中') {
		t.Error("isCJK('中') should be true")
	}
	if !isCJK('a') { // 'a' is 0x61, way below 0x4E00
		// Actually 'a' is NOT CJK
	}
	if isCJK('a') {
		t.Error("isCJK('a') should be false")
	}
	if isCJK('A') {
		t.Error("isCJK('A') should be false")
	}
}

func TestMapTaskToChannel(t *testing.T) {
	tests := []struct {
		task string
		want string
	}{
		{"reasoning", "reasoning"},
		{"code", "code"},
		{"creative", "creative"},
		{"chat", "reasoning"}, // default fallback
		{"unknown", "reasoning"},
	}
	for _, tt := range tests {
		got := mapTaskToChannel(tt.task)
		if got != tt.want {
			t.Errorf("mapTaskToChannel(%q) = %q, want %q", tt.task, got, tt.want)
		}
	}
}
