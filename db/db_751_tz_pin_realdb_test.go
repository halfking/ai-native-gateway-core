package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestEnsureUsageFactsDailyPartition_TZPin_RealDB 钉住 migration 751
// （R69 12h 审计轮，2026-09-26；1614 轮 O-3 裁决落地）：ensure_
// usage_facts_daily_partition 的函数级 GUC 钉扎（ALTER FUNCTION SET
// timezone）必须在 **UTC 会话**下仍产出 Asia/Shanghai 日历边界的分区。
//
// 这是 751 选 ALTER FUNCTION SET 而非 body 内 SET LOCAL 的实证依据：
// 750 的 start_ts/end_ts 在 DECLARE 初始化器里做 p_date::timestamptz，
// 函数级 SET 在函数入口生效（先于初始器），body 内 SET LOCAL 则不覆盖
// 初始器（694 先例）。无钉扎时本测试在 UTC 会话下会得到 [00:00Z,
// 24:00Z) 即 [+08 08:00) 起点的错位窗口。
//
// 无 TEST_DATABASE_URL / TEST_DB_URL 时跳过（与 750 真库回归同门控）。
// 纪律：修复子代理禁连库——本测试只随协调者的 scratch 容器单点执行。
func TestEnsureUsageFactsDailyPartition_TZPin_RealDB(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过真库回归")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("connect real db: %v", err)
	}
	defer pool.Close()

	// 安装/刷新 canonical 750 函数 + 751 钉扎（simple protocol 承载多语句）。
	for _, f := range []string{
		"750_usage_facts_daily_partition.sql",
		"751_usage_facts_partition_tz_pin.sql",
	} {
		sqlBytes, err := os.ReadFile(filepath.Join("..", "sql", "migrations", "startup", f))
		if err != nil {
			t.Fatalf("read canonical %s: %v", f, err)
		}
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatalf("acquire conn: %v", err)
		}
		if _, err := conn.Conn().PgConn().Exec(ctx, string(sqlBytes)).ReadAll(); err != nil {
			t.Fatalf("apply canonical %s: %v", f, err)
		}
		conn.Release()
	}

	// 全程钉在单个连接上：会话时区设 UTC（模拟 UTC 运维/DSN 场景），
	// ensure 与边界断言都在这条连接执行，杜绝 pool 换连引入的不确定。
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire pinned conn: %v", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SET TIME ZONE 'UTC'`); err != nil {
		t.Fatalf("set session tz UTC: %v", err)
	}

	// 探测日 = 上海日历今天 + 41（避开 750 族测试的 +40 探测日与 boot
	// 的当日/次日），日期文本嵌入 SQL（无参数 → 留在本连接）。
	var probeDate string
	if err := conn.QueryRow(ctx,
		`SELECT to_char((now() AT TIME ZONE 'Asia/Shanghai')::date + 41, 'YYYY-MM-DD')`).Scan(&probeDate); err != nil {
		t.Fatalf("derive probe date: %v", err)
	}
	if _, err := conn.Exec(ctx,
		fmt.Sprintf(`SELECT ensure_usage_facts_daily_partition('%s'::date)`, probeDate)); err != nil {
		t.Fatalf("ensure under UTC session must succeed: %v", err)
	}

	// 断言：分区边界必须是上海日历窗口 [probeDate 00:00+08, 次日
	// 00:00+08)。pg_get_expr 以**会话时区**（此处 UTC）渲染文本，所以
	// 不能按 '+08' 字面断言——解析两端时刻与上海零点对齐比较。无钉扎
	// 时 UTC 会话会得到 [probeDate 00:00Z, +24h) 即错位 8h 的窗口
	// （2026-09-26 scratch 首跑实证：钉扎后边界文本渲染为
	// '2026-11-05 16:00:00+00'，恰为上海 11-06 零点）。
	pname := "usage_facts_" + strings.ReplaceAll(probeDate, "-", "")
	var bound string
	if err := conn.QueryRow(ctx, `
		SELECT pg_get_expr(c.relpartbound, c.oid)
		FROM pg_class c
		WHERE c.relname = $1 AND c.relnamespace = 'public'::regnamespace
	`, pname).Scan(&bound); err != nil {
		t.Fatalf("read partition bound for %s: %v", pname, err)
	}
	m := regexp.MustCompile(`FOR VALUES FROM \('(.+?)'\) TO \('(.+?)'\)`).FindStringSubmatch(bound)
	if m == nil {
		t.Fatalf("unexpected partition bound expression: %s", bound)
	}
	parsePgTS := func(s string) time.Time {
		ts, err := time.Parse("2006-01-02 15:04:05.999999999-07", s)
		if err != nil {
			t.Fatalf("parse bound timestamp %q: %v", s, err)
		}
		return ts
	}
	startTS, endTS := parsePgTS(m[1]), parsePgTS(m[2])
	wantStart, err := time.Parse(time.RFC3339, probeDate+"T00:00:00+08:00")
	if err != nil {
		t.Fatalf("parse Shanghai-midnight expectation: %v", err)
	}
	if !startTS.Equal(wantStart) || !endTS.Equal(wantStart.Add(24*time.Hour)) {
		t.Fatalf("partition bound must be the Shanghai calendar window [%s, %s); got bound %q",
			wantStart.Format(time.RFC3339), wantStart.Add(24*time.Hour).Format(time.RFC3339), bound)
	}

	// 幂等二连：同一连接再调一次不报错、边界不变。
	if _, err := conn.Exec(ctx,
		fmt.Sprintf(`SELECT ensure_usage_facts_daily_partition('%s'::date)`, probeDate)); err != nil {
		t.Fatalf("ensure round 2 (idempotent): %v", err)
	}
	var bound2 string
	if err := conn.QueryRow(ctx, `
		SELECT pg_get_expr(c.relpartbound, c.oid)
		FROM pg_class c
		WHERE c.relname = $1 AND c.relnamespace = 'public'::regnamespace
	`, pname).Scan(&bound2); err != nil || bound2 != bound {
		t.Fatalf("bound changed after idempotent rerun (err=%v): %q -> %q", err, bound, bound2)
	}

	// 清理探测分区（空分区直接 DETACH+DROP）。
	if _, err := conn.Exec(ctx,
		fmt.Sprintf(`ALTER TABLE usage_facts DETACH PARTITION %s`, pname)); err != nil {
		t.Fatalf("cleanup detach: %v", err)
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS %s`, pname)); err != nil {
		t.Fatalf("cleanup drop: %v", err)
	}
}
