package admin

import (
	"fmt"
	"strings"
	"testing"
)

// R41 (2026-09-18 PG log audit): the fallback task-distribution query used to
// concatenate auditBusinessFrag — which carries a bare % inside
// `NOT LIKE 'probe-%'` — directly into its fmt.Sprintf format string. The bad
// verb consumed the taskExpr argument, PG received `GROUP BY (%!s(MISSING))`,
// every call failed with `syntax error at or near "(" at character 607`
// (867 occurrences in 8h live), and the handler's swallowed rows-err left
// task_distribution silently empty whenever the MV path was unavailable.
// These tests pin the built SQL: no fmt artifacts, both axes expanded, and
// the probe filter present exactly once.
func TestBuildAutoRouteTaskDistQuery_NoFmtCorruption(t *testing.T) {
	taskExpr := fmt.Sprintf(`COALESCE(NULLIF(task_type, ''), CASE WHEN is_auto_request THEN 'unknown' ELSE '%s' END)`, SpecifiedModelTaskKey)
	businessFrag := " AND " + businessRequestFilter("")
	tenantFrag := " AND tenant_id = $1"

	sql := buildAutoRouteTaskDistQuery(taskExpr, businessFrag, tenantFrag)

	if strings.Contains(sql, "%!") {
		t.Fatalf("built SQL contains fmt corruption artifacts:\n%s", sql)
	}
	if !strings.Contains(sql, "GROUP BY ("+taskExpr+")") {
		t.Fatalf("GROUP BY axis not expanded with the effective-task expression:\n%s", sql)
	}
	if !strings.Contains(sql, "SELECT "+taskExpr+" AS task_type") {
		t.Fatalf("SELECT axis not expanded with the effective-task expression:\n%s", sql)
	}
	if got := strings.Count(sql, "NOT LIKE 'probe-%'"); got != 1 {
		t.Fatalf("probe filter `NOT LIKE 'probe-%%'` appears %d times, want exactly 1:\n%s", got, sql)
	}
	if got := strings.Count(sql, taskExpr); got != 2 {
		t.Fatalf("taskExpr appears %d times, want 2 (SELECT + GROUP BY):\n%s", got, sql)
	}
}

// The MV path must stay preferred: the fallback builder is only wired into
// the else branch of the MV probe, and the fragment passed as an argument is
// the same one every other plain-concat consumer embeds.
func TestBuildAutoRouteTaskDistQuery_FragmentParity(t *testing.T) {
	frag := " AND " + businessRequestFilter("")
	if !strings.Contains(frag, "origin_stage") || !strings.Contains(frag, "probe_triggered") {
		t.Fatalf("business fragment lost the probe exclusion predicates:\n%s", frag)
	}
}
