package ursm_test

// audit_registry_table_sync_test.go —— 登记项**副本漂移**门（2026-11-03 第三十四轮）
//
// # 这道门补的是哪块缺口
//
// 第三十三轮实测出一条**结构**缺陷：**同一批登记项在审计档里被抄了很多遍**。
// 实测（第三十四轮量出来的权威计数）：
//
//	★ F13   出现在 **10** 个登记项小节（§7.6 / §8.6 / §9.6 / §13.6 / §14.8 /
//	          §19.8 / §20.7 / §21.7 / §22.8 / §23.2 / §24.6）
//	★ F28 / F29 / F30 各出现在 **10** 个小节
//	★ F4 / F5 / F9 各出现在 6 个小节
//	★ F21 / F22 / F23 各出现在 3–5 个小节
//
// ⇒ ⇒ ★★ **关掉一条、另若干条不会自动跟着关** ⇒ ⇒ **过期登记比没有登记更坏**。
//
// ★ 而这件事**主档自己早就写过**：§25.10 记录了 LESSONS §606 的同族坑
// （「同一语义维护多处 ⇒ 必然只修了一半」）⇒ ★ 第三十三轮只订正了 3 张表，
// ★ **漏掉了其余 13 个小节** ⇒ ★ 这就是那条教训**没被应用到的地方**。
//
// # ★★ 本门的核心思想：**副本分两类，只有一种必须跟着走**
//
//	① **指针型**：写「维持 §9.6 的结论」⇒ ★ **会自动跟随**（§9.6 改了它就跟着改）
//	② **复述型**：把结论**抄一遍**（「不是放大器」「维持登记（下一轮单独做）」）
//	   ⇒ ★★ **必然漂移** ⇒ ★ 这才是缺陷面
//
// ⇒ ⇒ 所以本门**不要求**「每份副本都写成一样」（★ 那会逼着人维护 10 份同样的文字，
// ★ 正是 LESSONS §606 那个坑）⇒ ⇒ 本门只要求：
//
//	★ **复述型副本一旦过期，就必须带闭合标记**（⇒ 读者一眼看出「这条已经变了」）
//	★ **规范源（§28.23）覆盖全部登记项**（⇒ 判定「过期」有唯一依据）
//
// # ★★ 判据（6 条）
//
//	C1 ★ **抽取器非空（防假绿）**：★ 规范源必须存在且条目 ≥ 15；
//	   ★ 且已知漂移面必须被认出来（F13 / F28 / F29 / F30 / F4 / F5 / F9
//	   各自出现在 **≥ 2** 个登记项小节）⇒ ⇒ 抽取器退化成「抽不到东西」时红。
//	C2 ★ **规范源覆盖**：★ 登记项小节里出现过的每个编号，★ 都必须在规范源里
//	   ⇒ ⇒ 规范源是**唯一**的现状依据。
//	C3 ★ **历史登记项小节的复述行**：★ 若该行提到的登记项在规范源里是
//	   **已闭合**，★ 而这一行又**断言了处置状态**（维持 / 仍挂着 / 单独一轮 / …）
//	   ⇒ ★ 该行**必须**带闭合标记 ⇒ 否则红。
//	C4 ★ **规范源的读数必须自洽**：★ 每行的现状格**必须**恰好落在一个
//	   状态词里（已闭合 / 已关闭 / 已实施 / 已落地 / 有意如此 / 仍开）
//	   ⇒ ⇒ 「第三种状态」（有意如此）必须**写出来**，不能混进「已闭合」。
//	C5 ★ **全景表（§23.2 / §24.6）**：★ 行里提到的编号若在规范源里是**已闭合**，
//	   ★ 该行**必须**带闭合标记 ⇒ ⇒ **规范全景表不许停在旧状态**。
//	C6 ★★ **反向对照 + 合成夹具**：★
//
//	★ (a) **反向对照**：★ 真实文档必须 **0 条违例**（⇒ 门不是恒红）
//	★ (b) **合成夹具**：★ 在**内存副本**上把一行复述行的闭合标记**摘掉**
//	  ⇒ ★ 门**必须**报出违例，且必须点名那一项
//	  ⇒ ★★ 且**先验语义有效性**：★ 摘完之后那一行确实**不再**含闭合标记
//	     （★ 本轮实测踩过的坑：★ 只摘**半个** `~~` ⇒ 尾部 `~~` 还在 ⇒ 语义没变
//	     ⇒ 门还是绿的 ⇒ ⇒ **弱变异 = 假牙**）
//
// # ★ 为什么「摘标记」而不是「改状态」
//
// ★ 因为规范源里 F13 是**已关闭**、它的复述行**必须**带闭合标记
// ⇒ ⇒ 摘掉标记 = 把一条已闭合的登记项**伪装成仍开** ⇒ ★ 这正是本轮要找的缺陷形态
// ⇒ ⇒ 门必须对它敏感。
//
// # ★ 覆盖范围（★ 明写，避免「以为管住了」）
//
//	★ 管：★ 3 层级标题、标题含「登记项」的**登记项小节**（历史表 / 列表 / 全景表）
//	★ 管：★ 规范源（§28.23）本身的自洽性
//	★ **不管**：★ ★ 只用**符号名**指代登记项的句子（★ 例：§12.8 写
//	  `dispatchFailureIsCredentialHealthy` 而不写 F5）⇒ ⇒ **这类副本本门看不见**
//	  ⇒ ★ 要靠人工复核发现（本轮已手工订正一处，并在此登记为**覆盖缺口**）
//	★ **不管**：★ 4 层级标题的小节（★ §28.x 的轮次记录区）⇒ ★ 那是**叙述**，不是登记册
//
// # ★ 为什么规范源必须是**一张表**而不是「每轮各自订正」
//
// ★ 因为「每轮各自订正」= **维护 N 份** ⇒ ⇒ 就是 LESSONS §606 那个坑本身
// ⇒ ⇒ 本门把「现状」**收敛到一处**，★ 副本只保留**指针 + 订正标记**。

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// regAuditDocRelPath 是主审计档相对仓根的路径。
const regAuditDocRelPath = "docs/audit/2026-11-02-上游4xx被记成节点不健康-下一轮修复项.md"

// ── 状态词表（★ 刻意收窄：★ 词表越宽、门越容易恒绿）────────────────────

// regClosedStatus 是规范源里表示「已闭合」的状态词。
// ★ 注意「已落地」「已实施」也是闭合（★ F32 / F-B 用的是这两个词）。
var regClosedStatus = []string{"已闭合", "已关闭", "已实施", "已落地"}

// regIntentionalStatus 是第三种状态：**有意如此**（★ 不是缺陷、也不是待办）。
var regIntentionalStatus = []string{"有意如此"}

// regOpenStatus 是「仍开」。
var regOpenStatus = []string{"仍开"}

// regClosedMarkers 是「这一行已经承认它闭合了」的措辞。
//
// ★ 用在**副本**上：★ 副本必须靠它们向读者表明「这条的状态已经变了」。
// ★ 「闭合」单独成词 ⇒ 「已闭合」/「本轮闭合」/「实测闭合」都能命中。
var regClosedMarkers = []string{"闭合", "已关闭", "已实施", "已落地", "已修", "已删", "✅"}

// regConclusionPhrases 是「这一行把**结论本身**抄了一遍」的措辞。
//
// ★★★ 这是本门最核心的一刀（★ 第三十四轮实测踩出来的）：
//
//	① **指针型**副本：★ 写「维持 §9.6 的结论」⇒ ★ **自动跟随**（§9.6 改了它就跟着改）
//	   ⇒ ⇒ **不需要**闭合标记（★ 逼它加 = 逼人维护 N 份同样的文字 = LESSONS §606 那个坑）
//	② **复述型**副本：★ 把结论**抄一遍**（「不是放大器」「维持登记（下一轮单独做）」）
//	   ⇒ ★★ **必然漂移** ⇒ ★ 这才是缺陷面 ⇒ ★ 过期了就**必须**带闭合标记
//
// ⇒ ⇒ 所以判据不是「有没有闭合标记」，★ 而是「**有没有把结论抄下来**」。
var regConclusionPhrases = []string{
	"仍挂着", "维持登记", "一律维持原登记", "下一轮单独做", "单独一轮", "独立一轮",
	"登记为", "待议", "本轮不动", "要改的是", "不修", "挂起",
	"不是放大器", "生产不可达", "非放大器", "不可达", "不属本机", "方向已否决",
}

// regSectionPointerRe 匹配「指回另一个小节」的指针（★ 例：§9.6、§二十三）。
var regSectionPointerRe = regexp.MustCompile(`§\s?[0-9一二三四五六七八九十百]+`)

// regAssertWords 是「这一行在断言处置状态」的措辞。
//
// ★ **只有断言行才受管**：★ 纯叙述行（某轮当时做了什么）**不算**状态声明
// ⇒ ⇒ 否则本门会去管历史叙述，那是**逼人维护 N 份历史文字**（LESSONS §606）。
var regAssertWords = []string{
	"维持", "仍挂着", "单独一轮", "独立一轮", "登记为",
	"待议", "不修", "不动", "下一轮", "挂起",
}

// ── 数据结构 ────────────────────────────────────────────────────────────

// regRow 是登记项小节里的一「行」（★ 表格行或列表项，★ 含续行）。
type regRow struct {
	Line  int      // 1-based 行号
	Text  string   // 整行文本（★ 含续行，★ 因为闭合标记常写在续行上）
	Cells []string // 表格行的格子（★ 列表行为 nil）
	IsTbl bool
}

// regSection 是审计档里的一节。
type regSection struct {
	Heading string
	Line    int
	Kind    string // "history" | "panorama" | "canonical"
	Rows    []regRow
}

// canonEntry 是规范源里的一行（★ 一行一项）。
type canonEntry struct {
	Key    string // F-项用归一编号（如 F13）；非 F 项用原文（如 §四.2）
	Raw    string // 第一格原文
	Status string // "closed" | "open" | "intentional"
	Line   int
}

// regViolation 是门报出的一条违例。
type regViolation struct {
	Section string
	Line    int
	ID      string
	Reason  string
}

func (v regViolation) String() string {
	return v.Section + ":" + strconv.Itoa(v.Line) + " [" + v.ID + "] " + v.Reason
}

// ── 解析：编号 ──────────────────────────────────────────────────────────

// extractRegIDs 从一行文本里抽出登记项编号。
//
// ★ 形如 F12 / F-21 / F-B / F28/F29/F30 ⇒ 归一成 F12 / F21 / FB / F28+F29+F30。
// ★ 手工扫而不是用正则：★ Go 的 RE2 **没有零宽断言**（lookbehind），
//
//	而「PDF4」里的 F4 必须**不**被当成登记项 ⇒ 需要看前一个字符。
func extractRegIDs(text string) []string {
	var out []string
	seen := map[string]bool{}
	for i, r := range text {
		if r != 'F' {
			continue
		}
		if i > 0 && isASCIILetter(text[i-1]) {
			continue // PDF4 / GoF5 ⇒ 前一个字符是字母 ⇒ 不是登记项
		}
		rest := text[i:]
		j := 1
		if len(rest) > 1 && (rest[1] == '-' || strings.HasPrefix(rest[1:], "–")) {
			j++
			if strings.HasPrefix(rest[1:], "–") {
				j += 2 // ★ en-dash 是 3 字节 UTF-8
			}
		}
		for j < len(rest) && rest[j] == ' ' {
			j++
		}
		k := j
		for k < len(rest) && (isASCIIDigit(rest[k]) || isASCIIUpper(rest[k])) {
			k++
		}
		if k == j {
			continue
		}
		tok := rest[:k]
		tok = strings.ReplaceAll(tok, "-", "")
		tok = strings.ReplaceAll(tok, "–", "")
		tok = strings.ToUpper(tok)
		if !regIDPattern.MatchString(tok) {
			continue
		}
		if !seen[tok] {
			seen[tok] = true
			out = append(out, tok)
		}
		_ = k
	}
	return out
}

// regIDPattern 是归一后允许的编号形状：F + 数字，或 F + 单个大写字母。
//
// ★ 第三十六轮修：★ 旧版只允许 `F([0-9]+|B)` ⇒ ★ **F-A / F-C / F-D / F-E**
// （★ 主档第七 / 十一 / 十二 / 十三轮各自的**轮次局部编号**）**在门里根本不存在**
// ⇒ ⇒ 「抽取器看不见 ⇒ 门恒绿」⇒ ★ 现在一视同仁。
var regIDPattern = regexp.MustCompile(`^F([0-9]+|[A-Z])$`)

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isASCIIDigit(c byte) bool { return c >= '0' && c <= '9' }

func isASCIIUpper(c byte) bool { return c >= 'A' && c <= 'Z' }

// ── 解析：小节与行 ──────────────────────────────────────────────────────

var regHeadingRe = regexp.MustCompile(`^(#{3,4})\s+(.*)$`)

// anyHeadingRe 匹配**任何**层级的小节标题（★ 用于判定小节边界）。
var anyHeadingRe = regexp.MustCompile(`^#{1,6}\s`)

// regSections 找出全部**登记项小节**。
//
// ★ 判定顺序（★ 顺序有意义）：
//
//	① 标题里含「规范源」⇒ canonical（★ §28.23）
//	② 标题里含「全景」⇒ panorama（★ §23.2 / §24.6）
//	③ 其余 ⇒ history
//
// ★ 且**必须是 3 层级标题**（★ §28.x 是 4 层级 ⇒ 轮次记录区 ⇒ 不算登记册）
// ★ 且**必须真的有登记项行**（★ ⇒ §24.1「真实缺口」那种散文小节自动排除）
func regSections(lines []string) []regSection {
	var secs []regSection
	cur := regSection{Kind: ""}
	flush := func() {
		if cur.Kind == "" {
			cur = regSection{}
			return
		}
		cur.Rows = collectRegRows(lines, cur)
		if len(cur.Rows) > 0 {
			secs = append(secs, cur)
		}
		cur = regSection{}
	}
	for i, l := range lines {
		if m := regHeadingRe.FindStringSubmatch(l); m != nil {
			flush()
			hashes, text := m[1], m[2]
			kind := ""
			switch {
			// ★ 规范源：★ 标题里点明「规范源」⇒ ★ **层级不限**
			// （★ 它住在 §28.x 的轮次记录区，★ 但它是**活表**、不是轮次叙述）
			case strings.Contains(text, "规范源") && strings.Contains(text, "现状总表"):
				// ★★ 定位**必须**同时命中两个词（★ 第三十四轮实测踩过）：
				// ★ 只看「规范源」一个词 ⇒ ★ 轮次记录里的一句「建**规范源**」
				// ★ 就把**那一节**误判成规范源 ⇒ ★ 它的表被当成现状表 ⇒ 门乱红
				kind = "canonical"
			case len(hashes) == 3 && strings.Contains(text, "登记项"):
				if strings.Contains(text, "全景") {
					kind = "panorama"
				} else {
					kind = "history"
				}
			}
			cur = regSection{Heading: text, Line: i + 1, Kind: kind}
			continue
		}
		// ★ 2 层级的标题（★ 例：§二十三 的下一个「## 二十四、…」）★
		// ★ **必须**结束当前小节 ⇒ ★ 否则它会被当成上一节的「行」（★ 本轮踩过）
		if anyHeadingRe.MatchString(l) {
			flush()
		}
	}
	flush()
	return secs
}

// collectRegRows 收集一节里的登记项行。
//
// ★ 两种形态：
//
//	① **表格行**：★ 行首是 `|`，★ 且**第一格**里有登记项编号
//	② **列表/散文行**：★ 行里有登记项编号
//	   ⇒ ★ 且要把**续行**并进来（★ 闭合标记常写在续行上，
//	     ★ 例：§11.8 的 F-B 那条的「仍挂着」在第 3 行）
func collectRegRows(lines []string, sec regSection) []regRow {
	start := sec.Line // 0-based index of the line **after** the heading
	end := len(lines)
	for i := start; i < len(lines); i++ {
		// ★★ 边界必须认**任何**层级的小节标题（★ 本轮修的第 4 个探测器 bug）：
		// ★ 旧版只认 3–4 层级 ⇒ ★ 2 层级的「## 二十、实施记录（… F-16 / F-17 / F-18 …）」
		// ★ 被算进了**上一节**的「行」⇒ ⇒ 门报出 6 条**假阳性**（说这些编号不在规范源里）
		if anyHeadingRe.MatchString(lines[i]) {
			end = i
			break
		}
	}
	var rows []regRow
	for i := start; i < end; i++ {
		l := lines[i]
		if strings.HasPrefix(strings.TrimSpace(l), "|") {
			cells := splitRowCells(l)
			if len(cells) == 0 {
				continue
			}
			ids := extractRegIDs(cells[0])
			if len(ids) == 0 {
				continue
			}
			rows = append(rows, regRow{Line: i + 1, Text: l, Cells: cells, IsTbl: true})
			continue
		}
		ids := extractRegIDs(l)
		if len(ids) == 0 {
			continue
		}
		// 列表/散文行：★ 并上**续行**（缩进行，★ 直到下一条列表项/标题/表格行/空行）
		//
		// ★★ 本轮修的两个探测器 bug：
		// ① ★ **不许跳行**（★ 旧版每 append 一行就 j++ ⇒ **每行都跳过一个**）
		// ② ★ 行号报**首行**（★ 旧版报的是末行 ⇒ ★ 定位会指错地方）
		startLine := i + 1
		text := l
		last := i
		for j := i + 1; j < end; j++ {
			nxt := lines[j]
			if strings.TrimSpace(nxt) == "" {
				break
			}
			if anyHeadingRe.MatchString(nxt) || strings.HasPrefix(strings.TrimSpace(nxt), "|") {
				break
			}
			if !isIndented(nxt) {
				break
			}
			if strings.HasPrefix(strings.TrimSpace(nxt), "- ") {
				break
			}
			text += "\n" + nxt
			last = j
		}
		i = last
		rows = append(rows, regRow{Line: startLine, Text: text})
	}
	return rows
}

func isIndented(s string) bool {
	return len(s) > 0 && (s[0] == ' ' || s[0] == '\t')
}

// splitRowCells 切表格行的格子；不是表格行则返回 nil。
func splitRowCells(l string) []string {
	s := strings.TrimSpace(l)
	if !strings.HasPrefix(s, "|") || !strings.HasSuffix(s, "|") || len(s) < 2 {
		return nil
	}
	inner := s[1 : len(s)-1]
	cells := strings.Split(inner, "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	// 分隔行（|---|---|）不算数据行
	allSep := true
	for _, c := range cells {
		if strings.Trim(c, "-: ") != "" {
			allSep = false
			break
		}
	}
	if allSep {
		return nil
	}
	return cells
}

// ── 解析：规范源 ────────────────────────────────────────────────────────

// regCanonical 解析规范源（★ §28.23）⇒ 返回每一项的现状。
func regCanonical(lines []string) []canonEntry {
	var out []canonEntry
	for _, sec := range regSections(lines) {
		if sec.Kind != "canonical" {
			continue
		}
		for _, r := range sec.Rows {
			if len(r.Cells) < 2 {
				continue
			}
			raw := r.Cells[0]
			statusCell := r.Cells[1]
			ids := extractRegIDs(raw)
			key := ""
			if len(ids) > 0 {
				key = ids[0]
			} else {
				key = strings.TrimSpace(raw)
				key = strings.Trim(key, "*")
			}
			if key == "" {
				continue
			}
			out = append(out, canonEntry{
				Key:    key,
				Raw:    raw,
				Status: classifyStatus(statusCell),
				Line:   r.Line,
			})
		}
	}
	return out
}

// classifyStatus 把规范源「现状」格归成三类之一。
func classifyStatus(cell string) string {
	closed := containsAny(cell, regClosedStatus)
	intent := containsAny(cell, regIntentionalStatus)
	open := containsAny(cell, regOpenStatus)
	switch {
	case closed && !intent && !open:
		return "closed"
	case intent && !closed && !open:
		return "intentional"
	case open && !closed && !intent:
		return "open"
	}
	return "mixed"
}

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// ── 门的主体 ───────────────────────────────────────────────────────────

// checkRegistry 跑全部判据，★ 返回违例列表（★ 空 ⇒ 绿）。
//
// ★ 纯函数（★ 只吃行数组）⇒ ⇒ 合成夹具可以在**内存副本**上跑（★ 仓内真文件零改动）。
func checkRegistry(lines []string) []regViolation {
	var vs []regViolation
	secs := regSections(lines)
	canon := regCanonical(lines)

	// ── C1：抽取器非空（防假绿）──
	if len(canon) == 0 {
		return []regViolation{{Section: "规范源", Line: 0, ID: "-", Reason: "★ 规范源（§28.23）不存在或没抽到条目 ⇒ 门恒绿 = 形同虚设"}}
	}
	if len(canon) < 15 {
		vs = append(vs, regViolation{Section: "规范源", Line: 0, ID: "-",
			Reason: "★ 规范源条目数 " + strconv.Itoa(len(canon)) + " < 15 ⇒ 抽取器可能退化了"})
	}
	// ★ 已知漂移面必须被认出来（★ 抽取器的**牙齿检验**）
	driftProne := []string{"F13", "F28", "F29", "F30", "F4", "F5", "F9"}
	for _, id := range driftProne {
		n := 0
		for _, sec := range secs {
			if sec.Kind != "history" {
				continue
			}
			for _, r := range sec.Rows {
				if containsID(r.Text, id) {
					n++
					break
				}
			}
		}
		if n < 2 {
			vs = append(vs, regViolation{Section: "抽取器", Line: 0, ID: id,
				Reason: "★ 已知漂移面 " + id + " 在登记项小节里只认出 " + strconv.Itoa(n) + " 处（应 ≥ 2）⇒ 抽取器退化了"})
		}
	}

	// ── C4：规范源的读数必须自洽（★ 每行恰好一个状态词）──
	for _, c := range canon {
		if c.Status == "mixed" || c.Status == "" {
			vs = append(vs, regViolation{Section: "规范源", Line: c.Line, ID: c.Key,
				Reason: "★ 现状格必须**恰好**落在一个状态词里（已闭合 / 已关闭 / 已实施 / 已落地 / 有意如此 / 仍开）"})
		}
	}

	// ── C2：规范源覆盖 ──
	canonKeys := map[string]bool{}
	for _, c := range canon {
		canonKeys[c.Key] = true
	}
	for _, sec := range secs {
		if sec.Kind == "canonical" {
			continue
		}
		for _, r := range sec.Rows {
			for _, id := range extractRegIDs(r.Text) {
				if !canonKeys[id] {
					vs = append(vs, regViolation{Section: sec.Heading, Line: r.Line, ID: id,
						Reason: "★ 该登记项**不在规范源**里 ⇒ 「过期」没有唯一依据"})
				}
			}
		}
	}

	// ── C3：历史登记项小节的复述行 ──
	for _, sec := range secs {
		if sec.Kind != "history" {
			continue
		}
		for _, r := range sec.Rows {
			if !isAssertionRow(r.Text) {
				continue
			}
			var hits []canonEntry
			for _, c := range canon {
				if c.Key != "" && containsID(r.Text, c.Key) {
					hits = append(hits, c)
				}
			}
			if len(hits) == 0 {
				continue
			}
			// ★ 指针型副本（★ 「维持 §X 的结论」+ ★ **没抄结论**）⇒ 豁免：
			// ★ 它会自动跟随 §X ⇒ ⇒ 逼它带闭合标记 = 逼人维护 N 份同样的文字
			if regSectionPointerRe.MatchString(r.Text) && !containsAny(r.Text, regConclusionPhrases) {
				continue
			}
			hasClosed := false
			for _, h := range hits {
				if h.Status == "closed" {
					hasClosed = true
				}
			}
			if hasClosed && !containsAny(r.Text, regClosedMarkers) {
				vs = append(vs, regViolation{Section: sec.Heading, Line: r.Line, ID: hits[0].Key,
					Reason: "★ 这一行**断言了处置状态**，★ 而它提到的登记项在规范源里是【已闭合】⇒ ⇒ 该行必须带闭合标记（★ 否则读者会以为它还开着）"})
			}
			// ★ 反向：★ 只提到**单个**且仍开的登记项时，★ 不许带闭合标记
			if !hasClosed && len(hits) == 1 && hits[0].Status == "open" {
				if containsAny(r.Text, regClosedMarkers) {
					vs = append(vs, regViolation{Section: sec.Heading, Line: r.Line, ID: hits[0].Key,
						Reason: "★ 规范源说它【仍开】，★ 而这一行却带闭合标记 ⇒ 两边矛盾"})
				}
			}
		}
	}

	// ── C5：全景表不许停在旧状态 ──
	for _, sec := range secs {
		if sec.Kind != "panorama" {
			continue
		}
		for _, r := range sec.Rows {
			// ★ 只管**表格行**：★ 全景小节里还有散文，★ 散文不是登记项表
			if !r.IsTbl {
				continue
			}
			var hits []canonEntry
			for _, c := range canon {
				if c.Key != "" && containsID(r.Text, c.Key) {
					hits = append(hits, c)
				}
			}
			if len(hits) == 0 {
				continue
			}
			hasClosed := false
			onlyOpen := true
			for _, h := range hits {
				if h.Status == "closed" {
					hasClosed = true
					onlyOpen = false
				}
			}
			if hasClosed && !containsAny(r.Text, regClosedMarkers) {
				vs = append(vs, regViolation{Section: sec.Heading, Line: r.Line, ID: hits[0].Key,
					Reason: "★ 全景表里提到的登记项在规范源里是【已闭合】，★ 而这一行**没有闭合标记**⇒ ⇒ 规范全景表停在旧状态"})
			}
			if !hasClosed && onlyOpen && containsAny(r.Text, regClosedMarkers) {
				vs = append(vs, regViolation{Section: sec.Heading, Line: r.Line, ID: hits[0].Key,
					Reason: "★ 全景表里提到的登记项都不是已闭合，★ 而这一行却带闭合标记 ⇒ 两边矛盾"})
			}
		}
	}

	sort.Slice(vs, func(i, j int) bool {
		if vs[i].Section != vs[j].Section {
			return vs[i].Section < vs[j].Section
		}
		return vs[i].Line < vs[j].Line
	})
	return vs
}

// isAssertionRow 判断这一行是不是在**断言处置状态**。
//
// ★ 纯叙述（某轮当时做了什么）不算 ⇒ ⇒ 否则门会去管历史叙述，
// ★ 那等于逼人维护 N 份历史文字（★ LESSONS §606 的坑本身）。
func isAssertionRow(text string) bool {
	return containsAny(text, regAssertWords)
}

// containsID 判断文本里有没有提到某个登记项编号。
//
// ★ 非 F 项（§四.2 / §五.4.1 / 部署与 push）用**字面包含**。
func containsID(text, id string) bool {
	if regIDPattern.MatchString(id) {
		for _, got := range extractRegIDs(text) {
			if got == id {
				return true
			}
		}
		return false
	}
	return strings.Contains(text, id)
}

// ── 门 ──────────────────────────────────────────────────────────────────

func repoRootRegistrySync(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readAuditDocLines(t *testing.T) []string {
	t.Helper()
	p := filepath.Join(repoRootRegistrySync(t), regAuditDocRelPath)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读审计档失败：%v", err)
	}
	return strings.Split(string(b), "\n")
}

// TestAuditRegistryTableSync 是本门的主判据。
func TestAuditRegistryTableSync(t *testing.T) {
	lines := readAuditDocLines(t)
	vs := checkRegistry(lines)
	if len(vs) > 0 {
		t.Errorf("登记项副本漂移门：%d 条违例", len(vs))
		for _, v := range vs {
			t.Errorf("  - %s", v.String())
		}
	}
}

// TestAuditRegistryTableSyncHasTeeth 是本门的**牙齿检验**。
//
// ★ 三步（★ 顺序不能换）：
//
//	① **反向对照**：★ 真实文档 ⇒ 0 条违例（门不是恒红）
//	② **合成夹具**：★ 在内存副本上摘掉一行复述行的闭合标记
//	   ⇒ ★ 门**必须**报出违例、且必须点名那一项
//	③ ★★ **语义有效性**：★ 摘完之后那一行确实**不再**含闭合标记
//	   ⇒ ★★ 否则就是**弱变异**（★ 本轮实测踩过：只摘半个 `~~` ⇒ 语义没变 ⇒ 门还是绿的）
func TestAuditRegistryTableSyncHasTeeth(t *testing.T) {
	lines := readAuditDocLines(t)

	// ① 反向对照
	if vs := checkRegistry(lines); len(vs) != 0 {
		t.Fatalf("反向对照失败：真实文档本该 0 条违例，实际 %d 条（第一条：%s）", len(vs), vs[0].String())
	}

	canon := regCanonical(lines)
	secs := regSections(lines)

	// ② 合成夹具：★ 找一行「提到已闭合项 + 带闭合标记 + 是断言行」的行
	target := -1
	targetID := ""
	for _, sec := range secs {
		if sec.Kind != "history" {
			continue
		}
		for ri, r := range sec.Rows {
			if !isAssertionRow(r.Text) {
				continue
			}
			if !containsAny(r.Text, regClosedMarkers) {
				continue
			}
			for _, c := range canon {
				if c.Status == "closed" && containsID(r.Text, c.Key) {
					target = ri
					targetID = c.Key
					break
				}
			}
			if target >= 0 {
				break
			}
		}
		if target >= 0 {
			break
		}
	}
	if target < 0 {
		t.Fatalf("★ 找不到可注入的复述行 ⇒ 夹具无法构造（★ 这是**门自己坏了**，★ 不是文档的问题）")
	}

	mut := append([]string(nil), lines...)
	// ★ 注入：在**那一行**上摘掉**全部**闭合标记（★ 弱变异会让门测不出东西）
	secIdx := -1
	for si, sec := range secs {
		if sec.Kind == "history" && containsID(sec.Rows[target].Text, targetID) {
			secIdx = si
			break
		}
	}
	if secIdx < 0 {
		t.Fatalf("★ 定位注入目标失败")
	}
	rowLine := secs[secIdx].Rows[target].Line
	mut[rowLine-1] = stripAllClosedMarkers(mut[rowLine-1])

	// ③ 语义有效性：★ 注入后该行确实不再含闭合标记
	if containsAny(mut[rowLine-1], regClosedMarkers) {
		t.Fatalf("★★ **弱变异**：★ 注入后该行仍含闭合标记 ⇒ 语义没变 ⇒ 门测不出东西（★ 本轮踩过这个坑）")
	}
	// ★ 且原行确实**本来**含闭合标记（★ 证明注入真的做了事）
	if !containsAny(lines[rowLine-1], regClosedMarkers) {
		t.Fatalf("★ 注入目标行本来就不含闭合标记 ⇒ 夹具构造错了")
	}

	// ② 门必须抓到
	vs := checkRegistry(mut)
	if len(vs) == 0 {
		t.Fatalf("★★ **探测器坏了**：★ 合成夹具（摘掉 %s 的闭合标记）**没有**被门抓住", targetID)
	}
	found := false
	for _, v := range vs {
		if v.ID == targetID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("★★ 门报出了违例，但**没有点名**注入的登记项 %s（★ 报的是：%v）", targetID, vs)
	}
}

// stripAllClosedMarkers 把一行里的闭合标记全部摘掉（★ 用于合成夹具）。
func stripAllClosedMarkers(line string) string {
	out := line
	for _, m := range regClosedMarkers {
		out = strings.ReplaceAll(out, m, "")
	}
	return out
}
