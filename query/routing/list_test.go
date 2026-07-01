package routing

import (
	"context"
	"strings"
	"testing"
)

// ── buildListSQL tests (pure, no DB) ─────────────────────────

func TestBuildListSQL_NoFilters(t *testing.T) {
	sql, args := buildListSQL(ListRoutesQuery{})
	if len(args) != 0 {
		t.Fatalf("expected no args, got %d: %v", len(args), args)
	}
	if !strings.Contains(sql, "FROM routing_overrides WHERE 1=1") {
		t.Fatalf("expected base FROM clause, got: %s", sql)
	}
	if strings.Contains(sql, "AND (expires_at IS NULL OR expires_at > NOW())") {
		t.Fatalf("active-only filter should be absent when ActiveOnly=false, got: %s", sql)
	}
	if !strings.HasSuffix(sql, "ORDER BY task_type, profile, mode, model_chosen") {
		t.Fatalf("expected stable ORDER BY suffix, got: %s", sql)
	}
}

func TestBuildListSQL_ActiveOnly(t *testing.T) {
	sql, args := buildListSQL(ListRoutesQuery{ActiveOnly: true})
	if len(args) != 0 {
		t.Fatalf("expected no args, got %d", len(args))
	}
	if !strings.Contains(sql, "AND (expires_at IS NULL OR expires_at > NOW())") {
		t.Fatalf("expected active filter, got: %s", sql)
	}
}

func TestBuildListSQL_TaskTypeFilter(t *testing.T) {
	sql, args := buildListSQL(ListRoutesQuery{TaskType: "summarize"})
	if len(args) != 1 || args[0] != "summarize" {
		t.Fatalf("expected one arg 'summarize', got: %v", args)
	}
	if !strings.Contains(sql, "AND task_type = $1") {
		t.Fatalf("expected $1 placeholder for task_type, got: %s", sql)
	}
}

func TestBuildListSQL_ProfileFilter(t *testing.T) {
	sql, args := buildListSQL(ListRoutesQuery{Profile: "cost_first"})
	if len(args) != 1 || args[0] != "cost_first" {
		t.Fatalf("expected one arg 'cost_first', got: %v", args)
	}
	if !strings.Contains(sql, "AND profile = $1") {
		t.Fatalf("expected $1 placeholder for profile, got: %s", sql)
	}
}

func TestBuildListSQL_AllFilters(t *testing.T) {
	sql, args := buildListSQL(ListRoutesQuery{
		ActiveOnly: true,
		TaskType:   "summarize",
		Profile:    "smart",
	})
	if len(args) != 2 {
		t.Fatalf("expected 2 args, got %d: %v", len(args), args)
	}
	if args[0] != "summarize" || args[1] != "smart" {
		t.Fatalf("unexpected arg order, want [summarize, smart], got %v", args)
	}
	if !strings.Contains(sql, "AND (expires_at IS NULL OR expires_at > NOW())") {
		t.Fatal("missing active filter")
	}
	if !strings.Contains(sql, "AND task_type = $1") {
		t.Fatal("missing task_type filter at $1")
	}
	if !strings.Contains(sql, "AND profile = $2") {
		t.Fatal("missing profile filter at $2")
	}
}

// TestBuildListSQL_StableOrdering is a regression guard: the admin UI
// depends on a deterministic ordering for diff rendering, so this is
// part of the public contract, not an implementation detail.
func TestBuildListSQL_StableOrdering(t *testing.T) {
	for _, q := range []ListRoutesQuery{
		{},
		{ActiveOnly: true},
		{TaskType: "x"},
		{Profile: "y"},
		{ActiveOnly: true, TaskType: "x", Profile: "y"},
	} {
		sql, _ := buildListSQL(q)
		if !strings.HasSuffix(sql, "ORDER BY task_type, profile, mode, model_chosen") {
			t.Fatalf("ordering not stable for q=%+v: %s", q, sql)
		}
	}
}

// ── NewHandler guard ──────────────────────────────────────────

func TestNewHandler_NilPoolPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil pool")
		}
	}()
	_ = NewHandler(nil)
}

// ── Handler.List with nil pool (path: panic inside db.Query) ───
//
// We don't try to fake pgxpool here — it's a concrete struct, not an
// interface, so we'd have to introduce a wrapper interface to mock it.
// That's deferred to B2+ when more handlers need the same mock
// surface. For PoC, the unit-testable surface is buildListSQL.

func TestHandler_List_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	t.Skip("integration harness not wired up in PoC; see TEST_DATABASE_URL")
	// Keep a reference to context so the import isn't flagged as unused
	// if someone deletes the test entirely.
	_ = context.Background
}
