//go:build integration

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/installer/internal/dbinit"
)

const installerFreshDBURL = "TEST_INSTALLER_FRESH_DB_URL"

// TestFreshInstallerSessionTurnsHotBootstrap applies the exact fresh-installer
// sequence to an empty, dedicated database and verifies the Session V2 hot-path
// objects that the runtime needs. The target must be disposable: the test rejects
// a database that already contains public relations.
func TestFreshInstallerSessionTurnsHotBootstrap(t *testing.T) {
	dsn := os.Getenv(installerFreshDBURL)
	if dsn == "" {
		t.Skipf("%s not set; skipping fresh installer integration test", installerFreshDBURL)
	}
	if got := psqlScalar(t, dsn, `
		SELECT count(*)
		FROM pg_class
		WHERE relnamespace = 'public'::regnamespace
		  AND relkind IN ('r', 'p', 'v', 'm', 'S');`); got != "0" {
		t.Fatalf("%s must point to an empty dedicated database; found %s public relations", installerFreshDBURL, got)
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
	for _, name := range runner.StartupFiles {
		applyInstallerSQL(t, dsn, filepath.Join(sqlDir, "startup", name))
	}

	for name, query := range map[string]string{
		"hot table":              `SELECT to_regclass('public.session_turns_hot') IS NOT NULL`,
		"current-month view":     `SELECT to_regclass('public.session_turns_with_current_month') IS NOT NULL`,
		"advisory lock function": `SELECT to_regprocedure('public.session_turns_advisory_lock_key(text,text)') IS NOT NULL`,
		"promotion function":     `SELECT to_regprocedure('public.promote_session_turns_hot_to_partition(interval,integer)') IS NOT NULL`,
		"hot table RLS":          `SELECT relrowsecurity FROM pg_class WHERE oid = 'public.session_turns_hot'::regclass`,
		"security-invoker view":  `SELECT 'security_invoker=true' = ANY(reloptions) FROM pg_class WHERE oid = 'public.session_turns_with_current_month'::regclass`,
		"hot table column count": `SELECT count(*) = 51 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'session_turns_hot'`,
		"view column count":      `SELECT count(*) = 51 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'session_turns_with_current_month'`,
		"nullable JSONB digest": `
			SELECT count(*) = 2
			FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name IN ('session_turns', 'session_turns_hot')
			  AND column_name = 'digest'
			  AND data_type = 'jsonb'
			  AND is_nullable = 'YES'`,
		"parent hot column parity": `
			WITH parent_columns AS (
				SELECT column_name, data_type, is_nullable, ordinal_position
				FROM information_schema.columns
				WHERE table_schema = 'public' AND table_name = 'session_turns'
			), hot_columns AS (
				SELECT column_name, data_type, is_nullable, ordinal_position
				FROM information_schema.columns
				WHERE table_schema = 'public' AND table_name = 'session_turns_hot'
			)
			SELECT NOT EXISTS (
				(SELECT * FROM parent_columns EXCEPT SELECT * FROM hot_columns)
				UNION ALL
				(SELECT * FROM hot_columns EXCEPT SELECT * FROM parent_columns)
			)`,
	} {
		if got := psqlScalar(t, dsn, query); got != "t" {
			t.Errorf("fresh installer %s check failed: got %q", name, got)
		}
	}

	if got := psqlScalar(t, dsn, `
		SELECT count(*) = 3
		FROM pg_policies
		WHERE schemaname = 'public'
		  AND tablename = 'session_turns_hot'`); got != "t" {
		t.Errorf("fresh installer hot table policies: got %q, want exactly three", got)
	}
}

func applyInstallerSQL(t *testing.T, dsn, path string) {
	t.Helper()
	cmd := exec.Command("psql", "-v", "ON_ERROR_STOP=1", "--single-transaction", "--dbname", dsn, "-f", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("apply %s: %v\n%s", filepath.Base(path), err, out)
	}
}

func psqlScalar(t *testing.T, dsn, query string) string {
	t.Helper()
	cmd := exec.Command("psql", "-At", "-v", "ON_ERROR_STOP=1", "--dbname", dsn, "-c", query)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("query installer integration database: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}
