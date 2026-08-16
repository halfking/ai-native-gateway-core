package handoff

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMemoryHandoffMessageBuilder_RedactsAndBounds(t *testing.T) {
	builder := NewMemoryHandoffMessageBuilder(2 << 10)
	createdAt := time.Date(2026, 8, 16, 3, 0, 0, 0, time.UTC)
	builder.now = func() time.Time { return createdAt }
	message, err := builder.Build("gw_old", TriggerSignal{
		Kind: SignalGoalFailed, Source: "goal", Reason: "switch_budget_exhausted", Severity: 4,
	}, &GoalState{
		Version: GoalStateVersion, SourceSessionID: "gw_old",
		TaskDescription: "use Bearer secret-value-123456789012",
		RemainingWork:   "call sk-abcdefghijklmnopqrstuvwxyz",
	}, "summary Bearer secret-value-123456789012")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 2<<10 {
		t.Fatalf("message exceeded bound: %d", len(encoded))
	}
	if strings.Contains(string(encoded), "secret-value") || strings.Contains(string(encoded), "sk-abc") {
		t.Fatalf("message leaked sensitive data: %s", encoded)
	}
	if message.Version != HandoffMessageVersion || message.CreatedAt != createdAt {
		t.Fatalf("unexpected schema metadata: %+v", message)
	}
}

func TestMemoryHandoffMessageBuilder_TruncatesWithoutMutatingInput(t *testing.T) {
	builder := NewMemoryHandoffMessageBuilder(1024)
	state := &GoalState{
		Version:         GoalStateVersion,
		SourceSessionID: "gw_old",
		TaskDescription: strings.Repeat("task ", 400),
		RemainingWork:   strings.Repeat("work ", 400),
		CompletedSteps:  []string{"keep this in the source"},
	}
	originalTask := state.TaskDescription
	originalRemaining := state.RemainingWork
	message, err := builder.Build("gw_old", TriggerSignal{Kind: SignalGoalFailed, Severity: 4}, state, strings.Repeat("summary ", 400))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 1024 || message.Summary != "" || len(message.GoalState.TaskDescription) > 512 || len(message.GoalState.RemainingWork) > 512 {
		t.Fatalf("message was not reduced to the configured bound: bytes=%d message=%+v", len(encoded), message)
	}
	if state.TaskDescription != originalTask || state.RemainingWork != originalRemaining || len(state.CompletedSteps) != 1 {
		t.Fatal("Build mutated the caller's GoalState")
	}
}

func TestMemoryHandoffMessageBuilder_ReturnsErrorWhenBasePayloadExceedsLimit(t *testing.T) {
	builder := NewMemoryHandoffMessageBuilder(64)
	_, err := builder.Build("gw_old", TriggerSignal{Kind: SignalGoalFailed, Severity: 4}, nil, "")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected bounded payload error, got %v", err)
	}
}

func TestResumePacket_OmitsGoalHandoffForLegacyPayload(t *testing.T) {
	encoded, err := json.Marshal(ResumePacket{
		Version: 1, PreviousSession: "gw_old", TriggerReason: "manual", Summary: "summary", SkillName: "handoff",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "goal_handoff") {
		t.Fatalf("legacy packet must omit optional Goal field: %s", encoded)
	}
}
