package startup

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/autoroute"
)

// Migration 730 (R48, 2026-09-20) lands the session role hierarchy: the
// sessions.agent_role/parent_* columns, the role_task_llm_mapping 2-D routing
// table (agent_role × task_kind → LLM), and the seed rows behind the
// override_pin > role_route > tier policy decision order.
//
// The contract pins five invariants (726 template):
//
//	C1  sessions.agent_role CHECK values == autoroute.AllAgentRoles, bound
//	    at compile time (import), not by copy-paste.
//	C2  role_task_llm_mapping role/kind CHECK values == the Go enums
//	    (AllAgentRoles / AllTaskKinds). A CHECK the Go writer can violate is
//	    a production 23514 on the first out-of-band value.
//	C3  The mapping UNIQUE constraint stays NULLS NOT DISTINCT on
//	    (tenant_id, agent_role, task_kind, priority) — with PG's default
//	    NULLS DISTINCT the platform-level NULL-tenant seed rows never hit
//	    ON CONFLICT and every replay doubles the seed set (252: 48→96).
//	    The Go lookup reader (autoroute/role_llm_router.go) stays bound to
//	    the same table name.
//	C4  The two partial indexes keep their WHERE gates (parent_session_id
//	    IS NOT NULL / agent_role <> 'main') — they exist for sub-agent ops
//	    queries; ungated they index every session row for no reader.
//	C5  Installer embeddata copies stay byte-identical (730 completed the
//	    five-point sync in R48; unlike the 727-729 CONCURRENTLY family this
//	    file is transaction-safe).
func TestMigration730SessionRoleHierarchyContract(t *testing.T) {
	upBytes, err := os.ReadFile("730_session_role_hierarchy.sql")
	if err != nil {
		t.Fatal(err)
	}
	downBytes, err := os.ReadFile("730_session_role_hierarchy.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	down := string(downBytes)

	// ── C1: sessions.agent_role CHECK == Go 枚举 ───────────────────────
	roleValues := checkInValues(up, `(?s)agent_role\s+TEXT.*?CHECK\s*\(agent_role\s+IN\s*\(([^)]*)\)`)
	if roleValues == nil {
		t.Fatal("sessions.agent_role CHECK constraint not found in 730")
	}
	if !sameStrings(roleValues, agentRoleStrings()) {
		t.Errorf("sessions.agent_role CHECK %v != autoroute.AllAgentRoles %v — sync all three sites (730 CHECK / Go enum / mapping CHECK)", roleValues, agentRoleStrings())
	}

	// ── C2: mapping role/kind CHECK == Go 枚举 ─────────────────────────
	mappingRoleValues := checkInValues(up, `CONSTRAINT\s+role_task_llm_mapping_role_check\s+CHECK\s*\(agent_role\s+IN\s*\(([^)]*)\)`)
	if mappingRoleValues == nil {
		t.Fatal("role_task_llm_mapping_role_check not found in 730")
	}
	if !sameStrings(mappingRoleValues, agentRoleStrings()) {
		t.Errorf("mapping role_check %v != autoroute.AllAgentRoles %v", mappingRoleValues, agentRoleStrings())
	}
	mappingKindValues := checkInValues(up, `CONSTRAINT\s+role_task_llm_mapping_kind_check\s+CHECK\s*\(task_kind\s+IN\s*\(([^)]*)\)`)
	if mappingKindValues == nil {
		t.Fatal("role_task_llm_mapping_kind_check not found in 730")
	}
	if !sameStrings(mappingKindValues, taskKindStrings()) {
		t.Errorf("mapping kind_check %v != autoroute.AllTaskKinds %v", mappingKindValues, taskKindStrings())
	}

	// ── C3: NULLS NOT DISTINCT 唯一键 + Go 读侧绑定 ────────────────────
	upCompact := normalizeSQL(up)
	if !strings.Contains(upCompact, "UNIQUE NULLS NOT DISTINCT (TENANT_ID, AGENT_ROLE, TASK_KIND, PRIORITY)") {
		t.Error("role_task_llm_mapping UNIQUE must stay NULLS NOT DISTINCT on (tenant_id, agent_role, task_kind, priority) — default NULLS DISTINCT doubles seed rows on every replay (252: 48→96)")
	}
	routerSrc, err := os.ReadFile(filepath.Join("..", "..", "..", "autoroute", "role_llm_router.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(normalizeGoSQL(string(routerSrc)), "FROM ROLE_TASK_LLM_MAPPING") {
		t.Error("autoroute/role_llm_router.go no longer reads role_task_llm_mapping — re-audit migration 730 against the new reader")
	}

	// ── C4: 两个部分索引的 WHERE 门 ────────────────────────────────────
	if !strings.Contains(upCompact, "WHERE PARENT_SESSION_ID IS NOT NULL") {
		t.Error("idx_sessions_parent_session must stay partial (WHERE parent_session_id IS NOT NULL)")
	}
	if !strings.Contains(upCompact, "WHERE AGENT_ROLE <> 'MAIN'") {
		t.Error("idx_sessions_agent_role must stay partial (WHERE agent_role <> 'main' — main sessions are the bulk of rows)")
	}

	// ── down: 语义受限回滚 ─────────────────────────────────────────────
	downCompact := normalizeSQL(down)
	if !strings.Contains(downCompact, "DROP TABLE IF EXISTS PUBLIC.ROLE_TASK_LLM_MAPPING CASCADE") {
		t.Error("730 down must drop role_task_llm_mapping")
	}
	for _, col := range []string{"PARENT_TASK_ID", "PARENT_SESSION_ID", "AGENT_ROLE"} {
		if !strings.Contains(downCompact, "DROP COLUMN IF EXISTS "+col) {
			t.Errorf("730 down must drop sessions.%s", col)
		}
	}

	// ── C5: installer embeddata byte-identical ─────────────────────────
	embedRoot := filepath.Join("..", "..", "..", "installer", "cmd", "llm-gw-installer", "embeddata", "startup")
	for _, pair := range []struct{ orig, embed string }{
		{"730_session_role_hierarchy.sql", filepath.Join(embedRoot, "730_session_role_hierarchy.sql")},
		{"730_session_role_hierarchy.down.sql", filepath.Join(embedRoot, "730_session_role_hierarchy.down.sql")},
	} {
		embedBytes, err := os.ReadFile(pair.embed)
		if err != nil {
			t.Fatalf("embeddata copy missing (%s): %v", pair.embed, err)
		}
		origBytes, err := os.ReadFile(pair.orig)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(origBytes, embedBytes) {
			t.Errorf("embeddata copy drifted from original: %s", pair.embed)
		}
	}
}

// checkInValues extracts the quoted values from a CHECK (... IN (...)) clause
// matched by pattern against the raw migration text.
func checkInValues(sqlText, pattern string) []string {
	re := regexp.MustCompile(`(?i)` + pattern)
	m := re.FindStringSubmatch(sqlText)
	if m == nil {
		return nil
	}
	raw := strings.Split(m[1], ",")
	out := []string{}
	for _, v := range raw {
		v = strings.TrimSpace(v)
		v = strings.Trim(v, "'")
		if v != "" {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func agentRoleStrings() []string {
	out := make([]string, 0, len(autoroute.AllAgentRoles))
	for _, r := range autoroute.AllAgentRoles {
		out = append(out, string(r))
	}
	sort.Strings(out)
	return out
}

func taskKindStrings() []string {
	out := make([]string, 0, len(autoroute.AllTaskKinds))
	for _, k := range autoroute.AllTaskKinds {
		out = append(out, string(k))
	}
	sort.Strings(out)
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
