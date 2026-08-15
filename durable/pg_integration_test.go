//go:build integration

// pg_integration_test.go — SR-W3 真实 PostgreSQL 集成测试（Wave B 从
// feat/m3-sr-w3-durable 移植，适配 durable/ 包 API 与 migration 516）。
//
// 覆盖只有真实 PG 才能验证的不变量（doc 18 §11.3 验收 15/16/19）：
//  1. claim 后并发续租/写回的 fencing 失效（SKIP LOCKED + fencing_token，
//     输家必须被拒而不是覆盖赢家的状态）；
//  2. 语义检查点任务不被 ReapDeadlines 回收（commit_state=content+ 的
//     任务只能被 ReapUnsafeCheckpointed 终态化为 resume_safety_blocked）；
//  3. outbox 投影幂等（每任务一行、投递成功即删、失败 fail-closed 停泊
//     并退避重试，不向 PendingStore 写垃圾）。
//
// 运行：go test -tags integration ./durable（需要 Docker）。
package durable

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/kaixuan/llm-gateway-go/pending"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// readMigrationSQL loads the production migration 516 so the integration
// suite exercises the exact schema shipped to deployments.
func readMigrationSQL() (string, error) {
	b, err := os.ReadFile("../sql/migrations/startup/516_durable_llm_tasks.sql")
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

func startDurablePG(t *testing.T) *Store {
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
	return itStore
}

func integrationKeyringForInit() *secret.Keyring {
	var key [32]byte
	for i := range key {
		key[i] = byte(i + 1)
	}
	kr, _ := secret.NewKeyring(map[string][32]byte{"current": key}, "current")
	return kr
}

const itSnapshotBody = `{"model":"gpt-5","input":"integration"}`

// createITTask creates one foreground-claimed task with a fresh request id.
func createITTask(t *testing.T, store *Store, label string, deadline time.Time) *Task {
	t.Helper()
	task, err := store.CreateAndClaim(context.Background(), NewTask{
		TenantID: "tenant-it", RequestID: "req-" + label, SessionID: "session-it",
		Protocol: "openai_responses", Endpoint: "/v1/responses",
		Snapshot: []byte(itSnapshotBody), SnapshotVersion: 1,
		RequestHash: "hash-" + label,
		DeadlineAt:  deadline, ExpiresAt: deadline.Add(time.Hour),
		LeaseOwner: "gateway/" + label, LeaseUntil: time.Now().Add(time.Minute),
	})
	require.NoError(t, err)
	return task
}

// expireITTask forces an existing task's deadline into the past (creation
// validates deadline currency, so already-expired tasks are made this way).
func expireITTask(t *testing.T, store *Store, taskID string) {
	t.Helper()
	_, err := store.db.Exec(context.Background(),
		`UPDATE durable_llm_tasks SET deadline_at = now() - interval '1 minute' WHERE id=$1`, taskID)
	require.NoError(t, err)
}

// expireITLease simulates a crashed/disconnected holder by aging out its lease.
func expireITLease(t *testing.T, store *Store, taskID string) {
	t.Helper()
	_, err := store.db.Exec(context.Background(),
		`UPDATE durable_llm_tasks SET lease_until = now() - interval '1 minute' WHERE id=$1`, taskID)
	require.NoError(t, err)
}

// TestPGConcurrentClaimFencing：任务释放回 runnable 后两个 worker 并发
// claim，SKIP LOCKED 保证只有一个赢；输家用旧 (owner, token) 续租/写终态
// 必须被 fence 拒绝，赢家的状态不被覆盖。
func TestPGConcurrentClaimFencing(t *testing.T) {
	store := startDurablePG(t)
	ctx := context.Background()
	deadline := time.Now().Add(2 * time.Hour)
	now := time.Now()

	foreground := createITTask(t, store, "fencing", deadline)
	// 前台释放租约（模拟断连）：Reschedule 回 runnable。
	require.NoError(t, store.Reschedule(ctx, RescheduleParams{
		TaskID: foreground.ID, LeaseOwner: foreground.LeaseOwner, FencingToken: foreground.FencingToken,
		NextRetryAt: now.Add(-time.Second), Reason: "detached",
	}))

	// 并发 claim：SKIP LOCKED 下恰好一个赢。
	var mu sync.Mutex
	var winners []*Task
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tasks, err := store.ClaimRunnable(ctx, ClaimOptions{
				Owner: "worker-" + string(rune('a'+i)), Lease: time.Minute, Batch: 1, Now: now,
			})
			require.NoError(t, err)
			mu.Lock()
			winners = append(winners, tasks...)
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	require.Len(t, winners, 1, "exactly one worker must win the claim")
	winner := winners[0]

	// 输家（旧 owner/token 的前台）续租必须被 fence 拒绝。
	err := store.RenewLease(ctx, foreground.ID, foreground.LeaseOwner, foreground.FencingToken, now.Add(time.Minute))
	require.ErrorIs(t, err, ErrLeaseLost, "stale foreground lease renew must be fenced off")
	// 输家终态写入被拒（Committed=false），不产生第二终态。
	stale, err := store.CommitTerminal(ctx, TerminalCommit{
		Task: foreground, Outcome: StatusCompleted, Body: []byte(`{"stale":true}`),
		ContentType: "application/json", Attempt: 1,
	})
	require.NoError(t, err)
	require.False(t, stale.Committed, "stale foreground terminal must be fenced off")

	// 赢家正常完成；快照可解密且绑定一致。
	tasks, err := store.ClaimRunnable(ctx, ClaimOptions{Owner: "worker-c", Lease: time.Minute, Batch: 1, Now: now})
	require.NoError(t, err)
	require.Empty(t, tasks, "no second task is runnable")
	projection, err := store.CommitTerminal(ctx, TerminalCommit{
		Task: winner, Outcome: StatusCompleted, Body: []byte(`{"ok":true}`),
		ContentType: "application/json", Attempt: 1,
	})
	require.NoError(t, err)
	require.True(t, projection.Committed)

	var outboxRows int
	require.NoError(t, store.db.QueryRow(ctx,
		`SELECT count(*) FROM durable_pending_outbox WHERE task_id=$1`, winner.ID).Scan(&outboxRows))
	require.Equal(t, 1, outboxRows, "completion must enqueue exactly one outbox row")

	snapshot, err := store.LoadSnapshot(ctx, winner.ID)
	require.NoError(t, err)
	require.Equal(t, itSnapshotBody, string(snapshot.Body))
	require.Equal(t, "hash-fencing", snapshot.RequestHash)
}

// TestPGSemanticCheckpointProtectedFromDeadlineReaper：越过语义检查点的
// 任务即使 deadline 已过也不能被 ReapDeadlines 打成干净的 expired（doc 18
// §11.3 验收 19）——只能由 ReapUnsafeCheckpointed 终态化为
// resume_safety_blocked；活跃 lease 期间两个 reaper 都不得碰。
func TestPGSemanticCheckpointProtectedFromDeadlineReaper(t *testing.T) {
	store := startDurablePG(t)
	ctx := context.Background()
	now := time.Now()

	task := createITTask(t, store, "reap", now.Add(2*time.Hour))
	// 语义检查点：content 已提交（lease 仍活跃）。
	require.NoError(t, store.CheckpointCommitState(ctx, CheckpointParams{
		TaskID: task.ID, LeaseOwner: task.LeaseOwner, FencingToken: task.FencingToken,
		State: CommitStateContent,
	}))

	// 活跃 lease 的检查点任务两个 reaper 都不能碰（doc 28 lease 护栏）。
	items, err := store.ReapDeadlines(ctx, 100, now)
	require.NoError(t, err)
	require.Empty(t, items, "live checkpointed task must not be reclaimed by the deadline reaper")
	blocked, err := store.ReapUnsafeCheckpointed(ctx, 100, now)
	require.NoError(t, err)
	require.Empty(t, blocked, "live-lease checkpointed task must not be terminalized by the unsafe reaper")

	// 断连/崩溃形态：deadline 过期 + lease 过期。
	expireITTask(t, store, task.ID)
	expireITLease(t, store, task.ID)
	items, err = store.ReapDeadlines(ctx, 100, now)
	require.NoError(t, err)
	require.Empty(t, items, "semantic-checkpointed task must not be expired by the deadline reaper")
	blocked, err = store.ReapUnsafeCheckpointed(ctx, 100, now)
	require.NoError(t, err)
	require.Len(t, blocked, 1)
	require.Equal(t, StatusResumeSafetyBlocked, blocked[0].Status)

	// 终态化后任何 fenced 写都不再可能。
	err = store.RenewLease(ctx, task.ID, task.LeaseOwner, task.FencingToken, now.Add(time.Minute))
	require.ErrorIs(t, err, ErrLeaseLost)
}

// TestPGReapDeadlinesExpiresUncheckedTasks：未过检查点的过期任务正常由
// deadline reaper 终态化（expired + 事件 + outbox 同事务）。
func TestPGReapDeadlinesExpiresUncheckedTasks(t *testing.T) {
	store := startDurablePG(t)
	ctx := context.Background()
	now := time.Now()

	task := createITTask(t, store, "expire", now.Add(2*time.Hour))
	expireITTask(t, store, task.ID)
	expireITLease(t, store, task.ID)
	items, err := store.ReapDeadlines(ctx, 100, now)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, StatusExpired, items[0].Status)
	require.Equal(t, ReasonSurvivalExpired, items[0].ReasonCode)

	var outboxRows int
	require.NoError(t, store.db.QueryRow(ctx,
		`SELECT count(*) FROM durable_pending_outbox WHERE task_id=$1`, task.ID).Scan(&outboxRows))
	require.Equal(t, 1, outboxRows, "deadline reap must enqueue the outbox row in the same transaction")
}

// TestPGOutboxIdempotentProjection：终态只产生一行 outbox（task_id 主键）；
// 投递成功即删行、重复投影无第二行；解密/哈希校验失败 fail-closed——
// 不向 PendingStore 写垃圾，行停泊退避后重试。
func TestPGOutboxIdempotentProjection(t *testing.T) {
	store := startDurablePG(t)
	ctx := context.Background()
	now := time.Now()
	deadline := now.Add(2 * time.Hour)

	task := createITTask(t, store, "outbox", deadline)
	projection, err := store.CommitTerminal(ctx, TerminalCommit{
		Task: task, Outcome: StatusCompleted, Body: []byte(`{"id":"resp_it"}`),
		ContentType: "application/json", Attempt: 1,
	})
	require.NoError(t, err)
	require.True(t, projection.Committed)

	// 幂等：同 task_id 再入队一行（模拟重复终态路径）必须收敛到同一行。
	var rows int
	_, err = store.db.Exec(ctx, `INSERT INTO durable_pending_outbox
		(task_id, tenant_id, result_version, next_attempt_at)
		VALUES ($1, 'tenant-it', $2, $3)
		ON CONFLICT (task_id) DO NOTHING`, task.ID, projection.ResultVersion, now)
	require.NoError(t, err)
	require.NoError(t, store.db.QueryRow(ctx,
		`SELECT count(*) FROM durable_pending_outbox`).Scan(&rows))
	require.Equal(t, 1, rows, "one outbox row per task, no duplicates")

	mr := miniredis.RunT(t)
	pendingStore := pending.NewStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}), time.Hour)
	// 入队时间戳晚于测试起点的 now：投影用一个已到期的时钟。
	due := now.Add(time.Minute)

	// 投递一次成功：行删除、PendingStore 可读。
	delivered, err := store.ProjectPendingOutbox(ctx, pendingStore, 8, due)
	require.NoError(t, err)
	require.Equal(t, 1, delivered)
	require.NoError(t, store.db.QueryRow(ctx,
		`SELECT count(*) FROM durable_pending_outbox`).Scan(&rows))
	require.Zero(t, rows, "delivered row must be deleted")
	resp, found, err := pendingStore.Get(ctx, task.SessionID, task.RequestID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, `{"id":"resp_it"}`, string(resp.Body))
	// 重复投影：无行可投，不产生副作用。
	delivered, err = store.ProjectPendingOutbox(ctx, pendingStore, 8, due)
	require.NoError(t, err)
	require.Zero(t, delivered)

	// fail-closed：result_hash 被篡改的任务不得投影，行停泊退避。
	tampered := createITTask(t, store, "outbox-tamper", deadline)
	_, err = store.CommitTerminal(ctx, TerminalCommit{
		Task: tampered, Outcome: StatusCompleted, Body: []byte(`{"tampered":true}`),
		ContentType: "application/json", Attempt: 1,
	})
	require.NoError(t, err)
	_, err = store.db.Exec(ctx,
		`UPDATE durable_llm_tasks SET result_hash = 'deadbeef' WHERE id=$1`, tampered.ID)
	require.NoError(t, err)
	delivered, err = store.ProjectPendingOutbox(ctx, pendingStore, 8, due)
	require.NoError(t, err)
	require.Zero(t, delivered, "hash-mismatched result must not be projected")
	require.False(t, mr.Exists("pending_response:"+tampered.SessionID+":"+tampered.RequestID),
		"no garbage may reach the PendingStore")
	var attempts int
	require.NoError(t, store.db.QueryRow(ctx,
		`SELECT attempts FROM durable_pending_outbox WHERE task_id=$1`, tampered.ID).Scan(&attempts))
	require.Equal(t, 1, attempts, "failed delivery must park the row for retry")

	// 退避到期后重试（仍失败则继续停泊，不丢行）。
	_, err = store.db.Exec(ctx,
		`UPDATE durable_pending_outbox SET next_attempt_at = now() - interval '1 second' WHERE task_id=$1`, tampered.ID)
	require.NoError(t, err)
	delivered, err = store.ProjectPendingOutbox(ctx, pendingStore, 8, due)
	require.NoError(t, err)
	require.Zero(t, delivered)
	require.NoError(t, store.db.QueryRow(ctx,
		`SELECT count(*) FROM durable_pending_outbox WHERE task_id=$1`, tampered.ID).Scan(&rows))
	require.Equal(t, 1, rows, "failed row must survive for retry")
}

// TestPGActiveCountsAndEvents：ActiveTaskCounts 权威计数与事件链完整。
func TestPGActiveCountsAndEvents(t *testing.T) {
	store := startDurablePG(t)
	ctx := context.Background()

	task := createITTask(t, store, "counts", time.Now().Add(2*time.Hour))
	counts, err := store.ActiveTaskCounts(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), counts["tenant-it"])

	var events int
	require.NoError(t, store.db.QueryRow(ctx,
		`SELECT count(*) FROM durable_llm_task_events WHERE request_id=$1`, task.RequestID).Scan(&events))
	require.Equal(t, 1, events, "creation must append the accepted→running event")
}
