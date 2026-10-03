//go:build integration

package sessionv2mirror

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/session/v2"
)

func getTestDBURL() string {
	return os.Getenv("TEST_DB_URL")
}

func setupTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := getTestDBURL()
	if dbURL == "" {
		t.Skip("TEST_DB_URL not set")
	}
	ctx := context.Background()
	// §9.102: build the pool the way db/db.go builds the gateway's, not the
	// way pgx defaults. db/db.go:72 sets
	//
	//	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	//
	// (a 2026-07-15 P0 fix that disables the prepared-statement cache, because
	// long-lived connections kept serving plans for relations that had since
	// been renamed). Under SimpleProtocol pgx inlines arguments as literals, so
	// the server never performs parameter type deduction.
	//
	// This is not a cosmetic detail. A pool built with pgx defaults runs the
	// extended protocol, where the server DOES deduce, and two statements that
	// work in production fail there with 42P08 — see §9.102. A real-DB gate
	// that used a different exec mode from the product was therefore
	// validating a configuration nobody runs, and reported two phantom
	// "production-breaking" defects that were really defects of the harness.
	// Keep this in sync with db/db.go; if that line changes, change it here.
	cfg, err := pgxpool.ParseConfig(dbURL)
	require.NoError(t, err)
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	require.NoError(t, db.Ping(ctx))

	// §9.103: a fresh install ships no partition for the CURRENT month — only
	// the months that existed when the schema was built. Writing a session row
	// then fails with 23514 "no partition of relation \"sessions\" found for
	// row". The function that creates them (migration 430's
	// ensure_sessions_v2_partitions) is only invoked by the bg/partition_manager
	// worker, so a fresh install has a startup window where session writes are
	// rejected until that worker first runs.
	//
	// Do the same thing here that boot/the worker does, rather than asserting
	// the write path works without it. This is a startup-ordering window, not
	// a permanent defect: production's worker runs continuously.
	if _, err := db.Exec(ctx, `SELECT public.ensure_sessions_v2_partitions(current_date)`); err != nil {
		t.Fatalf("ensure_sessions_v2_partitions(current_date): %v", err)
	}
	// Enable the V2 schema — defer settings init will pick up the feature flags
	// from the DB (settings_kv), but since the pool is separate the local
	// settings.Global may be nil → flags return false → hook short-circuits.
	//
	// We work around this by directly calling writer.Write to test the DB path.
	return db
}

func cleanupTestV2Data(t *testing.T, db *pgxpool.Pool, sessionID, tenantID string) {
	t.Helper()
	ctx := context.Background()
	tables := []string{
		"public.sessions",
		"public.session_turns_hot",
		"public.session_turns",
		"public.session_bodies",
		"public.session_turn_logs",
	}
	for _, table := range tables {
		q := fmt.Sprintf(`DELETE FROM %s WHERE session_id = $1 AND tenant_id = $2`, table)
		if _, err := db.Exec(ctx, q, sessionID, tenantID); err != nil {
			t.Logf("cleanup %s: %v", table, err)
		}
	}
}

// TestPersistHook_Integration_DBWrite exercises the full shadow write path
// against the real PG17 on 252. Requires TEST_DB_URL with write access to
// public.sessions / session_turns / session_bodies / session_turn_logs.
//
//	Run:   TEST_DB_URL="postgres://llm_gateway:$(pass)@172.16.2.210:5432/llm_gateway" \
//	         go test -tags=integration -run TestPersistHook_Integration_DBWrite \
//	         ./internal/sessionv2mirror/ -v -count=1
func TestPersistHook_Integration_DBWrite(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	sessionID := fmt.Sprintf("intg_test_%d", time.Now().UnixNano())
	tenantID := "test_tenant"

	defer cleanupTestV2Data(t, db, sessionID, tenantID)

	// Build the writer components
	turnWriter := v2.NewTurnWriter(db)
	bodiesWriter := v2.NewSessionBodiesWriter(db)
	aggregator := v2.NewSessionAggregator(db)
	turnLogsWriter := v2.NewTurnLogsWriter(db)
	writer := v2.NewSessionWriterV2(turnWriter, bodiesWriter, aggregator, turnLogsWriter)

	// Build a telemetry entry with GwSessionID
	now := time.Now()
	body := `{"messages":[{"role":"user","content":"hello from integration test"}]}`

	entry := &telemetry.RequestLogEntry{
		RequestID:    fmt.Sprintf("intg_req_%d", now.UnixNano()),
		GwSessionID:  &sessionID,
		TenantID:     tenantID,
		ClientModel:  strPtr("deepseek-v3-cn"),
		ProviderID:   intPtr(36),
		CredentialID: intPtr(42),
		Success:      true,
		EventAt:      &now,
		LatencyMs:    intPtr(500),
		PromptTokens: intPtr(100),
		RequestBody:  &body,
	}

	// Convert to ProcessedRequest and write directly (bypasses settings flag check)
	req := entryToProcessedRequest(entry, *entry.GwSessionID)
	require.NotNil(t, req)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := writer.Write(ctx, req)
	require.NoError(t, err, "writer.Write should succeed")

	// Verify data lands in the hot table before the asynchronous promotion job runs.
	var turnCount int
	var digestSchemaVersion string
	err = db.QueryRow(ctx,
		`SELECT COUNT(*), COALESCE(MAX(digest->>'schema_version'), '')
		 FROM public.session_turns_hot
		 WHERE session_id = $1 AND tenant_id = $2`,
		sessionID, tenantID).Scan(&turnCount, &digestSchemaVersion)
	require.NoError(t, err)
	require.Equal(t, 1, turnCount, "expected one hot turn")
	require.Equal(t, "1", digestSchemaVersion, "expected persisted digest schema version")

	// §9.103: this assertion read public.session_bodies and could never pass.
	// SessionBodiesWriter.WriteBodiesInTx writes to session_bodies_hot (bodies_writer.go,
	// "Write to session_bodies_hot (8-hour window), not directly to partitioned
	// table"); PartitionManager promotes rows into the session_bodies monthly
	// partitions later. So a row written moments ago is in _hot and nowhere
	// else — the test has been red for as long as it has existed, and the
	// failure had nothing to do with the write path it claimed to cover.
	var bodyCount int
	err = db.QueryRow(ctx,
		`SELECT COUNT(*) FROM public.session_bodies_hot WHERE session_id = $1 AND tenant_id = $2`,
		sessionID, tenantID).Scan(&bodyCount)
	require.NoError(t, err)
	require.Equal(t, 1, bodyCount, "expected 1 bodies row in session_bodies_hot")

	// Session aggregator runs async (goroutine), so give it a moment
	time.Sleep(500 * time.Millisecond)

	var sessionCount int
	err = db.QueryRow(ctx,
		`SELECT COUNT(*) FROM public.sessions WHERE session_id = $1 AND tenant_id = $2`,
		sessionID, tenantID).Scan(&sessionCount)
	require.NoError(t, err)
	require.Equal(t, 1, sessionCount, "expected 1 session row")

	t.Logf("Integration test passed: session=%s turn=%d bodies=%d sessions=%d",
		sessionID, turnCount, bodyCount, sessionCount)
}
