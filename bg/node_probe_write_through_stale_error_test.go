package bg

import (
	"strings"
	"testing"
)

// 2026-10-16 regression: node status inconsistency reported alongside the
// grok-4.6 routing outage — a credential displayed 可用 / healthy while still
// carrying an old failure message (on 211: credential 63 showed
// health_status=healthy next to "[auth] upstream 403: No permission to access
// model: deepseek-v4-flash").
//
// Cause: healthyCredentialSQL clears health_error / state_reason_code /
// state_reason_detail, but its "nothing needs to change" no-op guard listed
// only the five state columns. A row whose ONLY dirt was a stale error string
// therefore matched no OR branch and the whole UPDATE was skipped — the stale
// text then survived every subsequent successful request, because the guard
// kept skipping. Measured on 211: 18 credentials were stuck in that shape.

// TestHealthyCredentialSQLGuardCoversEveryClearedColumn is the invariant test:
// every column the SET clause resets to a "clean" value must also appear in
// the WHERE guard, otherwise clearing it depends on some OTHER column happening
// to be dirty too.
func TestHealthyCredentialSQLGuardCoversEveryClearedColumn(t *testing.T) {
	sql := healthyCredentialSQL()

	// Columns the SET clause resets; a stale value in any of them is dirt.
	cleared := []string{
		"health_error = NULL",
		"state_reason_code = NULL",
		"state_reason_detail = NULL",
	}
	// Guard predicates that make a row eligible for the UPDATE.
	guards := []string{
		"health_error IS NOT NULL",
		"state_reason_code IS NOT NULL",
		"state_reason_detail IS NOT NULL",
	}
	for _, c := range cleared {
		if !strings.Contains(sql, c) {
			t.Fatalf("healthyCredentialSQL must clear %q (SET clause)", c)
		}
	}
	for _, g := range guards {
		if !strings.Contains(sql, g) {
			t.Fatalf("healthyCredentialSQL guard is missing %q — a row whose only dirt is a stale error is skipped entirely and the message survives forever", g)
		}
	}
}

// TestHealthyCredentialSQLStillNoOpWhenFullyClean is the negative control for
// over-firing. The executor runs this on EVERY successful request, so a fully
// clean row must still match zero OR branches. Each predicate above must be
// false for a clean row — i.e. it must be an IS NOT NULL / IS DISTINCT FROM
// test, never a bare "= NULL" or a negation that flips for NULL rows.
func TestHealthyCredentialSQLStillNoOpWhenFullyClean(t *testing.T) {
	sql := healthyCredentialSQL()

	// A clean row has health_error/state_reason_* = NULL, so every added
	// predicate must be false. Assert each is written as IS NOT NULL, which is
	// exactly false when the column is NULL.
	for _, p := range []string{
		"OR health_error IS NOT NULL",
		"OR state_reason_code IS NOT NULL",
		"OR state_reason_detail IS NOT NULL",
	} {
		if !strings.Contains(sql, p) {
			t.Fatalf("expected guard term %q; without the OR-prefixed IS NOT NULL form a clean row could be rewritten on every success", p)
		}
	}

	// Forbidden shapes: these would make a CLEAN row match the guard.
	for _, bad := range []string{
		"OR health_error = NULL",
		"OR health_error IS NULL",
		"OR state_reason_detail IS NULL",
		"OR state_reason_code IS NULL",
	} {
		if strings.Contains(sql, bad) {
			t.Fatalf("healthyCredentialSQL contains %q — a fully healthy row would match and every successful request would rewrite it", bad)
		}
	}
}

// TestHealthyCredentialSQLKeepsBalanceFloorExemption verifies the new guard
// terms did not defeat the 2026-09-13 balance_floor exemption. That exemption
// lives in its own AND clause, so a row with state_reason_code='balance_floor'
// must still be excluded even though it is otherwise a candidate for cleanup.
func TestHealthyCredentialSQLKeepsBalanceFloorExemption(t *testing.T) {
	sql := healthyCredentialSQL()
	if !strings.Contains(sql, "COALESCE(state_reason_code, '') <> 'balance_floor'") {
		t.Fatal("healthyCredentialSQL must keep excluding balance_floor-pulled rows")
	}
	// The exemption must be an AND-clause gate, not folded into the OR list —
	// folding it in would let a balance_floor row through whenever another
	// column is dirty.
	if strings.Contains(sql, "OR state_reason_code = 'balance_floor'") {
		t.Fatal("balance_floor exemption must not appear as an OR branch of the update guard")
	}
}
