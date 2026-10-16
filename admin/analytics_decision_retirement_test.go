package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// This fixture is deliberately isolated: it never modifies the deployment DB's
// public tables, and exercises the real registered HTTP handler against PG.
func TestDecisionReplayRetirementRealPG(t *testing.T) {
	dsn := os.Getenv("DECISION_RETIREMENT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("DECISION_RETIREMENT_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	adminDB, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	defer adminDB.Close()
	schema := fmt.Sprintf("decision_retirement_%d", time.Now().UnixNano())
	_, err = adminDB.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	defer func() {
		_, err := adminDB.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		require.NoError(t, err)
	}()
	cfg = cfg.Copy()
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	defer pool.Close()

	// Column types match 430/513/733: session IDs/credentials are text,
	// legacy decision is jsonb and L2 request_id is uuid. Cold tables really
	// are partitioned, rather than mocks that accept invalid UNIONs/casts.
	_, err = pool.Exec(ctx, `
CREATE TABLE session_turns (
 ts timestamptz NOT NULL DEFAULT now(), task_type text, auto_confidence double precision,
 model text, api_key_id text, credential_id text, auto_decision text, success boolean,
 latency_ms integer, tenant_id varchar(255) NOT NULL, request_id text NOT NULL,
 partition_date date NOT NULL DEFAULT CURRENT_DATE
) PARTITION BY RANGE (partition_date);
CREATE TABLE session_turns_cold PARTITION OF session_turns DEFAULT;
CREATE TABLE session_turns_hot (LIKE session_turns INCLUDING DEFAULTS);
CREATE TABLE session_turn_details (
 tenant_id varchar(255) NOT NULL, request_id text NOT NULL,
 partition_date date NOT NULL DEFAULT CURRENT_DATE, auto_profile text, client_model text,
 UNIQUE (tenant_id, request_id, partition_date)
) PARTITION BY RANGE (partition_date);
CREATE TABLE session_turn_details_cold PARTITION OF session_turn_details DEFAULT;
CREATE TABLE session_turn_details_hot (LIKE session_turn_details INCLUDING DEFAULTS INCLUDING INDEXES);
CREATE TABLE _request_logs (
 ts timestamptz NOT NULL DEFAULT now(), task_type text, auto_profile text, auto_confidence numeric(4,3),
 client_model text, outbound_model text, api_key_id bigint, credential_id bigint,
 auto_decision jsonb, success boolean, latency_ms integer, tenant_id text, request_id text
) PARTITION BY RANGE (ts);
CREATE TABLE _request_logs_cold PARTITION OF _request_logs DEFAULT;
CREATE TABLE _request_logs_hot (LIKE _request_logs INCLUDING DEFAULTS);
CREATE TABLE routing_decision_log (
 request_id uuid, tenant_id text, ts timestamptz NOT NULL DEFAULT now(),
 chosen_credential_id integer, chosen_provider_id integer, tier integer,
 candidates_tried integer, success boolean, resolution_path text, canonical_model text, decision_trace jsonb
);`)
	require.NoError(t, err)
	mux := http.NewServeMux()
	NewAnalyticsHandlers(pool).RegisterAnalyticsRoutes(mux, func(h http.HandlerFunc) http.HandlerFunc { return h })
	lookup := func(t *testing.T, id, tenant string, status int) map[string]any {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/admin/auto-route/analytics/decision/"+id, nil)
		r = setTestRequestContext(r, "tenant_admin", tenant, "decision-test")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		require.Equal(t, status, w.Code, w.Body.String())
		var got map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
		return got
	}
	insertTurn := func(t *testing.T, table, id, tenant string) {
		t.Helper()
		_, err := pool.Exec(ctx, "INSERT INTO "+table+` (request_id, tenant_id, task_type, auto_confidence, model,
 api_key_id, credential_id, auto_decision, success, latency_ms)
 VALUES ($1,$2,'coding',0.875,'chosen-model','123','456','{"reason":"session-decision"}',true,37)`, id, tenant)
		require.NoError(t, err)
	}
	insertDetail := func(t *testing.T, table, id, tenant, profile string) {
		t.Helper()
		_, err := pool.Exec(ctx, "INSERT INTO "+table+` (request_id,tenant_id,auto_profile,client_model)
 VALUES ($1,$2,$3,'auto')`, id, tenant, profile)
		require.NoError(t, err)
	}
	insertLegacy := func(t *testing.T, table, id, tenant string) {
		t.Helper()
		_, err := pool.Exec(ctx, "INSERT INTO "+table+` (request_id, tenant_id, task_type, auto_profile,
 auto_confidence, client_model, outbound_model, api_key_id, credential_id, auto_decision, success, latency_ms)
 VALUES ($1,$2,'legacy-task','legacy-profile',0.5,'legacy-client','legacy-model',11,22,
 '{"reason":"legacy-decision"}',false,99)`, id, tenant)
		require.NoError(t, err)
	}

	for i, layers := range [][2]string{
		{"session_turns_hot", "session_turn_details_hot"},
		{"session_turns", "session_turn_details"},
		{"session_turns_hot", "session_turn_details"},
		{"session_turns", "session_turn_details_hot"},
	} {
		t.Run(strings.Join(layers[:], "+"), func(t *testing.T) {
			id := fmt.Sprintf("841eb980c90c46d86dbcf0556cbb%04d", i)
			insertTurn(t, layers[0], id, "tenant-a")
			insertDetail(t, layers[1], id, "tenant-a", "session-profile")
			// Conflicting legacy row must never supersede current session data.
			insertLegacy(t, "_request_logs_hot", id, "tenant-a")
			insertTurn(t, layers[0], id, "tenant-b")
			insertDetail(t, layers[1], id, "tenant-b", "secret-other-profile")
			_, err := pool.Exec(ctx, `INSERT INTO routing_decision_log
 (request_id,tenant_id,chosen_credential_id,success,decision_trace) VALUES
 ($1::uuid,'tenant-a',456,true,'{"scope":"own"}'),
 ($1::uuid,'tenant-b',999,true,'{"scope":"other"}')`, id)
			require.NoError(t, err)
			// Dashed path resolves the compact text L1 ID and UUID L2 ID.
			got := lookup(t, uuidVariants(id)[1], "tenant-a", http.StatusOK)
			require.Equal(t, "chosen-model", got["outbound_model"])
			require.Equal(t, "auto", got["client_model"])
			require.Equal(t, float64(123), got["api_key_id"])
			require.Equal(t, float64(456), got["credential_id"])
			require.Equal(t, float64(37), got["latency_ms"])
			l1 := got["l1"].(map[string]any)
			require.Equal(t, "session-profile", l1["profile"])
			require.Equal(t, "session-decision", l1["reason"])
			require.Equal(t, "coding", l1["task_type"])
			require.Equal(t, 0.875, l1["confidence"])
			l2 := got["l2"].(map[string]any)
			require.Equal(t, float64(456), l2["chosen_credential_id"])
			require.Equal(t, "own", l2["decision_trace"].(map[string]any)["scope"])
			lookup(t, id, "tenant-c", http.StatusNotFound)
		})
	}
	for _, table := range []string{"_request_logs_hot", "_request_logs"} {
		t.Run("legacy-"+table, func(t *testing.T) {
			id := "probe-" + table
			insertLegacy(t, table, id, "tenant-a")
			got := lookup(t, id, "tenant-a", http.StatusOK)
			require.Equal(t, "legacy-model", got["outbound_model"])
			require.Equal(t, "legacy-profile", got["l1"].(map[string]any)["profile"])
			require.Equal(t, "legacy-decision", got["l1"].(map[string]any)["reason"])
			require.NotContains(t, got, "l2", "probe IDs must not be cast to uuid")
			lookup(t, id, "tenant-b", http.StatusNotFound)
		})
	}
	t.Run("hot-priority-and-dashed-storage", func(t *testing.T) {
		id := "741eb980-c90c-46d8-6dbc-f0556cbb2687"
		insertTurn(t, "session_turns", id, "tenant-a")
		insertTurn(t, "session_turns_hot", id, "tenant-a")
		_, err := pool.Exec(ctx, `UPDATE session_turns SET model='stale-cold-model' WHERE request_id=$1`, id)
		require.NoError(t, err)
		insertDetail(t, "session_turn_details", id, "tenant-a", "stale-cold-profile")
		insertDetail(t, "session_turn_details_hot", id, "tenant-a", "fresh-hot-profile")
		got := lookup(t, strings.ToUpper(id), "tenant-a", http.StatusOK)
		require.Equal(t, "chosen-model", got["outbound_model"])
		require.Equal(t, "fresh-hot-profile", got["l1"].(map[string]any)["profile"])
		got = lookup(t, strings.ReplaceAll(id, "-", ""), "tenant-a", http.StatusOK)
		require.Equal(t, "fresh-hot-profile", got["l1"].(map[string]any)["profile"])
	})
	t.Run("details-full-key-isolation", func(t *testing.T) {
		id := "probe-details-key"
		insertTurn(t, "session_turns_hot", id, "tenant-a")
		insertDetail(t, "session_turn_details_hot", id, "tenant-b", "other-tenant")
		insertDetail(t, "session_turn_details_hot", id, "tenant-a", "other-date")
		_, err := pool.Exec(ctx, `UPDATE session_turn_details_hot SET partition_date=CURRENT_DATE-1
 WHERE request_id=$1 AND tenant_id='tenant-a'`, id)
		require.NoError(t, err)
		got := lookup(t, id, "tenant-a", http.StatusOK)
		require.Equal(t, "", got["l1"].(map[string]any)["profile"], "wrong tenant/date details must not enrich a turn")
	})
	t.Run("session-without-details-nonnumeric-credentials", func(t *testing.T) {
		insertTurn(t, "session_turns_hot", "probe-session-only", "tenant-a")
		_, err := pool.Exec(ctx, `UPDATE session_turns_hot SET api_key_id='opaque-key', credential_id='provider-key'
 WHERE request_id='probe-session-only'`)
		require.NoError(t, err)
		got := lookup(t, "probe-session-only", "tenant-a", http.StatusOK)
		require.Equal(t, "", got["l1"].(map[string]any)["profile"])
		require.Equal(t, "session-decision", got["l1"].(map[string]any)["reason"])
		require.NotContains(t, got, "api_key_id")
		require.NotContains(t, got, "credential_id")
		require.NotContains(t, got, "l2")
	})
	t.Run("missing", func(t *testing.T) {
		lookup(t, "missing-request", "tenant-a", http.StatusNotFound)
	})
}
