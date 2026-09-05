package admin

import (
	"regexp"
	"strings"
	"testing"
)

func TestForceEnableCredentialSQLRestoresAllRoutingGates(t *testing.T) {
	for _, clause := range []string{
		"manual_disabled = false",
		"lifecycle_status = 'active'",
		"availability_state = 'ready'",
		"quota_state = 'ok'",
		"circuit_state = 'closed'",
	} {
		if !strings.Contains(forceEnableCredentialSQL, clause) {
			t.Fatalf("force-enable SQL missing %q", clause)
		}
	}
}

// TestForceEnableCredentialSQL_Structural guards the SQL shape against
// regressions that the literal-grep TestForceEnableCredentialSQLRestoresAllRoutingGates
// cannot catch (a missing UPDATE target, a missing WHERE clause, or a wrong
// placeholder count would all pass the substring check above). The
// force-enable endpoint is super_admin gated and clears every routing-gate
// column, so a malformed UPDATE could either silently no-op (no WHERE) or
// nuke every credential's state (no $1 placeholder) — both are P0-class
// incidents we want a unit test to catch before deploy.
func TestForceEnableCredentialSQL_Structural(t *testing.T) {
	sql := forceEnableCredentialSQL
	lower := strings.ToLower(sql)

	// Must target the credentials table. A typo like "credential" (singular)
	// or "creds" would compile but fail at runtime; the grep check above
	// would also miss it because it doesn't look at the UPDATE target.
	if !strings.Contains(lower, "update credentials") {
		t.Fatalf("force-enable SQL must target the `credentials` table; got:\n%s", sql)
	}

	// Must have a WHERE clause. Without it the UPDATE would mass-reset every
	// row, effectively wiping all per-credential state — the most severe
	// incident the endpoint can produce.
	if !strings.Contains(lower, "where") {
		t.Fatalf("force-enable SQL missing WHERE clause; got:\n%s", sql)
	}

	// WHERE must bind the credential id placeholder. We require the WHERE
	// clause to mention `id = $1` so the UPDATE can only touch the row the
	// caller asked for.
	whereRe := regexp.MustCompile(`(?is)where\s+id\s*=\s*\$1`)
	if !whereRe.MatchString(sql) {
		t.Fatalf("force-enable SQL WHERE must bind `id = $1`; got:\n%s", sql)
	}

	// Placeholder count must match the parameter list the handler passes.
	// The handler calls tx.Exec(ctx, forceEnableCredentialSQL, id, detail)
	// (see routing.go around line 945) so we expect exactly two $-prefixed
	// placeholders. If a future change adds a column referencing a third
	// parameter without updating the handler, this catches it.
	phRe := regexp.MustCompile(`\$\d+`)
	placeholders := phRe.FindAllString(sql, -1)
	if len(placeholders) != 2 {
		t.Fatalf("force-enable SQL must declare exactly 2 placeholders ($1 for id, $2 for detail); found %v in:\n%s",
			placeholders, sql)
	}

	// Sanity: every routing-gate column from the grep test must be SET in
	// the UPDATE, not merely mentioned somewhere else (e.g. a comment).
	// We check for `column = <value>` shape rather than just substring so a
	// stray comment containing "lifecycle_status = 'active'" would not
	// pass.
	gates := map[string]string{
		"manual_disabled":      "false",
		"lifecycle_status":     "'active'",
		"availability_state":   "'ready'",
		"quota_state":          "'ok'",
		"circuit_state":        "'closed'",
		"consecutive_failures": "0",
	}
	for column, value := range gates {
		// Allow any whitespace between column, =, and value; allow optional
		// trailing comma.
		pattern := regexp.MustCompile(`(?im)^\s*` + regexp.QuoteMeta(column) +
			`\s*=\s*` + regexp.QuoteMeta(value) + `\s*,?`)
		if !pattern.MatchString(sql) {
			t.Fatalf("force-enable SQL must SET %s = %s; got:\n%s", column, value, sql)
		}
	}

	// Health / recover_at columns must be cleared. A force-enable that
	// leaves `cooling_until` or `state_reason_detail` populated would leave
	// the credential stuck in a degraded read despite manual override.
	for _, mustReset := range []string{
		"cooling_until = NULL",
		"availability_recover_at = NULL",
		"quota_recover_at = NULL",
		"health_status = 'healthy'",
		"state_reason_code = NULL",
		"state_updated_at = NOW()",
	} {
		if !strings.Contains(sql, mustReset) {
			t.Fatalf("force-enable SQL must reset %s; got:\n%s", mustReset, sql)
		}
	}
}
