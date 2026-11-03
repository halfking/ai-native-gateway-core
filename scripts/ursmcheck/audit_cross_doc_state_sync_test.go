package ursm_test

// audit_cross_doc_state_sync_test.go —— 跨文档登记项**三态等价**门（2026-11-03 第三十六轮）
//
// # 这道门补的是哪块缺口
//
// ★ 第三十六轮实测：**跨文档门（`tests/audit_ledger_consistency_test.sh`）是绿的，
//   而两档登记册实际上有 8 行分叉** ⇒ ⇒ ★★ **绿门 + 真分叉** = 门坏了，不是文档没问题。
//
// ★ ★★ **根因（★ 实测出来的，不是猜的）**：★ 那道 bash 门的状态抽取用
//
//	awk -F'|' '$3 ~ /闭合/ {print $2}'
//
//   ⇒ ★★ 它**只认「闭合」两个字** ⇒ ⇒ 「**已关闭**」「**已实施**」「**已落地**」
//   以及第三态「**有意如此**」**在门里根本不存在**
//   ⇒ ⇒ F13（已关闭）、F32（已落地）、F-B（已实施）**从一开始就没进过闭合集**
//   ⇒ ⇒ 而 F28 / F29 / F30（有意如此）**整族失明**
//   ⇒ ⇒ 第三十三轮的核心结论（跨文档门从未看见）。
//
// ★ 实测量化（第三十六轮量具，★ 读数可复算）：
//
//	★ 规范源条目总数        = 18
//	★ 旧门认出的「闭合」    = 11   ← ★ 盲区
//	★ 旧门认出的第三态      =  0   ← ★ **整族失明**
//	★ 旧门**看不见**的       =  6   （F13 / F32 / F-B / F28 / F29 / F30）
//	★ ⇒ **盲区 = 33.3%**
//
// # ★★★ 本门的核心思想：**按「状态类」比，而不是按「闭合 / 未闭合」比**
//
// ★ 规范源 §28.23 有**三种**状态：
//
//	closed      = 已闭合 / 已关闭 / 已实施 / 已落地
//	intentional = 有意如此（★ 第三十三轮引入的第三态：★ 不是缺陷、也不是待办）
//	open        = 仍开
//
// ★ ⇒ ★★ 旧门是**二元**的（闭合 vs 没闭合）⇒ ⇒ **第三态没有位置**
// ⇒ ⇒ ★★ 于是「有意如此」这一族（★ 4 条）**在门里不存在**。
// ⇒ ⇒ 本门比的是**类**（class），★ **三态一一对应**才算一致。
//
// # ★★ 判据（5 条）
//
//	X1 ★ **抽取器自己有牙**（★ 防假绿）：★ 规范源必须抽得到、★ 条目数 ≥ 30、
//	   ★ 且**已知的漂移面**必须被认出来（F13 / F28 / F29 / F30 / F31 各自 ≥ 1 条）
//	   ⇒ ⇒ 抽取器一退化（抽不到东西 / 词表漏词）门就红。
//	X2 ★ **覆盖**：★ **兄弟档登记的每个编号**都必须在规范源里
//	   ⇒ ⇒ ★ 反方向**不要求**（★ 主档独有的 F-A / F33 / F34 在兄弟档里没有 ⇒ 不是分叉）。
//	X3 ★★ **三态等价**：★ 兄弟档 §三 的行、§六 的项，★ 它们的**状态类**
//	   必须与规范源里**同一个编号**的类**一致**。
//	   ⇒ ★ §六 一个条目可覆盖**多个**编号（★ 例：第 6 条覆盖 F20/F21/F22/F23）
//	   ⇒ ★ 每个编号**分别**比对。
//	X4 ★ **词表完整**：★ 兄弟档的状态格**必须**落进规范词表
//	   ⇒ ★ 「维持登记」「✅ R1」「口径已订正」「✅ 部分」这类**旧词一律算违例**
//	   ⇒ ⇒ ★★ 这正是旧门放行的那批写法。
//	X5 ★★ **反向对照 + 合成夹具牙齿检验**：
//	   ★ (a) 真实文档 ⇒ **0 条违例**（⇒ 门不是恒红）
//	   ★ (b) **词表牙齿检验**：★ 6 个规范词必须**各自**映到正确的类
//	   ★ (c) **合成夹具**：★ 在**内存副本**上把兄弟档某一行**改回旧词**
//	   ⇒ ★ 门**必须**报出违例、且必须**点名那个编号**
//	   ⇒ ★★ 且**先验语义有效性**：★ 改完之后那一行确实**不再**含规范词
//	   ⇒ ★★★ **弱变异 = 假牙**（★ 本项目踩过：只摘半个标记 ⇒ 语义没变 ⇒ 门还是绿的）
//
// # ★ 为什么「旧词」必须算违例，而不是「兼容」
//
// ★ 因为旧词（★ 「维持登记」/「✅ R1」/「口径已订正」）**不表达状态类**
// ⇒ ⇒ 门读到它**无从判断** ⇒ ⇒ 它是「绿门 + 真分叉」的**直接来源**
// ⇒ ⇒ 兼容旧词 = 把那个洞**留着**。

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	xdMainDocRelPath = "docs/audit/2026-11-02-上游4xx被记成节点不健康-下一轮修复项.md"
	xdSibDocRelPath  = "docs/12小时内修订审计-2026-11-02-0350.md"
)

// ── 状态类 ──────────────────────────────────────────────────────────────

const (
	xdClosed      = "closed"
	xdIntentional = "intentional"
	xdOpen        = "open"
	xdUnknown     = "" // ⇒ 状态格里的词**不在规范词表**内
)

// xdClosedWords 是「已做完」的状态词（★ 四个都算闭合，★ 词表必须完整）。
var xdClosedWords = []string{"已闭合", "已关闭", "已实施", "已落地"}

// xdIntentionalWords 是第三态。
var xdIntentionalWords = []string{"有意如此"}

// xdOpenWords 是「仍开着」。
var xdOpenWords = []string{"仍开"}

// xdClassOf 把一段状态文字映成状态类。
//
// ★ **顺序有意义**：★ 先认闭合类、再认第三态、最后认仍开
// ⇒ ⇒ 混了两个类 ⇒ 返回 "mixed"（⇒ 门会报「状态格自相矛盾」）。
func xdClassOf(text string) string {
	var hits []string
	for _, w := range xdClosedWords {
		if strings.Contains(text, w) {
			hits = append(hits, xdClosed)
			break
		}
	}
	for _, w := range xdIntentionalWords {
		if strings.Contains(text, w) {
			hits = append(hits, xdIntentional)
			break
		}
	}
	for _, w := range xdOpenWords {
		if strings.Contains(text, w) {
			hits = append(hits, xdOpen)
			break
		}
	}
	if len(hits) == 0 {
		return xdUnknown
	}
	seen := map[string]bool{}
	for _, h := range hits {
		seen[h] = true
	}
	if len(seen) > 1 {
		return "mixed"
	}
	return hits[0]
}

// ── 编号 ────────────────────────────────────────────────────────────────

// xdIDRe 匹配登记项编号：F + 数字，或 F + 单个大写字母（★ F-A / F-B / F-C …）。
var xdIDRe = regexp.MustCompile(`F-?[0-9]+|F-[A-Z]`)

// xdNormID 把编号归一（★ 去连字符 + 大写）：★ `F-21` ⇒ `F21`，★ `F-B` ⇒ `FB`。
func xdNormID(s string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(s, "-", ""), "–", ""))
}

// xdExtractIDs 抽出一段文本里的登记项编号（★ 去重、保序）。
//
// ★ 注意：★ 「PDF4」里的 F4 **不是**登记项 ⇒ ★ 需要看前一个字符是不是字母。
func xdExtractIDs(text string) []string {
	var out []string
	seen := map[string]bool{}
	for i, r := range text {
		if r != 'F' {
			continue
		}
		if i > 0 && isXDLatinLetter(text[i-1]) {
			continue
		}
		m := xdIDRe.FindString(text[i:])
		if m == "" {
			continue
		}
		k := xdNormID(m)
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

func isXDLatinLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// ── 数据结构 ────────────────────────────────────────────────────────────

// xdEntry 是「某处写着某个编号的某个状态类」。
type xdEntry struct {
	Where string // "规范源" / "兄弟档§三" / "兄弟档§六"
	Line  int
	Class string
	Raw   string // 状态格原文（★ 报违例时要给人看）
}

type xdViolation struct {
	Where string
	Line  int
	ID    string
	Why   string
}

func (v xdViolation) String() string {
	return v.Where + ":" + strconv.Itoa(v.Line) + " [" + v.ID + "] " + v.Why
}

// ── 解析：规范源（主档 §28.23）────────────────────────────────────────

// xdCanon 解析主档 §28.23 规范源。
//
// ★ 定位必须**同时**命中「规范源」与「现状总表」（★ 第三十四轮踩过：只看一个词
// ⇒ 轮次记录里的一句「建**规范源**」就把它误判成规范源）。
func xdCanon(lines []string) (map[string]xdEntry, int) {
	out := map[string]xdEntry{}
	start := -1
	for i, ln := range lines {
		if strings.HasPrefix(ln, "### ") &&
			strings.Contains(ln, "规范源") && strings.Contains(ln, "现状总表") {
			start = i
			break
		}
	}
	if start < 0 {
		return out, -1
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "### ") || strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	for i := start + 1; i < end; i++ {
		ln := lines[i]
		if !strings.HasPrefix(ln, "|") {
			continue
		}
		cells := strings.Split(ln, "|")
		if len(cells) < 5 {
			continue
		}
		ids := xdExtractIDs(cells[1])
		if len(ids) == 0 {
			continue
		}
		raw := strings.TrimSpace(cells[2])
		out[ids[0]] = xdEntry{Where: "规范源", Line: i + 1, Class: xdClassOf(raw), Raw: raw}
	}
	return out, start + 1
}

// ── 解析：兄弟档 §三 表 ───────────────────────────────────────────────

// xdSib3 解析兄弟档 §三 的表行（★ 状态格 = 第 6 格：★ | # | 级别 | 位置 | 问题 | 本轮 |）。
func xdSib3(lines []string) map[string]xdEntry {
	out := map[string]xdEntry{}
	for i, ln := range lines {
		if !strings.HasPrefix(ln, "|") {
			continue
		}
		cells := strings.Split(ln, "|")
		if len(cells) < 7 {
			continue
		}
		ids := xdExtractIDs(cells[1])
		if len(ids) == 0 {
			continue
		}
		raw := strings.TrimSpace(cells[5])
		if _, dup := out[ids[0]]; dup {
			continue // ★ 同一个编号在 §三 只应有一行
		}
		out[ids[0]] = xdEntry{Where: "兄弟档§三", Line: i + 1, Class: xdClassOf(raw), Raw: raw}
	}
	return out
}

// ── 解析：兄弟档 §六 清单 ─────────────────────────────────────────────

// xdSib6 解析兄弟档 §六 的条目（★ 状态标记 = `★ **状态**`）。
//
// ★ ★★ 一个条目**可以覆盖多个编号**（★ 例：第 6 条同时覆盖 F20/F21/F22/F23）
// ⇒ ⇒ 返回「编号 → 它出现过的所有条目」。
func xdSib6(lines []string) map[string][]xdEntry {
	out := map[string][]xdEntry{}
	start := -1
	for i, ln := range lines {
		if strings.HasPrefix(ln, "## 六、") {
			start = i
			break
		}
	}
	if start < 0 {
		return out
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	markerRe := regexp.MustCompile(`★\s*\*\*([^*]+)\*\*`)
	for i := start; i < end; i++ {
		ln := lines[i]
		if !regexp.MustCompile(`^[0-9]+\.\s`).MatchString(ln) {
			continue
		}
		m := markerRe.FindStringSubmatch(ln)
		raw := ""
		if m != nil {
			raw = m[1]
		}
		for _, id := range xdExtractIDs(ln) {
			out[id] = append(out[id], xdEntry{
				Where: "兄弟档§六", Line: i + 1, Class: xdClassOf(raw), Raw: raw,
			})
		}
	}
	return out
}

// ── 判据 ────────────────────────────────────────────────────────────────

// xdCheck 是全部判据的实现（★ 纯函数：★ 只吃行数组 ⇒ 合成夹具可在内存副本上跑）。
func xdCheck(mainLines, sibLines []string) []xdViolation {
	var vs []xdViolation

	canon, canonLine := xdCanon(mainLines)
	s3 := xdSib3(sibLines)
	s6 := xdSib6(sibLines)

	// ── X1：抽取器自己有牙（★ 防假绿）───────────────────────────────
	if canonLine < 0 {
		return []xdViolation{{Where: "规范源", Line: 0, ID: "-",
			Why: "★ 主档里找不到 §28.23 规范源（★ 必须同时命中「规范源」+「现状总表」）⇒ 门恒绿 = 形同虚设"}}
	}
	if len(canon) < 30 {
		vs = append(vs, xdViolation{Where: "规范源", Line: canonLine, ID: "-",
			Why: "★ 规范源条目数 " + strconv.Itoa(len(canon)) + " < 30 ⇒ 抽取器可能退化了（★ 第三十六轮实测值 = 39）"})
	}
	// ★ 已知漂移面必须被认出来（★ 抽取器的牙齿检验）
	for _, id := range []string{"F13", "F28", "F29", "F30", "F31", "F24"} {
		if _, ok := canon[id]; !ok {
			vs = append(vs, xdViolation{Where: "规范源", Line: canonLine, ID: id,
				Why: "★ 已知项 " + id + " 没被抽到 ⇒ 抽取器口径退化（★ 第三十六轮实测：F13/F28/F29/F30/F31/F24 全在表里）"})
		}
	}
	// ★ 规范源每一行的状态格必须**恰好**落在一个类里
	for _, id := range xdSortedKeys(canon) {
		e := canon[id]
		if e.Class == "" || e.Class == "mixed" {
			vs = append(vs, xdViolation{Where: "规范源", Line: e.Line, ID: id,
				Why: "★ 现状格必须**恰好**落在一个状态词里（已闭合 / 已关闭 / 已实施 / 已落地 / 有意如此 / 仍开），★ 实得 " + strconv.Quote(e.Raw)})
		}
	}

	// ── X2：覆盖（★ 兄弟档登记的每个编号都必须在规范源里）────────────
	ids := map[string]bool{}
	for id := range s3 {
		ids[id] = true
	}
	for id := range s6 {
		ids[id] = true
	}
	for _, id := range xdSortedSet(ids) {
		if _, ok := canon[id]; !ok {
			vs = append(vs, xdViolation{Where: "兄弟档", Line: 0, ID: id,
				Why: "★ 兄弟档登记了 " + id + "，★ 但规范源里没有 ⇒ 「过期」没有唯一依据（★ 缺登记是过期登记的另一半）"})
		}
	}

	// ── X3 + X4：三态等价 + 词表完整 ────────────────────────────────
	compare := func(id string, e xdEntry) {
		c, ok := canon[id]
		if !ok {
			return // ★ 已由 X2 报过
		}
		if e.Class == xdUnknown {
			vs = append(vs, xdViolation{Where: e.Where, Line: e.Line, ID: id,
				Why: "★ 状态标记 " + strconv.Quote(e.Raw) + " **不在规范词表**内（已闭合/已关闭/已实施/已落地/有意如此/仍开）⇒ ★ 门无从判断状态类 ⇒ ★ 「旧词」是「绿门 + 真分叉」的直接来源"})
			return
		}
		if e.Class == "mixed" {
			vs = append(vs, xdViolation{Where: e.Where, Line: e.Line, ID: id,
				Why: "★ 状态标记同时含**多个**状态类：" + strconv.Quote(e.Raw) + " ⇒ 自相矛盾"})
			return
		}
		if e.Class != c.Class {
			vs = append(vs, xdViolation{Where: e.Where, Line: e.Line, ID: id,
				Why: "★ 两档状态类不一致：★ 规范源 = " + c.Class + "（" + c.Raw + "），★ 而这里 = " + e.Class + "（" + e.Raw + "）⇒ ★ 两份登记册分叉"})
		}
	}
	for id, e := range s3 {
		compare(id, e)
	}
	for id, es := range s6 {
		for _, e := range es {
			compare(id, e)
		}
	}

	sort.Slice(vs, func(i, j int) bool {
		if vs[i].Where != vs[j].Where {
			return vs[i].Where < vs[j].Where
		}
		if vs[i].Line != vs[j].Line {
			return vs[i].Line < vs[j].Line
		}
		return vs[i].ID < vs[j].ID
	})
	return vs
}

func xdSortedKeys(m map[string]xdEntry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func xdSortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ── 门 ──────────────────────────────────────────────────────────────────

func xdRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func xdReadLines(t *testing.T, rel string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(xdRepoRoot(t), rel))
	if err != nil {
		t.Fatalf("读 %s 失败：%v", rel, err)
	}
	return strings.Split(string(b), "\n")
}

// TestAuditCrossDocStateSync 是本门的主判据。
func TestAuditCrossDocStateSync(t *testing.T) {
	mainLines := xdReadLines(t, xdMainDocRelPath)
	sibLines := xdReadLines(t, xdSibDocRelPath)
	vs := xdCheck(mainLines, sibLines)
	if len(vs) > 0 {
		t.Errorf("跨文档登记项三态等价门：%d 条违例", len(vs))
		for _, v := range vs {
			t.Errorf("  - %s", v.String())
		}
	}
}

// TestAuditCrossDocStateSyncHasTeeth 是本门的**牙齿检验**。
//
// ★ 四步（★ 顺序不能换）：
//
//	① **反向对照**：★ 真实文档 ⇒ 0 条违例（门不是恒红）
//	② **词表牙齿检验**：★ 6 个规范词必须各自映到正确的类
//	③ **合成夹具**：★ 把兄弟档某一行**改回旧词** ⇒ 门必须报出违例并点名
//	④ ★★ **语义有效性**：★ 改完之后那一行确实**不再**含规范词
func TestAuditCrossDocStateSyncHasTeeth(t *testing.T) {
	mainLines := xdReadLines(t, xdMainDocRelPath)
	sibLines := xdReadLines(t, xdSibDocRelPath)

	// ① 反向对照
	if vs := xdCheck(mainLines, sibLines); len(vs) != 0 {
		t.Fatalf("反向对照失败：真实文档本该 0 条违例，实际 %d 条（第一条：%s）", len(vs), vs[0].String())
	}

	// ② 词表牙齿检验：★ 6 个规范词 ⇒ 6 个类
	want := map[string]string{
		"已闭合": xdClosed, "已关闭": xdClosed, "已实施": xdClosed, "已落地": xdClosed,
		"有意如此": xdIntentional, "仍开": xdOpen,
	}
	for word, cls := range want {
		if got := xdClassOf(word); got != cls {
			t.Fatalf("★★ **词表退化**：★ 规范词 %q 被映成 %q（应为 %q）⇒ 门会漏判", word, got, cls)
		}
	}
	// ★★ 旧词必须**不被**映成任何类（★ 它们是「绿门 + 真分叉」的来源）
	for _, legacy := range []string{"维持登记", "口径已订正", "✅ R1", "✅ 部分"} {
		if got := xdClassOf(legacy); got != xdUnknown {
			t.Fatalf("★★ **词表退化**：★ 旧词 %q 被映成了 %q（应为「不在词表内」）⇒ 门会把它放过去", legacy, got)
		}
	}

	// ③ 合成夹具：★ 挑一行「状态类 = intentional」的兄弟档 §三 行 ⇒ 改成旧词
	canon, _ := xdCanon(mainLines)
	s3 := xdSib3(sibLines)
	targetID, targetLine, targetRaw := "", 0, ""
	for _, id := range xdSortedKeys(s3) {
		e := s3[id]
		if e.Class != xdIntentional {
			continue
		}
		if _, ok := canon[id]; !ok {
			continue
		}
		targetID, targetLine, targetRaw = id, e.Line, e.Raw
		break
	}
	if targetID == "" {
		t.Fatalf("★ 找不到可注入的「有意如此」行 ⇒ 夹具无法构造（★ 这是**门自己坏了**，★ 不是文档的问题）")
	}

	mut := append([]string(nil), sibLines...)
	// ★ ★★ 注入：★ 把状态格**整个换成旧词**（★ 弱变异会让门测不出东西）
	cells := strings.Split(mut[targetLine-1], "|")
	if len(cells) < 7 {
		t.Fatalf("★ 注入目标行不是 §三 的表行 ⇒ 夹具构造错了")
	}
	cells[5] = " ★ **维持登记**（主档 §9.6） "
	mut[targetLine-1] = strings.Join(cells, "|")

	// ④ 语义有效性：★ 注入后该行确实**不再**含规范词
	mutRaw := strings.TrimSpace(strings.Split(mut[targetLine-1], "|")[5])
	if xdClassOf(mutRaw) != xdUnknown {
		t.Fatalf("★★ **弱变异**：★ 注入后该行仍含规范状态词（%q）⇒ 语义没变 ⇒ 门测不出东西", mutRaw)
	}
	// ★ 且原行确实**本来**含规范词（★ 证明注入真的做了事）
	if xdClassOf(strings.TrimSpace(targetRaw)) == xdUnknown {
		t.Fatalf("★ 注入目标行本来就不含规范状态词 ⇒ 夹具构造错了")
	}

	// ③ 门必须抓到
	vs := xdCheck(mainLines, mut)
	if len(vs) == 0 {
		t.Fatalf("★★ **探测器坏了**：★ 合成夹具（把 %s 的状态改成旧词「维持登记」）**没有**被门抓住", targetID)
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
