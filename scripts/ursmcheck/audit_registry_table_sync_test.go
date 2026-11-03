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
// # ★★ 判据（8 条）
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
//	C7 ★★ **整档覆盖**（★ 第三十七轮新增）：★ ★ 扫**整档**（★ 含 2 层级、
//	   4 层级的全部正文）★ 出现的**每一个**编号，都**必须**在规范源里
//	   ⇒ ⇒ ★ 「**缺登记**」这一半（★ 第三十六轮教训：★ 缺登记是过期登记的另一半）
//	   从此**有机械护栏**，★ 而不再只靠「人工复核」
//	   ⇒ ★ 代价（★ 明写）：★ **空闲编号不许以编号形态出现在正文里**
//	     （★ 「下一个空闲编号是 X」这种前瞻 ⇒ ★ 会被本门判红
//	     ⇒ ★ ★ **空闲编号不是登记项、⇒ 不许以编号形态出现** ⇒ 要写就写数量）
//	C8 ★★★ **定义点 ↔ 规范源 的指回**（★ 第三十八轮新增）：★ 正文的
//	   **定义点**（★ 某个号在正文里被「定义」的那一节）★ **必须**能被规范源
//	   那一行**指回** ⇒ ⇒ ★ 「**同号不同义**」（★ 撞号）从此**有机械护栏**
//	   ⇒ ★ ★ 与 C7 **互补**：★ C7 抓「**缺登记**」、★ C8 抓「**撞号**」
//	   （★ 撞号的两边**归一后都是已登记的号** ⇒ ★ C7 看不见它）
//	C6 ★★ **反向对照 + 合成夹具**：★
//
//	★ (a) **反向对照**：★ 真实文档必须 **0 条违例**（⇒ 门不是恒红）
//	★ (b) **合成夹具**：★ 在**内存副本**上把一行复述行的闭合标记**摘掉**
//	  ⇒ ★ 门**必须**报出违例，且必须点名那一项
//	  ⇒ ★★ 且**先验语义有效性**：★ 摘完之后那一行确实**不再**含闭合标记
//	     （★ 本轮实测踩过的坑：★ 只摘**半个** `~~` ⇒ 尾部 `~~` 还在 ⇒ 语义没变
//	     ⇒ 门还是绿的 ⇒ ⇒ **弱变异 = 假牙**）
//	★ (c) ★ **C8 的夹具**（★ 第三十八轮）：★ 把规范源那一行的 § 引用改成别的节
//	  ⇒ ★ 门**必须**报出违例、且必须以 **C8**（★ Section=定义点）点名那一项
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
//	★ **不管**（★ 仅限 C2/C3/C5）：★ 4 层级标题的小节（★ §28.x 的轮次记录区）
//	  ⇒ ★ 那是**叙述**，不是登记册
//	★ ★★ 但 **C7 管整档**（★ 第三十七轮）：★ C7 扫**全档正文**
//	  ⇒ ⇒ ★ 4 层级小节里的编号**也**必须登记（★ 「缺登记」没有护栏的那一半，
//	    ★ 就是在这些地方漏的）
//	★ ★★ **C8 管「定义点」**（★ 第三十八轮）：★ 2/3 层级标题的**主语位**里
//	  出现的编号 ⇒ ★ 规范源那一行必须**指回**它
//	  ⇒ ★★ **C8 的已知盲区（★ 明写）**：
//	    ★ ① ★ **只在标题的「主语位」判**：★ 标题里**顺带一提**的编号
//	      （★ 例：§10.6「本轮**未**动的登记项：F-B（…）」⇒ F-B 的主语不是 F-B）
//	      ⇒ ★ 不判（★ 否则会把「同一件事在两处被提到」误判成撞号）
//	    ★ ② ★ 规范源引的节与定义点**必须有祖先关系**才算指回 ⇒ ★ 若规范源
//	      引的是**完全无关**的节 ⇒ ★ 判红（★ 这正是撞号的形态）
//	    ★ ③ ★ ★ **只在主档内判**：★ 兄弟档 §三 的行**不参与**定义点判定
//	      ⇒ ★ 跨文档的撞号仍要靠**跨文档门**（★ 它比的是状态、★ 不是指向）
//
// # ★★ 字母族（F-A … F-E）的**形状约定**
//
//	★ 登记项的字母族**必须写成 `F-A`（带连字符）**：
//	  ① 散文里遍地是「F + 单个大写字母」（★ `fr-FR` / `FAIL` / `RFC3339` /
//	     `FROM` / `CONFLICT` / `FORCE_LOCK`）⇒ 裸形态**不可信**
//	  ② 本档实测：★ 字母族 **58 处全部**是 `F-A` 形态（★ FA 18 / FB 20 /
//	     FC 6 / FD 6 / FE 8）⇒ ⇒ 收紧**零代价**（★ 收紧前后 token 差集 = {FR}）
//	★ 由 TestAuditRegistryTableSyncLetterFamilyShape **双向证明**
//	  （★ 假阳性侧 + 假阴性侧 + 真实文档三面）
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

// ★ ★★ 第四十四轮新增：C8 / C9 的判据面基线（★ 棘轮）。
//
// ★ ★★ 为什么要有它（★ 第四十四轮实测）：★ C8（★ 第三十八轮建的「撞号」护栏）
// ★ **只判有定义点的编号** ⇒ ★★ 实测它的判据面 = **25/54** ⇒ ★★ 也就是说
// ★★ 兄弟档登记的 **32** 个编号里 **19 个**在「撞号」这件事上**完全无护栏**
// ⇒ ⇒ ★★ 而那个缺口**原本是隐形的** ⇒ ⇒ ★★ 所以用**两条棘轮**把它钉在纸面上：
// ★ ① 孤儿数不许变大 ★ ② 被判数不许变小。
//
// ★ ★★ 基线值改动的规矩：★ 改它必须**同时**在本档（★ §28.34）写清理由。
const (
	// regOrphanBaseline 是「规范源里【没有定义点】的行数」的上限。
	regOrphanBaseline = 29
	// regJudgedBaseline 是「C8 判得到的行数」的下限。
	regJudgedBaseline = 25
)

// ★ ★★ C10 的两条**判据面棘轮**（★ 第四十五轮新增、★ 实测基线）──────
//
// ★ ★★ 为什么要有它们（★ 与 C9 同款理由）：★ C10 的判据面在某些改动下会**悄悄变小**
//
//	★ 例：★ 有人把「登记项表」的口径改窄（★ 或把某张表从表头改掉）⇒ ★ 门判到的行少了
//	★ ⇒ ⇒ ★★ 而**没人会喊** ⇒ ★★ 所以用**两条棘轮**把判据面钉在纸面上。
//
// ★ ★★ 口径（★ 实测值、★ 第四十五轮量具跑全档）：
//
//	★ regC10RowBaseline    = 「登记项表」的**表体行数**（★ 被判的面）
//	★ regC10LabelBaseline  = 首格里的 **§ 标签数**（★ C10-b 真正判的那一面）
const (
	// regC10RowBaseline 是 C10 判到的表体行数下限。
	regC10RowBaseline = 46
	// regC10LabelBaseline 是 C10-b 判到的「§ 标签」数下限。
	regC10LabelBaseline = 12
)

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
	Key      string // F-项用归一编号（如 F13）；非 F 项用原文（如 §四.2）
	Raw      string // 第一格原文
	Evidence string // 依据列（★ 第三十八轮新增：★ C8 要拿它里面的 § 引用）
	Status   string // "closed" | "open" | "intentional"
	Line     int
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
//
// ★★★ 第三十七轮修（★ 收紧**字母族**形状）：★
//
//	★ 散文里遍地是「F + 单个大写字母」：★ `fr-FR`（i18n 语言标签）、
//	  `FAIL`（PASS/FAIL 读数）、`RFC3339`、`FROM`、`CONFLICT`、`FORCE_LOCK`…
//	★ 而登记项的**字母族**（F-A … F-E）★ **一律带连字符**
//	  （★ 本档实测：★ FA 18 / FB 20 / FC 6 / FD 6 / FE 8 处，★ **全部**是 `F-A` 形态）
//	⇒ ⇒ 所以规则是：★ **字母族必须写成 F-A（带连字符）**；
//	  ★ 裸的 `F`+单字母 ⇒ 一律**不是**登记项
//
//	★★ 实测代价（★ 量具跑整档，★ 收紧前后 token 集合差集 = {FR}）：★
//	  ★ 只掉了 `fr-FR` 那一个假 token ⇒ ★ **零代价**
//	  （★ 数字族 F13 / F-21 的形态**完全不受影响**）
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
		// ★★★ 第三十七轮：★ 记下 F 后面**有没有连字符**（★ 字母族形状的判据）
		hadDash := false
		if len(rest) > 1 && (rest[1] == '-' || strings.HasPrefix(rest[1:], "–")) {
			hadDash = true
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
		// ★ 后缀 = F 之后（★ 含可选连字符）的那一段（★ 必是 ASCII 数字/大写字母）
		suffix := rest[j:k]
		// ★★★ 字母族**必须**带连字符（★ F-A 形态）：★
		//   裸的 `F`+单字母 = 散文缩写（★ fr-FR / FAIL / FROM …）⇒ 不是登记项
		if isLetterFamilySuffix(suffix) && !hadDash {
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

// isLetterFamilySuffix 判断后缀是不是「**单个**大写字母」（★ 字母族）。
//
// ★ 只有「单个」才可能是字母族：★ `AIL`（FAIL）/ `ROM`（FROM）/ `ORCE`（FORCE）
// ⇒ 本来就被 regIDPattern 挡掉，★ 这里只是把「字母族」这条规则**写显**。
func isLetterFamilySuffix(s string) bool {
	return len(s) == 1 && isASCIIUpper(s[0])
}

// regIDPattern 是归一后允许的编号形状：F + 数字，或 F + 单个大写字母。
//
// ★ 第三十六轮修：★ 旧版只允许 `F([0-9]+|B)` ⇒ ★ **F-A / F-C / F-D / F-E**
// （★ 主档第七 / 十一 / 十二 / 十三轮各自的**轮次局部编号**）**在门里根本不存在**
// ⇒ ⇒ 「抽取器看不见 ⇒ 门恒绿」⇒ ★ 现在一视同仁。
//
// ★ 第三十七轮注：★ 这个正则**只用来判「归一后的 token 像不像编号」**
// （★ 规范源的键是归一后的 FA / F13 …），★ **抽取入口另有更严的形状规则**
// ⇒ 见 extractRegIDs 里的「★ 字母族必须写成 F-A」。
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
				Key:      key,
				Raw:      raw,
				Evidence: evidenceOf(r),
				Status:   classifyStatus(statusCell),
				Line:     r.Line,
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

// ── 判据 C8 的量具：★ 节号归一 ＋ 定义点抽取 ─────────────────────────────

// regDefHeadRe 匹配 **2/3/4 层级**的小节标题（★ 定义点只取 2/3 层级）。
//
// ★ 注意与 regHeadingRe 的区别：★ regHeadingRe 只认 3/4 层级（★ 那是
// ★ 「登记项小节」的判定口径）；★ C8 要的是**定义点**，★ 它在 2/3 层级里。
var regDefHeadRe = regexp.MustCompile(`^(#{2,4})\s+(.*)$`)

// regSecNumRe 匹配**标题开头**的节号（★ 阿拉伯数字或中文数字）。
//
// ★ 例：`## 十九、实施记录…` → 十九 ／ `### 11.2 F-35（Medium）：…` → 11.2
var regSecNumRe = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)*|[零一二三四五六七八九十]+(?:\.[0-9]+)*)\s*[、.．]?\s*`)

// regSecRefRe 匹配正文里的 **§ 节引用**。
var regSecRefRe = regexp.MustCompile(`§\s*([0-9]+(?:\.[0-9]+)*|[零一二三四五六七八九十]+(?:\.[0-9]+)*)`)

// regCNDigits 是中文数字的字面值。
var regCNDigits = map[rune]int{'零': 0, '一': 1, '二': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}

// regChineseNum 把「十一」「二十」「二十四」这类中文数字转成整数。
//
// ★ 只覆盖 1–99（★ 本档的节号最多到二十四、★ 再加 §28.x）。
func regChineseNum(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	if allASCIIDigits(s) {
		n, err := strconv.Atoi(s)
		return n, err == nil
	}
	r := []rune(s)
	idx := -1
	for i, c := range r {
		if c == '十' {
			idx = i
			break
		}
	}
	if idx < 0 {
		if len(r) == 1 {
			v, ok := regCNDigits[r[0]]
			return v, ok
		}
		return 0, false
	}
	tens := 10
	if idx > 0 {
		if idx != 1 {
			return 0, false
		}
		v, ok := regCNDigits[r[0]]
		if !ok {
			return 0, false
		}
		tens = v * 10
	}
	ones := 0
	if idx+1 < len(r) {
		if idx+2 != len(r) {
			return 0, false
		}
		v, ok := regCNDigits[r[idx+1]]
		if !ok {
			return 0, false
		}
		ones = v
	}
	return tens + ones, true
}

func allASCIIDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isASCIIDigit(s[i]) {
			return false
		}
	}
	return true
}

// regNormSec 把节号**归一**（★ 中文数字 → 阿拉伯，★ 便于两边比对）。
//
// ★ 例：「§十一.2」⇒ 11.2 ／ 「§二十四」⇒ 24 ／ 「§9.1」⇒ 9.1
func regNormSec(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "§")
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	parts := strings.Split(s, ".")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			return ""
		}
		if allASCIIDigits(p) {
			out = append(out, p)
			continue
		}
		n, ok := regChineseNum(p)
		if !ok {
			return ""
		}
		out = append(out, strconv.Itoa(n))
	}
	return strings.Join(out, ".")
}

// regSecRefs 抽出一段文本里的 **§ 节引用**（★ 归一后的节号，★ 去重排序）。
func regSecRefs(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range regSecRefRe.FindAllStringSubmatch(text, -1) {
		n := regNormSec(m[1])
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// regSecCovered 判断某节与 refs 是否**互相覆盖**（★ 祖先关系，★ 双向）。
//
// ★ 为什么是**双向**（★ 第三十八轮实测）：
//
//	★ 规范源引的可能是**子节**（★ 例：F16 引「§20.2」）
//	★ 而标题是**父节**（★ 例：「## 二十、实施记录（… F-16 …）」⇒ §20）
//	⇒ ⇒ 只认「祖先覆盖后代」会把这**判成不相交** ⇒ ★ 假阳性
//	★ ⇒ 所以：**任一方是另一方的祖先**即算覆盖
func regSecCovered(sec string, refs []string) bool {
	for _, r := range refs {
		if sec == r || strings.HasPrefix(sec, r+".") || strings.HasPrefix(r, sec+".") {
			return true
		}
	}
	return false
}

// regDefPoint 是正文的**定义点**：★ 一个 2/3 层级的小节标题，★ 其「节号之后、
// 第一个冒号之前」那一段里带着登记项编号 ⇒ ⇒ 那一节就是该编号的**定义处**。
type regDefPoint struct {
	Line int
	Sec  string
	IDs  []string
	Head string
}

// regDefPoints 抽出全部**定义点**。
//
// ★ 口径（★ 明写，★ 避免「以为管住了」）：
//
//	★ **3 层级标题**：★ 取「节号之后、★ **第一个冒号之前**」那一段里的编号
//	  （★ 例：「### 11.2 F-35（Medium）：…」⇒ §11.2 的**主语**是 F-35）
//	★ **2 层级标题**：★ 取「★ 第一个冒号之后、★ 破折号之前」那一段里的编号
//	  （★ 例：「## 十七、实施记录（… 第十四轮：F-16 —— 模态不匹配判决表可单测…）」
//	    ⇒ §17 的**主语**是 F-16）
//	★ **不要**：标题里**顺带一提**的编号（★ 例：「### 10.6 本轮**未**动的登记项：
//	  F-B（4xx 上其它落节点侧的正文规则）」⇒ F-B 在**副标题**里、★ 不是主语
//	  ⇒ ★ 那一节的 F-B 其实是**第十二轮**登记的那一条 ⇒ ★ 不能按本节判）
//	★ **不要**：4 层级标题（★ §28.x 的轮次记录区 ⇒ 那是**叙述**，不是定义）
//
// ★★ 这套口径是**实测出来的**（★ 第三十八轮）：★ 候选口径有三套
//
//	★ ①「整段标题都算」⇒ ★ 在**当前（正确）**文档上就报 **4 条假阳性**
//	  （★ 例：§7.3 / §10.6 里的顺带一提）
//	★ ②「冒号之前」⇒ ★ 只认 3 层级 ⇒ ★ 漏掉 §十七 那一族撞号
//	★ ③ ★ **本口径**（★ 3 层级取冒号前、★ 2 层级取冒号后破折号前）
//	  ⇒ ★ 当前文档 **0 条**、★ 旧文档点名 **5 处真撞号**（★ 两族全命中）
func regDefPoints(lines []string) []regDefPoint {
	var out []regDefPoint
	for i, l := range lines {
		m := regDefHeadRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		if len(m[1]) == 4 {
			continue // ★ 4 层级 = 轮次记录区
		}
		text := m[2]
		sm := regSecNumRe.FindStringSubmatch(text)
		if sm == nil {
			continue
		}
		sec := regNormSec(sm[1])
		if sec == "" {
			continue
		}
		rest := text[len(sm[0]):]
		var seg string
		if len(m[1]) == 3 {
			seg = rest
			if p := strings.IndexAny(seg, "：:"); p >= 0 {
				seg = seg[:p]
			}
		} else {
			// ★ 2 层级：★ 冒号之后、破折号之前
			seg = ""
			if p := strings.Index(rest, "："); p >= 0 {
				seg = rest[p+1:]
			}
			if p := strings.Index(seg, "——"); p >= 0 {
				seg = seg[:p]
			}
		}
		ids := extractRegIDs(seg)
		if len(ids) == 0 {
			continue
		}
		out = append(out, regDefPoint{Line: i + 1, Sec: sec, IDs: ids, Head: text})
	}
	return out
}

// evidenceOf 取一行的**依据列**（★ 规范源表的第 3 格）。
func evidenceOf(r regRow) string {
	if len(r.Cells) < 3 {
		return ""
	}
	return r.Cells[2]
}

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// regCoverageCounts 算「★ 规范源里有定义点 / 没有定义点」这两条数（★ C9 的量具）。
//
// ★★ 为什么抽成独立函数（★ 第四十四轮补）：★ 让**测试**能把这两个数**打印出来**
//
//	⇒ ⇒ ★ 文档 §28.34.2 里写的那几个读数（★ 「54 条 / 判得到 25 / 孤儿 29」）
//	★★ **从此可被机器复算**（★ `go test ./scripts/ursmcheck/ -run TestAuditRegistryTableSync -v`）
//	★★ ⇒ ⇒ **而不是只靠「文档里写着」**（★ 那正是本轮订正掉的那类缺陷）
//
// ★ 口径（★ 与 C9 判据**逐字一致**、★ 不许各算各的）：
//
//	★ 定义点 = `regDefPoints` 的口径（★ 2/3 层级标题的主语位）
//	★ ⇒ ★★ **不是**「表格行首格」⇒ ★ 那个口径会被**规范源表自身**污染 ⇒ ⇒ 自指的万能定义点
func regCoverageCounts(lines []string, canon []canonEntry) (orphan int, judged int) {
	withDef := map[string]bool{}
	for _, dp := range regDefPoints(lines) {
		for _, id := range dp.IDs {
			withDef[id] = true
		}
	}
	for _, c := range canon {
		if c.Key == "" {
			continue
		}
		if withDef[c.Key] {
			judged++
		} else {
			orphan++
		}
	}
	return orphan, judged
}

// ── C10 的量具：★ 表 / 行 ─────────────────────────────────────────────────
//
// ★ ★★ 本节的三个量具（★ `regIsSepRow` / `regIsHeaderRow` / `regTables`）
// ★ ★★ 都是**第四十五轮实测踩出来的**（★ 记在这里、★ 免得下一个人重做）：
//
//	★ ① **分隔行必须专门判**：★ `splitRowCells == nil` 对【分隔行】与【空行】**都成立**
//	  ⇒ ★★ 那个口径分不出「表头下面那行分隔线」
//	★ ② **表头 = 分隔行的【上一行】**：★ ★ 而**不能**用「下一行不是表格行 ⇒ 本行是表头」
//	  ⇒ ★★ 那个口径会把**表格的末行**（★ 它的下一行是空行）误判成表头 ⇒ ★★ **整行被吃掉**
//	  ⇒ ★★ ★ 本轮**实测踩了两次**才把它逼出来

// regIsSepRow 判断这一行是不是表格的**分隔行**（★ `|---|---|`）。
func regIsSepRow(l string) bool {
	t := strings.TrimSpace(l)
	if !strings.HasPrefix(t, "|") || !strings.HasSuffix(t, "|") || len(t) < 2 {
		return false
	}
	for _, c := range strings.Split(t[1:len(t)-1], "|") {
		if strings.Trim(c, "-: ") != "" {
			return false
		}
	}
	return true
}

// regIsHeaderRow 判断第 i 行是不是**表头行**。
//
// ★ ★ 口径（★ 第四十五轮实测）：★ **分隔行的上一行**才是表头。
func regIsHeaderRow(lines []string, i int) bool {
	if i+1 >= len(lines) {
		return false
	}
	if splitRowCells(lines[i]) == nil {
		return false // ★ 本行不是表格行
	}
	return regIsSepRow(lines[i+1]) // ★ 下一行【是分隔行】⇒ 本行是表头
}

// regTable 是文档里的一张表（★ C10 的判据面单位）。
type regTable struct {
	Line     int      // 表头行号（1-based）
	SecHead  string   // 最近的**任意**层级标题（★ 去井号）
	SecLevel int      // 该标题的层级
	Header   []string // 表头格
	Rows     []regRow // 表体行（★ 已排除表头行与分隔行）
}

// regTables 抽出文档里全部的表（★ 代码块感知）。
//
// ★ 代码块感知（★ 与 `woSplitSections` 同口径）：★ 围栏内的行不算表。
func regTables(lines []string) []regTable {
	var out []regTable
	inCode := false
	secHead, secLevel := "", 0
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			inCode = !inCode
			continue
		}
		if inCode {
			continue
		}
		if anyHeadingRe.MatchString(l) {
			secHead = strings.TrimSpace(strings.TrimLeft(l, "#"))
			secLevel = len(l) - len(strings.TrimLeft(l, "#"))
			continue
		}
		if !strings.HasPrefix(strings.TrimSpace(l), "|") {
			continue
		}
		cells := splitRowCells(l)
		if cells == nil {
			continue
		}
		if !regIsHeaderRow(lines, i) {
			continue
		}
		t := regTable{Line: i + 1, SecHead: secHead, SecLevel: secLevel, Header: cells}
		for j := i + 1; j < len(lines); j++ {
			s2 := strings.TrimSpace(lines[j])
			if strings.HasPrefix(s2, "```") || !strings.HasPrefix(s2, "|") {
				break
			}
			if regIsSepRow(lines[j]) {
				continue // ★ 分隔行是**结构**、★ 不是数据行 ⇒ ★ 跳过、★ 不 break
			}
			c2 := splitRowCells(lines[j])
			if c2 == nil {
				break
			}
			t.Rows = append(t.Rows, regRow{Line: j + 1, Text: lines[j], Cells: c2, IsTbl: true})
		}
		out = append(out, t)
		// ★ 跳过已吃掉的表体（★ 下一轮从「表的结束处」继续）
		i = t.Line + len(t.Rows) - 1
	}
	return out
}

// regIsRegistryTable 判断这张表是不是「登记项表」（★ C10 的判据面）。
//
// ★ ★★ 口径（★ 明写、★ 避免「以为管住了」）：
//
//	① **3 层级**且标题含「登记项」⇒ ★ 算（★ 与 `regSections` 的 history / panorama **逐字一致**）
//	② **4 层级**（★ 轮次记录区）⇒ ★ 只认 ★ **表头首格含「登记项」**的表
//	   （★ 例：§28.21 的表头是「§7.6 登记项」⇒ 算；★ 而 §28.20.4 的表头是
//	    「项 \| 读数」⇒ ★ **不算** —— ★ 那是**测量表**、★ 不是登记项表）
//	③ **规范源表**（★ §28.23）⇒ ★ **不算**（★ 它就是登记册本身）
func regIsRegistryTable(t regTable) bool {
	if strings.Contains(t.SecHead, "规范源") && strings.Contains(t.SecHead, "现状总表") {
		return false
	}
	if !strings.Contains(t.SecHead, "登记项") {
		return false
	}
	if t.SecLevel == 3 {
		return true
	}
	if t.SecLevel == 4 {
		return len(t.Header) > 0 && strings.Contains(t.Header[0], "登记项")
	}
	return false
}

// regC10Violations 跑 **C10** 的全部判据（★ 纯函数：★ 只吃行数组）。
//
// ★ ★★ C10 ＝ 「★ **登记** ⇒ 必须**有编号**」＝ C7 的**反向半边**（★ 第四十五轮新增）：
//
//	★ C7（★ 第三十六/三十七轮）＝「★ **编号** ⇒ 必须**登记**」
//	★ ⇒ ⇒ ★★ C10 ＝「★ **登记** ⇒ 必须**有编号**」⇒ ★★ ⇒ ★★ **⇒ 两条合起来才把「编号」闭死**
//
// ★ ★★ 判据面（★ 明写）：★ 「登记项表」的**表体行首格**（★ 那是**登记项标签**的位置）
//
//	★ ⇒ ★★ **为什么只认首格**（★ 第四十五轮实测）：★ 同一个表里、★ § 引用出现在第 2/3 格时
//	  是**出处 / 依据**（★ 例：「★ 12h 审计轮 P3-12」那一列）⇒ ★★ 不是标签
//	  ⇒ ★★ ⇒ ★★ **全档首格含 § 的表体行【本轮改动前】共 24 行**、★ 其中**只有 10 行在登记项表里**
//	  ⇒ ★★ ⇒ ★★ （★ 其余 14 行是别的表的**出处列**）⇒ ★★ **落在整行上会误报一片**（★ 实测过）
func regC10Violations(lines []string, canon []canonEntry) []regViolation {
	var vs []regViolation

	// ★ 规范源里「依据列引了 §X」⇒ 「§X 对应哪个登记项编号」的索引
	byRef := map[string][]string{}
	for _, c := range canon {
		if c.Key == "" {
			continue
		}
		for _, ref := range regSecRefs(c.Evidence) {
			byRef[ref] = append(byRef[ref], c.Key)
		}
	}

	tables := regTables(lines)
	nChecked := 0
	nLabeled := 0
	for _, t := range tables {
		if !regIsRegistryTable(t) {
			continue
		}
		// ★ 全景表的第一列混着**对外可见动作**（★ 部署 211 / push origin/main）
		//   ⇒ ★ 它们不是 F-编号族的登记项 ⇒ ★ **C10-a 不管全景表**
		panorama := strings.Contains(t.SecHead, "全景")
		for _, r := range t.Rows {
			if len(r.Cells) == 0 {
				continue
			}
			first := r.Cells[0]
			ids := extractRegIDs(first)
			refs := regSecRefs(first)
			nChecked++

			// ── C10-a：★ 首格是登记项标签 ⇒ 必须有编号 ─────────────────
			//
			// ★ ★★ 口径（★ 第四十五轮端到端实测逼出来的）：★ 这里**不 continue**
			//
			//	★ 因为 ★ `continue` 会让**这一行不再被 C10-b 判**、★ 也不计入 nLabeled
			//	★ ⇒ ⇒ ★★ 于是「★ 一行缺号」会**连带**把 C10-c 的棘轮打红
			//	★ ⇒ ⇒ ★★ 而那是**两条判据互相污染**（★ 实测：摘掉一个号 ⇒ C10-a 与
			//	  C10-c **同时**报 ⇒ ★★ 读者会以为判据面空了、★ 而其实只是查出一个缺陷）
			if !panorama && len(ids) == 0 {
				if !strings.HasPrefix(first, "`") {
					vs = append(vs, regViolation{Section: t.SecHead, Line: r.Line, ID: "-",
						Reason: "★ **C10-a**：★ 这一行是一笔登记（★ 首格就是登记项标签）、★ 而它**没有编号**" +
							"⇒ ★ 「过期」没有唯一依据（★ 第四十五轮实测：★ 全档有 8 行是这个形状）"})
				}
				// ★ **测量行**（★ 量的是代码符号、不是登记项）⇒ ★ 不报 C10-a
			}

			// ── C10-b：★ 首格里的每个 § 标签 ⇒ 必须解析到**带号**的规范源行 ──
			for _, ref := range refs {
				nLabeled++
				keys := byRef[ref]
				if len(keys) == 0 {
					vs = append(vs, regViolation{Section: t.SecHead, Line: r.Line, ID: "-",
						Reason: "★ **C10-b**：★ 首格把 §" + ref + " 当作**登记项标签**、★ 而规范源里" +
							"**没有任何一行**的依据列引它 ⇒ ★ 它没有编号 ⇒ ★ 「过期」没有唯一依据"})
					continue
				}
				hit := false
				for _, k := range keys {
					if containsID(first, k) {
						hit = true
						break
					}
				}
				if !hit {
					vs = append(vs, regViolation{Section: t.SecHead, Line: r.Line, ID: "-",
						Reason: "★ **C10-b**：★ 首格把 §" + ref + " 当作登记项标签、★ 规范源里对应的编号是 " +
							strings.Join(keys, " / ") + "、★ 而它**没有出现在这个首格里**" +
							"⇒ ★ 「有号的旁边藏着没号的」（★ 全景表那一行就是这个形状）"})
				}
			}
		}
	}

	// ── C10-c：★ 防假绿（★ 判据面不许悄悄空掉）────────────────────────
	if nChecked < regC10RowBaseline {
		vs = append(vs, regViolation{Section: "判据面", Line: 0, ID: "-",
			Reason: "★ **C10-c**：★ C10 判到的表体行只有 " + strconv.Itoa(nChecked) + " 行 < " +
				strconv.Itoa(regC10RowBaseline) + "（★ 第四十五轮实测基线）⇒ ★ 判据面在缩小 ⇒ 门恒绿 = 形同虚设"})
	}
	if nLabeled < regC10LabelBaseline {
		vs = append(vs, regViolation{Section: "判据面", Line: 0, ID: "-",
			Reason: "★ **C10-c**：★ C10-b 判到的「§ 标签」只有 " + strconv.Itoa(nLabeled) + " 个 < " +
				strconv.Itoa(regC10LabelBaseline) + "（★ 第四十五轮实测基线）⇒ ★ 「§ 标签 ⇒ 必须带号」" +
				"这一面空了 ⇒ 门恒绿 = 形同虚设"})
	}

	sort.Slice(vs, func(i, j int) bool {
		if vs[i].Section != vs[j].Section {
			return vs[i].Section < vs[j].Section
		}
		return vs[i].Line < vs[j].Line
	})
	return vs
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

	// ── C7：★★ 整档 id ⊆ 规范源 id（★ 第三十七轮新增）────────────────
	//
	// ★★ C2 只扫「登记项小节」⇒ ★ **2 层级**的小节（★ 例：§十一 / §十七 /
	//   §二十 —— 那些「## 二十、实施记录（… F-16 / F-17 / F-18 …）」）
	//   **根本不在扫描范围内** ⇒ ★ ⇒ 「缺登记」这一半**没有机械护栏**。
	// ★★ 而第三十七轮实测（★ 量具跑整档）：★ 撞号修完后，
	//   **整档出现的每个编号都在规范源里**（★ 44 = 44，★ 两边完全相等）
	//   ⇒ ⇒ ★ 这条**可以变成断言**（★ 而不必停在「人工复核」）。
	// ★★ 口径（★ 明写，避免「以为管住了」）：
	//   ★ 扫**整档**（★ 含 4 层级的轮次记录区：★ 它们也在用编号）
	//   ★ ⇒ ★★ 所以**空闲编号不能以编号形态出现在正文里**
	//     （★ 例：★ 「下一个空闲编号是 X」这种前瞻 ⇒ ★ 会被本门判红
	//     ⇒ ★ ★ **空闲编号不是登记项、⇒ 不许以编号形态出现** ⇒ 要写就写数量）
	for i, ln := range lines {
		for _, id := range extractRegIDs(ln) {
			if !canonKeys[id] {
				vs = append(vs, regViolation{Section: "整档", Line: i + 1, ID: id,
					Reason: "★ 整档出现的这个编号**不在规范源**里 ⇒ ★ 「缺登记」没有唯一依据（★ 第三十六轮教训：★ 缺登记是过期登记的另一半）"})
			}
		}
	}

	// ── C8：★★ 定义点 ↔ 规范源 的**指回**（★ 第三十八轮新增）──────────────
	//
	// ★★ 为什么需要它：★ 第三十七轮实测出「**同号不同义**」（★ 撞号）：
	// ★   主档 §十一 的 `F-1`～`F-5` 与兄弟档的 `F1`～`F5` 是**不同的两件事**，
	// ★   而 C7（整档 id ⊆ 规范源 id）**看不见**它（★ 归一后都是已登记的号）
	// ⇒ ⇒ 所以判据必须落在「**这个号在正文里被定义在哪一节**」上：
	// ★★★ **规范源那一行必须指回那个定义点**
	// ⇒ ★ 指不回去 ⇒ ★ 说明这个号在正文里被当成了**另一件事** ⇒ ★ **撞号**
	//
	// ★★ 口径（★ 明写）：
	//   ★ 定义点 = 2/3 层级标题、★ 且「节号之后、第一个冒号之前」那段里带编号
	//   ★ 「指回」= 规范源依据列里**有**一个 § 引用，★ 它**等于**该定义点
	//     或**是它的祖先**（★ 例：依据列引「第九轮（§九）」⇒ 覆盖 §9.1）
	//   ★ 4 层级标题（§28.x 轮次记录区）**不是**定义点（★ 那是叙述）
	//
	// ★★ ★★ **上一版设计被实测推翻过（★ 记下来，★ 免得下一个人重做）：★★
	//   ★ 第一版想把「**依据列的代码 token**」当指纹、★ 比对两边的交集
	//   ★ ⇒ ★ **在当前（正确）文档上就报出 92 条假阳性**
	//   ★ ⇒ ★ 原因：★ 规范源的依据列只是**摘要**、★ 不是全部证据
	//   ★   （★ 例：兄弟档的 F4 覆盖**两条**死代码、★ 而规范源只引了其中一条）
	//   ★ ⇒ ★ ⇒ **token 交集这条路不成立**（★ 先量具再读数：★ 量具把它否了）
	canonByKey := map[string]canonEntry{}
	for _, c := range canon {
		canonByKey[c.Key] = c
	}
	for _, dp := range regDefPoints(lines) {
		for _, id := range dp.IDs {
			c, ok := canonByKey[id]
			if !ok {
				continue // ★ C7 已经报「不在规范源里」了
			}
			refs := regSecRefs(c.Evidence)
			if regSecCovered(dp.Sec, refs) {
				continue
			}
			vs = append(vs, regViolation{Section: "定义点", Line: dp.Line, ID: id,
				Reason: "★ 正文 §" + dp.Sec + " 把这个号**定义**在这里 ⇒ ★ 而规范源那一行（★ L" +
					strconv.Itoa(c.Line) + "）**没有指回**这一节（★ 它引的是：" + regSecRefList(refs) +
					"）⇒ ⇒ ★ **同号不同义**：★ 这个号在正文里被当成了另一件事 ⇒ ★ 撞号"})
		}
	}

	// ── C9：★★ 规范源里【没有定义点】的行数不许悄悄变大（★ 第四十四轮新增）──
	//
	// ★★ 为什么需要它（★ 第四十四轮实测）：
	//   ★ C8（★ 第三十八轮建的「撞号」护栏）**只判有定义点的编号**
	//   ★ ⇒ ★★ 实测它的判据面 = **25/54** ⇒ ★★ 而兄弟档登记的 **32** 个编号里
	//   ★★ **19 个**在正文里**没有定义点** ⇒ ⇒ ★★ **它们在「撞号」这件事上完全无护栏**
	//   ★ ⇒ ⇒ ★★ 而这个缺口**原本是隐形的**（★ 没人知道 C8 判不到它们）
	//   ★ ⇒ ⇒ ★★ 所以：★ 把「孤儿面」变成**可数的**、★ 并用棘轮挡住它变大
	//
	// ★ 口径（★ 明写）：★ 定义点 = `regDefPoints` 的口径（★ 2/3 层级标题的主语位）
	//   ★ ★★ 而**不是**「表格行首格」⇒ ★ 因为量具实测那个口径会被**规范源表自身**污染
	//   ★ ★★ ⇒ ⇒ 「定义点」退化成**自指的万能定义点** ⇒ ⇒ 信号被摧毁（★ §28.34）
	orphan, judged := regCoverageCounts(lines, canon)
	if orphan > regOrphanBaseline {
		vs = append(vs, regViolation{Section: "规范源", Line: 0, ID: "-",
			Reason: "★ 规范源里【没有定义点】的行数 " + strconv.Itoa(orphan) + " > " +
				strconv.Itoa(regOrphanBaseline) + "（★ 第四十四轮实测基线）⇒ ★ 「孤儿面」变大了 ⇒ ★ 这些行 C8 判不到（★ 撞号 / 漂移对它们无护栏）⇒ ★ 要么给它们补定义点、★ 要么显式更新基线并写清理由"})
	}
	if judged < regJudgedBaseline {
		vs = append(vs, regViolation{Section: "规范源", Line: 0, ID: "-",
			Reason: "★ C8 的判据面只有 " + strconv.Itoa(judged) + " 条 < " +
				strconv.Itoa(regJudgedBaseline) + "（★ 第四十四轮实测基线）⇒ ★ 「撞号」护栏在缩小 ⇒ ★ 兄弟档登记的编号有一半以上没有护栏"})
	}

	// ── C10：★★ 「★ 登记 ⇒ 必须有编号」＝ C7 的反向半边（★ 第四十五轮新增）──
	vs = append(vs, regC10Violations(lines, canon)...)

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

// regSecRefList 把节号引用渲染成可读的一串（★ 供违例说明用）。
func regSecRefList(refs []string) string {
	if len(refs) == 0 {
		return "（无）"
	}
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, "§"+r)
	}
	return strings.Join(out, " / ")
}

// regBreakCoveringRef 把一行里**第一个**覆盖 target 的 § 引用改成 "§99.99"。
//
// ★ 用于**合成夹具**：★ 把规范源那一行的指向改掉 ⇒ ★ 那个定义点就指不回去了。
func regBreakCoveringRef(line, target string) (string, bool) {
	for _, m := range regSecRefRe.FindAllStringSubmatchIndex(line, -1) {
		n := regNormSec(line[m[2]:m[3]])
		if n == "" || !regSecCovered(target, []string{n}) {
			continue
		}
		return line[:m[0]] + "§99.99" + line[m[1]:], true
	}
	return line, false
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

// readAuditDocLines 读主审计档（★ 可用 `AUDIT_MAIN_DOC` 覆盖路径）。
//
// ★ 环境覆盖的用处（★ 第三十八轮新增）：★ 让「★ 拿新门去测**真实旧文档**」
// ★ 这件事**一条命令**就能复现（★ 与跨文档 bash 门的 `AUDIT_MAIN_DOC` 同口径）。
func readAuditDocLines(t *testing.T) []string {
	t.Helper()
	p := filepath.Join(repoRootRegistrySync(t), regAuditDocRelPath)
	if v := strings.TrimSpace(os.Getenv("AUDIT_MAIN_DOC")); v != "" {
		p = v
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读审计档失败（%s）：%v", p, err)
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

	// ★ ★★ 把 C9 的几个数**打印出来**（★ 第四十四轮新增）：★
	//   ★ 文档 §28.34.2 里写的读数（★ 「★ 规范源 54 条 / ★ 判得到 25 / ★ 孤儿 29」）
	//   ★ ★★ **从此可被机器复算**、★ 而不必只信「文档里写着」
	//   ★ ⇒ ★⇒ 下一轮的人只要跑这一条命令就能核对自己的数（★ 口径不统一时立刻看出来）
	canon := regCanonical(lines)
	orphan, judged := regCoverageCounts(lines, canon)
	t.Logf("★ C9 读数：★ 规范源条目 **%d**（★ 全部 F-编号项口径）｜★ 判得到 **%d**｜★ 孤儿 **%d**"+
		"（★ 棘轮：★ 孤儿 ≤ %d、★ 被判 ≥ %d）",
		len(canon), judged, orphan, regOrphanBaseline, regJudgedBaseline)
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

// TestAuditRegistryTableSyncC10HasTeeth 是 **C10** 的**牙齿检验**。
//
// ★ ★★ 两步（★ 顺序不能换）：
//
//	① **反向对照**：★ 真实文档 ⇒ C10 违例 **0** 条（★ 门不是恒红）
//	② **合成夹具**：★ 在**内存副本**上把一行登记项标签的**编号摘掉**
//	   ⇒ ★ C10 **必须**报出违例、★ 且必须点名**那一行**
//
// ★ ★★ 为什么选「★ 摘编号」当变异（★ 而不是「★ 删一行」）：
//
//	★ ★ 「★ 摘编号」造出的正是**第四十五轮修的那个缺陷的原型**
//	★ （★ ★ 原型 = 「★ 拿 § 节号当登记项标签、★ 却没给它编号」）
//	★ ⇒ ⇒ ★★ 所以这一条夹具**等于**把修掉的那个 bug 原样放回去
func TestAuditRegistryTableSyncC10HasTeeth(t *testing.T) {
	lines := readAuditDocLines(t)

	// ① 反向对照
	if vs := regC10Violations(lines, regCanonical(lines)); len(vs) != 0 {
		t.Fatalf("反向对照失败：真实文档本该 0 条 C10 违例，实际 %d 条（第一条：%s）", len(vs), vs[0].String())
	}

	// ② 合成夹具：★ 找一行「首格同时有编号和 § 标签」的登记项行
	targetLine := -1
	for _, tb := range regTables(lines) {
		if !regIsRegistryTable(tb) {
			continue
		}
		for _, r := range tb.Rows {
			if len(r.Cells) == 0 {
				continue
			}
			first := r.Cells[0]
			if len(extractRegIDs(first)) == 0 {
				continue
			}
			if len(regSecRefs(first)) == 0 {
				continue
			}
			targetLine = r.Line
			break
		}
		if targetLine >= 0 {
			break
		}
	}
	if targetLine < 0 {
		t.Fatalf("★ 找不到可注入的登记项行 ⇒ 夹具无法构造（★ 这是**门自己坏了**，★ 不是文档的问题）")
	}

	mut := append([]string(nil), lines...)
	mut[targetLine-1] = regStripIDFromFirstCell(mut[targetLine-1])
	if mut[targetLine-1] == lines[targetLine-1] {
		t.Fatalf("★ 注入没有改变任何东西 ⇒ 夹具构造错了")
	}

	// ③ 语义有效性：★ 摘完之后首格**确实**没有编号了、★ 而 § 标签**还在**
	newCells := splitRowCells(mut[targetLine-1])
	if len(newCells) == 0 {
		t.Fatalf("★ 注入后不是表格行")
	}
	if len(extractRegIDs(newCells[0])) != 0 {
		t.Fatalf("★★ **弱变异**：★ 注入后首格仍含编号 ⇒ 语义没变 ⇒ 门测不出东西")
	}
	if len(regSecRefs(newCells[0])) == 0 {
		t.Fatalf("★★ **弱变异**：★ 注入后首格连 § 标签都没了 ⇒ 测的不是 C10")
	}

	// ④ 门必须抓到
	vs := regC10Violations(mut, regCanonical(mut))
	if len(vs) == 0 {
		t.Fatalf("★★ **探测器坏了**：★ 合成夹具（摘掉首格的编号）**没有**被 C10 抓住")
	}
	found := false
	for _, v := range vs {
		if v.Line == targetLine {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("★★ C10 报出了违例，但**没有点名**注入的那一行 L%d（★ 报的是：%v）", targetLine, vs)
	}
}

// regStripIDFromFirstCell 把一行表格行的**首格**里的编号摘掉（★ 用于合成夹具）。
//
// ★ ★★ 摘的是**带粗体标记**的整段（★ `**F24**` ⇒ 摘成空）⇒ ★ 不会留下 `****` 这种垃圾。
func regStripIDFromFirstCell(line string) string {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "|") || !strings.HasSuffix(trimmed, "|") {
		return line
	}
	inner := trimmed[1 : len(trimmed)-1]
	cells := strings.Split(inner, "|")
	if len(cells) == 0 {
		return line
	}
	first := cells[0]
	ids := extractRegIDs(first)
	if len(ids) == 0 {
		return line
	}
	for _, id := range ids {
		first = strings.ReplaceAll(first, "**"+id+"**", "")
		first = strings.ReplaceAll(first, id, "")
	}
	cells[0] = first
	return "|" + strings.Join(cells, "|") + "|"
}

// stripAllClosedMarkers 把一行里的闭合标记全部摘掉（★ 用于合成夹具）。
func stripAllClosedMarkers(line string) string {
	out := line
	for _, m := range regClosedMarkers {
		out = strings.ReplaceAll(out, m, "")
	}
	return out
}

// TestAuditRegistryTableSyncCoversWholeDoc 是 **C7** 的**牙齿检验**
// （★ 第三十七轮新增）。
//
// ★ C7 的主张：★ **整档**出现的每个登记项编号，都必须在规范源里有唯一一行
// ⇒ ⇒ ★ 「缺登记」这一半（★ 第三十六轮教训：★ 缺登记是过期登记的另一半）
// 从此**有机械护栏**，★ 而不再只靠「人工复核」。
//
// ★ 三步（★ 顺序不能换）：
//
//	① **反向对照**：★ 真实文档 ⇒ 0 条违例（门不是恒红）
//	② **合成夹具**：★ 在**内存副本**上让规范源某一行的依据里出现一个**没有登记**的编号
//	   ⇒ ★ 门**必须**报出违例、且必须**点名那个编号**
//	③ ★★ **语义有效性**：★ 注入之后那个编号确实**在整档里**、且**不在规范源里**
func TestAuditRegistryTableSyncCoversWholeDoc(t *testing.T) {
	lines := readAuditDocLines(t)

	// ① 反向对照
	if vs := checkRegistry(lines); len(vs) != 0 {
		t.Fatalf("反向对照失败：真实文档本该 0 条违例，实际 %d 条（第一条：%s）", len(vs), vs[0].String())
	}

	// ② 合成夹具：★ 挑规范源里的一行，★ 在它的依据列里塞一个**未登记**的编号
	//
	// ★★★ 第三十七轮修（★ 夹具原本是**空转**的）：★
	//
	//	★ extractRegIDs 返回的是**归一后**的 token（★ `F-97` ⇒ `F97`）
	//	★ 而原夹具拿**带连字符的原形**去比 ⇒ ★ 比对**恒不成立**
	//	★ ⇒ 「注入前不在整档」那条先验**永远通过**（★ 假通过）
	//	★ ⇒ 「注入后在整档里」那条**永远失败** ⇒ ★ 夹具**测不出任何东西**
	//	★ ⇒ ★ 而这个 bug **被假阳性挡住了**：★ 反向对照（真实文档 0 违例）
	//	★   本该先红（★ 12 条 fr-FR 假阳性）⇒ ★ ⇒ 夹具的 bug **一直没被看见**
	//	★   ⇒ ★ 这就是「★ 门要能回答『它能不能红』」的又一条实例
	//
	// ★⇒ 所以现在**原形**（写进正文）与**归一形**（供比对）必须分开。
	const ghostRaw = "F-97"
	const ghostNorm = "F97"
	canon := regCanonical(lines)
	targetLine := 0
	for _, c := range canon {
		if c.Line > targetLine {
			targetLine = c.Line
		}
	}
	if targetLine == 0 {
		t.Fatalf("★ 找不到规范源的行 ⇒ 夹具无法构造（★ 这是**门自己坏了**）")
	}
	// ★ 先验：这个编号在**注入前**既不在整档、也不在规范源里（★ 否则夹具无效）
	// ★ ★ 用**归一形**比对（★ 归一形才是抽取器吐出来的东西）
	for _, l := range lines {
		for _, id := range extractRegIDs(l) {
			if id == ghostNorm {
				t.Fatalf("★ 注入目标编号 %s（归一形 %s）在**注入前**就出现在整档里 ⇒ 夹具无效（★ 换一个）", ghostRaw, ghostNorm)
			}
		}
	}

	mut := append([]string(nil), lines...)
	mut[targetLine-1] = mut[targetLine-1] + " ★ 顺带提一句 " + ghostRaw

	// ③ 语义有效性：★ 注入后它**在整档里**、且**不在规范源里**
	foundInDoc := false
	for _, l := range mut {
		for _, id := range extractRegIDs(l) {
			if id == ghostNorm {
				foundInDoc = true
			}
		}
	}
	if !foundInDoc {
		t.Fatalf("★★ **弱变异**：★ 注入后该编号仍不在整档里 ⇒ 语义没变 ⇒ 门测不出东西")
	}
	for _, c := range regCanonical(mut) {
		if c.Key == ghostNorm {
			t.Fatalf("★★ **弱变异**：★ 注入后该编号进了规范源 ⇒ 语义没变 ⇒ 门测不出东西")
		}
	}

	// ② 门必须抓到，★ 且必须是 **C7** 那一条（★ Section == "整档"）
	vs := checkRegistry(mut)
	if len(vs) == 0 {
		t.Fatalf("★★ **探测器坏了**：★ 合成夹具（整档里出现一个未登记的编号 %s）**没有**被门抓住", ghostRaw)
	}
	hitC7 := false
	for _, v := range vs {
		if v.ID == ghostNorm && v.Section == "整档" {
			hitC7 = true
			break
		}
	}
	if !hitC7 {
		t.Fatalf("★★ 门报出了违例，但**没有**以 C7（Section=整档）点名 %s（★ 报的是：%v）", ghostRaw, vs)
	}
}

// TestAuditRegistryTableSyncLetterFamilyShape 是**字母族形状**的牙齿检验
// （★ 第三十七轮新增）。
//
// ★ 它为什么必须存在：★ 第三十七轮给 C7 加判据时，★ 门把文档里 **12 处
//
//	`fr-FR`（★ i18n 语言标签）**误认成登记项 `FR` ⇒ 门报出 12 条**假阳性**
//	⇒ ★ 那**不是**文档的缺陷，★ 是**探测器**的缺陷
//	（★ 第三十六轮教训的同构：★ 「门报红」不等于「文档有病」⇒ 先分清谁有病）
//
// ★ 所以必须**同时**证明两件事（★ 顺序不能换，★ 只证一面＝假牙）：
//
//	① **假阳性侧**：★ 散文里的 `fr-FR` / `FAIL` / `RFC3339` / `FROM` /
//	   `CONFLICT` / `BUFFERS` / `FORCE_LOCK` / `DEFAULT false` ⇒ **一个都不许**
//	   被当成登记项
//	② **假阴性侧**：★ 登记项的字母族 `F-A` … `F-E` ⇒ **五个都**必须被认出
//	   （★ 否则就是「为了消掉假阳性、★ 把真编号一起消掉了」⇒ 门会恒绿）
//	③ ★ **真实文档**：★ 五个字母族都认得出（★ 约定漂移 ⇒ 门自己红）
func TestAuditRegistryTableSyncLetterFamilyShape(t *testing.T) {
	// ① 假阳性侧：★ 这些都是**本档里真实出现过**的散文（★ 不是编造的）
	prose := []string{
		"fr-FR: 实测 1119 > 基线 1118",
		"ar-SA / de-DE / es-ES / fr-FR / ja-JP 五个语种",
		"门 **PASS=161 FAIL=0** / EXIT=0",
		"★ 契约门 PASS=11 FAIL=0 ＋ 本轮新门 PASS=10 FAIL=0",
		"显式接受 RFC3339 + PG 文本时间戳",
		"SELECT ... FROM request_log",
		"ON CONFLICT DO UPDATE",
		"（ANALYZE, BUFFERS）两条查询",
		"FORCE_LOCK_REQUESTS",
		"boolean DEFAULT false",
		"0xFA",
	}
	for _, s := range prose {
		if ids := extractRegIDs(s); len(ids) != 0 {
			t.Errorf("★★ **假阳性**：★ 散文 %q 被抽出登记项 %v（★ 应当一个都没有）", s, ids)
		}
	}

	// ② 假阴性侧：★ 五个字母族必须都认得出来（★ 归一后是 FA…FE）
	for src, want := range map[string]string{
		"F-A": "FA", "F-B": "FB", "F-C": "FC", "F-D": "FD", "F-E": "FE",
	} {
		ids := extractRegIDs("登记项 " + src + " 已闭合")
		if len(ids) != 1 || ids[0] != want {
			t.Errorf("★★ **假阴性**：★ %s 没被抽出登记项（★ 期望 %s，实际 %v）", src, want, ids)
		}
	}

	// ③ ★ 真实文档：★ 五个字母族都认得出（★ 约定漂移 ⇒ 门自己红）
	found := map[string]bool{}
	for _, l := range readAuditDocLines(t) {
		for _, id := range extractRegIDs(l) {
			found[id] = true
		}
	}
	for _, want := range []string{"FA", "FB", "FC", "FD", "FE"} {
		if !found[want] {
			t.Errorf("★★ **约定漂移**：★ 真实文档里认不出字母族 %s（★ 字母族必须写成 F-A 形态）", want)
		}
	}
}

// TestAuditRegistryTableSyncDefinitionPointBacklink 是 **C8** 的牙齿检验
// （★ 第三十八轮新增）。
//
// ★ C8 的主张：★ 正文的**定义点**（★ 某个号在正文里被「定义」的那一节）
// ★ 必须能被**规范源那一行指回** ⇒ ⇒ ★ 「**同号不同义**」（★ 撞号）从此
// ★ **有机械护栏**，★ 而不再只靠人工对拍
//
// ★ 三步（★ 顺序不能换）：
//
//	① **反向对照**：★ 真实文档 ⇒ 0 条违例（门不是恒红）
//	② **合成夹具**：★ 把规范源那一行的 § 引用改成别的节
//	  ⇒ ★ 门**必须**报出违例、且必须**点名那个编号**
//	③ ★★ **语义有效性**：★ 注入之后那个定义点确实**指不回去了**
//	  （★ 否则就是**弱变异** ⇒ 门测不出东西）
func TestAuditRegistryTableSyncDefinitionPointBacklink(t *testing.T) {
	lines := readAuditDocLines(t)

	// ① 反向对照
	if vs := checkRegistry(lines); len(vs) != 0 {
		t.Fatalf("反向对照失败：真实文档本该 0 条违例，实际 %d 条（第一条：%s）", len(vs), vs[0].String())
	}

	// ② 挑一个定义点（★ 用规范源里有 § 引用的那些，★ 否则改不动）
	canon := regCanonical(lines)
	byKey := map[string]canonEntry{}
	for _, c := range canon {
		byKey[c.Key] = c
	}
	type target struct {
		dp  regDefPoint
		id  string
		sec string
	}
	var targets []target
	for _, dp := range regDefPoints(lines) {
		for _, id := range dp.IDs {
			c, ok := byKey[id]
			if !ok {
				continue
			}
			if len(regSecRefs(c.Evidence)) == 0 {
				continue // ★ 规范源那行一个 § 引用都没有 ⇒ 夹具无从下手
			}
			targets = append(targets, target{dp: dp, id: id, sec: dp.Sec})
		}
	}
	if len(targets) == 0 {
		t.Fatalf("★ 找不到可注入的定义点 ⇒ 夹具无法构造（★ 这是**门自己坏了**，★ 不是文档的问题）")
	}
	tg := targets[0]

	// ③ 注入：★ 把规范源那一行里**覆盖该定义点**的 § 引用改掉
	canonLine := byKey[tg.id].Line
	mut := append([]string(nil), lines...)
	broken, ok := regBreakCoveringRef(mut[canonLine-1], tg.sec)
	if !ok {
		t.Fatalf("★ 注入失败：★ 规范源 L%d 里没有覆盖 §%s 的 § 引用（★ 夹具构造错了）", canonLine, tg.sec)
	}
	mut[canonLine-1] = broken

	// ③ 语义有效性：★ 注入后那个定义点确实**指不回去了**
	// ★ ★ 且那一行**仍然**有 § 引用（★ 否则就是「整行删掉」的弱变异）
	newRefs := regSecRefs(mut[canonLine-1])
	if len(newRefs) == 0 {
		t.Fatalf("★★ **弱变异**：★ 注入后规范源那行一个 § 引用都没有了 ⇒ 语义没变 ⇒ 门测不出东西")
	}
	if regSecCovered(tg.sec, newRefs) {
		t.Fatalf("★★ **弱变异**：★ 注入后 §%s 仍被覆盖 ⇒ 语义没变 ⇒ 门测不出东西", tg.sec)
	}
	// ★ ★ 且定义点本身**没动**（★ 只改了规范源那一行）
	if mut[tg.dp.Line-1] != lines[tg.dp.Line-1] {
		t.Fatalf("★★ 夹具改错了地方：★ 定义点那行被动了")
	}

	// ② 门必须抓到，★ 且必须是 **C8** 那一条（★ Section == "定义点"）
	vs := checkRegistry(mut)
	if len(vs) == 0 {
		t.Fatalf("★★ **探测器坏了**：★ 合成夹具（§%s 的定义点指不回去了）**没有**被门抓住", tg.sec)
	}
	hit := false
	for _, v := range vs {
		if v.ID == tg.id && v.Section == "定义点" {
			hit = true
			break
		}
	}
	if !hit {
		t.Fatalf("★★ 门报出了违例，但**没有**以 C8（Section=定义点）点名 %s（★ 报的是：%v）", tg.id, vs)
	}
}
