package executors

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Byte-slicing a string splits multi-byte runes and produces invalid UTF-8.
// PostgreSQL rejects such values with SQLSTATE 22021 and drops the entire row —
// incident 2026-06-11, which manifested as missing request_logs rows for
// Chinese and emoji traffic.
//
// The limits swept below deliberately cross the real call-site caps (200, 320,
// 1024) with input long enough to exceed them: a fixture shorter than the cap
// never truncates and would prove nothing.
func TestTruncateUTF8_NeverSplitsRunes(t *testing.T) {
	inputs := []string{
		strings.Repeat("中", 400), // 1200 bytes, crosses 1024 mid-rune
		strings.Repeat("🙂", 400), // 1600 bytes, 4-byte runes
		"abc" + strings.Repeat("中", 400),
		strings.Repeat("a", 2000), // pure ASCII must be unaffected
		"中文abc🙂mixed" + strings.Repeat("中", 400),
	}
	limits := []int{0, 1, 2, 3, 4, 5, 199, 200, 201, 319, 320, 321, 1023, 1024, 1025}
	for _, in := range inputs {
		for _, limit := range limits {
			got := truncateUTF8(in, limit)
			if !utf8.ValidString(got) {
				t.Fatalf("invalid UTF-8 for limit=%d input=%.20q: %q", limit, in, got)
			}
			if len(got) > limit {
				t.Fatalf("exceeded limit=%d: got %d bytes", limit, len(got))
			}
			if !strings.HasPrefix(in, got) {
				t.Fatalf("result is not a prefix of the input: %q", got)
			}
		}
	}
}

// Guards the specific regression: the previous implementation sliced bytes
// directly, so a value crossing the cap mid-rune became invalid UTF-8. The
// first assertion fails if the fixture stops reproducing the original bug.
func TestTruncateUTF8_FixesByteSliceRegression(t *testing.T) {
	s := strings.Repeat("中", 400) // 1200 bytes; 1024 % 3 == 1 splits a rune
	if utf8.ValidString(s[:1024]) {
		t.Fatal("fixture no longer reproduces the split-rune case")
	}
	if got := truncateUTF8(s, 1024); !utf8.ValidString(got) {
		t.Fatalf("truncateUTF8 still produces invalid UTF-8: %q", got)
	}
}

func TestTruncateUTF8_ShortInputUnchanged(t *testing.T) {
	in := "中文"
	if got := truncateUTF8(in, 1024); got != in {
		t.Errorf("short input was modified: %q", got)
	}
}
