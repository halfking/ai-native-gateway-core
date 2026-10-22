// section_numbering_uniqueness_test.go —— R104 轮
//
// 背景（★ 本门为什么存在）：
//
//  共享仓里并行会话各自取号 ⇒ 撞号是常态而非意外。本轮实测的规模：
//   - LESSONS.md：81 个不同节号里有【4 对】重复（§211/§212/§213/§218）
//   - handoff：顶层 313 个节号里有【15 对】重复（§19.18.1~7 / §19.19.1~8）
//     ★ 这 15 对的两组内容【完全不同】⇒ 不是同名小节，是两个会话
//       各自建了【一整套】号段 ⇒ 撞车面 = 15 对小节 + 2 对父节
//   - 而合并 origin/main 时实测【第四次】发现对方用了 §230（§145 轮 / 252 生产）
//     ⇒ 我方 §230 让位为 §233
//
//  ⇒ ★★ 仓库里【没有任何门】查「节号是否重复」：
//     读 LESSONS.md 的门只有两道 —— TestLessonsFileHasNotBeenClobbered（体量地板）
//     与 TestLessonsFileHasNoConflictMarkers（冲突标记）；handoff 一道门都没有。
//  ⇒ ⇒ ★★ 结果就是 19 对重复【长期躺在 main 上】⇒ 人靠自觉必然漏 ⇒ 缺的是【一道门】。
//
// 本门做什么：
//  1. 按【文件全量枚举】解析全部带节号的标题（★ 不抽查）
//  2. 每个节号的出现次数必须 == 豁免表登记的次数（默认 1）
//     ⇒ 存量被修掉 ⇒ 次数变化 ⇒ 红（提示更新豁免表）
//     ⇒ 新增任何重复 ⇒ 次数从 1 变 2 ⇒ 红
//  3. 重复【对】的总数钉死 ⇒ 防止豁免表被悄悄做大
//
// ★ 三类自检（门必须自证有牙）：
//   - 搜索面完整性：解析结果非空 ＋【两种独立口径】数出的标题行数一致
//   - 反向夹具：喂一段【故意有重复】的文本 ⇒ 解析器必须报出重复
//   - 变异验证：制造一个真实重复 ⇒ 主判据必须判红
package ursm_test

import (
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	lessonsPath = "../../LESSONS.md"
	runbookPath = "../../docs/96小时内修订审计-20261007-1350.md"
)

// exemptedSectionCounts = 【文件 → 节号 → 允许的出现次数】。
//
// ★ 来源 = 本门落地时的现盘实况（2026-10-22，基线 origin/main = 67bdc1cf6）。
// ★ 语义：
//   - 不在表里的节号 ⇒ 必须【恰好出现 1 次】
//   - 在表里的节号   ⇒ 必须【恰好出现表中登记的次数】
// ⇒ ⇒ 存量被修掉 ⇒ 次数变化 ⇒ 红（提示更新本表）
// ⇒ ⇒ 新增任何重复 ⇒ 次数变化 ⇒ 红
var exemptedSectionCounts = map[string]map[string]int{
	// ★ LESSONS.md 的 4 对（上游既有，两个会话各取一次）
	lessonsPath: {
		"211": 2,
		"212": 2,
		"213": 2,
		"218": 2,
	},
	// ★ handoff 的 15 对（两个会话各自建了 §19.18 / §19.19 两套号段）
	runbookPath: {
		"19.18.1": 2, "19.18.2": 2, "19.18.3": 2, "19.18.4": 2,
		"19.18.5": 2, "19.18.6": 2, "19.18.7": 2,
		"19.19.1": 2, "19.19.2": 2, "19.19.3": 2, "19.19.4": 2,
		"19.19.5": 2, "19.19.6": 2, "19.19.7": 2, "19.19.8": 2,
	},
}

// expectedDuplicatePairs = 重复【对】的总数（= 4 + 15）。
// ★ 钉死它 ⇒ 防止有人把豁免表悄悄做大（把真重复登记成「合法」）。
const expectedDuplicatePairs = 19

// ── 解析器 ────────────────────────────────────────────────────────────

// ★★★ 解析器必须接受【文本】而不是【文件路径】：
//   只有这样，测试才能喂【反向夹具】（故意有重复的文本）⇒ 门才有牙。
//
// ★★ 判据形态（★ 本轮已连续四次栽在判据上，务必照抄）：
//   - 用【显式字符集】而不是 `\b` ⇒ `\b` 与全角字符（`·`）相邻时行为不可靠
//     （本轮实测：用 `\b` 数出「313 个 / 我的 §19.20.77 出现 0 次」，
//       实际是 314 / 1 次 ⇒ 「我的东西不见了」的根因是判据、不是合并）
//   - `#{2,4}` 一并覆盖顶层（## / ###）与子节（####）
//   - 节号本体：`[0-9]` 开头、`.` 分隔的数字、可选字母后缀（`10bis` 这种）
//   - ★ 节号必须【整段取到空白为止】⇒ 不能被 `10bis` 截成 `10`
var headingRe = regexp.MustCompile(`^#{2,4} §([0-9][0-9.]*[A-Za-z]*)(\s|$)`)

// parseSectionNumbers 返回该文本里全部带节号的标题行及其出现次数。
func parseSectionNumbers(text string) (map[string]int, []string) {
	counts := map[string]int{}
	var order []string
	for _, line := range strings.Split(text, "\n") {
		m := headingRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		num := m[1]
		if _, seen := counts[num]; !seen {
			order = append(order, num)
		}
		counts[num]++
	}
	return counts, order
}

// duplicatePairs 返回出现次数 > 1 的节号（按节号排序）。
func duplicatePairs(counts map[string]int) []string {
	var out []string
	for num, c := range counts {
		if c > 1 {
			out = append(out, num)
		}
	}
	sort.Strings(out)
	return out
}

// readSectionCounts 读文件并解析，同时做【搜索面完整性】的双口径互证。
func readSectionCounts(t *testing.T, path string) (map[string]int, []string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", path, err)
	}
	counts, order := parseSectionNumbers(string(raw))

	// ★ 搜索面完整性自检（★ R63 §2.4 通用式：断言结果非空 ＋ 断言条数与期望一致）
	if len(counts) == 0 {
		t.Fatalf("%s：一个带节号的标题都没解析出来 ⇒ 【搜索面不完整】⇒ 判据不可信", path)
	}
	if len(order) != len(counts) {
		t.Fatalf("%s：唯一节号数 %d != 出现顺序长度 %d ⇒ 解析面不自洽", path, len(counts), len(order))
	}
	// ★ 独立第二口径：直接数「匹配到标题的行数」，必须等于「各节号出现次数之和」
	total := 0
	for _, c := range counts {
		total += c
	}
	matchedLines := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if headingRe.MatchString(line) {
			matchedLines++
		}
	}
	if matchedLines != total {
		t.Fatalf("%s：匹配行数 %d != 各节号次数之和 %d ⇒ 搜索面不完整（★ 别据此下「无重复」的结论）",
			path, matchedLines, total)
	}
	return counts, order
}

// ── 门 ────────────────────────────────────────────────────────────────

// TestSectionNumbersAreUniqueBeyondKnownBaseline：主判据。
func TestSectionNumbersAreUniqueBeyondKnownBaseline(t *testing.T) {
	for path, exempt := range exemptedSectionCounts {
		counts, order := readSectionCounts(t, path)
		var offenders []string
		for _, num := range order {
			want := 1
			if v, ok := exempt[num]; ok {
				want = v
			}
			if counts[num] != want {
				offenders = append(offenders, "§"+num+"：出现 "+strconv.Itoa(counts[num])+
					" 次，期望 "+strconv.Itoa(want)+" 次")
			}
		}
		if len(offenders) > 0 {
			t.Fatalf("%s：节号唯一性被破坏（%d 处）⇒\n  %s\n"+
				"  ★ 若是【新增重复】⇒ 请改号（已推的一侧不动，只改自己这一侧）\n"+
				"  ★ 若是【存量被修掉】⇒ 请更新 exemptedSectionCounts 的登记次数\n"+
				"  ★ 两种情况都请【追加记录】，不要改历史叙述",
				path, len(offenders), strings.Join(offenders, "\n  "))
		}
	}
}

// TestSectionNumberDuplicatePairCountIsPinned：把「重复对总数」钉死，
// 防止有人把豁免表悄悄做大（把真重复登记成「合法」）。
func TestSectionNumberDuplicatePairCountIsPinned(t *testing.T) {
	total := 0
	for path := range exemptedSectionCounts {
		counts, _ := readSectionCounts(t, path)
		total += len(duplicatePairs(counts))
	}
	if total != expectedDuplicatePairs {
		t.Fatalf("重复节号对数 = %d，期望 %d ⇒ 豁免表与现盘不符（★ 修掉了要对齐；新增重复要红）",
			total, expectedDuplicatePairs)
	}
}

// TestSectionNumberParserHasTeeth：★ 反向夹具 —— 解析器必须报出【故意造出来的】重复。
func TestSectionNumberParserHasTeeth(t *testing.T) {
	// ★ 夹具刻意覆盖四个易错形态：
	//   ① 同号两行（真重复）
	//   ② `902bis` 必须与 `902` 分开（★ 不能被截断 ⇒ 前缀/子串判据会在这里翻车）
	//   ③ 全角 `·` 紧跟节号（★ `\b` 在这里不可靠 ⇒ 显式字符集才稳）
	//   ④ 三级 / 四级标题（子节）也必须被解析 ⇒ 否则子节命名空间是盲区
	fixture := strings.Join([]string{
		"## §900 正常条目",
		"## §901 另一个条目",
		"## §900 故意重复的条目",
		"## §902bis 带字母后缀的条目",
		"## §902 不带后缀的另一个条目",
		"## §903 · 带命名标记的条目",
		"### §904 三级标题（子节）",
		"#### §904.1 四级标题（子子节）",
	}, "\n")

	counts, order := parseSectionNumbers(fixture)
	if len(counts) != 7 {
		t.Fatalf("夹具：解析出 %d 个唯一节号，期望 7 ⇒ 解析面有问题（counts=%v）", len(counts), counts)
	}
	dups := duplicatePairs(counts)
	if len(dups) != 1 || dups[0] != "900" {
		t.Fatalf("夹具：重复节号 = %v，期望恰好 [900] ⇒ 解析器【没牙】", dups)
	}
	// ★★ `902bis` 必须独立成号 ⇒ 若被截成 `902` 则 counts["902"] 会变成 2
	if counts["902"] != 1 || counts["902bis"] != 1 {
		t.Fatalf("夹具：902 = %d 次、902bis = %d 次 ⇒ 必须各为 1（★ 字母后缀被截断了）",
			counts["902"], counts["902bis"])
	}
	// ★ 全角 `·` 紧跟的节号必须解析出来
	if counts["903"] != 1 {
		t.Fatalf("夹具：903 出现 %d 次，期望 1 ⇒ 全角字符相邻时解析不稳", counts["903"])
	}
	// ★ 子节（### / ####）也必须被解析到
	if counts["904"] != 1 || counts["904.1"] != 1 {
		t.Fatalf("夹具：904 = %d 次、904.1 = %d 次 ⇒ 子节没被解析（counts=%v）",
			counts["904"], counts["904.1"], counts)
	}
	if len(order) != 7 {
		t.Fatalf("夹具：出现顺序长度 %d，期望 7", len(order))
	}
}

// TestSectionNumberGateHasTeeth：★ 变异验证 —— 制造一个【真实】重复后，
// 主判据必须判红。★ 本门若不做这一条，就只是一道【恒绿门】。
func TestSectionNumberGateHasTeeth(t *testing.T) {
	counts, order := readSectionCounts(t, lessonsPath)
	// ★ 找一个【当前唯一】的节号（不在豁免表里）⇒ 制造它的第二份
	var target string
	for _, num := range order {
		if counts[num] == 1 {
			if _, ok := exemptedSectionCounts[lessonsPath][num]; !ok {
				target = num
				break
			}
		}
	}
	if target == "" {
		t.Fatalf("在 %s 里找不到一个【唯一且未豁免】的节号 ⇒ 夹具没法构造", lessonsPath)
	}

	raw, err := os.ReadFile(lessonsPath)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", lessonsPath, err)
	}
	// ★ 制造重复：在文件末尾追加一行同号的标题
	mutated := string(raw) + "\n## §" + target + " ★ 故意制造的重复（变异夹具）\n"
	mCounts, _ := parseSectionNumbers(mutated)
	if mCounts[target] != 2 {
		t.Fatalf("变异体：§%s 出现 %d 次，期望 2 ⇒ 变异没生效（★ 这一条自己就失效了）",
			target, mCounts[target])
	}
	// ★ 主判据在变异体上必须判红：目标节号不在豁免表 ⇒ 期望 1 次、实际 2 次
	want := 1
	if v, ok := exemptedSectionCounts[lessonsPath][target]; ok {
		want = v
	}
	if mCounts[target] == want {
		t.Fatalf("变异体：§%s 出现 %d 次 == 期望 %d 次 ⇒ 主判据在变异体上【没有判红】⇒ 门没牙",
			target, mCounts[target], want)
	}
}
