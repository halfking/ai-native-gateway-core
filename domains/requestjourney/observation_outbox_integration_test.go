//go:build integration

package requestjourney

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// setupIntegrationOutboxEnv builds a real PostgreSQL pool from TEST_DATABASE_URL
// (skipping if absent) and verifies that the durable outbox schema is reachable.
// Returns the pool (as superuser validator), an optional Redis client built from
// TEST_REDIS_ADDR (also skippable), and a cleanup function that drops any
// leftover outbox rows + releases connections.
func setupIntegrationOutboxEnv(t *testing.T) (*pgxpool.Pool, *redis.Client, string, func()) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping durable observation outbox integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		t.Fatalf("test database unreachable: %v", err)
	}
	var ok bool
	if err := pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables
		   WHERE table_name = 'request_journey_observation_outbox')`).Scan(&ok); err != nil {
		pool.Close()
		t.Fatalf("probe outbox table: %v", err)
	}
	if !ok {
		pool.Close()
		t.Skip("request_journey_observation_outbox missing; apply migrations 511..552 first")
	}

	// Derive a parallel non-bypass-pool DSN by swapping the user. The
	// validator superuser pool above bypasses RLS even with FORCE; the
	// non-bypass pool is required to assert RLS isolation.
	tenantDSN := os.Getenv("TEST_TENANT_DATABASE_URL")
	var redisClient *redis.Client
	if addr := os.Getenv("TEST_REDIS_ADDR"); addr != "" {
		redisClient = redis.NewClient(&redis.Options{Addr: addr})
		if err := redisClient.Ping(context.Background()).Err(); err != nil {
			pool.Close()
			_ = redisClient.Close()
			t.Skipf("TEST_REDIS_ADDR unreachable: %v", err)
		}
	}

	cleanup := func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM request_journey_observation_outbox
			   WHERE tenant_id LIKE 'rj-it-%'`)
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM request_state_transitions
			   WHERE tenant_id LIKE 'rj-it-%'`)
		pool.Close()
		if redisClient != nil {
			_ = redisClient.Close()
		}
	}
	return pool, redisClient, tenantDSN, cleanup
}

// refreshLease writes a fresh claim_until on the row so tests can simulate a
// still-active lease without depending on real-time clock injection.
func refreshLease(t *testing.T, pool *pgxpool.Pool, id int64, lease time.Duration) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE request_journey_observation_outbox
		    SET claim_until = now() + $2::interval
		  WHERE id = $1`, id, lease); err != nil {
		t.Fatalf("refreshLease: %v", err)
	}
}

// TestObservationOutbox_TenantRLSIsolation proves that the
// request_journey_observation_outbox RLS policy hides rows from non-bypass
// callers without the matching app.current_tenant GUC set.
func TestObservationOutbox_TenantRLSIsolation(t *testing.T) {
	pool, _, tenantDSN, cleanup := setupIntegrationOutboxEnv(t)
	defer cleanup()
	if tenantDSN == "" {
		t.Skip("TEST_TENANT_DATABASE_URL not set; skipping RLS isolation probe (it requires a NOBYPASSRLS role)")
	}

	ctx := context.Background()

	// Seed two rows via the superuser pool (RLS bypass via app.bypass_rls).
	outbox := newObservationOutbox(pool, NewPostgresRepository(pool), nil, "worker-it-rls")
	outbox.clock = func() time.Time { return time.Now().UTC() }
	if err := outbox.Enqueue(ctx, testJourneyEvent("rj-it-alpha", "request-rls-1", 1)); err != nil {
		t.Fatalf("enqueue alpha: %v", err)
	}
	if err := outbox.Enqueue(ctx, testJourneyEvent("rj-it-beta", "request-rls-1", 1)); err != nil {
		t.Fatalf("enqueue beta: %v", err)
	}

	tenantPool, err := pgxpool.New(ctx, tenantDSN)
	if err != nil {
		t.Fatalf("tenant pool: %v", err)
	}
	defer tenantPool.Close()

	tx, err := tenantPool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin rls-probe: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', 'rj-it-alpha', true)`); err != nil {
		t.Fatalf("set tenant alpha: %v", err)
	}
	var alphaCount, betaCount int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE tenant_id='rj-it-alpha'),
		         count(*) FILTER (WHERE tenant_id='rj-it-beta')
		   FROM request_journey_observation_outbox`).Scan(&alphaCount, &betaCount); err != nil {
		t.Fatalf("alpha rls probe: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit rls-probe: %v", err)
	}
	if alphaCount != 1 {
		t.Fatalf("alpha scope rows = %d, want 1", alphaCount)
	}
	if betaCount != 0 {
		t.Fatalf("alpha scope saw %d beta rows, want 0 (RLS leak)", betaCount)
	}

	// Sanity: same non-bypass role with NO app.current_tenant set sees
	// nothing (default GUC returns '' which never matches any tenant_id).
	tx2, err := tenantPool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin unset probe: %v", err)
	}
	var totalUnset int
	if err := tx2.QueryRow(ctx,
		`SELECT count(*) FROM request_journey_observation_outbox`).Scan(&totalUnset); err != nil {
		t.Fatalf("unset probe: %v", err)
	}
	if err := tx2.Commit(ctx); err != nil {
		t.Fatalf("commit unset probe: %v", err)
	}
	if totalUnset != 0 {
		t.Fatalf("unset GUC saw %d rows, want 0 (RLS must require app.current_tenant)", totalUnset)
	}
}

// TestObservationOutbox_WorkerClaimAndAckDrainsOutbox uses real-PG RLS bypass
// (the outbox itself sets app.bypass_rls=true per-transaction) and confirms a
// single worker drains both tenants' pending rows.
func TestObservationOutbox_WorkerClaimAndAckDrainsOutbox(t *testing.T) {
	pool, _, _, cleanup := setupIntegrationOutboxEnv(t)
	defer cleanup()

	ctx := context.Background()
	outbox := newObservationOutbox(pool, NewPostgresRepository(pool), nil, "worker-it-drain")
	outbox.clock = func() time.Time { return time.Now().UTC() }

	if err := outbox.Enqueue(ctx, testJourneyEvent("rj-it-drain-a", "r-drain-1", 1)); err != nil {
		t.Fatalf("enqueue a: %v", err)
	}
	if err := outbox.Enqueue(ctx, testJourneyEvent("rj-it-drain-b", "r-drain-1", 1)); err != nil {
		t.Fatalf("enqueue b: %v", err)
	}

	outbox.RunOnce(ctx)

	var remaining int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM request_journey_observation_outbox
		   WHERE tenant_id LIKE 'rj-it-drain-%'`).Scan(&remaining); err != nil {
		t.Fatalf("count remaining: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("worker did not drain: remaining=%d", remaining)
	}
}

// TestObservationOutbox_StaleFencingRejectsAckRelease forces a row into the
// 'processing' state, then issues the ack/release UPDATE directly with the
// PREVIOUS claim_fencing_token. Both must affect 0 rows.
func TestObservationOutbox_StaleFencingRejectsAckRelease(t *testing.T) {
	pool, _, _, cleanup := setupIntegrationOutboxEnv(t)
	defer cleanup()

	ctx := context.Background()
	outbox := newObservationOutbox(pool, NewPostgresRepository(pool), nil, "worker-it-fence")
	outbox.clock = func() time.Time { return time.Now().UTC() }
	if err := outbox.Enqueue(ctx, testJourneyEvent("rj-it-fence", "request-fence-1", 1)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	claims, err := outbox.claim(ctx, "rj-it-fence", "request-fence-1", 1, false)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if len(claims) != 1 || claims[0].ClaimFencingToken != 1 {
		t.Fatalf("first claim token = %+v, want token=1", claims)
	}

	// Stale release with token=0 must reject.
	tag, err := pool.Exec(ctx,
		`UPDATE request_journey_observation_outbox
		    SET status='failed', claim_owner=NULL, claim_until=NULL,
		        last_error=$4, next_retry_at=$5, updated_at=$6
		  WHERE id=$1 AND claim_owner=$2 AND claim_fencing_token=$3
		    AND status='processing' AND claim_until > now()`,
		claims[0].ID, claims[0].Owner, int64(0), "stale-release",
		time.Now().UTC().Add(time.Minute), time.Now().UTC())
	if err != nil {
		t.Fatalf("stale release exec: %v", err)
	}
	if tag.RowsAffected() != 0 {
		t.Fatalf("stale release affected %d rows, want 0", tag.RowsAffected())
	}

	// Stale ack with token=0 must reject.
	tag, err = pool.Exec(ctx,
		`DELETE FROM request_journey_observation_outbox
		  WHERE id=$1 AND claim_owner=$2 AND claim_fencing_token=$3
		    AND status='processing' AND claim_until > now()`,
		claims[0].ID, claims[0].Owner, int64(0))
	if err != nil {
		t.Fatalf("stale ack exec: %v", err)
	}
	if tag.RowsAffected() != 0 {
		t.Fatalf("stale ack affected %d rows, want 0", tag.RowsAffected())
	}

	var status string
	var fence int64
	if err := pool.QueryRow(ctx,
		`SELECT status, claim_fencing_token FROM request_journey_observation_outbox WHERE id=$1`,
		claims[0].ID).Scan(&status, &fence); err != nil {
		t.Fatalf("read row after stale ops: %v", err)
	}
	if status != "processing" || fence != 1 {
		t.Fatalf("row mutated by stale ops: status=%s fence=%d", status, fence)
	}

	// Recovery: Replay(force=true) bumps fence to 2 and then deliver drains.
	claims2, err := outbox.claim(ctx, "rj-it-fence", "request-fence-1", 1, true)
	if err != nil {
		t.Fatalf("recovery claim: %v", err)
	}
	if len(claims2) != 1 || claims2[0].ClaimFencingToken != 2 {
		t.Fatalf("recovery claim token = %+v, want token=2", claims2)
	}
	if err := outbox.deliver(ctx, claims2[0]); err != nil {
		t.Fatalf("recovered deliver: %v", err)
	}
}

// TestObservationOutbox_RedisFailureReplay proves that when the post-pg
// Redis projection fails, release() re-queues the row; the next worker
// reclaims it and the eventual projection is idempotent (single row in
// request_state_transitions).
func TestObservationOutbox_RedisFailureReplay(t *testing.T) {
	pool, redisClient, _, cleanup := setupIntegrationOutboxEnv(t)
	defer cleanup()
	if redisClient == nil {
		t.Skip("TEST_REDIS_ADDR not set; skipping Redis-failure replay test")
	}

	ctx := context.Background()
	store := NewRedisStore(redisClient, DefaultConfig())
	outbox := newObservationOutbox(pool, NewPostgresRepository(pool), store, "worker-it-redis")
	outbox.clock = func() time.Time { return time.Now().UTC() }
	outbox.lease = 30 * time.Second // keep lease in DB future across the test
	outbox.pollInterval = 50 * time.Millisecond

	event := testJourneyEvent("rj-it-redis", "request-redis-1", 1)
	if err := outbox.Enqueue(ctx, event); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	claims, err := outbox.claim(ctx, "rj-it-redis", "request-redis-1", 1, false)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim: %+v err=%v", claims, err)
	}

	// Force a Redis failure by closing the client; deliver must error.
	_ = redisClient.Close()
	if err := outbox.deliver(ctx, claims[0]); err == nil {
		t.Fatal("expected Redis-unavailable error from deliver()")
	}
	if err := outbox.release(ctx, claims[0], errors.New("redis unavailable")); err != nil {
		t.Fatalf("release after redis failure: %v", err)
	}

	var status string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM request_journey_observation_outbox WHERE id=$1`, claims[0].ID).Scan(&status); err != nil {
		t.Fatalf("post-release read: %v", err)
	}
	if status != "failed" {
		t.Fatalf("status after redis-fail release = %q, want 'failed'", status)
	}

	// Reopen Redis; a fresh worker reclaims and replays idempotently.
	addr := redisClient.Options().Addr
	redisClient = redis.NewClient(&redis.Options{Addr: addr})
	if err := redisClient.Ping(ctx).Err(); err != nil {
		t.Fatalf("reopen redis: %v", err)
	}
	store = NewRedisStore(redisClient, DefaultConfig())
	outbox2 := newObservationOutbox(pool, NewPostgresRepository(pool), store, "worker-it-redis-restart")
	outbox2.clock = func() time.Time { return time.Now().UTC() }
	// Wait past release's next_retry_at (default 250ms).
	time.Sleep(300 * time.Millisecond)
	claims2, err := outbox2.claim(ctx, "rj-it-redis", "request-redis-1", 1, false)
	if err != nil || len(claims2) != 1 {
		t.Fatalf("replay claim: %+v err=%v", claims2, err)
	}
	if err := outbox2.deliver(ctx, claims2[0]); err != nil {
		t.Fatalf("replay deliver: %v", err)
	}
	var remaining int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM request_journey_observation_outbox WHERE tenant_id='rj-it-redis'`).Scan(&remaining); err != nil {
		t.Fatalf("post-replay count: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("replay did not drain outbox: remaining=%d", remaining)
	}
	var pgRows int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM request_state_transitions
		   WHERE tenant_id='rj-it-redis' AND request_id='request-redis-1' AND seq=1`).Scan(&pgRows); err != nil {
		t.Fatalf("pg projection count: %v", err)
	}
	if pgRows != 1 {
		t.Fatalf("pg projection rows = %d, want 1 (idempotent replay)", pgRows)
	}
	_ = redisClient.Close()
}

// TestObservationOutbox_LeaseExpiryReclaim asserts that an expired lease can
// be reclaimed by a new worker process even when the original owner is still
// recorded.
func TestObservationOutbox_LeaseExpiryReclaim(t *testing.T) {
	pool, _, _, cleanup := setupIntegrationOutboxEnv(t)
	defer cleanup()

	ctx := context.Background()
	outboxA := newObservationOutbox(pool, NewPostgresRepository(pool), nil, "worker-it-lease-A")
	outboxA.clock = func() time.Time { return time.Now().UTC() }
	outboxA.lease = 100 * time.Millisecond
	if err := outboxA.Enqueue(ctx, testJourneyEvent("rj-it-lease", "request-lease-1", 1)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	claimsA, err := outboxA.claim(ctx, "rj-it-lease", "request-lease-1", 1, false)
	if err != nil || len(claimsA) != 1 {
		t.Fatalf("workerA claim: %+v err=%v", claimsA, err)
	}

	// Manually expire the lease (simulating real-time advance past 100ms).
	if _, err := pool.Exec(ctx,
		`UPDATE request_journey_observation_outbox
		    SET claim_until = now() - interval '1 second'
		  WHERE id = $1`, claimsA[0].ID); err != nil {
		t.Fatalf("expire lease: %v", err)
	}

	outboxB := newObservationOutbox(pool, NewPostgresRepository(pool), nil, "worker-it-lease-B")
	outboxB.clock = func() time.Time { return time.Now().UTC() }
	claimsB, err := outboxB.claim(ctx, "rj-it-lease", "request-lease-1", 1, false)
	if err != nil || len(claimsB) != 1 {
		t.Fatalf("workerB claim after lease expiry: %+v err=%v", claimsB, err)
	}
	if claimsB[0].ClaimFencingToken != 2 {
		t.Fatalf("reclaim fence = %d, want 2", claimsB[0].ClaimFencingToken)
	}
	if claimsB[0].Owner != "worker-it-lease-B" {
		t.Fatalf("reclaim owner = %q, want worker-it-lease-B", claimsB[0].Owner)
	}
	// Refresh lease so deliver's claim_until > now() check still passes.
	refreshLease(t, pool, claimsB[0].ID, 30*time.Second)
	if err := outboxB.deliver(ctx, claimsB[0]); err != nil {
		t.Fatalf("reclaim deliver: %v", err)
	}
}

// TestObservationOutbox_DownMigrationShape verifies that the 552 down SQL
// does NOT silently drain the outbox. Operators must drain first.
func TestObservationOutbox_DownMigrationShape(t *testing.T) {
	_, _, _, cleanup := setupIntegrationOutboxEnv(t)
	defer cleanup()
	downPath := os.Getenv("RJ_PGVAL_DOWN_SQL")
	if downPath == "" {
		t.Skip("RJ_PGVAL_DOWN_SQL not set; skipping down-migration contract probe")
	}
	body, err := os.ReadFile(downPath)
	if err != nil {
		t.Fatalf("read down sql: %v", err)
	}
	text := string(body)
	for _, forbidden := range []string{"DELETE FROM request_journey_observation_outbox", "TRUNCATE request_journey_observation_outbox"} {
		if contains(text, forbidden) {
			t.Fatalf("down migration must NOT silently empty the outbox: found %q", forbidden)
		}
	}
	// Confirm the down contains the documented fail-closed shape.
	for _, expected := range []string{"DROP POLICY IF EXISTS request_journey_observation_outbox_tenant_isolation", "DROP TABLE IF EXISTS request_journey_observation_outbox"} {
		if !contains(text, expected) {
			t.Fatalf("down migration missing expected marker: %q", expected)
		}
	}
}

// TestObservationOutbox_RetryAtRoundTripParity writes retry_at on a
// retry_scheduled event (allowed) and rejects it on a non-retry event.
func TestObservationOutbox_RetryAtRoundTripParity(t *testing.T) {
	pool, _, _, cleanup := setupIntegrationOutboxEnv(t)
	defer cleanup()

	ctx := context.Background()
	// Disallowed: retry_at on a non-retry_scheduled event.
	if _, err := pool.Exec(ctx,
		`INSERT INTO request_state_transitions
		    (tenant_id, gateway_instance_id, request_id, seq, event_type, stage,
		     observation_status, occurred_at, retry_at)
		 VALUES ('rj-it-retry', 'gw-1', 'request-retry-1', 1,
		         'received', 'received', 'complete', now(), now())`); err == nil {
		t.Fatal("expected check constraint violation for retry_at on non-retry_scheduled event")
	}
	// Allowed: retry_at on retry_scheduled.
	if _, err := pool.Exec(ctx,
		`INSERT INTO request_state_transitions
		    (tenant_id, gateway_instance_id, request_id, seq, event_type, stage,
		     observation_status, occurred_at, retry_at)
		 VALUES ('rj-it-retry', 'gw-1', 'request-retry-1', 2,
		         'retry_scheduled', 'retrying', 'complete', now(), now())`); err != nil {
		t.Fatalf("retry_scheduled retry_at insert: %v", err)
	}
}

func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// Compile-time confirmation: pgxpool.Pool satisfies observationOutboxDB.
var _ observationOutboxDB = (*pgxpool.Pool)(nil)
