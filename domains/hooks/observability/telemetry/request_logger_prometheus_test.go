package telemetry

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/dbdegradation"
	dto "github.com/prometheus/client_model/go"
)

// walCounterValue fetches the float value of one request_wal_events_total
// sample. Pre-touch with .Add(0) so the series exists before reading.
func walCounterValue(t *testing.T, event string) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := requestWALEventsTotal.WithLabelValues(event).Write(m); err != nil {
		t.Fatalf("counter write for event=%q: %v", event, err)
	}
	return m.Counter.GetValue()
}

// TestRequestWALEventsTotal_Registered asserts the Prometheus surface
// exists with the spec §12 contract: a CounterVec named
// "request_wal_events_total" with an "event" label and all seven event
// values pre-initialised so dashboards never see a missing label.
func TestRequestWALEventsTotal_Registered(t *testing.T) {
	if requestWALEventsTotal == nil {
		t.Fatal("requestWALEventsTotal CounterVec is nil; spec §12 GAP 1 not implemented")
	}
	// Every event label the spec mandates must resolve to a series.
	for _, ev := range []string{
		"queue_overflow",
		"fallback_write_failure",
		"unrecoverable_fallback",
		"replay_attempt",
		"replay_success",
		"replay_failure",
		"replay_marker",
	} {
		// Reading must not panic; the series is pre-initialised.
		_ = walCounterValue(t, ev)
	}
}

// TestRequestWALEventsTotal_MirrorRecordOverflow pins the sync-Inc
// contract: recordOverflow bumps BOTH the in-process atomic counter
// AND the Prometheus "queue_overflow" series (delta mode).
func TestRequestWALEventsTotal_MirrorRecordOverflow(t *testing.T) {
	if requestWALEventsTotal == nil {
		t.Fatal("requestWALEventsTotal CounterVec is nil")
	}
	fallback := newStubBackupWriter()
	rl := &RequestLogger{
		config:     &RequestLoggerConfig{Enabled: true},
		asyncQueue: make(chan *LogUpdate, 1),
		fallback:   fallback,
		done:       make(chan struct{}),
	}
	// Pre-touch so the delta read is deterministic.
	requestWALEventsTotal.WithLabelValues("queue_overflow").Add(0)
	before := walCounterValue(t, "queue_overflow")

	rl.Update(&LogUpdate{RequestID: "prom-1", Stage: StageCompressed})
	rl.Update(&LogUpdate{RequestID: "prom-2", Stage: StageTransformed}) // queue full

	stats := rl.OverflowCounts()
	if stats.QueueOverflow != 1 {
		t.Fatalf("in-process QueueOverflow = %d, want 1", stats.QueueOverflow)
	}
	after := walCounterValue(t, "queue_overflow")
	if got := after - before; got != 1 {
		t.Fatalf("prometheus queue_overflow delta = %v, want 1 (in-process=%d)", got, stats.QueueOverflow)
	}
}

// TestRequestWALEventsTotal_MirrorReplayFallback pins the replay path:
// ReplayFallback must bump the prometheus replay_attempt /
// replay_success / replay_failure / replay_marker series in lock-step
// with the atomic counters.
func TestRequestWALEventsTotal_MirrorReplayFallback(t *testing.T) {
	if requestWALEventsTotal == nil {
		t.Fatal("requestWALEventsTotal CounterVec is nil")
	}
	rl := &RequestLogger{
		config: &RequestLoggerConfig{Enabled: true},
		db:     nil, // forces "database not configured" failure path
	}
	for _, ev := range []string{
		"replay_attempt", "replay_success", "replay_failure", "replay_marker",
	} {
		requestWALEventsTotal.WithLabelValues(ev).Add(0)
	}
	bAttempt := walCounterValue(t, "replay_attempt")
	bSuccess := walCounterValue(t, "replay_success")
	bFailure := walCounterValue(t, "replay_failure")

	// A marker record → replay_marker + replay_attempt + replay_success.
	marker := requestLoggerOverflowMarker{
		Kind:      overflowMarkerKind,
		RequestID: "replay-marker",
		Stage:     StageCompressed,
		Status:    StatusPending,
	}
	payload, _ := json.Marshal(marker)
	if err := rl.ReplayFallback(context.Background(), dbdegradation.BackupRecord{
		RecordKey: overflowRecordPrefix + "replay-marker",
		Payload:   payload,
	}); err != nil {
		t.Fatalf("replay marker: %v", err)
	}
	if got := walCounterValue(t, "replay_attempt") - bAttempt; got != 1 {
		t.Fatalf("replay_attempt delta = %v, want 1", got)
	}
	if got := walCounterValue(t, "replay_success") - bSuccess; got != 1 {
		t.Fatalf("replay_success delta = %v, want 1", got)
	}

	// A :update record with no DB configured → replay_attempt +
	// replay_failure (persistUpdate path errors on nil db inside Replay).
	upd := &LogUpdate{RequestID: "replay-fail", Stage: StageTransformed}
	updPayload, _ := json.Marshal(upd)
	bAttempt2 := walCounterValue(t, "replay_attempt")
	bFailure2 := walCounterValue(t, "replay_failure")
	if err := rl.ReplayFallback(context.Background(), dbdegradation.BackupRecord{
		RecordKey: "replay-fail:update",
		Payload:   updPayload,
	}); err == nil {
		t.Fatal("expected replay failure for nil-db update path")
	}
	if got := walCounterValue(t, "replay_attempt") - bAttempt2; got != 1 {
		t.Fatalf("replay_attempt delta (fail path) = %v, want 1", got)
	}
	if got := walCounterValue(t, "replay_failure") - bFailure2; got != 1 {
		t.Fatalf("replay_failure delta = %v, want 1", got)
	}
	_ = bFailure // silence
}
