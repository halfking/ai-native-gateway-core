package admin

// V3.3-OBS OBS-BE3 contract tests (docs/会话优化v3/13 号 §4): the
// queue_snapshot pipeline layer on the SSE wire.
//
//   - pipeline + sourceVersion are optional: the JSON keys must be entirely
//     ABSENT when dispatch is not enabled (not zero-value placeholders);
//   - when present, depth/inFlight 0 is a real zero and must serialize,
//     while waitingMsP50/P95 are absent when the ring window has no sample.

import (
	"encoding/json"
	"testing"
)

func marshalToMap(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return m
}

func TestLiveQueueSnapshotJSON_DispatchDisabledOmitsPipeline(t *testing.T) {
	snap := &LiveQueueSnapshot{Enabled: false, Wired: false}

	m := marshalToMap(t, snap)
	if _, ok := m["pipeline"]; ok {
		t.Error("pipeline key must be absent when dispatch is disabled")
	}
	if _, ok := m["sourceVersion"]; ok {
		t.Error("sourceVersion key must be absent when dispatch is disabled")
	}
	if _, ok := m["enabled"]; !ok {
		t.Error("enabled/wired semantics must be unchanged (enabled key present)")
	}
	if _, ok := m["wired"]; !ok {
		t.Error("enabled/wired semantics must be unchanged (wired key present)")
	}
}

func TestLiveQueueSnapshotJSON_PipelinePresentCarriesRealStats(t *testing.T) {
	p50 := int64(120)
	p95 := int64(980)
	snap := &LiveQueueSnapshot{
		Enabled:       true,
		Wired:         true,
		SourceVersion: 42,
		Pipeline: &LiveQueuePipelineStats{
			Depth:        0, // real zero: queues empty at snapshot time
			WaitingMsP50: &p50,
			WaitingMsP95: &p95,
			InFlight:     3,
			Degraded:     false,
		},
	}

	m := marshalToMap(t, snap)
	if got, ok := m["sourceVersion"].(float64); !ok || int64(got) != 42 {
		t.Errorf("sourceVersion = %v, want 42", m["sourceVersion"])
	}
	p, ok := m["pipeline"].(map[string]any)
	if !ok {
		t.Fatalf("pipeline object missing: %v", m)
	}
	if v, ok := p["depth"].(float64); !ok || v != 0 {
		t.Errorf("pipeline.depth must serialize real zero 0, got %v", p["depth"])
	}
	if v, ok := p["inFlight"].(float64); !ok || int64(v) != 3 {
		t.Errorf("pipeline.inFlight = %v, want 3", p["inFlight"])
	}
	if v, ok := p["waitingMsP50"].(float64); !ok || int64(v) != 120 {
		t.Errorf("pipeline.waitingMsP50 = %v, want 120", p["waitingMsP50"])
	}
	if v, ok := p["waitingMsP95"].(float64); !ok || int64(v) != 980 {
		t.Errorf("pipeline.waitingMsP95 = %v, want 980", p["waitingMsP95"])
	}
	if v, ok := p["degraded"].(bool); !ok || v {
		t.Errorf("pipeline.degraded = %v, want false (serialized, not omitted)", p["degraded"])
	}
}

func TestLiveQueueSnapshotJSON_NoWaitingSamplesOmitsPercentiles(t *testing.T) {
	snap := &LiveQueueSnapshot{
		Enabled:       true,
		Wired:         true,
		SourceVersion: 7,
		Pipeline: &LiveQueuePipelineStats{
			Depth:    5,
			InFlight: 2,
		},
	}

	m := marshalToMap(t, snap)
	p, ok := m["pipeline"].(map[string]any)
	if !ok {
		t.Fatalf("pipeline object missing: %v", m)
	}
	if _, ok := p["waitingMsP50"]; ok {
		t.Error("waitingMsP50 must be absent when no window sample exists (not 0)")
	}
	if _, ok := p["waitingMsP95"]; ok {
		t.Error("waitingMsP95 must be absent when no window sample exists (not 0)")
	}
	if _, ok := p["depth"]; !ok {
		t.Error("depth must be present even when percentiles are absent")
	}
}

func TestLiveStreamEnvelopeJSON_QueueSnapshotOptional(t *testing.T) {
	// Envelope without queue data: the queue key stays absent (unchanged
	// semantics); with pipeline-carrying queue data the nested keys ride
	// along.
	plain := marshalToMap(t, LiveStreamEnvelope{Type: "queue_snapshot"})
	if _, ok := plain["queue"]; ok {
		t.Error("queue key must be absent on an envelope without queue data")
	}

	p95 := int64(500)
	withQueue := LiveStreamEnvelope{Type: "queue_snapshot"}
	withQueue.Queue = &LiveQueueSnapshot{
		Enabled:       true,
		Wired:         true,
		SourceVersion: 1,
		Pipeline: &LiveQueuePipelineStats{
			Depth:        2,
			WaitingMsP95: &p95,
			InFlight:     1,
			Degraded:     true,
		},
	}
	m := marshalToMap(t, withQueue)
	q, ok := m["queue"].(map[string]any)
	if !ok {
		t.Fatalf("queue object missing: %v", m)
	}
	if _, ok := q["pipeline"]; !ok {
		t.Error("queue.pipeline must be present when supplied")
	}
}
