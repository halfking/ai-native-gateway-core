package audit

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSafeTruncateUTF8_NeverProducesInvalidSequence(t *testing.T) {
	tests := []struct {
		name  string
		input string
		limit int
	}{
		{"cut inside Chinese char", strings.Repeat("a", 318) + "弊弊弊", 320},
		{"cut exactly on rune boundary", strings.Repeat("a", 319) + "弊", 322},
		{"all Chinese 100 chars", strings.Repeat("中", 100), 50},
		{"limit 0", "hello", 0},
		{"limit 1", "hello", 1},
		{"limit 2 inside emoji", "🎉🎉", 3},
		{"empty string", "", 10},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := safeTruncateUTF8(tc.input, tc.limit)
			if !utf8.ValidString(got) {
				t.Errorf("invalid UTF-8 for input=%q limit=%d: got=%q (bytes=%x)",
					tc.input, tc.limit, got, []byte(got))
			}
			if len(got) > tc.limit {
				t.Errorf("exceeded limit: len=%d limit=%d got=%q",
					len(got), tc.limit, got)
			}
		})
	}
}

func TestAppendText_RespectsMaxTextContentBytesWithUTF8(t *testing.T) {
	sc := NewStreamCapture()
	// Fill up to maxTextContentBytes - 1, then append a multi-byte rune.
	sc.textContent = make([]byte, maxTextContentBytes-1)
	for i := range sc.textContent {
		sc.textContent[i] = 'a'
	}
	sc.appendText("弊弊弊")
	if !utf8.ValidString(string(sc.textContent)) {
		t.Fatalf("textContent contains invalid UTF-8: %x", sc.textContent[maxTextContentBytes-4:])
	}
	if len(sc.textContent) > maxTextContentBytes {
		t.Fatalf("textContent exceeds cap: %d > %d", len(sc.textContent), maxTextContentBytes)
	}
}

func TestObservePayload_PreviewIsAlwaysValidUTF8(t *testing.T) {
	sc := NewStreamCapture()
	// Fill preview to near-2048 boundary with single-byte chars, then
	// push multi-byte chars that would straddle the boundary.
	for i := 0; i < 204; i++ {
		sc.ObservePayload(strings.Repeat("a", 10), "", false)
	}
	sc.ObservePayload("弊弊弊弊弊弊弊弊", "", false)
	if len(sc.preview) > 0 && !utf8.ValidString(string(sc.preview)) {
		t.Fatalf("preview contains invalid UTF-8: %x", sc.preview[2040:])
	}
}

func TestMessageDigest_PreviewIsAlwaysValidUTF8(t *testing.T) {
	// Build a messages JSON array with Chinese content that, when truncated
	// at byte position 200, would land inside a multi-byte rune.
	messages := `[{"role":"user","content":"` + strings.Repeat("中", 100) + `"}]`
	_, preview := MessageDigest([]byte(messages))
	if !utf8.ValidString(preview) {
		t.Fatalf("MessageDigest preview is invalid UTF-8: %q (bytes: %x)", preview, []byte(preview))
	}
}
