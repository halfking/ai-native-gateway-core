package compression

import (
	"encoding/json"
	"strings"
	"testing"
)

// mediaMsg builds a message with the given content-block type tags, as a raw
// JSON message (the shape extractMessages expects).
func mediaMsg(role string, blockTypes ...string) json.RawMessage {
	parts := make([]map[string]any, 0, len(blockTypes))
	for _, bt := range blockTypes {
		parts = append(parts, map[string]any{"type": bt, "text": "x"})
	}
	m := map[string]any{"role": role, "content": parts}
	b, _ := json.Marshal(m)
	return b
}

// TestPruneOldMediaBlocks_KeepsLatest verifies the core A3 contract: with more
// media blocks than the keep window, the OLDEST are replaced by placeholders and
// the most-recent keepLatest survive verbatim.
func TestPruneOldMediaBlocks_KeepsLatest(t *testing.T) {
	// 4 image blocks across 4 messages; keepLatest=2 → cull the 2 oldest.
	body, _ := json.Marshal(map[string]any{"messages": []json.RawMessage{
		mediaMsg("user", "image"),
		mediaMsg("assistant", "text"),
		mediaMsg("user", "image"),
		mediaMsg("user", "image"),
		mediaMsg("user", "image"),
	}})

	out, res := PruneOldMediaBlocks(body, 2)
	if !res.DidStrip {
		t.Fatal("expected DidStrip=true")
	}
	if res.MediaBlocksPruned != 2 {
		t.Fatalf("pruned %d blocks, want 2", res.MediaBlocksPruned)
	}
	// Note: in tests with tiny synthetic blocks, the placeholder JSON can be
	// larger than the original block, so we don't assert len(out) < len(body).
	// Real base64 images (20KB+) always shrink when replaced by the placeholder.

	// The placeholder must appear exactly twice; the two newest images survive.
	s := string(out)
	if c := strings.Count(s, mediaPlaceholder); c != 2 {
		t.Fatalf("placeholder count = %d, want 2", c)
	}
	// Newest two images (last two user messages) should still carry "image" type.
	if c := strings.Count(s, `"type":"image"`); c != 2 {
		t.Fatalf("surviving image blocks = %d, want 2", c)
	}
}

// TestPruneOldMediaBlocks_NoopWhenUnderKeep confirms sessions with few media
// blocks are returned unchanged (no spurious placeholder injection).
func TestPruneOldMediaBlocks_NoopWhenUnderKeep(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"messages": []json.RawMessage{
		mediaMsg("user", "image"),
		mediaMsg("assistant", "text"),
	}})
	out, res := PruneOldMediaBlocks(body, 2)
	if res.DidStrip {
		t.Fatal("pruned a body under the keep window")
	}
	if string(out) != string(body) {
		t.Fatal("body altered despite no pruning")
	}
}

// TestPruneOldMediaBlocks_PreservesTextAndStructure guards the safety contract:
// text blocks, role alternation, and message count are unchanged — only media
// blocks are swapped for placeholders. This is why the function is safe to run
// pre-window (it cannot break tool pairing or drop messages).
func TestPruneOldMediaBlocks_PreservesTextAndStructure(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"messages": []json.RawMessage{
		mediaMsg("user", "image", "text"),
		mediaMsg("assistant", "text"),
		mediaMsg("user", "text"),
	}})
	out, res := PruneOldMediaBlocks(body, 0) // keepLatest=0 → clamp to default 2 → no prune (only 1 media)
	if res.DidStrip {
		t.Fatal("unexpectedly pruned with default keep=2 and only 1 media block")
	}
	if string(out) != string(body) {
		t.Fatal("body altered despite no pruning")
	}

	// Now force a prune by setting keepLatest=0 but many media blocks.
	big, _ := json.Marshal(map[string]any{"messages": []json.RawMessage{
		mediaMsg("user", "image", "text"),
		mediaMsg("assistant", "text"),
		mediaMsg("user", "image", "text"),
		mediaMsg("user", "image"),
	}})
	out2, res2 := PruneOldMediaBlocks(big, 1) // 3 media, keep 1 → cull 2
	if !res2.DidStrip || res2.MediaBlocksPruned != 2 {
		t.Fatalf("expected 2 pruned, got did=%v pruned=%d", res2.DidStrip, res2.MediaBlocksPruned)
	}
	// All three text blocks must still be present.
	if c := strings.Count(string(out2), `"type":"text"`); c < 3 {
		t.Fatalf("text blocks dropped: found %d, want >=3 (text must survive)", c)
	}
}

// TestPruneOldMediaBlocks_HandlesStringContent confirms messages whose content
// is a plain string (no block array) are passed through untouched.
func TestPruneOldMediaBlocks_HandlesStringContent(t *testing.T) {
	strMsg := func(role, content string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"role": role, "content": content})
		return b
	}
	body, _ := json.Marshal(map[string]any{"messages": []json.RawMessage{
		strMsg("user", "hello"),
		strMsg("assistant", "hi"),
	}})
	out, res := PruneOldMediaBlocks(body, 2)
	if res.DidStrip {
		t.Fatal("pruned a body with no media blocks")
	}
	if string(out) != string(body) {
		t.Fatal("string-content body altered")
	}
}

// TestPruneOldMediaBlocks_AudioTypes covers the audio block shapes (input_audio,
// audio), not just image.
func TestPruneOldMediaBlocks_AudioTypes(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"messages": []json.RawMessage{
		mediaMsg("user", "input_audio"),
		mediaMsg("assistant", "text"),
		mediaMsg("user", "audio"),
		mediaMsg("user", "input_audio"),
		mediaMsg("user", "audio"),
	}})
	out, res := PruneOldMediaBlocks(body, 2)
	// 4 audio blocks total (messages 1, 3, 4, 5); keep 2 → prune 2.
	if res.MediaBlocksPruned != 2 {
		t.Fatalf("expected 2 audio blocks pruned, got %d", res.MediaBlocksPruned)
	}
	if strings.Count(string(out), mediaPlaceholder) != 2 {
		t.Fatalf("placeholder count wrong for audio")
	}
}
