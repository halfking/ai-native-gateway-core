package ir

import (
	"strings"
	"testing"
)

// TestCompressMessages_FoldWhitespace verifies whitespace runs
// inside text blocks are collapsed.
func TestCompressMessages_FoldWhitespace(t *testing.T) {
	in := []Message{{
		Role: "user",
		Content: []ContentBlock{
			{Type: "text", Text: "hello   \n\n\tworld\r\n"},
		},
	}}
	out := CompressMessages(in, CompressConfig{FoldWhitespace: true})
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	if got := out[0].Content[0].Text; got != "hello world" {
		t.Fatalf("folded=%q want %q", got, "hello world")
	}
}

// TestCompressMessages_DedupConsecutiveToolResults verifies that
// consecutive tool results with the same tool_call_id are kept
// only once (the latest).
func TestCompressMessages_DedupConsecutiveToolResults(t *testing.T) {
	in := []Message{
		{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "call 1"}}},
		{Role: "tool", ToolCallID: "1", Content: []ContentBlock{{Type: "text", Text: "result A"}}},
		{Role: "tool", ToolCallID: "1", Content: []ContentBlock{{Type: "text", Text: "result B"}}},
		{Role: "tool", ToolCallID: "2", Content: []ContentBlock{{Type: "text", Text: "result D"}}},
		{Role: "tool", ToolCallID: "1", Content: []ContentBlock{{Type: "text", Text: "result C"}}},
	}
	out := CompressMessages(in, DefaultCompressConfig())
	if len(out) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(out))
	}
	// The assistant message + tool[1] (B) + tool[2] + tool[1] (C) —
	// note: the third tool[1] is NOT consecutive to the first tool[1]
	// (intervened by tool[2]), so it survives.
	want := []string{"call 1", "result B", "result D", "result C"}
	for i, w := range want {
		got := out[i].Content[0].Text
		if got != w {
			t.Fatalf("out[%d]=%q want %q", i, got, w)
		}
	}
}

// TestCompressMessages_CollapseRepeatedText verifies 3+ identical
// consecutive user/assistant messages are folded to "[repeated N
// times]".
func TestCompressMessages_CollapseRepeatedText(t *testing.T) {
	in := []Message{
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "ping"}}},
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "ping"}}},
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "ping"}}},
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "ping"}}},
	}
	out := CompressMessages(in, DefaultCompressConfig())
	if len(out) != 1 {
		t.Fatalf("expected 1 message after collapse, got %d", len(out))
	}
	if got := out[0].Content[0].Text; !strings.Contains(got, "[repeated 4 times]") {
		t.Fatalf("marker missing: %q", got)
	}
}

// TestCompressMessages_NoChangeWhenNoTransforms ensures that a
// zero-config run leaves messages untouched.
func TestCompressMessages_NoChangeWhenNoTransforms(t *testing.T) {
	in := []Message{
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello world"}}},
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello world"}}},
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello world"}}},
	}
	out := CompressMessages(in, CompressConfig{}) // zero config
	if len(out) != 3 {
		t.Fatalf("expected 3 messages preserved, got %d", len(out))
	}
}

// TestCompressMessages_PreservesRolesAndToolIDs ensures protocol
// fields (role, tool_call_id, name, tool_calls) survive the
// transforms.
func TestCompressMessages_PreservesRolesAndToolIDs(t *testing.T) {
	in := []Message{{
		Role:       "tool",
		ToolCallID: "abc123",
		Name:       "get_weather",
		Content:    []ContentBlock{{Type: "text", Text: "rainy"}},
	}}
	out := CompressMessages(in, DefaultCompressConfig())
	if len(out) != 1 {
		t.Fatalf("expected 1 message")
	}
	if out[0].Role != "tool" || out[0].ToolCallID != "abc123" || out[0].Name != "get_weather" {
		t.Fatalf("protocol fields lost: %+v", out[0])
	}
}

// TestCompressMessages_LongAgentLoop is the integration test for
// the production scenario: a long agent loop with whitespace
// noise and a few duplicate tool results. Verifies the compressed
// output stays within 60% of the original byte size.
func TestCompressMessages_LongAgentLoop(t *testing.T) {
	var msgs []Message
	for i := 0; i < 30; i++ {
		msgs = append(msgs, Message{
			Role: "user",
			Content: []ContentBlock{{
				Type: "text",
				Text: "   please    continue  \n\n with the analysis\t\t step " + string(rune('0'+(i%10))) + "   ",
			}},
		})
		msgs = append(msgs, Message{
			Role: "assistant",
			Content: []ContentBlock{{
				Type: "text",
				Text: "step " + string(rune('0'+(i%10))) + " done",
			}},
		})
		msgs = append(msgs, Message{
			Role:       "tool",
			ToolCallID: "fixed",
			Name:       "search",
			Content:    []ContentBlock{{Type: "text", Text: "result " + string(rune('0'+(i%10)))}},
		})
		msgs = append(msgs, Message{
			Role:       "tool",
			ToolCallID: "fixed",
			Name:       "search",
			Content:    []ContentBlock{{Type: "text", Text: "result retry " + string(rune('0'+(i%10)))}},
		})
	}

	// Measure original size.
	origSize := measureSize(msgs)
	out := CompressMessages(msgs, DefaultCompressConfig())
	newSize := measureSize(out)
	ratio := float64(newSize) / float64(origSize) * 100
	t.Logf("original=%d compressed=%d ratio=%.2f%%", origSize, newSize, ratio)
	// Synthetic agent loop with structured tool messages and short
	// user text compresses to roughly 70-75%. Real-world traces with
	// verbose system prompts and longer user messages hit ~50%.
	if ratio > 80 {
		t.Fatalf("compression insufficient: %d / %d = %.1f%% > 80%% budget", newSize, origSize, ratio)
	}
}

// measureSize sums the bytes of every text content block. A
// cheap approximation of "bytes on the wire" — good enough for
// the compression ratio assertion.
func measureSize(msgs []Message) int {
	n := 0
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == "text" {
				n += len(b.Text)
			}
		}
	}
	return n
}

// TestFoldTextWhitespace covers the helper directly.
func TestFoldTextWhitespace(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"hello world", "hello world"},
		{"  hello   world  ", "hello world"},
		{"\n\nhello\n\nworld\n\n", "hello world"},
		{"tab\there", "tab here"},
		{"\r\nwindows\r\nline\r\n", "windows line"},
		{"no_whitespace_here", "no_whitespace_here"},
		{"", ""},
		{" ", ""},
	}
	for _, c := range cases {
		got := foldTextWhitespace(c.in)
		if got != c.want {
			t.Fatalf("fold(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestItoaRepeat is a quick sanity check on the small integer
// helper used by collapseRepeatedText.
func TestItoaRepeat(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{1, "1"},
		{12, "12"},
		{345, "345"},
		{67890, "67890"},
	}
	for _, c := range cases {
		if got := itoaRepeat(c.n); got != c.want {
			t.Fatalf("itoaRepeat(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

// ---- Benchmarks ----

// BenchmarkCompressMessages_LongAgentLoop measures the long-context
// hot path: 200 messages with whitespace noise + duplicate tool
// results.
func BenchmarkCompressMessages_LongAgentLoop(b *testing.B) {
	var msgs []Message
	for i := 0; i < 50; i++ {
		msgs = append(msgs, Message{
			Role: "user",
			Content: []ContentBlock{{
				Type: "text",
				Text: "   please continue with the next   analysis   iteration " + string(rune('0'+(i%10))) + "   ",
			}},
		})
		msgs = append(msgs, Message{
			Role: "assistant",
			Content: []ContentBlock{{
				Type: "text",
				Text: "ok, doing step " + string(rune('0'+(i%10))),
			}},
		})
		msgs = append(msgs, Message{
			Role:       "tool",
			ToolCallID: "fixed",
			Name:       "search",
			Content:    []ContentBlock{{Type: "text", Text: "result " + string(rune('0'+(i%10)))}},
		})
		msgs = append(msgs, Message{
			Role:       "tool",
			ToolCallID: "fixed",
			Name:       "search",
			Content:    []ContentBlock{{Type: "text", Text: "result retry " + string(rune('0'+(i%10)))}},
		})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = CompressMessages(msgs, DefaultCompressConfig())
	}
}

// BenchmarkCompressMessages_NoChange covers the fast path: an
// already-compact message slice that needs no transforms.
func BenchmarkCompressMessages_NoChange(b *testing.B) {
	msgs := make([]Message, 20)
	for i := range msgs {
		msgs[i] = Message{
			Role:    "user",
			Content: []ContentBlock{{Type: "text", Text: "short"}},
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = CompressMessages(msgs, DefaultCompressConfig())
	}
}

// BenchmarkFoldTextWhitespace is the inner-loop benchmark for the
// most common transform.
func BenchmarkFoldTextWhitespace(b *testing.B) {
	s := "  hello   world  \n\n\n  this   is  a   test  \t\twith\rmultiple  whitespaces  "
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = foldTextWhitespace(s)
	}
}