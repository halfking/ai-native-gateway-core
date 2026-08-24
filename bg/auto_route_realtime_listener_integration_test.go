//go:build integration

// AutoRouteRealtimeListener — real PostgreSQL LISTEN/NOTIFY end-to-end.
//
// The unit tests in auto_route_realtime_listener_test.go exercise the
// listener's debounce / cancellation / lifecycle semantics with a fake
// refresher and no real database. Those tests catch logical regressions
// inside the listener itself, but they cannot catch:
//
//   - LISTEN registration regressions (e.g. wrong channel name, missing
//     EXEC permission on the connection)
//   - WaitForNotification never returning after a real pg_notify
//   - Connection-release bugs that only manifest after a real Acquire
//     / Exec / Release cycle
//   - Trigger / notify_auto_route_refresh() contract drift (the trigger
//     payload format the listener must cope with)
//
// This file spins up a real PostgreSQL via testcontainers, installs the
// credentials table + notify_auto_route_refresh() function + the
// trg_notify_auto_route_creds trigger (the exact DDL the production
// baseline relies on), and asserts the listener observes and coalesces
// real NOTIFY events end-to-end.
//
// Run with:
//
//	go test -tags=integration -timeout 5m -count=1 -run TestAutoRouteRealtimeListener_Integration ./bg
//
// Optional: set TEST_PG_URL to point at an external Postgres (matches
// the convention used by domains/hooks/handoff/migration_527_*).
package bg

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// autoRouteListenerContainer returns an isolated Postgres pool with the
// minimal credentials schema + auto_route_refresh trigger installed. The
// returned cleanup closes the pool and terminates the container (or just
// the pool when TEST_PG_URL is supplied).
//
// pgxpool.New does a single ping during construction; on Docker Desktop
// the mapped TCP socket occasionally resets the first probe while the
// container's "ready" log fires before postgres fully accepts
// connections. Retry on connection-level errors until either we succeed
// or the context expires (matches the convention used by
// domains/hooks/handoff/migration_527_testcontainers_integration_test.go).
func autoRouteListenerContainer(t *testing.T, ctx context.Context) (*pgxpool.Pool, func()) {
	t.Helper()
	openPool := func(dsn string) (*pgxpool.Pool, error) {
		var (
			pool *pgxpool.Pool
			err  error
		)
		for attempt := 0; attempt < 30; attempt++ {
			pool, err = pgxpool.New(ctx, dsn)
			if err == nil {
				pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
				pingErr := pool.Ping(pingCtx)
				pingCancel()
				if pingErr == nil {
					return pool, nil
				}
				pool.Close()
				err = pingErr
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Second):
			}
		}
		return nil, err
	}

	if dsn := os.Getenv("TEST_PG_URL"); dsn != "" {
		pool, err := openPool(dsn)
		if err != nil {
			t.Fatalf("connect TEST_PG_URL: %v", err)
		}
		mustExec(t, ctx, pool, autoRouteListenerSchema)
		return pool, func() { pool.Close() }
	}
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("auto_route_listener"),
		postgres.WithUsername("auto_route_listener"),
		postgres.WithPassword("auto_route_listener"),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("connection string: %v", err)
	}
	pool, err := openPool(dsn)
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("pgxpool.New: %v", err)
	}
	mustExec(t, ctx, pool, autoRouteListenerSchema)
	cleanup := func() {
		pool.Close()
		terminateCtx, terminateCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer terminateCancel()
		if err := container.Terminate(terminateCtx); err != nil {
			t.Errorf("terminate postgres: %v", err)
		}
	}
	return pool, cleanup
}

// autoRouteListenerSchema mirrors the production baseline's credentials
// trigger definition (see deploy/sql/schemas/baseline/01-schema.sql:27802
// and the notify_auto_route_refresh() function body). Only the columns
// the trigger inspects are required.
const autoRouteListenerSchema = `
CREATE TABLE public.credentials (
	id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	tenant_id TEXT NOT NULL DEFAULT 't0',
	provider_id BIGINT NOT NULL DEFAULT 1,
	raw_model TEXT NOT NULL DEFAULT 'm0',
	status TEXT NOT NULL DEFAULT 'active',
	availability_state TEXT NOT NULL DEFAULT 'available',
	quota_state TEXT NOT NULL DEFAULT 'ok',
	circuit_state TEXT NOT NULL DEFAULT 'closed',
	concurrency_limit INT NOT NULL DEFAULT 8,
	lifecycle_status TEXT NOT NULL DEFAULT 'live',
	manual_disabled BOOLEAN NOT NULL DEFAULT false
);

CREATE OR REPLACE FUNCTION public.notify_auto_route_refresh()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
	entity_id TEXT := '';
BEGIN
	IF TG_TABLE_NAME = 'credential_model_bindings' THEN
		entity_id := COALESCE(NEW.credential_id, OLD.credential_id)::TEXT;
	ELSIF TG_TABLE_NAME IN ('credentials', 'api_keys', 'providers') THEN
		entity_id := COALESCE(NEW.id, OLD.id)::TEXT;
	END IF;
	PERFORM pg_notify('auto_route_refresh', TG_TABLE_NAME || ':' || TG_OP || ':' || entity_id);
	RETURN COALESCE(NEW, OLD);
END;
$$;

CREATE TRIGGER trg_notify_auto_route_creds
AFTER UPDATE OF status, availability_state, quota_state, circuit_state,
	concurrency_limit, lifecycle_status, manual_disabled
ON public.credentials
FOR EACH ROW WHEN (OLD.* IS DISTINCT FROM NEW.*)
EXECUTE FUNCTION public.notify_auto_route_refresh();
`

func mustExec(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string) {
	t.Helper()
	if _, err := pool.Exec(ctx, sql); err != nil {
		t.Fatalf("exec schema: %v", err)
	}
}

// seedCredential inserts a baseline row so the UPDATE in each test has
// something to mutate (the trigger fires only when OLD.* IS DISTINCT
// FROM NEW.*).
func seedCredential(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(ctx, `INSERT INTO public.credentials DEFAULT VALUES RETURNING id`).Scan(&id); err != nil {
		t.Fatalf("seed credentials: %v", err)
	}
	return id
}

// TestAutoRouteRealtimeListener_IntegrationTriggerFiresRefreshOnce wires
// the listener against a real Postgres, mutates the credentials row, and
// asserts the debounced refresh runs exactly once after the trailing
// edge.
func TestAutoRouteRealtimeListener_IntegrationTriggerFiresRefreshOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, cleanup := autoRouteListenerContainer(t, ctx)
	defer cleanup()

	fake := &fakeRefresher{}
	l := NewAutoRouteRealtimeListener(pool, fake)
	l.debounceWindow = 100 * time.Millisecond

	l.Start(ctx)
	defer l.Stop()

	seedCredential(t, ctx, pool)

	// UPDATE a trigger-observed column so the real trg_notify_auto_route_creds
	// fires pg_notify('auto_route_refresh', ...).
	if _, err := pool.Exec(ctx, `UPDATE public.credentials SET status='cooling' WHERE id=$1`, seedCredential(t, ctx, pool)); err != nil {
		t.Fatalf("update credentials: %v", err)
	}

	if !waitFor(t, 2*time.Second, func() bool { return fake.count() >= 1 }) {
		t.Fatalf("real NOTIFY never reached refresher; calls=%d", fake.count())
	}
	// Trailing edge: no second burst should follow.
	time.Sleep(3 * l.debounceWindow)
	if got := fake.count(); got != 1 {
		t.Fatalf("single NOTIFY produced %d refreshes, want exactly 1", got)
	}
	if pending := l.PendingRefreshes(); pending != 0 {
		t.Fatalf("pending flag not cleared after real refresh; PendingRefreshes=%d", pending)
	}
}

// TestAutoRouteRealtimeListener_IntegrationBurstCoalescesInRealListen
// verifies the trailing-edge debounce holds against a real burst of
// pg_notify events (5 UPDATEs in tight succession).
func TestAutoRouteRealtimeListener_IntegrationBurstCoalescesInRealListen(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, cleanup := autoRouteListenerContainer(t, ctx)
	defer cleanup()

	fake := &fakeRefresher{}
	l := NewAutoRouteRealtimeListener(pool, fake)
	l.debounceWindow = 150 * time.Millisecond

	l.Start(ctx)
	defer l.Stop()

	id := seedCredential(t, ctx, pool)

	// 5 rapid UPDATEs, each mutating a different trigger-observed column so
	// every statement fires its own pg_notify.
	burst := []string{
		`UPDATE public.credentials SET status='cooling' WHERE id=$1`,
		`UPDATE public.credentials SET availability_state='degraded' WHERE id=$1`,
		`UPDATE public.credentials SET quota_state='warning' WHERE id=$1`,
		`UPDATE public.credentials SET circuit_state='half_open' WHERE id=$1`,
		`UPDATE public.credentials SET manual_disabled=true WHERE id=$1`,
	}
	for _, stmt := range burst {
		if _, err := pool.Exec(ctx, stmt, id); err != nil {
			t.Fatalf("update credentials: %v\nSQL: %s", err, stmt)
		}
	}

	if !waitFor(t, 2*time.Second, func() bool { return fake.count() >= 1 }) {
		t.Fatalf("real burst never reached refresher; calls=%d", fake.count())
	}
	// Trailing edge: only the last event of the burst should have fired a
	// refresh; any subsequent burst would have arrived at least one
	// debounceWindow later.
	time.Sleep(3 * l.debounceWindow)
	if got := fake.count(); got != 1 {
		t.Fatalf("burst produced %d refreshes, want exactly 1 (trailing edge)", got)
	}
}

// TestAutoRouteRealtimeListener_IntegrationStopReturnsPromptlyAfterRealListen
// asserts the listener releases its LISTEN connection on Stop so the
// caller can close the pool without "conn busy" errors.
func TestAutoRouteRealtimeListener_IntegrationStopReturnsPromptlyAfterRealListen(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, cleanup := autoRouteListenerContainer(t, ctx)
	defer cleanup()

	fake := &fakeRefresher{}
	l := NewAutoRouteRealtimeListener(pool, fake)
	l.debounceWindow = 50 * time.Millisecond

	l.Start(ctx)

	id := seedCredential(t, ctx, pool)
	if _, err := pool.Exec(ctx, `UPDATE public.credentials SET status='cooling' WHERE id=$1`, id); err != nil {
		t.Fatalf("update credentials: %v", err)
	}
	if !waitFor(t, 2*time.Second, func() bool { return fake.count() >= 1 }) {
		t.Fatalf("real NOTIFY never reached refresher before Stop; calls=%d", fake.count())
	}

	done := make(chan struct{})
	go func() { l.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop after real LISTEN session did not return within 2s")
	}

	// Repeated Stop must remain a no-op.
	done2 := make(chan struct{})
	go func() { l.Stop(); close(done2) }()
	select {
	case <-done2:
	case <-time.After(2 * time.Second):
		t.Fatal("repeated Stop after real LISTEN session deadlocked")
	}

	// The LISTEN connection must be released; the pool must close cleanly.
	poolCloseDone := make(chan struct{})
	go func() { pool.Close(); close(poolCloseDone) }()
	select {
	case <-poolCloseDone:
	case <-time.After(2 * time.Second):
		t.Fatal("pool.Close() blocked after Stop — LISTEN connection not released")
	}
}

// TestAutoRouteRealtimeListener_IntegrationContextCancelStopsPendingRefresh
// ensures that cancelling the parent context supplied to Start cancels
// the in-flight debounce loop and prevents a pending refresh from
// running.
func TestAutoRouteRealtimeListener_IntegrationContextCancelStopsPendingRefresh(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, cleanup := autoRouteListenerContainer(t, ctx)
	defer cleanup()

	fake := &fakeRefresher{}
	l := NewAutoRouteRealtimeListener(pool, fake)
	l.debounceWindow = 500 * time.Millisecond

	parentCtx, parentCancel := context.WithCancel(context.Background())
	l.Start(parentCtx)

	id := seedCredential(t, ctx, pool)
	if _, err := pool.Exec(ctx, `UPDATE public.credentials SET status='cooling' WHERE id=$1`, id); err != nil {
		t.Fatalf("update credentials: %v", err)
	}
	// Wait long enough for WaitForNotification to consume the event and
	// for handleNotification to arm the debounce timer, but short enough
	// that the debounceWindow has not elapsed.
	time.Sleep(150 * time.Millisecond)
	if got := fake.count(); got != 0 {
		t.Fatalf("refresh ran before cancel; calls=%d", got)
	}

	parentCancel()
	l.Stop()

	// Give any straggling goroutine more than the debounce window to fire
	// (it must not).
	time.Sleep(2 * l.debounceWindow)
	if got := fake.count(); got != 0 {
		t.Fatalf("cancelled listener still ran %d refreshes, want 0", got)
	}
	if pending := l.PendingRefreshes(); pending != 1 {
		t.Fatalf("pending flag unexpectedly cleared before refresh; PendingRefreshes=%d", pending)
	}
}