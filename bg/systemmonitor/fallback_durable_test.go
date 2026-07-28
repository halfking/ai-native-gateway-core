// Package bg/systemmonitor — fallback_durable_test.go
//
// Unit tests for the audit follow-up #2 durable backstop:
//   - submitFallback also writes to system_monitor_fallback_queue (PG)
//   - checkRedisHealthOnce drains the table on fallback → healthy
//
// pgxmock is used for the DB; miniredis handles the Redis side.
package systemmonitor

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/redis/go-redis/v9"
)

// newDurableSystemMonitor wires a SystemMonitor with both Redis
// (miniredis) and a pgxmock-backed PG pool. The pgxmock pool is
// attached via SetFallbackDBForTest after construction (NewSystemMonitor
// expects a concrete *pgxpool.Pool, but the fallback path uses an
// internal interface that both real and mock pools satisfy).
func newDurableSystemMonitor(t *testing.T) (*SystemMonitor, *miniredis.Miniredis, pgxmock.PgxPoolIface) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}

	cfg := Config{
		DB:          nil, // not used by the fallback path
		Redis:       rdb,
		Keyring:     nil,
		EncKey:      nil,
		TimeoutMs:   30000,
		Concurrency: 2,
		WorkerCount: 1,
		WorkerID:    "test-worker-durable",
	}
	sm, smErr := NewSystemMonitor(cfg)
	if smErr != nil {
		t.Fatalf("NewSystemMonitor: %v", smErr)
	}
	sm.SetFallbackDBForTest(mock)
	return sm, mr, mock
}

// TestPublishFallbackDurable_InsertsTask verifies that publishFallbackDurable
// issues an INSERT with the task JSON. Uses SetFallbackDBForTest to wire
// pgxmock after construction (since NewSystemMonitor expects a
// concrete *pgxpool.Pool).
func TestPublishFallbackDurable_InsertsTask(t *testing.T) {
	sm, _, _ := newDurableSystemMonitor(t)
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	sm.SetFallbackDBForTest(mock)

	task := &Task{
		ID:           1234,
		TaskType:     TaskTypeChatTool,
		Automaticity: AutomaticityAutomatic,
		Source:       SourceNodeProbe,
		CredentialID: 17,
		ProviderID:   1,
		RawModel:     "gpt-4",
		MaxAttempts:  3,
	}

	mock.ExpectExec(`INSERT INTO system_monitor_fallback_queue`).
		WithArgs(int64(1234), pgxmock.AnyArg(), "test-worker-durable").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	sm.publishFallbackDurable(context.Background(), task)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations not met: %v", err)
	}
}

// TestPublishFallbackDurable_NilDBIsNoop verifies the safe-on-nil-DB
// contract: production disabled-DB deployments must not panic.
func TestPublishFallbackDurable_NilDBIsNoop(t *testing.T) {
	sm, _, _ := newDurableSystemMonitor(t)
	// Override with nil to simulate disabled DB.
	sm.SetFallbackDBForTest(nil)
	task := &Task{ID: 1, TaskType: TaskTypeChatTool}
	// Must not panic.
	sm.publishFallbackDurable(context.Background(), task)
}

// TestDrainFallbackQueue_NoRowsIsNoop verifies the drain when the
// durable table is empty.
func TestDrainFallbackQueue_NoRowsIsNoop(t *testing.T) {
	sm, _, _ := newDurableSystemMonitor(t)
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	sm.SetFallbackDBForTest(mock)

	// Begin transaction succeeds.
	mock.ExpectBegin()
	// SELECT returns zero rows.
	mock.ExpectQuery(`SELECT id, task_json\s+FROM system_monitor_fallback_queue`).
		WillReturnRows(pgxmock.NewRows([]string{"id", "task_json"}))
	// Commit (no rows, nothing to do).
	mock.ExpectCommit()

	n, err := sm.drainFallbackQueue(context.Background())
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if n != 0 {
		t.Fatalf("drain count = %d, want 0", n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations not met: %v", err)
	}
}

// TestDrainFallbackQueue_RedisLPushAndDelete verifies the drain path
// for a non-empty table: LPUSH into Redis, DELETE the row.
func TestDrainFallbackQueue_RedisLPushAndDelete(t *testing.T) {
	sm, mr, _ := newDurableSystemMonitor(t)
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	sm.SetFallbackDBForTest(mock)

	// Build a valid task JSON that round-trips through MarshalForLua.
	task := &Task{
		ID:           99,
		TaskType:     TaskTypeChatTool,
		Automaticity: AutomaticityAutomatic,
		Source:       SourceNodeProbe,
		CredentialID: 17,
		ProviderID:   1,
		RawModel:     "gpt-4",
		EnqueuedAt:   time.Now().UTC(),
		ScheduledAt:  time.Now().UTC(),
		NextRunAt:    time.Now().UTC(),
		MaxAttempts:  3,
	}
	luaJSON, _ := marshalTaskForLua(task)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, task_json\s+FROM system_monitor_fallback_queue`).
		WillReturnRows(pgxmock.NewRows([]string{"id", "task_json"}).
			AddRow(int64(1), []byte(luaJSON)))
	mock.ExpectExec(`DELETE FROM system_monitor_fallback_queue WHERE id`).
		WithArgs(int64(1)).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	mock.ExpectCommit()

	n, err := sm.drainFallbackQueue(context.Background())
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if n != 1 {
		t.Fatalf("drain count = %d, want 1", n)
	}

	// Verify the LPUSH landed in miniredis.
	queueSize, err := sm.queue.QueueSize(context.Background())
	if err != nil {
		t.Fatalf("QueueSize: %v", err)
	}
	if queueSize != 1 {
		t.Fatalf("queue size = %d, want 1", queueSize)
	}
	_ = mr // keep mr alive until end of test

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations not met: %v", err)
	}
}

// TestCheckRedisHealthOnce_DrainsFallbackOnRecovery verifies the
// integration: on fallback → healthy transition, the monitor calls
// drainFallbackQueue. We pre-load a fallback row via pgxmock and
// verify the LPUSH happens.
func TestCheckRedisHealthOnce_DrainsFallbackOnRecovery(t *testing.T) {
	sm, mr, _ := newDurableSystemMonitor(t)
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	sm.SetFallbackDBForTest(mock)

	// Drive into fallback (3 failures to cross default threshold).
	sm.pingFn = func(_ context.Context) error { return errRedisDown }
	for i := 0; i < 4; i++ {
		sm.checkRedisHealthOnce(context.Background())
	}
	if !sm.IsFallback() {
		t.Fatalf("setup: must be in fallback")
	}

	// Prepare drain expectations.
	task := &Task{
		ID:           7,
		TaskType:     TaskTypeChatTool,
		Automaticity: AutomaticityAutomatic,
		Source:       SourceNodeProbe,
		CredentialID: 1,
		RawModel:     "m",
		EnqueuedAt:   time.Now().UTC(),
		ScheduledAt:  time.Now().UTC(),
		NextRunAt:    time.Now().UTC(),
		MaxAttempts:  3,
	}
	luaJSON, _ := marshalTaskForLua(task)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, task_json\s+FROM system_monitor_fallback_queue`).
		WillReturnRows(pgxmock.NewRows([]string{"id", "task_json"}).
			AddRow(int64(1), []byte(luaJSON)))
	mock.ExpectExec(`DELETE FROM system_monitor_fallback_queue WHERE id`).
		WithArgs(int64(1)).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	mock.ExpectCommit()

	// Recover.
	sm.pingFn = func(_ context.Context) error { return nil }
	sm.checkRedisHealthOnce(context.Background())

	if sm.IsFallback() {
		t.Fatalf("must exit fallback after recovery")
	}
	queueSize, _ := sm.queue.QueueSize(context.Background())
	if queueSize != 1 {
		t.Fatalf("queue size = %d after drain, want 1", queueSize)
	}
	_ = mr

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations not met: %v", err)
	}
}

// errRedisDown is a sentinel used by the fallback-drain tests.
var errRedisDown = redisErr("redis is down")

type redisErr string

func (e redisErr) Error() string { return string(e) }
