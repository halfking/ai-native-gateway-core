package textsplit

import (
	"strings"
	"testing"
)

func TestSplitLeadingThink_Basic(t *testing.T) {
	think, rest, ok := SplitLeadingThink("<think>plan</think>\n\nHELLO")
	if !ok || think != "plan" || rest != "HELLO" {
		t.Errorf("got (%q, %q, %v), want (plan, HELLO, true)", think, rest, ok)
	}
}

func TestSplitLeadingThink_NoPrefix(t *testing.T) {
	think, rest, ok := SplitLeadingThink("just a normal response")
	if ok || think != "" || rest != "just a normal response" {
		t.Errorf("got (%q, %q, %v), want (, just a normal response, false)", think, rest, ok)
	}
}

func TestSplitLeadingThink_UnclosedThink(t *testing.T) {
	think, rest, ok := SplitLeadingThink("<think>never closes\ntext continues")
	if ok || think != "" || rest != "<think>never closes\ntext continues" {
		t.Errorf("unclosed think must be rejected; got (%q, %q, %v)", think, rest, ok)
	}
}

func TestSplitLeadingThink_ThinkNotAtStart(t *testing.T) {
	input := "intro line\n<think>late think</think>"
	think, rest, ok := SplitLeadingThink(input)
	if ok {
		t.Errorf("late <think> must not be promoted; got ok=true with think=%q rest=%q", think, rest)
	}
	if rest != input {
		t.Errorf("input must round-trip unchanged when <think> is not at start")
	}
}

func TestSplitLeadingThink_TruncatesHugeThinking(t *testing.T) {
	big := strings.Repeat("a", 5*1024*1024) // 5 MiB
	input := "<think>" + big + "</think>\nvisible"
	think, rest, ok := SplitLeadingThink(input)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if len(think) > MaxThinkBytes+10 {
		t.Errorf("thinking text exceeded cap: len=%d, cap=%d", len(think), MaxThinkBytes)
	}
	if !strings.HasSuffix(think, "…") {
		t.Errorf("truncated thinking should end with ellipsis; got last 5 bytes: %q", think[len(think)-5:])
	}
	if rest != "visible" {
		t.Errorf("rest should be unchanged after truncation; got %q", rest)
	}
}

func TestSplitLeadingThinkBounded_CustomCap(t *testing.T) {
	input := "<think>0123456789abcdef</think>"
	think, _, _ := SplitLeadingThinkBounded(input, 5)
	if len(think) > 10 { // 5 bytes + ellipsis overhead
		t.Errorf("custom cap not honored; got len=%d", len(think))
	}
}

func TestSplitLeadingThink_UTF8SafeTruncate(t *testing.T) {
	// CJK character: 3 bytes per rune. 700_000 runes = ~2.1 MiB.
	cjk := strings.Repeat("思", 700_000)
	input := "<think>" + cjk + "</think>"
	think, _, ok := SplitLeadingThink(input)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if !isValidUTF8(think) {
		t.Errorf("thinking contains invalid UTF-8 (rune cut mid-byte)")
	}
}

func TestSplitLeadingThink_EmptyInput(t *testing.T) {
	think, rest, ok := SplitLeadingThink("")
	if ok || think != "" || rest != "" {
		t.Errorf("empty input should return ok=false; got (%q, %q, %v)", think, rest, ok)
	}
}

func TestSplitLeadingThink_OnlyOpenTag(t *testing.T) {
	for _, input := range []string{"<", "<think>", "<think>\nhello\nworld\n"} {
		think, rest, ok := SplitLeadingThink(input)
		if ok {
			t.Errorf("input %q should NOT be promoted (no close tag); got think=%q rest=%q", input, think, rest)
		}
	}
}

// isValidUTF8 returns true iff s is valid UTF-8. Cheap shim for tests.
func isValidUTF8(s string) bool {
	for i := 0; i < len(s); {
		r, size := decodeRune([]byte(s[i:]))
		if r == 0xFFFD && size == 1 {
			return false
		}
		i += size
	}
	return true
}

func decodeRune(b []byte) (rune, int) {
	if len(b) == 0 {
		return 0xFFFD, 1
	}
	c := b[0]
	switch {
	case c < 0x80:
		return rune(c), 1
	case c < 0xC2:
		return 0xFFFD, 1
	case c < 0xE0:
		if len(b) < 2 {
			return 0xFFFD, 1
		}
		return rune(c&0x1F)<<6 | rune(b[1]&0x3F), 2
	case c < 0xF0:
		if len(b) < 3 {
			return 0xFFFD, 1
		}
		return rune(c&0x0F)<<12 | rune(b[1]&0x3F)<<6 | rune(b[2]&0x3F), 3
	default:
		if len(b) < 4 {
			return 0xFFFD, 1
		}
		return rune(c&0x07)<<18 | rune(b[1]&0x3F)<<12 | rune(b[2]&0x3F)<<6 | rune(b[3]&0x3F), 4
	}
}