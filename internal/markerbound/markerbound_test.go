package markerbound

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSpansRecognizesMarkerForms(t *testing.T) {
	cases := []struct {
		text  string
		want  int
		label string
	}{
		{"call {SENSITIVE:phone:1} now", 1, "canonical"},
		{"{SENSITIVE: server_ip : 1}", 1, "spaced"},
		{"{ｓｅｎｓｉｔｉｖｅ：secret：２}", 1, "fullwidth"},
		{"{sensitive:secret:1}", 1, "lowercase"},
		{"{SENSITIVE:server-ip:3}", 1, "dashed type"},
		{"SENSITIVE:phone:1 bare", 1, "bare form"},
		{"{SENSITIVE:secret:X}", 1, "degenerate echo"},
		{"trailing {SENSITIVE:phon", 1, "unterminated tail"},
		{"plain {hello world} braces", 0, "ordinary braces"},
		{"sensitive data ahead", 0, "prose word"},
		{"the sensitive:12345 field", 0, "prose colon"},
		{"", 0, "empty"},
	}
	for _, tc := range cases {
		if got := len(Spans(tc.text)); got != tc.want {
			t.Errorf("%s: Spans(%q) = %d spans, want %d", tc.label, tc.text, got, tc.want)
		}
	}
}

func TestSafeCutKeepHeadKeepsWholeMarker(t *testing.T) {
	text := "hello {SENSITIVE:phone:1} world"
	start := strings.Index(text, "{")
	end := strings.Index(text, "}") + 1
	for cut := start + 1; cut < end; cut++ {
		got := SafeCutKeepHead(text, cut)
		if got != end {
			t.Fatalf("cut %d: SafeCutKeepHead = %d, want span end %d", cut, got, end)
		}
		if kept := text[:got]; !strings.Contains(kept, "{SENSITIVE:phone:1}") {
			t.Fatalf("cut %d: kept text does not hold the whole marker: %q", cut, kept)
		}
	}
	if got := SafeCutKeepHead(text, start); got != start {
		t.Errorf("cut at span start excludes the marker wholly; must not move: got %d want %d", got, start)
	}
	if got := SafeCutKeepHead(text, end); got != end {
		t.Errorf("cut at span end must not move: got %d want %d", got, end)
	}
	if got := SafeCutKeepHead(text, 3); got != 3 {
		t.Errorf("cut outside any span must not move: got %d want 3", got)
	}
}

func TestSafeCutKeepTailKeepsWholeMarker(t *testing.T) {
	text := "hello {SENSITIVE:phone:1} world"
	start := strings.Index(text, "{")
	end := strings.Index(text, "}") + 1
	for cut := start + 1; cut < end; cut++ {
		got := SafeCutKeepTail(text, cut)
		if got != start {
			t.Fatalf("cut %d: SafeCutKeepTail = %d, want span start %d", cut, got, start)
		}
		if kept := text[got:]; strings.Contains(kept, "phone:1}") && !strings.Contains(kept, "{SENSITIVE") {
			t.Fatalf("cut %d: kept text starts with a marker fragment: %q", cut, kept)
		}
	}
}

func TestSafeCutKeepHeadMutatedAndDegenerateSpans(t *testing.T) {
	for _, text := range []string{
		"前缀 {SENSITIVE: server_ip : 1} 后缀",
		"前缀 {sensitive:secret:X} 后缀",
		"前缀 ｛ＳＥＮＳＩＴＩＶＥ：secret：１｝ 后缀",
		"前缀 {SENSITIVE:phon",
	} {
		span := Spans(text)[0]
		for cut := span[0] + 1; cut < span[1]; cut++ {
			got := SafeCutKeepHead(text, cut)
			if got != span[1] {
				t.Fatalf("%q cut %d: got %d, want %d", text, cut, got, span[1])
			}
		}
	}
}

func TestSafeCutKeepTailRunesMultibyte(t *testing.T) {
	// "中文{SENSITIVE:phone:1}尾部" — cut in the middle of the marker counted
	// in runes must move to the marker start, and the result must stay valid
	// UTF-8.
	text := "中文{SENSITIVE:phone:1}尾部"
	start := strings.Index(text, "{")
	cutRunes := utf8.RuneCountInString(text[:start]) + 4 // inside the marker
	got := SafeCutKeepTailRunes(text, cutRunes)
	if got != start {
		t.Fatalf("SafeCutKeepTailRunes = %d, want marker start %d", got, start)
	}
	if !utf8.ValidString(text[got:]) {
		t.Fatalf("kept tail is not valid UTF-8: %q", text[got:])
	}
	// A rune cut outside any marker passes through byte-exact.
	if got := SafeCutKeepTailRunes(text, 1); got != 3 {
		t.Errorf("rune cut 1 = byte %d, want 3", got)
	}
	if got := SafeCutKeepTailRunes(text, 0); got != 0 {
		t.Errorf("rune cut 0 must stay 0, got %d", got)
	}
}

func TestAdjustNeverLeavesCutInsideSpan(t *testing.T) {
	texts := []string{
		"a{SENSITIVE:phone:1}b{SENSITIVE:email:2}c",
		"{SENSITIVE:secret:1}",
		"x{ sensitive : id_card : 07 }y",
		"bare SENSITIVE:phone:1 tail",
		"{SENSITIVE:phon",
		"中文{SENSITIVE:secret:1}中文{SENSITIVE:secret:2}中文",
	}
	for _, text := range texts {
		spans := Spans(text)
		if len(spans) == 0 {
			t.Fatalf("corpus %q unexpectedly has no spans", text)
		}
		for cut := 0; cut <= len(text); cut++ {
			for _, fn := range []struct {
				name  string
				apply func(string, int) int
			}{{"head", SafeCutKeepHead}, {"tail", SafeCutKeepTail}} {
				got := fn.apply(text, cut)
				for _, span := range spans {
					if span[0] < got && got < span[1] {
						t.Fatalf("%q cut %d via %s: adjusted cut %d still inside span %v", text, cut, fn.name, got, span)
					}
				}
			}
		}
	}
}
