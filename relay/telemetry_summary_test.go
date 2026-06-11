package relay

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTruncateText_NoInvalidUTF8WhenCuttingChinese reproduces the 2026-06-11
// incident: previously truncateText used s[:limit-1] which split multi-byte
// Chinese characters mid-rune, producing byte sequences that PostgreSQL
// rejected with SQLSTATE 22021 (invalid UTF8: 0xe5 0xbc 0xe2 ...).  The
// truncated output MUST always be valid UTF-8.
func TestTruncateText_NoInvalidUTF8WhenCuttingChinese(t *testing.T) {
	// Build a string that, when cut at byte 319 (limit-1 with limit=320),
	// lands inside a 3-byte Chinese character.  '弊' is 0xE5 0xBC 0x8A —
	// exactly the prefix seen in the production error log.
	prefix := strings.Repeat("a", 318)
	input := prefix + "弊弊弊弊" + strings.Repeat("b", 100)

	got := truncateText(input, 320)

	if !utf8.ValidString(got) {
		t.Fatalf("truncateText returned invalid UTF-8: %q (bytes: %x)", got, []byte(got))
	}
	if len(got) > 320 {
		t.Fatalf("truncateText exceeded limit: len=%d limit=320", len(got))
	}
}

func TestTruncateText_NoTruncationWhenWithinLimit(t *testing.T) {
	input := "hello 世界"
	got := truncateText(input, 320)
	if got != input {
		t.Fatalf("expected unchanged %q, got %q", input, got)
	}
}

func TestTruncateText_PreservesAllRunesWhenJustOver(t *testing.T) {
	// One 3-byte rune beyond limit forces truncation; result must still be
	// valid UTF-8 and end with the ellipsis.
	input := strings.Repeat("a", 318) + "弊"
	got := truncateText(input, 320)
	if !utf8.ValidString(got) {
		t.Fatalf("invalid UTF-8: %q (bytes: %x)", got, []byte(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected ellipsis suffix, got %q", got)
	}
}

func TestTruncateText_AllChineseInput(t *testing.T) {
	// 100 Chinese characters * 3 bytes = 300 bytes.  Truncate to 50 bytes.
	input := strings.Repeat("中", 100)
	got := truncateText(input, 50)
	if !utf8.ValidString(got) {
		t.Fatalf("invalid UTF-8: %q (bytes: %x)", got, []byte(got))
	}
	if len(got) > 50 {
		t.Fatalf("exceeded limit: len=%d limit=50", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected ellipsis suffix, got %q", got)
	}
}
