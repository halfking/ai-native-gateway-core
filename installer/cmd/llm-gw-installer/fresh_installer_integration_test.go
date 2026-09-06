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
		FROM pg_class c
		WHERE c.relnamespace = 'public'::regnamespace
		  AND c.relkind IN ('r', 'p', 'v', 'm', 'S')
		  AND NOT EXISTS (
		    SELECT 1 FROM pg_depend d
		    WHERE d.objid = c.oid AND d.deptype = 'e'
		  );`); got != "0" {
		t.Fatalf("%s must point to an empty dedicated database; found %s non-extension public relations", installerFreshDBURL, got)
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

	// 2026-09-07 embeddata sync (632 + 666..681 wired into StartupFiles): a
	// fresh install must expose every object the runtime expects from those
	// migrations. 665/661 are intentionally absent (audit ruling: 664 + 681 is
	// authoritative; 661 repairs存量库 whose 563 source has already been fixed).
	for name, query := range map[string]string{
		"632 fs-cleanup ledger table": `SELECT to_regclass('public.audit_attachments_filesystem_cleanup') IS NOT NULL`,
		"666 llm_hourly_stats table":  `SELECT to_regclass('public.llm_hourly_stats') IS NOT NULL`,
		"666 orchestration table":     `SELECT to_regclass('public.orchestration_runtime_instances') IS NOT NULL`,
		"667 normalize function":      `SELECT to_regprocedure('public.normalize_hour_timestamp(text)') IS NOT NULL`,
		"668 batch upsert function":   `SELECT to_regprocedure('public.upsert_llm_hourly_stats_batch(jsonb)') IS NOT NULL`,
		"669 annotations table":       `SELECT to_regclass('public.training_human_annotations') IS NOT NULL`,
		"673 annotation_stats view":   `SELECT to_regclass('public.annotation_stats') IS NOT NULL`,
		"674 annotations unique index": `
			SELECT count(*) = 1 FROM pg_indexes
			WHERE schemaname = 'public' AND indexname = 'uq_training_human_annotations_request_id'`,
		"670 routing opt state table": `SELECT to_regclass('public.routing_optimization_state') IS NOT NULL`,
		"670 feedback log table":      `SELECT to_regclass('public.routing_feedback_log') IS NOT NULL`,
		"676 single-active index": `
			SELECT count(*) = 1 FROM pg_indexes
			WHERE schemaname = 'public' AND indexname = 'idx_opt_state_single_active'`,
		"671 local catalog capabilities": `
			SELECT count(*) = 5 FROM provider_catalog
			WHERE kind = 'local' AND capabilities ? 'hosting_type'`,
		"672 title/summary local routes": `
			SELECT count(*) = 2 FROM work_type_model_route
			WHERE work_type_key IN ('session_title', 'session_summary')
			  AND canonical_name = '4-bit' AND weight = 10.0 AND enabled`,
		"675 qwen3.8 family row": `
			SELECT count(*) = 1 FROM model_families WHERE id = 'qwen3.8' AND vendor = 'Alibaba'`,
		"679 local credential unique index": `
			SELECT count(*) = 1 FROM pg_indexes
			WHERE schemaname = 'public' AND indexname = 'uq_credentials_local_placeholder_per_provider'`,
		"680 current-month view":       `SELECT to_regclass('public.request_logs_with_current_month') IS NOT NULL`,
		"681 fingerprint index is 8-part (no error_message)": `
			SELECT position('error_message' in pg_get_indexdef(
				'public.idx_provider_error_details_tenant_cred_fingerprint'::regclass)) = 0`,
	} {
		if got := psqlScalar(t, dsn, query); got != "t" {
			t.Errorf("fresh installer 66x-68x sync %s check failed: got %q", name, got)
		}
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
