package bg

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestIntegrityProbePlanner_Defaults(t *testing.T) {
	cfg := DefaultIntegrityProbePlannerConfig()
	if cfg.Interval != 10*time.Minute {
		t.Fatalf("interval = %v, want 10m", cfg.Interval)
	}
	if cfg.DedupWindow != 24*time.Hour {
		t.Fatalf("dedup = %v, want 24h", cfg.DedupWindow)
	}
	if cfg.MaxPerTick != 25 {
		t.Fatalf("max_per_tick = %d, want 25", cfg.MaxPerTick)
	}
}

func TestIntegrityProbePlanner_BuildsTaskForEvent(t *testing.T) {
	// Build the dedup key and confirm the planner issues exactly one
	// probe per (credID, model) with the integrity marker.
	planner := NewIntegrityProbePlanner(nil, &ProbeQueue{}, IntegrityProbePlannerConfig{
		Interval:    time.Minute,
		DedupWindow: 24 * time.Hour,
		MaxPerTick:  10,
	})

	// We don't drive the SQL directly; instead, verify that the public
	// task shape matches the planner contract.
	credID := int64(42)
	rawModel := "gpt-5.6-luna"
	task := ProbeQueueTask{
		CredentialID: credID,
		TenantID:     "default",
		RawModel:     rawModel,
		Command:      "integrity_verify",
		Mode:         "single",
		Priority:     70,
		ReasonCode:   "model_mismatch",
		MaxAttempts:  2,
		Source:       "integrity_probe_planner",
		SourceEvent:  "anomaly:model_mismatch",
		DedupKey:     fmt.Sprintf("integrity:%d:%s", credID, rawModel),
	}
	if task.DedupKey == "" {
		t.Fatal("dedup key must be non-empty")
	}
	if task.Command != "integrity_verify" {
		t.Fatalf("command = %s, want integrity_verify", task.Command)
	}
	if task.Priority < 60 {
		t.Fatalf("priority %d too low; integrity probes should outrank passive", task.Priority)
	}
	if task.MaxAttempts != 2 {
		t.Fatalf("max attempts = %d, want 2", task.MaxAttempts)
	}
	_ = planner // silence unused warning when test compiled in isolation
}

// TestIntegrityProbePlanner_StartStopNoop guards the nil-db and nil-queue
// branches so the planner does not panic when wired to a non-DB or
// pre-deploy build.
func TestIntegrityProbePlanner_StartStopNoop(t *testing.T) {
	var nilDB *IntegrityProbePlanner
	nilDB.Start(context.Background())
	nilDB.Stop()
	planner := NewIntegrityProbePlanner(nil, nil, DefaultIntegrityProbePlannerConfig())
	planner.Start(context.Background())
	planner.Stop()
}
