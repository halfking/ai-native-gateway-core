//go:build integration

package sessionv2mirror

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/session/v2"
)

const ingestReconcileMigration = "865_session_bodies_kind_reconcile.sql"

// This fixture is destructive only inside an explicitly dedicated throwaway DB.
// It uses real historical DDL rather than a mock accepting the writer's SQL.
func ingestFixture(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_SESSION_INGEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_SESSION_INGEST_DATABASE_URL to a throwaway *_test database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("parse isolated database configuration failed")
	}
	if !strings.HasSuffix(cfg.ConnConfig.Database, "_test") || os.Getenv("TEST_SESSION_INGEST_ISOLATED") != "1" {
		t.Fatal("refusing schema changes outside explicitly isolated *_test database")
	}
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	db, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal("open isolated database failed")
	}
	t.Cleanup(db.Close)
	ingestExec(t, db, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;
 CREATE TABLE public.schema_migrations(version text PRIMARY KEY, description text);
 CREATE FUNCTION get_current_tenant() RETURNS text LANGUAGE sql STABLE AS $$ SELECT current_setting('app.current_tenant', true) $$;`)
	ingestMigration(t, db, "430_sessions_v2_schema.sql")
	// The pre-707 session metadata prerequisites. These belong to other migrations
	// whose unrelated request-log dependencies are outside this writer fixture.
	ingestExec(t, db, `ALTER TABLE public.session_turns
 ADD COLUMN project_id text, ADD COLUMN namespace text,
 ADD COLUMN parent_request_id text, ADD COLUMN task_type text,
 ADD COLUMN attachment_count integer, ADD COLUMN attachment_total_bytes bigint,
 ADD COLUMN multimodal_types text[], ADD COLUMN title text, ADD COLUMN summary text,
 ADD COLUMN digest jsonb, ADD COLUMN request_status text, ADD COLUMN client_protocol text,
 ADD COLUMN t0_arrived_at timestamptz, ADD COLUMN t1_total_enqueued_at timestamptz,
 ADD COLUMN t2_total_dequeued_at timestamptz, ADD COLUMN t3_model_enqueued_at timestamptz,
 ADD COLUMN t4_model_dequeued_at timestamptz,
 ADD COLUMN t5_cred_enqueued_at timestamptz, ADD COLUMN t6_cred_dequeued_at timestamptz,
 ADD COLUMN t7_forward_start_at timestamptz, ADD COLUMN t8_response_start_at timestamptz,
 ADD COLUMN t9_response_end_at timestamptz;
 CREATE TABLE public.session_turns_hot (LIKE public.session_turns INCLUDING ALL);
 CREATE FUNCTION public.session_turns_advisory_lock_key(text,text) RETURNS bigint
 LANGUAGE sql IMMUTABLE STRICT AS $$ SELECT hashtextextended($1 || ':' || $2,0) $$;`)
	ingestMigration(t, db, "707_session_turns_s1a.sql")
	ingestMigration(t, db, "614_session_bodies_hot.sql")
	ingestMigration(t, db, "630_session_aggregate_outbox.sql")
	ingestExec(t, db, `CREATE VIEW public.session_turns_with_current_month AS
 SELECT * FROM public.session_turns_hot UNION ALL SELECT * FROM public.session_turns;
 CREATE TABLE public.gateway_db_revision_sequences(sequence_name text PRIMARY KEY, content_sha256 text);
 INSERT INTO public.gateway_db_revision_sequences VALUES
 ('local:708_session_bodies_s1a.sql','6f1f8d-marker-is-not-a-physical-postcondition');`)
	return db
}

func ingestExec(t *testing.T, db *pgxpool.Pool, sql string) {
	t.Helper()
	if _, err := db.Exec(context.Background(), sql); err != nil {
		t.Fatalf("isolated fixture SQL failed: %v", err)
	}
}

func ingestMigration(t *testing.T, db *pgxpool.Pool, name string) {
	t.Helper()
	path := filepath.Join("..", "..", "sql", "migrations", "startup", name)
	if name == ingestReconcileMigration && os.Getenv("TEST_SESSION_INGEST_RECONCILE_SQL") != "" {
		path = os.Getenv("TEST_SESSION_INGEST_RECONCILE_SQL")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("required migration %s unavailable: %v", name, err)
	}
	sql := regexp.MustCompile(`(?m)^\s*(?:BEGIN;|COMMIT;|\\[^\n]*)\s*$`).ReplaceAllString(string(data), "")
	ingestExec(t, db, sql)
}

func ingestRequest(id string) *v2.ProcessedRequest {
	return &v2.ProcessedRequest{TenantID: "ingest-tenant", SessionID: "ingest-session", RequestID: id,
		Timestamp: time.Now().UTC(), RequestBody: []v2.Message{{Role: "user", Content: "preserve me"}},
		ResponseBody: []v2.Message{{Role: "assistant", Content: "stored"}}, Success: true, StatusCode: 200,
		RequestStatus: telemetry.RequestStatusSuccess}
}

func ingestPairCount(t *testing.T, db *pgxpool.Pool, id string) (int, int) {
	t.Helper()
	var turns, bodies int
	err := db.QueryRow(context.Background(), `SELECT
 (SELECT count(*) FROM public.session_turns_hot WHERE request_id=$1),
 (SELECT count(*) FROM public.session_bodies_hot WHERE request_id=$1)`, id).Scan(&turns, &bodies)
	if err != nil {
		t.Fatal(err)
	}
	return turns, bodies
}

func TestSessionIngestMissingKindReconcilesWithoutOrphanTurn(t *testing.T) {
	db := ingestFixture(t)
	writer := v2.NewSessionWriterV2(v2.NewTurnWriter(db), v2.NewSessionBodiesWriter(db), nil, nil)
	req := ingestRequest("schema-retry")
	err := writer.Write(context.Background(), req)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42703" || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("missing-kind prerequisite must reach real bodies INSERT, got %v", err)
	}
	if turns, bodies := ingestPairCount(t, db, req.RequestID); turns != 0 || bodies != 0 {
		t.Fatalf("failed transaction left turns=%d bodies=%d", turns, bodies)
	}
	ingestMigration(t, db, ingestReconcileMigration)
	ingestMigration(t, db, ingestReconcileMigration)
	for i := 0; i < 2; i++ {
		if err := writer.Write(context.Background(), req); err != nil {
			t.Fatalf("retry %d after reconcile: %v", i, err)
		}
	}
	if turns, bodies := ingestPairCount(t, db, req.RequestID); turns != 1 || bodies != 1 {
		t.Fatalf("reconciled idempotent transaction turns=%d bodies=%d; want 1/1", turns, bodies)
	}
	var kind, text string
	if err := db.QueryRow(context.Background(), `SELECT kind,request_delta->0->>'content' FROM public.session_bodies_unified WHERE request_id=$1`, req.RequestID).Scan(&kind, &text); err != nil {
		t.Fatal(err)
	}
	if kind != "turn_delta" || text != "preserve me" {
		t.Fatalf("persisted body kind=%q content=%q", kind, text)
	}
}

func TestSessionIngestFullPersistHookTerminalPairs(t *testing.T) {
	db := ingestFixture(t)
	ingestMigration(t, db, ingestReconcileMigration)
	writer := v2.NewSessionWriterV2(v2.NewTurnWriter(db), v2.NewSessionBodiesWriter(db), nil, nil)
	prev := shadowWriteEnabledPtr.Load().(shadowEnabledFn)
	prevAsync := shadowWriteDispatchAsync
	setShadowWriteEnabledForTest(func() bool { return true })
	shadowWriteDispatchAsync = false
	t.Cleanup(func() { setShadowWriteEnabledForTest(prev); shadowWriteDispatchAsync = prevAsync })
	hook := PersistHook(writer)
	now := time.Now().UTC()
	body := `{"messages":[{"role":"user","content":"preserve me"}]}`
	session := "ingest-hook-session"
	for _, tc := range []struct {
		name, status string
		success      bool
		want         int
	}{
		{"success", telemetry.RequestStatusSuccess, true, 1},
		{"failure", telemetry.RequestStatusFailure, false, 1},
		{"rate-limit", telemetry.RequestStatusRateLimited, false, 1},
		{"in-progress", telemetry.RequestStatusInProgress, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status := tc.status
			entry := &telemetry.RequestLogEntry{RequestID: "hook-" + tc.name, GwSessionID: &session, TenantID: "ingest-tenant", Success: tc.success, RequestStatus: &status, RequestBody: &body, EventAt: &now}
			hook(entry)
			hook(entry)
			if turns, bodies := ingestPairCount(t, db, entry.RequestID); turns != tc.want || bodies != tc.want {
				t.Fatalf("full hook turns=%d bodies=%d, want %d each", turns, bodies, tc.want)
			}
			if tc.want == 1 {
				var success bool
				var storedStatus, content string
				if err := db.QueryRow(context.Background(), `SELECT t.success,t.request_status,b.request_delta->0->>'content' FROM public.session_turns_hot t JOIN public.session_bodies_hot b USING(tenant_id,request_id,partition_date) WHERE t.request_id=$1`, entry.RequestID).Scan(&success, &storedStatus, &content); err != nil {
					t.Fatal(err)
				}
				if success != tc.success || storedStatus != tc.status || content != "preserve me" {
					t.Fatalf("terminal pair values success=%v status=%q content=%q", success, storedStatus, content)
				}
			}
		})
	}
	t.Run("source-selected-before-live-config-switch", func(t *testing.T) {
		setShadowWriteEnabledForTest(func() bool { return false })
		defer setShadowWriteEnabledForTest(func() bool { return true })
		entry := &telemetry.RequestLogEntry{RequestID: "hook-selected-source", GwSessionID: &session, TenantID: "ingest-tenant", Success: true, RequestBody: &body, EventAt: &now}
		// The durable serialized decision must survive fallback/outbox handoff.
		if err := json.Unmarshal([]byte(`{"session_v2_write_required":true}`), entry); err != nil {
			t.Fatal(err)
		}
		hook(entry)
		if turns, bodies := ingestPairCount(t, db, entry.RequestID); turns != 1 || bodies != 1 {
			t.Fatalf("already-selected V2 fact source lost after live switch: turns=%d bodies=%d", turns, bodies)
		}
	})
	t.Run("normal-request-without-session-header", func(t *testing.T) {
		entry := &telemetry.RequestLogEntry{RequestID: "hook-no-session", TenantID: "ingest-tenant", Success: true, RequestBody: &body, EventAt: &now}
		hook(entry)
		hook(entry)
		if turns, bodies := ingestPairCount(t, db, entry.RequestID); turns != 1 || bodies != 1 {
			t.Fatalf("synthetic normal request turns=%d bodies=%d", turns, bodies)
		}
		var storedSession string
		if err := db.QueryRow(context.Background(), `SELECT session_id FROM public.session_turns_hot WHERE request_id=$1`, entry.RequestID).Scan(&storedSession); err != nil {
			t.Fatal(err)
		}
		if storedSession != SyntheticSessionID(entry) {
			t.Fatalf("normal no-header request used unexpected session %q", storedSession)
		}
	})
	t.Run("cancel-while-waiting-session-lock", func(t *testing.T) {
		req := ingestRequest("lock-cancelled-request")
		lock, err := db.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Rollback(context.Background())
		if _, err := lock.Exec(context.Background(), `SELECT pg_advisory_xact_lock(public.session_turns_advisory_lock_key($1,$2))`, req.TenantID, req.SessionID); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- writer.Write(ctx, req) }()
		deadline := time.Now().Add(3 * time.Second)
		for {
			var waiting bool
			if err := db.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event='advisory' AND query LIKE '%session_turns_advisory_lock_key%')`).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("writer never reached contended session lock")
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("lock-canceled writer succeeded")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("writer ignored canceled lock wait")
		}
		if turns, bodies := ingestPairCount(t, db, req.RequestID); turns != 0 || bodies != 0 {
			t.Fatalf("canceled lock waiter turns=%d bodies=%d", turns, bodies)
		}
		if err := lock.Rollback(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := writer.Write(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		if turns, bodies := ingestPairCount(t, db, req.RequestID); turns != 1 || bodies != 1 {
			t.Fatalf("retry after lock cancellation turns=%d bodies=%d", turns, bodies)
		}
	})

	t.Run("canceled-writer-does-not-publish", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		req := ingestRequest("cancelled-request")
		if err := writer.Write(ctx, req); err == nil {
			t.Fatal("canceled writer unexpectedly succeeded")
		}
		if turns, bodies := ingestPairCount(t, db, req.RequestID); turns != 0 || bodies != 0 {
			t.Fatalf("canceled write turns=%d bodies=%d", turns, bodies)
		}
		if err := writer.Write(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		if turns, bodies := ingestPairCount(t, db, req.RequestID); turns != 1 || bodies != 1 {
			t.Fatalf("retry turns=%d bodies=%d", turns, bodies)
		}
	})
}

// A correct ledger fingerprint is evidence of delivery, not the current physical
// schema. An upgrade may repair this state or refuse it; success while kind is
// still absent is forbidden even when every ledger marker matches.
func TestSessionIngestRevisionChannelRejectsPhysicalDriftBehindMarkers(t *testing.T) {
	db := ingestFixture(t)
	ingestExec(t, db, `CREATE TABLE public.session_summaries(session_key text)`)
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "scripts", "apply-db-revision-sequence.sh")
	if override := os.Getenv("TEST_SESSION_INGEST_REVISION_SCRIPT"); override != "" {
		script = override
	}
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	matches := regexp.MustCompile(`(?m)^\s*"\$ROOT_DIR/([^"\n]+)"`).FindAllStringSubmatch(string(data), -1)
	if len(matches) < 200 {
		t.Fatalf("revision fixture found only %d migration entries", len(matches))
	}
	for _, match := range matches {
		bytes, err := os.ReadFile(filepath.Join(root, match[1]))
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(bytes)
		marker := "session-summary-and-integrity-2026-09:" + filepath.Base(match[1])
		if _, err := db.Exec(context.Background(), `INSERT INTO public.gateway_db_revision_sequences(sequence_name,content_sha256) VALUES($1,$2) ON CONFLICT(sequence_name) DO UPDATE SET content_sha256=EXCLUDED.content_sha256`, marker, hex.EncodeToString(hash[:])); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", script)
	cmd.Env = append(os.Environ(), "DATABASE_URL="+os.Getenv("TEST_SESSION_INGEST_DATABASE_URL"))
	if _, err := exec.LookPath("psql"); err != nil {
		container := os.Getenv("TEST_SESSION_INGEST_PG_CONTAINER")
		if container == "" {
			t.Fatal("revision-channel real DB test requires psql or TEST_SESSION_INGEST_PG_CONTAINER")
		}
		cfg, err := pgxpool.ParseConfig(os.Getenv("TEST_SESSION_INGEST_DATABASE_URL"))
		if err != nil {
			t.Fatal("invalid isolated DB configuration")
		}
		cmd.Env = append(cmd.Env, "LLM_GATEWAY_PG_CONTAINER="+container, "LLM_GATEWAY_PG_USER=postgres", "LLM_GATEWAY_PG_DATABASE="+cfg.ConnConfig.Database, "LLM_GATEWAY_PG_PASSWORD=isolated-test")
	}
	output, runErr := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("revision channel timed out")
	}
	if runErr != nil {
		if !strings.Contains(string(output), "session_bodies") || !strings.Contains(string(output), "kind") {
			t.Fatalf("channel failed before physical postcondition: %s", output)
		}
		return
	}
	var present bool
	if err := db.QueryRow(context.Background(), `SELECT count(*)=3 FROM information_schema.columns WHERE table_schema='public' AND table_name IN ('session_bodies','session_bodies_hot','session_bodies_unified') AND column_name='kind'`).Scan(&present); err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatal("revision channel claimed success with correct ledger fingerprints but missing physical session_bodies kind contract")
	}
}

func TestSessionIngestReconcilePreservesLegacyRowsAndRejectsContractDrift(t *testing.T) {
	db := ingestFixture(t)
	ingestExec(t, db, `ALTER TABLE public.sessions ADD COLUMN last_full_request jsonb;
 INSERT INTO public.sessions(session_id,tenant_id,last_full_request)
 VALUES('legacy-session','ingest-tenant','{"keep":9007199254740993}');
 INSERT INTO public.session_bodies(session_id,turn_no,tenant_id,request_id,request_delta)
 VALUES('legacy-session',1,'ingest-tenant','legacy-parent','[{"role":"user","content":"parent survives"}]');
 INSERT INTO public.session_bodies_hot(session_id,turn_no,tenant_id,request_id,request_delta)
 VALUES('legacy-session',2,'ingest-tenant','legacy-hot','[{"role":"user","content":"hot survives"}]');`)
	ingestMigration(t, db, ingestReconcileMigration)
	ingestMigration(t, db, ingestReconcileMigration)
	var preserved bool
	if err := db.QueryRow(context.Background(), `SELECT
 (SELECT last_full_request->>'keep'='9007199254740993' FROM public.sessions WHERE session_id='legacy-session')
 AND (SELECT count(*)=2 FROM public.session_bodies_unified WHERE session_id='legacy-session' AND kind='turn_delta' AND request_delta->0->>'content' IN ('parent survives','hot survives'))`).Scan(&preserved); err != nil {
		t.Fatal(err)
	}
	if !preserved {
		t.Fatal("reconcile removed legacy payload or hid same-day parent/hot bodies")
	}
	for name, sql := range map[string]string{
		"nullable-kind":        `ALTER TABLE public.session_bodies_hot ALTER COLUMN kind DROP NOT NULL`,
		"wrong-default":        `ALTER TABLE public.session_bodies_hot ALTER COLUMN kind SET DEFAULT 'final_full'`,
		"missing-unique-index": `DROP INDEX public.uq_session_bodies_hot_final_full`,
	} {
		t.Run(name, func(t *testing.T) {
			tx, err := db.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, err := tx.Exec(context.Background(), sql); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(context.Background(), `SELECT public.assert_session_bodies_kind_contract()`); err == nil {
				t.Fatal("physical postcondition accepted drift")
			}
		})
	}
}
