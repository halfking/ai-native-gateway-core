package startup

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// Only connection-local temporary fixtures are used; never public business data.
func TestMigration867GlobalOnlinePlan_RealDB(t *testing.T) {
	dsn := os.Getenv("CONTEXT_ATTRS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set CONTEXT_ATTRS_TEST_DATABASE_URL for isolated PostgreSQL plan regression")
	}
	ctx := context.Background()
	cfg, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	cfg.RuntimeParams["search_path"] = "pg_temp"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	require.NoError(t, err)
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, `CREATE TEMP TABLE session_summaries (session_key varchar(255) PRIMARY KEY, tenant_id text, last_request_at timestamptz);
INSERT INTO session_summaries SELECT i::text, 'tenant-' || (i % 100)::text, now() - (i/3) * interval '1 minute' FROM generate_series(1, 20000) i;
CREATE INDEX tenant_time_fixture ON session_summaries(tenant_id, last_request_at DESC);
ANALYZE session_summaries;`)
	require.NoError(t, err)
	query := `EXPLAIN (FORMAT JSON) SELECT session_key,last_request_at FROM session_summaries WHERE last_request_at IS NOT NULL AND last_request_at >= now()-interval '1 hour' ORDER BY last_request_at DESC,session_key DESC LIMIT 21`
	var before, after []byte
	require.NoError(t, conn.QueryRow(ctx, query).Scan(&before))
	require.NotContains(t, string(before), idx867Name)
	raw, err := os.ReadFile(idx867File)
	require.NoError(t, err)
	ddl := strings.ReplaceAll(string(raw), "public.session_summaries", "pg_temp.session_summaries")
	ddl = strings.ReplaceAll(ddl, "public."+idx867Name, "pg_temp."+idx867Name)
	_, err = conn.Exec(ctx, ddl)
	require.NoError(t, err)
	require.NoError(t, conn.QueryRow(ctx, query).Scan(&after))
	require.Contains(t, string(after), idx867Name)
	require.NotContains(t, string(after), `"Node Type": "Sort"`)
	require.NotContains(t, string(after), `"Node Type": "Incremental Sort"`)
	var cursorPlan []byte
	require.NoError(t, conn.QueryRow(ctx, `EXPLAIN (FORMAT JSON) SELECT session_key,last_request_at FROM session_summaries WHERE last_request_at IS NOT NULL AND last_request_at >= now()-interval '1 hour' AND (last_request_at,session_key)<(now(),'999') ORDER BY last_request_at DESC,session_key DESC LIMIT 21`).Scan(&cursorPlan))
	require.Contains(t, string(cursorPlan), idx867Name)
	require.NotContains(t, string(cursorPlan), `"Node Type": "Sort"`)
	require.NotContains(t, string(cursorPlan), `"Node Type": "Incremental Sort"`)

	for _, definition := range []string{
		`tenant_id,last_request_at DESC`,
		`last_request_at DESC,session_key COLLATE "C" DESC`,
		`last_request_at DESC,session_key text_pattern_ops DESC`,
		`last_request_at DESC,session_key bpchar_ops DESC`,
	} {
		_, err = conn.Exec(ctx, `DROP INDEX pg_temp.`+idx867Name+`;
CREATE INDEX `+idx867Name+` ON pg_temp.session_summaries(`+definition+`) WHERE last_request_at IS NOT NULL;`)
		require.NoError(t, err)
		_, err = conn.Exec(ctx, ddl)
		require.ErrorContains(t, err, "definition mismatch", "wrong ordering: %s", definition)
		// The canonical migration has an explicit BEGIN; recover the failed tx.
		_, err = conn.Exec(ctx, "ROLLBACK")
		require.NoError(t, err)
	}
	_, err = conn.Exec(ctx, `DROP INDEX pg_temp.`+idx867Name+`;
CREATE INDEX `+idx867Name+` ON pg_temp.session_summaries(last_request_at DESC,session_key DESC) WHERE last_request_at IS NOT NULL;`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, ddl)
	require.NoError(t, err, "an exact index permits idempotent replay")

	_, err = conn.Exec(ctx, `ALTER TABLE session_summaries ADD COLUMN padding text;
ALTER TABLE session_summaries ALTER COLUMN padding SET STORAGE PLAIN;
UPDATE session_summaries SET padding=repeat('x',2048);
DROP INDEX pg_temp.`+idx867Name+`;`)
	require.NoError(t, err)
	var bytes int64
	require.NoError(t, conn.QueryRow(ctx, "SELECT pg_relation_size('pg_temp.session_summaries')").Scan(&bytes))
	require.Greater(t, bytes, int64(33554432))
	_, err = conn.Exec(ctx, ddl)
	require.ErrorContains(t, err, "prebuild", "large tables must refuse an unprepared blocking build")
	_, err = conn.Exec(ctx, "ROLLBACK")
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `CREATE INDEX `+idx867Name+` ON pg_temp.session_summaries(last_request_at DESC,session_key DESC) WHERE last_request_at IS NOT NULL`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, ddl)
	require.NoError(t, err, "a valid prebuilt index permits replay")
}
