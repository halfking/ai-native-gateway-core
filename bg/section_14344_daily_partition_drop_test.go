package bg

// section_14344_daily_partition_drop_test.go —— §143.44 门：
// usage_facts 的留存登记（TTL=365 + 按日分区单元）与**按日 DROP** 的行为。
//
// ★ 背景：usage_facts 是两实例上**唯一没有任何留存**的族
//   （settings_kv 里 11 个 `lifecycle.*_ttl_days` 键两实例一致、其中没有它）。
//   而既有 SQL helper `drop_old_state_partition_table` 只枚举**月度**分区
//   （`^<parent>_\d{4}_\d{2}$`）⇒ 对 `usage_facts_20261021` **一个都不匹配**
//   ⇒ 只加登记行 = **什么都没做**（分区一个也掉不了）。
//
// ⇒ 本门钉三件事：
//  ① **登记**：usage_facts 在 `stateTableTTLSpecs()` 里，且 **partitionUnit="day"**、
//     fallback=365；settings 目录里同一个键的默认值也是 365（两边不能漂）。
//  ② **行为**：过期分区被 DROP、未过期分区**不碰**（不碰今天/未来 ⇒ 写入永不 23514）。
//  ③ **诚实计数**：DROP **不带** `IF EXISTS`（对端已删 ⇒ 42P01 ⇒ 跳过 ⇒ 计数不虚增）。

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// TestSection14344UsageFactsRetentionIsRegistered 钉住「登记」这一侧。
func TestSection14344UsageFactsRetentionIsRegistered(t *testing.T) {
	var spec *stateTableTTLSpec
	for i := range stateTableTTLSpecs() {
		s := stateTableTTLSpecs()[i]
		if s.parent == "usage_facts" {
			spec = &s
			break
		}
	}
	if spec == nil {
		t.Fatal("stateTableTTLSpecs() 里没有 usage_facts ⇒ 该族仍然没有任何留存 ⇒ 无界增长")
	}
	if spec.partitionUnit != "day" {
		t.Errorf("usage_facts 的 partitionUnit = %q，期望 \"day\" —— 既有 SQL helper 只枚举"+
			"月度分区名（^<parent>_\\d{4}_\\d{2}$），用它对日分区名一个都不匹配 ⇒ 等于什么都没做",
			spec.partitionUnit)
	}
	if spec.fallback != 365 {
		t.Errorf("usage_facts 的 fallback TTL = %d 天，期望 365（属主 2026-10-21 定：计费事实族，"+
			"对齐年度审计/账期窗口）", spec.fallback)
	}
	if spec.setting != "lifecycle.usage_facts_ttl_days" {
		t.Errorf("usage_facts 的 settings 键 = %q，期望 lifecycle.usage_facts_ttl_days", spec.setting)
	}

	// settings 目录必须登记同一个键，且默认值一致（两边漂移 = 回收与展示对不上）。
	var found bool
	for _, s := range settings.LifecycleSpecs() {
		if s.Key != "lifecycle.usage_facts_ttl_days" {
			continue
		}
		found = true
		if s.Default != 365 {
			t.Errorf("settings 目录里 usage_facts_ttl_days 的默认值 = %v，期望 365", s.Default)
		}
	}
	if !found {
		t.Error("settings 目录里没有 lifecycle.usage_facts_ttl_days ⇒ 管理员看不到、也设不了这个保留期")
	}
}

// TestSection14344DailyDropDropsOnlyExpiredPartitions 钉住「行为」。
func TestSection14344DailyDropDropsOnlyExpiredPartitions(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	expired := time.Now().AddDate(0, 0, -400).Format("20060102") // 早于 365 天
	fresh := time.Now().AddDate(0, 0, -10).Format("20060102")    // 仍在窗口内

	mock.ExpectQuery("SELECT c.relname").WithArgs("usage_facts").WillReturnRows(
		pgxmock.NewRows([]string{"relname"}).AddRow("usage_facts_" + expired).AddRow("usage_facts_" + fresh))

	// 只有过期分区进入 DROP 流程：Begin → SET lock_timeout=0 → advisory → 恢复 lock_timeout → DROP → Commit。
	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL lock_timeout = '0'").WillReturnResult(pgxmock.NewResult("SET", 0))
	mock.ExpectQuery("pg_try_advisory_xact_lock").WithArgs(dailyStateDropLockKey).WillReturnRows(
		pgxmock.NewRows([]string{"pg_try_advisory_xact_lock"}).AddRow(true))
	mock.ExpectExec("SET LOCAL lock_timeout = '5min'").WillReturnResult(pgxmock.NewResult("SET", 0))
	mock.ExpectExec("DROP TABLE").WillReturnResult(pgxmock.NewResult("DROP TABLE", 0))
	mock.ExpectCommit()

	pm := &PartitionManager{db: mock}
	dropped, err := pm.dropOldDailyStatePartitions(context.Background(), "usage_facts", 365)
	if err != nil {
		t.Fatalf("dropOldDailyStatePartitions: %v", err)
	}
	if dropped != 1 {
		t.Fatalf("dropped = %d，期望 1（只有过期分区该被 DROP）", dropped)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("未满足的期望：%v", err)
	}
}

// TestSection14344DailyDropNeverUsesIfExists 钉住「诚实计数」。
//
// ★ 不用 `DROP TABLE IF EXISTS`：对端实例删过时它同样「成功」⇒ 计数被虚增
//
//	（这正是 retention_partition.go 里已登记的教训：两台各报一次 bytes_reclaimed）。
//	⇒ 改用 42P01（对象不存在）归类为「跳过」。
//
// ⚠⚠ 判据形态：**必须走 go/ast 只看字符串字面量**，不能对整文件做文本匹配 ——
// 本函数**自己的文档注释**里就写着「不用 IF EXISTS」⇒ 文本匹配会被注释喂饱
// ⇒ ⇒ 门恒绿、毫无意义（这是本轮第三次现场复现「判据被注释喂饱」）。
func TestSection14344DailyDropNeverUsesIfExists(t *testing.T) {
	literals := funcStringsInFunc(t, "partition_manager.go", "dropDailyPartition")

	var sawDrop bool
	for _, lit := range literals {
		if !strings.Contains(strings.ToUpper(lit), "DROP TABLE") {
			continue
		}
		sawDrop = true
		if strings.Contains(strings.ToUpper(lit), "IF EXISTS") {
			t.Errorf("dropDailyPartition 的 SQL 字面量里含 `IF EXISTS`：%q ⇒ "+
				"对端已删时它同样返回成功 ⇒ 计数会被虚增", lit)
		}
	}
	if !sawDrop {
		t.Error("dropDailyPartition 里找不到 DROP TABLE 的 SQL 字面量 —— 断言面失效（量具坏了）")
	}
}

// funcStringsInFunc 用 go/ast 取出指定函数体内的**全部字符串字面量**。
//
// ★ 为什么必须用 AST：Go 注释不在 AST 里 ⇒ 断言不会被注释喂饱。
func funcStringsInFunc(t *testing.T, path, fnName string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("解析 %s: %v", path, err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != fnName || fn.Body == nil {
			continue
		}
		var out []string
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if s, err := strconv.Unquote(lit.Value); err == nil {
				out = append(out, s)
			}
			return true
		})
		return out
	}
	t.Fatalf("%s 里找不到函数 %s", path, fnName)
	return nil
}

// TestSection14344DailyDropSkipsAbsentPartition 钉住「对端已删 ⇒ 跳过而非计数」。
func TestSection14344DailyDropSkipsAbsentPartition(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	expired := time.Now().AddDate(0, 0, -400).Format("20060102")
	mock.ExpectQuery("SELECT c.relname").WithArgs("usage_facts").WillReturnRows(
		pgxmock.NewRows([]string{"relname"}).AddRow("usage_facts_" + expired))

	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL lock_timeout = '0'").WillReturnResult(pgxmock.NewResult("SET", 0))
	mock.ExpectQuery("pg_try_advisory_xact_lock").WithArgs(dailyStateDropLockKey).WillReturnRows(
		pgxmock.NewRows([]string{"pg_try_advisory_xact_lock"}).AddRow(true))
	mock.ExpectExec("SET LOCAL lock_timeout = '5min'").WillReturnResult(pgxmock.NewResult("SET", 0))
	// 对端已删 ⇒ 42P01（对象不存在）
	mock.ExpectExec("DROP TABLE").WillReturnError(&pgconn.PgError{Code: "42P01"})
	mock.ExpectRollback()

	pm := &PartitionManager{db: mock}
	dropped, err := pm.dropOldDailyStatePartitions(context.Background(), "usage_facts", 365)
	if err != nil {
		t.Fatalf("对端已删应当被归类为「跳过」而不是错误：%v", err)
	}
	if dropped != 0 {
		t.Fatalf("dropped = %d，期望 0（没删成就不计数）", dropped)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("未满足的期望：%v", err)
	}
}
