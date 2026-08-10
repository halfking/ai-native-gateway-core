package v2

import (
	"testing"
)

// TestSummarizeMessages_PreviewForTurnTitle verifies the deterministic preview
// used to populate session_turns.title / .summary on the write path. The admin
// turns-list UI renders these one-liners, so an empty / wrong shape here would
// make every turn row show "(无请求摘要)".
func TestSummarizeMessages_PreviewForTurnTitle(t *testing.T) {
	cases := []struct {
		name string
		in   []Message
		want string
	}{
		{"empty", nil, ""},
		{"single user message", []Message{{Role: "user", Content: "帮我把这段代码改成 Go"}}, "帮我把这段代码改成 Go"},
		{
			"multi-message suffix",
			[]Message{{Role: "user", Content: "第一轮"}, {Role: "assistant", Content: "好的"}},
			"第一轮 (2 messages)",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := summarizeMessages(c.in)
			if got != c.want {
				t.Fatalf("summarizeMessages(%+v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestSummarizeMessages_TruncatesLongContent guards the 200-char cap so a
// megabyte system prompt can never blow up the session_turns.title column.
func TestSummarizeMessages_TruncatesLongContent(t *testing.T) {
	long := make([]byte, 5000)
	for i := range long {
		long[i] = 'x'
	}
	got := summarizeMessages([]Message{{Role: "user", Content: string(long)}})
	if len(got) > 204 {
		t.Fatalf("expected <= 204 chars (200 + ellipsis), got %d", len(got))
	}
}

// TestTurnRecord_TitleSummaryFields is a compile-time guard: the new Title /
// Summary fields on TurnRecord are read by turn_writer's INSERT path and must
// not be silently dropped by future struct refactors.
func TestTurnRecord_TitleSummaryFields(t *testing.T) {
	rec := TurnRecord{Title: "t", Summary: "s"}
	if rec.Title != "t" || rec.Summary != "s" {
		t.Fatalf("TurnRecord title/summary fields not retained: %+v", rec)
	}
}
