package bg

import (
	"context"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

func TestNewBrokenProbeReviver_Defaults(t *testing.T) {
	w := NewBrokenProbeReviver(nil, 0, 0)
	if w == nil {
		t.Fatal("nil")
	}
	if w.interval != 30*time.Minute {
		t.Errorf("interval=%v want 30m", w.interval)
	}
	if w.reviveAfter != 30*time.Minute {
		t.Errorf("reviveAfter=%v want 30m", w.reviveAfter)
	}
}

func TestBrokenProbeReviver_Revive_NilPool(t *testing.T) {
	w := NewBrokenProbeReviver(nil, 0, 0)
	if err := w.revive(context.Background()); err != nil {
		t.Errorf("nil pool should be no-op, got %v", err)
	}
}

func TestBrokenProbeReviver_Stop_Idempotent(t *testing.T) {
	w := NewBrokenProbeReviver(nil, 10*time.Hour, 10*time.Hour)
	w.Start(context.Background())
	time.Sleep(20 * time.Millisecond)
	w.Stop()
	w.Stop() // must not panic
}

// TestBrokenProbeReviver_SkipsUnderNewProbeMode pins the R36 gate: under the
// new probe mode (the default) the reviver must not touch the frozen
// model_probe_state table — reviving frozen broken_confirmed rows into
// 'recovering' would dissolve the credential-recovery all-models-broken
// guard and re-admit credentials the new system has proven dead. pgxmock has
// no expectations registered, so any Exec attempt fails the test.
func TestBrokenProbeReviver_SkipsUnderNewProbeMode(t *testing.T) {
	t.Setenv("LLM_GATEWAY_USE_NEW_PROBE_MODE", "true") // explicit; unset defaults to true as well
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	w := &BrokenProbeReviver{db: mock, interval: time.Minute, reviveAfter: time.Minute, stopCh: make(chan struct{})}
	if err := w.revive(context.Background()); err != nil {
		t.Fatalf("revive under new probe mode must be a silent no-op, got %v", err)
	}
}

// TestBrokenProbeReviver_RevivesInLegacyMode keeps the legacy rollback path
// honest: with LLM_GATEWAY_USE_NEW_PROBE_MODE=false the legacy workers still
// own model_probe_state, so the reviver must run its re-queue UPDATE.
func TestBrokenProbeReviver_RevivesInLegacyMode(t *testing.T) {
	t.Setenv("LLM_GATEWAY_USE_NEW_PROBE_MODE", "false")
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectExec("UPDATE model_probe_state").
		WithArgs(float64(time.Minute.Seconds())).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	w := &BrokenProbeReviver{db: mock, interval: time.Minute, reviveAfter: time.Minute, stopCh: make(chan struct{})}
	if err := w.revive(context.Background()); err != nil {
		t.Fatalf("revive in legacy mode: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestProbeGuardStateTable_FollowsProbeMode pins the credential-recovery
// guard's data source to the ACTIVE probe system: the new system's verdicts
// arrive through v_node_probe_state_compat (same credential/raw_model/state
// vocabulary), the legacy workers keep writing model_probe_state.
func TestProbeGuardStateTable_FollowsProbeMode(t *testing.T) {
	t.Setenv("LLM_GATEWAY_USE_NEW_PROBE_MODE", "true")
	if got := probeGuardStateTable(); got != "v_node_probe_state_compat mps" {
		t.Fatalf("probeGuardStateTable() under new mode = %q, want the node_probe_state compat projection", got)
	}

	t.Setenv("LLM_GATEWAY_USE_NEW_PROBE_MODE", "false")
	if got := probeGuardStateTable(); got != "model_probe_state mps" {
		t.Fatalf("probeGuardStateTable() under legacy mode = %q, want model_probe_state", got)
	}
}
