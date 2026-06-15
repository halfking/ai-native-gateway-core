package auth

import (
	"context"
	"testing"
)

func TestKeyVerifier_Disabled(t *testing.T) {
	kv := NewKeyVerifier()
	if kv.Enabled() {
		t.Fatal("no DB should be disabled")
	}
	_, err := kv.Verify(context.Background(), "sk-test")
	if err == nil {
		t.Fatal("disabled verifier should return error")
	}
}

func TestKeyVerifier_EnabledRequiresDB(t *testing.T) {
	kv := NewKeyVerifier()
	if kv.Enabled() {
		t.Fatal("no DB configured should be disabled")
	}
}

func TestKeyVerifier_InvalidKeyError(t *testing.T) {
	err := &InvalidKeyError{Message: "Invalid or expired API key"}
	if err.Error() != "Invalid or expired API key" {
		t.Errorf("unexpected error message: %s", err.Error())
	}
}

func TestKeyVerifier_BudgetExceededError(t *testing.T) {
	var err error = &BudgetExceededError{KeyID: 1, Budget: 100.0, Spent: 150.0}
	if err.Error() == "" {
		t.Error("expected non-empty error message")
	}
	if _, ok := err.(*BudgetExceededError); !ok {
		t.Error("should be BudgetExceededError")
	}
}

func TestIsBudgetExceeded(t *testing.T) {
	if isBudgetExceeded(&BudgetExceededError{}) == false {
		t.Error("should recognize BudgetExceededError")
	}
	if isBudgetExceeded(&InvalidKeyError{}) == true {
		t.Error("should not recognize InvalidKeyError as budget exceeded")
	}
}

func intPtr(v int) *int { return &v }

func TestEffectiveRPM(t *testing.T) {
	tests := []struct {
		name     string
		rpm      *int
		tier     string
		expected int
	}{
		{"nil falls back to default tier", nil, "default", 12},
		{"nil falls back to production tier", nil, "production", 60},
		{"0 means unlimited", intPtr(0), "default", 0},
		{"0 means unlimited even on production tier", intPtr(0), "production", 0},
		{"positive value used as-is", intPtr(100), "default", 100},
		{"empty tier defaults to default", intPtr(50), "", 50},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ki := &KeyInfo{RateLimitRPM: tt.rpm, KeyTier: tt.tier}
			if got := ki.EffectiveRPM(); got != tt.expected {
				t.Errorf("EffectiveRPM() = %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestEffectiveConcurrent(t *testing.T) {
	tests := []struct {
		name     string
		conc     *int
		tier     string
		expected int
	}{
		{"nil falls back to default tier", nil, "default", 6},
		{"nil falls back to production tier", nil, "production", 20},
		{"0 means unlimited", intPtr(0), "default", 0},
		{"0 means unlimited even on production tier", intPtr(0), "production", 0},
		{"positive value used as-is", intPtr(50), "default", 50},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ki := &KeyInfo{RateLimitConcurrent: tt.conc, KeyTier: tt.tier}
			if got := ki.EffectiveConcurrent(); got != tt.expected {
				t.Errorf("EffectiveConcurrent() = %d, want %d", got, tt.expected)
			}
		})
	}
}
