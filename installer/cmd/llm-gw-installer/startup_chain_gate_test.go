//go:build integration

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/installer/internal/dbinit"
)

// knownStartupChainGaps lists the registered startup migrations that are known
// not to apply on a fresh install, each with the relation (or column) that has
// no creator in the fresh-install chain.
//
// R43 (2026-09-30) context. The previous round closed the 568 blocker by
// registering 541, its sole creator. Doing that surfaced a second failure at
// 622, then a third — the chain was being debugged one migration at a time.
// This allowlist is the replacement for that loop: one run reports every
// migration that fails, and the test fails when that set changes in either
// direction.
//
//   - A migration that fails and is NOT listed here is a new regression.
//   - A listed migration that now applies has been fixed; delete its entry so
//     the list keeps shrinking and cannot quietly go stale.
//
// This test deliberately does not assert a green install. Asserting that would
// require registering the ~150 unregistered canonical migrations, which changes
// the object set a fresh install produces and is an architecture decision, not
// a mechanical fix. Until that decision is made, the honest invariant is
// "the set of broken migrations is exactly the reviewed set".
var knownStartupChainGaps = map[string]string{
	"622_provider_error_aggregator_state.sql":               `relation "public.candidate_failure_logs_hot" does not exist (creator 392 unregistered)`,
	"624_candidate_failure_logs_promote_atomic_v2.sql":      `relation "public.candidate_failure_logs_hot" does not exist (creator 392 unregistered)`,
	"627_candidate_failure_logs_aggregation_id_unified.sql": `relation "public.candidate_failure_logs_hot" does not exist (creator 392 unregistered)`,
	"682_model_offers_context_window_columns.sql":           `column cmb.context_window_source does not exist`,
	"683_session_dim_ownership_columns.sql":                 `relation "session_dim" does not exist (creator 350 unregistered)`,
	"686_fix_session_module_executions_2026_10_bounds.sql":  `relation "public.session_module_executions_2026_10" does not exist`,
	"690_session_summaries_archived_ttl_index.sql":          `column "archived_at" does not exist`,
	"696_request_logs_view_system_fingerprint.sql":          `relation "public.request_logs_with_current_month_without_request_class_due_at" does not exist`,
	"700_request_logs_view_raw_model_name.sql":              `relation "public.request_logs_with_current_month_without_request_class_due_at" does not exist`,
	"707_session_turns_s1a.sql":                             `relation "public.session_turns_hot" does not exist (creator 526 unregistered)`,
	"708_session_bodies_s1a.sql":                            `UPDATE and CTID scans not supported for ColumnarScan`,
	"713_session_turns_cost_precision.sql":                  `relation "public.session_turns_hot" does not exist (creator 526 unregistered)`,
	"733_session_turn_details.sql":                          `column "request_type" does not exist`,
	"734_request_logs_view_details_join.sql":                `migration 734 requires session_turns family (run 430/513/526 first)`,
	"738_view_chain_credits_rate_multiplier.sql":            `request_logs_with_current_month column count = 0 (expected 114)`,
	"740_view_chain_client_ip.sql":                          `request_logs_with_current_month_without_customer_id column count = 0 (expected 110)`,
	"757_session_turns_origin_actor_projection.sql":         `view public.session_turns_with_current_month not found`,
	"762_session_project_backfill_chain.sql":                `relation "public.session_dim" does not exist (creator 350 unregistered)`,
}

// TestStartupChainGapsMatchReviewedSet applies the whole fresh-install chain to
// a disposable database and compares the set of migrations that fail against
// knownStartupChainGaps. Each migration runs in its own transaction so one
// failure does not hide the migrations behind it.
func TestStartupChainGapsMatchReviewedSet(t *testing.T) {
	dsn := os.Getenv(installerFreshDBURL)
	if dsn == "" {
		t.Skipf("%s not set; skipping startup chain gate", installerFreshDBURL)
	}

	sqlDir, cleanup, err := setupSQLDir()
	if err != nil {
		t.Fatalf("set up installer SQL directory: %v", err)
	}
	defer cleanup()

	for _, name := range []string{"00-prereqs.sql", "01-schema.sql", "02-seed.sql"} {
		applyInstallerSQL(t, dsn, filepath.Join(sqlDir, name))
	}

	runner := dbinit.NewRunner("", "", "", sqlDir)
	observed := map[string]string{}
	for _, name := range runner.StartupFiles {
		path := filepath.Join(sqlDir, "startup", name)
		if reason, fails := applyInstallerSQLTolerant(t, dsn, path); fails {
			observed[name] = reason
		}
	}

	var unexpected, fixed []string
	for name, reason := range observed {
		if _, known := knownStartupChainGaps[name]; !known {
			unexpected = append(unexpected, name+"  ->  "+reason)
		}
	}
	for name := range knownStartupChainGaps {
		if _, stillBroken := observed[name]; !stillBroken {
			fixed = append(fixed, name)
		}
	}
	sort.Strings(unexpected)
	sort.Strings(fixed)

	for _, u := range unexpected {
		t.Errorf("startup chain gap not in knownStartupChainGaps (new breakage): %s\n"+
			"    add it with the missing relation and its creator, or fix the chain", u)
	}
	for _, f := range fixed {
		t.Errorf("knownStartupChainGaps entry %q now applies — the chain improved.\n"+
			"    Delete the entry so the reviewed set keeps shrinking", f)
	}
	t.Logf("startup chain: %d of %d registered migrations fail, %d expected",
		len(observed), len(runner.StartupFiles), len(knownStartupChainGaps))
}

// applyInstallerSQLTolerant runs one migration in its own transaction and
// reports the first ERROR line instead of failing the test. A migration that
// aborts leaves nothing behind, so the following migrations see the same
// catalog a real fresh install would give them.
func applyInstallerSQLTolerant(t *testing.T, dsn, path string) (reason string, failed bool) {
	t.Helper()
	out, code := runInstallerPSQL(t, dsn, path)
	if code == "0" {
		return "", false
	}
	for _, line := range strings.Split(out, "\n") {
		l := strings.TrimSpace(line)
		if strings.Contains(l, "ERROR:") {
			return l, true
		}
	}
	return "exit " + code, true
}

// runInstallerPSQL mirrors applyInstaller's real invocation (stdin-free -f,
// ON_ERROR_STOP=1, single transaction) and returns the merged output plus the
// process exit code as a string. Merged output is deliberate here: this is a
// gate that wants to *read* failures, and unlike psqlScalar it never parses a
// scalar out of the output, so citus's background-worker warnings on stderr
// cannot be mistaken for a value.
func runInstallerPSQL(t *testing.T, dsn, path string) (string, string) {
	t.Helper()
	cmd := exec.Command("psql", "-v", "ON_ERROR_STOP=1", "--single-transaction", "--dbname", dsn, "-f", path)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), "0"
	}
	var ee *exec.ExitError
	code := "unknown"
	if errors.As(err, &ee) {
		code = strconv.Itoa(ee.ExitCode())
	} else {
		code = err.Error()
	}
	return string(out), code
}
