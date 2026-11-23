package admin

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// 错误处理手册的管理面手动路径（手动执行 / 手动分析）必须以操作者身份落留痕。
//
//	★ 2026-11-23 审计轮：此前两处都直接调引擎的自动化入口 ⇒ 留痕 executed_by
//	  恒为 "error_playbook"，与 worker 的自动执行/自动分析无法区分。
//	  引擎侧行为由 errorplaybook 包的钉测守（TestManualExecutionRecordsOperatorIdentity /
//	  TestManualAnalysisRecordsOperatorIdentity）；本门守「处理器接线没有退回裸入口」——
//	  处理器没有可单测的装配面（需要 pool / secret / AdminMiddleware），故用源码结构断言。
func TestErrorPlaybookManualPathsCarryOperatorIdentity(t *testing.T) {
	raw, err := os.ReadFile("error_playbook_handler.go")
	if err != nil {
		t.Fatalf("read handler source: %v", err)
	}
	code := stripGoCommentsKeepingStrings(string(raw))

	// countWired 数「同一行里既有带身份入口、又以 piAppliedBy(r) 收尾」的调用行。
	countWired := func(src, entry string) int {
		n := 0
		for _, l := range strings.Split(src, "\n") {
			if strings.Contains(l, entry) && strings.Contains(l, "piAppliedBy(r))") {
				n++
			}
		}
		return n
	}
	if n := countWired(code, ".ExecuteEntryAs("); n != 1 {
		t.Fatalf("manual execute must go through ExecuteEntryAs(…, piAppliedBy(r)) exactly once, got %d", n)
	}
	if n := countWired(code, ".AnalyzeUnknownAs("); n != 1 {
		t.Fatalf("manual analyze must go through AnalyzeUnknownAs(…, piAppliedBy(r)) exactly once, got %d", n)
	}

	// 裸的自动化入口在处理器里一行都不许有。
	bare := regexp.MustCompile(`\.(ExecuteEntry|AnalyzeUnknown)\(`)
	if loc := bare.FindStringIndex(code); loc != nil {
		lo, hi := loc[0]-40, loc[1]+20
		if lo < 0 {
			lo = 0
		}
		if hi > len(code) {
			hi = len(code)
		}
		t.Fatalf("handler calls the automation entry point directly (operator identity lost): %q",
			strings.TrimSpace(code[lo:hi]))
	}

	// 量具自检：两处各退回一种错法，本门都必须抓到（否则上面的绿是空的）。
	// ⚠ 变异点必须用**唯一锚点**定位：`piAppliedBy(r))` 在本文件出现 4 次
	//   （另两处是日志行），盲替换第一个命中的是日志而不是调用。
	m1 := strings.Replace(code, ".AnalyzeUnknownAs(", ".AnalyzeUnknown(", 1)
	if m1 == code || !bare.MatchString(m1) {
		t.Fatal("self-check: a manual analyze call reverted to AnalyzeUnknown must be detected")
	}
	const execAnchor = "TriggerManual, piAppliedBy(r))"
	if strings.Count(code, execAnchor) != 1 {
		t.Fatalf("self-check premise broken: anchor %q must occur exactly once, got %d",
			execAnchor, strings.Count(code, execAnchor))
	}
	m2 := strings.Replace(code, execAnchor, `TriggerManual, "")`, 1)
	if countWired(m2, ".ExecuteEntryAs(") != 0 {
		t.Fatal("self-check: a manual execute call that drops piAppliedBy(r) must be detected")
	}
}
