package startup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Migration 715 (bba08b922 incident fix, second half) extends
// route_incidents.state with 'pending' so DecideState's sub-threshold
// incidents can be written at all. This file pins the SQL contract statically;
// the DB-backed up/down matrix runs only when TEST_PG_URL points at a
// disposable database (same discipline as migration 711).
//
// The wiring half is load-bearing: 715 shipped without its Go ensure mirror,
// so the startup chain kept enforcing the old CHECK and the first 'pending'
// write died with 23514 (observer retries, gives up, incident tracking goes
// silently dark). The wiring test below fails if anyone repeats that.
func TestMigration715PendingStateContract(t *testing.T) {
	up, err := os.ReadFile("715_route_incidents_pending_state.sql")
	if err != nil {
		t.Fatalf("read migration failed: %v", err)
	}
	down, err := os.ReadFile("715_route_incidents_pending_state.down.sql")
	if err != nil {
		t.Fatalf("read down migration failed: %v", err)
	}
	for name, sql := range map[string]string{"up": string(up), "down": string(down)} {
		if !strings.Contains(string(sql), "BEGIN;") || !strings.Contains(string(sql), "COMMIT;") {
			t.Errorf("715 %s migration must be transactional", name)
		}
	}

	upText := string(up)

	// All four lifecycle states in the CHECK, in both migrations.
	for _, state := range []string{"pending", "active", "recovering", "recovered"} {
		if !strings.Contains(upText, "'"+state+"'") {
			t.Errorf("715 up state CHECK missing %q", state)
		}
	}
	downText := string(down)
	for _, state := range []string{"active", "recovering", "recovered"} {
		if !strings.Contains(downText, "'"+state+"'") {
			t.Errorf("715 down state CHECK missing %q", state)
		}
	}

	// The partial unique index must admit 'pending' on up and drop it on
	// down; both directions must keep the exact 389 column list so the
	// conflict target for concurrent incident upserts is unchanged.
	const indexColumns = "tenant_id, endpoint_protocol, model, COALESCE(provider_id, 0), COALESCE(credential_id, 0)"
	if !strings.Contains(upText, indexColumns) {
		t.Error("715 up must recreate uq_route_incidents_active_route with the 389 column list")
	}
	if !strings.Contains(upText, "WHERE state IN ('pending', 'active', 'recovering')") {
		t.Error("715 up index WHERE must include 'pending'")
	}
	if !strings.Contains(downText, "WHERE state IN ('active', 'recovering')") {
		t.Error("715 down index WHERE must exclude 'pending'")
	}

	// Down migration must warn about pending rows: the restored CHECK
	// rejects them, so a blind down-apply fails mid-transaction.
	if !strings.Contains(strings.ToLower(downText), "pending") ||
		!strings.Contains(strings.ToLower(downText), "fail") {
		t.Error("715 down must document the pending-row hazard")
	}
}

// TestMigration715StartupWiring pins the Go-side ensure mirror: without it
// the startup chain never relaxes the old CHECK and every 'pending' write
// fails with 23514 (the exact gap found during 09-16 deploy verification).
func TestMigration715StartupWiring(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "db.go"))
	if err != nil {
		t.Fatalf("read db.go failed: %v", err)
	}
	text := string(src)

	if !strings.Contains(text, "db.ensureRouteIncidentPendingState(migCtx)") {
		t.Error("applyMigrationsOnce must call ensureRouteIncidentPendingState (715 startup mirror)")
	}
	if !strings.Contains(text, "func (d *DB) ensureRouteIncidentPendingState(") {
		t.Error("ensureRouteIncidentPendingState must be defined on *DB")
	}

	// Locate the ensure body and pin both DDL halves inside it.
	const fn = "func (d *DB) ensureRouteIncidentPendingState("
	start := strings.Index(text, fn)
	if start < 0 {
		t.Fatal("ensure function body not found")
	}
	body := text[start:]
	if end := strings.Index(body, "\nfunc "); end >= 0 {
		body = body[:end]
	}
	for _, want := range []string{
		"DROP CONSTRAINT IF EXISTS route_incidents_state_check",
		"CHECK (state IN ('pending', 'active', 'recovering', 'recovered'))",
		"DROP INDEX IF EXISTS uq_route_incidents_active_route",
		"WHERE state IN ('pending', 'active', 'recovering')",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("ensureRouteIncidentPendingState body missing %q", want)
		}
	}

	// The mirror must run after the base route_incident schema so the
	// fresh-install path upgrades the inline 389 CHECK in the same boot.
	base := strings.Index(text, "db.ensureRouteIncidentSchema(migCtx)")
	pend := strings.Index(text, "db.ensureRouteIncidentPendingState(migCtx)")
	if base < 0 || pend < 0 || pend < base {
		t.Error("ensureRouteIncidentPendingState must be wired after ensureRouteIncidentSchema")
	}
}
