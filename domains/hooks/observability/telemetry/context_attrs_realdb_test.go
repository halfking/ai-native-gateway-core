package telemetry

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// Uses only a connection-local temporary table. Never run the production
// migration or modify a persistent table, even if the supplied DSN is wrong.
func TestPersistContextAttrsSparseUpdatePreservesMetadata_RealDB(t *testing.T) {
	dsn := os.Getenv("CONTEXT_ATTRS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set CONTEXT_ATTRS_TEST_DATABASE_URL for temporary-table PostgreSQL regression")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	cfg.MaxConns = 1
	cfg.MinConns = 0
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = time.Hour
	// Apply to every backend, including replacements after a disconnect.
	// A replacement without the fixture must fail, never fall back to public.
	cfg.ConnConfig.RuntimeParams["search_path"] = "pg_temp"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	defer pool.Close()

	migration, err := os.ReadFile("../../../../sql/migrations/startup/407_request_context_attrs.sql")
	require.NoError(t, err)
	ddl := string(migration)
	start := strings.Index(ddl, "CREATE TABLE IF NOT EXISTS public.request_context_attrs (")
	require.NotEqual(t, -1, start)
	ddl = ddl[start:]
	end := strings.Index(ddl, ");")
	require.NotEqual(t, -1, end)
	ddl = strings.Replace(ddl[:end+2], "CREATE TABLE IF NOT EXISTS public.request_context_attrs", "CREATE TEMP TABLE request_context_attrs", 1)
	_, err = pool.Exec(ctx, ddl)
	require.NoError(t, err)
	var tempTable *string
	require.NoError(t, pool.QueryRow(ctx, "SELECT to_regclass('pg_temp.request_context_attrs')::text").Scan(&tempTable))
	require.NotNil(t, tempTable, "temporary fixture must exist before exercising production writer")
	client := &Client{dbPool: pool}
	text := func(v string) *string { return &v }
	integer := func(v int) *int { return &v }
	customer := int64(42)
	full := &ContextAttrsEntry{
		RequestID: "sparse-regression", TenantID: "test-tenant",
		GwSessionID: text("session"), GwTaskID: text("task"),
		IdentityHash: text("identity"), VirtualClientID: text("virtual"),
		VirtualIP: text("10.0.0.1"), VirtualMAC: text("02:00:00:00:00:01"),
		AgentName: text("agent"), AgentType: text("cli"), ClientIP: text("127.0.0.1"),
		ClientForwardedFor: text("127.0.0.1"), APIKeyFingerprint: text("fingerprint"),
		APIKeyID: integer(7), ApplicationID: integer(8), ApplicationCode: text("app-old"),
		OwnerUser: text("owner"), EndUserID: text("end-user"), CustomerID: &customer,
		ClientProtocol: text("openai-chat"), AttemptNo: integer(1), TurnNo: integer(2),
		OriginStage: text("business"), SourceChannel: text("api"), ClientRequestID: text("client-request"),
		ProjectID: text("project-old"), SessionTitle: text("title"), SessionSummary: text("summary"),
		TaskID: text("task"), FingerprintRaw: json.RawMessage(`{"os":"test"}`),
	}
	readMetadata := func() map[string]interface{} {
		t.Helper()
		var raw []byte
		err := pool.QueryRow(ctx, `SELECT to_jsonb(r) - 'ts' - 'is_retry' - 'is_probe' FROM request_context_attrs r WHERE request_id=$1`, full.RequestID).Scan(&raw)
		require.NoError(t, err)
		var result map[string]interface{}
		require.NoError(t, json.Unmarshal(raw, &result))
		return result
	}
	require.NoError(t, client.persistContextAttrs(full))
	before := readMetadata()
	// A terminal/retry emission may not have the initial attempt's metadata.
	require.NoError(t, client.persistContextAttrs(&ContextAttrsEntry{
		RequestID: full.RequestID, TenantID: full.TenantID, IsRetry: true,
	}))
	require.Equal(t, before, readMetadata(), "absent metadata must not erase the initial emission")
	var retry bool
	require.NoError(t, pool.QueryRow(ctx, "SELECT is_retry FROM request_context_attrs WHERE request_id=$1", full.RequestID).Scan(&retry))
	require.True(t, retry, "present terminal-state values still update")

	// Non-null late values must replace earlier values (not first-write-wins).
	require.NoError(t, client.persistContextAttrs(&ContextAttrsEntry{
		RequestID: full.RequestID, TenantID: full.TenantID,
		ApplicationCode: text("app-new"), ProjectID: text("project-new"),
		FingerprintRaw: json.RawMessage(`{"os":"new"}`),
	}))
	after := readMetadata()
	require.Equal(t, "app-new", after["application_code"])
	require.Equal(t, "project-new", after["project_id"])
	require.Equal(t, map[string]interface{}{"os": "new"}, after["fingerprint_raw"])
	require.Equal(t, before["api_key_id"], after["api_key_id"])

	// Simulate replacement of the only backend. Temporary metadata is lost,
	// but isolation must survive and the writer must fail closed.
	conn, err := pool.Acquire(ctx)
	require.NoError(t, err)
	require.NoError(t, conn.Conn().Close(ctx))
	conn.Release()
	var searchPath string
	require.NoError(t, pool.QueryRow(ctx, "SHOW search_path").Scan(&searchPath))
	require.Equal(t, "pg_temp", searchPath)
	err = client.persistContextAttrs(full)
	require.ErrorContains(t, err, "SQLSTATE 42P01", "reconnected backend must not resolve a persistent public table")
}
