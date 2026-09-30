// benchreport —— 对帐多维读面的长区间耗时实测（可复现的验证工具）。
//
// 为什么它是仓内资产而不是一次性脚本：迁移 759 加两个
// `(report_date) WHERE scope = 'daily_grain' / 'internal_grain'` partial 索引
// 的唯一理由，就是这个工具第一次跑出来的那组执行计划——见迁移文件里
// 引用的实测数字。判断「读面扫描是否退化成全表扫描」需要能随时重跑，
// 结论写进注释但工具被删掉，等于下一个人没法复核。
//
// 用法（只允许本机库，工具自检）：
//
//	DSN="postgres://user:pass@127.0.0.1:5432/<db>?sslmode=disable" \
//	  go run ./tools/benchreport
//
// 可选：SKIP_SEED=1 复用已灌好的 scratch schema（避开刚灌完的冷缓存，
// 否则量到的永远是 I/O 而不是查询本身）；DAYS / ROWS_PER_DAY 控制规模。
//
// 规模默认值取的是本地真库实测的维度基数：grain 约 1000 行/日、六维基数
// 3/6/3/4/12/5（供应商/凭据/租户/用户/apikey/模型），internal_grain 按
// business 流量占 85% 抽样，与两个视角的真实行数比例一致。
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domains/reportrollup"
)

const schema = "report_bench"

var (
	days       = envInt("DAYS", 366)
	rowsPerDay = envInt("ROWS_PER_DAY", 1000)
)

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		n := 0
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func main() {
	dsn := os.Getenv("DSN")
	if dsn == "" {
		panic("DSN required，例如 DSN=postgres://user:pass@127.0.0.1:5432/llm_gateway?sslmode=disable")
	}
	if !strings.Contains(dsn, "127.0.0.1") && !strings.Contains(dsn, "localhost") {
		panic("refusing to run against a non-local database: " + dsn)
	}
	ctx := context.Background()

	// search_path 挂在 AfterConnect 上（连接是复用的），并**追加**原值：
	// 本机装了 citus_columnar，其 ddl_command_end 事件触发器要解析
	// columnar_insert_only_parents()，收窄 search_path 会让建表直接 42883。
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		panic(err)
	}
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		var orig string
		if err := c.QueryRow(ctx, `SHOW search_path`).Scan(&orig); err != nil {
			return err
		}
		_, err := c.Exec(ctx, "SET search_path TO "+schema+", "+orig)
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		panic(err)
	}
	defer pool.Close()

	// SKIP_SEED=1 = 表已在、只想复测耗时；此时不能 DROP，否则刚灌完的行
	// 被丢掉又重灌，量到的永远是冷缓存。
	warmOnly := os.Getenv("SKIP_SEED") == "1"
	if !warmOnly {
		if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
			panic(err)
		}
		if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
			panic(err)
		}
		if _, err := pool.Exec(ctx, benchDDL); err != nil {
			panic(err)
		}
		if os.Getenv("NO_INDEX") == "1" {
			fmt.Println("NO_INDEX=1 —— 故意不建 grain 日期索引，用来复现迁移 759 之前的行为")
		} else if _, err := pool.Exec(ctx, benchIndexes); err != nil {
			panic(err)
		}
	}

	var rows int
	if !warmOnly {
		t0 := time.Now()
		if _, err := pool.Exec(ctx, benchSeed, days, rowsPerDay); err != nil {
			panic(err)
		}
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM report_snapshots").Scan(&rows); err != nil {
			panic(err)
		}
		fmt.Printf("seeded %d grain rows (%d days × %d rows/day) in %s\n\n",
			rows, days, rowsPerDay, time.Since(t0).Round(time.Millisecond))
	} else {
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM report_snapshots").Scan(&rows); err != nil {
			panic(err)
		}
		fmt.Printf("reusing %d rows in schema %s (warm cache run)\n\n", rows, schema)
	}

	names := reportrollup.Names{
		Providers:   map[int64]string{},
		Credentials: map[int64]string{},
		APIKeys:     map[int64]string{},
	}
	end := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	for _, span := range spansToMeasure() {
		if span > days {
			continue
		}
		start := end.AddDate(0, 0, -(span - 1))
		for _, view := range []reportrollup.View{reportrollup.ViewProvider, reportrollup.ViewInternal} {
			for _, daily := range []bool{false, true} {
				med, groups, err := median(ctx, pool, start, end, view, daily, names)
				if err != nil {
					fmt.Printf("span=%-4d view=%-8s detail=%-5v ERROR %v\n", span, view, daily, err)
					continue
				}
				fmt.Printf("span=%-4d view=%-8s detail=%-5v median=%-8s groups=%d\n",
					span, view, daily, med.Round(time.Millisecond), groups)
			}
		}
	}
	fmt.Println("\n提示：绝对耗时强依赖 shared_buffers 与存储。判断「扫描是否退化成全表扫描」")
	fmt.Println("要看计划形状而不是耗时——EXPLAIN 里是 Index Scan 还是 Seq Scan。")
	fmt.Println("A/B 复现迁移 759 的依据：DAYS=40 go run ./tools/benchreport  vs  NO_INDEX=1 同参数。")
}

// spansToMeasure 支持 SPANS="7,30" 只测指定区间——366 天规模下整轮矩阵要
// 十几分钟，做 A/B 对照时先只留关心的一档。
func spansToMeasure() []int {
	raw := os.Getenv("SPANS")
	if raw == "" {
		return []int{7, 30, 90, 366}
	}
	var out []int
	for _, part := range strings.Split(raw, ",") {
		if n := envInt(strings.TrimSpace(part), 0); n > 0 {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return []int{7, 30, 90, 366}
	}
	return out
}

// median 重复 3 次取中位数：单次测量会被缓存冷热带偏，中位数才可比较。
func median(ctx context.Context, pool *pgxpool.Pool, start, end time.Time, view reportrollup.View, daily bool, names reportrollup.Names) (time.Duration, int, error) {
	var samples []time.Duration
	groups := 0
	for i := 0; i < 3; i++ {
		t := time.Now()
		rep, err := reportrollup.BuildGrainReport(ctx, pool, start, end, view, reportrollup.GrainFilter{}, names, daily)
		el := time.Since(t)
		if err != nil {
			return 0, 0, err
		}
		groups = len(rep.Providers) + len(rep.Credentials) + len(rep.APIKeys) +
			len(rep.ModelTotals) + len(rep.Tenants) + len(rep.Persons) + len(rep.DailyModels)
		samples = append(samples, el)
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	return samples[len(samples)/2], groups, nil
}

const benchDDL = `
CREATE TABLE report_snapshots (
    id                  BIGSERIAL PRIMARY KEY,
    scope               TEXT NOT NULL,
    scope_key           TEXT NOT NULL,
    report_date         DATE NOT NULL,
    raw_model_name      TEXT NOT NULL DEFAULT '',
    request_count       BIGINT NOT NULL DEFAULT 0,
    success_count       BIGINT NOT NULL DEFAULT 0,
    error_count         BIGINT NOT NULL DEFAULT 0,
    input_tokens        BIGINT NOT NULL DEFAULT 0,
    output_tokens       BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens   BIGINT NOT NULL DEFAULT 0,
    cache_write_tokens  BIGINT NOT NULL DEFAULT 0,
    error_kind_breakdown JSONB NOT NULL DEFAULT '{}'::jsonb,
    cache_hit_ratio     NUMERIC(6,4),
    estimated_cost_cents BIGINT NOT NULL DEFAULT 0,
    currency            TEXT NOT NULL DEFAULT 'USD',
    price_snapshot      JSONB NOT NULL DEFAULT '{}'::jsonb,
    provider_id         BIGINT,
    tenant_id           TEXT,
    credential_id       BIGINT,
    api_key_id          BIGINT,
    person              TEXT,
    credits_charged     BIGINT NOT NULL DEFAULT 0,
    latency_p50_ms      BIGINT NOT NULL DEFAULT 0,
    latency_p95_ms      BIGINT NOT NULL DEFAULT 0,
    CONSTRAINT report_snapshots_scope_key_date_raw_model_key
        UNIQUE (scope, scope_key, report_date, raw_model_name)
)`

// benchIndexes 与迁移 759 的两个日期 partial 索引一致。NO_INDEX=1 跳过它们，
// 即可 A/B 复现「规划器因 scope=ANY 行数估算失真而选全表顺序扫描」。
const benchIndexes = `
CREATE INDEX idx_bench_grain_date    ON report_snapshots (report_date) WHERE scope = 'daily_grain';
CREATE INDEX idx_bench_internal_date ON report_snapshots (report_date) WHERE scope = 'internal_grain'`

// benchSeed 造两个口径的 grain 行：daily_grain 全量，internal_grain 按
// business 流量占 85% 抽样，让两个视角的行数比例与真库一致。
const benchSeed = `
INSERT INTO report_snapshots (
    scope, scope_key, report_date, raw_model_name,
    request_count, success_count, error_count,
    input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
    error_kind_breakdown, estimated_cost_cents, currency, price_snapshot,
    provider_id, tenant_id, credential_id, api_key_id, person,
    credits_charged, latency_p50_ms, latency_p95_ms
)
SELECT
    sc.scope,
    sc.scope || '|' || d::date || '|' || i || '|' || mm.m,
    d::date,
    mm.m,
    1 + (i % 3),
    1 + (i % 3) - CASE WHEN i % 11 = 0 THEN 1 ELSE 0 END,
    CASE WHEN i % 11 = 0 THEN 1 ELSE 0 END,
    100 * (i % 50), 20 * (i % 30), 50 * (i % 10), 0,
    (CASE WHEN i % 11 = 0 THEN '{"rate_limit_exceeded":1,"timeout":1}'::jsonb ELSE '{}'::jsonb END),
    (i % 7), 'USD',
    (CASE WHEN sc.scope = 'internal_grain' THEN '{"cents_per_credit":0.2,"currency":"CNY"}'::jsonb ELSE '{}'::jsonb END),
    (CASE WHEN i % 9 = 0 THEN NULL ELSE (i % 3) + 1 END),
    'tenant' || (i % 3),
    (CASE WHEN i % 9 = 0 THEN NULL ELSE (i % 6) + 11 END),
    (i % 12) + 100,
    'user' || (i % 4),
    CASE WHEN sc.scope = 'internal_grain' THEN 1 ELSE 0 END,
    300, 3000
FROM generate_series(
        (now()::date - $1::int), (now()::date - 1), '1 day') AS d
CROSS JOIN LATERAL generate_series(1, $2::int) AS i
CROSS JOIN LATERAL (VALUES ('daily_grain'), ('internal_grain')) AS sc(scope)
CROSS JOIN LATERAL (
    SELECT (ARRAY['glm-5.3','glm-5.3-flash','deepseek-v3','qwen-max','gpt-4o-mini'])[1 + (i % 5)] AS m
) mm
WHERE sc.scope = 'daily_grain' OR i % 7 <> 0`
