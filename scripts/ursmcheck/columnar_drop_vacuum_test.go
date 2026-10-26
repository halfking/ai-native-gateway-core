package ursm_test

// pg17-drop-old-columnar-partitions.sh 的 VACUUM 分支门（2026-10-18，§88）
//
// 这条分支**从未成功执行过一次**。生产日志里 6 条
// `ERROR: VACUUM cannot run inside a transaction block`、0 条成功，
// 而脚本自己在注释里写着「实测 14 GB bloat，必须 VACUUM FULL 才能释放」。
// 根因与 ursm-snapshot-health.sh 的 de() 注释记的是同一族问题：
// 把多条语句塞进 psql 的单个 -c 字符串 ⇒ PostgreSQL 视其为隐式事务块，
// 而 VACUUM 禁止在事务块内运行。
//
// 本门是**源码文本断言**（脚本要 docker 才能跑，单测里无法执行）。
// 它的边界：钉住「语句串里不再混 SET」，**钉不住** VACUUM 在真实环境里成功。
// 那只能靠部署后看日志里出现 `VACUUM FULL <meta> OK`。

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const dropScript = "../252-monitor/pg17-drop-old-columnar-partitions.sh"

func readDropScript(t *testing.T, stripComments bool) string {
	t.Helper()
	b, err := os.ReadFile(dropScript)
	if err != nil {
		t.Fatalf("read %s: %v", dropScript, err)
	}
	s := string(b)
	if stripComments {
		s = stripFullLineComments(s)
	}
	return s
}

// TestVacuumNotWrappedInImplicitTransaction —— 核心门。
//
// VACUUM 不能在事务块里跑，而 psql 的单个 -c 多语句串会构成隐式事务块。
// ⇒ 传给它的那条语句**只能是 VACUUM 本身**，超时/锁设置必须走 PGOPTIONS。
func TestVacuumNotWrappedInImplicitTransaction(t *testing.T) {
	src := readDropScript(t, true)

	// 逐行找真正传给 psql 的 VACUUM 语句串。
	var vacuumArgs []string
	for _, line := range strings.Split(src, "\n") {
		if strings.Contains(line, "VACUUM FULL columnar_internal") && strings.Contains(line, `"`) {
			vacuumArgs = append(vacuumArgs, strings.TrimSpace(line))
		}
	}
	if len(vacuumArgs) == 0 {
		t.Fatalf("%s 找不到传给 psql 的 VACUUM FULL 语句 —— 本门可能已因重构而失效，"+
			"请重新确认 VACUUM 是否还在语句串里（见 §88）", dropScript)
	}
	for _, arg := range vacuumArgs {
		if reSETinStmt.MatchString(arg) {
			t.Fatalf("VACUUM 语句串里混进了 SET：%s\n"+
				"PostgreSQL 会把 psql 单个 -c 的多条语句包成**隐式事务块**，"+
				"而 VACUUM 禁止在事务块内运行 ⇒ 每次都报 "+
				"\"VACUUM cannot run inside a transaction block\"。\n"+
				"生产日志里这条分支 6 次失败 / 0 次成功。修法：超时设置走 PGOPTIONS（-e），"+
				"语句串里只留 VACUUM 本身。", arg)
		}
	}

	// 反向确认：超时设置确实改由 PGOPTIONS 承载。
	if !strings.Contains(src, "PGOPTIONS=-c statement_timeout=60min") {
		t.Fatalf("%s 里找不到 PGOPTIONS 承载的 statement_timeout —— "+
			"语句串里没有 SET 就必须在这里有它，否则 60 分钟超时会退回库默认值 30s，"+
			"大表 VACUUM FULL 会中途被取消。", dropScript)
	}
}

// reSETinStmt 匹配「语句串里的 SET 语句」。
//
// ★ 首版写成 `;\s*SET\s+\w+\s*=`（只认 SET 在分号**之后**）⇒ **变异验证首轮假绿**：
//   把它改成 `SET x=…; VACUUM`（SET 在**开头**）时门照样通过。
//   两种顺序都会把语句串变成多语句、都会构成隐式事务块 ⇒ 两种都必须匹配。
//   锚点放宽为「行首或分号之后」。
var reSETinStmt = regexp.MustCompile(`(?:^|;)\s*SET\s+\w+\s*=`)

// TestVacuumCompletionLogIsNotUnconditional —— 防「做没做都报完成」。
//
// 旧代码用 `|| echo WARN` 吞掉错误，循环末尾无条件打 `VACUUM FULL done`，
// 于是运维在日志里看到 done、以为 14 GB bloat 被清了，实际一张表都没动。
func TestVacuumCompletionLogIsNotUnconditional(t *testing.T) {
	src := readDropScript(t, true)
	if !strings.Contains(src, "vac_fail") {
		t.Fatalf("%s 没有失败计数 —— 无法区分「全成功」与「全失败」，"+
			"就会重演「日志报 done、实际没做」。", dropScript)
	}
	if !strings.Contains(src, "PARTIAL") {
		t.Fatalf("%s 缺少部分失败时的独立日志 —— 旧写法只有 done，"+
			"失败被 `||` 吞掉后仍然打 done。", dropScript)
	}
}
