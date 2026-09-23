package strip

import (
	"strings"
	"testing"
)

func TestUnwrapMiniMaxTokenWrappers(t *testing.T) {
	in := `minimax[>[<tool_call> ]<]minimax[>[]<]minimax[>[ssh ls]<]minimax[>[]<]minimax[>[Check nginx]<]minimax[>[ ]<]minimax[>[</tool_call>`
	got := UnwrapMiniMaxTokenWrappers(in)
	// 2026-09-23: wrapper boundaries are field separators — adjacent
	// non-empty payloads join with a single space instead of merging
	// ("lsCheck" → "ls Check"), so a recovered tool-call payload keeps
	// its command/description structure.
	want := `<tool_call> ssh ls Check nginx </tool_call>`
	if got != want {
		t.Fatalf("unwrap = %q, want %q", got, want)
	}
}

func TestUnwrapMiniMaxTokenWrappers_SingleContinuousPayload(t *testing.T) {
	// A payload wrapped as one unit (no field split) round-trips unchanged.
	in := `minimax[>[hello world]<]minimax[>[]<]`
	if got := UnwrapMiniMaxTokenWrappers(in); got != "hello world" {
		t.Fatalf("continuous payload changed: %q", got)
	}
}

func TestUnwrapMiniMaxTokenWrappers_UnclosedWrapper(t *testing.T) {
	in := `minimax[>[dangling`
	if got := UnwrapMiniMaxTokenWrappers(in); got != "dangling" {
		t.Fatalf("unclosed wrapper not preserved: %q", got)
	}
}

func TestUnwrapMiniMaxTokenWrappers_LeavesPlainText(t *testing.T) {
	in := "ordinary assistant text"
	if got := UnwrapMiniMaxTokenWrappers(in); got != in {
		t.Fatalf("plain text changed: %q", got)
	}
}

func TestCleanMinimaxLeakFields_UnwrapsTokenWrappers(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"minimax[>[<tool_call> ssh]<]minimax[>[</tool_call>"}}]}`)
	got := DefaultRegistry.Strip(body, VendorMiniMax)
	if strings.Contains(string(got), "minimax[>[") {
		t.Fatalf("wrapper leaked: %s", got)
	}
	if !strings.Contains(string(got), "tool_call") || !strings.Contains(string(got), "ssh") {
		t.Fatalf("unwrapped content missing: %s", got)
	}
}
