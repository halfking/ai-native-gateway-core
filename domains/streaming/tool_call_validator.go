package streaming

import (
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// tool_call_validator.go — Tool Call Completeness Validator
//
// Tracks tool_use blocks and their corresponding tool_result blocks in
// Anthropic SSE streams to detect incomplete tool executions. When the
// upstream sends tool_use but the stream ends before tool_result arrives,
// the validator marks the stream as interrupted with Resumable=true so
// the executor can retry.
//
// Background (2026-08-29): Users reported "Tool result is missing for tool
// call call-xxx" errors. Root cause: upstream (Anthropic Claude) sends
// content_block_start(type:tool_use) but the stream is interrupted before
// content_block_start(type:tool_result) arrives. Similar to the SSE frame
// validator pattern, this validator provides early detection and transparent
// recovery within the holdback window.

// ToolCallValidator tracks the state of tool_use blocks during streaming.
// It verifies that every tool_use has a corresponding tool_result before
// the stream completes.
//
// Thread-safety: The validator is written from a single goroutine (the
// stream reader) but uses a mutex for defensive depth and to support
// potential concurrent validation in tests.
type ToolCallValidator struct {
	mu              sync.RWMutex
	pendingToolUses map[string]*ToolUseState
	// toolResultSeen tracks tool_result IDs to detect mismatches where
	// tool_result arrives without a preceding tool_use
	toolResultSeen map[string]bool
	streamEnded    bool
}

// ToolUseState tracks the lifecycle of a single tool_use block.
type ToolUseState struct {
	ID        string
	Name      string    // Tool function name for logging
	Index     int       // Content block index
	StartedAt time.Time
	Completed bool // True when matching tool_result received
}

// NewToolCallValidator creates a new validator.
func NewToolCallValidator() *ToolCallValidator {
	return &ToolCallValidator{
		pendingToolUses: make(map[string]*ToolUseState),
		toolResultSeen:  make(map[string]bool),
	}
}

// OnToolUse registers a tool_use block. Call this when observing
// content_block_start with type=tool_use in the Anthropic stream.
func (v *ToolCallValidator) OnToolUse(id, name string, index int) {
	if v == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.streamEnded {
		// Validator already finalized, don't accept new registrations
		return
	}

	if id == "" {
		// Invalid tool_use (missing ID) — don't track it
		return
	}

	// Register the tool_use as pending
	v.pendingToolUses[id] = &ToolUseState{
		ID:        id,
		Name:      name,
		Index:     index,
		StartedAt: time.Now(),
		Completed: false,
	}
}

// OnToolResult marks a tool_use as completed. Call this when observing
// content_block_start with type=tool_result in the Anthropic stream.
func (v *ToolCallValidator) OnToolResult(toolUseID string) {
	if v == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.streamEnded {
		return
	}

	if toolUseID == "" {
		return
	}

	// Mark the tool_result as seen (for mismatch detection)
	v.toolResultSeen[toolUseID] = true

	// If we have a matching pending tool_use, mark it completed
	if state, exists := v.pendingToolUses[toolUseID]; exists {
		state.Completed = true
	}
	// If no matching tool_use exists, that's a protocol violation
	// (tool_result without tool_use), but we don't fail the stream
	// for that — the client SDK will handle it.
}

// ValidateComplete checks if all tool_use blocks have matching tool_result
// blocks. Returns nil if complete, or an error describing the incompleteness.
//
// This should be called when the stream ends (either EOF or message_stop).
func (v *ToolCallValidator) ValidateComplete() error {
	if v == nil {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()

	v.streamEnded = true

	// Find any pending tool_use blocks that never received a tool_result
	var incomplete []string
	for id, state := range v.pendingToolUses {
		if !state.Completed {
			incomplete = append(incomplete, id)
		}
	}

	if len(incomplete) > 0 {
		// At least one tool_use is missing its tool_result
		return &IncompleteToolCallError{
			MissingResults: incomplete,
			TotalToolUses:  len(v.pendingToolUses),
		}
	}

	return nil
}

// HasPendingToolUses reports whether any tool_use blocks are registered
// but not yet completed. Useful for deciding whether a stream interruption
// happened during tool execution.
func (v *ToolCallValidator) HasPendingToolUses() bool {
	if v == nil {
		return false
	}
	v.mu.RLock()
	defer v.mu.RUnlock()

	for _, state := range v.pendingToolUses {
		if !state.Completed {
			return true
		}
	}
	return false
}

// PendingCount returns the number of incomplete tool_use blocks.
func (v *ToolCallValidator) PendingCount() int {
	if v == nil {
		return 0
	}
	v.mu.RLock()
	defer v.mu.RUnlock()

	count := 0
	for _, state := range v.pendingToolUses {
		if !state.Completed {
			count++
		}
	}
	return count
}

// IncompleteToolCallError is returned by ValidateComplete when one or
// more tool_use blocks did not receive a matching tool_result.
type IncompleteToolCallError struct {
	MissingResults []string
	TotalToolUses  int
}

func (e *IncompleteToolCallError) Error() string {
	if len(e.MissingResults) == 1 {
		return "tool_result missing for tool_use " + e.MissingResults[0]
	}
	return "tool_result missing for multiple tool_use blocks"
}

// classifyIncompleteToolCall determines the resumability and error kind
// for an incomplete tool call scenario.
//
// Strategy:
//   - If the stream was interrupted mid-execution (upstream closed early),
//     classify as KindUpstreamDown and Resumable=true so the executor retries.
//   - If the stream completed cleanly (message_stop) but tool_result is missing,
//     that's a protocol violation from the upstream — still KindUpstreamDown
//     but logged differently for investigation.
func classifyIncompleteToolCall(streamReceivedDone bool) (kind errorsx.ErrorKind, resumable bool, reason string) {
	if streamReceivedDone {
		// The upstream sent message_stop, indicating it believed the stream
		// was complete, but tool_result is missing. This is a protocol
		// violation from the upstream.
		return errorsx.KindUpstreamDown, true, "incomplete_tool_call_after_done"
	}
	// Stream interrupted before message_stop — upstream closed early.
	return errorsx.KindUpstreamDown, true, "incomplete_tool_call_interrupted"
}
