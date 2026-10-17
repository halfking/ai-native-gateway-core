package strip

import (
	"encoding/json"
	"strings"
	"testing"
)

func streamMiniMaxFrame(t *testing.T, index int, text, finish string) []byte {
	t.Helper()
	choice := map[string]any{"index": index, "delta": map[string]any{"content": text, "reasoning_content": "<function_calls>private</tool_call>"}}
	if finish != "" {
		choice["finish_reason"] = finish
	}
	body, err := json.Marshal(map[string]any{"nvext": map[string]any{}, "choices": []any{choice}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}
func streamMiniMaxContent(t *testing.T, body []byte) string {
	t.Helper()
	var root struct {
		NVExt   json.RawMessage `json:"nvext"`
		Choices []struct {
			Delta struct {
				Content   string
				Reasoning string `json:"reasoning_content"`
			}
		}
	}
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatal(err)
	}
	if len(root.NVExt) != 0 || root.Choices[0].Delta.Reasoning != "" {
		t.Fatalf("private/reasoning cleanup regressed: %s", body)
	}
	return root.Choices[0].Delta.Content
}

func TestMiniMaxStreamStatePreservesCandidateAndReleasesAtCloseFinishDone(t *testing.T) {
	strip := NewMiniMaxStreamStripper()
	for _, part := range []string{"<tool_call>", "minimax[>[continued]<]", "</tool_", "call>"} {
		if got := streamMiniMaxContent(t, strip(streamMiniMaxFrame(t, 0, part, ""))); got != part {
			t.Fatalf("candidate changed: %q != %q", got, part)
		}
		if got := streamMiniMaxContent(t, strip(streamMiniMaxFrame(t, 1, "minimax[>[ordinary]<]", ""))); got != "ordinary" {
			t.Fatalf("choice state leaked: %q", got)
		}
	}
	ordinary := "minimax[>[ordinary]<]"
	if got := streamMiniMaxContent(t, strip(streamMiniMaxFrame(t, 0, ordinary, ""))); got != "ordinary" {
		t.Fatalf("close failed to release: %q", got)
	}
	strip(streamMiniMaxFrame(t, 0, "<tool_call>", ""))
	if got := streamMiniMaxContent(t, strip(streamMiniMaxFrame(t, 0, ordinary, "length"))); got != ordinary {
		t.Fatalf("terminal content cleaned before release: %q", got)
	}
	if got := streamMiniMaxContent(t, strip(streamMiniMaxFrame(t, 0, ordinary, ""))); got != "ordinary" {
		t.Fatalf("finish failed to release: %q", got)
	}
	strip(streamMiniMaxFrame(t, 0, "<tool_call>", ""))
	strip([]byte("[DONE]"))
	if got := streamMiniMaxContent(t, strip(streamMiniMaxFrame(t, 0, ordinary, ""))); got != "ordinary" {
		t.Fatalf("DONE failed to release: %q", got)
	}
}

func TestMiniMaxStreamStateChoiceCapIsResponseLocal(t *testing.T) {
	state := &miniMaxToolStreamState{choices: map[int]miniMaxToolTail{}}
	strip := miniMaxStripper{stream: state}.StripFields
	for i := 0; i < maxMiniMaxToolChoices+1; i++ {
		strip(streamMiniMaxFrame(t, i, "<tool_call>", ""))
	}
	if len(state.choices) > maxMiniMaxToolChoices || !state.preserveAll {
		t.Fatalf("unbounded/missing safe fallback: count=%d fallback=%v", len(state.choices), state.preserveAll)
	}
	for _, tail := range state.choices {
		if len(tail.tail) > miniMaxToolTailBytes {
			t.Fatal("unbounded suffix")
		}
	}
	text := "minimax[>[continuation]<]"
	if got := streamMiniMaxContent(t, strip(streamMiniMaxFrame(t, maxMiniMaxToolChoices, text, ""))); got != text {
		t.Fatalf("cap dropped continuation: %q", got)
	}
	if got := streamMiniMaxContent(t, NewMiniMaxStreamStripper()(streamMiniMaxFrame(t, 0, text, ""))); got != "continuation" {
		t.Fatalf("fallback leaked across responses: %q", got)
	}
	if strings.Contains(string(strip(streamMiniMaxFrame(t, 0, text, ""))), `"nvext"`) {
		t.Fatal("cap skipped private field cleanup")
	}
}
