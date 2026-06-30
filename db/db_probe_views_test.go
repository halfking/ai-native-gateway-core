package db

import (
	"context"
	"testing"
)

// PR-8 (2026-06-30): probeViewAdvisoryLockID must be a stable int64 so
// all pods in a fleet race on the same lock. Pinned to 0x50524F42
// ("PROB" ASCII) — changing it would silently split the lock across
// values, reintroducing the DROP/CREATE race (audit P0-11).
func TestProbeViewAdvisoryLockID_StableValue(t *testing.T) {
	if probeViewAdvisoryLockID != 0x50524F42 {
		t.Errorf("probeViewAdvisoryLockID = 0x%X, want 0x50524F42", probeViewAdvisoryLockID)
	}
}

// PR-8: ensureProbeHealthDashboardViews must not panic on nil receiver
// or nil pool — the caller in db.Open does not nil-check before calling.
func TestEnsureProbeHealthDashboardViews_NilSafety(t *testing.T) {
	// nil receiver
	var d *DB
	d.ensureProbeHealthDashboardViews(context.Background()) // must not panic

	// non-nil receiver but nil pool
	d = &DB{}
	d.ensureProbeHealthDashboardViews(context.Background()) // must not panic
}
