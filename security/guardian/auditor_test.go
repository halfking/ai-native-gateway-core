package guardian

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
)

func TestNewAuditor(t *testing.T) {
	a := NewAuditor(nil)
	if a == nil {
		t.Fatal("NewAuditor returned nil")
	}
}

func TestAuditor_Log(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	a := NewAuditor(logger)

	a.Log(context.Background(), &AuditEvent{
		TenantID: "t-1",
		Guard:    "test-guard",
		Action:   "block",
		Message:  "test message",
		Blocked:  true,
	})

	output := buf.String()
	if !contains(output, "t-1") {
		t.Fatalf("log missing tenant_id: %s", output)
	}
	if !contains(output, "test-guard") {
		t.Fatalf("log missing guard: %s", output)
	}
	if !contains(output, "block") {
		t.Fatalf("log missing action: %s", output)
	}
}

func TestAuditor_Log_NilEvent(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	a := NewAuditor(logger)

	a.Log(context.Background(), nil)

	if buf.Len() > 0 {
		t.Fatal("nil event should not log")
	}
}

func TestAuditor_LogVerdicts(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	a := NewAuditor(logger)

	verdicts := []*GuardVerdict{
		{Action: ActionPass, GuardName: "g1", Message: "pass"},
		{Action: ActionBlock, GuardName: "g2", Message: "blocked"},
	}

	a.LogVerdicts(context.Background(), "t-1", verdicts)

	output := buf.String()
	if !contains(output, "g1") || !contains(output, "g2") {
		t.Fatalf("log missing guards: %s", output)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsStr(s, substr)
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
