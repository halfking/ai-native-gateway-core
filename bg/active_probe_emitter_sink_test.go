package bg

import (
	"context"
	"sync"
	"testing"
	"time"
)

// captureSink records every PublishProbeEvent call.
type captureSink struct {
	mu     sync.Mutex
	events []ProbeStreamEvent
}

func (c *captureSink) PublishProbeEvent(evt ProbeStreamEvent) {
	c.mu.Lock()
	c.events = append(c.events, evt)
	c.mu.Unlock()
}

func (c *captureSink) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

func (c *captureSink) last() ProbeStreamEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.events) == 0 {
		return ProbeStreamEvent{}
	}
	return c.events[len(c.events)-1]
}

// TestActiveProbeEmitter_SinkFiresWithoutTelemetry pins the 2026-08-11
// contract that the self-check SSE sink fires INDEPENDENTLY of telemetry.
// A probe result must reach the 自检 tab even when telemetry is disabled —
// the SSE stream is a separate operator-facing surface.
func TestActiveProbeEmitter_SinkFiresWithoutTelemetry(t *testing.T) {
	em := NewActiveProbeEmitter(nil) // no telemetry client
	sink := &captureSink{}
	em.SetProbeSink(sink)

	result := &ProbeResult{
		Status:      ProbeStatusSuccess,
		HTTPStatus:  200,
		LatencyMs:   150,
		StartedAt:   time.Now(),
		CompletedAt: time.Now(),
		Target: ProbeTarget{
			CredentialID:  7,
			ProviderID:    3,
			RawModel:      "gpt-5.6",
			OutboundModel: "gpt-5.6",
		},
	}
	em.Emit(context.Background(), 7, 3, "default", "gpt-5.6", "gpt-5.6", "direct", "req-parent-1", 1, result)

	if sink.count() != 1 {
		t.Fatalf("sink must fire once per Emit even without telemetry, got %d", sink.count())
	}
	evt := sink.last()
	if evt.Status != "ok" {
		t.Errorf("status = %q, want ok", evt.Status)
	}
	if evt.Source != "node_probe" {
		t.Errorf("source = %q, want node_probe", evt.Source)
	}
	if evt.CredentialID != 7 || evt.RawModel != "gpt-5.6" {
		t.Errorf("credential/model not propagated: %+v", evt)
	}
	if evt.LatencyMs == nil || *evt.LatencyMs != 150 {
		t.Errorf("latency not propagated: %v", evt.LatencyMs)
	}
	if evt.HTTPStatus == nil || *evt.HTTPStatus != 200 {
		t.Errorf("http status not propagated: %v", evt.HTTPStatus)
	}
	if evt.ID == "" {
		t.Errorf("ID must be populated (requestID), got empty")
	}
}

// TestActiveProbeEmitter_SinkFailureMapsToFail verifies the failure branch
// sets status=fail and propagates the error code/message.
func TestActiveProbeEmitter_SinkFailureMapsToFail(t *testing.T) {
	em := NewActiveProbeEmitter(nil)
	sink := &captureSink{}
	em.SetProbeSink(sink)

	result := &ProbeResult{
		Status:      ProbeStatusFailed,
		HTTPStatus:  429,
		ErrCode:     "rate_limited",
		ErrMsg:      "rate limit exceeded",
		LatencyMs:   20,
		StartedAt:   time.Now(),
		CompletedAt: time.Now(),
		Target:      ProbeTarget{CredentialID: 9, ProviderID: 2, RawModel: "claude"},
	}
	em.Emit(context.Background(), 9, 2, "default", "claude", "claude", "direct", "req-x", 2, result)

	if sink.count() != 1 {
		t.Fatalf("sink must fire once, got %d", sink.count())
	}
	evt := sink.last()
	if evt.Status != "fail" {
		t.Errorf("status = %q, want fail", evt.Status)
	}
	if evt.ErrCode != "rate_limited" {
		t.Errorf("err_code = %q, want rate_limited", evt.ErrCode)
	}
	if evt.Attempt != 2 {
		t.Errorf("attempt = %d, want 2", evt.Attempt)
	}
}

// TestActiveProbeEmitter_NoSinkIsSafe ensures Emit is safe when no sink is
// wired (the common case for existing tests / older wiring).
func TestActiveProbeEmitter_NoSinkIsSafe(t *testing.T) {
	em := NewActiveProbeEmitter(nil)
	// no SetProbeSink call
	result := &ProbeResult{Status: ProbeStatusSuccess, StartedAt: time.Now(),
		Target: ProbeTarget{CredentialID: 1, ProviderID: 1, RawModel: "m"}}
	em.Emit(context.Background(), 1, 1, "default", "m", "m", "direct", "p", 1, result)
	// reaching here without panic is the assertion
}

// TestProbeSourceForOrigin pins the origin→source mapping.
func TestProbeSourceForOrigin(t *testing.T) {
	cases := map[string]string{
		"integrity":        "integrity",
		"integrity_verify": "integrity",
		"selfcheck":        "selfcheck",
		"self_check":       "selfcheck",
		"direct":           "node_probe",
		"":                 "node_probe",
		"gateway":          "node_probe",
	}
	for in, want := range cases {
		if got := probeSourceForOrigin(in); got != want {
			t.Errorf("probeSourceForOrigin(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestTruncateErrDetail pins the cap so a verbose upstream body cannot blow up
// the SSE envelope.
func TestTruncateErrDetail(t *testing.T) {
	short := "small error"
	if got := truncateErrDetail(short); got != short {
		t.Errorf("short detail must pass through, got %q", got)
	}
	long := ""
	for i := 0; i < 2000; i++ {
		long += "x"
	}
	got := truncateErrDetail(long)
	// 512 chars + "…" (3 UTF-8 bytes) → 515 total. Allow slack for the marker.
	if len(got) > 520 {
		t.Errorf("long detail must be capped near 512, got %d", len(got))
	}
	if len(got) <= 512 {
		t.Errorf("long detail must be truncated (expected ellipsis marker), still %d", len(got))
	}
}
