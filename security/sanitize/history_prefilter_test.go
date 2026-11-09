package sanitize

import (
	"context"
	"errors"
	"io"
	"regexp"
	"regexp/syntax"
	"strings"
	"testing"
)

func TestHistoryPrefilterCannotExcludeActualMatches(t *testing.T) {
	patterns := []string{`secret`, `(?i)secret`, `(?i)key`, `(?i)S`, `(?i)K`, `(?i)password`, `[1-9][0-9]+`, `a?b`, `a*b`, `a+b`, `a{0,3}b`, `(?:a|bc)def`, `(?:x*|bc)`, `(?:|bc)`, `[^x]+`, `日本語`, `(?i)Σ`, `(?i)[a-z]`, `\btoken\b`, `(?i)"(?:password|username)"\s*:`, `(?:AB|12|密码)`}
	texts := []string{"secret", "SECRET", "ſecret", "Key", "KEY", "S", "ſ", "k", "K", "password", "PASSWORD", "paſſword", "1234", "b", "aaab", "abcdef", "bcdef", "", "anything", "日本語", "σ", "ς", "Σ", "token", `{"password":"value"}`, "密码"}
	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		ast, err := syntax.Parse(pattern, syntax.Perl)
		if err != nil {
			t.Fatal(err)
		}
		for _, text := range texts {
			if !re.MatchString(text) {
				continue
			}
			for _, prefix := range []int{0, 1, 127, 128, 129, (32 << 10) - 1, (32 << 10) + 126, 64 << 10} {
				source := strings.Repeat("x", prefix) + text + strings.Repeat("x", 300)
				possible, err := historyMayMatch(context.Background(), func() io.Reader { return strings.NewReader(source) }, historyRequiredTerms(ast))
				if err != nil || !possible {
					t.Fatalf("prefilter excluded %q matching %q at boundary %d: %v", text, pattern, prefix, err)
				}
			}
		}
	}
}

func TestHistoryPrefilterBoundsMemoryAndRetainsFailures(t *testing.T) {
	for _, size := range []int{0, 1, 127, 128, 129, 32<<10 + 128, 100_000} {
		safe, err := historyMayMatch(context.Background(), func() io.Reader { return strings.NewReader(strings.Repeat("x", size)) }, [][]byte{[]byte("absent")})
		if safe || err != nil {
			t.Fatalf("negative full scan size=%d possible=%v err=%v", size, safe, err)
		}
	}
	want := errors.New("source lost")
	if possible, err := historyMayMatch(context.Background(), func() io.Reader { return historyFailReader{want} }, [][]byte{[]byte("missing")}); possible || !errors.Is(err, want) {
		t.Fatalf("read error lost: %v %v", possible, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if possible, err := historyMayMatch(ctx, func() io.Reader { return strings.NewReader("safe") }, [][]byte{[]byte("missing")}); possible || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel lost: %v %v", possible, err)
	}
}
