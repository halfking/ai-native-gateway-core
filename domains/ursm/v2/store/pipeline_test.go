package store

import (
	"context"
	"strconv"
	"testing"
	"time"
)

func TestPipelineDecodesScoringFields(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := context.Background()
	mr.HSet("ursm:v2:node:tenant-a:9:model", "available", "1")
	mr.HSet("ursm:v2:node:tenant-a:9:model", "lat_ewma_ms", "123")
	mr.HSet("ursm:v2:node:tenant-a:9:model", "sr_5m", "0.91")
	mr.HSet("ursm:v2:node:tenant-a:9:model", "samples_5m", "17")
	views, err := s.PipelineNodeViews(ctx, "ursm:v2:", []NodeQuery{{
		TenantID: "tenant-a", CredentialID: 9, RawModel: "model",
	}})
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	if len(views) != 1 || views[0].LatEWMA != 123 || views[0].SR5m != 0.91 || views[0].Samples5m != 17 {
		t.Fatalf("scoring fields were not decoded: %+v", views)
	}
}

func TestPipelineBatchReadMissing(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	views, err := s.PipelineNodeViews(ctx, "ursm:v2:", []NodeQuery{
		{CredentialID: 1, RawModel: "gpt"},
		{CredentialID: 2, RawModel: "gpt"},
	})
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("expected 2 views, got %d", len(views))
	}
	if views[0].Available {
		t.Fatalf("missing node must default to unavailable")
	}
}

// TestPipelineExpiredCoolHalfOpen covers the 2026-08-18 154 incident
// (minimax-m3, cred 36): record_request.lua disables a node on a fail
// streak with a bounded cool window, and its own recovery branch only
// fires when a NEW event arrives. The read side kept returning the stale
// available="0" bit after cool_until expired, the router kept refusing to
// route traffic to the node, so the write-side recovery never triggered —
// the node stayed "no available candidates" until a process restart. An
// expired cool window must read as available (half-open).
func TestPipelineExpiredCoolHalfOpen(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := context.Background()
	past := time.Now().Add(-6 * time.Minute).UnixMilli()
	mr.HSet("ursm:v2:node:default:36:minimax-m3",
		"available", "0",
		"disabled", "1",
		"fail_streak", "3",
		"cool_until_ms", strconv.FormatInt(past, 10),
		"last_err", "provider_error")
	views, err := s.PipelineNodeViews(ctx, "ursm:v2:", []NodeQuery{{
		TenantID: "default", CredentialID: 36, RawModel: "minimax-m3",
	}})
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("expected 1 view, got %d", len(views))
	}
	if !views[0].Available {
		t.Fatalf("expired cool_until must half-open the node, got unavailable (cool_until=%s)", views[0].CoolUntil)
	}
	if views[0].Reason == "in_cool_until" {
		t.Fatalf("stale cooling reason must be cleared on half-open, got %q", views[0].Reason)
	}
}

// TestPipelineFutureCoolStillBlocks guards the other side of the same
// rule: a cool window still in the future keeps the node unavailable even
// if the available bit was optimistically set to "1".
func TestPipelineFutureCoolStillBlocks(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := context.Background()
	future := time.Now().Add(4 * time.Minute).UnixMilli()
	mr.HSet("ursm:v2:node:default:36:minimax-m3",
		"available", "1",
		"disabled", "1",
		"cool_until_ms", strconv.FormatInt(future, 10))
	views, err := s.PipelineNodeViews(ctx, "ursm:v2:", []NodeQuery{{
		TenantID: "default", CredentialID: 36, RawModel: "minimax-m3",
	}})
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	if views[0].Available {
		t.Fatalf("future cool_until must keep the node unavailable")
	}
}

// TestPipelineDisabledWithoutCoolStaysUnavailable: a disabled bit with no
// cool window (admin hold style) is NOT half-opened — only bounded cool
// windows recover automatically.
func TestPipelineDisabledWithoutCoolStaysUnavailable(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := context.Background()
	mr.HSet("ursm:v2:node:default:5:model",
		"available", "0",
		"disabled", "1")
	views, err := s.PipelineNodeViews(ctx, "ursm:v2:", []NodeQuery{{
		TenantID: "default", CredentialID: 5, RawModel: "model",
	}})
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	if views[0].Available {
		t.Fatalf("disabled without a cool window must stay unavailable")
	}
}

// TestPipelineManualHoldBeatsExpiredCool: apply_admin.lua writes
// manual_hold=1 + available=0 without clearing a pre-existing cool_until_ms,
// so an admin force_disable issued during an auto-cool window must NOT be
// half-opened back into rotation when the window expires (audit guard for
// the 2026-08-18 half-open fix).
func TestPipelineManualHoldBeatsExpiredCool(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := context.Background()
	past := time.Now().Add(-10 * time.Minute).UnixMilli()
	mr.HSet("ursm:v2:node:default:22:glm-5.2",
		"available", "0",
		"disabled", "1",
		"manual_hold", "1",
		"cool_until_ms", strconv.FormatInt(past, 10))
	views, err := s.PipelineNodeViews(ctx, "ursm:v2:", []NodeQuery{{
		TenantID: "default", CredentialID: 22, RawModel: "glm-5.2",
	}})
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	if views[0].Available {
		t.Fatalf("manual_hold must keep the node unavailable even with an expired cool window")
	}
}
