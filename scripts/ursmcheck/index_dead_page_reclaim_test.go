package ursm_test

// §143.16 的文档门：索引处置清单的**分档结论**与**两处就地订正**必须在场。
//
// 背景（runbook §143.16）：
// §143.9.6 给了 18 条 / 387MB 的 E 类清单，§143.12.6 给了「71MB 逐字重复对」，
// 两者都是**按体积排序**的候选表。§143.16 把它们升级成
// 「逐条实测读数 + 读方证据 + 分档（T0 REINDEX / T1 DROP 独立唯一索引 /
// T2 DROP 约束 / T3 DROP 无读方索引）」，并订正了 §143.9.6 与 §143.12.6 两处。
//
// # 为什么「文档也要有门」
//
// ① **订正横幅会被人删**。本仓的纪律是「错误结论就地加横幅 + 保留原文」；
//    原文还在（那是对的）⇒ 唯一的提示就是横幅 ⇒ 横幅没了，下一个读 runbook 的人
//    会把**已被证伪**的结论当结论继续用。§143.12.6 的「纯重复 71.1MB /
//    pkey 是约束」两处都需要横幅。
//
// ② **分档结论会被"简化"成一句"建议 DROP 最大的几条"** —— 那正是 §143.16 要纠正的
//    框架（按体积排 DROP 看不见死页：`request_stats_error_drill_minute_pkey`
//    61.2MiB 里 94.6% 是死页，REINDEX 一条就能回收 57.9MiB，而它不在「最大」那一档里）。
//
// ③ **「死页」这个机制必须连同结论一起在**。只有结论没有机制，下一个人会
//    「顺手加个 VACUUM」—— 而实测（本地 PG 17.11 七阶段对照 + 生产 17 次 autovacuum
//    互证）表明 **VACUUM 回收不了死页**，只有 REINDEX 能。
//
// # 这道门测什么
//
// 门断言的是**文档结构**（章节存在、关键量在、两处横幅在、机制表述在），
// 不连库、不跑 SQL ⇒ CI 与开发机都能跑。
// 判据的**语义**正确性由 §143.16 记录的真库实测背书（读数与探针脚本见 §143.16.8）。

import (
	"os"
	"strings"
	"testing"
)

const runbook14316 = "../../docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"

func readRunbook14316(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(runbook14316)
	if err != nil {
		t.Fatalf("读 runbook 失败：%v", err)
	}
	return strings.Split(string(raw), "\n")
}

// TestRunbook14316HasTieredReclaimPlan：§143.16 必须在场，且四档与关键量都在。
//
// 关键量是**实测读数**，不是修辞：
//   - 7413（request_stats_error_drill_minute_pkey 的死页块数）
//   - 94.6%（死页占该索引的比例）
//   - 145（MiB，session_summaries 三条唯一索引的逻辑冗余总量）
//   - 67.2（MiB，裸 uidx）
//   - 77.6（MiB，per_tenant 约束）
func TestRunbook14316HasTieredReclaimPlan(t *testing.T) {
	lines := readRunbook14316(t)
	all := strings.Join(lines, "\n")

	if !strings.Contains(all, "§143.16") {
		t.Fatal("runbook 里没有 §143.16：索引处置清单的分档结论丢了")
	}
	for _, tier := range []string{"**T0**", "**T1**", "**T2**", "**T3**"} {
		if !strings.Contains(all, tier) {
			t.Errorf("runbook §143.16 缺少 %s 档：四档处置建议是本节的核心结论", tier)
		}
	}
	for _, fact := range []string{"7413", "94.6%", "145 MiB", "67.2 MiB", "77.6 MiB"} {
		if !strings.Contains(all, fact) {
			t.Errorf("runbook §143.16 缺少实测读数 %q：清单的量必须是实测值，不能只剩结论", fact)
		}
	}
}

// TestRunbook14316CarriesBothCorrections：§143.9.6 与 §143.12.6 两处必须带
// 「§143.16 订正」横幅。
//
// 两处的订正内容不同，缺一不可：
//   - §143.9.6 E 类表：那一列是**表级**索引扫描数（分辨不出哪条索引热），
//     18 条里至少 6 条的每索引计数其实是 0；且这张表整体漏掉了「死页」这个量。
//   - §143.12.6 的「71MB 逐字重复对」：真实冗余是**三条 145MiB**；
//     且「pkey 是约束、删它要先改约束」这半句**弄反了**（uidx 是独立唯一索引）。
func TestRunbook14316CarriesBothCorrections(t *testing.T) {
	lines := readRunbook14316(t)

	anchors := []struct {
		what string
		mark string
	}{
		{"§143.9.6 E 类明细表", "### 143.9.6 E 类 387MB 的明细"},
		{"§143.12.6 的 71MB 逐字重复对", "纯重复 71.1MB"},
	}
	for _, a := range anchors {
		idx := -1
		for i, ln := range lines {
			if strings.Contains(ln, a.mark) {
				idx = i
				break
			}
		}
		if idx < 0 {
			t.Errorf("runbook 里找不到 %s（锚点 %q）：§143.16 的订正横幅无处安放，请同步更新这条门",
				a.what, a.mark)
			continue
		}
		if !hasBanner14316(lines, idx) {
			t.Errorf("runbook 的 %s 旁边没有 §143.16 的订正横幅：原文按纪律保留，"+
				"横幅是唯一的「此结论已被修订」提示 ⇒ 横幅没了会被当成结论继续读", a.what)
		}
	}
}

// TestRunbook14316StatesDeadPagesNeedReindex：死页机制必须**连同结论**一起在。
//
// 这条专门防一个具体的退化：只留「死页能回收 X MiB」的结论，
// 下一个人以为「那跑个 VACUUM 就行了」—— 而实测是
// VACUUM **回收不了**死页（本地 PG 17.11 连续两次 VACUUM 后 deleted_pages 与
// index_size 一字未变；生产侧该表已 autovacuum 17 次，死页至今仍在），
// **只有 REINDEX 能**。
func TestRunbook14316StatesDeadPagesNeedReindex(t *testing.T) {
	all := strings.Join(readRunbook14316(t), "\n")
	if !strings.Contains(all, "VACUUM") {
		t.Error("runbook §143.16 里没有 VACUUM：死页机制的关键一半（VACUUM 回收不了）丢了")
	}
	if !strings.Contains(all, "REINDEX") {
		t.Error("runbook §143.16 里没有 REINDEX：死页的正解（只有 REINDEX 能回收）丢了")
	}
	// 机制表述必须同时出现「VACUUM 回收不了」与「REINDEX 才能」两层，
	// 只有其中一层时读者可能反着理解。
	for _, phrase := range []string{"VACUUM 回收不了", "只有 REINDEX"} {
		if !strings.Contains(all, phrase) {
			t.Errorf("runbook §143.16 缺少机制表述 %q：结论必须带机制，"+
				"否则下一个人会以为「跑个 VACUUM 就行」（实测：回收不了）", phrase)
		}
	}
}

// hasBanner14316：横幅必须落在**同一小节内**（从锚点行往下，到下一个标题行为止）。
//
// 为什么不用「往下 N 行」这种固定窗口：§143.9.6 的横幅必须放在那张 18 行的表**之后**
// （表头是「表级扫描数」这个待订正的量本身）⇒ 距锚点 20+ 行。
// 固定窗口会让「横幅挪个位置」变成门红 ⇒ 逼着下一个人把横幅塞到奇怪的地方。
// ⇒ 规则取「同一小节内」：它约束的是**归属**（这条订正属于这一节），
//
//	而不约束排版位置。
func hasBanner14316(lines []string, idx int) bool {
	for i := idx; i < len(lines); i++ {
		ln := lines[i]
		if i > idx && strings.HasPrefix(strings.TrimSpace(ln), "#") {
			// 下一个标题 ⇒ 出了本小节 ⇒ 本小节内没有横幅。
			return false
		}
		if strings.Contains(ln, "订正") && strings.Contains(ln, "§143.16") {
			return true
		}
	}
	return false
}
