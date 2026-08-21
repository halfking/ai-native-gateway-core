//go:build integration

package startup

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestMigration552DurableObservationOutboxIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("requestjourney"),
		postgres.WithUsername("requestjourney"),
		postgres.WithPassword("requestjourney"),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	defer func() {
		terminateCtx, terminateCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer terminateCancel()
		if err := container.Terminate(terminateCtx); err != nil {
			t.Errorf("terminate postgres: %v", err)
		}
	}()

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	var conn *pgx.Conn
	for attempt := 0; attempt < 30; attempt++ {
		conn, err = pgx.ConnectConfig(ctx, cfg)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	exec := func(sql string) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatalf("exec failed: %v\nSQL: %s", err, sql)
		}
	}
	execFile := func(name string) {
		t.Helper()
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		exec(string(body))
	}
	for _, name := range []string{
		"511_state_transitions_table.sql",
		"515_state_transitions_seq_unique.sql",
		"521_repair_state_transitions_tenant.sql",
		"530_request_journey_contract.sql",
		"531_request_journey_tenant_uniqueness.sql",
		"552_request_journey_durable_outbox.sql",
	} {
		execFile(name)
	}
	execFile("552_request_journey_durable_outbox.sql")

	var rls, forced bool
	if err := conn.QueryRow(ctx, `SELECT relrowsecurity, relforcerowsecurity
		FROM pg_class WHERE oid='public.request_journey_observation_outbox'::regclass`).Scan(&rls, &forced); err != nil {
		t.Fatal(err)
	}
	if !rls || !forced {
		t.Fatalf("outbox RLS state = enabled:%t forced:%t, want both true", rls, forced)
	}

	exec(`CREATE ROLE requestjourney_552_rls NOLOGIN NOSUPERUSER NOBYPASSRLS;
		GRANT USAGE ON SCHEMA public TO requestjourney_552_rls;
		GRANT SELECT, INSERT, UPDATE, DELETE ON public.request_journey_observation_outbox TO requestjourney_552_rls;
		GRANT USAGE, SELECT ON SEQUENCE public.request_journey_observation_outbox_id_seq TO requestjourney_552_rls;
		BEGIN;
		SELECT set_config('app.bypass_rls', 'true', true);
		INSERT INTO public.request_journey_observation_outbox (tenant_id, request_id, seq, payload, payload_hash)
		VALUES ('tenant-a', 'request-a', 1, '{}'::jsonb, 'hash-a'),
		       ('tenant-b', 'request-b', 1, '{}'::jsonb, 'hash-b');
		COMMIT`)

	exec(`SET ROLE requestjourney_552_rls;
		BEGIN; SELECT set_config('app.current_tenant', 'tenant-a', true)`)
	var tenantRows int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM public.request_journey_observation_outbox`).Scan(&tenantRows); err != nil {
		t.Fatal(err)
	}
	exec("COMMIT")
	if tenantRows != 1 {
		t.Fatalf("tenant-visible outbox rows = %d, want 1", tenantRows)
	}

	_, err = conn.Exec(ctx, `BEGIN;
		SELECT set_config('app.current_tenant', 'tenant-a', true);
		INSERT INTO public.request_journey_observation_outbox (tenant_id, request_id, seq, payload, payload_hash)
		VALUES ('tenant-b', 'cross-tenant', 1, '{}'::jsonb, 'cross');
		COMMIT`)
	if !isMigration552PGCode(err, "42501") {
		t.Fatalf("cross-tenant insert error = %v, want SQLSTATE 42501", err)
	}
	exec("ROLLBACK; RESET ROLE")

	exec(`BEGIN;
		SELECT set_config('app.bypass_rls', 'true', true);
		UPDATE public.request_journey_observation_outbox
		SET status='processing', claim_owner='old-worker', claim_until=now() - interval '1 second',
			claim_fencing_token=claim_fencing_token+1, attempts=attempts+1
		WHERE tenant_id='tenant-a' AND request_id='request-a' AND seq=1;
		COMMIT`)
	exec(`BEGIN;
		SELECT set_config('app.bypass_rls', 'true', true);
		UPDATE public.request_journey_observation_outbox
		SET claim_owner='new-worker', claim_until=now() + interval '1 minute',
			claim_fencing_token=claim_fencing_token+1, attempts=attempts+1
		WHERE tenant_id='tenant-a' AND request_id='request-a' AND seq=1
			AND status='processing' AND claim_until < now();
		COMMIT`)
	exec(`BEGIN; SELECT set_config('app.bypass_rls', 'true', true)`)
	var token int64
	var attempts int
	if err := conn.QueryRow(ctx, `SELECT claim_fencing_token, attempts FROM public.request_journey_observation_outbox
		WHERE tenant_id='tenant-a' AND request_id='request-a' AND seq=1`).Scan(&token, &attempts); err != nil {
		t.Fatal(err)
	}
	exec("COMMIT")
	if token != 2 || attempts != 2 {
		t.Fatalf("reclaimed claim = token:%d attempts:%d, want 2/2", token, attempts)
	}
	exec(`BEGIN; SELECT set_config('app.bypass_rls', 'true', true)`)
	var staleRows int
	if err := conn.QueryRow(ctx, `WITH stale AS (
			DELETE FROM public.request_journey_observation_outbox
			WHERE tenant_id='tenant-a' AND request_id='request-a' AND seq=1
				AND claim_owner='old-worker' AND claim_fencing_token=1
				AND status='processing' AND claim_until > now()
			RETURNING id
		) SELECT count(*) FROM stale`).Scan(&staleRows); err != nil {
		t.Fatal(err)
	}
	exec("COMMIT")
	if staleRows != 0 {
		t.Fatalf("stale acknowledgement deleted %d rows, want 0", staleRows)
	}

	_, err = conn.Exec(ctx, `BEGIN; SELECT set_config('app.bypass_rls', 'true', true);
		INSERT INTO public.request_journey_observation_outbox (tenant_id, request_id, seq, payload, payload_hash, status)
		VALUES ('tenant-a', 'invalid-processing', 1, '{}'::jsonb, 'invalid', 'processing'); COMMIT`)
	if !isMigration552PGCode(err, "23514") {
		t.Fatalf("ownerless processing row error = %v, want SQLSTATE 23514", err)
	}
	exec("ROLLBACK")

	_, err = conn.Exec(ctx, readMigration552(t, "552_request_journey_durable_outbox.down.sql"))
	if err == nil {
		t.Fatal("down migration succeeded with durable outbox rows")
	}
	exec("ROLLBACK")
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT to_regclass('public.request_journey_observation_outbox') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("failed down migration removed the outbox table")
	}

	exec(`BEGIN; SELECT set_config('app.bypass_rls', 'true', true);
		DELETE FROM public.request_journey_observation_outbox; COMMIT`)
	exec(readMigration552(t, "552_request_journey_durable_outbox.down.sql"))
	if err := conn.QueryRow(ctx, `SELECT to_regclass('public.request_journey_observation_outbox') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("successful down migration retained the outbox table")
	}
	var retryAtExists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema='public' AND table_name='request_state_transitions' AND column_name='retry_at'
	)`).Scan(&retryAtExists); err != nil {
		t.Fatal(err)
	}
	if retryAtExists {
		t.Fatal("successful down migration retained retry_at")
	}
}

func readMigration552(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func isMigration552PGCode(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}
