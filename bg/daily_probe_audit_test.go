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
		"cmb.credential_id = recent.credential_id",
		"pm.raw_model_name = recent.raw_model_name",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("daily audit SQL missing binding-chain filter fragment %q:\n%s", want, sql)
		}
	}
	// The filter must sit in the outer WHERE (before ORDER BY) so binding-less
	// pairs never consume a submission slot. The correlation anchor
	// `cmb.credential_id = recent.credential_id` is unique to the outer EXISTS
	// (the request_logs branch correlates `cmb.credential_id = rl.credential_id`).
	filterPos := strings.Index(sql, "cmb.credential_id = recent.credential_id")
	orderByPos := strings.Index(sql, "ORDER BY")
	if filterPos < 0 || orderByPos < 0 || filterPos > orderByPos {
		t.Fatalf("binding-chain filter must be applied before ORDER BY (filter=%d order=%d)", filterPos, orderByPos)
	}
}
