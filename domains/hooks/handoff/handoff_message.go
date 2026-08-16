package handoff

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	HandoffMessageVersion  = 1
	defaultHandoffMaxBytes = 2 << 10
)

// HandoffMessage is the versioned Goal payload nested in ResumePacket.
type HandoffMessage struct {
	Version           int           `json:"version"`
	SourceSessionID   string        `json:"source_session_id"`
	Trigger           TriggerSignal `json:"trigger"`
	GoalState         *GoalState    `json:"goal_state,omitempty"`
	Summary           string        `json:"summary,omitempty"`
	ResumeInstruction string        `json:"resume_instruction"`
	CreatedAt         time.Time     `json:"created_at"`
}

// HandoffMessageBuilder creates a bounded, redacted cross-session payload.
type HandoffMessageBuilder interface {
	Build(sourceSessionID string, signal TriggerSignal, state *GoalState, summary string) (*HandoffMessage, error)
}

// MemoryHandoffMessageBuilder is a stateless in-memory message builder.
type MemoryHandoffMessageBuilder struct {
	maxBytes int
	now      func() time.Time
}

func NewMemoryHandoffMessageBuilder(maxBytes int) *MemoryHandoffMessageBuilder {
	if maxBytes <= 0 {
		maxBytes = defaultHandoffMaxBytes
	}
	return &MemoryHandoffMessageBuilder{maxBytes: maxBytes, now: time.Now}
}

func (b *MemoryHandoffMessageBuilder) Build(sourceSessionID string, signal TriggerSignal, state *GoalState, summary string) (*HandoffMessage, error) {
	if sourceSessionID == "" || !signalEligible(signal) {
		return nil, fmt.Errorf("handoff message requires an eligible signal and source session")
	}
	message := &HandoffMessage{
		Version:           HandoffMessageVersion,
		SourceSessionID:   sourceSessionID,
		Trigger:           signal,
		GoalState:         cloneGoalState(state),
		Summary:           redactResumeSensitive(strings.TrimSpace(summary)),
		ResumeInstruction: "Continue the original goal in Goal mode from the remaining work.",
		CreatedAt:         b.now().UTC(),
	}
	if message.GoalState != nil {
		redactGoalState(message.GoalState)
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("encode handoff message: %w", err)
	}
	if len(encoded) <= b.maxBytes {
		return message, nil
	}
	message.Summary = ""
	if message.GoalState != nil {
		message.GoalState.CompletedSteps = nil
		message.GoalState.TaskDescription = truncateRunes(message.GoalState.TaskDescription, 512)
		message.GoalState.RemainingWork = truncateRunes(message.GoalState.RemainingWork, 512)
	}
	encoded, err = json.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("encode bounded handoff message: %w", err)
	}
	if len(encoded) > b.maxBytes {
		return nil, fmt.Errorf("handoff message exceeds %d bytes", b.maxBytes)
	}
	return message, nil
}

func redactGoalState(state *GoalState) {
	state.TaskDescription = redactResumeSensitive(state.TaskDescription)
	state.RemainingWork = redactResumeSensitive(state.RemainingWork)
	for i := range state.CompletedSteps {
		state.CompletedSteps[i] = redactResumeSensitive(state.CompletedSteps[i])
	}
}
