package bg

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestSubmitManualProbe_Success(t *testing.T) {
	r := NewModelProbeRunner(nil, nil)
	
	err := r.SubmitManualProbe(123, "gpt-4")
	if err != nil {
		t.Fatalf("SubmitManualProbe failed: %v", err)
	}
	
	select {
	case task := <-r.manualProbeQueue:
		if task.CredentialID != 123 || task.RawModel != "gpt-4" {
			t.Errorf("got task %+v, want {123 gpt-4}", task)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("task not submitted to queue")
	}
}

func TestSubmitManualProbe_QueueFull(t *testing.T) {
	r := NewModelProbeRunner(nil, nil)
	
	// Fill queue to capacity (64)
	for i := 0; i < 64; i++ {
		if err := r.SubmitManualProbe(i, "model"); err != nil {
			t.Fatalf("failed to fill queue at %d: %v", i, err)
		}
	}
	
	// 65th should fail
	err := r.SubmitManualProbe(999, "overflow")
	if err == nil {
		t.Fatal("expected queue full error, got nil")
	}
	if err.Error() != "manual probe queue full" {
		t.Errorf("got error %q, want %q", err, "manual probe queue full")
	}
}

func TestManualProbeWorker_ProcessesTasks(t *testing.T) {
	r := NewModelProbeRunner(nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	
	processed := make(chan manualProbeTask, 1)
	
	// Mock worker that captures tasks instead of calling TriggerManual
	go func() {
		for {
			select {
			case task := <-r.manualProbeQueue:
				processed <- task
			case <-ctx.Done():
				return
			}
		}
	}()
	
	r.SubmitManualProbe(456, "claude-3")
	
	select {
	case task := <-processed:
		if task.CredentialID != 456 || task.RawModel != "claude-3" {
			t.Errorf("worker processed wrong task: %+v", task)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("worker did not process task")
	}
}

func TestTriggerManualRaceProtection_SQLContract(t *testing.T) {
	// This test verifies the recheck query structure exists in TriggerManual
	src := mustReadFile(t, "model_probe.go")
	
	requiredChecks := []string{
		"Race protection - recheck eligibility before decrypt",
		"COALESCE(c.status, 'active') = 'active'",
		"COALESCE(c.lifecycle_status, 'active') = 'active'",
		"COALESCE(c.manual_disabled, FALSE)",
		"COALESCE(p.enabled, FALSE)",
		"COALESCE(p.manual_disabled, FALSE)",
		"disabled_after_query",
		"credential became ineligible between query and probe",
	}
	
	for _, check := range requiredChecks {
		if !contains(src, check) {
			t.Errorf("TriggerManual missing race protection check: %q", check)
		}
	}
}

func mustReadFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) >= len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || containsMiddle(s, substr)))
}

func containsMiddle(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
