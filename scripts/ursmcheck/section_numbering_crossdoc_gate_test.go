package ursm_test

// AUDIT-GUARD: scripts/ursmcheck 属 GUARD_PACKAGES_SLOW ⇒ 本门在 `make guards-ursmcheck` 上真跑。

// section_numbering_crossdoc_gate_test.go —— ★ **姊妹门**：★ 把【节号唯一性】这道既有门的
// 【三层搜索面缺口】在【另一份文档】上补齐。
//
// # 这道门补的是哪块缺口（★ 背景，全部实测）
//
// 仓里已有一道【节号唯一性】门：`scripts/ursmcheck/section_numbering_uniqueness_test.go`
// ⇒ 但它的**搜索面**有三层缺口（★ 2026-11-24 实测得到）：
//
//	① 【文件范围】：★ 那门只覆盖 `LESSONS.md` ＋ `docs/96小时内修订审计-20261007-1350.md`
//	   ⇒ ★★ `docs/audit/2026-11-20-会话分析系统前端实施报告.md`（★ 下称「审计文档」★）
//	   **完全在门外** ★★
//	② 【编号形态】：★ 那门只认 **`§N`** ⇒ ★★ 而审计文档的标题是**裸 `N.` 形态**
//	   （`### 7.31` / `##### 7.31.1`）⇒ ★★ ⇒ **即便把文件加进那张表、★ 也解析不出号**
//	   ⇒ ⇒ ⇒ **【恒真空绿】** ★★
//	③ 【标题层级】：★ 那门只解析 **h2–h4** ⇒ ★★ 而审计文档里有一整套 **h5**（★ 73 个 ★）
//	   ⇒ ★★ ⇒ 「h2–h4 ＋ 裸数字」清点 ⇒ 只剩 **1 对**重号；★ 「扩到 h5」⇒ **8 对** ★★
//
// ⇒ ⇒ ⇒ ★★ **「只剩一处」＝【那套口径的真值】、★ 不是【文档真值】★★
// ⇒ ⇒ ⇒ ★★ 而这两轮的真因已被实测坐实：★ 审计文档里**确实存在 8 对重号**
// ⇒ ★★ 且**每个号都指向两个不同的块** ⇒ ★★ 按号引用【全部不可判读】★★
//
// ★★ 所以本门【不】去改那道既有门（★ 它的豁免表 ＋ `expectedDuplicatePairs` pin 属上游 ★★），
// ★★ 而是【另起一道姊妹门】、★ 用【更宽的搜索面】守住这份文档 ★★
// ⇒ ★★ ⇒ 【两车道并行修同一处缺陷 ⇒ 合并后得覆盖超集】★★★★
//
// # 本门做什么（★ 三条判据 ＋ 三条自检）
//
//	J1 【零重号】：★ 审计文档里**每个节号只允许出现一次**（★ h2–h6 全扫 ＋ 裸数字形态 ★★）
//	J2 【零悬空】：★ 文档里每个 `§7.x` 引用都必须能落到一个**存在的标题**
//	   ⇒ ★★ 例外只有一个：【改号记法】——★ 同一行里有 `→` 且箭头右端是一个**存在的标题号**
//	   ⇒ ★★★ 这条例外【必须窄】：★ 没有箭头的悬空引用【照样判红】★★★
//	J3 【搜索面自检】：★ 断言解析面【没有被人悄悄收窄】
//	   （★ 标题总数 ＋ 裸形态数 ＋ 各层级数 ＋ 引用数 ★★）
//
// ★ 三条自检（★ 门必须自证有牙）：
//	S1 【解析器覆盖 h2–h6 ＋ 两种形态】⇒ ★ 用夹具喂 h2/h6 ＋ 裸形态 ⇒ 必须解析出来
//	S2 【反向夹具】⇒ ★ 喂一段【故意重复】的文本 ⇒ 必须报出重复
//	S3 【悬空夹具 ＋ 改号夹具】⇒ ★ 喂【悬空且无箭头】⇒ 必须报悬空；
//	   喂【悬空但带箭头指向存在标题】⇒ **不得**报悬空（★ 证明这条例外不是恒真放水 ★）
//
// ★★ 诚实边界（★ 必须写下来）：
//
//	① 本门只守**这一份文档**；★ 其余文档的节号重复仍由那道既有门（＋ 它的豁免表）负责。
//	② 本门【不判断】某个 `§7.x` 是不是【指另一份文档的同号章节】——★ 审计文档里存在指向
//	   外部文档同号的引用 ⇒ 若这种引用将来变成悬空、★ 本门会报红 ⇒ ★★ 那可能是【误报】、
//	   ★★ 需要人工判一次 ⇒ ★★ ⇒ 【本门给的是信号、★ 不是判决】★★
//	③ 存量：★ 本门落地时审计文档【已零重号】（★ 8 对重号已在 2026-11-24 按「保甲移乙」归位 ★★）
//	   ⇒ ★★ ⇒ 所以本门【不需要存量豁免表】★★ ★★ ⇒ ★★ **这正是「改号先于扩面」的好处** ★★

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// crossDocTarget 是本门守的那份文档（★ 相对仓根 ★）。
const crossDocTarget = "docs/audit/2026-11-20-会话分析系统前端实施报告.md"

// ── 解析器 ────────────────────────────────────────────────────────────

// crossDocHeadingRe 抓 h2–h6 的标题行（★ ★ 【刻意只到 h6】★ ⇒ 因为 h5 是本门要补的那一层 ★）。
var crossDocHeadingRe = regexp.MustCompile(`^\s*(#{2,6})\s+(\S.*)$`)

// crossDocSecFormRe = `§N` 形态（★ 那道既有门唯一认得的那种 ★）。
var crossDocSecFormRe = regexp.MustCompile(`^§\s*([0-9][0-9A-Za-z]*(?:\.[0-9A-Za-z]+)*)\s`)

// crossDocBareFormRe = 裸 `N.` 形态（★ 那道既有门完全看不见的那种 ★）。
var crossDocBareFormRe = regexp.MustCompile(`^([0-9]+(?:\.[0-9A-Za-z]+)*)\s+\S`)

// crossDocRefRe = 正文里的 `§7.x` 引用。
var crossDocRefRe = regexp.MustCompile(`§\s*(7\.[0-9]+(?:\.[0-9]+)*)`)

// crossDocNumRe = 行内任意节号形态（★ 用于抓箭头右端 ★）。
var crossDocNumRe = regexp.MustCompile(`(?:§\s*)?([0-9][0-9A-Za-z]*(?:\.[0-9A-Za-z]+)*)`)

// crossDocHeading 是一条被解析出来的标题。
type crossDocHeading struct {
	line  int
	level int
	num   string
	form  string // "sec" = `§N` 形态；"bare" = 裸 `N.` 形态
}

// parseCrossDocHeadings 解析文本里全部 h2–h6 的带号标题。
//
// ★ 两种形态都认：★ `§N` 与裸 `N.` ⇒ ★★ 这是缺口②的正面回答。
// ★ 层级到 h6：★ 这是缺口③的正面回答。
func parseCrossDocHeadings(text string) []crossDocHeading {
	var out []crossDocHeading
	for i, line := range strings.Split(text, "\n") {
		m := crossDocHeadingRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		level := len(m[1])
		body := strings.TrimSpace(m[2])
		if sm := crossDocSecFormRe.FindStringSubmatch(body); sm != nil {
			out = append(out, crossDocHeading{line: i + 1, level: level, num: sm[1], form: "sec"})
			continue
		}
		if bm := crossDocBareFormRe.FindStringSubmatch(body); bm != nil {
			out = append(out, crossDocHeading{line: i + 1, level: level, num: bm[1], form: "bare"})
		}
	}
	return out
}

// crossDocFinding 是本门的一条判据结论。
type crossDocFinding struct {
	kind string // "dup" | "dangling"
	num  string
	line int
	note string
}

// checkCrossDoc 跑 J1（零重号）与 J2（零悬空），★ 返回全部结论。
//
// ★★ J2 的【改号记法例外】只认一件事：★ 同一行里有 `→`、★ 且箭头右端能落到一个**存在的标题号**
// ⇒ ★★ ⇒ 【没有箭头的悬空引用照样判红】★★ ★★ ⇒ ★★ 这条例外【窄到不会放水】★★
func checkCrossDoc(text string) (findings []crossDocFinding, headings []crossDocHeading, refCount int) {
	headings = parseCrossDocHeadings(text)

	// J1：重号
	counts := map[string]int{}
	for _, h := range headings {
		counts[h.num]++
	}
	var dupNums []string
	for num, c := range counts {
		if c > 1 {
			dupNums = append(dupNums, num)
		}
	}
	sort.Strings(dupNums)
	for _, num := range dupNums {
		first := 0
		for _, h := range headings {
			if h.num == num {
				first = h.line
				break
			}
		}
		findings = append(findings, crossDocFinding{
			kind: "dup", num: num, line: first,
			note: fmt.Sprintf("节号 %s 出现 %d 次", num, counts[num]),
		})
	}

	// J2：悬空引用
	have := map[string]bool{}
	for _, h := range headings {
		have[h.num] = true
	}
	for i, line := range strings.Split(text, "\n") {
		if parseCrossDocHeadings(line) != nil {
			continue // ★ 标题行本身不算引用 ★
		}
		arrowTargets := map[string]bool{}
		if idx := strings.Index(line, "→"); idx >= 0 {
			rest := line[idx:]
			for _, m := range crossDocNumRe.FindAllStringSubmatch(rest, -1) {
				arrowTargets[m[1]] = true
			}
		}
		for _, m := range crossDocRefRe.FindAllStringSubmatch(line, -1) {
			refCount++
			num := m[1]
			if have[num] {
				continue
			}
			// ★★ 改号记法例外：★ 同一行的箭头右端必须指向一个【存在的标题号】 ★★
			resolved := false
			for t := range arrowTargets {
				if have[t] {
					resolved = true
					break
				}
			}
			if !resolved {
				findings = append(findings, crossDocFinding{
					kind: "dangling", num: num, line: i + 1,
					note: fmt.Sprintf("引用 §%s 在本文件里没有对应标题", num),
				})
			}
		}
	}
	return findings, headings, refCount
}

// ── J1/J2/J3：主判据 ─────────────────────────────────────────────────

// TestSectionNumberingCrossDocHasNoDuplicateAndNoDangling 是【主判据】。
func TestSectionNumberingCrossDocHasNoDuplicateAndNoDangling(t *testing.T) {
	root := hollowRepoRoot(t)
	path := filepath.Join(root, filepath.FromSlash(crossDocTarget))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s 失败：%v\n★ 这道门守的就是这份文档 ⇒ ⇒ 读不到 ⇒ 门无从判断", crossDocTarget, err)
	}
	findings, headings, _ := checkCrossDoc(string(raw))

	// ★★ 先确认解析面【不是空的】⇒ ★★ 否则「零重号」可能只是【什么都没看见】★★
	if len(headings) == 0 {
		t.Fatalf("%s：一个带号标题都没解析出来 ⇒ ⇒ 【搜索面不完整】⇒ ⇒ 「零重号」是假绿", crossDocTarget)
	}
	for _, f := range findings {
		switch f.kind {
		case "dup":
			t.Errorf("★ %s：★ 【节号重号】%s（★ 行 %d）⇒ ⇒ 【按号引用不可判读】⇒ ⇒ 请改号（★ 已推的一侧不动、★ 只改自己这一侧 ★）",
				crossDocTarget, f.note, f.line)
		case "dangling":
			t.Errorf("★ %s：★ 【引用悬空】%s（★ 行 %d）⇒ ⇒ 【读者点进去找不到那一节】⇒ ⇒ 要么补标题、★ 要么改成存在的号",
				crossDocTarget, f.note, f.line)
		}
	}
}

// TestSectionNumberingCrossDocSearchSurfaceIsNotNarrowed 是【J3：搜索面自检】。
//
// ★★ 为什么需要它：★ 若有人把解析面收窄（★ 例如把 h5 去掉 ★）⇒ ★★ 那「零重号」会【假绿】
// ⇒ ★★ ⇒ 所以本门必须【断言解析面本身】★ ★★ ⇒ ★★ 【判据要对着【可判性】写、★ 而不是照抄上一轮口径】★★
func TestSectionNumberingCrossDocSearchSurfaceIsNotNarrowed(t *testing.T) {
	root := hollowRepoRoot(t)
	path := filepath.Join(root, filepath.FromSlash(crossDocTarget))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", crossDocTarget, err)
	}
	text := string(raw)
	headings := parseCrossDocHeadings(text)

	byLevel := map[int]int{}
	byForm := map[string]int{}
	for _, h := range headings {
		byLevel[h.level]++
		byForm[h.form]++
	}

	// ★ 阈值取【现盘读数】并留余量（★ 2026-11-24 实测：★ 标题 278 ＝ h3 69 ＋ h4 136 ＋ h5 73；
	// ★ 裸形态 278／`§N` 形态 0；★ 正文 `§7.x` 引用 206 处 ★★）。
	checks := []struct {
		name string
		got  int
		min  int
	}{
		{"标题总数（h2–h6 全扫）", len(headings), 250},
		{"裸数字形态标题（★ 缺口②）", byForm["bare"], 250},
		{"h3 标题", byLevel[3], 50},
		{"h4 标题", byLevel[4], 100},
		{"h5 标题（★ 缺口③）", byLevel[5], 60},
	}
	for _, c := range checks {
		if c.got < c.min {
			t.Errorf("★ %s：★ %s 只有 %d 个（★ 期望 ≥ %d）⇒ ⇒ 【搜索面被收窄】⇒ ⇒ 「零重号」这条判据已经【假绿】了",
				crossDocTarget, c.name, c.got, c.min)
		}
	}

	// ★ 引用面同样要【不小】⇒ ★★ 否则 J2 也在空转 ★★
	findings, _, refCount := checkCrossDoc(text)
	_ = findings
	if refCount < 150 {
		t.Errorf("★ %s：★ 正文里只解析出 %d 处 `§7.x` 引用（★ 期望 ≥ 150）⇒ ⇒ 【引用面被收窄】⇒ ⇒ J2 可能在空转",
			crossDocTarget, refCount)
	}
}

// ── 自检臂（★ 门必须自证有牙 ★）──────────────────────────────────────

// TestSectionNumberingCrossDocCheckerHasTeeth 是【自检臂】：★ 喂夹具、★ 门必须按预期反应。
//
// ★ 三个臂：
//	M1 【重复夹具】⇒ 必须报出重号
//	M2 【悬空且无箭头】⇒ 必须报出悬空
//	M3 【悬空但带箭头指向存在标题】⇒ **不得**报悬空（★ 证明那条例外不是恒真放水 ★）
func TestSectionNumberingCrossDocCheckerHasTeeth(t *testing.T) {
	const dupFixture = "### 7.10 甲\n### 7.11 乙\n### 7.10 丙\n"
	const danglingFixture = "★ 见 §7.99 的那段 ★\n"
	// ★ M3 的夹具必须【自带那个被指向的标题】★ ⇒ ★★ 否则「右端是存在标题」这个前提
	// ★★ 本来就不成立 ⇒ ★★ ⇒ 门会（正确地）报悬空 ⇒ ⇒ 那就【测不到例外本身】★★
	const renameFixture = "### 7.11 目标节\n★ 见 §7.99 → §7.11 ★\n"
	// ★ M4：★ 有箭头、★ 但箭头右端【不是存在标题】⇒ ★★ 必须照样报悬空 ★★
	// ⇒ ⇒ ★★ 这条臂钉住「例外【要求右端存在】」⇒ ★★ 防止例外退化成恒真放水 ★★
	const renameBrokenFixture = "### 7.11 目标节\n★ 见 §7.99 → §7.88 ★\n"

	// M1：重复
	f, _, _ := checkCrossDoc(dupFixture)
	dupFound := false
	for _, x := range f {
		if x.kind == "dup" && x.num == "7.10" {
			dupFound = true
		}
	}
	if !dupFound {
		t.Errorf("★ 自检臂 M1 红：★ 故意重复的夹具（★ `### 7.10` 出现两次 ★）没被报出来 ⇒ ⇒ 门【没牙】")
	}

	// M2：悬空且无箭头
	f, _, _ = checkCrossDoc(danglingFixture)
	danglingFound := false
	for _, x := range f {
		if x.kind == "dangling" && x.num == "7.99" {
			danglingFound = true
		}
	}
	if !danglingFound {
		t.Errorf("★ 自检臂 M2 红：★ 悬空引用（★ `§7.99` 且【行内没有箭头】★）没被报出来 ⇒ ⇒ 门【没牙】")
	}

	// M4：有箭头、★ 但箭头右端【不存在】⇒ 必须照样报悬空
	f, _, _ = checkCrossDoc(renameBrokenFixture)
	brokenFound := false
	for _, x := range f {
		if x.kind == "dangling" && x.num == "7.99" {
			brokenFound = true
		}
	}
	if !brokenFound {
		t.Errorf("★ 自检臂 M4 红：★ `§7.99 → §7.88`（★ 右端【不是存在标题】★）没被报出来 ⇒ ⇒ 【改号记法例外】退化成【恒真放水】了")
	}

	// M3：悬空但有箭头指向存在标题 ⇒ 不得报
	f, _, _ = checkCrossDoc(renameFixture)
	for _, x := range f {
		if x.kind == "dangling" && x.num == "7.99" {
			t.Errorf("★ 自检臂 M3 红：★ 改号记法（★ `§7.99 → §7.11`、★ 右端是存在标题 ★）被误报成悬空 ⇒ ⇒ 【改号记法例外】失效了")
		}
	}
}

// TestSectionNumberingCrossDocParserCoversAllLevelsAndForms 是【自检臂 S1】：
// ★ 解析器必须同时认【h2–h6 六个层级】＋【两种编号形态】。
//
// ★★ 为什么单独钉：★ 那道既有门只解析 h2–h4 ＋ 只认 `§N` ⇒ ★★ 而本门要守的文档
// ★★ 【偏偏是 h5 ＋ 裸形态】⇒ ★★ ⇒ 若本门的解析器也只认 h2–h4 ＋ `§N` ⇒ ★★ 它会【恒真空绿】★★
func TestSectionNumberingCrossDocParserCoversAllLevelsAndForms(t *testing.T) {
	const fixture = "## §1.1 一级\n### 1.2 二级裸形态\n#### 1.3 三级\n##### 1.4 四级\n###### 1.5 五级\n"
	headings := parseCrossDocHeadings(fixture)
	if len(headings) != 5 {
		t.Fatalf("★ 自检臂 S1 红：★ 夹具里 5 条标题只解析出 %d 条 ⇒ ⇒ 解析器的层级覆盖不全 ⇒ ⇒ 门会【恒真空绿】", len(headings))
	}
	levels := map[int]bool{}
	forms := map[string]bool{}
	for _, h := range headings {
		levels[h.level] = true
		forms[h.form] = true
	}
	for lvl := 2; lvl <= 6; lvl++ {
		if !levels[lvl] {
			t.Errorf("★ 自检臂 S1 红：★ 解析器没认出 h%d ⇒ ⇒ 层级覆盖不全", lvl)
		}
	}
	if !forms["sec"] {
		t.Errorf("★ 自检臂 S1 红：★ 解析器没认出 `§N` 形态")
	}
	if !forms["bare"] {
		t.Errorf("★ 自检臂 S1 红：★ 解析器没认出裸 `N.` 形态 ⇒ ⇒ 【缺口②】没被补上")
	}
}
