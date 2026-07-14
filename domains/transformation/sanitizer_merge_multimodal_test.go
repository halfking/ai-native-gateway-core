// Test-only helpers for MergeConsecutiveMessages regressions.
// Kept in tests so they don't widen the production API.

package transformation

import (
	"encoding/json"
	"testing"
)

// helper: round-trip through json so the body matches what a real client sends
func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// Repro #1 — two consecutive user messages, where one carries an image_url
// content array. Before the fix, dedupConsecutive silently string-coerces
// both sides, dropping every image_url block.
func TestMergeConsecutiveMessages_PreservesImageURL(t *testing.T) {
	body := mustMarshal(t, map[string]any{
		"model": "minimax-m3",
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "看一下这张图"},
					map[string]any{
						"type": "image_url",
						"image_url": map[string]any{
							"url": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNgAAIAAAUAAen63NgAAAAASUVORK5CYII=",
						},
					},
				},
			},
			map[string]any{
				"role":    "user",
				"content": "再描述一下细节",
			},
		},
	})

	got := MergeConsecutiveMessages(body)
	var obj map[string]any
	if err := json.Unmarshal(got, &obj); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	msgs, _ := obj["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatalf("messages lost")
	}
	// After merge we expect 1 user message that retains the image_url part.
	first, _ := msgs[0].(map[string]any)
	content, _ := first["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("image content parts were silently dropped (got %v)", first["content"])
	}
	hasImage := false
	hasText := false
	for _, p := range content {
		m, _ := p.(map[string]any)
		switch m["type"] {
		case "image_url":
			hasImage = true
		case "text":
			hasText = true
		}
	}
	if !hasImage {
		t.Errorf("image_url block lost: content=%v", content)
	}
	if !hasText {
		t.Errorf("text block lost: content=%v", content)
	}
}

// Repro #2 — single user message with image_url only (no merge trigger)
// must pass through completely untouched.
func TestMergeConsecutiveMessages_NoMergeNeeded_PassesThroughImage(t *testing.T) {
	body := mustMarshal(t, map[string]any{
		"model": "minimax-m3",
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type": "image_url",
						"image_url": map[string]any{
							"url": "https://example.com/cat.png",
						},
					},
				},
			},
		},
	})

	got := MergeConsecutiveMessages(body)
	if string(got) != string(body) {
		t.Errorf("single multimodal message must be unchanged.\nwant=%s\ngot =%s", body, got)
	}
}

// Repro #3 — two assistant messages where the second carries tool_calls
// (already passed before, but make sure the rewrite doesn't regress it).
func TestMergeConsecutiveMessages_NoMergeForDifferentRoles(t *testing.T) {
	body := mustMarshal(t, map[string]any{
		"model": "minimax-m3",
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": []any{map[string]any{"type": "text", "text": "hi"}},
			},
			map[string]any{
				"role":    "assistant",
				"content": []any{map[string]any{"type": "text", "text": "hello"}},
			},
			map[string]any{
				"role":    "user",
				"content": []any{map[string]any{"type": "text", "text": "byemultimodal"}},
			},
		},
	})

	got := MergeConsecutiveMessages(body)
	var obj map[string]any
	_ = json.Unmarshal(got, &obj)
	msgs, _ := obj["messages"].([]any)
	// role pattern user/assistant/user has no two consecutive same-role
	// user/assistant, so no merge should happen.
	if len(msgs) != 3 {
		t.Errorf("unexpected message count after merge: got %d want 3", len(msgs))
	}
}

// Repro #4 — regression sanity: the existing single-text-merge behavior
// for two consecutive plain-text user messages must keep working.
func TestMergeConsecutiveMessages_TextOnlyStillMerges(t *testing.T) {
	body := []byte(`{"model":"minimax-m3","messages":[{"role":"user","content":"a"},{"role":"user","content":"b"}]}`)
	got := MergeConsecutiveMessages(body)
	var obj map[string]any
	_ = json.Unmarshal(got, &obj)
	msgs, _ := obj["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 merged message, got %d", len(msgs))
	}
	first, _ := msgs[0].(map[string]any)
	if first["content"] != "a\nb" {
		t.Errorf("text merge broken: %v", first["content"])
	}
}

// Repro #5 — sanity that an existing image_url turns into a single
// non-string content block when it isn't merged. Tests that dedup path
// of merge_array_into_string still keeps the array on at least one side.
func TestMergeConsecutiveMessages_TextIntoArrayDoesNotCoerceAwayImage(t *testing.T) {
	body := mustMarshal(t, map[string]any{
		"model": "minimax-m3",
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "describe:"},
					map[string]any{
						"type":      "image_url",
						"image_url": map[string]any{"url": "https://example.com/dog.jpg"},
					},
				},
			},
			map[string]any{
				"role":    "user",
				"content": "thanks",
			},
		},
	})
	got := MergeConsecutiveMessages(body)
	var obj map[string]any
	_ = json.Unmarshal(got, &obj)
	msgs, _ := obj["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatalf("messages lost")
	}
	first, _ := msgs[0].(map[string]any)
	content, isArr := first["content"].([]any)
	if !isArr {
		t.Fatalf("merged content must remain an array (got %T)", first["content"])
	}
	for _, p := range content {
		m, _ := p.(map[string]any)
		if m["type"] == "image_url" {
			return
		}
	}
	t.Errorf("image_url lost after merge: %v", content)
}
