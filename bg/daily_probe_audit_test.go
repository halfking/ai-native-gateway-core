package bg

import (
	"strings"
	"testing"
)

// TestDailyProbeAuditSQLFiltersMissingBindingPairs pins the P3 audit fix
// (handoff 2026-09-11-selfcheck-necessity-gate, 审计记录第五轮): the daily
// audit is a recurring (24h) node_probe scheduler, and its
// candidate_failure_logs branch is binding-blind — historical
// (credential_id, raw_model_name) pairs keep being submitted for the whole
// 72h lookback window even after their binding chain is broken (binding
// removed / provider_model_id dangling / pm.raw_model_name renamed). Each
// submission is a doomed run: queue claim → in-flight tile → endpoint-build
// "no rows" → missing-binding drop + a fake-success audit row. The
// request_logs branch is binding-anchored by its own JOINs, so ONE outer
// binding-chain EXISTS (same predicate shape as pumpDueStatesSQL's) covers
// the UNION without changing branch-1 semantics.
func TestDailyProbeAuditSQLFiltersMissingBindingPairs(t *testing.T) {
	sql := dailyProbeAuditSQL()
	// The filter must reference BOTH pair columns (drift guard against a
	// credential-only or model-only filter) and must JOIN provider_models so
	// a dangling cmb.provider_model_id is filtered too — the exact structural
	// shapes resolveDirectTarget's "no rows" covers.
	for _, want := range []string{
		"FROM credential_model_bindings cmb",
		"JOIN provider_models pm ON pm.id = cmb.provider_model_id",
		"cmb.credential_id = recent.id",
		"pm.raw_model_name = recent.raw_model_name",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("daily audit SQL missing binding-chain filter fragment %q:\n%s", want, sql)
		}
	}
	// The filter must sit in the outer WHERE (before the final ORDER BY) so
	// binding-less pairs never consume a submission slot. The correlation
	// anchor `cmb.credential_id = recent.id` is unique to the outer EXISTS.
	// Anchor on the OUTER ORDER BY ("ORDER BY recent.id") — the CTEs above
	// legitimately carry their own ORDER BY (row_number / DISTINCT ON).
	filterPos := strings.Index(sql, "cmb.credential_id = recent.id")
	orderByPos := strings.Index(sql, "ORDER BY recent.id")
	if filterPos < 0 || orderByPos < 0 || filterPos > orderByPos {
		t.Fatalf("binding-chain filter must be applied before the outer ORDER BY (filter=%d order=%d)", filterPos, orderByPos)
	}
}

// TestDailyProbeAuditSQLErrorGated pins the 2026-09-20 probe-volume policy
// (docs/probe/2026-09-20-probe-volume-optimization.md): the daily audit is an
// error-gated verification scan, not a fleet census.
func TestDailyProbeAuditSQLErrorGated(t *testing.T) {
	sql := dailyProbeAuditSQL()
	for _, want := range []string{
		// INV-3: probe traffic must not count as usage (self-sustaining loop).
		"NOT COALESCE('probe' = ANY(rl.quality_flags), FALSE)",
		// INV-3: 3-day usage scope on the request_logs branch.
		probeUsageWindowInterval,
		// INV-5: usage branch only for credentials with failure evidence.
		credentialFailureEvidenceSQL("c.id", probeUsageWindowInterval),
		// INV-4: two-recent-probe-success gate on BOTH branches.
		credentialTwoProbeSuccessGateSQL("u.id"),
		credentialTwoProbeSuccessGateSQL("f.id"),
		// per-credential usage cap (2 most recently used models).
		"rn <= 2",
		// bounded run.
		"LIMIT $1",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("daily audit SQL missing policy fragment %q:\n%s", want, sql)
		}
	}
	if dailyProbeAuditBatch != 100 {
		t.Fatalf("daily audit batch = %d, want 100 (bounded run)", dailyProbeAuditBatch)
	}
	if dailyProbeUsagePerCredentialCap != 2 {
		t.Fatalf("usage per-credential cap = %d, want 2", dailyProbeUsagePerCredentialCap)
	}
}
