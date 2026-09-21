package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// R40 (2026-09-18) census guards for RLS policy vocabulary (RLS design
// docs/design/rls-tenant-isolation-architecture.md §五 Phase 1 item 4):
//
//   - canonical tenant GUC  = app.current_tenant (via get_current_tenant())
//   - canonical bypass GUCs = app.current_role='super_admin' OR app.bypass_rls
//   - deprecated vocabulary = app.tenant_id (zero code setting points, V371
//     comment acknowledged the bypass_rls fallback) and app.is_super_admin
//     (2 residual baseline policies, zero setting points)
//
// Migration 720 rewrote all 59 live policies off the deprecated vocabulary;
// these guards stop new policies (SQL migrations or Go ensure bodies) from
// reintroducing it. The live-DB census closes the loop on the actual catalog
// and encodes the §六 Phase 1 acceptance criterion (deprecated-GUC policy
// count = 0).

// deprecatedPolicyGUCs 是已弃用 GUC 词汇表（新 policy 禁用）。
var deprecatedPolicyGUCs = []string{"app.tenant_id", "app.is_super_admin"}

// policyWindows returns the text of every CREATE POLICY statement in sql
// (from the CREATE POLICY keyword to the terminating semicolon). Handles both
// raw .sql files and Go ensure bodies (backtick SQL constants): statements
// inside Go source still end at a semicolon before any Go code resumes.
// Full-line SQL comments are stripped first — migration headers discuss the
// vocabulary history ("app.tenant_id → get_current_tenant()") and must not
// trip the guard.
func policyWindows(t *testing.T, sql string) []string {
	t.Helper()
	var kept strings.Builder
	for _, line := range strings.Split(sql, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		kept.WriteString(line)
		kept.WriteString("\n")
	}
	text := kept.String()
	var windows []string
	for start := strings.Index(text, "CREATE POLICY"); start >= 0; {
		end := strings.Index(text[start:], ";")
		if end < 0 {
			end = len(text) - start
		}
		windows = append(windows, text[start:start+end])
		next := strings.Index(text[start+end:], "CREATE POLICY")
		if next < 0 {
			break
		}
		start = start + end + next
	}
	return windows
}

// assertNoDeprecatedGUC fails the test when any policy window references a
// deprecated GUC.
func assertNoDeprecatedGUC(t *testing.T, source string, windows []string) {
	t.Helper()
	for _, w := range windows {
		for _, guc := range deprecatedPolicyGUCs {
			if strings.Contains(w, guc) {
				head := w
				if len(head) > 200 {
					head = head[:200] + "…"
				}
				t.Errorf("%s: policy uses deprecated GUC %q (canonical: app.current_tenant via get_current_tenant(); bypass: app.current_role/app.bypass_rls):\n%s", source, guc, head)
			}
		}
	}
}

// TestRLSPolicyVocabularyNoDeprecatedGUCsInStartupMigrations scans every
// canonical startup migration (the installer embeddata copies are
// byte-equality-guarded in installer/cmd/llm-gw-installer) so a new migration
// cannot reintroduce the deprecated vocabulary.
func TestRLSPolicyVocabularyNoDeprecatedGUCsInStartupMigrations(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "sql", "migrations", "startup"))
	if err != nil {
		t.Fatalf("read startup migrations: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") || strings.HasSuffix(name, ".down.sql") {
			// .down.sql exempt: rollback migrations restore the deprecated
			// vocabulary by design (720 down re-creates the pre-720 policies).
			continue
		}
		text, err := os.ReadFile(filepath.Join("..", "sql", "migrations", "startup", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		assertNoDeprecatedGUC(t, name, policyWindows(t, string(text)))
	}
}

// TestRLSPolicyVocabularyNoDeprecatedGUCsInEnsure scans the Go ensure chain
// (the only non-migration source of CREATE POLICY) for the same vocabulary.
func TestRLSPolicyVocabularyNoDeprecatedGUCsInEnsure(t *testing.T) {
	for _, name := range []string{"db.go", "db_omnifree.go"} {
		text, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		assertNoDeprecatedGUC(t, name, policyWindows(t, string(text)))
	}
}

// TestRLSPolicyVocabularyLiveCensus runs the §六 acceptance census against a
// live database (opt-in via TEST_DB_URL): zero public-schema policies may
// reference the deprecated GUCs.
func TestRLSPolicyVocabularyLiveCensus(t *testing.T) {
	dsn := os.Getenv("TEST_DB_URL")
	if dsn == "" {
		t.Skip("set TEST_DB_URL to run the live policy-vocabulary census")
	}
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(t.Context())

	for _, guc := range deprecatedPolicyGUCs {
		var n int
		err := conn.QueryRow(t.Context(), `
			SELECT count(*) FROM pg_policies
			WHERE schemaname = 'public'
			  AND (qual LIKE '%' || $1 || '%' OR with_check LIKE '%' || $1 || '%')`,
			guc).Scan(&n)
		if err != nil {
			t.Fatalf("census %s: %v", guc, err)
		}
		if n != 0 {
			t.Errorf("live census: %d public policies still reference deprecated GUC %q (acceptance: 0)", n, guc)
		}
	}
}
