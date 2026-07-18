package main

import (
	"encoding/json"
	"testing"
)

func TestReconstructFromDeltas_SingleTurn(t *testing.T) {
	bodies := []V2Body{
		{
			TurnNo:        1,
			RequestDelta:  json.RawMessage(`[{"role":"user","content":"hello"}]`),
			ResponseDelta: json.RawMessage(`[{"role":"assistant","content":"hi"}]`),
		},
	}

	reconstructor := NewMessageReconstructor()
	history, err := reconstructor.ReconstructFromDeltas(bodies)

	if err != nil {
		t.Fatalf("ReconstructFromDeltas failed: %v", err)
	}

	if len(history) != 1 {
		t.Fatalf("Expected 1 turn in history, got %d", len(history))
	}

	if len(history[0]) != 2 {
		t.Errorf("Expected 2 messages in turn 1, got %d", len(history[0]))
	}

	if history[0][0].Role != "user" || history[0][0].Content != "hello" {
		t.Errorf("First message incorrect: %+v", history[0][0])
	}

	if history[0][1].Role != "assistant" || history[0][1].Content != "hi" {
		t.Errorf("Second message incorrect: %+v", history[0][1])
	}
}

func TestReconstructFromDeltas_Accumulation(t *testing.T) {
	bodies := []V2Body{
		{
			TurnNo:        1,
			RequestDelta:  json.RawMessage(`[{"role":"user","content":"hello"}]`),
			ResponseDelta: json.RawMessage(`[{"role":"assistant","content":"hi"}]`),
		},
		{
			TurnNo:        2,
			RequestDelta:  json.RawMessage(`[{"role":"user","content":"how are you?"}]`),
			ResponseDelta: json.RawMessage(`[{"role":"assistant","content":"good"}]`),
		},
	}

	reconstructor := NewMessageReconstructor()
	history, err := reconstructor.ReconstructFromDeltas(bodies)

	if err != nil {
		t.Fatalf("ReconstructFromDeltas failed: %v", err)
	}

	if len(history) != 2 {
		t.Fatalf("Expected 2 turns in history, got %d", len(history))
	}

	// Turn 1 should have 2 messages
	if len(history[0]) != 2 {
		t.Errorf("Turn 1: expected 2 messages, got %d", len(history[0]))
	}

	// Turn 2 should have 4 messages (accumulated)
	if len(history[1]) != 4 {
		t.Errorf("Turn 2: expected 4 messages (accumulated), got %d", len(history[1]))
	}

	// Verify turn 2 contains all messages
	expected := []string{"user", "assistant", "user", "assistant"}
	for i, msg := range history[1] {
		if msg.Role != expected[i] {
			t.Errorf("Turn 2 msg[%d]: expected role=%s, got %s", i, expected[i], msg.Role)
		}
	}
}

func TestReconstructFromDeltas_EmptyDeltas(t *testing.T) {
	bodies := []V2Body{
		{
			TurnNo:        1,
			RequestDelta:  json.RawMessage(`[]`),
			ResponseDelta: json.RawMessage(`[]`),
		},
	}

	reconstructor := NewMessageReconstructor()
	history, err := reconstructor.ReconstructFromDeltas(bodies)

	if err != nil {
		t.Fatalf("ReconstructFromDeltas failed: %v", err)
	}

	if len(history) != 1 {
		t.Fatalf("Expected 1 turn in history, got %d", len(history))
	}

	if len(history[0]) != 0 {
		t.Errorf("Expected 0 messages with empty deltas, got %d", len(history[0]))
	}
}

func TestReconstructFromDeltas_InvalidJSON(t *testing.T) {
	bodies := []V2Body{
		{
			TurnNo:       1,
			RequestDelta: json.RawMessage(`{invalid json`),
		},
	}

	reconstructor := NewMessageReconstructor()
	_, err := reconstructor.ReconstructFromDeltas(bodies)

	if err == nil {
		t.Errorf("Expected error for invalid JSON, got nil")
	}
}

func TestMessagesEqual_Identical(t *testing.T) {
	a := []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}
	b := []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}

	if !messagesEqual(a, b) {
		t.Errorf("Expected identical messages to be equal")
	}
}

func TestMessagesEqual_DifferentLength(t *testing.T) {
	a := []Message{
		{Role: "user", Content: "hello"},
	}
	b := []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}

	if messagesEqual(a, b) {
		t.Errorf("Expected messages with different lengths to be unequal")
	}
}

func TestMessagesEqual_DifferentContent(t *testing.T) {
	a := []Message{
		{Role: "user", Content: "hello"},
	}
	b := []Message{
		{Role: "user", Content: "hi"},
	}

	if messagesEqual(a, b) {
		t.Errorf("Expected messages with different content to be unequal")
	}
}

func TestValidateReconstruction_FullModeMatch(t *testing.T) {
	v1Turns := []V1Turn{
		{
			RequestID:   "req_1",
			RequestBody: json.RawMessage(`{"messages":[{"role":"user","content":"hello"}]}`),
		},
	}
	v2Turns := []V2Turn{
		{TurnNo: 1, RequestID: "req_1", SubmitMode: "full"},
	}
	v2Bodies := []V2Body{
		{
			TurnNo:        1,
			RequestID:     "req_1",
			RequestDelta:  json.RawMessage(`[{"role":"user","content":"hello"}]`),
			ResponseDelta: json.RawMessage(`[]`),
		},
	}

	reconstructor := NewMessageReconstructor()
	results := reconstructor.ValidateReconstruction(v1Turns, v2Turns, v2Bodies)

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	if results[0].Status != "ok" {
		t.Errorf("Expected status=ok for full mode match, got %s: %s",
			results[0].Status, results[0].Description)
	}
}

func TestValidateReconstruction_FullModeMismatch(t *testing.T) {
	v1Turns := []V1Turn{
		{
			RequestID:   "req_1",
			RequestBody: json.RawMessage(`{"messages":[{"role":"user","content":"hello"}]}`),
		},
	}
	v2Turns := []V2Turn{
		{TurnNo: 1, RequestID: "req_1", SubmitMode: "full"},
	}
	v2Bodies := []V2Body{
		{
			TurnNo:        1,
			RequestID:     "req_1",
			RequestDelta:  json.RawMessage(`[{"role":"user","content":"hi"}]`), // Different content
			ResponseDelta: json.RawMessage(`[]`),
		},
	}

	reconstructor := NewMessageReconstructor()
	results := reconstructor.ValidateReconstruction(v1Turns, v2Turns, v2Bodies)

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	if results[0].Status != "error" {
		t.Errorf("Expected status=error for full mode mismatch, got %s", results[0].Status)
	}
}

func TestValidateReconstruction_CompressedModeWarning(t *testing.T) {
	v1Turns := []V1Turn{
		{
			RequestID:   "req_1",
			RequestBody: json.RawMessage(`{"messages":[{"role":"user","content":"hello"}]}`),
		},
	}
	v2Turns := []V2Turn{
		{TurnNo: 1, RequestID: "req_1", SubmitMode: "inferred_compressed"},
	}
	v2Bodies := []V2Body{
		{
			TurnNo:        1,
			RequestID:     "req_1",
			RequestDelta:  json.RawMessage(`[]`),
			ResponseDelta: json.RawMessage(`[]`),
		},
	}

	reconstructor := NewMessageReconstructor()
	results := reconstructor.ValidateReconstruction(v1Turns, v2Turns, v2Bodies)

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	if results[0].Status != "warning" {
		t.Errorf("Expected status=warning for compressed mode, got %s", results[0].Status)
	}

	if results[0].SubmitMode != "inferred_compressed" {
		t.Errorf("Expected submit_mode=inferred_compressed, got %s", results[0].SubmitMode)
	}
}
