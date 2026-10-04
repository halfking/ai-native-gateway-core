// Package vendorprice — 从原厂定价页的 markdown 快照里提出**价格候选**。
//
// # 这个包的产出是「提案」，不是价格
//
// 抓取原厂定价页并自动解析出价格，是「拿到标准价」这个诉求里最诱人也最
// 危险的一步。仓里已有的 docs/02-resources/research/pricing/raw/*.md 与
// internal/vendorprice/testdata/live-*.md 是真实快照，实测它们至少有四种
// 互不兼容的形态：
//
//	anthropic(旧) / xai(旧)  「模型在行内 + 表头给列义」   —— 可可靠提取
//	anthropic(新)           「模型在表头 + 单元格自带标签」 —— 可提取，走另一条路
//	xai(新)                 双层表头 + 上下文分档          —— 必须拒绝
//	deepseek                无 markdown 表格（纯文本版面）  —— 提不出
//	MiniMax                 删除线原价 + 上下文分档          —— naive 提取必错
//	zhipu / mistral         无 $ 符号                       —— 提不出任何东西
//
// MiniMax 那一类是最要命的：`| **MiniMax-M3** ≤ 512k input tokens |
// ~~$0.60~~ $0.30 / M tokens |` 里同时有**被划掉的原价**、**折后价**和
// **上下文分档**。任何「把这一行里第一个数字当输入价」的提取器都会把
// 划掉的原价 $0.60 写进账本。
//
// 所以本包的契约是：**宁可少提，不可提错**。
//   - 只在「表头可映射 + 无删除线 + 无上下文分档 + 单位确实是每 1M token」时
//     给出 confidence=table_row 的候选；
//   - 遇到划线、分档、列义不明、**单位不是 token**，一律给出
//     confidence=unusable 并把原因写进 Warnings，让它出现在提案里被人看见，
//     而不是静默消失；
//   - **表里出现的每一个金额都必须对应到一条候选**（见 extract_test.go 的
//     TestNoMoneyEverDisappears）。静默丢弃的形态是「某个模型从提案里凭空
//     消失」，而提案的全部价值就在于它列出了我们看见的所有东西；
//   - 全程不写任何价格表。写 SSOT 的唯一路径是 bg/pricing_baseline_sync.go
//     的 SyncBaselinePricesToDB，而它的输入是那份人确认过的 SSOT。
package vendorprice

import (
	"regexp"
	"strconv"
	"strings"
)

// 置信度分级。Proposal 只会给出这三档。
const (
	// ConfidenceTableRow：行内模型名可读、列义由表头唯一确定、无划线、
	// 无上下文分档、单位是每 1M token。可以直接进人确认队列。
	ConfidenceTableRow = "table_row"
	// ConfidenceUnattributed：抓到了价格数字，但无法确定它属于哪个模型。
	// 数字留着，人来判断。
	ConfidenceUnattributed = "unattributed"
	// ConfidenceUnusable：这一行有明确的失效原因（划线原价 / 上下文分档 /
	// 列义不明 / 单位不是每 1M token）。数字**不得**使用。
	ConfidenceUnusable = "unusable"
)

// 计费单位。
//
// **只有 UnitPer1M 会被写进 SSOT。** 其余单位是「另一种产品」的价格：
// 一张图、一秒视频、一通电话的价与「每 1M token」的价不是同一个量纲，
// 填进同一列会让成本核算偏差几个数量级。单位必须由单元格里**写明的**内容
// 判定，而不是假设整页都在讲 token —— 实测 xAI 的同一页里，chat 段是
// 「per 1M tokens」而图像段是「$0.002 / img」，两者共用一个页眉。
const (
	UnitPer1M      = "per_1m"       // 每 1M token（token 计费的唯一可存储单位）
	UnitPerImage   = "per_image"    // 每张图
	UnitPerSecond  = "per_second"   // 每秒视频/音频
	UnitPerMinute  = "per_minute"   // 每分钟通话
	UnitPerHour    = "per_hour"     // 每小时
	UnitPer1KCalls = "per_1k_calls" // 每 1k 次工具调用
	UnitPerChar    = "per_char"     // 每字符（TTS）
	UnitPerMessage = "per_message"  // 每条消息
	UnitUnknown    = ""             // 页面与单元格都没说
)

// ColumnRole 是表头列的语义。
type ColumnRole string

const (
	RoleModel      ColumnRole = "model"
	RoleInput      ColumnRole = "input"
	RoleOutput     ColumnRole = "output"
	RoleCacheRead  ColumnRole = "cache_read"
	RoleCacheWrite ColumnRole = "cache_write"
	RoleOther      ColumnRole = "other"
)

// Candidate 是一条价格候选。
//
// Row 与 LineNo 必须留着：判读一个人工提案的唯一办法就是回去看原页面的
// 那一行，把它们删掉等于让提案不可核对。
type Candidate struct {
	Vendor    string   `json:"vendor"`
	Model     string   `json:"model,omitempty"`
	Input     *float64 `json:"input_per_1m,omitempty"`
	Output    *float64 `json:"output_per_1m,omitempty"`
	CacheRead *float64 `json:"cache_read_per_1m,omitempty"`
	CacheWrit *float64 `json:"cache_write_per_1m,omitempty"`
	Currency  string   `json:"currency,omitempty"`
	// Unit 是**这一行实际声明的**单位。历史版本把它硬编码成 "per_1m"，
	// 于是 `$0.002 / img`（每张图）也顶着 per_1m 的标签往下走 —— 见
	// TestPerImagePriceIsNotTaggedAsPerMillionTokens。
	Unit string `json:"unit,omitempty"`
	// Description 是模型名后面粘着的说明文字（"Text, Image → Image"）。
	// 它不进任何计算，但人核对提案时靠它分清「这个价是哪个产品的」。
	Description string   `json:"description,omitempty"`
	SourceURL   string   `json:"source_url"`
	Confidence  string   `json:"confidence"`
	Warnings    []string `json:"warnings,omitempty"`
	Row         string   `json:"row"`
	LineNo      int      `json:"line_no"`
	Columns     []string `json:"-"`
}

// ---------------------------------------------------------------------------
// 正则
// ---------------------------------------------------------------------------

// markdownRowRE 匹配一行 markdown 表格行。
var markdownRowRE = regexp.MustCompile(`^\s*\|.*\|\s*$`)

// separatorRowRE 匹配 markdown 表格的分隔行（| --- | --- |）。
var separatorRowRE = regexp.MustCompile(`^\s*\|(\s*:?-{2,}:?\s*\|)+\s*$`)

// moneyRE 匹配一个金额：$1.25 / $10 / €0.5 / 1.25（无符号时由列义兜底）。
var moneyRE = regexp.MustCompile(`[$€£¥]?\s*([0-9]+(?:\.[0-9]+)?)`)

// currencySymbolRE 判断一行/一格是否含有货币符号。含符号的表格行是
// 「有金额的行」——TestNoMoneyEverDisappears 用它证明没有金额被吞掉。
var currencySymbolRE = regexp.MustCompile(`[$€£¥]`)

// struckRE 匹配 markdown 删除线。
var struckRE = regexp.MustCompile(`~~`)

// tierQualifierRE 匹配上下文分档限定语。分档价不是标准价：清单要的是
// 「每 1M token 的挂牌价」，而这一行的价格只在 >512k 时成立。
var tierQualifierRE = regexp.MustCompile(`(?i)(>\s*\d+\s*k|≥\s*\d+\s*k|over\s*\d+|context[_ ]over|per\s+token|tier)`)

// dimensionRE 匹配**非标准计费维度**的措辞。
//
// 实测（anthropic.md，2026-06-12 快照）：同一个 Claude Opus 4.8 在同一页
// 出现三次、价各不相同，三份都长得像「一张干净的定价表」：
//
//	:161  | Base Input Tokens | ... | Output Tokens |   → $5  / $25   真正的挂牌价
//	:297  | Input | Output |                             → $10 / $50   Fast mode（premium）
//	:314  | Batch input | Batch output |                 → $2.50/$12.50 Batch API（5 折）
//
// 只防删除线与分档是不够的：这三行没有一条带删除线或分档词。维度信号在
// **表头**（Batch input/output）与**表前的散文**（"Fast mode pricing"、
// "with a 50% discount"）里。漏掉它的形态是：一个模型拿到三份「已核实」的
// 基准价，SSOT 的键又是 canonical 名——三份会互相覆盖或取其一，而看提案
// 的人看不出它们其实是三种产品。
//
// # 这张表里装的是「处理模式」，不是「计费对象」
//
// 曾经的版本还包含 `cached input` 与 `tool use`，两个都已被移除，理由是
// 它们描述的是**被计费的东西**而不是**哪种产品档位**，而后者才是会让
// 「标准价」这个概念失效的东西：缓存读价同样是每 1M token 的价，填进基准
// 价的 cache_read 列是正确且有用的。
//
// 留着它们的代价是实测出来的：anthropic 的 models overview 页在正文里写
// 「All current models support text and image input, …, vision, and tool use」，
// `tool use` 一命中，那张表里**唯一正确的四个价格**整表被判 unavailable。
// 一个防御词把一页最好的数据挡在门外，是「假牙」而不是「有牙」。
//
// 短词一律带 `\b`：不加词边界时 `tier` 会匹配 "fron**tier**"——实测正是
// 它把 anthropic 新页整表判死（表头里的 "near-frontier intelligence"）。
var dimensionRE = regexp.MustCompile(`(?i)(batch|fast[\s-]?mode|\bflex\b|\bpriority\b|regional|premium pricing|standard processing|short[\s-]?context|long[\s-]?context)`)

// headerDimensionRE 是**表头专用**的维度词表，比 dimensionRE 多收
// `tier`（表头里的档位名本身），其余同源。
//
// 它存在的理由与 dimensionRE 分家时一样：表头还要额外挡住**档位名**这种
// 只在表头出现、行内完全没有信号的形态。实测 xAI 新页的表头
// `| Model | Context | Short context | Long context |` 就是被这一条拦下的
// ——而且拦得对：那张表的每一行都同时给出短上下文与长上下文两套价，而
// SSOT 的键只有 canonical 名一个，装不下两套价。**即使把双层表头猜对了，
// 取哪一套也是运营决策，不是解析器该替人做的选择。**
var headerDimensionRE = regexp.MustCompile(`(?i)(batch|fast[\s-]?mode|\bflex\b|\bpriority\b|regional|premium|short[\s-]?context|long[\s-]?context|\btier(s|ed|ing)?\b|standard processing)`)

// perMillionRE 匹配「每 1M token」的单位声明（页眉或单元格里都算）。
var perMillionRE = regexp.MustCompile(`(?i)(per\s*1m|/\s*mtok|per\s*m\s*token|1m\s*(input|output|token)|每\s*1\s*[mM]|元\s*/\s*[mM])`)

// markdownLinkRE 把 [text](url) 还原成 text。
var markdownLinkRE = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)

// leadingLinkRE 只匹配单元格里**开头**那个 markdown 链接。
//
// 「取链接文本、丢掉后面的说明」这件事本身就是个陷阱：`| [grok-imagine-image](url)
// Text, Image → Image |` 里链接文本是模型名，后面粘的是模态说明；直接
// 整体 cleanCell 会得到 "grok-imagine-image Text, Image → Image"，这个名字
// 在 canonical 清单里永远解析不出来，于是人看到的是「解析失败」，而真正
// 的原因是提取器把两段文本焊在了一起。
var leadingLinkRE = regexp.MustCompile(`^\s*\[([^\]]+)\]\([^)]*\)`)

// markdownEmphRE 去掉 **bold** / *italic* 标记。
var markdownEmphRE = regexp.MustCompile(`\*{1,3}`)

// unitPatterns 把单元格里写明的单位映射成 Unit。
//
// 顺序即优先级，**先具体后笼统**："$15.00 / 1M chars" 必须先被 per_char
// 吃掉，否则笼统的 `/\s*1m\b` 会把它记成每 1M token —— 一个字符与一个
// token 差了三个数量级。
var unitPatterns = []struct {
	re   *regexp.Regexp
	unit string
}{
	{regexp.MustCompile(`(?i)/\s*1k\s*(?:api\s*)?calls?\b`), UnitPer1KCalls},
	{regexp.MustCompile(`(?i)/\s*1\s*m\s*chars?\b`), UnitPerChar},
	{regexp.MustCompile(`(?i)/\s*(?:ms|msec|milliseconds?)\b`), UnitUnknown},
	{regexp.MustCompile(`(?i)/\s*(?:s|sec|secs|second|seconds)\b`), UnitPerSecond},
	{regexp.MustCompile(`(?i)/\s*(?:min|mins|minute|minutes)\b`), UnitPerMinute},
	{regexp.MustCompile(`(?i)/\s*(?:h|hr|hrs|hour|hours)\b`), UnitPerHour},
	{regexp.MustCompile(`(?i)/\s*(?:img|image|images)\b`), UnitPerImage},
	{regexp.MustCompile(`(?i)/\s*(?:msg|messages?|message)\b`), UnitPerMessage},
	{regexp.MustCompile(`(?i)(/\s*1\s*m\b|/\s*mtok\b|/\s*m\s*tokens?\b|per\s*1m|per\s*million\s*tokens?|每\s*1\s*[mM])`), UnitPer1M},
}

// labelledPriceRE 匹配**自带标签**的金额：金额后面直接跟维度名与 token 单位。
//
// 形如 `$10 / input MTok$50 / output MTok` —— 整个单元格里挤着输入价与输出价，
// 列语义由文本自己给出，不需要表头。实测 anthropic 的「Compare models」页
// 就是这个形态：模型名在**表头**，价格挤在一个单元格里。
var labelledPriceRE = regexp.MustCompile(`(?i)([$€£¥]\s*[0-9]+(?:\.[0-9]+)?)\s*(?:/|per)\s*(cached\s+input|cache\s+writes?|cache\s+reads?|cache\s+hits?|input|output)[\s-]*(?:1\s*m\s*tokens?|m\s*tokens?|mtok)`)

// ---------------------------------------------------------------------------
// 列义
// ---------------------------------------------------------------------------

// RoleFromHeader 把一个表头单元格映射成语义列。
//
// 判定顺序有讲究：「cache」与「input/output」会同时出现（"Cached input"、
// "5m Cache Writes"），所以先判 cache，再判 input/output。
func RoleFromHeader(header string) ColumnRole {
	h := strings.ToLower(strings.TrimSpace(header))
	if h == "" {
		return RoleOther
	}
	if strings.Contains(h, "model") {
		return RoleModel
	}
	hasCache := strings.Contains(h, "cach")
	if hasCache {
		// write 必须在 read 之前判：anthropic 同时有 "5m Cache Writes" 与
		// "1h Cache Writes" 两列，判错就等于把一种缓存价当另一种。
		if strings.Contains(h, "writ") {
			return RoleCacheWrite
		}
		if strings.Contains(h, "read") || strings.Contains(h, "hit") || strings.Contains(h, "refresh") {
			return RoleCacheRead
		}
		// "Cached input"（xAI）既没有 read/hit/refresh 也没有 write。
		// 缓存**读**价才是它要的语义——落到默认的 cache_write 会把一个
		// $0.20 的缓存读价记成缓存写价，两者在成本核算里方向相反。
		if strings.Contains(h, "input") || strings.Contains(h, "prompt") {
			return RoleCacheRead
		}
		if strings.Contains(h, "output") {
			return RoleCacheWrite
		}
		// 裸 "Cached"（xAI 新页第二层表头 `| Input | Cached | Output |`）
		// 连方向词都没有。判 RoleOther 看着安全，实际代价是**整张表读不出来**：
		// 第二层表头认不出，它就退化成一行「列数对不上」，而真正的信息
		// （这是双层表头，表头按上下文分档）全丢了。
		//
		// 判 read 的理由：缓存**写**在各家页面里几乎总是带限定词
		//（"5m Cache Writes"、"Cache writes"），而 "Cached" 是分词
		// "已缓存"，语义上就是读侧。缓存写价在同一行里另有其列时，
		// 那一列会先被 writ 分支接住。
		return RoleCacheRead
	}
	if strings.Contains(h, "input") {
		return RoleInput
	}
	if strings.Contains(h, "output") {
		return RoleOutput
	}
	return RoleOther
}

// ---------------------------------------------------------------------------
// 单元格
// ---------------------------------------------------------------------------

// splitTableRow 把一行 markdown 表格切成单元格。
func splitTableRow(line string) []string {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimPrefix(trimmed, "|")
	trimmed = strings.TrimSuffix(trimmed, "|")
	parts := strings.Split(trimmed, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// cleanCell 去掉 markdown 链接与强调标记，取出可读文本。
func cleanCell(cell string) string {
	cell = markdownLinkRE.ReplaceAllString(cell, "$1")
	cell = markdownEmphRE.ReplaceAllString(cell, "")
	return strings.TrimSpace(cell)
}

// splitIdent 把单元格拆成「标识」与「说明」。
//
// 原厂页面几乎都会把模型名做成指向该模型文档的链接，然后在链接后面粘一句
// 说明（"For demanding reasoning…"、"Text, Image → Image"）。链接文本才是
// 标识，后面那段是人看的。分不开的结果是名字多出尾巴，canonical 解析永远
// 失败，而失败原因看起来是「模型不在清单里」。
func splitIdent(cell string) (ident, description string) {
	if m := leadingLinkRE.FindStringSubmatch(cell); m != nil {
		return cleanCell(m[1]), cleanCell(strings.TrimPrefix(cell, m[0]))
	}
	return cleanCell(cell), ""
}

// cellUnit 返回单元格里**写明的**单位；没写明就返回 ""。
//
// 这里刻意不做「翻页找默认单位」的推断：默认值由调用方按
// 显式单位 > 页面单位 的顺序决定，而显式单位永远赢。
func cellUnit(cell string) string {
	for _, p := range unitPatterns {
		if p.re.MatchString(cell) {
			return p.unit
		}
	}
	return UnitUnknown
}

// parseMoney 从单元格里取金额。要求同时出现货币符号与「每 1M」单位，
// 否则返回 nil —— 半截数字比没有数字更危险。
func parseMoney(cell string, unitKnown bool) *float64 {
	if !currencySymbolRE.MatchString(cell) {
		return nil
	}
	if !unitKnown && !perMillionRE.MatchString(cell) {
		return nil
	}
	m := moneyRE.FindStringSubmatch(cell)
	if m == nil {
		return nil
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return nil
	}
	return &v
}

// currencyOf 从一行里认货币符号。认不出返回 ""——币种未知比猜错好，
// 因为跨币种比较会造出纯噪声的「偏差」。
func currencyOf(line string) string {
	for sym, code := range map[string]string{
		"$": "USD", "€": "EUR", "£": "GBP", "¥": "CNY",
	} {
		if strings.Contains(line, sym) {
			return code
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// 表格分块
// ---------------------------------------------------------------------------

// rawRow 是一行数据行的原文与行号。
type rawRow struct {
	raw    string
	lineNo int
}

// tableBlock 是一张 markdown 表：表头 + 数据行 + 表前的散文。
type tableBlock struct {
	headerRaw   string
	headerCells []string
	headerLine  int // 1-based
	rows        []rawRow
	prose       string
}

// Extract 从一份原厂页面 markdown 快照里提出候选。
//
// 结构是「先分块、再逐块判定」：一张表 = 一个**由紧随分隔行确认的**表头
// 加上其后连续的表格行。分块带来两个此前拿不到的性质：
//
//  1. 表头不可能泄漏到段落之后（块边界就是表边界）；
//  2. **块外的表格行一定会被报出来**，而不是被当成「看起来像表头」吞掉。
//     后者是真实的漏价形态：anthropic 新页里价格挤在
//     `|  | $10 / input MTok$50 / output MTok | … |` 这一行，它同时含有
//     "input" 与 "output" 两个词，在旧实现里会被登记成候选表头；紧随其后的
//     分隔行并不存在，于是它作为「表头」永远不会被确认，也永远不会被报出来。
//     实测结果是整个 anthropic 新页**一条价格候选都出不来**，而且看不出是
//     丢了——因为提案里其他行都好好地报着。
//
// vendor 与 sourceURL 只是原样带进提案，供人回溯；本函数不做任何网络
// 访问——抓取与解析分开，才可能对着历史快照测解析。
func Extract(vendor, sourceURL string, markdown []byte) []Candidate {
	lines := strings.Split(string(markdown), "\n")
	var out []Candidate

	pageUnit := UnitUnknown
	if perMillionRE.MatchString(string(markdown)) {
		pageUnit = UnitPer1M
	}

	// prose 累积「上一张表结束到本行之间」的非表格文本。维度措辞
	//（"Fast mode pricing" / "with a 50% discount"）几乎总是写在表前的散文里
	// 而不是表头里。
	var prose strings.Builder

	i := 0
	for i < len(lines) {
		line := lines[i]
		if !markdownRowRE.MatchString(line) {
			// 非表格行：如果它自己声明了「每 1M」，那是单位上下文更新。
			if perMillionRE.MatchString(line) {
				pageUnit = UnitPer1M
			} else if strings.TrimSpace(line) != "" {
				prose.WriteString(line)
				prose.WriteString(" ")
			}
			i++
			continue
		}

		// markdown 规定表头**必须**由紧随的分隔行确认。没有这条前提，
		// 段落之后的第一行数据会被当成表头吞掉。
		if !separatorRowRE.MatchString(line) && i+1 < len(lines) && separatorRowRE.MatchString(lines[i+1]) {
			block := tableBlock{
				headerRaw:   line,
				headerCells: splitTableRow(line),
				headerLine:  i + 1,
				prose:       prose.String(),
			}
			prose.Reset()
			i += 2
			for i < len(lines) && markdownRowRE.MatchString(lines[i]) {
				if !separatorRowRE.MatchString(lines[i]) {
					block.rows = append(block.rows, rawRow{raw: lines[i], lineNo: i + 1})
				}
				i++
			}
			out = append(out, extractBlock(vendor, sourceURL, block, pageUnit)...)
			continue
		}

		// 块外孤儿行：**必须报出来**。这是「钱去哪了」的唯一线索。
		out = append(out, orphanCandidate(vendor, sourceURL, line, i+1))
		i++
	}
	return out
}

// orphanCandidate 报出一个不在任何已确认表块里的表格行。
func orphanCandidate(vendor, sourceURL, line string, lineNo int) Candidate {
	c := Candidate{
		Vendor: vendor, SourceURL: sourceURL, LineNo: lineNo, Row: line,
		Confidence: ConfidenceUnusable,
		Warnings: []string{"table row outside any header-confirmed table — " +
			"no column semantics to attribute these values to"},
	}
	if currencySymbolRE.MatchString(line) {
		c.Warnings = append(c.Warnings,
			"this row carries money but was not parsed: either the page has no markdown "+
				"table around it, or the layout is one this extractor does not model — "+
				"the amounts below are NOT accounted for anywhere in this proposal")
		c.Currency = currencyOf(line)
		c.Unit = cellUnit(line)
	}
	return c
}

// headerRole 判断一个表头单元格是什么列义。
//
// 必须先 splitIdent 再判，**不能直接对整格判**。实测 anthropic 新页的表头
// 里有一格是 `[Claude Haiku 4.5](…)The fastest model with near-frontier
// intelligence` ——链接后面粘的说明里含 "model"，整格判下来它是 model 列，
// 于是整张表被判成「不是列向表」，四个价格一个都没提出来。这正是
// splitIdent 存在的理由：链接文本是标识，后面那段是人看的，判列义时后者
// 根本不该参与。
func headerRole(cell string) ColumnRole {
	ident, _ := splitIdent(cell)
	return RoleFromHeader(ident)
}

// isColumnOriented 判断一张表是不是「模型在表头」的形态。
//
// 判据是两条同时成立，缺一不可：
//
//	① 至少两个表头单元格（不含第 0 列）是 markdown 链接且带非空文本 ——
//	   原厂页面给模型名做链接是稳定习惯，这是「这是一批模型名」的客观证据；
//	② 表头里**没有任何**价格列义或 model 列 —— 否则列义已经由表头确定了，
//	   按行读才对，强行按列读会把它读反。
//
// 实测：anthropic 新页 ①（4 个模型链接）②（表头只有 "Feature" + 模型名）
// 同时成立 ⇒ 按列读；xAI 新旧两页 ② 都不成立（有 "Model" 与 "Context" 列）
// ⇒ 按行读。
func isColumnOriented(hc []string) bool {
	links := 0
	for j, c := range hc {
		if headerRole(c) != RoleOther {
			return false
		}
		if j == 0 {
			continue
		}
		if _, ok := linkIdent(c); ok {
			links++
		}
	}
	return links >= 2
}

// linkIdent 在单元格里取链接文本。
func linkIdent(cell string) (string, bool) {
	m := leadingLinkRE.FindStringSubmatch(cell)
	if m == nil {
		return "", false
	}
	name := cleanCell(m[1])
	if name == "" {
		return "", false
	}
	return name, true
}

// looksLikeSubHeader 判断一行是不是「第二层表头」。
//
// 实测 xAI 新页：
//
//	| Model | Context | Short context | Long context |     ← 第一层：档位分组
//	| ---  | ---      | ---           | ---          |
//	| Input | Cached  | Output        | Input ...    |     ← 第二层：维度名
//
// 第一层给出的是**档位**（短/长上下文），第二层才给出维度。数据行 8 列、
// 第一层表头 4 列，于是旧实现只能报一句「column count 8 does not match the
// header's 4」——技术上准确，但对看提案的人毫无用处：他得自己打开页面，
// 数一遍列，才知道这里有个没被建模的第二层表头。
//
// 判据：列数与第一层表头不一致，**且**该行每一格都能映射成价格维度。
// 一行全是 "Input/Cached/Output" 而没有模型名，它就不是数据。
func looksLikeSubHeader(row rawRow) bool {
	cells := splitTableRow(row.raw)
	if len(cells) == 0 {
		return false
	}
	priced := 0
	for _, c := range cells {
		switch RoleFromHeader(cleanCell(c)) {
		case RoleInput, RoleOutput, RoleCacheRead, RoleCacheWrite:
			priced++
		}
	}
	return priced == len(cells)
}

// extractBlock 判定一张表的形态并逐行提出候选。
func extractBlock(vendor, sourceURL string, b tableBlock, pageUnit string) []Candidate {
	var out []Candidate
	if len(b.rows) == 0 {
		return out
	}
	if isColumnOriented(b.headerCells) {
		return extractColumnOriented(vendor, sourceURL, b, pageUnit)
	}

	roles := make([]ColumnRole, len(b.headerCells))
	for j, c := range b.headerCells {
		roles[j] = headerRole(c)
	}
	headerDim := headerDimensionRE.FindString(b.headerRaw)

	// 第二层表头检测：第一行数据若是「整行都是维度名」，它就是第二层表头，
	// 本提取器不建模这种版式。先把它单独报出来，让人一眼看出「这里是两层
	// 表头」而不是「这里的列数对不上」。
	subHeaderLine := 0
	if len(b.rows) > 0 && len(splitTableRow(b.rows[0].raw)) != len(roles) && looksLikeSubHeader(b.rows[0]) {
		subHeaderLine = b.rows[0].lineNo
		out = append(out, Candidate{
			Vendor: vendor, SourceURL: sourceURL, LineNo: b.rows[0].lineNo, Row: b.rows[0].raw,
			Confidence: ConfidenceUnusable,
			Warnings: []string{"this row is a second header row: every cell is a price " +
				"dimension, so the real column semantics live across two header rows — " +
				"a multi-level header this extractor deliberately does not guess at"},
		})
		if headerDim != "" {
			out[len(out)-1].Warnings = append(out[len(out)-1].Warnings,
				"and the top header groups those columns by billing dimension ("+
					strings.TrimSpace(headerDim)+"), so the prices are conditional, not a flat list price")
		}
	}

	for _, r := range b.rows {
		if r.lineNo == subHeaderLine {
			continue
		}
		cells := splitTableRow(r.raw)
		if len(cells) != len(roles) {
			w := []string{"column count " + strconv.Itoa(len(cells)) +
				" does not match the header's " + strconv.Itoa(len(roles))}
			if subHeaderLine > 0 {
				w = append(w,
					"this table has a second header row at line "+strconv.Itoa(subHeaderLine)+
						" that carries the real column semantics")
			}
			if headerDim != "" {
				w = append(w,
					"and the header names a billing dimension ("+strings.TrimSpace(headerDim)+
						") — these prices are conditional, not a flat list price")
			}
			out = append(out, Candidate{
				Vendor: vendor, SourceURL: sourceURL, LineNo: r.lineNo, Row: r.raw,
				Confidence: ConfidenceUnusable, Warnings: w,
			})
			continue
		}
		out = append(out, buildCandidate(vendor, sourceURL, r.raw, r.lineNo, cells, roles,
			pageUnit, b.headerLine, b.prose, headerDim))
	}
	return out
}

// extractColumnOriented 处理「模型在表头、价格在单元格」的那张表。
func extractColumnOriented(vendor, sourceURL string, b tableBlock, pageUnit string) []Candidate {
	var out []Candidate
	names := make([]string, len(b.headerCells))
	for j, c := range b.headerCells {
		if n, ok := linkIdent(c); ok {
			names[j] = n
		}
	}
	for _, r := range b.rows {
		cells := splitTableRow(r.raw)
		for j, cell := range cells {
			if j >= len(names) || names[j] == "" {
				continue
			}
			if !currencySymbolRE.MatchString(cell) {
				// 没有金额的列（"Slower" / "1M tokens" / "Jun 2026"）不构成
				// 提案内容。契约要求的是「金额不被吞」，不是「每个格子都报」。
				continue
			}
			out = append(out, buildColumnCandidate(vendor, sourceURL, r.raw, r.lineNo,
				names[j], cell, pageUnit, b.headerLine, b.prose, headerDimensionRE.FindString(b.headerRaw)))
		}
	}
	return out
}

// buildColumnCandidate 从一个自带标签的单元格里造候选。
func buildColumnCandidate(vendor, sourceURL, rawRowText string, lineNo int, model, cell, pageUnit string, headerLine int, sectionProse, headerDim string) Candidate {
	c := Candidate{
		Vendor: vendor, SourceURL: sourceURL, LineNo: lineNo, Row: rawRowText,
		Model: model, Currency: currencyOf(cell), Unit: cellUnit(cell),
	}
	if c.Unit == UnitUnknown {
		c.Unit = pageUnit
	}

	matches := labelledPriceRE.FindAllStringSubmatchIndex(cell, -1)
	consumed := make([]bool, len(cell))
	for _, m := range matches {
		amount := cell[m[2]:m[3]]
		label := strings.ToLower(strings.ReplaceAll(cell[m[4]:m[5]], " ", " "))
		v := moneyRE.FindStringSubmatch(amount)
		if v == nil {
			continue
		}
		f, err := strconv.ParseFloat(v[1], 64)
		if err != nil {
			continue
		}
		pv := f
		switch {
		case strings.Contains(label, "input") && !strings.Contains(label, "cached"):
			if c.Input == nil {
				c.Input = &pv
			}
		case strings.Contains(label, "output"):
			if c.Output == nil {
				c.Output = &pv
			}
		case strings.Contains(label, "cached") && strings.Contains(label, "input"):
			if c.CacheRead == nil {
				c.CacheRead = &pv
			}
		case strings.Contains(label, "writ"):
			if c.CacheWrit == nil {
				c.CacheWrit = &pv
			}
		case strings.Contains(label, "read") || strings.Contains(label, "hit"):
			if c.CacheRead == nil {
				c.CacheRead = &pv
			}
		}
		for k := m[0]; k < m[1]; k++ {
			consumed[k] = true
		}
	}

	// 单元格里还有没被归因的金额 ⇒ 不能只报已归因的那几个。形态是
	// 「$0.01 / sec$0.002 / img」这类把两笔不同计费的价塞进一格：报出
	// 第一个而对第二个只字不提，读提案的人会以为这就是全部。
	var leftover strings.Builder
	for k, r := range cell {
		if !consumed[k] {
			leftover.WriteRune(r)
		}
	}
	if currencySymbolRE.MatchString(leftover.String()) && moneyRE.MatchString(leftover.String()) {
		c.Confidence = ConfidenceUnusable
		c.Warnings = append(c.Warnings,
			"cell carries money beyond the labelled input/output amounts ("+
				leftover.String()+") — attributing only part of a cell would hide the rest")
	}

	markUnusable(&c, cell, pageUnit, headerLine, sectionProse, headerDim)

	if c.Confidence == "" {
		if c.Input == nil && c.Output == nil {
			c.Confidence = ConfidenceUnusable
			c.Warnings = append(c.Warnings,
				"no input or output amount found in this cell's labels")
		} else {
			c.Confidence = ConfidenceTableRow
		}
	}
	return c
}

// buildCandidate 把一行数据行变成候选，并按失效原因定级。
func buildCandidate(vendor, sourceURL, line string, lineNo int, cells []string, header []ColumnRole, pageUnit string, headerLine int, sectionProse, headerDim string) Candidate {
	c := Candidate{
		Vendor: vendor, SourceURL: sourceURL, LineNo: lineNo, Row: line,
		Currency: currencyOf(line), Unit: UnitUnknown,
	}

	markUnusable(&c, line, pageUnit, headerLine, sectionProse, headerDim)

	// 表头里必须有明确的 input 与 output 列，否则这一行不能定位到
	// 「某模型的输入价/输出价」——这是最根本的可归属性要求。
	hasInput, hasOutput := false, false
	for _, r := range header {
		switch r {
		case RoleInput:
			hasInput = true
		case RoleOutput:
			hasOutput = true
		}
	}
	if !hasInput || !hasOutput {
		c.Confidence = ConfidenceUnusable
		c.Warnings = append(c.Warnings,
			"header does not map to both an input and an output column (header line "+
				strconv.Itoa(headerLine)+")")
	}

	if len(cells) > 0 {
		c.Model, c.Description = splitIdent(cells[0])
	}
	if c.Model == "" {
		c.Confidence = ConfidenceUnattributed
		c.Warnings = append(c.Warnings, "first column does not yield a model name")
	}

	extra := 0
	for j, role := range header {
		if j >= len(cells) {
			break
		}
		cell := cells[j]
		// 单元格里写明了非 token 单位 ⇒ 这一列根本不是 token 价，跳过。
		if cu := cellUnit(cell); cu != UnitUnknown && cu != UnitPer1M {
			continue
		}
		// 「256k」这类上下文列不是钱：它没有货币符号，parseMoney 会拒。
		v := parseMoney(cell, pageUnit == UnitPer1M)
		if v == nil {
			continue
		}
		before := countPrices(&c)
		switch role {
		case RoleInput:
			if c.Input == nil {
				c.Input = v
			}
		case RoleOutput:
			if c.Output == nil {
				c.Output = v
			}
		case RoleCacheRead:
			if c.CacheRead == nil {
				c.CacheRead = v
			}
		case RoleCacheWrite:
			if c.CacheWrit == nil {
				c.CacheWrit = v
			}
		default:
			continue
		}
		if countPrices(&c) == before {
			extra++
		}
	}
	if extra > 0 {
		// anthropic 的表里同时有 "5m Cache Writes" 与 "1h Cache Writes"，
		// 我们只取第一个。**第二个必须说话**——否则提案里那个价看起来像
		// 就是全部，而 1h 缓存写价与 5m 相差数倍。
		c.Warnings = append(c.Warnings,
			"row has "+strconv.Itoa(extra)+" extra priced column(s) of the same role "+
				"that were not taken (only the first is kept); check the page for the others")
	}
	if c.Unit == UnitUnknown {
		c.Unit = pageUnit
	}

	if c.Confidence == "" {
		if c.Input == nil && c.Output == nil {
			c.Confidence = ConfidenceUnusable
			c.Warnings = append(c.Warnings, "no input or output price found in the mapped columns")
		} else {
			c.Confidence = ConfidenceTableRow
		}
	}
	if c.Model == "" && c.Confidence == ConfidenceTableRow {
		c.Confidence = ConfidenceUnattributed
	}
	return c
}

func countPrices(c *Candidate) int {
	n := 0
	for _, p := range []*float64{c.Input, c.Output, c.CacheRead, c.CacheWrit} {
		if p != nil {
			n++
		}
	}
	return n
}

// markUnusable 打上所有与列义无关的失效原因：删除线、计费维度、上下文分档、
// 单位不是每 1M token。
func markUnusable(c *Candidate, text, pageUnit string, headerLine int, sectionProse, headerDim string) {
	if struckRE.MatchString(text) {
		c.Confidence = ConfidenceUnusable
		c.Warnings = append(c.Warnings,
			"row contains a struck-through price (~~...~~) — the superseded original must not be used")
	}
	// 维度检测：行、表头、表前散文三路。命中即不可用——它是**别的产品**的
	// 价格（Batch 五折 / Fast mode 溢价 / 长上下文分档），不是挂牌价。
	if m := dimensionRE.FindString(text); m != "" {
		c.Confidence = ConfidenceUnusable
		c.Warnings = append(c.Warnings,
			"row prices a non-standard billing dimension ("+strings.TrimSpace(m)+
				") — not the model's flat list price")
	}
	if headerDim != "" {
		c.Confidence = ConfidenceUnusable
		c.Warnings = append(c.Warnings,
			"the table header names a non-standard billing dimension ("+
				strings.TrimSpace(headerDim)+") — these prices are conditional, not a flat list price")
	}
	if m := dimensionRE.FindString(sectionProse); m != "" {
		c.Confidence = ConfidenceUnusable
		c.Warnings = append(c.Warnings,
			"the prose above this table names a non-standard billing dimension ("+
				strings.TrimSpace(m)+") — not the model's flat list price")
	}
	if tierQualifierRE.MatchString(text) {
		c.Confidence = ConfidenceUnusable
		c.Warnings = append(c.Warnings,
			"row is context-tiered or per-token — a tiered price is not the flat list price")
	}
	// 单位判定：单元格里写明的单位 > 页面单位。
	if u := cellUnit(text); u != UnitUnknown && u != UnitPer1M {
		c.Unit = u
		c.Confidence = ConfidenceUnusable
		c.Warnings = append(c.Warnings,
			"prices in this row are billed per "+u+", not per 1M tokens — "+
				"a different unit cannot be stored in a per-1M price column")
	} else if c.Unit == UnitUnknown {
		c.Unit = pageUnit
	}
}
