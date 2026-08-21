package store

import (
	"context"
	"math"
	"strconv"
	"testing"
	"time"
)

func TestRecordRequestEmptyResponseTracksWindowWithoutDisablingNode(t *testing.T) {
	s, mr := newTestStore(t)
	defer mr.Close()

	ctx := context.Background()
	node := "ursm:v2:node:empty-rate"
	w1, w5, w30 := node+":w1", node+":w5", node+":w30"
	now := int64(10_000_000)
	outcomes := []struct {
		success bool
		kind    string
	}{
		{success: false, kind: "empty_response"},
		{success: false, kind: "empty_response"},
		{success: true},
		{success: false, kind: "timeout"},
		{success: false, kind: "empty_response"},
	}
	for i, outcome := range outcomes {
		_, err := s.RecordRequest(ctx, node, w1, w5, w30, RecordOutcome{
			Success: outcome.success, ErrorKind: outcome.kind, NowMs: now + int64(i)*1000,
			LatencyMs: 20, RequestID: "request-" + strconv.Itoa(i), NodeTTL: time.Hour,
			Window5mTTL: 6 * time.Minute, Window30mTTL: 35 * time.Minute,
		})
		if err != nil {
			t.Fatalf("record outcome %d: %v", i, err)
		}
	}

	if got := mr.HGet(node, "samples_5m"); got != "5" {
		t.Fatalf("samples_5m=%q, want 5", got)
	}
	if got := mr.HGet(node, "empty_responses_5m"); got != "3" {
		t.Fatalf("empty_responses_5m=%q, want 3", got)
	}
	rate, err := strconv.ParseFloat(mr.HGet(node, "empty_response_rate_5m"), 64)
	if err != nil || math.Abs(rate-0.6) > 1e-12 {
		t.Fatalf("empty_response_rate_5m=%q err=%v, want 0.6", mr.HGet(node, "empty_response_rate_5m"), err)
	}
	if got := mr.HGet(node, "disabled"); got == "1" {
		t.Fatalf("empty responses must not hard-disable a node, disabled=%q", got)
	}
	if got := mr.HGet(node, "available"); got != "1" {
		t.Fatalf("a first empty response must seed a routable node, available=%q", got)
	}
	if got := mr.HGet(node, "fail_streak"); got != "1" {
		t.Fatalf("empty responses must not advance fail streak; fail_streak=%q, want 1 from timeout only", got)
	}
}

func TestRecordRequestEmptyResponseWindowExpires(t *testing.T) {
	s, mr := newTestStore(t)
	defer mr.Close()

	ctx := context.Background()
	node := "ursm:v2:node:empty-expiry"
	w1, w5, w30 := node+":w1", node+":w5", node+":w30"
	now := int64(20_000_000)
	if _, err := s.RecordRequest(ctx, node, w1, w5, w30, RecordOutcome{
		ErrorKind: "empty_response", NowMs: now, LatencyMs: 20, RequestID: "empty",
		NodeTTL: time.Hour, Window5mTTL: 6 * time.Minute, Window30mTTL: 35 * time.Minute,
	}); err != nil {
		t.Fatalf("record empty response: %v", err)
	}
	if _, err := s.RecordRequest(ctx, node, w1, w5, w30, RecordOutcome{
		Success: true, NowMs: now + 60_001, LatencyMs: 20, RequestID: "success",
		NodeTTL: time.Hour, Window5mTTL: 6 * time.Minute, Window30mTTL: 35 * time.Minute,
	}); err != nil {
		t.Fatalf("record later success: %v", err)
	}

	if got := mr.HGet(node, "samples_1m"); got != "1" {
		t.Fatalf("samples_1m=%q, want 1 after expiry", got)
	}
	if got := mr.HGet(node, "empty_responses_1m"); got != "0" {
		t.Fatalf("empty_responses_1m=%q, want 0 after expiry", got)
	}
	if got := mr.HGet(node, "empty_response_rate_1m"); got != "0" {
		t.Fatalf("empty_response_rate_1m=%q, want 0 after expiry", got)
	}
}

func TestRecordRequestSuccessfulOutcomeDoesNotCountAsEmptyResponse(t *testing.T) {
	s, mr := newTestStore(t)
	defer mr.Close()

	node := "ursm:v2:node:empty-success"
	_, err := s.RecordRequest(context.Background(), node, node+":w1", node+":w5", node+":w30", RecordOutcome{
		Success: true, ErrorKind: "empty_response", NowMs: 30_000_000, LatencyMs: 10, RequestID: "contradictory",
		NodeTTL: time.Hour, Window5mTTL: 6 * time.Minute, Window30mTTL: 35 * time.Minute,
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if got := mr.HGet(node, "empty_responses_5m"); got != "0" {
		t.Fatalf("successful outcome must not count as empty response, got %q", got)
	}
}
