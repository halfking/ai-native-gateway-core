package sessiondigest

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBuildAndUnmarshalRoundTrip(t *testing.T) {
	envelope := Build(
		[]map[string]any{{"role": "user", "content": "explain the result"}},
		[]map[string]any{{"role": "assistant", "content": "the result is ready", "tool_calls": []any{map[string]any{"id": "call-1", "function": map[string]any{"name": "lookup"}}}}},
		map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "latency_ms": 20},
		map[string]any{"compression_applied": true},
		time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	)
	if envelope == nil {
		t.Fatal("Build returned nil")
	}
	if envelope.SchemaVersion != SchemaVersion || envelope.AlgorithmVersion != AlgorithmVersion {
		t.Fatalf("unexpected envelope version: %#v", envelope)
	}

	raw, err := Marshal(envelope)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := Unmarshal(raw)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Payload.UserInput != "explain the result" || got.Payload.AssistantOutput != "the result is ready" {
		t.Fatalf("unexpected payload: %#v", got.Payload)
	}
	if got.Payload.ToolUsage == nil || got.Payload.ToolUsage.ToolCallCount != 1 || got.Payload.ToolUsage.ToolsUsed[0] != "lookup" {
		t.Fatalf("unexpected tool usage: %#v", got.Payload.ToolUsage)
	}
}

func TestBuildBoundsLongText(t *testing.T) {
	long := "a"
	for range 400 {
		long += "a"
	}
	envelope := Build([]map[string]any{{"role": "user", "content": long}}, nil, map[string]any{"prompt_tokens": 1}, nil, time.Time{})
	if envelope == nil {
		t.Fatal("Build returned nil")
	}
	if got := len([]rune(envelope.Payload.UserInput)); got != 260 {
		t.Fatalf("bounded user input runes = %d, want 260", got)
	}
}

func TestBuildExcludesToolArguments(t *testing.T) {
	const secretArguments = `{"api_key":"do-not-persist","payload":"sensitive"}`
	envelope := Build(
		[]map[string]any{{"role": "user", "content": "run lookup"}},
		[]map[string]any{{"role": "assistant", "tool_calls": []any{map[string]any{
			"function": map[string]any{"name": "lookup", "arguments": secretArguments},
		}}}},
		nil, nil, time.Time{},
	)
	if envelope == nil || envelope.Payload.ToolUsage == nil {
		t.Fatalf("Build did not retain tool name: %#v", envelope)
	}
	if got := envelope.Payload.ToolUsage.ToolsUsed; len(got) != 1 || got[0] != "lookup" {
		t.Fatalf("unexpected tools: %#v", got)
	}
	raw, err := Marshal(envelope)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(raw) == secretArguments || strings.Contains(string(raw), secretArguments) {
		t.Fatalf("digest leaked tool arguments: %s", raw)
	}
}

func TestBuildReturnsNilWithoutUsefulContent(t *testing.T) {
	if envelope := Build(nil, nil, nil, nil, time.Time{}); envelope != nil {
		t.Fatalf("Build returned an empty envelope: %#v", envelope)
	}
}

func TestUnmarshalRejectsUnknownVersion(t *testing.T) {
	raw, err := json.Marshal(Envelope{SchemaVersion: SchemaVersion + 1, AlgorithmVersion: AlgorithmVersion})
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if _, err := Unmarshal(raw); err == nil {
		t.Fatal("Unmarshal accepted an unknown schema version")
	}
}

// Audit R20 (2026-09-13): Responses protocol turns carry the user payload
// under top-level "input" (string or message-item array). messageValues'
// walk previously only descended messages/choices/message/output/content,
// so every Responses turn digested with an empty user_input.
func TestBuildExtractsResponsesInputAsString(t *testing.T) {
	request := map[string]any{
		"model": "gpt-6",
		"input": "summarize this log",
	}
	envelope := Build(request, map[string]any{"output": []any{map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "here you go"}}}}}, nil, nil, time.Now())
	if envelope == nil {
		t.Fatal("Build returned nil")
	}
	if envelope.Payload.UserInput != "summarize this log" {
		t.Fatalf("UserInput = %q, want %q", envelope.Payload.UserInput, "summarize this log")
	}
}

func TestBuildExtractsResponsesInputAsItems(t *testing.T) {
	request := map[string]any{
		"model": "gpt-6",
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "list the errors"}}},
		},
	}
	envelope := Build(request, nil, nil, nil, time.Now())
	if envelope == nil {
		t.Fatal("Build returned nil")
	}
	if envelope.Payload.UserInput != "list the errors" {
		t.Fatalf("UserInput = %q, want %q", envelope.Payload.UserInput, "list the errors")
	}
}
