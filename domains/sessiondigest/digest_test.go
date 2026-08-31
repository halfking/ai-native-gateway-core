package sessiondigest

import (
	"encoding/json"
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

func TestUnmarshalRejectsUnknownVersion(t *testing.T) {
	raw, err := json.Marshal(Envelope{SchemaVersion: SchemaVersion + 1, AlgorithmVersion: AlgorithmVersion})
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if _, err := Unmarshal(raw); err == nil {
		t.Fatal("Unmarshal accepted an unknown schema version")
	}
}
