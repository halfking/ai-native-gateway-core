package db

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 真库行为验证：三条本轮新增的目录短路守卫（runbook §10.98.7~§10.98.9）。
//
// 为什么要它：db/*_guard_test.go 全是**文本断言**——它们能证明
// 「源码里写了守卫调用」，证明不了「守卫真的会短路、真的不取锁」。
// 这次要证明的是**行为**：
//
//	A 守卫在列/索引缺失时返回 false（必须走 DDL，绝不能跳过）
//	B 列/索引齐备后守卫返回 true（必须短路）
//	C ★ 决定性：无守卫的 DDL 在并发读存在时**真的会被锁阻塞**；
//	       守卫后的 ensure **不再被阻塞**
//	D 负控：C 里的「被阻塞」必须由同一份 DDL 直接执行复现出来，
//	       否则 C 可能恒真（锁没冲突 / 夹具没建好 / 超时没生效）
//
// C 与 D 是一对：只有 D 先证明「这条 DDL 在这个夹具下确实会超时」，
// C 的「没超时」才有意义。否则 C 可能只是「根本没锁冲突」。
//
// 无 TEST_DATABASE_URL / TEST_DB_URL 时跳过（与 749/750 真库回归同门控）。
// 纪律：本测试只跑一次性 scratch 容器，绝不连 154/245/252。
// guardEnv 是行为验证的共享脚手架：一次性真库 + 并发持锁 + 带 lock_timeout 的执行。
// 两组测试（§10.98.7~§10.98.9 新增的三条、§10.98.12 更早的三条）共用同一套读数，
// 避免"新验证自己另发明一套判据"导致两边的通过/失败不可比。
type guardEnv struct {
	dsn  string
	ctx  context.Context
	pool *pgxpool.Pool
	d    *DB
}

// holdFor 必须明显大于 lockTimeout，否则负控可能只是"还没等到冲突"。
const (
	guardHoldFor    = "2.5" // seconds, as a pg_sleep literal
	guardLockTimout = 1200 * time.Millisecond
)

func newGuardEnv(t *testing.T) *guardEnv {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过真库行为验证")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("connect real db: %v", err)
	}
	t.Cleanup(pool.Close)
	return &guardEnv{dsn: dsn, ctx: ctx, pool: pool, d: &DB{pool: pool}}
}

func (e *guardEnv) exec(t *testing.T, q string) {
	t.Helper()
	if _, err := e.pool.Exec(e.ctx, q); err != nil {
		t.Fatalf("exec failed: %v\nSQL: %s", err, q)
	}
}

func (e *guardEnv) drop(t *testing.T, tables string) {
	t.Helper()
	e.exec(t, "DROP TABLE IF EXISTS "+tables+" CASCADE;")
}

func (e *guardEnv) holdAccessShare(t *testing.T, rel string, sleepLiteral string) func() {
	t.Helper()
	conn, err := pgx.Connect(e.ctx, e.dsn)
	if err != nil {
		t.Fatalf("second connection: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	ready := make(chan struct{})
	go func() {
		defer wg.Done()
		defer conn.Close(context.Background())
		tx, err := conn.Begin(context.Background())
		if err != nil {
			return
		}
		defer tx.Rollback(context.Background())
		// 真正扫一遍表，锁在此刻取得
		if _, err := tx.Exec(context.Background(), "SELECT count(*) FROM "+rel); err != nil {
			return
		}
		close(ready) // 锁已持有
		_, _ = tx.Exec(context.Background(), "SELECT pg_sleep("+sleepLiteral+")")
		_ = tx.Commit(context.Background())
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("并发读会话没能在 5 秒内取得 ACCESS SHARE")
	}
	return func() { wg.Wait() }
}

// runWithLockTimeout 在一条新连接上设 lock_timeout 后执行，返回错误文本（空 = 成功）。
//
// ★ 用 lock_timeout 而不是 statement_timeout：后者触发时 PG 报
// 57014 canceling statement，对「等锁」和「跑太久」不加区分；
// lock_timeout 只管锁等待，超时必然是 55P03 lock_not_available，
// 因此「这条 DDL 被并发读挡住了」是无歧义的读数。
func (e *guardEnv) runWithLockTimeout(t *testing.T, sqlText string) string {
	t.Helper()
	conn, err := pgx.Connect(e.ctx, e.dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(e.ctx,
		"SET lock_timeout = '"+guardLockTimout.String()+"'"); err != nil {
		t.Fatalf("set lock_timeout: %v", err)
	}
	if _, err := conn.Exec(e.ctx, sqlText); err != nil {
		return err.Error()
	}
	return ""
}

// expectLocked 是负控的判据：这条 DDL 在并发读持锁时必须被挡住。
func (e *guardEnv) expectLocked(t *testing.T, rel string, sqlText string) {
	t.Helper()
	wait := e.holdAccessShare(t, rel, guardHoldFor)
	defer wait()
	errText := e.runWithLockTimeout(t, sqlText)
	if errText == "" {
		t.Fatal("负控失败：并发读持锁时这条 DDL 竟然没被阻塞 ⇒ " +
			"夹具/超时没生效，后面 C 的「没阻塞」就没有意义")
	}
	if !strings.Contains(errText, "55P03") {
		t.Errorf("负控期望 55P03 等锁超时，实际是: %s", errText)
	}
}

func TestStartupDDLGuards_RealDB(t *testing.T) {
	env := newGuardEnv(t)
	ctx, pool, d := env.ctx, env.pool, env.d
	exec := func(t *testing.T, q string) { env.exec(t, q) }
	holdAccessShare := func(t *testing.T, rel, lit string) func() {
		return env.holdAccessShare(t, rel, lit)
	}
	runWithLockTimeout := func(t *testing.T, sqlText string) string {
		return env.runWithLockTimeout(t, sqlText)
	}
	dropAll := func(t *testing.T) {
		env.drop(t, "provider_models, providers, credentials, session_summaries, "+
			"goal_sessions, schema_migrations")
	}

	t.Run("provider_models_canonical_cleared_at", func(t *testing.T) {
		dropAll(t)
		exec(t, `CREATE TABLE public.provider_models (
			id BIGSERIAL PRIMARY KEY, tenant_id TEXT, canonical_id TEXT);`)
		// ensure 的 migration stamp 要写这张表
		exec(t, `CREATE TABLE public.schema_migrations (
			version TEXT PRIMARY KEY, description TEXT);`)

		t.Run("A_守卫在缺列时必须返回false", func(t *testing.T) {
			if d.providerModelsCanonicalClearedAtCurrent(ctx) {
				t.Error("列还不存在，守卫却返回 true ⇒ 会跳过 DDL，真缺列的库永远补不上")
			}
		})

		t.Run("DDL_负控_无守卫时必须被并发读阻塞", func(t *testing.T) {
			wait := holdAccessShare(t, "provider_models", guardHoldFor)
			defer wait()
			errText := runWithLockTimeout(t, providerModelsCanonicalClearedAtDDL)
			if errText == "" {
				t.Fatal("负控失败：并发读持锁时这条 DDL 竟然没被阻塞 ⇒ " +
					"夹具/超时没生效，后面 C 的「没阻塞」就没有意义")
			}
			// 55P03 = lock_not_available，由 lock_timeout 触发 ⇒ 无歧义的等锁证据
			if !strings.Contains(errText, "55P03") {
				t.Errorf("负控期望 55P03 等锁超时，实际是: %s", errText)
			}
		})

		t.Run("ensure_首次必须把列与注释都建出来", func(t *testing.T) {
			if err := d.ensureProviderModelsCanonicalClearedAt(ctx); err != nil {
				t.Fatalf("ensure: %v", err)
			}
			var hasCol, hasComment bool
			if err := pool.QueryRow(ctx, `
				SELECT EXISTS (SELECT 1 FROM information_schema.columns
				                WHERE table_schema='public' AND table_name='provider_models'
				                  AND column_name='canonical_cleared_at'),
				       col_description('public.provider_models'::regclass,
				                       (SELECT attnum FROM pg_attribute
				                         WHERE attrelid='public.provider_models'::regclass
				                           AND attname='canonical_cleared_at')) IS NOT NULL
			`).Scan(&hasCol, &hasComment); err != nil {
				t.Fatal(err)
			}
			if !hasCol {
				t.Error("首次 ensure 之后列仍不存在")
			}
			if !hasComment {
				t.Error("首次 ensure 之后列注释仍不存在 —— 只守 ALTER 是不够的，COMMENT 同样要取锁")
			}
		})

		t.Run("B_齐备后守卫必须返回true", func(t *testing.T) {
			if !d.providerModelsCanonicalClearedAtCurrent(ctx) {
				t.Error("列与注释都已存在，守卫却返回 false ⇒ 守卫无效，每次启动仍白取两次锁")
			}
		})

		t.Run("C_决定性_守卫后并发读下ensure必须立即成功", func(t *testing.T) {
			wait := holdAccessShare(t, "provider_models", guardHoldFor)
			defer wait()
			start := time.Now()
			if err := d.ensureProviderModelsCanonicalClearedAt(ctx); err != nil {
				t.Fatalf("守卫后仍被阻塞或报错: %v", err)
			}
			if elapsed := time.Since(start); elapsed > guardLockTimout {
				t.Errorf("守卫后耗时 %v，超过负控的阻塞时长 %v ⇒ 守卫没有真正短路",
					elapsed, guardLockTimout)
			}
		})

		t.Run("stamp_两条路径都要落台账", func(t *testing.T) {
			var n int
			if err := pool.QueryRow(ctx,
				`SELECT count(*) FROM public.schema_migrations WHERE version='693'`).
				Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 1 {
				t.Errorf("schema_migrations 693 应恰好 1 行，实际 %d 行（重复写入或漏写）", n)
			}
		})
	})

	t.Run("provider_soft_delete", func(t *testing.T) {
		dropAll(t)
		exec(t, `CREATE TABLE public.providers (
			id BIGSERIAL PRIMARY KEY, tenant_id TEXT, name TEXT);`)
		// ensureProviderSoftDelete 的另一半是 credentials 的 CHECK 约束批次，
		// 夹具里必须真的有这张表，否则测到的是 42P01 而不是锁行为。
		exec(t, `CREATE TABLE public.credentials (
			id BIGSERIAL PRIMARY KEY, status TEXT);`)

		t.Run("A_守卫在缺列时必须返回false", func(t *testing.T) {
			if d.providerSoftDeleteCurrent(ctx) {
				t.Error("deleted_at 与 idx_providers_live 都不存在，守卫却返回 true")
			}
		})

		t.Run("DDL_负控_无守卫时必须被并发读阻塞", func(t *testing.T) {
			wait := holdAccessShare(t, "providers", guardHoldFor)
			defer wait()
			errText := runWithLockTimeout(t, providerSoftDeleteDDL)
			if errText == "" {
				t.Fatal("负控失败：并发读持锁时这条 DDL 竟然没被阻塞")
			}
		})

		t.Run("ensure_首次必须建出列与部分索引", func(t *testing.T) {
			if err := d.ensureProviderSoftDelete(ctx); err != nil {
				t.Fatalf("ensure: %v", err)
			}
			if !d.providerSoftDeleteCurrent(ctx) {
				t.Error("首次 ensure 之后守卫仍为 false ⇒ 列或索引没建出来")
			}
		})

		t.Run("B_齐备后守卫必须返回true", func(t *testing.T) {
			if !d.providerSoftDeleteCurrent(ctx) {
				t.Error("列与索引都已存在，守卫却返回 false")
			}
		})

		t.Run("C_决定性_守卫后并发读下ensure必须立即成功", func(t *testing.T) {
			wait := holdAccessShare(t, "providers", guardHoldFor)
			defer wait()
			start := time.Now()
			if err := d.ensureProviderSoftDelete(ctx); err != nil {
				t.Fatalf("守卫后仍被阻塞或报错: %v", err)
			}
			if elapsed := time.Since(start); elapsed > guardLockTimout {
				t.Errorf("守卫后耗时 %v，超过负控阻塞时长 %v", elapsed, guardLockTimout)
			}
		})

		t.Run("缺索引时守卫必须仍为false", func(t *testing.T) {
			// 只守列不守索引 ⇒ 有列但缺索引的库会被永久跳过。
			exec(t, `DROP INDEX public.idx_providers_live;`)
			if d.providerSoftDeleteCurrent(ctx) {
				t.Error("索引被删后守卫仍返回 true ⇒ 缺索引的库永远补不上索引")
			}
		})
	})

	t.Run("goal_client_signal", func(t *testing.T) {
		dropAll(t)
		exec(t, `CREATE TABLE public.goal_sessions (id BIGSERIAL PRIMARY KEY);`)
		exec(t, `CREATE TABLE public.session_summaries (id BIGSERIAL PRIMARY KEY,
			tenant_id TEXT, summary TEXT);`)

		t.Run("A_守卫在缺列时必须返回false", func(t *testing.T) {
			if d.goalClientSignalCurrent(ctx) {
				t.Error("两张表的列都不存在，守卫却返回 true")
			}
		})

		t.Run("DDL_负控_无守卫时必须被并发读阻塞", func(t *testing.T) {
			wait := holdAccessShare(t, "session_summaries", guardHoldFor)
			defer wait()
			errText := runWithLockTimeout(t, goalClientSignalDDL)
			if errText == "" {
				t.Fatal("负控失败：并发读持锁时这条 DDL 竟然没被阻塞")
			}
		})

		t.Run("ensure_首次必须建出两表列与索引", func(t *testing.T) {
			if err := d.ensureGoalClientSignalSchema(ctx); err != nil {
				t.Fatalf("ensure: %v", err)
			}
			if !d.goalClientSignalCurrent(ctx) {
				t.Error("首次 ensure 之后守卫仍为 false")
			}
		})

		t.Run("B_齐备后守卫必须返回true", func(t *testing.T) {
			if !d.goalClientSignalCurrent(ctx) {
				t.Error("列与索引都已存在，守卫却返回 false")
			}
		})

		t.Run("C_决定性_守卫后并发读下ensure必须立即成功", func(t *testing.T) {
			wait := holdAccessShare(t, "session_summaries", guardHoldFor)
			defer wait()
			start := time.Now()
			if err := d.ensureGoalClientSignalSchema(ctx); err != nil {
				t.Fatalf("守卫后仍被阻塞或报错: %v", err)
			}
			if elapsed := time.Since(start); elapsed > guardLockTimout {
				t.Errorf("守卫后耗时 %v，超过负控阻塞时长 %v", elapsed, guardLockTimout)
			}
		})

		t.Run("只缺session_summaries一半时守卫必须为false", func(t *testing.T) {
			// 整体判定：一张表齐备不代表可以跳过。
			exec(t, `ALTER TABLE public.goal_sessions DROP COLUMN sub_agents_pending;`)
			if d.goalClientSignalCurrent(ctx) {
				t.Error("goal_sessions 缺列时守卫仍返回 true ⇒ 分表判定会留下半迁移的库")
			}
		})
	})
}
