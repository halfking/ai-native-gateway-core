//go:build !integration

package ursm_test

// AUDIT-GUARD: scripts/ursmcheck 属 GUARD_PACKAGES_SLOW ⇒ 本门在 `make guards-ursmcheck` 上真跑。
//
// agents_md_round_label_gate_test.go —— AGENTS.md「逐轮 L 标记」的两条完整性门（2026-11-25 第 28 轮）。
//
// ★★ 为什么立这道门（★ 当天实测到的两类事故，★ 都没有门兜 ★★）：
//
//	① 同号撞车：两条车道共用同一份 AGENTS.md、各写各的「L 编号」。第 28 轮开场时
//	   顶部是 L107 ⇒ 若照惯例写 L108 ⇒ 会与并行车道 06:10 刚落的 L108 撞号
//	   （内容完全不同）⇒ 「见 L108」这种交叉引用从此不可判读。当天靠开写前重新
//	   fetch 才避开 ⇒ 靠人记不住 ⇒ 要有门。
//	② 整块丢失：AGENTS.md 顶部是人工合并的高频冲突区 ⇒ 段头/整块被覆盖或丢行。
//	   当天实测两起：L87 整块（10 行 + 「⑩ 订正」行）在 01:36 的合并提交 570087c16
//	   里被丢；L108 段头在 34b4a9a6f 里被顶部的 L109 覆盖掉。两者都是「内容在 git
//	   历史和报告侧都还在、唯独交接面没了」⇒ 没有任何门会红。
//
// 判据：
//
//	R5 主档内唯一：AGENTS.md 里每个 L 标记的**段头**至多 1 行。
//	R6 只许搬、不许丢：主档 ∪ 归档档里出现过的 L 标记集合，必须是
//	   本门登记的基线集合的超集（归档＝把段头搬进归档档 ⇒ 并集不变）。
//
// ★ 边界（诚实登记）：
//   - R6 只保证「段头还在」，不保证段头下面的正文行数不变（正文行的丢失要靠
//     agents_md_archive_duplicate_gate_test.go 那族与人工合并纪律）。
//   - 历史上已有一次同号：L79 被 16:4x 与 17:5x 两轮先后用过（一在主档、一在归档
//     档）⇒ R5 只管主档内唯一 ⇒ 该历史同号如实放行、并以日志披露。
//   - 归档档内部有历史原因的逐字重复（L21、L74 各两份）⇒ 不属本门判据 ⇒ 只披露。

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// agentsRoundHeaderRE 匹配「逐轮 L 标记」段头行：
//
//	> ★★★★ **★ L108（★ 2026-11-25 06:1x）：★★★**【…     （主档现行形态）
//	> ★★★★ **★ L87（★ 2026-11-24 20:0x）：★★★【…          （早期形态）
//	> ★ **L21（待 owner 拍板）**                            （更早的单行块）
//
// 「（已归档）…**L103** 轮」这类**指针行**不匹配（`**` 之后紧跟的是括注、不是 L）。
var agentsRoundHeaderRE = regexp.MustCompile(`(?m)^>\s*[★\s]*\*\*[★\s]*(L\d+)[（(★]`)

// agentsRoundLabelCounts 返回文本里每个 L 标记的段头行数（键＝标记的数字部分）。
func agentsRoundLabelCounts(text string) map[int]int {
	counts := map[int]int{}
	for _, m := range agentsRoundHeaderRE.FindAllStringSubmatch(text, -1) {
		n, err := strconv.Atoi(m[1][1:])
		if err != nil {
			continue // 正则已保证是数字 ⇒ 走不到这里
		}
		counts[n]++
	}
	return counts
}

// agentsRoundLabelDups 返回段头超过 1 行的标记（升序）。
func agentsRoundLabelDups(counts map[int]int) []int {
	var dups []int
	for label, c := range counts {
		if c > 1 {
			dups = append(dups, label)
		}
	}
	sort.Ints(dups)
	return dups
}

// agentsRoundLabelMissing 返回基线里有、而并集里没有的标记（升序）。
func agentsRoundLabelMissing(union map[int]bool, baseline []int) []int {
	var missing []int
	for _, label := range baseline {
		if !union[label] {
			missing = append(missing, label)
		}
	}
	return missing
}

// agentsRoundLabelBaseline 是 R6 的基线：2026-11-25 第 28 轮修复后，主档 ∪ 归档档里
// 存在的全部 L 标记（45 个）。
//
// ★ 只许追加、不许删：新一轮落盘后不必回来改它（R6 是超集判据）。
// ★ 若某轮真的要从两份文件里**永久删除**一个轮块 ⇒ 那是 owner 的显式决定 ⇒
//
//	届时再从本表摘掉那一个号，并在提交正文里写明理由。
var agentsRoundLabelBaseline = []int{
	21, 66, 67, 68, 69, 70, 71, 72, 73, 74,
	75, 76, 77, 78, 79, 80, 81, 82, 83, 84,
	85, 86, 87, 88, 89, 90, 91, 92, 93, 94,
	95, 96, 97, 98, 99, 100, 101, 102, 103, 104,
	105, 106, 107, 108, 109,
}

// TestAgentsMdRoundLabelUniqueInMain —— R5：主档内每个 L 标记的段头至多 1 行。
func TestAgentsMdRoundLabelUniqueInMain(t *testing.T) {
	counts := agentsRoundLabelCounts(agentsReadRepoFile(t, agentsMdFile))
	if len(counts) == 0 {
		t.Fatalf("★ %s 里一条「L 标记」段头都匹配不到 ⇒ ⇒ ⇒ 判据可能已失效（★ 别按本门结论改代码）",
			agentsMdFile)
	}
	for _, label := range agentsRoundLabelDups(counts) {
		t.Errorf("★ %s 里 L%d 的段头出现了 %d 次 ⇒ ⇒ ⇒ 两条车道各写各的编号、撞号了 ⇒ ⇒ "+
			"★ 「见 L%d」这类交叉引用从此不可判读 ⇒ ★ 修法：开写前先 fetch、取当时最大号 +1",
			agentsMdFile, label, counts[label], label)
	}
	t.Logf("★ 主档里有 %d 个 L 标记（范围 L%d–L%d）", len(counts), minIntInMap(counts), maxIntInMap(counts))
}

// TestAgentsMdRoundLabelNotLost —— R6：轮块只许搬、不许丢。
func TestAgentsMdRoundLabelNotLost(t *testing.T) {
	mainCounts := agentsRoundLabelCounts(agentsReadRepoFile(t, agentsMdFile))
	archiveCounts := agentsRoundLabelCounts(agentsReadRepoFile(t, agentsMdArchiveFile))
	union := map[int]bool{}
	for label := range mainCounts {
		union[label] = true
	}
	for label := range archiveCounts {
		union[label] = true
	}
	for _, label := range agentsRoundLabelMissing(union, agentsRoundLabelBaseline) {
		t.Errorf("★ L%d 的段头在 %s 与 %s 里**都不存在**了 ⇒ ⇒ ⇒ 轮块被丢（不是被归档）⇒ ⇒ "+
			"★ 最近一次对 AGENTS.md 顶部的人工合并/插入是否覆盖了段头？⇒ "+
			"★ 修法：git log -G'^>.*L%d[（(]' -- AGENTS.md 找到最后一个完整版本、逐字恢复",
			label, agentsMdFile, agentsMdArchiveFile, label)
	}
	// 如实披露、不判红（历史同号与归档档内的历史重复见文件头「边界」）。
	var both []int
	for label := range mainCounts {
		if archiveCounts[label] > 0 {
			both = append(both, label)
		}
	}
	sort.Ints(both)
	if len(both) > 0 {
		t.Logf("★ 同时出现在主档与归档档的标记（历史同号或归档未摘净，非本门判据）：%v", both)
	}
	if dups := agentsRoundLabelDups(archiveCounts); len(dups) > 0 {
		t.Logf("★ 归档档内段头重复的标记（历史原因，非本门判据）：%v", dups)
	}
}

// TestAgentsMdRoundLabelDetectorBites —— 量具自证：检测器必须抓到「同号」与「丢失」。
func TestAgentsMdRoundLabelDetectorBites(t *testing.T) {
	const header = "> ★★★★ **★ L%d（★ 2026-11-25 06:1x）：★★★**【★ 某轮】\n"

	// 臂 A：同一份主档里 L108 出现两次（两条车道各写一个 L108）⇒ 必须报出。
	mainA := fmt.Sprintf(header+"> ★★★★★ **① 甲车的内容】\n"+header+"> ★★★★★ **① 乙车的内容】\n", 108, 108)
	if dups := agentsRoundLabelDups(agentsRoundLabelCounts(mainA)); len(dups) != 1 || dups[0] != 108 {
		t.Fatalf("★ 量具自证失败：同号样本没抓到（得到 %v）⇒ R5 恒绿", dups)
	}

	// 臂 B：指针行不是段头 ⇒ 不得被当成段头计数（否则「已归档」指针会让 R5 假红）。
	pointer := "> ★★ **（已归档）**2026-11-25 **L103** 轮（★ 四车道取证轮 ★★）——★ 原文已**逐字**移入 `AGENTS-archive-20261031.md`（★ 段头 `# 追加：**2026-11-25 L103 轮（★ 四车道取证轮）**` ★★）\n"
	if got := agentsRoundLabelCounts(pointer); len(got) != 0 {
		t.Fatalf("★ 量具自证失败：把指针行当成了段头（得到 %v）⇒ R5 会假红", got)
	}

	// 臂 C：并集里缺了基线里的 108 与 87 ⇒ 必须逐个报出。
	unionC := map[int]bool{}
	for _, label := range agentsRoundLabelBaseline {
		if label != 108 && label != 87 {
			unionC[label] = true
		}
	}
	missing := agentsRoundLabelMissing(unionC, agentsRoundLabelBaseline)
	if len(missing) != 2 || missing[0] != 87 || missing[1] != 108 {
		t.Fatalf("★ 量具自证失败：丢失样本没抓全（得到 %v）⇒ R6 恒绿", missing)
	}

	// 臂 D：并集是基线的真超集（多出一个新轮次）⇒ 不得报丢失。
	unionD := map[int]bool{}
	for _, label := range agentsRoundLabelBaseline {
		unionD[label] = true
	}
	unionD[110] = true
	if missing := agentsRoundLabelMissing(unionD, agentsRoundLabelBaseline); len(missing) != 0 {
		t.Fatalf("★ 量具自证失败：超集被判成丢失（得到 %v）⇒ R6 会误伤新一轮", missing)
	}

	// 臂 E：真实文件的正则读数必须与「逐行前缀」口径一致（防多行模式下的意外匹配）。
	realMain := agentsReadRepoFile(t, agentsMdFile)
	want := 0
	for _, line := range strings.Split(realMain, "\n") {
		if strings.HasPrefix(line, "> ") && agentsRoundHeaderRE.MatchString(line+"\n") {
			want++
		}
	}
	got := 0
	for _, c := range agentsRoundLabelCounts(realMain) {
		got += c
	}
	if got != want {
		t.Fatalf("★ 量具自证失败：整文件读数 %d ≠ 逐行读数 %d ⇒ 正则的行锚不对", got, want)
	}
}

func minIntInMap(m map[int]int) int {
	out := 0
	for k := range m {
		if out == 0 || k < out {
			out = k
		}
	}
	return out
}

func maxIntInMap(m map[int]int) int {
	out := 0
	for k := range m {
		if k > out {
			out = k
		}
	}
	return out
}
