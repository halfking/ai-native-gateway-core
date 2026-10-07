package bg

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// partitionRetentionDSN841 returns the real-DB DSN, or skips.
//
// ★ These cases exist because the contract gate could NOT catch the bug they
// pin: `right(relname, 6)` silently produced `026_07`, so every partition —
// including the current month and the pre-created future month — compared as
// "expired". The SQL still contained `mth < cutoff`, so the contract gate
// passed. Only executing it proved the logic wrong.
// ⇒ 这是「机制断言 ≠ 效果断言」的又一个实例（runbook §10.107.5 同族）。
func partitionRetentionDSN841(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过 841 分区保留真库回归")
	}
	return dsn
}

// apply841InTx applies migration 841 plus a throwaway partitioned parent with
// three past months, the current month and a pre-created future month.
func apply841InTx(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)

	p, err := pgxpool.New(ctx, partitionRetentionDSN841(t))
	if err != nil {
		t.Fatalf("连库失败: %v", err)
	}
	t.Cleanup(p.Close)

	sql, err := os.ReadFile("../sql/migrations/startup/841_monthly_partition_retention.sql")
	if err != nil {
		t.Fatalf("读 841 失败: %v", err)
	}
	if _, err := p.Exec(ctx, string(sql)); err != nil {
		t.Skipf("该库不接受 841（可能无建表权限）: %v", err)
	}

	// ★ 每个用例必须自足：这些用例共享同一个库，而被测函数会真的 DROP 分区。
	//   不重置就会互相污染——上一个用例插的配置行漏给下一个，
	//   上一个用例删掉的分区让下一个少看见几个（实测就是这么假红的）。
	//   重置不是"洁癖"：漏读一行配置就会把「空配置不删任何东西」这条
	//   变成一句看起来在测、实际在测残留的判据。
	if _, err := p.Exec(ctx,
		`DROP TABLE IF EXISTS public.t841_parent CASCADE`); err != nil {
		t.Fatalf("清理夹具父表失败: %v", err)
	}
	if _, err := p.Exec(ctx,
		`DELETE FROM public.llm_gateway_partition_retention`); err != nil {
		t.Fatalf("清理配置表失败: %v", err)
	}
	if _, err := p.Exec(ctx,
		`DELETE FROM public.llm_gateway_partition_drop_log`); err != nil {
		t.Fatalf("清理审计表失败: %v", err)
	}

	_, err = p.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS public.t841_parent(ts timestamptz NOT NULL, v text)
		  PARTITION BY RANGE (ts)`)
	if err != nil {
		t.Fatalf("建夹具父表失败: %v", err)
	}
	for _, off := range []int{-3, -2, -1, 0, 1} {
		m := time.Now().AddDate(0, off, 0)
		first := time.Date(m.Year(), m.Month(), 1, 0, 0, 0, 0, time.UTC)
		next := first.AddDate(0, 1, 0)
		name := "t841_parent_" + first.Format("2006_01")
		// ★ DDL 不接受绑定参数（PostgreSQL: "mismatched param and argument
		//   count"）⇒ 这里必须把时间写成字面量。名字与时间都由 Go 生成，
		//   不含外部输入，拼接是安全的。
		if _, err := p.Exec(ctx, fmt.Sprintf(
			`CREATE TABLE IF NOT EXISTS public.%s PARTITION OF public.t841_parent
			   FOR VALUES FROM ('%s') TO ('%s')`,
			name,
			first.Format("2006-01-02 15:04:05-07"),
			next.Format("2006-01-02 15:04:05-07"))); err != nil {
			t.Fatalf("建夹具分区 %s 失败: %v", name, err)
		}
		if _, err := p.Exec(ctx,
			`INSERT INTO public.`+name+` VALUES ($1, 'x')`, first.Add(48*time.Hour)); err != nil {
			t.Fatalf("灌数据 %s 失败: %v", name, err)
		}
	}
	return p, ctx
}

func expiredPartitions841(t *testing.T, p *pgxpool.Pool, ctx context.Context) []string {
	t.Helper()
	rows, err := p.Query(ctx,
		`SELECT partition_name FROM public.llm_gateway_expired_month_partitions()`)
	if err != nil {
		t.Fatalf("调用 expired 函数失败: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("扫描失败: %v", err)
		}
		out = append(out, n)
	}
	return out
}

func alivePartitions841(t *testing.T, p *pgxpool.Pool, ctx context.Context) []string {
	t.Helper()
	rows, err := p.Query(ctx, `
		SELECT c.relname FROM pg_class c
		JOIN pg_inherits i ON i.inhrelid = c.oid
		WHERE i.inhparent = 'public.t841_parent'::regclass
		ORDER BY c.relname`)
	if err != nil {
		t.Fatalf("查存活分区失败: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("扫描失败: %v", err)
		}
		out = append(out, n)
	}
	return out
}

// TestPartition841_CurrentAndFuturePartitionsAreNeverDropped is the one that
// matters: dropping the current month's partition would take the live write
// target offline, and the future one is pre-created on purpose.
func TestPartition841_CurrentAndFuturePartitionsAreNeverDropped(t *testing.T) {
	p, ctx := apply841InTx(t)

	if _, err := p.Exec(ctx,
		`INSERT INTO public.llm_gateway_partition_retention (family, retain_months)
		 VALUES ('t841_parent', 1)`); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}

	var dropped []string
	if err := p.QueryRow(ctx,
		`SELECT llm_gateway_drop_expired_month_partitions()`).Scan(&dropped); err != nil {
		t.Fatalf("调用 drop 函数失败: %v", err)
	}

	alive := alivePartitions841(t, p, ctx)
	if len(alive) != 2 {
		t.Fatalf("保留 N=1（只留当月）时应存活「当月 + 预建的未来月」2 个分区，实得 %v", alive)
	}
	cur := time.Now().Format("2006_01")
	future := time.Now().AddDate(0, 1, 0).Format("2006_01")
	want := map[string]bool{"t841_parent_" + cur: true, "t841_parent_" + future: true}
	for _, a := range alive {
		if !want[a] {
			t.Errorf("存活列表里出现了不该留下的分区 %q —— 当月(%s)与未来(%s)必须保留", a, cur, future)
		}
	}
	for _, d := range dropped {
		if strings.Contains(d, cur) || strings.Contains(d, future) {
			t.Errorf("★ 被删掉了不该删的分区 %q", d)
		}
	}
}

// TestPartition841_EmptyConfigDropsNothing: shipping the migration must not
// delete production data as a side effect.
func TestPartition841_EmptyConfigDropsNothing(t *testing.T) {
	p, ctx := apply841InTx(t)

	if got := expiredPartitions841(t, p, ctx); len(got) != 0 {
		t.Errorf("配置表为空时不应有任何分区过期，实得 %v", got)
	}
	var dropped []string
	if err := p.QueryRow(ctx,
		`SELECT llm_gateway_drop_expired_month_partitions()`).Scan(&dropped); err != nil {
		t.Fatalf("调用 drop 函数失败: %v", err)
	}
	if len(dropped) != 0 {
		t.Errorf("配置表为空时不应删任何东西，实得 %v", dropped)
	}
	if alive := alivePartitions841(t, p, ctx); len(alive) != 5 {
		t.Errorf("五个夹具分区应全部存活，实得 %v", alive)
	}
}

// TestPartition841_RetainMonthsWidensTheWindow pins the N semantics:
// retain_months = N keeps N months INCLUDING the current one.
func TestPartition841_RetainMonthsWidensTheWindow(t *testing.T) {
	p, ctx := apply841InTx(t)

	for _, tc := range []struct {
		retain int
		want   int
	}{
		{retain: 1, want: 3}, // 07,08,09 expired; keeps 10 (current) + 11 (future)
		{retain: 2, want: 2}, // keeps 09,10,11
	} {
		if _, err := p.Exec(ctx, `DELETE FROM public.llm_gateway_partition_retention`); err != nil {
			t.Fatalf("清配置失败: %v", err)
		}
		if _, err := p.Exec(ctx,
			`INSERT INTO public.llm_gateway_partition_retention (family, retain_months)
			 VALUES ('t841_parent', $1)`, tc.retain); err != nil {
			t.Fatalf("写入配置失败: %v", err)
		}
		got := expiredPartitions841(t, p, ctx)
		if len(got) != tc.want {
			t.Errorf("retain_months=%d 应有 %d 个过期分区，实得 %d 个：%v",
				tc.retain, tc.want, len(got), got)
		}
	}
}

// TestPartition841_WrongFamilyNameMatchesNothing: a typo in the family name
// must be a no-op, never "drop something that looks close".
func TestPartition841_WrongFamilyNameMatchesNothing(t *testing.T) {
	p, ctx := apply841InTx(t)

	if _, err := p.Exec(ctx,
		`INSERT INTO public.llm_gateway_partition_retention (family, retain_months)
		 VALUES ('t841_parent_TYPO', 1)`); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	if got := expiredPartitions841(t, p, ctx); len(got) != 0 {
		t.Errorf("族名写错时不应匹配任何分区，实得 %v", got)
	}
}

// TestPartition841_DisabledRowIsIgnored
func TestPartition841_DisabledRowIsIgnored(t *testing.T) {
	p, ctx := apply841InTx(t)

	if _, err := p.Exec(ctx,
		`INSERT INTO public.llm_gateway_partition_retention (family, retain_months, enabled)
		 VALUES ('t841_parent', 1, false)`); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	if got := expiredPartitions841(t, p, ctx); len(got) != 0 {
		t.Errorf("enabled=false 的族不应有任何过期分区，实得 %v", got)
	}
}
