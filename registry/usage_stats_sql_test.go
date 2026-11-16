package registry

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// usage_stats_sql_test.go —— registry 包「用量统计」两条查询的**真库**门
// 与**静态**形态门。
//
// ## 为什么需要它（2026-11-17 实测）
//
// 真库 PREPARE 扫全仓 SQL 时抓到：
// `WHERE usage_date >= CURRENT_DATE - $1` ⇒ **PREPARE 直接报**
// `operator does not exist: date >= integer`。
//
// ★ 机理：`CURRENT_DATE - $1` 里 $1 是**未定类型**（unknown）⇒ PostgreSQL
//
//	在候选算子里挑了 `date - date`（结果是 **integer**，即天数差）
//	而不是 `date - integer`（结果是 **date**）⇒ 于是比较变成
//	`date >= integer` ⇒ 运算符不存在 ⇒ **整条查询在运行时炸**。
//	⇒ 显式写 `$1::int` 就把它钉成 integer ⇒ `date - integer` ⇒ date ✓。
//
// ★ 这条查询（GetTopTools）当时**没有生产调用方**（死代码）⇒ 潜伏着；
//
//	一旦有人接上就会立刻踩到。
//
// ★ 同包另一条 GetUsageStats 已经写对了（`$1::int * INTERVAL '1 day'`）
//
//	⇒ 说明这类歧义是**逐条**产生的 ⇒ 需要一条形态门把它们统一钉住。
//
// ## 两条腿的分工
//
//   - 静态腿（不需要库）：扫本包源码，凡出现 `CURRENT_DATE - $n`（没有显式
//     转型）即失败 ⇒ 挡住**新写**出来的同形态。
//   - 真库腿（需要库）：**直接调用真实代码路径** GetUsageStats / GetTopTools
//     ⇒ 不抄一份 SQL（抄了就会与代码漂移，变成同源自证）⇒ 挡住**已写**的。
const registryUsageStatsEnv = "TOOL_REGISTRY_TEST_DB"

// TestUsageStatsSQL_NoAmbiguousDateArithmetic pins the shape.
//
// `CURRENT_DATE - $n`（$n 未转型）在 PostgreSQL 里是**歧义**的：
// date - date ⇒ integer（天数差），date - integer ⇒ date。
// PG 对未定类型参数会挑前者 ⇒ 与 date 列比较时报
// "operator does not exist: date >= integer"。
func TestUsageStatsSQL_NoAmbiguousDateArithmetic(t *testing.T) {
	data, err := os.ReadFile("usage_stats.go")
	if err != nil {
		t.Fatalf("read usage_stats.go: %v", err)
	}
	// ⚠️ 用捕获组判断后缀，**不用前瞻**（Go 的 RE2 不支持 `(?!...)`）。
	//    后缀两种合法形态（都实测 PREPARE 通过）：
	//      `$n::int`            ⇒ date - integer ⇒ date
	//      `$n * INTERVAL '…'`  ⇒ unknown * interval ⇒ numeric ⇒ timestamp
	//    其余（尤其是**裸** `$n`）⇒ PG 挑 date - date ⇒ 结果是 integer
	//    ⇒ 比较变成 `date >= integer` ⇒ 运算符不存在。
	re := regexp.MustCompile(`CURRENT_DATE\s*-\s*\$\d+([^\s]*)`)
	for _, m := range re.FindAllStringSubmatch(string(data), -1) {
		suffix := m[1]
		if strings.HasPrefix(suffix, "::") || strings.HasPrefix(suffix, "*") {
			continue
		}
		t.Errorf("发现歧义的日期算术 %q ⇒ PostgreSQL 会把占位符解析成 date，"+
			"结果变成整数天数差，比较时报 \"operator does not exist: date >= integer\"；"+
			"请显式写成 $n::int", strings.TrimSpace(m[0]))
	}
}

// TestUsageStatsQueries_RunOnRealDB exercises the **real code paths**
// (not a copy of the SQL) against a real database.
//
// ⇒ 这是唯一能抓住「日期算术歧义」这类**运行时**炸点的腿：
//
//	解析器看不出 `CURRENT_DATE - $1` 有歧义，mock 也测不出来。
func TestUsageStatsQueries_RunOnRealDB(t *testing.T) {
	dsn := os.Getenv(registryUsageStatsEnv)
	if dsn == "" {
		t.Skip("TOOL_REGISTRY_TEST_DB 未设置 —— 本次**没有**跑真库查询验证" +
			"（跳过不构成证据）")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("连不上真库（%v）⇒ 本次没跑真库查询验证", err)
	}
	t.Cleanup(db.Close)

	tr := NewToolRegistry(db, nil)

	// GetUsageStats：按天聚合的明细
	if _, err := tr.GetUsageStats(ctx, "", "", 30); err != nil {
		t.Fatalf("GetUsageStats 在真库上失败：%v", err)
	}
	// GetTopTools：聚合榜（这条此前就是坏的）
	if _, err := tr.GetTopTools(ctx, "", 10, 30); err != nil {
		t.Fatalf("GetTopTools 在真库上失败：%v", err)
	}
	// 带租户过滤的分支（$2 追加路径）
	if _, err := tr.GetTopTools(ctx, "default", 10, 30); err != nil {
		t.Fatalf("GetTopTools(tenant) 在真库上失败：%v", err)
	}
}
