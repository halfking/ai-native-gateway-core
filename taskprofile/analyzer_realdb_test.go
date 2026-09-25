package taskprofile

// analyzer_realdb_test.go — generate 去重临界区的真库契约回归（R64 P1）。
//
// 钉住的行为：insertDraftsGuarded 在单事务内先取 pg_advisory_xact_lock
// （tuningProposalLockKey，专用于 tuning proposal generate 去重）再逐条
// EXISTS→INSERT——tuning_proposals 无业务唯一约束，两个并发生成方（定时
// analyzer + admin 手动 generate）不得各自通过探针后重复落 pending。
//
// 环境门控：无 TEST_DATABASE_URL / TEST_DB_URL 即跳过（与 db/
// db_745_ensure_realdb_test.go 同款门控）。纪律：修复子代理禁连库——本
// 测试只随协调者的 scratch PG 单点执行（只写不跑）。不跑 migrations 全链，
// 仅按 sql/migrations/startup/005_tuning_proposals.sql 的 DDL 口径建最小
// 夹具表（IF NOT EXISTS：迁移已在位时零操作），行级隔离用专用 category。

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestInsertDraftsGuarded_ConcurrentDedup_RealDB(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过真库契约回归")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("connect real db: %v", err)
	}
	defer pool.Close()

	// 最小夹具：覆盖被测 SQL 触及的全部列（status/category/task_type/
	// proposal/evidence + 默认列），DDL 口径照抄 005_tuning_proposals.sql。
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS tuning_proposals (
			id          BIGSERIAL PRIMARY KEY,
			ts          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			category    TEXT NOT NULL,
			task_type   TEXT,
			proposal    JSONB NOT NULL,
			evidence    JSONB NOT NULL,
			status      TEXT NOT NULL DEFAULT 'pending',
			reviewed_by TEXT,
			reviewed_at TIMESTAMPTZ,
			applied_at  TIMESTAMPTZ,
			review_note TEXT,
			created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`); err != nil {
		t.Fatalf("create tuning_proposals fixture: %v", err)
	}

	// 行级隔离：测试专用 category，先清残留、结束后由 t.Cleanup 再清一次。
	const cat = "r64_realdb_contract"
	cleanup := func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM tuning_proposals WHERE category = $1`, cat)
	}
	cleanup()
	t.Cleanup(cleanup)

	newDraft := func() *ProposalDraft {
		return &ProposalDraft{
			Category: cat,
			TaskType: "r64fix",
			Proposal: map[string]any{
				"key":     "keywords.reasoning",
				"add":     []string{"r64并发令牌"},
				"channel": "reasoning",
			},
			Evidence: map[string]any{"source": "r64_realdb_contract_test"},
		}
	}

	// 两个 goroutine 并发各跑一次完整「advisory 锁+去重+插入」路径（直接调
	// insert 层函数），模拟定时 analyzer 与 admin 手动 generate 撞车。
	var (
		wg       sync.WaitGroup
		inserted = make([]int, 2)
		errs     = make([]error, 2)
	)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := insertDraftsGuarded(ctx, pool, []*ProposalDraft{newDraft()})
			inserted[i] = len(got)
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d insertDraftsGuarded: %v", i, err)
		}
	}
	if inserted[0]+inserted[1] != 1 {
		t.Errorf("concurrent runs inserted %d+%d proposals, want exactly 1 (advisory-lock dedup broken)",
			inserted[0], inserted[1])
	}

	var pending int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM tuning_proposals
		WHERE category = $1 AND status = 'pending'
	`, cat).Scan(&pending); err != nil {
		t.Fatalf("count pending: %v", err)
	}
	if pending != 1 {
		t.Fatalf("pending rows for %q = %d, want 1", cat, pending)
	}

	// 串行第二轮：pending 已在位 → 探针去重必须拦下（0 插入）。
	got, err := insertDraftsGuarded(ctx, pool, []*ProposalDraft{newDraft()})
	if err != nil {
		t.Fatalf("serial re-run: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("serial re-run inserted %d, want 0 (pending dedup probe broken)", len(got))
	}
}
