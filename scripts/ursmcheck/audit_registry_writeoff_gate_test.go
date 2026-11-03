package ursm_test

// ★★★ 第四十三轮新增：★ 「★ 登记项销账门」
//
// ★ 背景（一句话）：★ 以前「★ 把一件事登记给下一轮」是**纯自然语言**的
//
//	⇒ ⇒ 它**没有编号** ⇒ ⇒ 它**不在任何门的射程里**（★ 实测：登记门 `regSections`
//	  只认 3 层级标题、★ 而这些登记写在 **4 层级**小节里 ⇒ ★ 轮次记录区**机制上**
//	  不受任何门管）
//	⇒ ⇒ 所以它会**静默过期**（★ 而「过期登记比没有登记更坏」：★ 它让读者以为
//	  有人在管这件事）
//
// ★ 本门做的事（一句话）：★ 把「★ 登记 ⇒ 必须有编号 ⇒ 编号必须在规范源里 ⇒
// ★ 状态必须两边一致」这条纪律**变成机器可判**。
//
// ★★★ 口径（★ 明写，避免「以为管住了」）：
//
//	W0（防假绿）：★ 抽取器自己必须认出登记点 / 销账标记 / 规范源↔正文绑定
//	W1（登记必带号）：★ 每个「★ 把一件事登记给下一轮」的小节 ⇒ 必须有一条销账标记
//	W2（号必在规范源）：★ 销账标记上的编号 ⇒ 必须在 §28.23 规范源里
//	W3（状态必一致）：★ 销账标记的状态词 ⇒ 必须与规范源那一行一致
//	W4（销账必有登记）：★ 销账标记所在的小节 ⇒ 必须真的有登记点（★ 防「空销账」）
//	W5（双向绑定）：★ 规范源行的依据列若指向一个含登记点的小节
//	                ⇒ 那一小节必须有一条带该编号的销账标记
//
// ★ 「同源」：★ W2/W3/W5 复用既有登记门的 `extractRegIDs` / `classifyStatus` /
// ★ `regCanonical` / `regSecRefs` ⇒ ★ **与 C2/C7/C8 同一套抽取器**
// ⇒ ⇒ 好处：★ 两套判据互证、★ 不必维护第二份「编号是什么形状」的知识。

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ── 口径常量 ───────────────────────────────────────────────────────────

// woDeferPhrases 是「★ 把一件事推迟到下一轮」的措辞表。
//
// ★ 这张表是**量出来的**、不是想出来的（★ 第四十三轮量具跑全档）：
//
//	★ 全档命中 **11 处** ⇒ ★ 其中 **2 处**是引号内的【元引用】
//	  （★ 例：「★ 全档还有 7 组「★ 登记给下一轮」的条目」⇒ ★ 那是在**谈论**这个词）
//	⇒ ⇒ 真正的登记点 = **9 处** ⇒ ⇒ 落在 **8 个小节**里（★ 最小小节口径）
//
// ★★★ 词表越宽、门越容易恒绿；★ 词表越窄、门越容易漏报
// ⇒ ⇒ 所以这张表**逐条写明它量到了什么**，★ 且 W0 会检查它没有退化成空表。
var woDeferPhrases = []string{
	"登记给下一轮", // ★ §28.18.8 / §28.19.8 / §28.27.7 / §28.29 / §28.30.4 / §28.31 / §28.32
	"登记到下一轮",
	"给下一轮登记", // ★ §28.32.8
	"下一轮登记",
	"下一轮该做", // ★ §28.18.8
	"留给下一轮", // ★ §7.6（★ 它是「登记项小节」⇒ 不在本门射程内）
	"下一轮补",  // ★ §28.30.11（★ 是 §28.30.4 那条登记的**复述**）
}

// woMarkerTag 是「销账标记」的识别词（★ 正文里显式写出来的那个词）。
const woMarkerTag = "销账标记"

// ── 量具：代码块感知的小节切分 ─────────────────────────────────────────

// woHeadRe 匹配**任意**层级的小节标题。
var woHeadRe = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)

// woSecNumRe 匹配标题**开头**的节号（★ 例：§28.18.8 ⇒ "28.18.8"）。
var woSecNumRe = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)*)`)

// woSec 是审计档的一个小节。
//
// ★★ 口径：**最近的任意层级标题**（★ 不是「3 层级祖先」）。
//
// ★★ 为什么（★ 本轮实测）：★ 本档的标题层级**本身就不一致** ——
//
//	★ §28.18–§28.22 是 **4 层级**（★ `#### 28.18`）
//	★ 而 §28.23 及以后是 **3 层级**（★ `### 28.23`）
//
// ⇒ ⇒ 按「3 层级祖先」分组会把**不同轮次串进同一组** ⇒ ⇒ 只能用最近标题。
type woSec struct {
	Heading string
	Line    int    // 标题行号（1-based）
	Level   int    // 井号个数
	SecNum  string // 标题开头的节号（★ 没有则空）
	From    int    // 正文起始下标（0-based，含）
	To      int    // 正文结束下标（0-based，不含）
}

// woSplitSections 把文档切成小节。
//
// ★★★ **代码块感知**是这一版的重点（★ 第四十三轮实测）：
//
//	★ 本档正文里**嵌着 shell 片段**（★ 例：§28.17 里的 `deploy/phase0/verify.sh`）
//	⇒ ⇒ 那些片段里有 `# ✔ 实测合格…` 这样的**单井号注释**（★ 实测 3 处）
//	⇒ ⇒ 而 `^#{1,6}\s` 会把它们**当成小节标题**
//	⇒ ⇒ ⇒ **既有登记门的小节边界会被这些代码注释提前截断**
//	★ ⇒ 本门**必须**跳过围栏内的行。
func woSplitSections(lines []string) []woSec {
	var secs []woSec
	inCode := false
	var cur *woSec
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			inCode = !inCode
			continue
		}
		if inCode {
			continue
		}
		m := woHeadRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		if cur != nil {
			cur.To = i
			secs = append(secs, *cur)
		}
		cur = &woSec{
			Heading: m[2],
			Line:    i + 1,
			Level:   len(m[1]),
			SecNum:  woSecNum(m[2]),
			// ★★★ **标题行本身也算这一节的正文**（★ 本轮实测踩到）：
			//   ★ §28.18.8 与 §28.19.8 的登记短语写在**标题里**
			//   （★ 「#### 28.19.8 ★★ 登记给下一轮（本轮不做、但已查清的三件事）」）
			//   ★ ⇒ ★ 只扫标题**之后** ⇒ ★ 那两节会被漏掉 ⇒ ★ 门恒绿
			From: i,
		}
	}
	if cur != nil {
		cur.To = len(lines)
		secs = append(secs, *cur)
	}
	return secs
}

// woCodeFlags 标出哪些行在代码围栏内。
func woCodeFlags(lines []string) []bool {
	out := make([]bool, len(lines))
	in := false
	for i, l := range lines {
		out[i] = in
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			in = !in
		}
	}
	return out
}

// woSecNum 取出标题开头的节号。
func woSecNum(heading string) string {
	m := woSecNumRe.FindStringSubmatch(heading)
	if m == nil {
		return ""
	}
	return m[1]
}

// ── 判定：登记点 / 销账标记 ────────────────────────────────────────────

// woIsMarkerLine 判断这一行是不是一条**销账标记**。
//
// ★★★ 口径：★ **有「销账标记」这个词 ＋ 有编号** ⇒ ★ 才算标记。
//
//	★ 为什么必须带编号（★ 本轮实测踩到的**自指坑**）：
//	  ★ 本门自己的文档（§28.33）里**反复出现「销账标记」这个词**
//	  ⇒ ⇒ 若「有这个词就是标记」⇒ ⇒ **门会把自己的说明文字判成违例**
//	★ ⇒ ⇒ 而「★ 写了标记却忘了写编号」**不会被漏掉**：★ 那一节就没有有效标记
//	  ⇒ ⇒ **W1 照样红**
func woIsMarkerLine(text string) bool {
	if !strings.Contains(text, woMarkerTag) || len(extractRegIDs(text)) == 0 {
		return false
	}
	// ★★★ 第三要素（★ 第四十四轮补的）：★ **必须带一个状态词**。
	//
	// ★ 为什么（★ 本轮实测踩到的**第二类自指坑**）：★ §28.34.5 里有一句**散文**
	//   「★ 本轮把 `F49` 的**登记语句**写进 §28.34 ⇒ ⇒ **销账门当场要求同一节里有销账标记**」
	//   ⇒ ⇒ 它**同时**含「销账标记」这个词**和**一个编号 `F49`
	//   ⇒ ⇒ ⇒ **被误判成了一条销账标记** ⇒ ⇒ 门报「★ 它所在的小节一个登记点都没有」
	//   ⇒ ⇒ ⇒ 而它**根本不是标记**、★ 只是一句在**谈论**标记的话
	//   ⇒ ⇒ ★★ **⇒ 口径收紧成三要素**：★ 「★ 有这个词 ＋ 有编号 ＋ 有状态词」
	//   ⇒ ⇒ ★★ ⇒ 而**真的**销账标记**三者齐备**（★ 11 条实测全部齐备）⇒ ⇒ 判据面不受影响
	st := classifyStatus(text)
	return st == "closed" || st == "intentional" || st == "open"
}

// woQuoted 判断 text 中第 i 个字符是否落在「」之内。
//
// ★ 元引用豁免：★ 「★ 全档还有 7 组「★ 登记给下一轮」的条目」这一行
// 是在**谈论**这个词、不是在**登记**一件事 ⇒ ⇒ 不算登记点。
func woQuoted(text string, i int) bool {
	lo := strings.LastIndex(text[:i], "「")
	hi := strings.LastIndex(text[:i], "」")
	return lo > hi
}

// woIsDeferLine 判断这一行是不是「★ 把一件事推迟到下一轮」的登记点。
func woIsDeferLine(text string) bool {
	for _, p := range woDeferPhrases {
		if i := strings.Index(text, p); i >= 0 && !woQuoted(text, i) {
			return true
		}
	}
	return false
}

// woInScope 判断这个小节是否在本门的射程内。
//
// ★ 排除两类：
//
//	① **规范源小节**（§28.23）本身 = 【编号表本体】⇒ 它就是登记册 ⇒ 不重复管
//	② **「登记项小节」**（3 层级 ＋ 标题含「登记项」）⇒ 它们受既有门
//	   C2/C3/C5 管（★ 表格行、编号、状态标记都在它们的射程里）⇒ 不重复管
//
// ★★ 本门专治的是**轮次记录区**：★ 那些「4 层级小节里的自然语言登记」。
func woInScope(s *woSec) bool {
	if strings.Contains(s.Heading, "规范源") && strings.Contains(s.Heading, "现状总表") {
		return false
	}
	if s.Level == 3 && strings.Contains(s.Heading, "登记项") {
		return false
	}
	return true
}

// ── 数据结构 ───────────────────────────────────────────────────────────

// woSecInfo 是一个小节上的量具读数。
type woSecInfo struct {
	sec     *woSec
	inScope bool
	defers  []int // 登记点行号（1-based）
	markers []int // 销账标记行号（1-based）
}

// woViolation 是门报出的一条违例。
type woViolation struct {
	Section string
	Line    int
	ID      string
	Reason  string
}

func (v woViolation) String() string {
	return v.Section + ":" + strconv.Itoa(v.Line) + " [" + v.ID + "] " + v.Reason
}

// ── 判据实现 ───────────────────────────────────────────────────────────

// checkWriteoff 是本门的全部判据（★ 纯函数：★ 只吃行数组 ⇒ 合成夹具可在内存副本上跑）。
func checkWriteoff(lines []string) []woViolation {
	var vs []woViolation

	canon := regCanonical(lines)
	canonByKey := map[string]canonEntry{}
	for _, c := range canon {
		canonByKey[c.Key] = c
	}

	// ── W0（防假绿）：★ 规范源必须抽得到东西 ─────────────────────────
	if len(canon) == 0 {
		return []woViolation{{Section: "规范源", Line: 0, ID: "-",
			Reason: "★ 规范源（§28.23）不存在或没抽到条目 ⇒ 门恒绿 = 形同虚设"}}
	}

	secs := woSplitSections(lines)
	code := woCodeFlags(lines)

	infos := make([]woSecInfo, 0, len(secs))
	for i := range secs {
		s := &secs[i]
		info := woSecInfo{sec: s, inScope: woInScope(s)}
		for j := s.From; j < s.To && j < len(lines); j++ {
			if code[j] {
				continue
			}
			t := lines[j]
			if woIsDeferLine(t) {
				info.defers = append(info.defers, j+1)
			}
			// ★★★ **「销账标记」必须带编号才算标记**（★ 本轮实测踩到的自指坑）：
			//
			//	★ 本门自己的文档（§28.33）里**反复出现「销账标记」这个词**
			//	  （★ 例：「★ 正文对应小节**补销账标记**」）⇒ ⇒ 若「有这个词就是标记」
			//	  ⇒ ⇒ **门会把自己的说明文字判成违例**
			//	★ ⇒ ⇒ 所以口径是：★ **有这个词 ＋ 有编号** ⇒ ★ 才是销账标记
			//	★ ⇒ ⇒ ★★ 而「写了标记却忘了写编号」这种情况**不会被漏掉**：
			//	  ★ 那一节就会**没有有效标记** ⇒ ⇒ **W1 照样红**
			if woIsMarkerLine(t) {
				info.markers = append(info.markers, j+1)
			}
		}
		infos = append(infos, info)
	}

	// 节号 → 小节下标（★ 规范源的依据列要靠它指回正文）
	bySecNum := map[string]int{}
	for i := range infos {
		if infos[i].sec.SecNum != "" {
			if _, dup := bySecNum[infos[i].sec.SecNum]; !dup {
				bySecNum[infos[i].sec.SecNum] = i
			}
		}
	}

	// ── W1：★ 登记点所在的小节必须有一条销账标记 ──────────────────────
	for i := range infos {
		info := &infos[i]
		if !info.inScope || len(info.defers) == 0 {
			continue
		}
		if len(info.markers) == 0 {
			vs = append(vs, woViolation{
				Section: "§" + info.sec.SecNum,
				Line:    info.defers[0],
				ID:      "-",
				Reason: "★ 这一节把 " + strconv.Itoa(len(info.defers)) +
					" 件事**登记给了下一轮**，★ 而它**一条销账标记都没有** ⇒ ★ 「登记」没有编号 ⇒ ★ 不在 §28.23 规范源里 ⇒ ★ 不受任何门管 ⇒ ★ 它会静默过期（★ 过期登记比没有登记更坏）",
			})
		}
	}

	// ── W2 / W3 / W4：★ 逐条检查销账标记行 ───────────────────────────
	for i := range infos {
		info := &infos[i]
		if !info.inScope {
			continue
		}
		for _, ln := range info.markers {
			text := lines[ln-1]
			// ★ 说明：★ 这里**不需要**再判「有没有编号」⇒ ★ 因为
			//   woIsMarkerLine 已保证标记行**至少带一个编号**（★ 见该函数的注释）
			ids := extractRegIDs(text)
			for _, id := range ids {
				c, ok := canonByKey[id]
				if !ok {
					vs = append(vs, woViolation{
						Section: "§" + info.sec.SecNum, Line: ln, ID: id,
						Reason: "★ 销账标记引用了 " + id + "，★ 而它**不在 §28.23 规范源里** ⇒ ★ 这个编号是凭空写的 ⇒ ★ 「过期」没有唯一依据",
					})
					continue
				}
				st := classifyStatus(text)
				switch st {
				case "closed", "intentional", "open":
				default:
					vs = append(vs, woViolation{
						Section: "§" + info.sec.SecNum, Line: ln, ID: id,
						Reason: "★ 销账标记的状态词必须**恰好**落在一个类里（已闭合 / 有意如此 / 仍开），★ 实得 " + strconv.Quote(st),
					})
					continue
				}
				if st != c.Status {
					vs = append(vs, woViolation{
						Section: "§" + info.sec.SecNum, Line: ln, ID: id,
						Reason: "★ 正文销账标记说它是「" + st + "」，★ 而 §28.23 规范源说它是「" + c.Status + "」⇒ ★ 两处矛盾（★ 状态漂移）",
					})
				}
			}
			if len(info.defers) == 0 {
				vs = append(vs, woViolation{
					Section: "§" + info.sec.SecNum, Line: ln, ID: "-",
					Reason: "★ 这一行自称「销账标记」，★ 但它所在的小节**一个登记点都没有** ⇒ ★ 它在销账一个不存在的东西",
				})
			}
		}
	}

	// ── W5：★ 规范源 ↔ 正文 的双向绑定 ──────────────────────────────
	nBind := 0
	for _, c := range canon {
		if c.Key == "" {
			continue
		}
		for _, ref := range regSecRefs(c.Evidence) {
			idx, ok := bySecNum[ref]
			if !ok {
				continue
			}
			info := &infos[idx]
			if !info.inScope || len(info.defers) == 0 {
				continue
			}
			nBind++
			found := false
			for _, ln := range info.markers {
				if containsID(lines[ln-1], c.Key) {
					found = true
					break
				}
			}
			if !found {
				vs = append(vs, woViolation{
					Section: "规范源", Line: c.Line, ID: c.Key,
					Reason: "★ 规范源这一行指回 §" + ref + "（★ 那一节确实把一件事登记给了下一轮），★ 而**那一节里没有**带 " + c.Key + " 的销账标记 ⇒ ★ 编号与正文**对不上**",
				})
			}
		}
	}

	// ── W6：★★ 规范源必须指回一个【带它销账标记】的小节 ─────────────────
	//
	// ★★ 为什么需要它（★ 本轮牙齿检验 M5 **当场逼出来的**）：
	//
	//	★ W5 只管「★ 引的那一节有登记点、★ 却没带这个号」
	//	★ ⇒ ★★ 但如果规范源把依据列**改到别处**（★ 或者干脆不引了）
	//	  ⇒ ⇒ W5 的判据面就**悄悄空了** ⇒ ★★ 绑定**静默**断掉 ⇒ ★ 没人喊
	//	★ ⇒ ⇒ 所以补一条**反向**判据：★ 「有销账标记的规范源行 ⇒ 必须指回那个小节」
	//
	// ★ 口径：★ 只对「正文里已经有它的销账标记」的规范源行生效
	//   （★ 既有 F1–F39 没有销账标记、★ 它们由 C2/C3/C5 管 ⇒ 不在本门射程内）
	for _, c := range canon {
		if c.Key == "" {
			continue
		}
		hasMarker := false
		for i := range infos {
			for _, ln := range infos[i].markers {
				if containsID(lines[ln-1], c.Key) {
					hasMarker = true
					break
				}
			}
			if hasMarker {
				break
			}
		}
		if !hasMarker {
			continue
		}
		ok := false
		for _, ref := range regSecRefs(c.Evidence) {
			idx, found := bySecNum[ref]
			if !found {
				continue
			}
			for _, ln := range infos[idx].markers {
				if containsID(lines[ln-1], c.Key) {
					ok = true
					break
				}
			}
			if ok {
				break
			}
		}
		if !ok {
			vs = append(vs, woViolation{
				Section: "规范源", Line: c.Line, ID: c.Key,
				Reason: "★ 规范源这一行指回 " + regSecRefList(regSecRefs(c.Evidence)) +
					"，★ 而**那些小节里没有**带 " + c.Key + " 的销账标记 ⇒ ★ 编号与正文**对不上**",
			})
		}
	}

	// ── W0（防假绿）：★ 结构性非空 ───────────────────────────────────
	nDefer, nMarker := 0, 0
	for i := range infos {
		if !infos[i].inScope {
			continue
		}
		nDefer += len(infos[i].defers)
		nMarker += len(infos[i].markers)
	}
	if nDefer == 0 {
		vs = append(vs, woViolation{Section: "量具", Line: 0, ID: "-",
			Reason: "★ 全档一个登记点都没认出来 ⇒ ★ 措辞词表退化了 ⇒ 门恒绿 = 形同虚设"})
	}
	if nMarker == 0 {
		vs = append(vs, woViolation{Section: "量具", Line: 0, ID: "-",
			Reason: "★ 全档一条销账标记都没认出来 ⇒ ★ 标记写法变了 ⇒ 门恒绿 = 形同虚设"})
	}
	if nBind == 0 {
		vs = append(vs, woViolation{Section: "量具", Line: 0, ID: "-",
			Reason: "★ 规范源与正文**一条都没绑上** ⇒ ★ W5 的判据面空了 ⇒ 门恒绿 = 形同虚设"})
	}

	sort.Slice(vs, func(i, j int) bool {
		if vs[i].Section != vs[j].Section {
			return vs[i].Section < vs[j].Section
		}
		return vs[i].Line < vs[j].Line
	})
	return vs
}

// ── 测试 ───────────────────────────────────────────────────────────────

// TestAuditRegistryWriteoffGate 是本门的主判据。
func TestAuditRegistryWriteoffGate(t *testing.T) {
	lines := readAuditDocLines(t)
	vs := checkWriteoff(lines)
	if len(vs) > 0 {
		t.Errorf("登记项销账门：%d 条违例", len(vs))
		for _, v := range vs {
			t.Errorf("  - %s", v.String())
		}
		return
	}

	// ★★★ 判据面基线（★ 本轮实测值）：★★
	//   ★ 这些数字是「★ 本门到底管着多大一片」的**可见读数**
	//   ★ ⇒ 词表或标记写法一旦变了、★ 它们会立刻动 ⇒ ★ 不会「悄悄少管一片」
	//
	// ★★★ 第四十四轮订正：★ 下限从 **48** 提到 **54** ⇒ ★★ 原因有二：
	//   ★ ① ★ **48 那个数本身是错的**：★ 第四十三轮写的「48」是【F-数字族】口径
	//     ⇒ ★★ 而本门（★ 与 `regCanonical` / ★ `xdCanon` 同口径）数的是【全部 F-编号项】
	//     ⇒ ★★ **两个口径恒差 5** ⇒ ⇒ ★★ 真实值第四十三轮末是 **53**、★ 现在是 **54**
	//   ★ ② ★ ⇒ ★★ 这个下限因此变成**棘轮**：★ 删一行规范源 ⇒ ★ 门红 ⇒ ★
	//     ★ 要改它必须**同时**在本档（★ §28.34）写清理由
	canon := regCanonical(lines)
	if len(canon) < 54 {
		t.Errorf("规范源条目数 %d < 54（★ 第四十四轮实测值）⇒ 抽取器退化了、★ 或规范源被删了行（★ 棘轮：★ 改它必须写清理由）", len(canon))
	}
	secs := woSplitSections(lines)
	code := woCodeFlags(lines)
	nDeferSec, nMarker := 0, 0
	for i := range secs {
		s := &secs[i]
		if !woInScope(s) {
			continue
		}
		gotDefer := false
		for j := s.From; j < s.To && j < len(lines); j++ {
			if code[j] {
				continue
			}
			if woIsDeferLine(lines[j]) {
				gotDefer = true
			}
			if woIsMarkerLine(lines[j]) {
				nMarker++
			}
		}
		if gotDefer {
			nDeferSec++
		}
	}
	if nDeferSec < 9 {
		t.Errorf("含登记点的小节数 %d < 9（★ 第四十三轮实测值）⇒ 措辞词表少管了一片", nDeferSec)
	}
	if nMarker < 11 {
		t.Errorf("销账标记数 %d < 11（★ 第四十三轮实测值）⇒ 标记写法变了、少管了一片", nMarker)
	}
}

// TestAuditRegistryWriteoffGateHasTeeth 是本门的**牙齿检验**。
//
// ★ 三步（★ 顺序不能换）：
//
//	① **反向对照**：★ 真实文档 ⇒ 0 条违例（门不是恒红）
//	② **逐判据变异**：★ 在**内存副本**上分别破坏 W1/W2/W3/W4/W5
//	   ⇒ ★ 门**必须**逐条报出违例、★ 且必须点名那一项
//	③ ★★ **语义有效性**：★ 变异确实改掉了那一行的语义（★ 否则就是弱变异）
//
// ★★ 为什么用**内存副本**而不是改文件：★ 内存副本**不可能**留下脏树
//
//	⇒ ⇒ 而「改文件再还原」那种玩法**万一还原不干净**就会污染落点
//	⇒ ⇒ （★ 既有门 `TestAuditRegistryTableSyncHasTeeth` 也是这个路子）
func TestAuditRegistryWriteoffGateHasTeeth(t *testing.T) {
	lines := readAuditDocLines(t)

	// ① 反向对照
	if vs := checkWriteoff(lines); len(vs) != 0 {
		t.Fatalf("反向对照失败：真实文档本该 0 条违例，实际 %d 条（第一条：%s）", len(vs), vs[0].String())
	}

	canon := regCanonical(lines)
	canonByKey := map[string]canonEntry{}
	for _, c := range canon {
		canonByKey[c.Key] = c
	}

	// 找一条真实的销账标记行（★ 优先挑「那一节只有它一条标记」的：
	// ★ 这样 M1（删掉它 ⇒ 那一节就没有标记了）才是**有效**变异）
	type target struct {
		line int    // 1-based
		id   string // 规范源里的编号
		sec  string // 所在小节节号
		sole bool   // ★ 该小节里是不是只有这一条标记
	}
	var picks []target
	secs := woSplitSections(lines)
	code := woCodeFlags(lines)
	for i := range secs {
		s := &secs[i]
		if !woInScope(s) {
			continue
		}
		var ms []int
		hasDefer := false
		for j := s.From; j < s.To && j < len(lines); j++ {
			if code[j] {
				continue
			}
			if woIsDeferLine(lines[j]) {
				hasDefer = true
			}
			if woIsMarkerLine(lines[j]) {
				ms = append(ms, j+1)
			}
		}
		if !hasDefer {
			continue
		}
		for _, ln := range ms {
			ids := extractRegIDs(lines[ln-1])
			if len(ids) == 0 {
				continue
			}
			c, ok := canonByKey[ids[0]]
			if !ok {
				continue
			}
			picks = append(picks, target{line: ln, id: c.Key, sec: s.SecNum, sole: len(ms) == 1})
		}
	}
	if len(picks) == 0 {
		t.Fatal("反向对照之后的取样失败：真实文档里一条销账标记都没找到 ⇒ 本门恒绿")
	}
	// ★ 优先挑「独占」的（★ 那一节只有它一条标记）＋「依据列引的就是它那一节」的
	pick := picks[0]
	for _, p := range picks {
		if p.sole && woEvidenceCites(canon, p.id, p.sec) {
			pick = p
			break
		}
	}
	for _, p := range picks {
		if p.sole {
			pick = p
			break
		}
	}

	// ── 变异臂 M1（W1）：★ 删掉一条销账标记 ⇒ 那一节就不再有销账标记 ──
	{
		p := pick
		if !p.sole {
			t.Skip("M1 跳过：真实文档里找不到「那一节只有它一条销账标记」的样本")
		}
		cp := append([]string(nil), lines...)
		cp[p.line-1] = ""
		vs := checkWriteoff(cp)
		if !woHasViolation(vs, "销账标记都没有") {
			t.Errorf("M1（W1：删掉销账标记）没有报出违例 ⇒ 门没牙（违例 %d 条）", len(vs))
		}
		// ★ 语义有效性：★ 那一行确实不再带标记
		if strings.Contains(cp[p.line-1], woMarkerTag) {
			t.Errorf("M1 变异无效：那一行仍带「%s」", woMarkerTag)
		}
	}

	// ── 变异臂 M2（W2）：★ 把编号换成一个不在规范源里的号 ────────────
	{
		p := pick
		cp := append([]string(nil), lines...)
		cp[p.line-1] = strings.Replace(cp[p.line-1], p.id, "F9999", 1)
		vs := checkWriteoff(cp)
		if !woHasViolation(vs, "不在 §28.23 规范源里") {
			t.Errorf("M2（W2：编号不在规范源）没有报出违例 ⇒ 门没牙（违例 %d 条）", len(vs))
		}
		if !strings.Contains(cp[p.line-1], "F9999") {
			t.Errorf("M2 变异无效：那一行没有换成 F9999")
		}
	}

	// ── 变异臂 M3（W3）：★ 把状态词改成与规范源**相反**的那个 ─────────
	{
		p := pick
		c := canonByKey[p.id]
		// 找一个与规范源**不同**的状态词来写
		other := "⏳ 仍开"
		switch c.Status {
		case "open":
			other = "✅ 已闭合"
		case "closed":
			other = "⏳ 仍开"
		case "intentional":
			other = "✅ 已闭合"
		}
		cp := append([]string(nil), lines...)
		cp[p.line-1] = woReplaceStatus(cp[p.line-1], other)
		vs := checkWriteoff(cp)
		if !woHasViolation(vs, "两处矛盾") {
			t.Errorf("M3（W3：正文与规范源状态矛盾）没有报出违例 ⇒ 门没牙（违例 %d 条）", len(vs))
		}
		if classifyStatus(cp[p.line-1]) == c.Status {
			t.Errorf("M3 变异无效：变异后状态类仍是 %s", c.Status)
		}
	}

	// ── 变异臂 M4（W4）：★ 把销账标记挪到一个没有登记点的小节 ────────
	{
		p := pick
		// 找一个「没有登记点」的小节（★ 标题行也算、★ 所以要用同一个扫描区间）
		target := -1
		for i := range secs {
			s := &secs[i]
			if !woInScope(s) || s.SecNum == p.sec {
				continue
			}
			hasDefer := false
			for j := s.From; j < s.To && j < len(lines); j++ {
				if code[j] {
					continue
				}
				if woIsDeferLine(lines[j]) {
					hasDefer = true
				}
			}
			if !hasDefer {
				target = s.Line
				break
			}
		}
		if target < 0 {
			t.Skip("M4 跳过：真实文档里找不到「没有登记点」的小节")
		}
		marker := lines[p.line-1]
		cp := append([]string(nil), lines...)
		cp[p.line-1] = ""
		cp = woInsertAfter(cp, target-1, marker)
		vs := checkWriteoff(cp)
		if !woHasViolation(vs, "一个登记点都没有") {
			t.Errorf("M4（W4：销账没有对象）没有报出违例 ⇒ 门没牙（违例 %d 条）", len(vs))
		}
	}

	// ── 变异臂 M5（W5）：★ 规范源那一行的依据列指向别处 ⇒ 绑定断掉 ──
	{
		p := pick
		var cLine int
		for _, c := range canon {
			if c.Key == p.id {
				cLine = c.Line
			}
		}
		if cLine == 0 {
			t.Fatalf("M5 取样失败：规范源里没有 %s", p.id)
		}
		cp := append([]string(nil), lines...)
		cp[cLine-1] = strings.Replace(cp[cLine-1], "§"+p.sec, "§99.99", 1)
		if cp[cLine-1] == lines[cLine-1] {
			t.Skip("M5 跳过：那一行的依据列里没有 §" + p.sec)
		}
		vs := checkWriteoff(cp)
		if !woHasViolation(vs, "编号与正文**对不上**") {
			t.Errorf("M5（W5：规范源↔正文绑定断掉）没有报出违例 ⇒ 门没牙（违例 %d 条）", len(vs))
		}
	}
}

// woEvidenceCites 判断规范源那一行的依据列有没有引用指定的小节。
func woEvidenceCites(canon []canonEntry, id, sec string) bool {
	for _, c := range canon {
		if c.Key != id {
			continue
		}
		for _, r := range regSecRefs(c.Evidence) {
			if r == sec {
				return true
			}
		}
	}
	return false
}

// woInsertAfter 在下标 idx（0-based）**之后**插入一行（★ 返回新切片）。
func woInsertAfter(lines []string, idx int, text string) []string {
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:idx+1]...)
	out = append(out, text)
	out = append(out, lines[idx+1:]...)
	return out
}

// woHasViolation 判断违例里有没有一条含指定措辞的。
func woHasViolation(vs []woViolation, needle string) bool {
	for _, v := range vs {
		if strings.Contains(v.Reason, needle) {
			return true
		}
	}
	return false
}

// woReplaceStatus 把一行里的**所有**状态词抹掉、再写上指定的那个（★ 用于合成矛盾）。
//
// ★ 为什么是「抹掉全部」而不是「替换第一个」：★ 有的销账标记行里
// 「已闭合」与「已闭合并」**同时**出现（★ 例：§28.31 的 F47 那一行）
// ⇒ ⇒ 只替换第一个会留下第二个 ⇒ ⇒ `classifyStatus` 落进 `mixed`
// ⇒ ⇒ ⇒ 那个变异就**测不到 W3 的矛盾分支**（★ 弱变异）。
func woReplaceStatus(line, want string) string {
	out := line
	all := append([]string{}, regClosedStatus...)
	all = append(all, regIntentionalStatus...)
	all = append(all, regOpenStatus...)
	for _, w := range all {
		out = strings.ReplaceAll(out, w, "")
	}
	return out + " ⇒ " + want
}

// TestAuditRegistryWriteoffGateFixture 在一份**最小合成文档**上跑本门。
//
// ★ 为什么要有它：★ 牙齿检验跑的是**真实文档**（★ 变异真实行）
// ⇒ ⇒ 它证明「门能抓住破坏」，★ 但**不能**证明「门在最小输入上也是对的」
// ⇒ ⇒ 这份夹具给出的是「★ 一份只有 1 个登记项的文档上、★ 门应当怎么判」。
func TestAuditRegistryWriteoffGateFixture(t *testing.T) {
	doc := woFixtureDoc()

	if vs := checkWriteoff(doc); len(vs) != 0 {
		t.Fatalf("合成夹具的基准应当 0 条违例，实际 %d 条（第一条：%s）", len(vs), vs[0].String())
	}

	// ★ W1：★ 删掉销账标记 ⇒ 必须红
	cp := append([]string(nil), doc...)
	cp[11] = ""
	if vs := checkWriteoff(cp); !woHasViolation(vs, "销账标记都没有") {
		t.Errorf("夹具 W1：删掉销账标记后门没红（违例 %d 条）", len(vs))
	}

	// ★ W2：★ 编号不在规范源 ⇒ 必须红
	cp = append([]string(nil), doc...)
	cp[11] = strings.Replace(cp[11], "F41", "F42", 1)
	if vs := checkWriteoff(cp); !woHasViolation(vs, "不在 §28.23 规范源里") {
		t.Errorf("夹具 W2：编号不在规范源时门没红（违例 %d 条）", len(vs))
	}

	// ★ W3：★ 状态与规范源矛盾 ⇒ 必须红
	cp = append([]string(nil), doc...)
	cp[11] = woReplaceStatus(cp[11], "⏳ 仍开")
	if vs := checkWriteoff(cp); !woHasViolation(vs, "两处矛盾") {
		t.Errorf("夹具 W3：状态矛盾时门没红（违例 %d 条）", len(vs))
	}

	// ★ W4：★ 销账标记所在的小节没有登记点 ⇒ 必须红
	cp = append([]string(nil), doc...)
	cp[9] = "★ 这一行只是叙述"
	if vs := checkWriteoff(cp); !woHasViolation(vs, "一个登记点都没有") {
		t.Errorf("夹具 W4：销账没有对象时门没红（违例 %d 条）", len(vs))
	}

	// ★ W5：★ 规范源引了 §28.18.8、★ 而那里的标记**没带** F41 ⇒ 绑定断掉 ⇒ 必须红
	cp = append([]string(nil), doc...)
	cp[11] = strings.Replace(cp[11], "、★ **F41**", "", 1)
	if cp[11] == doc[11] {
		t.Fatal("夹具 W5 取样失败：没能从标记里摘掉 F41")
	}
	if vs := checkWriteoff(cp); !woHasViolation(vs, "编号与正文**对不上**") {
		t.Errorf("夹具 W5：绑定断掉时门没红（违例 %d 条）", len(vs))
	}
}

// woFixtureDoc 是一份**最小**的合成文档
// （★ 2 个登记项 ＋ 1 个登记点 ＋ 1 条同时销掉两个号的销账标记）。
func woFixtureDoc() []string {
	return []string{
		"### 28.23 登记项现状总表（★ **规范源**）", // 0
		"",                 // 1
		"| 编号 | 现状 | 依据 |", // 2
		"|---|---|---|",    // 3
		"| **F40** | ✅ 已闭合 | ★ §28.18.8 |", // 4
		"| **F41** | ✅ 已闭合 | ★ §28.18.8 |", // 5
		"",                     // 6
		"#### 28.18.8 ★★★ 某件事", // 7
		"",                     // 8
		"★ 登记给下一轮（★ 本轮不做）", // 9
		"", // 10
		"> ★★★ **销账标记**：★ ★ **F40**、★ **F41** ⇒ ★★ **✅ 已闭合** ⇒ ★ 依据：★ §28.18.8", // 11
		"", // 12
	}
}

// TestAuditRegistryWriteoffGateCodeBlockAwareness 证明「代码块里的 `#` 注释
// 会被误当成小节标题」这个陷阱是**真的**，★ 而本门已经躲开它。
//
// ★ 为什么要有这个测试：★ 第四十三轮量具在读本档时踩到 ⇒ ★ 本档正文里
// **嵌着 shell 片段** ⇒ ⇒ 片段里有 `# ✔ 实测合格…` 这样的单井号注释
// ⇒ ⇒ 而 `^#{1,6}\s` 会把它们当成标题 ⇒ ⇒ **既有登记门的小节边界**
// ⇒ ⇒ ⇒ **会在这些代码注释上被提前截断**。
//
// ★ 本测试断言两件事：
//
//	① 真实文档里**确实**存在「围栏内、被当作标题的单井号行」⇒ ★ 陷阱是真的
//	② 本门的小节切分**不会**把那些行当成标题 ⇒ ★ 本门躲开了它
func TestAuditRegistryWriteoffGateCodeBlockAwareness(t *testing.T) {
	lines := readAuditDocLines(t)

	code := woCodeFlags(lines)
	traps := 0
	for i, l := range lines {
		if !code[i] {
			continue
		}
		if woHeadRe.MatchString(l) {
			traps++
		}
	}
	if traps == 0 {
		t.Fatal("前提失效：真实文档里一个「围栏内、长得像标题的行」都没有 ⇒ ① 陷阱不再存在（★ 那就把这个测试删掉，★ 别留一条恒真的测试）")
	}

	// ② 本门的切分不得把它们当成标题
	for _, s := range woSplitSections(lines) {
		if s.Line-1 < len(lines) && code[s.Line-1] {
			t.Errorf("本门把代码块里的第 %d 行（%s）当成了小节标题", s.Line, lines[s.Line-1])
		}
	}
}
