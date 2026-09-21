package streaming

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
)

// Regression for 2026-09-05 audit B1: in lite storage mode PostgreSQL is
// intentionally bypassed, so a nil db connector is a configuration fact —
// /readyz must not report not_ready 503 forever.
func TestReadyzLiteModeReadyWithoutDB(t *testing.T) {
	h := NewHealthHandler(nil, nil, nil, nil, nil)
	h.SetDepsOptional(true)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/readyz", nil)
	h.serveReadyz(rec, req)

	if rec.Code != 200 {
		t.Fatalf("lite /readyz should be 200 with nil db and no redis, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Status   string          `json:"status"`
		Mode     string          `json:"mode"`
		Database *ResourceStatus `json:"database"`
		Redis    *ResourceStatus `json:"redis"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode readyz body: %v", err)
	}
	if resp.Status != "ready" || resp.Mode != "lite" {
		t.Fatalf("unexpected readyz payload: %+v", resp)
	}
	if resp.Database == nil || !resp.Database.Connected || resp.Database.Mode != "local" {
		t.Fatalf("database status should advertise local storage: %+v", resp.Database)
	}
	if resp.Redis == nil || !resp.Redis.Connected || resp.Redis.Mode != "not_required" {
		t.Fatalf("redis status should advertise not_required when unset: %+v", resp.Redis)
	}
}

func TestReadyzLiteModeStillFailsOnDownRedis(t *testing.T) {
	h := NewHealthHandler(nil, nil, nil, nil, errConnector{})
	h.SetDepsOptional(true)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/readyz", nil)
	h.serveReadyz(rec, req)

	if rec.Code != 503 {
		t.Fatalf("lite /readyz must stay fail-closed on an unreachable configured redis, got %d", rec.Code)
	}
}

func TestDependenciesReadyLiteModeIgnoresNilDB(t *testing.T) {
	h := NewHealthHandler(nil, nil, nil, nil, nil)
	h.SetDepsOptional(true)
	req := httptest.NewRequest("GET", "/healthz", nil)
	if !h.dependenciesReady(req) {
		t.Fatal("lite mode: nil db + nil redis should be ready")
	}

	// Full mode unchanged: nil connectors stay not-ready.
	full := NewHealthHandler(nil, nil, nil, nil, nil)
	if full.dependenciesReady(req) {
		t.Fatal("full mode: nil connectors must never be ready")
	}
}

func TestDependenciesReadyLiteModePingsConfiguredRedis(t *testing.T) {
	h := NewHealthHandler(nil, nil, nil, nil, errConnector{})
	h.SetDepsOptional(true)
	req := httptest.NewRequest("GET", "/healthz", nil)
	if h.dependenciesReady(req) {
		t.Fatal("lite mode: a configured-but-down redis must block readiness")
	}
}

type errConnector struct{}

func (errConnector) Ping(context.Context) error { return errors.New("down") }
