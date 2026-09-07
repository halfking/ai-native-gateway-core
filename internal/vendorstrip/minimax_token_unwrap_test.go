package strip

import (
	"strings"
	"testing"
)

func TestUnwrapMiniMaxTokenWrappers(t *testing.T) {
	in := `minimax[>[<tool_call> ]<]minimax[>[]<]minimax[>[ssh ls]<]minimax[>[]<]minimax[>[Check nginx]<]minimax[>[</tool_call>`
	got := UnwrapMiniMaxTokenWrappers(in)
	want := `<tool_call> ssh lsCheck nginx</tool_call>`
	if got != want {
		t.Fatalf("unwrap = %q, want %q", got, want)
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
