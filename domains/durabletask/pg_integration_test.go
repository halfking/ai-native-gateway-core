//go:build integration

// pg_integration_test.go — SR-W3 Wave 2 真实 PostgreSQL 集成测试。
//
// 覆盖三条只有真实 PG 才能验证的不变量：
//  1. claim 后并发续租/写回的 fencing 失效（SKIP LOCKED + fencing_token，
//     输家必须拿到 ErrLeaseLost 而不是覆盖赢家的状态）；
//  2. 语义检查点任务不被 ReapDeadlines 回收（commit_state=content+ 的任务
//     只能被 ReapUnsafeCheckpoints 终态化为 resume_safety_blocked）；
//  3. outbox 幂等投递（(task_id, result_version) 唯一约束 + delivered 行
//     不可重认领 + 同版本重复出队不产生第二行）。
//
// 运行：go test -tags integration ./domains/durabletask（需要 Docker）。
package durabletask

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/kaixuan/llm-gateway-go/secret"
)

// readMigrationSQL loads the production migration 515 so the integration
// suite exercises the exact schema shipped to deployments.
func readMigrationSQL() (string, error) {
	b, err := os.ReadFile(filepath.Join("..", "..", "sql", "migrations", "startup", "515_durable_llm_tasks.sql"))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// One shared container for the whole package run: five parallel containers
// raced the Docker port proxy on connect; a single instance with per-test
// TRUNCATE keeps tests isolated and the suite fast.
var (
	itOnce  sync.Once
	itStore *Store
	itKR    *secret.Keyring
	itErr   error
)

func startDurablePG(t *testing.T) (*Store, *secret.Keyring) {
	t.Helper()
	itOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		pgContainer, err := postgres.Run(ctx, "postgres:16-alpine",
			postgres.WithDatabase("testdb"),
			postgres.WithUsername("testuser"),
			postgres.WithPassword("testpass"))
		if err != nil {
			itErr = err
			return
		}
		connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			itErr = err
			return
		}
		// The Docker port proxy can accept TCP before forwarding is wired:
		// retry the first connect with backoff.
		var pool *pgxpool.Pool
		for attempt := 0; attempt < 30; attempt++ {
			pool, err = pgxpool.New(ctx, connStr)
			if err == nil {
				if perr := pool.Ping(ctx); perr != nil {
					pool.Close()
					pool = nil
					err = perr
				} else {
					break
				}
			}
			time.Sleep(time.Second)
		}
		if err != nil {
			itErr = err
			return
		}
		// Apply the production migration verbatim.
		schema, err := readMigrationSQL()
		if err != nil {
			itErr = err
			return
		}
		if _, err = pool.Exec(ctx, schema); err != nil {
			itErr = err
			return
		}
		itKR = integrationKeyringForInit()
		itStore = NewStore(pool, itKR)
	})
	require.NoError(t, itErr)
	// Tests run sequentially; TRUNCATE (which does not fire row triggers)
	// gives each test a clean append-only event table too.
	t.Cleanup(func() {
		_, err := itStore.db.Exec(context.Background(),
			`TRUNCATE durable_llm_tasks, durable_llm_task_events, durable_pending_outbox CASCADE`)
		require.NoError(t, err)
	})
	return itStore, itKR
}

func integrationKeyringForInit() *secret.Keyring {
	var key [32]byte
	for i := range key {
		key[i] = byte(i + 1)
	}
	kr, _ := secret.NewKeyring(map[string][32]byte{"current": key}, "current")
	return kr
}

// integrationSnapshot builds a snapshot with a fresh UUID task id and a
// matching request hash; the label is used in request/session ids for logs.
func integrationSnapshot(label string) DurableRequestSnapshotV1 {
	id := uuid.NewString()
	return DurableRequestSnapshotV1{
		Version: SnapshotVersionV1, TaskID: id, RequestID: "req-" + label,
		TenantID: "tenant-it", SessionID: "session-it",
		ClientProtocol: "openai_responses", Endpoint: "/v1/responses",
		RequestHash: HashRequestBody([]byte(id)), NormalizedBody: []byte(`{"model":"gpt-5"}`),
	}
}

func createTask(t *testing.T, store *Store, label string, deadline time.Time) Lease {
	t.Helper()
	snapshot := integrationSnapshot(label)
	lease, err := store.CreateAndClaim(context.Background(), CreateAndClaimParams{
		Snapshot: snapshot, LeaseOwner: "gateway/" + label,
		LeaseUntil: time.Now().Add(time.Minute), DeadlineAt: deadline,
		ResultExpiresAt: deadline.Add(time.Hour),
	})
	require.NoError(t, err)
	t.Logf("durable task %s (label %s)", lease.TaskID, label)
	return lease
}

// expireTask forces an existing task's deadline into the past (creation
// validates deadline > lease, so already-expired tasks are made this way).
func expireTask(t *testing.T, store *Store, lease Lease) {
	t.Helper()
	_, err := store.db.Exec(context.Background(),
		`UPDATE durable_llm_tasks SET deadline_at = now() - interval '1 minute' WHERE id=$1`, lease.TaskID)
	require.NoError(t, err)
}

// TestPGConcurrentClaimFencing：任务租约到期后两个 worker 并发 claim，
// SKIP LOCKED 保证只有一个赢；输家用旧 fencing token 续租/写终态必须
// 全部 ErrLeaseLost，赢家的状态不被覆盖。
func TestPGConcurrentClaimFencing(t *testing.T) {
	store, kr := startDurablePG(t)
	ctx := context.Background()
	deadline := time.Now().Add(2 * time.Hour)

	lease := createTask(t, store, "018f-it-fencing", deadline)
	// 前台释放租约（模拟断连）：Reschedule 回 runnable。
	require.NoError(t, store.Reschedule(ctx, lease, RescheduleParams{
		Status: StatusRetryScheduled, NextRetryAt: time.Now().Add(-time.Second),
	}))

	// 并发 claim：SKIP LOCKED 下恰好一个赢。
	var mu sync.Mutex
	var winners []Lease
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tasks, err := store.ClaimRunnable(ctx, "worker-"+string(rune('a'+i)), 1, time.Minute)
			require.NoError(t, err)
			mu.Lock()
			for _, task := range tasks {
				winners = append(winners, task.Lease)
			}
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	require.Len(t, winners, 1, "exactly one worker must win the claim")
	winner := winners[0]

	// 输家（旧 token 的前台）续租与终态写入必须被 fence 拒绝。
	err := store.RenewLease(ctx, lease, time.Now().Add(time.Minute))
	require.ErrorIs(t, err, ErrLeaseLost, "stale foreground lease renew must be fenced off")
	_, err = store.Complete(ctx, lease, CompleteParams{Body: []byte(`{"stale":true}`)})
	require.ErrorIs(t, err, ErrLeaseLost, "stale foreground complete must be fenced off")

	// 赢家正常完成；快照可解密且绑定一致。
	tasks, err := store.ClaimRunnable(ctx, winner.Owner, 1, time.Minute)
	require.NoError(t, err)
	require.Empty(t, tasks, "no second task is runnable")
	item, err := store.Complete(ctx, winner, CompleteParams{Body: []byte(`{"ok":true}`)})
	require.NoError(t, err)
	require.Equal(t, StatusCompleted, item.Status)
	require.NotZero(t, item.ID, "completion must enqueue the outbox row")

	var requestHash string
	require.NoError(t, store.db.QueryRow(context.Background(),
		`SELECT request_hash FROM durable_llm_tasks WHERE id=$1`, winner.TaskID).Scan(&requestHash))
	snapshot, _, err := DecryptRequestSnapshotV1(
		mustTaskCiphertext(t, store, winner.TaskID), SnapshotVersionV1, kr,
		secret.AADBinding{TenantID: "tenant-it", TaskID: winner.TaskID, RequestHash: requestHash})
	require.NoError(t, err)
	require.Equal(t, winner.TaskID, snapshot.TaskID)
}

func mustTaskCiphertext(t *testing.T, store *Store, taskID string) string {
	t.Helper()
	var ciphertext string
	err := store.db.QueryRow(context.Background(),
		`SELECT request_snapshot_ciphertext FROM durable_llm_tasks WHERE id=$1`, taskID).Scan(&ciphertext)
	require.NoError(t, err)
	return ciphertext
}

// TestPGSemanticCheckpointProtectedFromDeadlineReaper：越过语义检查点的
// 任务即使 deadline 已过也不会被 ReapDeadlines 回收（不能重放），只能由
// ReapUnsafeCheckpoints 终态化为 resume_safety_blocked。
func TestPGSemanticCheckpointProtectedFromDeadlineReaper(t *testing.T) {
	store, _ := startDurablePG(t)
	ctx := context.Background()

	past := createTask(t, store, "018f-it-reap", time.Now().Add(2*time.Hour))
	expireTask(t, store, past)
	// 语义检查点：content 已提交。
	require.NoError(t, store.Checkpoint(ctx, past, CommitContent))

	items, err := store.ReapDeadlines(ctx, 100)
	require.NoError(t, err)
	require.Empty(t, items, "semantic-checkpointed task must not be reclaimed by the deadline reaper")

	items, err = store.ReapUnsafeCheckpoints(ctx, 100)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, StatusResumeSafetyBlocked, items[0].Status)
	require.Equal(t, ReasonResumeSafetyBlocked, items[0].ReasonCode)

	// 终态化后任何 fenced 写都不再可能。
	err = store.RenewLease(ctx, past, time.Now().Add(time.Minute))
	require.ErrorIs(t, err, ErrLeaseLost)
}

// TestPGReapDeadlinesExpiresUncheckedTasks：未过检查点的过期任务正常
// 由 deadline reaper 终态化（expired + 事件 + outbox 同事务）。
func TestPGReapDeadlinesExpiresUncheckedTasks(t *testing.T) {
	store, _ := startDurablePG(t)
	ctx := context.Background()

	lease := createTask(t, store, "018f-it-expire", time.Now().Add(2*time.Hour))
	expireTask(t, store, lease)
	items, err := store.ReapDeadlines(ctx, 100)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, StatusExpired, items[0].Status)
	require.Equal(t, ReasonSurvivalExpired, items[0].ReasonCode)
}

// TestPGOutboxIdempotentDelivery：终态只产生一行 outbox；delivered 行不
// 可重认领；重复投递（fail→retry）不产生第二行，(task_id,result_version)
// 唯一约束兜底。
func TestPGOutboxIdempotentDelivery(t *testing.T) {
	store, _ := startDurablePG(t)
	ctx := context.Background()
	deadline := time.Now().Add(2 * time.Hour)

	lease := createTask(t, store, "018f-it-outbox", deadline)
	item, err := store.Complete(ctx, lease, CompleteParams{Body: []byte(`{"id":"resp_it"}`)})
	require.NoError(t, err)

	// 唯一约束：同 (task_id, result_version) 再插一行必须收敛到同一行。
	dup, err := store.claimOutboxRowDirect(ctx, item.TaskID, item.ResultVersion)
	require.NoError(t, err)
	require.Equal(t, item.ID, dup, "duplicate (task_id,result_version) must not create a second outbox row")

	// 投递一次成功。
	claimed, err := store.ClaimOutbox(ctx, "outbox-1", 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, StatusCompleted, claimed[0].Status)
	require.NoError(t, store.MarkOutboxDelivered(ctx, claimed[0].ID, "outbox-1"))

	// delivered 行不可重认领。
	again, err := store.ClaimOutbox(ctx, "outbox-2", 10, time.Minute)
	require.NoError(t, err)
	require.Empty(t, again, "delivered rows must not be re-claimable")

	// 失败重试路径：failed + 到期后可再认领，仍只有一行。
	lease2 := createTask(t, store, "018f-it-outbox2", deadline)
	_, err = store.Fail(ctx, lease2, FailureParams{Status: StatusExpired, ReasonCode: "test_fail"})
	require.NoError(t, err)
	c2, err := store.ClaimOutbox(ctx, "outbox-1", 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, c2, 1)
	require.NoError(t, store.MarkOutboxFailed(ctx, c2[0].ID, "outbox-1", "boom", time.Now().Add(-time.Second)))
	c3, err := store.ClaimOutbox(ctx, "outbox-3", 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, c3, 1, "failed+due row must be re-claimable")
	require.NoError(t, store.MarkOutboxDelivered(ctx, c3[0].ID, "outbox-3"))

	var rows int
	require.NoError(t, store.db.QueryRow(ctx,
		`SELECT count(*) FROM durable_pending_outbox`).Scan(&rows))
	require.Equal(t, 2, rows, "one outbox row per terminal task, no duplicates")
}

// claimOutboxRowDirect re-inserts the same (task_id, result_version) outbox
// row to prove the unique constraint collapses duplicates (ON CONFLICT DO
// UPDATE keeps the original id).
func (s *Store) claimOutboxRowDirect(ctx context.Context, taskID string, version int64) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx, `INSERT INTO durable_pending_outbox
		(task_id,tenant_id,request_id,session_id,fencing_token,result_version,result_hash,projection_status,projection_payload)
		VALUES($1,'tenant-it','req-x','session-it',1,$2,'','completed','{}'::jsonb)
		ON CONFLICT(task_id,result_version) DO UPDATE SET updated_at=now()
		RETURNING id`, taskID, version).Scan(&id)
	return id, err
}

// TestPGActiveCountsAndEvents：ActiveTaskCounts 权威计数与事件链完整。
func TestPGActiveCountsAndEvents(t *testing.T) {
	store, _ := startDurablePG(t)
	ctx := context.Background()
	deadline := time.Now().Add(2 * time.Hour)

	createTask(t, store, "018f-it-counts", deadline)
	counts, err := store.ActiveTaskCounts(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), counts["tenant-it"])

	var events int
	require.NoError(t, store.db.QueryRow(ctx,
		`SELECT count(*) FROM durable_llm_task_events WHERE request_id=$1`, "req-018f-it-counts").Scan(&events))
	require.Equal(t, 1, events, "creation must append the accepted→running event")
}
