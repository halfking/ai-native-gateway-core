package db

import (
	"os"
	"strings"
	"testing"
)

// TestProbeMarkFunctions_AdminProtectedGuard pins the "manual records are
// only manually deletable" contract for the four probe state functions
// installed by ensureProbeStateFunctionFixes (db.go):
//
//	model_probe_mark_available
//	model_probe_mark_unavailable
//	unified_probe_mark_healthy
//	unified_probe_mark_failing
//
// Each must guard its credential_model_bindings UPDATE with the
// admin_protected skip so automated probes never mutate manually-added
// bindings. The SQL is embedded inline in db.go, so this test reads the
// source and counts the guard per function body (struct contract, no DB).
func TestProbeMarkFunctions_AdminProtectedGuard(t *testing.T) {
	src, err := os.ReadFile("db.go")
	if err != nil {
		t.Skipf("cannot read db.go: %v", err)
	}
	s := string(src)

	funcNames := []string{
		"model_probe_mark_available",
		"model_probe_mark_unavailable",
		"unified_probe_mark_healthy",
		"unified_probe_mark_failing",
	}

	// The guard line must exist inside each function's plpgsql body.
	// Locate each CREATE OR REPLACE FUNCTION block and assert the guard
	// appears within that block.
	for _, name := range funcNames {
		start := strings.Index(s, "CREATE OR REPLACE FUNCTION "+name+"(")
		if start < 0 {
			t.Errorf("db.go: expected CREATE OR REPLACE FUNCTION %s not found", name)
			continue
		}
		end := strings.Index(s[start:], "$$;")
		if end < 0 {
			t.Errorf("db.go: function %s body has no terminating $$;", name)
			continue
		}
		body := s[start : start+end]
		if !strings.Contains(body, "AND COALESCE(cmb.admin_protected, FALSE) = FALSE;") {
			t.Errorf("db.go: function %s UPDATE is missing the admin_protected guard", name)
		}
	}
}

// TestProbeMarkFunctions_GuardCount sanity-checks there is exactly one
// admin_protected guard per probe function across the whole embedded SQL
// block (no accidental guard drift/duplication).
func TestProbeMarkFunctions_GuardCount(t *testing.T) {
	src, err := os.ReadFile("db.go")
	if err != nil {
		t.Skipf("cannot read db.go: %v", err)
	}
	s := string(src)

	start := strings.Index(s, "func (d *DB) ensureProbeStateFunctionFixes")
	if start < 0 {
		t.Fatal("ensureProbeStateFunctionFixes not found in db.go")
	}
	end := strings.Index(s[start:], "func (d *DB) ensureTenantModelPoliciesSchema")
	if end < 0 {
		t.Fatal("ensureTenantModelPoliciesSchema not found in db.go (cannot bound probe function block)")
	}
	block := s[start : start+end]

	want := 4 // one guard per probe function UPDATE
	got := strings.Count(block, "AND COALESCE(cmb.admin_protected, FALSE) = FALSE;")
	if got != want {
		t.Errorf("ensureProbeStateFunctionFixes block has %d admin_protected guards, want %d", got, want)
	}
}
