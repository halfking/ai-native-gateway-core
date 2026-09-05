package goal

import (
	"encoding/json"
	"testing"
)

func TestParseGoalRequest_Valid(t *testing.T) {
	validJSON := `{
		"version": 1,
		"enabled": true,
		"root_goal_id": "client-goal-123",
		"instruction": "Complete the data migration task",
		"execution_mode": "continuous",
		"durability": "durable",
		"completion_policy": {
			"detector": "goal_v2",
			"min_confidence": 0.8,
			"require_terminal_evidence": true
		},
		"limits": {
			"max_wall_time_seconds": 86400,
			"max_turns": 50,
			"max_follow_ups": 50,
			"max_model_switches": 3,
			"max_handoffs": 5
		},
		"delivery": {
			"mode": "poll"
		}
	}`

	req, err := ParseGoalRequest([]byte(validJSON))
	if err != nil {
		t.Fatalf("ParseGoalRequest failed: %v", err)
	}

	if req.Version != 1 {
		t.Errorf("expected version 1, got %d", req.Version)
	}

	if !req.Enabled {
		t.Error("expected enabled=true")
	}

	if req.RootGoalID != "client-goal-123" {
		t.Errorf("expected root_goal_id='client-goal-123', got '%s'", req.RootGoalID)
	}

	if req.Instruction != "Complete the data migration task" {
		t.Errorf("unexpected instruction: %s", req.Instruction)
	}

	if req.ExecutionMode != ExecutionModeContinuous {
		t.Errorf("expected continuous mode, got %s", req.ExecutionMode)
	}

	if req.Durability != DurabilityDurable {
		t.Errorf("expected durable, got %s", req.Durability)
	}

	if req.Limits.MaxWallTimeSeconds != 86400 {
		t.Errorf("expected 86400 seconds, got %d", req.Limits.MaxWallTimeSeconds)
	}

	if req.Limits.MaxTurns != 50 {
		t.Errorf("expected 50 turns, got %d", req.Limits.MaxTurns)
	}

	if req.CompletionPolicy.Detector != "goal_v2" {
		t.Errorf("expected goal_v2 detector, got %s", req.CompletionPolicy.Detector)
	}

	if req.CompletionPolicy.MinConfidence != 0.8 {
		t.Errorf("expected 0.8 confidence, got %f", req.CompletionPolicy.MinConfidence)
	}

	if req.Delivery.Mode != "poll" {
		t.Errorf("expected poll mode, got %s", req.Delivery.Mode)
	}
}

func TestParseGoalRequest_EmptyData(t *testing.T) {
	_, err := ParseGoalRequest([]byte{})
	if err == nil {
		t.Fatal("expected error for empty data")
	}
}

func TestParseGoalRequest_InvalidJSON(t *testing.T) {
	_, err := ParseGoalRequest([]byte(`{invalid json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestGoalRequest_Validate_UnsupportedVersion(t *testing.T) {
	req := GoalRequest{
		Version:       2,
		Enabled:       true,
		Instruction:   "test",
		ExecutionMode: ExecutionModeContinuous,
		Durability:    DurabilityDurable,
		Limits: GoalLimits{
			MaxWallTimeSeconds: 3600,
			MaxTurns:           10,
		},
		CompletionPolicy: CompletionPolicy{
			Detector:      "goal_v2",
			MinConfidence: 0.8,
		},
		Delivery: DeliveryConfig{Mode: "poll"},
	}

	err := req.Validate()
	if err == nil {
		t.Fatal("expected error for unsupported version")
	}
}

func TestGoalRequest_Validate_EnabledFalse(t *testing.T) {
	req := GoalRequest{
		Version:       1,
		Enabled:       false,
		Instruction:   "test",
		ExecutionMode: ExecutionModeContinuous,
		Durability:    DurabilityDurable,
		Limits: GoalLimits{
			MaxWallTimeSeconds: 3600,
			MaxTurns:           10,
		},
		CompletionPolicy: CompletionPolicy{
			Detector:      "goal_v2",
			MinConfidence: 0.8,
		},
		Delivery: DeliveryConfig{Mode: "poll"},
	}

	err := req.Validate()
	if err == nil {
		t.Fatal("expected error for enabled=false")
	}
}

func TestGoalRequest_Validate_MissingInstruction(t *testing.T) {
	req := GoalRequest{
		Version:       1,
		Enabled:       true,
		Instruction:   "",
		ExecutionMode: ExecutionModeContinuous,
		Durability:    DurabilityDurable,
		Limits: GoalLimits{
			MaxWallTimeSeconds: 3600,
			MaxTurns:           10,
		},
		CompletionPolicy: CompletionPolicy{
			Detector:      "goal_v2",
			MinConfidence: 0.8,
		},
		Delivery: DeliveryConfig{Mode: "poll"},
	}

	err := req.Validate()
	if err == nil {
		t.Fatal("expected error for missing instruction")
	}
}

func TestGoalRequest_Validate_InstructionTooLong(t *testing.T) {
	longInstruction := make([]byte, 10001)
	for i := range longInstruction {
		longInstruction[i] = 'a'
	}

	req := GoalRequest{
		Version:       1,
		Enabled:       true,
		Instruction:   string(longInstruction),
		ExecutionMode: ExecutionModeContinuous,
		Durability:    DurabilityDurable,
		Limits: GoalLimits{
			MaxWallTimeSeconds: 3600,
			MaxTurns:           10,
		},
		CompletionPolicy: CompletionPolicy{
			Detector:      "goal_v2",
			MinConfidence: 0.8,
		},
		Delivery: DeliveryConfig{Mode: "poll"},
	}

	err := req.Validate()
	if err == nil {
		t.Fatal("expected error for instruction exceeding 10000 characters")
	}
}

func TestGoalRequest_Validate_UnsupportedExecutionMode(t *testing.T) {
	req := GoalRequest{
		Version:       1,
		Enabled:       true,
		Instruction:   "test",
		ExecutionMode: "invalid_mode",
		Durability:    DurabilityDurable,
		Limits: GoalLimits{
			MaxWallTimeSeconds: 3600,
			MaxTurns:           10,
		},
		CompletionPolicy: CompletionPolicy{
			Detector:      "goal_v2",
			MinConfidence: 0.8,
		},
		Delivery: DeliveryConfig{Mode: "poll"},
	}

	err := req.Validate()
	if err == nil {
		t.Fatal("expected error for unsupported execution mode")
	}
}

func TestGoalRequest_Validate_UnsupportedDurability(t *testing.T) {
	req := GoalRequest{
		Version:       1,
		Enabled:       true,
		Instruction:   "test",
		ExecutionMode: ExecutionModeContinuous,
		Durability:    "invalid_durability",
		Limits: GoalLimits{
			MaxWallTimeSeconds: 3600,
			MaxTurns:           10,
		},
		CompletionPolicy: CompletionPolicy{
			Detector:      "goal_v2",
			MinConfidence: 0.8,
		},
		Delivery: DeliveryConfig{Mode: "poll"},
	}

	err := req.Validate()
	if err == nil {
		t.Fatal("expected error for unsupported durability")
	}
}

func TestGoalRequest_Validate_MissingDeliveryMode(t *testing.T) {
	req := GoalRequest{
		Version:       1,
		Enabled:       true,
		Instruction:   "test",
		ExecutionMode: ExecutionModeContinuous,
		Durability:    DurabilityDurable,
		Limits: GoalLimits{
			MaxWallTimeSeconds: 3600,
			MaxTurns:           10,
		},
		CompletionPolicy: CompletionPolicy{
			Detector:      "goal_v2",
			MinConfidence: 0.8,
		},
		Delivery: DeliveryConfig{},
	}

	err := req.Validate()
	if err == nil {
		t.Fatal("expected error for missing delivery mode")
	}
}

func TestGoalLimits_Validate_NegativeValues(t *testing.T) {
	tests := []struct {
		name   string
		limits GoalLimits
	}{
		{
			name: "zero wall time",
			limits: GoalLimits{
				MaxWallTimeSeconds: 0,
				MaxTurns:           10,
			},
		},
		{
			name: "zero turns",
			limits: GoalLimits{
				MaxWallTimeSeconds: 3600,
				MaxTurns:           0,
			},
		},
		{
			name: "negative follow ups",
			limits: GoalLimits{
				MaxWallTimeSeconds: 3600,
				MaxTurns:           10,
				MaxFollowUps:       -1,
			},
		},
		{
			name: "negative model switches",
			limits: GoalLimits{
				MaxWallTimeSeconds: 3600,
				MaxTurns:           10,
				MaxModelSwitches:   -1,
			},
		},
		{
			name: "negative handoffs",
			limits: GoalLimits{
				MaxWallTimeSeconds: 3600,
				MaxTurns:           10,
				MaxHandoffs:        -1,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.limits.Validate()
			if err == nil {
				t.Fatalf("expected error for %s", tt.name)
			}
		})
	}
}

func TestGoalLimits_Validate_ExceedsMax(t *testing.T) {
	tests := []struct {
		name   string
		limits GoalLimits
	}{
		{
			name: "wall time exceeds 7 days",
			limits: GoalLimits{
				MaxWallTimeSeconds: 604801,
				MaxTurns:           10,
			},
		},
		{
			name: "turns exceeds 500",
			limits: GoalLimits{
				MaxWallTimeSeconds: 3600,
				MaxTurns:           501,
			},
		},
		{
			name: "follow ups exceeds 500",
			limits: GoalLimits{
				MaxWallTimeSeconds: 3600,
				MaxTurns:           10,
				MaxFollowUps:       501,
			},
		},
		{
			name: "model switches exceeds 10",
			limits: GoalLimits{
				MaxWallTimeSeconds: 3600,
				MaxTurns:           10,
				MaxModelSwitches:   11,
			},
		},
		{
			name: "handoffs exceeds 20",
			limits: GoalLimits{
				MaxWallTimeSeconds: 3600,
				MaxTurns:           10,
				MaxHandoffs:        21,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.limits.Validate()
			if err == nil {
				t.Fatalf("expected error for %s", tt.name)
			}
		})
	}
}

func TestCompletionPolicy_Validate_MissingDetector(t *testing.T) {
	policy := CompletionPolicy{
		Detector:      "",
		MinConfidence: 0.8,
	}

	err := policy.Validate()
	if err == nil {
		t.Fatal("expected error for missing detector")
	}
}

func TestCompletionPolicy_Validate_InvalidConfidence(t *testing.T) {
	tests := []struct {
		name       string
		confidence float64
	}{
		{"negative confidence", -0.1},
		{"confidence > 1.0", 1.1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := CompletionPolicy{
				Detector:      "goal_v2",
				MinConfidence: tt.confidence,
			}

			err := policy.Validate()
			if err == nil {
				t.Fatalf("expected error for %s", tt.name)
			}
		})
	}
}

func TestTightenLimits_AppliesTenantLimits(t *testing.T) {
	requested := GoalLimits{
		MaxWallTimeSeconds: 86400,
		MaxTurns:           100,
		MaxFollowUps:       100,
		MaxModelSwitches:   5,
		MaxHandoffs:        10,
	}

	tightened := TightenLimits(requested, 3600, 20, 30)

	if tightened.MaxWallTimeSeconds != 3600 {
		t.Errorf("expected wall time 3600, got %d", tightened.MaxWallTimeSeconds)
	}

	if tightened.MaxTurns != 20 {
		t.Errorf("expected turns 20, got %d", tightened.MaxTurns)
	}

	if tightened.MaxFollowUps != 30 {
		t.Errorf("expected follow ups 30, got %d", tightened.MaxFollowUps)
	}
}

func TestTightenLimits_AppliesPlatformCaps(t *testing.T) {
	requested := GoalLimits{
		MaxWallTimeSeconds: 1000000,
		MaxTurns:           1000,
		MaxFollowUps:       1000,
		MaxModelSwitches:   50,
		MaxHandoffs:        100,
	}

	tightened := TightenLimits(requested, 0, 0, 0)

	if tightened.MaxWallTimeSeconds != 604800 {
		t.Errorf("expected wall time capped at 604800, got %d", tightened.MaxWallTimeSeconds)
	}

	if tightened.MaxTurns != 500 {
		t.Errorf("expected turns capped at 500, got %d", tightened.MaxTurns)
	}

	if tightened.MaxFollowUps != 500 {
		t.Errorf("expected follow ups capped at 500, got %d", tightened.MaxFollowUps)
	}

	if tightened.MaxModelSwitches != 10 {
		t.Errorf("expected model switches capped at 10, got %d", tightened.MaxModelSwitches)
	}

	if tightened.MaxHandoffs != 20 {
		t.Errorf("expected handoffs capped at 20, got %d", tightened.MaxHandoffs)
	}
}

func TestTightenLimits_PreservesValidRequest(t *testing.T) {
	requested := GoalLimits{
		MaxWallTimeSeconds: 3600,
		MaxTurns:           10,
		MaxFollowUps:       10,
		MaxModelSwitches:   2,
		MaxHandoffs:        3,
	}

	tightened := TightenLimits(requested, 7200, 50, 50)

	if tightened.MaxWallTimeSeconds != 3600 {
		t.Errorf("expected wall time preserved at 3600, got %d", tightened.MaxWallTimeSeconds)
	}

	if tightened.MaxTurns != 10 {
		t.Errorf("expected turns preserved at 10, got %d", tightened.MaxTurns)
	}

	if tightened.MaxFollowUps != 10 {
		t.Errorf("expected follow ups preserved at 10, got %d", tightened.MaxFollowUps)
	}
}

func TestCreatePolicySnapshot(t *testing.T) {
	limits := GoalLimits{
		MaxWallTimeSeconds: 3600,
		MaxTurns:           20,
		MaxFollowUps:       20,
		MaxModelSwitches:   3,
		MaxHandoffs:        5,
	}

	snapshot := CreatePolicySnapshot("tenant_123", "key_456", limits)

	if snapshot.Version != 1 {
		t.Errorf("expected version 1, got %d", snapshot.Version)
	}

	if snapshot.TenantID != "tenant_123" {
		t.Errorf("expected tenant_123, got %s", snapshot.TenantID)
	}

	if snapshot.APIKeyID != "key_456" {
		t.Errorf("expected key_456, got %s", snapshot.APIKeyID)
	}

	if snapshot.EffectiveLimits.MaxTurns != 20 {
		t.Errorf("expected 20 turns, got %d", snapshot.EffectiveLimits.MaxTurns)
	}

	if snapshot.CreatedAt.IsZero() {
		t.Error("expected non-zero created_at")
	}
}

func TestPolicySnapshot_JSON(t *testing.T) {
	limits := GoalLimits{
		MaxWallTimeSeconds: 3600,
		MaxTurns:           20,
		MaxFollowUps:       20,
		MaxModelSwitches:   3,
		MaxHandoffs:        5,
	}

	snapshot := CreatePolicySnapshot("tenant_123", "key_456", limits)

	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("failed to marshal snapshot: %v", err)
	}

	var decoded PolicySnapshot
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal snapshot: %v", err)
	}

	if decoded.TenantID != snapshot.TenantID {
		t.Errorf("tenant_id mismatch: expected %s, got %s", snapshot.TenantID, decoded.TenantID)
	}

	if decoded.EffectiveLimits.MaxTurns != snapshot.EffectiveLimits.MaxTurns {
		t.Errorf("max_turns mismatch: expected %d, got %d", snapshot.EffectiveLimits.MaxTurns, decoded.EffectiveLimits.MaxTurns)
	}
}
