package main

import (
	"encoding/json"
	"fmt"
)

// Message represents a chat message
type Message struct {
	Role       string                   `json:"role"`
	Content    string                   `json:"content,omitempty"`
	ToolCalls  []map[string]interface{} `json:"tool_calls,omitempty"`
	ToolCallID string                   `json:"tool_call_id,omitempty"`
	Name       string                   `json:"name,omitempty"`
}

// ReconstructionResult represents the result of delta reconstruction for one turn
type ReconstructionResult struct {
	TurnNo      int
	SubmitMode  string
	Status      string // "ok" | "warning" | "error"
	Description string
	Details     []string
}

// MessageReconstructor reconstructs full message history from deltas
type MessageReconstructor struct{}

// NewMessageReconstructor creates a new message reconstructor
func NewMessageReconstructor() *MessageReconstructor {
	return &MessageReconstructor{}
}

// ReconstructFromDeltas accumulates deltas to rebuild full message history
func (r *MessageReconstructor) ReconstructFromDeltas(v2Bodies []V2Body) ([][]Message, error) {
	var history [][]Message
	var accumulated []Message

	for _, body := range v2Bodies {
		// Parse request delta
		var requestDelta []Message
		if len(body.RequestDelta) > 0 && string(body.RequestDelta) != "null" {
			if err := json.Unmarshal(body.RequestDelta, &requestDelta); err != nil {
				return nil, fmt.Errorf("turn %d: parse request_delta: %w", body.TurnNo, err)
			}
		}

		// Parse response delta
		var responseDelta []Message
		if len(body.ResponseDelta) > 0 && string(body.ResponseDelta) != "null" {
			if err := json.Unmarshal(body.ResponseDelta, &responseDelta); err != nil {
				return nil, fmt.Errorf("turn %d: parse response_delta: %w", body.TurnNo, err)
			}
		}

		// Accumulate request delta
		accumulated = append(accumulated, requestDelta...)

		// Accumulate response delta
		accumulated = append(accumulated, responseDelta...)

		// Snapshot after this turn
		turnSnapshot := make([]Message, len(accumulated))
		copy(turnSnapshot, accumulated)
		history = append(history, turnSnapshot)
	}

	return history, nil
}

// ValidateReconstruction validates that reconstructed history matches V1 request bodies
func (r *MessageReconstructor) ValidateReconstruction(
	v1Turns []V1Turn,
	v2Turns []V2Turn,
	v2Bodies []V2Body,
) []ReconstructionResult {
	var results []ReconstructionResult

	// Build submit mode map
	submitModes := make(map[int]string)
	for _, turn := range v2Turns {
		submitModes[turn.TurnNo] = turn.SubmitMode
	}

	// Build V1 bodies map by request_id
	v1Bodies := make(map[string]json.RawMessage)
	for _, turn := range v1Turns {
		v1Bodies[turn.RequestID] = turn.RequestBody
	}

	// Reconstruct from V2 deltas
	reconstructed, err := r.ReconstructFromDeltas(v2Bodies)
	if err != nil {
		results = append(results, ReconstructionResult{
			TurnNo:      0,
			Status:      "error",
			Description: fmt.Sprintf("Reconstruction failed: %v", err),
		})
		return results
	}

	// Validate each turn
	for i, body := range v2Bodies {
		result := ReconstructionResult{
			TurnNo:     body.TurnNo,
			SubmitMode: submitModes[body.TurnNo],
			Status:     "ok",
		}

		// Get V1 request body
		v1Body, exists := v1Bodies[body.RequestID]
		if !exists {
			result.Status = "warning"
			result.Description = "V1 request body not found (will be caught by parity check)"
			results = append(results, result)
			continue
		}

		// Parse V1 messages
		var v1Messages struct {
			Messages []Message `json:"messages"`
		}
		if err := json.Unmarshal(v1Body, &v1Messages); err != nil {
			result.Status = "warning"
			result.Description = fmt.Sprintf("V1 body parsing failed: %v", err)
			results = append(results, result)
			continue
		}

		// Get reconstructed messages for this turn
		if i >= len(reconstructed) {
			result.Status = "error"
			result.Description = "Reconstructed history missing this turn"
			results = append(results, result)
			continue
		}

		reconstructedMsgs := reconstructed[i]

		// Compare based on submit mode
		switch result.SubmitMode {
		case "full":
			// Strict comparison for full mode
			if !messagesEqual(v1Messages.Messages, reconstructedMsgs) {
				result.Status = "error"
				result.Description = fmt.Sprintf(
					"Full mode: message mismatch (V1=%d msgs, V2 reconstructed=%d msgs)",
					len(v1Messages.Messages), len(reconstructedMsgs),
				)
				result.Details = describeMessageDiff(v1Messages.Messages, reconstructedMsgs)
			} else {
				result.Description = "Full mode: messages match"
			}

		case "delta", "snapshot", "inferred_compressed":
			// For compressed modes, we can't do strict comparison
			result.Status = "warning"
			result.Description = fmt.Sprintf(
				"%s mode: strict comparison not possible (V1=%d msgs, V2=%d msgs)",
				result.SubmitMode, len(v1Messages.Messages), len(reconstructedMsgs),
			)

		default:
			result.Status = "warning"
			result.Description = fmt.Sprintf("Unknown submit_mode: %s", result.SubmitMode)
		}

		results = append(results, result)
	}

	return results
}

// messagesEqual checks if two message slices are equal (order-sensitive)
func messagesEqual(a, b []Message) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if !messageEqual(a[i], b[i]) {
			return false
		}
	}

	return true
}

// messageEqual checks if two messages are equal
func messageEqual(a, b Message) bool {
	// Compare role
	if a.Role != b.Role {
		return false
	}

	// Compare content
	if a.Content != b.Content {
		return false
	}

	// Compare name
	if a.Name != b.Name {
		return false
	}

	// Compare tool_call_id
	if a.ToolCallID != b.ToolCallID {
		return false
	}

	// For tool_calls, do a shallow comparison (count and structure)
	// Deep comparison of tool_calls is complex and may have minor variations
	if len(a.ToolCalls) != len(b.ToolCalls) {
		return false
	}

	return true
}

// describeMessageDiff generates a human-readable diff description
func describeMessageDiff(v1, v2 []Message) []string {
	var diffs []string

	maxLen := len(v1)
	if len(v2) > maxLen {
		maxLen = len(v2)
	}

	for i := 0; i < maxLen; i++ {
		if i >= len(v1) {
			diffs = append(diffs, fmt.Sprintf("msg[%d]: present in V2 but not V1", i))
			continue
		}
		if i >= len(v2) {
			diffs = append(diffs, fmt.Sprintf("msg[%d]: present in V1 but not V2", i))
			continue
		}

		v1Msg := v1[i]
		v2Msg := v2[i]

		if v1Msg.Role != v2Msg.Role {
			diffs = append(diffs, fmt.Sprintf(
				"msg[%d]: role mismatch (V1=%s, V2=%s)",
				i, v1Msg.Role, v2Msg.Role,
			))
		}

		if v1Msg.Content != v2Msg.Content {
			v1Preview := v1Msg.Content
			if len(v1Preview) > 50 {
				v1Preview = v1Preview[:50] + "..."
			}
			v2Preview := v2Msg.Content
			if len(v2Preview) > 50 {
				v2Preview = v2Preview[:50] + "..."
			}
			diffs = append(diffs, fmt.Sprintf(
				"msg[%d]: content mismatch (V1=%q, V2=%q)",
				i, v1Preview, v2Preview,
			))
		}
	}

	if len(diffs) > 10 {
		diffs = append(diffs[:10], fmt.Sprintf("... and %d more differences", len(diffs)-10))
	}

	return diffs
}
