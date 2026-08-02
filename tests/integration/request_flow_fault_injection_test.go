//go:build integration

// Package integration holds the real-PostgreSQL / real-Redis fault
// injection tests required by spec §13 BLOCK #8 ("真实 PostgreSQL/Redis
// 故障注入集成测试缺失").
//
// Each scenario in TestFaultInjection exercises one production fault path
// against a live backend (PG or Redis), rather than the pgxmock / miniredis
// doubles the unit tests use. Tests t.Skip when the backend is not reachable
// (TEST_PG_URL / TEST_REDIS_URL absent), so CI without the infrastructure
// never fails.
//
// Run locally:
//
//	TEST_PG_URL=postgres://llm_gateway:llm_gateway@localhost:5432/llm_gateway?sslmode=disable \
//	TEST_REDIS_URL=localhost:6379 \
//	go test -tags=integration ./tests/integration/ -run TestFaultInjection -v -count=1
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/domains/dbdegradation"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	sessionsv2 "github.com/kaixuan/llm-gateway-go/domains/session/v2"
	v2 "github.com/kaixuan/llm-gateway-go/domains/ursm/v2"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/statesource"
)

// defaultPGURL matches the spec convention. The local dev stack runs on
// port 5432 with the llm_gateway superuser; callers override via TEST_PG_URL.
const defaultPGURL = "postgres://maintain:maintain@localhost:55432/llm_gateway?sslmode=disable"

// defaultRedisAddr matches the spec convention ("localhost:55432"); the local
// dev stack runs Redis on 6379, which the helper falls back to.
const defaultRedisAddr = "localhost:55432"

// pgEnvVar is the env var read to decide whether the PG scenarios run.
const pgEnvVar = "TEST_PG_URL"

// redisEnvVar is the env var read to decide whether the Redis scenario runs.
const redisEnvVar = "TEST_REDIS_URL"

// requirePG parses TEST_PG_URL (falling back to the spec default), opens a
// real pgxpool, and returns it together with a teardown. It t.Skips when the
// env var is unset OR the backend is not reachable, so environments without a
// real PG never fail CI.
func requirePG(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	pgURL := os.Getenv(pgEnvVar)
	if pgURL == "" {
		pgURL = defaultPGURL
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg, err := pgxpool.ParseConfig(pgURL)
	if err != nil {
		t.Skipf("%s unparseable, skipping PG integration test: %v", pgEnvVar, err)
	}
	// Keep SimpleProtocol consistent with the happy-path lifecycle test so
	// behaviour matches the production wiring (and the existing suite).
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Skipf("%s unreachable, skipping PG integration test: %v", pgEnvVar, err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("%s ping failed, skipping PG integration test: %v", pgEnvVar, err)
	}
	t.Cleanup(pool.Close)
	return pool, pgURL
}

// requireRedis resolves TEST_REDIS_URL. Per the spec convention the default is
// "localhost:55432" (shared with PG); when the *default* value is not a Redis
// we fall back to the conventional Redis port localhost:6379 so the local dev
// stack works out of the box. An EXPLICITLY set TEST_REDIS_URL is honoured
// verbatim — if it is unreachable the scenario t.Skips (so a misconfigured CI
// surfaces the failure rather than silently switching ports).
func requireRedis(t *testing.T) (*redis.Client, string) {
	t.Helper()
	envAddr := os.Getenv(redisEnvVar)
	usingDefault := envAddr == ""
	addr := envAddr
	if usingDefault {
		addr = defaultRedisAddr
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	probe := func(a string) (*redis.Client, error) {
		c := redis.NewClient(&redis.Options{Addr: a})
		if err := c.Ping(ctx).Err(); err != nil {
			_ = c.Close()
			return nil, err
		}
		return c, nil
	}

	client, err := probe(addr)
	// Only the spec default path falls through to localhost:6379. An explicit
	// TEST_REDIS_URL that fails to connect skips (don't mask a bad override).
	if err != nil && usingDefault && addr != "localhost:6379" {
		client, err = probe("localhost:6379")
		if err == nil {
			addr = "localhost:6379"
		}
	}
	if err != nil {
		t.Skipf("%s unreachable, skipping Redis integration test: %v", redisEnvVar, err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client, addr
}

// deleteRequestRows cleans the rows a scenario created so tests are idempotent
// against a shared dev database. Best-effort; failures only logged.
func deleteRequestRows(t *testing.T, pool *pgxpool.Pool, requestIDs ...string) {
	t.Helper()
	if len(requestIDs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, id := range requestIDs {
		_, _ = pool.Exec(ctx, `DELETE FROM request_wal_bodies WHERE request_id = $1`, id)
		_, _ = pool.Exec(ctx, `DELETE FROM request_wal_hot WHERE request_id = $1`, id)
	}
}

// uniqueReq returns a request id unique within a test run so concurrent
// scenarios and re-runs never collide.
func uniqueReq(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), atomic.AddUint64(&reqSeq, 1))
}

var reqSeq uint64

// captureFallbackWriter is a thread-safe dbdegradation.BackupWriter that
// records every key/payload handed to it so the integration tests can assert
// the production fallback path received the expected records. It mirrors the
// stubBackupWriter used by the unit tests, but lives in the external test
// package because the integration suite cannot reach package-private types.
type captureFallbackWriter struct {
	mu       sync.Mutex
	keys     []string
	payloads map[string]json.RawMessage
}

func newCaptureFallbackWriter() *captureFallbackWriter {
	return &captureFallbackWriter{payloads: make(map[string]json.RawMessage)}
}

func (w *captureFallbackWriter) WriteRequestLog(_ context.Context, _ string, _ any) error {
	return nil
}

func (w *captureFallbackWriter) WriteRequestWAL(_ context.Context, key string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		// The overflow marker path serializes a struct; a marshal failure
		// would itself be a bug worth surfacing as a test failure.
		return fmt.Errorf("capture fallback marshal: %w", err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.keys = append(w.keys, key)
	w.payloads[key] = raw
	return nil
}

func (w *captureFallbackWriter) snapshot() ([]string, map[string]json.RawMessage) {
	w.mu.Lock()
	defer w.mu.Unlock()
	keys := make([]string, len(w.keys))
	copy(keys, w.keys)
	payloads := make(map[string]json.RawMessage, len(w.payloads))
	for k, v := range w.payloads {
		payloads[k] = append([]byte(nil), v...)
	}
	return keys, payloads
}

// countKeyWithSuffix counts captured keys that end with suffix.
func (w *captureFallbackWriter) countKeyWithSuffix(suffix string) int {
	keys, _ := w.snapshot()
	n := 0
	for _, k := range keys {
		if strings.HasSuffix(k, suffix) {
			n++
		}
	}
	return n
}

// hasKeyWithPrefix reports whether any captured key starts with prefix.
func (w *captureFallbackWriter) hasKeyWithPrefix(prefix string) bool {
	keys, _ := w.snapshot()
	for _, k := range keys {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

// ----------------------------------------------------------------------------
// Scenario 1: PG transaction abort -> fallback
//
// We wrap the real pgxpool with a txAbortingDB whose Begin returns a tx that
// executes `SELECT 1/0` *on the live connection* before the logger's UPDATE.
// That forces the real Postgres transaction into SQLSTATE 25P02 (aborted),
// so persistUpdateInTx errors, flushBatch short-circuits, and every record
// in the batch is routed to the fallback writer (the production "stop on
// first failure, fall back per-row" contract, exercised on real PG).
// ----------------------------------------------------------------------------

// txAbortingDB wraps a real pgxpool.Pool and returns transactions that abort
// themselves before the logger's UPDATE runs. Implements telemetry.RequestLoggerDB.
type txAbortingDB struct {
	pool *pgxpool.Pool
}

func (d *txAbortingDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return d.pool.Exec(ctx, sql, args...)
}

func (d *txAbortingDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	// Force a real-PG tx abort: division by zero aborts the transaction
	// (SQLSTATE 22012 -> 25P02 for all subsequent statements).
	if _, err := tx.Exec(ctx, `SELECT 1/0`); err != nil {
		// Expected: the division raised an error and aborted the tx. We keep
		// the tx open so the logger's UPDATE then fails with 25P02.
		_ = err
	}
	return &abortedTx{Tx: tx}, nil
}

// abortedTx forwards a pgx.Tx but ensures the underlying connection is
// released on Rollback/Commit. pgx.Tx already handles this; the wrapper
// exists so the abort is deterministic and the rollback semantics stay
// explicit.
type abortedTx struct {
	pgx.Tx
}

func TestFaultInjection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Scenario 1: PG transaction abort -> fallback.
	t.Run("PG_TxAbort_Fallback", func(t *testing.T) {
		pool, _ := requirePG(t)

		fallback := newCaptureFallbackWriter()
		// BatchSize must be large enough that both updates land in one batch
		// so we can assert the ENTIRE batch is routed to the fallback.
		rl := telemetry.NewRequestLogger(pool, &telemetry.RequestLoggerConfig{
			QueueSize:    100,
			BatchSize:    50,
			FlushTimeout: 50 * time.Millisecond,
			Enabled:      true,
		})
		rl.SetFallbackWriter(fallback)
		// Swap in the tx-aborting DB AFTER construction; the worker is already
		// running but has not flushed yet because the queue is empty.
		rl.SetDBForTest(&txAbortingDB{pool: pool})
		defer rl.Stop()

		// CreateInitial writes a real row on the live pool (upsertInitial uses
		// db.Exec, which the wrapper delegates unchanged), so the subsequent
		// UPDATE inside flushBatch targets an existing row.
		reqA := uniqueReq("fi-abort-a")
		reqB := uniqueReq("fi-abort-b")
		t.Cleanup(func() { deleteRequestRows(t, pool, reqA, reqB) })

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, rl.CreateInitial(ctx, &telemetry.InitialRequest{
			RequestID:   reqA,
			TenantID:    "fi-tenant",
			SessionID:   "gw_fi_session",
			ClientModel: "gpt-4o-mini",
		}))
		require.NoError(t, rl.CreateInitial(ctx, &telemetry.InitialRequest{
			RequestID:   reqB,
			TenantID:    "fi-tenant",
			ClientModel: "gpt-4o-mini",
		}))

		// Non-terminal updates flow through the async queue -> flushBatch.
		rl.Update(&telemetry.LogUpdate{RequestID: reqA, Stage: telemetry.StageCompressed, Status: telemetry.StatusPending})
		rl.Update(&telemetry.LogUpdate{RequestID: reqB, Stage: telemetry.StageTransformed, Status: telemetry.StatusPending})

		// flushBatch runs on FlushTimeout; wait for it to process (and fail).
		require.Eventually(t, func() bool {
			return fallback.countKeyWithSuffix(":update") >= 2
		}, 3*time.Second, 25*time.Millisecond, "fallback did not receive both updates after tx abort")

		keys, _ := fallback.snapshot()
		t.Logf("fallback received %d records: %v", len(keys), keys)
		// The whole batch must have been routed to the fallback — both updates
		// present, no overflow markers (fallback writes succeeded).
		assert.GreaterOrEqual(t, fallback.countKeyWithSuffix(":update"), 2)
		assert.False(t, fallback.hasKeyWithPrefix("request_logger:overflow:"),
			"fallback write succeeded, so no overflow marker should be emitted")

		// Replay must be able to recover: re-applying one of the captured
		// updates through the *real* (non-aborting) logger must succeed.
		replayRL := telemetry.NewRequestLogger(pool, &telemetry.RequestLoggerConfig{
			QueueSize: 100, BatchSize: 10, FlushTimeout: 50 * time.Millisecond, Enabled: true,
		})
		defer replayRL.Stop()
		_, payloads := fallback.snapshot()
		keyA := reqA + ":update"
		payloadA, ok := payloads[keyA]
		require.True(t, ok, "captured update for %s missing", keyA)
		require.NoError(t, replayRL.ReplayFallback(ctx, dbdegradation.BackupRecord{
			Type:      "request_wal",
			RecordKey: keyA,
			Payload:   payloadA,
		}))
		stats := replayRL.OverflowCounts()
		assert.GreaterOrEqual(t, stats.ReplayAttempt, uint64(1))
		assert.GreaterOrEqual(t, stats.ReplaySuccess, uint64(1))
	})

	// Scenario 2: RequestLogger queue full -> overflow/marker.
	t.Run("PG_QueueFull_FallbackMarker", func(t *testing.T) {
		pool, _ := requirePG(t)

		fallback := newCaptureFallbackWriter()
		// QueueSize=1 so the 2nd/3rd Update overflow. The worker is blocked
		// from draining by a tiny queue and the BatchSize; we want the overflow
		// path (recordOverflow) to fire.
		rl := telemetry.NewRequestLogger(pool, &telemetry.RequestLoggerConfig{
			QueueSize:    1,
			BatchSize:    50,
			FlushTimeout: 2 * time.Second, // long enough that the queue stays full during the burst
			Enabled:      true,
		})
		rl.SetFallbackWriter(fallback)
		defer rl.Stop()

		base := uniqueReq("fi-overflow")
		var created []string
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// Create the initial rows on the real pool first so the updates are
		// well-formed.
		for i := 0; i < 3; i++ {
			rid := fmt.Sprintf("%s-%d", base, i)
			created = append(created, rid)
			require.NoError(t, rl.CreateInitial(ctx, &telemetry.InitialRequest{
				RequestID: rid, TenantID: "fi-tenant", ClientModel: "gpt-4o-mini",
			}))
		}
		t.Cleanup(func() { deleteRequestRows(t, pool, created...) })

		// Fire 3 non-terminal updates faster than the 2s flush can drain the
		// 1-slot queue. At least 2 must overflow.
		for i := 0; i < 3; i++ {
			rl.Update(&telemetry.LogUpdate{
				RequestID: created[i],
				Stage:     telemetry.StageCompressed,
				Status:    telemetry.StatusPending,
			})
		}

		// The overflow accounting is synchronous inside recordOverflow, so it
		// is observable immediately after the burst.
		stats := rl.OverflowCounts()
		assert.Greater(t, stats.QueueOverflow, uint64(0),
			"queue overflow counter must increment when the queue is full")
		t.Logf("queue overflow=%d after 3 updates on a 1-slot queue", stats.QueueOverflow)

		// The overflowed original LogUpdate is written to the fallback as
		// "<req>:update" (before any marker). At least one such record must be
		// present.
		require.Eventually(t, func() bool {
			return fallback.countKeyWithSuffix(":update") >= 1
		}, 2*time.Second, 25*time.Millisecond, "overflowed update not written to fallback")
		assert.GreaterOrEqual(t, fallback.countKeyWithSuffix(":update"), 1)
	})

	// Scenario 3: Redis kill -> URSM v2 fallback.
	//
	// We exercise the production PlanCandidates surface via PlanReadyWithSource,
	// which is the S-3 (routing_state_source) entry point the router calls. On
	// the authoritative + Redis-up path it records the inner NodeMirror source
	// (node_mirror_miss with the mirror disabled); on the Redis-down path
	// filterAndScore errors and PlanReadyWithSource records
	// StateSourceFallback (the fail-open signal) before returning.
	t.Run("Redis_Kill_URSMv2Fallback", func(t *testing.T) {
		rdb, _ := requireRedis(t)

		cfg := v2.DefaultConfig()
		cfg.Mode = api.ModeAuthoritative
		// Disable the process LRU mirror so every read hits Redis — otherwise
		// a warm mirror would mask the kill (fail-open) and we would not
		// observe the fallback source increment.
		cfg.LRUMirrorSize = 0
		// Unique prefix so this test never collides with another run's keys.
		cfg.RedisKeyPrefix = "ursm:v2:fi:" + strconv.FormatInt(time.Now().UnixNano(), 16) + ":"

		mgr := v2.New(v2.Dependencies{Redis: rdb, Config: cfg})
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		// Open the recovery gate and seed an available node.
		require.NoError(t, mgr.SetReady(ctx, true))
		t.Cleanup(func() {
			cleanup, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer ccancel()
			// Best-effort cleanup of the prefix namespace.
			_ = rdb.Del(cleanup, cfg.RedisKeyPrefix+"meta:ready").Err()
		})

		require.NoError(t, mgr.SetSeedForTest(ctx, v2.CandidateSeed{
			ProviderID: 1, CredentialID: 77, RawModel: "gpt-fi", TenantID: "t",
		}))

		seeds := []v2.CandidateSeed{{
			ProviderID: 1, CredentialID: 77, RawModel: "gpt-fi",
			TenantID: "t", BaseURLMs: 200, PriceIn: 1, PriceOut: 2, Trust: 0.9,
		}}

		// Reset the global state-source counters so the assertions are
		// isolated from any prior scenario in this binary.
		statesource.ResetForTest()

		// First call: authoritative path must succeed against the live Redis.
		// PlanReadyWithSource returns the filtered (available) seeds on the
		// happy path and records the inner NodeMirror source.
		plan, src, err := mgr.PlanReadyWithSource(ctx, seeds, "t", "gpt-fi", true)
		require.NoError(t, err, "authoritative PlanCandidates must succeed with Redis up")
		require.Len(t, plan, 1, "seeded available node must survive the plan filter")
		assert.Equal(t, "gpt-fi", plan[0].RawModel)
		t.Logf("authoritative PlanCandidates: source=%s plan_len=%d", src, len(plan))

		snap := statesource.Snapshot()
		missBefore := snap[statesource.StateSourceNodeMirrorMiss]
		fallbackBefore := snap[statesource.StateSourceFallback]
		assert.Greater(t, missBefore, int64(0),
			"node_mirror_miss must be recorded on the authoritative read (LRU disabled)")
		assert.Equal(t, int64(0), fallbackBefore, "no fallback before Redis kill")

		// KILL Redis: we do NOT control the external Redis process, so we
		// simulate the kill by swapping in a client pointed at a closed
		// (unreachable) port. The v2 Manager exposes SetRedisForTest exactly
		// for restart/kill simulation.
		deadClient := redis.NewClient(&redis.Options{
			Addr:        "127.0.0.1:1", // port 1 is reserved and never accepts
			DialTimeout: 100 * time.Millisecond,
		})
		t.Cleanup(func() { _ = deadClient.Close() })
		mgr.SetRedisForTest(deadClient)

		// Next PlanCandidates: Redis is unreachable -> filterAndScore errors
		// -> PlanReadyWithSource records StateSourceFallback (the fail-open
		// signal) and returns (nil, fallback, err).
		plan2, src2, err2 := mgr.PlanReadyWithSource(ctx, seeds, "t", "gpt-fi", true)
		require.Error(t, err2, "PlanCandidates must fail with Redis down")
		assert.Nil(t, plan2, "no candidates can be returned on the fallback path")
		assert.Equal(t, statesource.StateSourceFallback, src2,
			"killed Redis must surface the fallback state source")

		snap2 := statesource.Snapshot()
		assert.Greater(t, snap2[statesource.StateSourceFallback], fallbackBefore,
			"StateSourceFallback counter must increment after Redis kill")
		// The miss counter must not advance on the fallback path (the read
		// short-circuited before scoring recorded an inner source).
		assert.Equal(t, missBefore, snap2[statesource.StateSourceNodeMirrorMiss],
			"miss counter must not advance on the fallback path")
		t.Logf("after kill: source=%s fallback=%d -> %d",
			src2, fallbackBefore, snap2[statesource.StateSourceFallback])
	})

	// Scenario 4: replay.
	t.Run("PG_ReplayFallback_RestoresRow", func(t *testing.T) {
		pool, _ := requirePG(t)

		fallback := newCaptureFallbackWriter()
		rl := telemetry.NewRequestLogger(pool, &telemetry.RequestLoggerConfig{
			QueueSize: 100, BatchSize: 10, FlushTimeout: 50 * time.Millisecond, Enabled: true,
		})
		rl.SetFallbackWriter(fallback)
		defer rl.Stop()

		reqID := uniqueReq("fi-replay")
		t.Cleanup(func() { deleteRequestRows(t, pool, reqID) })

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// Drive a real CreateInitial so the replayed :initial has a matching
		// row to UPSERT against, then capture a fallback :update record by
		// replaying it.
		require.NoError(t, rl.CreateInitial(ctx, &telemetry.InitialRequest{
			RequestID: reqID, TenantID: "fi-tenant", ClientModel: "gpt-4o-mini",
		}))

		// Build a fallback :update record by hand (simulating what the
		// overflow / degraded path would have written) and replay it.
		update := telemetry.LogUpdate{
			RequestID:           reqID,
			Stage:               telemetry.StageCompleted,
			Status:              telemetry.StatusSuccess,
			CompressionStrategy: "delta_append",
			CompressionMeta:     map[string]interface{}{"msg_count": 7},
		}
		payload, err := json.Marshal(update)
		require.NoError(t, err)

		statsBefore := rl.OverflowCounts()
		require.NoError(t, rl.ReplayFallback(ctx, dbdegradation.BackupRecord{
			Type:      "request_wal",
			RecordKey: reqID + ":update",
			Payload:   payload,
		}))
		statsAfter := rl.OverflowCounts()
		assert.Equal(t, statsBefore.ReplayAttempt+1, statsAfter.ReplayAttempt)
		assert.Equal(t, statsBefore.ReplaySuccess+1, statsAfter.ReplaySuccess)
		assert.Equal(t, statsBefore.ReplayFailure, statsAfter.ReplayFailure,
			"replay must not record a failure on the happy path")

		// Verify the replayed update actually landed in PG.
		var status string
		var stage int
		var strategy *string
		err = pool.QueryRow(ctx,
			`SELECT status, stage, compression_strategy FROM request_wal_hot WHERE request_id = $1`,
			reqID,
		).Scan(&status, &stage, &strategy)
		require.NoError(t, err, "replayed row must exist in PG")
		assert.Equal(t, telemetry.StatusSuccess, status)
		assert.Equal(t, telemetry.StageCompleted, stage)
		if strategy == nil || *strategy != "delta_append" {
			t.Errorf("expected compression_strategy=delta_append, got %v", strategy)
		}
		t.Logf("replay restored row: status=%s stage=%d strategy=%v", status, stage, strategy)

		// Replay an overflow marker record: must be a no-op that still bumps
		// replay success + replay marker counters.
		markerPayload, err := json.Marshal(map[string]any{
			"kind":       "request_logger_overflow",
			"request_id": reqID,
			"stage":      telemetry.StageTransformed,
			"status":     telemetry.StatusFailure,
			"reason":     "queue_full",
		})
		require.NoError(t, err)
		markerBefore := rl.OverflowCounts()
		require.NoError(t, rl.ReplayFallback(ctx, dbdegradation.BackupRecord{
			Type:      "request_wal",
			RecordKey: "request_logger:overflow:" + reqID,
			Payload:   markerPayload,
		}))
		markerAfter := rl.OverflowCounts()
		assert.Equal(t, markerBefore.ReplayAttempt+1, markerAfter.ReplayAttempt)
		assert.Equal(t, markerBefore.ReplaySuccess+1, markerAfter.ReplaySuccess)
		assert.Equal(t, markerBefore.ReplayMarker+1, markerAfter.ReplayMarker)
	})

	// Scenario 5: duplicate request id -> turn_writer idempotent.
	t.Run("PG_TurnWriter_DuplicateRequestId", func(t *testing.T) {
		pool, _ := requirePG(t)

		tw := sessionsv2.NewTurnWriter(pool)

		tenant := "fi-tenant"
		sess := "gw_fi_session_" + strconv.FormatInt(time.Now().UnixNano(), 16)
		reqID := uniqueReq("fi-turn")

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		now := time.Now().UTC()
		base := sessionsv2.TurnRecord{
			SessionID: sess, TenantID: tenant, RequestID: reqID, Ts: now,
			Model: "gpt-4o-mini", Provider: "openai",
		}

		t.Cleanup(func() {
			cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer ccancel()
			_, _ = pool.Exec(cctx, `DELETE FROM gateway.session_turns WHERE session_id = $1`, sess)
		})

		turn1, err := tw.AppendTurn(ctx, base)
		require.NoError(t, err, "first AppendTurn")
		require.Greater(t, turn1, 0)

		// Repeating the SAME request_id must be idempotent: same turn_no,
		// no double increment. This is the production idempotency contract
		// (ON CONFLICT (request_id, partition_date) DO NOTHING + re-read).
		turn1b, err := tw.AppendTurn(ctx, base)
		require.NoError(t, err, "duplicate AppendTurn")
		assert.Equal(t, turn1, turn1b,
			"duplicate request_id must return the same turn_no (idempotent)")

		// A DIFFERENT request_id in the same session must advance turn_no.
		reqID2 := uniqueReq("fi-turn")
		turn2, err := tw.AppendTurn(ctx, sessionsv2.TurnRecord{
			SessionID: sess, TenantID: tenant, RequestID: reqID2, Ts: now,
			Model: "gpt-4o-mini", Provider: "openai",
		})
		require.NoError(t, err, "second AppendTurn with a new request_id")
		assert.Equal(t, turn1+1, turn2,
			"a new request_id must increment turn_no")

		t.Logf("turns: dup=%d/%d (equal), new=%d (incremented)", turn1, turn1b, turn2)
	})
}
