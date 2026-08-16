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
