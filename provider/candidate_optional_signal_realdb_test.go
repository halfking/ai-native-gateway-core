package provider

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestCandidateOptionalSignal_ChrFunctionMissing_OnRealDB 在真库上复现 2026-10-28 211 事故的
// 触发条件（chr 函数缺失），并验证修复：候选查询不因可选信号失败、不计入熔断、函数补齐后自愈。
//
// ★ 破坏性：会在目标库里 DROP recent_cache_hit_rate。因此只在库名含 "itgate_"（本仓一次性库
// 约定，见 AGENTS.md 真库腿）时运行，其它库一律跳过。需要 TEST_DATABASE_URL。
func TestCandidateOptionalSignal_ChrFunctionMissing_OnRealDB(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — optional-signal degradation needs a real database")
	}
	if !strings.Contains(dsn, "/itgate_") {
		t.Skip("refusing to DROP a function outside a disposable itgate_* database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	const fnIdent = "public.recent_cache_hit_rate(bigint,text,integer,integer)"
	var fnDef string
	if err := pool.QueryRow(ctx, `SELECT pg_get_functiondef(to_regprocedure($1))`, fnIdent).Scan(&fnDef); err != nil {
		t.Fatalf("保存 chr 函数定义失败（库未完整新装？）: %v", err)
	}
	// 恢复必须独立于测试 pool：defer pool.Close() 先于 t.Cleanup 执行，复用 pool 会静默失败，
	// 把库留在"函数缺失"状态（2026-10-31 实测踩到）。恢复失败必须判红而不是吞掉。
	t.Cleanup(func() {
		rctx, rcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer rcancel()
		rp, err := pgxpool.New(rctx, dsn)
		if err != nil {
			t.Errorf("恢复 chr 函数：连接失败，库已处于函数缺失状态: %v", err)
			return
		}
		defer rp.Close()
		if _, err := rp.Exec(rctx, fnDef); err != nil {
			t.Errorf("恢复 chr 函数失败，库已处于函数缺失状态: %v", err)
		}
	})

	if _, err := pool.Exec(ctx, `DROP FUNCTION `+fnIdent); err != nil {
		t.Fatalf("drop chr function: %v", err)
	}

	// 故障模式必须真实存在：全量 SQL 在函数缺失时确实报 42883。
	if _, err := pool.Query(ctx, candidateQuerySQL(), "probe-model", "default", ""); err == nil {
		t.Fatal("前置条件不成立：函数已删除但全量候选 SQL 仍成功，测试无意义")
	} else if !isCacheSignalAbsent(err) {
		t.Fatalf("全量候选 SQL 的失败不是 chr 函数缺失（判据不匹配）: %v", err)
	}

	// 修复后：候选查询成功（可为空集，但不得报错）。
	c := &Client{dbPool: pool}
	if _, err := c.loadCandidatesByModalityDB(ctx, "probe-model", "default", ""); err != nil {
		t.Fatalf("chr 缺失时候选查询不得失败: %v", err)
	}
	if !c.cacheSignal.degradedAt(time.Now()) {
		t.Fatal("chr 缺失后应进入降级态")
	}
	if allow, _ := c.candCircuit.AllowProbe(time.Now()); !allow {
		t.Fatal("可选信号缺失不得计入候选熔断（熔断已打开）")
	}

	// 自愈：恢复函数后，新的 Client（无降级态）应直接走全量 SQL 成功，且不降级。
	if _, err := pool.Exec(ctx, fnDef); err != nil {
		t.Fatalf("restore chr function: %v", err)
	}
	healed := &Client{dbPool: pool}
	if _, err := healed.loadCandidatesByModalityDB(ctx, "probe-model", "default", ""); err != nil {
		t.Fatalf("函数恢复后候选查询失败: %v", err)
	}
	if healed.cacheSignal.degradedAt(time.Now()) {
		t.Fatal("函数存在时不应降级")
	}
}
