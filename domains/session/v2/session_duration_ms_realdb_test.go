package v2

// session_duration_ms_realdb_test.go —— `sessions.duration_ms` 写入方真库门。
//
// # 这道门守的是什么
//
// 706 声明了 `sessions.duration_ms BIGINT`，列注释写「关闭时
// closed_at - created_at」。但那条注释描述的写入点**全仓零调用方**：
// `CloseSession`（session_aggregator.go:427）没有任何生产调用方
// ——2026-09-14 审计已把这一事实记在 session_writer_v2.go:744 与
// bodies_writer.go:213（两处都在讲「没有关闭链路」这同一件事）。
//
// ⇒ 该列在生产上**恒为 NULL**。这是真缺陷，但成因不是「零 Go 引用」
// （CloseSession 确实写它），而是「唯一写入方不可达」。
//
// # 为什么这道门必须是真库门，不能是 SQL 形状锁
//
// pgxmock 的形状锁只能证明「SQL 里有 duration_ms 这个词」，证明不了
// 「写进去的值是对的」。判据纪律：判据必须在与被测函数同一层。
// `upsertSessionSnapshot` 是一条真 SQL，它对 created_at 的取法有两个
// 都能通过形状锁、但结果完全不同的写法：
//
//	EXCLUDED.updated_at - EXCLUDED.created_at   ← 恒为 0（每轮起点被重置到"现在"）
//	EXCLUDED.updated_at - sessions.created_at   ← 真实流逝时长
//
// 只有真跑一遍、再把列读回来，才能分辨。所以本门建夹具、真调
// upsertSessionSnapshot、真读 duration_ms。
//
// # 门控（沿用本包既有约定）
//
//	TEST_DATABASE_URL=postgres://user:pass@host:port/db?sslmode=disable \
//	go test ./domains/session/v2/ -run TestSessionDurationMs -v -count=1
//
// ⚠ 夹具走一次性数据库（statusBackfillFixtureDB），**不碰生产库**：
//   本函数的 SQL 是 public.-限定的，而更重要的是 t.Cleanup 晚于 defer 关池
//   这条本仓踩过的坑（同包 session_request_status_backfill_test.go 头注）。
//   一次性库从根上消掉残留，而不是靠清理。

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// durationMsFixtureSQL 是 upsertSessionSnapshot 真正触及的那些列。
// 少一列这道门就会以 42703 红——那正是「判据与被测函数同层」的体现：
// 夹具缺列 = 被测 SQL 缺列，两者是同一件事。
const durationMsFixtureSQL = `
CREATE TABLE public.sessions (
	session_id             TEXT    NOT NULL,
	tenant_id              TEXT    NOT NULL,
	-- 706:124 是 NOT NULL DEFAULT NOW()，夹具必须同形：
	-- 声明成可空会让生产上不可达的 NULL 分支在测试里变得可达。
	created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at             TIMESTAMPTZ,
	status                 TEXT,
	total_turns            INTEGER NOT NULL DEFAULT 0,
	total_tokens           INTEGER NOT NULL DEFAULT 0,
	total_cost_usd         NUMERIC NOT NULL DEFAULT 0,
	last_turn_no           INTEGER,
	last_request_summary   TEXT,
	last_response_summary  TEXT,
	last_model             TEXT,
	last_provider          TEXT,
	client_type            TEXT,
	project_id             TEXT,
	api_key_id             TEXT,
	application_id         TEXT,
	end_user_id            TEXT,
	owner_user             TEXT,
	client_ip              TEXT,
	agent_name             TEXT,
	agent_role             TEXT    NOT NULL DEFAULT 'main',
	parent_session_id      TEXT,
	parent_task_id         TEXT,
	partition_date         DATE    NOT NULL,
	primary_request_id     TEXT,
	duration_ms            BIGINT,
	UNIQUE (session_id, partition_date)
);
`

// TestSessionDurationMsIsWrittenOnEveryTurn 是本题的核心判据：
// 连写两轮后 duration_ms 必须等于「第二轮时间 - 首轮时间」。
//
// 三条断言缺一不可：
//  1. 非 NULL —— 挡住「仍是永久 NULL」这个原缺陷；
//  2. 值等于真实流逝 —— 挡住上面那个 EXCLUDED-created_at 恒 0 的写法；
//  3. 单调 —— 挡住每轮把起点重置（那会让它恒 0 或乱跳）。
func TestSessionDurationMsIsWrittenOnEveryTurn(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping real-database gate")
	}
	ctx := context.Background()

	pool, name := statusBackfillFixtureDB(t, dsn)
	t.Cleanup(func() {
		// 一次性库：在这里关池，disconnect 后整库 DROP。
		pool.Close()
		admin, err := pgxpool.New(ctx, dsn)
		if err != nil {
			t.Errorf("reconnect admin to drop fixture db: %v", err)
			return
		}
		defer admin.Close()
		if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop fixture db %s: %v", name, err)
		}
	})

	if _, err := pool.Exec(ctx, durationMsFixtureSQL); err != nil {
		t.Fatalf("fixture DDL: %v", err)
	}

	base := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	part := base.Truncate(24 * time.Hour)

	// 第一轮：建行。此时 created_at == updated_at，已流逝应为 0。
	require.NoError(t, upsertSessionSnapshot(ctx, pool, SessionUpdate{
		SessionID:     "sess-dur",
		TenantID:      "tenant-1",
		TurnIncrement: 1,
		UpdatedAt:     base,
	}, part))

	var after1 *int64
	if err := pool.QueryRow(ctx,
		`SELECT duration_ms FROM public.sessions WHERE session_id='sess-dur'`).Scan(&after1); err != nil {
		t.Fatalf("read duration_ms after turn 1: %v", err)
	}
	require.NotNil(t, after1, "turn 1: duration_ms must not be NULL (706 declares it, CloseSession is unreachable)")
	require.Equal(t, int64(0), *after1, "turn 1: a freshly created session has zero elapsed")

	// 第二轮：+90 秒。冲突臂必须按存量 created_at 算，而不是重置起点。
	second := base.Add(90 * time.Second)
	require.NoError(t, upsertSessionSnapshot(ctx, pool, SessionUpdate{
		SessionID:     "sess-dur",
		TenantID:      "tenant-1",
		TurnIncrement: 1,
		UpdatedAt:     second,
	}, part))

	var after2 *int64
	if err := pool.QueryRow(ctx,
		`SELECT duration_ms FROM public.sessions WHERE session_id='sess-dur'`).Scan(&after2); err != nil {
		t.Fatalf("read duration_ms after turn 2: %v", err)
	}
	require.NotNil(t, after2, "turn 2: duration_ms must not be NULL")
	require.Equal(t, int64(90_000), *after2,
		"turn 2: duration_ms must be EXCLUDED.updated_at - sessions.created_at (90s), "+
			"not EXCLUDED.updated_at - EXCLUDED.created_at (which resets the origin to now and yields 0)")

	// 第三轮：+30 秒 ⇒ 120 秒。单调性断言：起点必须始终是首写 created_at。
	third := second.Add(30 * time.Second)
	require.NoError(t, upsertSessionSnapshot(ctx, pool, SessionUpdate{
		SessionID:     "sess-dur",
		TenantID:      "tenant-1",
		TurnIncrement: 1,
		UpdatedAt:     third,
	}, part))

	var after3 *int64
	if err := pool.QueryRow(ctx,
		`SELECT duration_ms FROM public.sessions WHERE session_id='sess-dur'`).Scan(&after3); err != nil {
		t.Fatalf("read duration_ms after turn 3: %v", err)
	}
	require.NotNil(t, after3, "turn 3: duration_ms must not be NULL")
	require.Equal(t, int64(120_000), *after3,
		"turn 3: elapsed must accumulate from the FIRST write's created_at, not from the previous turn")
}
