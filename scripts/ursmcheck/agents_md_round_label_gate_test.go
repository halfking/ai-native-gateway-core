//go:build !integration

package ursm_test

// AUDIT-GUARD: scripts/ursmcheck 属 GUARD_PACKAGES_SLOW ⇒ 本门在 `make guards-ursmcheck` 上真跑。
//
// agents_md_round_label_gate_test.go —— AGENTS.md「逐轮 L 标记」的三条完整性门
// （2026-11-25 第 28 轮立 R5/R6；本会话系列第 29 轮补 R7）。
//
// ★★ 为什么立这道门（★ 实测到的三类事故，★ 都没有门兜 ★★）：
//
//	① 同号撞车：两条车道共用同一份 AGENTS.md、各写各的「L 编号」。第 28 轮开场时
//	   顶部是 L107 ⇒ 若照惯例写 L108 ⇒ 会与并行车道 06:10 刚落的 L108 撞号
//	   （内容完全不同）⇒ 「见 L108」这种交叉引用从此不可判读。当天靠开写前重新
//	   fetch 才避开 ⇒ 靠人记不住 ⇒ 要有门。
//	② 整块丢失：AGENTS.md 顶部是人工合并的高频冲突区 ⇒ 段头/整块被覆盖或丢行。
//	   当天实测两起：L87 整块（10 行 + 「⑩ 订正」行）在 01:36 的合并提交 570087c16
//	   里被丢；L108 段头在 34b4a9a6f 里被顶部的 L109 覆盖掉。两者都是「内容在 git
//	   历史和报告侧都还在、唯独交接面没了」⇒ 没有任何门会红。
//	③ 块内段落重复：合并提交 29b3dfbd5 解两条车道的 L115 撞号时，把自己的块改号成
//	   L116、并给「⑤ 状态与待 owner」补了一句订正 —— 但新 ⑤ 是**追加**在旧 ⑤ 下面的、
//	   旧 ⑤ 没删 ⇒ 同一块里 ⑤ 出现两遍（前一行是后一行的前缀）⇒ 段头还在、行数只多不少
//	   ⇒ R5、R6 与归档查重那一族全部放行 ⇒ 同一次合并还在块间留了一行孤立的
//	   `=======`（由冲突标记门的判据④兜）。
//
// 判据：
//
//	R5 主档内唯一：AGENTS.md 里每个 L 标记的**段头**至多 1 行。
//	R6 只许搬、不许丢：主档 ∪ 归档档里出现过的 L 标记集合，必须是
//	   本门登记的基线集合的超集（归档＝把段头搬进归档档 ⇒ 并集不变）。
//	R7 块内段号唯一：每个轮块里「段首编号」（`> ★★★★★ **① …` 形态的 ①②③…，含 ⑦b
//	   这类字母后缀子项）至多出现 1 次；隔着空行、但【编号＋方括号标题】一字不差的
//	   也算同一段的两份稿。
//
// ★ 边界（诚实登记）：
//   - R6 只保证「段头还在」，不保证段头下面的正文行数不变（正文行的丢失要靠
//     agents_md_archive_duplicate_gate_test.go 那族与人工合并纪律）。
//   - R7 只保证「段首编号不重复」：不带编号的正文行丢了、或两段编号不同却内容相同，
//     仍不在判据面里。R7 只认「行首引用标记 ⇒ 星号 ⇒ `**` ⇒ 编号令牌」这一种形态
//     （主档现行与早期形态都是它）；列表项（`> - **①`）与行中间的编号不计数。
//   - 历史上已有一次同号：L79 被 16:4x 与 17:5x 两轮先后用过（一在主档、一在归档
//     档）⇒ R5 只管主档内唯一 ⇒ 该历史同号如实放行、并以日志披露。
//   - 归档档内部有历史原因的逐字重复（L21、L74 各两份）⇒ 不属本门判据 ⇒ 只披露。

import (
	"fmt"
	"reflect"
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
// 存在的全部 L 标记（48 个；★ 第 28 轮落盘时为 45 个，其后各轮只追加）。
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
	105, 106, 107, 108, 109, 110, 111, 112,
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

// agentsRoundParaRE 匹配轮块里的「段首编号」行，编号令牌＝带圈数字＋可选的字母数字后缀：
//
//	> ★★★★★ **① ★★ 【…】★★★**：…      （主档现行形态：编号后面带方括号标题）
//	> ★★ **① …**                          （早期形态：没有方括号标题）
//	> ★ **⑦ …** ／ > ★ **⑦b …** ／ > ★ **⑦m …**   （旧式轮块的字母后缀子项：L88 实测 ⑦–⑦m 共 13 行）
//
// 只认「行首引用标记 ⇒ 星号 ⇒ `**` ⇒ 编号令牌」；列表项（`> - **①`）与行中间的编号不匹配。
// 后缀必须进令牌：只取带圈数字的话，⑦b…⑦m 会被当成 13 个 ⑦ ⇒ 旧式轮块被误判成重复（★ 落地前实测撞到）。
var agentsRoundParaRE = regexp.MustCompile(`^>\s*★*\s*\*\*([①-⑳][A-Za-z0-9-]*)\s*★*\s*(?:【([^】]*)】)?`)

// agentsRoundPara 轮块里的一个「段首编号」行。
type agentsRoundPara struct {
	Label string // ★ 所属轮块的标记，如 "L116"
	Num   string // ★ 段首编号
	Title string // ★ 方括号里的标题；早期形态没有 ⇒ 空串
	Line  int    // ★ 行号（从 1 起）
	Run   int    // ★ 所在的【连续引用段】序号：引用一断（空行等）就换一段
}

// agentsRoundParas 返回主档里全部段首编号行（按出现顺序）。
//
// 轮块＝段头行（agentsRoundHeaderRE）到下一个段头为止；块内每个连续引用段
// （以空行或别的非引用行分界）是一个 Run —— 旧式轮块（如 L88、L104）里带多段
// 各自从 ① 重新编号的补记，靠这个分段彼此隔开、不算重复。
func agentsRoundParas(text string) []agentsRoundPara {
	var out []agentsRoundPara
	label, run := "", 0
	for i, line := range strings.Split(text, "\n") {
		if m := agentsRoundHeaderRE.FindStringSubmatch(line); m != nil {
			label, run = "L"+m[1][1:], 0
			continue
		}
		if !strings.HasPrefix(line, ">") {
			run++ // ★ 引用行断开 ⇒ 下一段
			continue
		}
		if label == "" {
			continue // ★ 第一个段头之前的引用行 ⇒ 不属于任何轮块
		}
		if m := agentsRoundParaRE.FindStringSubmatch(line); m != nil {
			title := ""
			if len(m) > 1 {
				title = strings.TrimSpace(m[2])
			}
			out = append(out, agentsRoundPara{Label: label, Num: m[1], Title: title, Line: i + 1, Run: run})
		}
	}
	return out
}

// agentsRoundParaDups 返回 R7 的判红组：同一轮块里出现 ≥2 次的段首编号
// （键＝轮块标记 ⇒ 段首编号 ⇒ 行号升序）。
//
// 两种形态都算重复：
//   - 同一个连续引用段里编号相同（★ 相邻形态：新稿追加在旧稿正下方）；
//   - 同一轮块里【编号＋方括号标题】都相同（★ 隔着空行的旧稿：标题一字不差 ⇒ 是同一段的两份稿）。
//
// 没有方括号标题的早期形态只按第一种形态比（它们的标题无从对齐）。
func agentsRoundParaDups(paras []agentsRoundPara) map[string]map[string][]int {
	seen := map[string]map[int]bool{} // ★ 判据键 ⇒ 行号集合
	for i, p := range paras {
		for j := i + 1; j < len(paras); j++ {
			q := paras[j]
			if q.Label != p.Label || q.Num != p.Num {
				continue
			}
			sameRun := q.Run == p.Run
			sameTitle := p.Title != "" && q.Title == p.Title
			if !sameRun && !sameTitle {
				continue
			}
			key := p.Label + "\x00" + p.Num
			if seen[key] == nil {
				seen[key] = map[int]bool{}
			}
			seen[key][p.Line] = true
			seen[key][q.Line] = true
		}
	}
	dups := map[string]map[string][]int{}
	for key, lines := range seen {
		parts := strings.SplitN(key, "\x00", 2)
		if dups[parts[0]] == nil {
			dups[parts[0]] = map[string][]int{}
		}
		for line := range lines {
			dups[parts[0]][parts[1]] = append(dups[parts[0]][parts[1]], line)
		}
		sort.Ints(dups[parts[0]][parts[1]])
	}
	return dups
}

// agentsRoundLabelNum 把「L116」还原成 116（★ 只用于排序与日志）。
func agentsRoundLabelNum(label string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(label, "L"))
	if err != nil {
		return 0
	}
	return n
}

const agentsRoundParaNumOrder = "①②③④⑤⑥⑦⑧⑨⑩⑪⑫⑬⑭⑮⑯⑰⑱⑲⑳"

// TestAgentsMdRoundParaNumberingUniqueInBlock —— R7：轮块内段首编号不重复。
func TestAgentsMdRoundParaNumberingUniqueInBlock(t *testing.T) {
	paras := agentsRoundParas(agentsReadRepoFile(t, agentsMdFile))
	if len(paras) == 0 {
		t.Fatalf("★ %s 里一条「段首编号」行都匹配不到 ⇒ ⇒ ⇒ 判据可能已失效（★ 别按本门结论改代码）",
			agentsMdFile)
	}
	dups := agentsRoundParaDups(paras)
	labels := make([]string, 0, len(dups))
	for label := range dups {
		labels = append(labels, label)
	}
	sort.Slice(labels, func(i, j int) bool {
		return agentsRoundLabelNum(labels[i]) < agentsRoundLabelNum(labels[j])
	})
	for _, label := range labels {
		nums := make([]string, 0, len(dups[label]))
		for num := range dups[label] {
			nums = append(nums, num)
		}
		sort.Slice(nums, func(i, j int) bool {
			return strings.Index(agentsRoundParaNumOrder, nums[i]) < strings.Index(agentsRoundParaNumOrder, nums[j])
		})
		for _, num := range nums {
			lines := dups[label][num]
			t.Errorf("★ %s 的 %s 块里段首编号 %s 出现了 %d 次（行 %v）⇒ ⇒ ⇒ "+
				"多半是合并/改稿时把新稿【追加】在旧稿下面、旧稿没删 ⇒ "+
				"★ 修法：保留信息最全的那一行（通常是后一行）、删掉其余；★ 别改编号去迁就重复",
				agentsMdFile, label, num, len(lines), lines)
		}
	}
	blocks := map[string]bool{}
	for _, p := range paras {
		blocks[p.Label] = true
	}
	t.Logf("★ R7：主档里段首编号 %d 行（分布在 %d 个轮块）、块内重复 %d 组", len(paras), len(blocks), len(dups))
}

// TestAgentsMdRoundParaDetectorBites —— R7 量具自证：检测器必须抓到「块内重复」（相邻与隔空行两种形态），
// 且不得把「跨块同号」「列表项」「分段重新编号」误判成重复。
func TestAgentsMdRoundParaDetectorBites(t *testing.T) {
	const header = "> ★★★★ **★ L%d（★ 2026-11-25 06:1x）：★★★**【★ 某轮】\n"
	dupsOf := func(text string) map[string]map[string][]int {
		return agentsRoundParaDups(agentsRoundParas(text))
	}

	// 臂 A：同一连续引用段里 ⑤ 两行（★ 后一行是前一行加一句 ⇒ 2026-11-25 真实事故的形态）⇒ 恰好一组、两个行号。
	a := fmt.Sprintf(header, 116) +
		"> ★★★★★ **① ★★ 【甲】★★★**：…\n" +
		"> ★★★★★ **⑤ ★★ 【状态与待 owner】★★★**：旧稿\n" +
		"> ★★★★★ **⑤ ★★ 【状态与待 owner】★★★**：旧稿｜补了一句\n"
	if dups := dupsOf(a); len(dups) != 1 || !reflect.DeepEqual(dups["L116"], map[string][]int{"⑤": {3, 4}}) {
		t.Fatalf("★ 量具自证失败：相邻形态的块内重复没抓准（得到 %#v）⇒ R7 恒绿", dupsOf(a))
	}

	// 臂 A2：旧稿与新稿之间隔了一个空行、但【编号＋标题】一字不差 ⇒ 也是同一段的两份稿 ⇒ 必须报。
	a2 := fmt.Sprintf(header, 116) +
		"> ★★★★★ **⑤ ★★ 【状态与待 owner】★★★**：旧稿\n" +
		"\n" +
		"> ★★★★★ **⑤ ★★ 【状态与待 owner】★★★**：旧稿｜补了一句\n"
	if dups := dupsOf(a2); len(dups) != 1 || !reflect.DeepEqual(dups["L116"], map[string][]int{"⑤": {2, 4}}) {
		t.Fatalf("★ 量具自证失败：隔空行的同题旧稿没抓到（得到 %#v）⇒ R7 只能抓相邻形态", dupsOf(a2))
	}

	// 臂 B：两个轮块各有一个 ⑤ ⇒ 判据是【块内】⇒ 不得报。
	b := fmt.Sprintf(header+"> ★★★★★ **⑤ ★★ 【甲】】\n\n", 115) +
		fmt.Sprintf(header+"> ★★★★★ **⑤ ★★ 【乙】】\n", 116)
	if dups := dupsOf(b); len(dups) != 0 {
		t.Fatalf("★ 量具自证失败：跨块同号被判成重复（得到 %#v）⇒ R7 会误伤", dups)
	}

	// 臂 C：列表项里的编号不计数（★ ① 出现两次都不许报）；行首形态的 ② 重复 ⇒ 必须报。
	c := fmt.Sprintf(header, 116) +
		"> - **① 列表项甲**\n" +
		"> - **① 列表项乙**\n" +
		"> ★★★★★ **② ★★ 【甲】】 正文里还提到 ②\n" +
		"> ★★★★★ **② ★★ 【乙】】\n"
	if dups := dupsOf(c); len(dups) != 1 || !reflect.DeepEqual(dups["L116"], map[string][]int{"②": {4, 5}}) {
		t.Fatalf("★ 量具自证失败：列表项/行首形态的区分不对（得到 %#v）", dupsOf(c))
	}

	// 臂 D：隔空行、编号相同但标题不同 ⇒ 是两段不同的补记 ⇒ 不得报。
	d := fmt.Sprintf(header, 116) +
		"> ★★★★★ **① ★★ 【首轮读数】】\n" +
		"\n" +
		"> ★★★★★ **① ★★ 【补记读数】】\n"
	if dups := dupsOf(d); len(dups) != 0 {
		t.Fatalf("★ 量具自证失败：分段重新编号被判成重复（得到 %#v）⇒ R7 会误伤旧式多补记的轮块", dups)
	}

	// 臂 E：没有方括号标题的早期形态 ⇒ 只按连续引用段比：同段重复要报、隔空行不报。
	e1 := fmt.Sprintf(header, 116) + "> ★★ **① 甲**\n> ★★ **① 乙**\n"
	if dups := dupsOf(e1); len(dups) != 1 || !reflect.DeepEqual(dups["L116"], map[string][]int{"①": {2, 3}}) {
		t.Fatalf("★ 量具自证失败：早期形态的同段重复没抓到（得到 %#v）", dupsOf(e1))
	}
	e2 := fmt.Sprintf(header, 116) + "> ★★ **① 甲**\n\n> ★★ **① 乙**\n"
	if dups := dupsOf(e2); len(dups) != 0 {
		t.Fatalf("★ 量具自证失败：早期形态隔空行被判成重复（得到 %#v）⇒ R7 会误伤", dups)
	}

	// 臂 G：字母后缀子项（★ L88 的真实形态）不是重复：⑦、⑦b、⑦m 同在一个连续引用段里 ⇒ 不得报。
	g := fmt.Sprintf(header, 88) +
		"> ★ **⑥ 甲**\n" +
		"> ★ **⑦ 乙**\n" +
		"> ★ **⑦b 丙**\n" +
		"> ★ **⑦m 丁**\n" +
		"> ★ **⑧ 戊**\n"
	if dups := dupsOf(g); len(dups) != 0 {
		t.Fatalf("★ 量具自证失败：字母后缀子项被判成重复（得到 %#v）⇒ R7 会误伤旧式轮块", dups)
	}

	// 臂 H：真实文件的读数必须与「逐行前缀」口径对得上（防行锚或分块逻辑失准后多数行被吞掉）。
	realMain := agentsReadRepoFile(t, agentsMdFile)
	want := 0
	for _, line := range strings.Split(realMain, "\n") {
		if strings.HasPrefix(line, ">") && agentsRoundParaRE.MatchString(line) {
			want++
		}
	}
	if got := len(agentsRoundParas(realMain)); got == 0 || got > want {
		t.Fatalf("★ 量具自证失败：索引读数 %d 与逐行读数 %d 对不上 ⇒ 行锚或分块逻辑失准", got, want)
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
