package ursm_test

// 巡检基线窗口 vs 快照留存窗口的不变式门（2026-10-18，§86）
//
// 这道门存在的原因是一个**跨语言、跨主机、结构上不可能有编译期检查**的不变式：
//
//	巡检基线窗口（scripts/252-monitor/ursm-snapshot-health.sh 的 BASELINE_DAYS）
//	    必须 ≤ 数据实际留存天数（domains/ursm/v2/persist/retention.go 的 Retention）
//
// 前者在 252 上跑（本仓代码到不了那台机），后者随网关部署（不在 252 上）。
// 两边全仓 grep 的交集**只有注释和文档**，没有任何测试能发现它们倒挂。
//
// ⇒ 这道门只能守住**默认值**。它防不住生产 env 被改（那只靠运行时检查），
// 见 §86 第五节的动作 (1)。**不要把本门当成覆盖面更大的那个。**

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

const (
	healthScript = "../252-monitor/ursm-snapshot-health.sh"
	retentionGo  = "../../domains/ursm/v2/persist/retention.go"
)

// healthBaselineDays 从巡检脚本里解析 `: "${BASELINE_DAYS:=N}"` 的默认值。
//
// ★ 必须解析真实源而不是硬编码 7：硬编码的话，改脚本的人只要忘了改测试就恒绿，
//   而这道门的作用恰恰是「挡住单边改动」。
var reBaselineDefault = regexp.MustCompile(`BASELINE_DAYS:=(\d+)`)

// retentionDefaultDays 从 retention.go 的 DefaultSnapshotRetentionConfig() 里
// 解析 `Retention: N * 24 * time.Hour`。
var reRetentionDefault = regexp.MustCompile(`Retention:\s*(\d+)\s*\*\s*24\s*\*\s*time\.Hour`)

func mustParseDays(t *testing.T, path string, re *regexp.Regexp, what string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	m := re.FindSubmatch(b)
	if m == nil {
		// ★ 解析不到必须判红，不能当作「跳过」。
		//   这是判据自己变成恒绿的最短路径：重命名常量 / 改写法都会静默放行。
		t.Fatalf("%s: no match in %s — the gate's probe must track the real source; "+
			"skipping instead would make this test silently vacuous", what, path)
	}
	n, err := strconv.Atoi(string(m[1]))
	if err != nil {
		t.Fatalf("%s: parse days %q: %v", what, m[1], err)
	}
	if n <= 0 {
		t.Fatalf("%s: parsed non-positive window %d — refusing to gate on it", what, n)
	}
	return n
}

func healthBaselineDays(t *testing.T) int {
	return mustParseDays(t, healthScript, reBaselineDefault, "BASELINE_DAYS")
}

func retentionDefaultDays(t *testing.T) int {
	return mustParseDays(t, retentionGo, reRetentionDefault, "default Retention")
}

// TestBaselineWindowNotWiderThanRetention —— 核心门。
//
// 不变式取「≤」而非「<」：两者相等是**合法且当前就是**的状态（都 7 天），
// 但它零余量，所以下面那条测试专门盯住「相等」这个形态。
func TestBaselineWindowNotWiderThanRetention(t *testing.T) {
	base, ret := healthBaselineDays(t), retentionDefaultDays(t)
	if base > ret {
		t.Fatalf("BASELINE_DAYS=%d > retention default=%d\n"+
			"巡检基线窗口比数据留存更宽 ⇒ 基线永远读不满 %d 天，\n"+
			"而判定阈值是按 %d 天样本标定的 ⇒ 判据会静默变松且不报错。\n"+
			"（§86 第三节：脚本只检查 ref_p50<=0，即基线「空」；\n"+
			"  基线「薄」这一端至今没有任何检查。）",
			base, ret, base, ret)
	}
}

// TestBaselineWindowHasNoMarginOverRetention —— 记录「零余量」这个形态。
//
// 这**不是**说相等是错的（它是当前状态，且能跑）。
// 它钉住的是：一旦有人只改一边，让两者出现缺口或倒挂，
// 上面那条测试会变红，而这条会告诉你**是哪个方向**变了。
//
// 保留它是因为 §86 的判断：相等意味着「基线样本不足」这个失效模式
// 在结构上随时可能被一次单边 env 改动触发，而这个门是仓库侧唯一的探测器。
func TestBaselineWindowHasNoMarginOverRetention(t *testing.T) {
	base, ret := healthBaselineDays(t), retentionDefaultDays(t)
	t.Logf("BASELINE_DAYS=%d, retention default=%d, margin=%d day(s)", base, ret, ret-base)

	if base > ret {
		t.Fatalf("倒挂（见 TestBaselineWindowNotWiderThanRetention）")
	}
	// 余量 = 0 是已知且被接受的状态；这里只做记录，不判红。
	// 若将来有人给它留出余量（retention 调大或 BASELINE_DAYS 调小），
	// 本测试仍然通过 —— 它不是余量门，是形态记录器。
}

// TestHealthScriptKnowsItsOwnFailureMode —— 钉住脚本自己写下的那句提示。
//
// §86 第三节：脚本第 391 行写着「常见原因：BASELINE_DAYS 超过快照保留天数」。
// 那行注释是**这个失效模式在本仓唯一的书面记录** ——
// 如果它被删掉，就再也没有任何地方说明「基线窗口受留存窗口约束」这件事，
// 而本文件就是下一个能发现这点的地方。
func TestHealthScriptKnowsItsOwnFailureMode(t *testing.T) {
	b, err := os.ReadFile(healthScript)
	if err != nil {
		t.Fatalf("read %s: %v", healthScript, err)
	}
	if !regexp.MustCompile(`BASELINE_DAYS 超过快照保留天数`).Match(b) {
		t.Fatalf("%s no longer documents 「BASELINE_DAYS 超过快照保留天数」\n"+
			"该提示是本失效模式在脚本侧唯一的书面记录；\n"+
			"删掉它之后，「基线窗口受留存窗口约束」就只剩本测试文件知道。", healthScript)
	}
}
