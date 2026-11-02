//go:build integration

// store_sql_realdb_test.go — SQLStore 的真库验证（P1-1 S1）：
// 迁移 930 建表 + Enqueue 幂等 + Claim/Mark 语义 + 僵尸回收 + Sweep。
//
// 启用：TEST_PG_URL=<DSN> go test -tags=integration ./internal/taskbus/
package taskbus

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/internal/testdb"
)

func newSQLStore(t *testing.T) *SQLStore {
	t.Helper()
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL not set")
	}
	dsn = testdb.Create(t, dsn)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	migration, err := os.ReadFile("../../sql/migrations/startup/930_task_events.sql")
	if err != nil {
		t.Fatal(err)
	}
	// 迁移含 schema_migrations 登记，一次性库先补最小 ledger。
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS public.schema_migrations (
		version TEXT PRIMARY KEY, description TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(migration)); err != nil {
		t.Fatalf("apply migration 930: %v", err)
	}
	return NewSQLStore(pool)
}

func TestSQLStoreEnqueueIdempotent(t *testing.T) {
	s := newSQLStore(t)
	ctx := context.Background()

	first, err := s.Enqueue(ctx, "asm.event.v1", "evt-1", 1, []byte(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	// 同 key 重入 → 返回同一行（幂等去重窗口）。
	second, err := s.Enqueue(ctx, "asm.event.v1", "evt-1", 1, []byte(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || second.Status != StatusPending {
		t.Fatalf("idempotent enqueue: first=%+v second=%+v", first, second)
	}
	// 不同 task_type 同 key 不冲突（唯一键是 (type,key)）。
	other, err := s.Enqueue(ctx, "cache.save.v1", "evt-1", 1, []byte{})
	if err != nil || other.ID == first.ID {
		t.Fatalf("cross-type same key must not conflict: %+v err=%v", other, err)
	}
}

func TestSQLStoreClaimMarkLifecycle(t *testing.T) {
	s := newSQLStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if _, err := s.Enqueue(ctx, "asm.event.v1", "evt-l1", 1, []byte("p")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(ctx, "asm.event.v1", "evt-l2", 1, []byte("p")); err != nil {
		t.Fatal(err)
	}

	// Claim 拿到两行；同 now 再 claim 不重复（claimed 锁）。
	batch, err := s.ClaimBatch(ctx, 10, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 2 {
		t.Fatalf("claim batch = %d, want 2", len(batch))
	}
	again, _ := s.ClaimBatch(ctx, 10, now)
	if len(again) != 0 {
		t.Fatalf("claimed rows must not re-claim, got %d", len(again))
	}

	// MarkSent 一条，MarkFailed 一条（带退避）。
	if err := s.MarkSent(ctx, batch[0].ID); err != nil {
		t.Fatal(err)
	}
	retryAt := now.Add(time.Minute)
	if err := s.MarkFailed(ctx, batch[1].ID, 1, retryAt, "boom"); err != nil {
		t.Fatal(err)
	}

	// failed 且未到 next_retry_at ⇒ 不可 claim。
	got, _ := s.ClaimBatch(ctx, 10, now.Add(10*time.Second))
	if len(got) != 0 {
		t.Fatalf("failed-before-retry must not claim, got %d", len(got))
	}
	// 到期后可 claim（僵尸回收不影响：claimed_at 已被 MarkFailed 清空）。
	got, _ = s.ClaimBatch(ctx, 10, now.Add(2*time.Minute))
	if len(got) != 1 || got[0].ID != batch[1].ID || got[0].Attempts != 1 || got[0].LastError != "boom" {
		t.Fatalf("retry claim = %+v", got)
	}
	// MarkDLQ 终态。
	if err := s.MarkDLQ(ctx, batch[1].ID, "gave up"); err != nil {
		t.Fatal(err)
	}
	n, _ := s.CountByStatus(ctx, "asm.event.v1", StatusSent)
	if n != 1 {
		t.Fatalf("sent count = %d", n)
	}
	n, _ = s.CountByStatus(ctx, "asm.event.v1", StatusDLQ)
	if n != 1 {
		t.Fatalf("dlq count = %d", n)
	}
}

func TestSQLStoreZombieClaimRecovery(t *testing.T) {
	s := newSQLStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := s.Enqueue(ctx, "asm.event.v1", "evt-z1", 1, []byte{}); err != nil {
		t.Fatal(err)
	}
	batch, err := s.ClaimBatch(ctx, 1, now)
	if err != nil || len(batch) != 1 {
		t.Fatalf("claim = %+v err=%v", batch, err)
	}
	// 模拟持有者死亡：claim 后不动，超过 claimTimeout ⇒ 重新可 claim。
	future := now.Add(DefaultSQLStoreClaimTimeout + time.Minute)
	recovered, err := s.ClaimBatch(ctx, 1, future)
	if err != nil || len(recovered) != 1 || recovered[0].ID != batch[0].ID {
		t.Fatalf("zombie recovery = %+v err=%v", recovered, err)
	}
}

func TestSQLStoreSweepTerminalRows(t *testing.T) {
	s := newSQLStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := s.Enqueue(ctx, "asm.event.v1", "evt-s1", 1, []byte{}); err != nil {
		t.Fatal(err)
	}
	batch, _ := s.ClaimBatch(ctx, 1, now)
	if len(batch) != 1 {
		t.Fatal("claim failed")
	}
	if err := s.MarkSent(ctx, batch[0].ID); err != nil {
		t.Fatal(err)
	}
	// 窗口内不回收（幂等去重窗口）。
	n, err := s.Sweep(ctx, 10*time.Minute)
	if err != nil || n != 0 {
		t.Fatalf("in-window sweep = %d err=%v", n, err)
	}
	// 超窗回收。completed_at=now ⇒ retention 负向近似：直接用极小窗口。
	n, err = s.Sweep(ctx, -time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("post-window sweep = %d err=%v", n, err)
	}
	// 幂等 enqueue 在回收后 ⇒ 新行（窗口已过，符合语义）。
	fresh, err := s.Enqueue(ctx, "asm.event.v1", "evt-s1", 1, []byte{})
	if err != nil || fresh.ID == batch[0].ID {
		t.Fatalf("post-sweep enqueue should be fresh: %+v err=%v", fresh, err)
	}
}
