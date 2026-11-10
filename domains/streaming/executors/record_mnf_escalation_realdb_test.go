// record_mnf_escalation_realdb_test.go — 2026-11-11 错误族审计 P1 真库行为门：
// recordModelNotFound 的 node_probe_state upsert 从「无条件 now()+5min」改为
// 「GREATEST(阶梯排程, cf≥阈值 ⇒ 1h, 否则 5min)」后的回归门。
//
// 门的三层断言（每层各治一种坏法）：
//  1. 梯子排程不被缩短：已有 next_retry_at = now()+6h（协议类复检档）的行，
//     MNF 再写后 next_retry_at 仍 ≥ now()+5h —— 修的是「5min 塌缩」缺陷本体；
//  2. 持续失败升级：cf=500（≥ 阈值 21）且排程已过期的行，MNF 再写后
//     next_retry_at ≥ now()+55min（1h 档），不再回到 5min；
//  3. 新失败语义逐字保留：cf=0 且排程已过期的行，MNF 写后 next_retry_at
//     在 now()+4min ~ now()+6min 之间（fresh-failure 5min 档不变）。
//
// 无 TEST_DATABASE_URL / TEST_DB_URL 时跳过（bg/*_realdb_test.go 同款模板）。
// 测试用自己的合成 (credential_id, raw_model_name) 行并在 Cleanup 删除；
// node_probe_state 的 credential_id 无外键（schema 341/402），合成 ID 安全。
package executors

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func mnfUpsertRealDBPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过真库回归")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("connect real db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestRecordModelNotFoundStateUpsertEscalationRealDB executes the production
// upsert (recordModelNotFoundStateUpsertSQL) against a real database and
// asserts the three behaviors above. Each sub-case seeds its own row, runs
// the upsert, and checks the resulting next_retry_at.
func TestRecordModelNotFoundStateUpsertEscalationRealDB(t *testing.T) {
	pool := mnfUpsertRealDBPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const synthCred = int64(975311111) // 合成凭据 ID（无 FK，绝不与真实凭据撞）
	sustainedAt := 21                  // 与生产默认对齐；参数走 $5 同生产路径

	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		_, _ = pool.Exec(cctx, `DELETE FROM node_probe_state WHERE credential_id = $1`, synthCred)
	})

	// synthModel 注册一行种子并返回其初始 next_retry_at 偏移（可为负 = 已过期）。
	seed := func(t *testing.T, model string, cf int, schedOffset time.Duration) {
		t.Helper()
		_, err := pool.Exec(ctx, `
			INSERT INTO node_probe_state (
				credential_id, raw_model_name, consecutive_failures, consecutive_successes,
				last_attempt_at, next_retry_at, next_retry_seconds, paused,
				last_direct_ok, last_gateway_ok, last_err_code, updated_at
			) VALUES (
				$1, $2, $3, 0,
				now(), now() + $4::interval, 300, FALSE,
				FALSE, FALSE, 'seed', now()
			)
			ON CONFLICT (credential_id, raw_model_name) DO UPDATE
			SET consecutive_failures = EXCLUDED.consecutive_failures,
			    next_retry_at = EXCLUDED.next_retry_at,
			    last_err_code = 'seed',
			    updated_at = now()
		`, synthCred, model, cf, schedOffset.String())
		if err != nil {
			t.Fatalf("seed %s: %v", model, err)
		}
	}

	runUpsert := func(t *testing.T, model string) {
		t.Helper()
		if _, err := pool.Exec(ctx, recordModelNotFoundStateUpsertSQL(),
			synthCred, model, (5 * time.Minute).String(), "model_not_found", sustainedAt); err != nil {
			t.Fatalf("upsert %s: %v", model, err)
		}
	}

	readRow := func(t *testing.T, model string) (nextRetry time.Time, cf int) {
		t.Helper()
		err := pool.QueryRow(ctx,
			`SELECT next_retry_at, consecutive_failures FROM node_probe_state WHERE credential_id = $1 AND raw_model_name = $2`,
			synthCred, model).Scan(&nextRetry, &cf)
		if err != nil {
			t.Fatalf("read %s: %v", model, err)
		}
		return nextRetry, cf
	}

	t.Run("ladder schedule never shortened", func(t *testing.T) {
		const m = "mnf-gate-ladder-not-shortened"
		seed(t, m, 5, 6*time.Hour) // 协议类复检档：now()+6h，cf 远低于阈值
		runUpsert(t, m)
		nextRetry, cf := readRow(t, m)
		if remaining := time.Until(nextRetry); remaining < 5*time.Hour {
			t.Fatalf("MNF write shortened the probe ladder schedule: next_retry_at has %v remaining, want ≥ 5h (cf now %d)", remaining, cf)
		}
		if cf != 6 {
			t.Fatalf("cf should increment by 1: got %d, want 6", cf)
		}
	})

	t.Run("sustained failure escalates to 1h", func(t *testing.T) {
		const m = "mnf-gate-sustained-1h"
		seed(t, m, 500, -1*time.Minute) // 排程已过期 + cf ≥ 阈值
		runUpsert(t, m)
		nextRetry, _ := readRow(t, m)
		if remaining := time.Until(nextRetry); remaining < 55*time.Minute {
			t.Fatalf("sustained-failure row must re-arm at ≥ 1h (got %v remaining), not collapse to 5min", remaining)
		}
	})

	t.Run("fresh failure keeps 5min arm", func(t *testing.T) {
		const m = "mnf-gate-fresh-5min"
		seed(t, m, 0, -1*time.Minute) // 新失败：排程已过期 + cf=0
		runUpsert(t, m)
		nextRetry, _ := readRow(t, m)
		remaining := time.Until(nextRetry)
		if remaining < 4*time.Minute || remaining > 6*time.Minute {
			t.Fatalf("fresh-failure row must re-arm at ~5min, got %v", remaining)
		}
	})

	t.Run("transient suppression never shortens ladder", func(t *testing.T) {
		const m = "mnf-gate-transient-greatest"
		seed(t, m, 3, 6*time.Hour) // 阶梯未来排程 now()+6h（cf 低于阈值，纯 GREATEST 守卫）
		if _, err := pool.Exec(ctx, recordTransientDispatchFailureStateUpsertSQL(),
			synthCred, m, (5 * time.Minute).String(), "upstream_down"); err != nil {
			t.Fatalf("transient upsert: %v", err)
		}
		nextRetry, cf := readRow(t, m)
		if remaining := time.Until(nextRetry); remaining < 5*time.Hour {
			t.Fatalf("transient suppression shortened the probe ladder schedule: %v remaining, want ≥ 5h (cf now %d)", remaining, cf)
		}
		// 瞬时路径刻意**不**做 cf 升级（可恢复族快复检是恢复 SLA 的承重墙）：
		// cf 高但排程已过期的行仍按 5min 抑制窗口重武装。
		seed(t, m, 500, -1*time.Minute)
		if _, err := pool.Exec(ctx, recordTransientDispatchFailureStateUpsertSQL(),
			synthCred, m, (5 * time.Minute).String(), "upstream_down"); err != nil {
			t.Fatalf("transient upsert 2: %v", err)
		}
		nextRetry, _ = readRow(t, m)
		if remaining := time.Until(nextRetry); remaining > 6*time.Minute {
			t.Fatalf("transient suppression must keep the fast 5min arm for recoverable errors (recovery SLA), got %v", remaining)
		}
	})
}
