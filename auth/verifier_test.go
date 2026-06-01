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
