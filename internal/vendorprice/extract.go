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
	"sort"
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
	Description string `json:"description,omitempty"`
	// Tier 是这一行的价格属于哪个**列内计费维度**（"Short context" / "Long
	// context" / "Modality" …）。空串表示这一行与维度分档无关。
	//
	// 它存在的理由是**让被拒绝的价格仍然可见**：改造之前，OpenAI 与 xAI 的
	// 分档表只能产出「列数 8 对不上表头的 4」，数字一个都没有——看提案的人
	// 既看不到数，也看不出这是「有条件价」还是「页面变了」。现在数字带着它
	// 所属的维度一起出现，而 Confidence 仍然是 unusable、永远进不了 SSOT。
	//
	// 取哪一档当基准价是运营决策，解析器不替人做；但**把两套都摆出来让人
	// 裁决**，是解析器该做的。
	Tier string `json:"tier,omitempty"`
	// ProductTier 是**这张表所属的产品档位**，已归一成关键词（standard /
	// batch / flex / fast / ultrafast / priority …）。空串表示表上方没有档位
	// 标签。
	//
	// 它必须与 Tier **分开**成一个字段，原因是实测出来的：
	// OpenAI 一页五张同形状的表（Standard / Batch / Flex / Fast / Ultrafast），
	// 五张表的**列内维度全都是 "Short context"**。只留 Tier 时，
	// gpt-6-astra 的 5 个价——10/50、5/25、5/25、20/100、60/300——在结构化
	// 字段上**完全无法区分**（跨 12 倍），而产品档位只以一句 warning 字符串
	// 存在。谁要是照 Tier 挑「基准价」，挑中 60/300 就是 6 倍高估、挑中
	// 5/25 就是 2 倍低估——正好打在「准确控制模型实际成本」这个目标上。
	//
	// ⇒ Tier 与 ProductTier 合起来构成一条价格的**完整条件**，让人一眼
	// 看出这个数属于哪个产品、因而该判它拒绍。
	//
	// ★ 它们**不是要存进 SSOT 的维度**。SSOT 按「一个模型一个价」建模
	//   （2026-10-04 决策）：原厂把分档做成**不同的模型名**时，那本来就
	//   该是两条 models 行，而不是一条模型的多个价。剩下同名同行的分档
	//   （gpt-6-astra 那 5 行）一律拒绍——SSOT 的键是 canonical_name，
	//   5 行会撞进同一个键互相覆盖。
	ProductTier string   `json:"product_tier,omitempty"`
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

// dateBoundedPriceRE 判断一格里是否出现了「带生效日期的调价」。
//
// 形态（2026-10-05 在活的 ai.google.dev/pricing 上实测）：
//
//	$0.75 through December 31, 2026. $1.50 starting January 1, 2027.
//
// 这一格里有**两个**价，而基准价列的语义是**一个**数。谁是基准价是一个**定价
// 决策**（用当前价 / 用调价后的价 / 干脆不收这个模型），不是解析问题。
// 提取器挑一个，就是在替厂商做那个决策。
//
// ⚠ 不能只用「一格里有几个金额」当信号：`$0.075 $1.00 / 1M tokens per hour` 是
// 「缓存读 + 存储」两笔**不同**计费，一个金额都不是多余的。所以这里要求**同时**
// 出现日期边界措辞与年份 —— 那才是「厂商说了这个价会变」的形状。
var dateBoundedPriceRE = regexp.MustCompile(`(?i)\b(through|starting|until|ends?)\b[\s\S]{0,48}?\b(19|20)\d{2}\b`)

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
var headerDimensionRE = regexp.MustCompile(`(?i)(batch|fast[\s-]?mode|\bflex\b|\bpriority\b|regional|premium|short[\s-]?context|long[\s-]?context|\btier(s|ed|ing)?\b|standard processing|\bmodality\b|\bper\s+modality\b)`)

// productTierRE 匹配「产品档位标签」这种独立成行的短文本。
//
// 只在长度很短（≤40 字符）且不含货币符号时才认：页面上随便一行散文都可能被
// 误认成档位，而把一句正文报成档位只会污染警告。
// proseWindow 是「表前散文」保留的行数上限。
const proseWindow = 6

// headingRE 匹配 markdown 标题行。
var headingRE = regexp.MustCompile(`^#{1,6}\s`)

var productTierRE = regexp.MustCompile(`(?i)^\s*(standard|batch|flex|priority|fast|ultrafast|premium|on-?demand|pay-?as-?you-?go|paygo|base|list)\b`)

// perMillionSpelling 是「每 1M token」在各家页面上的**全部**写法。
//
// ★ 2026-10-06：这一份定义是被迫统一的，因为同一个概念在本文件里曾有**三份**
// 各自独立的正则，而它们已经漂移：
//
//	perMillionRE（块/页级单位声明）      —— 缺 `/\s*m\s*tokens?`
//	unitPatterns 末项（单元格级单位）    —— 早就有 `/\s*m\s*tokens?\b`
//	labelledPriceRE（自带标签的金额）    —— 有 `m\s*tokens?`
//
// 后果不是「某一行没读出来」，而是**整张表读不出来**：MiniMax paygo 页的 LLM
// 段每个单元格都写 `$0.3 / M tokens`（斜杠 + 无数字的 M + tokens），而块级
// 词表不认这种形态 ⇒ M2.7 / M2.5 / M2.1 / M2 全部落到
// "no input or output price found in the mapped columns"，而它们的行里明明写着
// $0.3 / $1.2。
//
// ★ 为什么这比看起来严重：MiniMax-M3 / M2.7 / M2.5 都是近 30 天真实在跑的
// 模型（70,684 / 1,996 / 234 次请求），而 M3 那几行**同时**还因
// 「划线原价 + 上下文分档」被正确拒收 —— 于是「按契约拒收」的理由**掩盖**了
// 「单位根本没读懂」这件事。分不清这两种拒绝，缺陷就能一直藏着：
// 连仓库里那条 `TestExtract_MiniMaxStrikethroughAndTiersAreUnusable` 都是
// **靠这个 bug 才绿的**（它断言「这一页只有分档/折扣价，没有挂牌价」——
// 而 M2.7/M2.5/M2.1/M2 是干净的挂牌价）。
//
// ⚠ 为什么不能放宽成「见到 M 就当每百万」：`/ 1K tokens`、`/ M characters`、
//
//	`/ M images` 在同一批页面里并存，放宽会把每千与按字符的价记成每百万
//	（偏差三个数量级）。所以每一种形态都**要求它自带 token 字样**。
//	`unitPatterns` 里字符/图像/秒等更具体的单位排在前面，顺序即优先级。
//
// ⚠ 那个「更具体的形态先接住」是**顺序**在守，不是词表在守。变异实测把
//
//	`/\s*m\s*tokens?\b` 放宽成 `/\s*m\b`（任何 "/ M" 都算每百万），本包
//	43 条判据**全绿** —— 因为按字符/按图像那几条在 unitPatterns 里排在
//	每百万**之前**，单元格先被判走了。⇒ 别以为「词表够窄」本身在提供保护，
//	真正承重的是**排列顺序**；调整 unitPatterns 的顺序时必须重跑这一条。
//	残留风险：某个单元格自身单位认不出来、而它所在表/页的散文里又出现
//	「/ M characters」时，块级单位会兜成每百万。罕见，但确实存在。
const perMillionSpelling = `per\s*1m|/\s*1\s*m\b|/\s*mtok\b|/\s*m\s*tokens?\b|` +
	`per\s*m\s*token|per\s*million\s*tokens?|1m\s*(?:input|output|token)|` +
	`每\s*1\s*[mM]|元\s*/\s*[mM]`

// perMillionRE 匹配「每 1M token」的单位声明（页眉或单元格里都算）。
var perMillionRE = regexp.MustCompile(`(?i)(` + perMillionSpelling + `)`)

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
	// 末项刻意复用 perMillionSpelling：它与 perMillionRE 曾经是两份独立正则，
	// 其中一份漏了 `/\s*m\s*tokens?`（MiniMax 的写法）而这一份早就有 ——
	// 漂移的代价是整张 MiniMax 价目表读不出价。共用一份定义之后，
	// 新增一种写法只需要改一处。
	{regexp.MustCompile(`(?i)(` + perMillionSpelling + `)`), UnitPer1M},
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
	// label 是紧贴在这张表**上方**的那一行散文。
	//
	// 实测 OpenAI 定价页：同一页有四张形状完全一样的表，原厂把产品档位写成
	// 紧贴表格上方的一行裸文本——"Standard"、"Batch"、"Flex"、"Fast"。
	// 没有它，四张表在提案里长得一模一样，人只能自己回去数这是第几张。
	label string
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
// ProseDimensionPolicy 决定「表前散文里出现维度词」时怎么处理。
//
// ★ 为什么它是一个**策略**而不是一个判断：实测两种情况在位置上**完全一样**。
//
//	MiniMax（实测 2026-10-05）：
//	  27| | Model | Input | Output | ... |          ← Priority 档的表
//	  29| | **MiniMax-M3** ≤ 512k ... |
//	  32| * Priority provides priority admission ... Pricing is 1.5x standard.
//	  34| | Model | Input | Output | ... |          ← M2.7 的表（干净的挂牌价）
//
//	anthropic（同一条规则的正例）：
//	  234| | Model | Base Input Tokens | Output Tokens |
//	  236| | Claude Opus 4.8 | $5 / MTok | $25 / MTok |
//	  238| Fast mode pricing, ... premium pricing.
//	  240| | Model | Input | Output |
//	  242| | Claude Opus 4.8 | $10 / MTok | $50 / MTok |
//
// 两例都是「散文夹在两张表之间」，**位置规则分不开**。MiniMax 那句是对**上一张**
// 档位表的脚注，anthropic 那句是对**下一张**表的声明。判据靠位置会二选一地错。
//
// ⇒ 解析器**不替人决定**。默认 ProseDimensionReject（保守，与既有行为逐字节
// 相同）；人看过页面、确认那句是脚注之后，用 ProseDimensionAcceptWithWarning
// 显式打开 —— 即使打开，维度词**仍然写进 warnings**，不隐藏。
type ProseDimensionPolicy int

const (
	// ProseDimensionReject：散文里出现维度词 ⇒ 整表判不可用（既有行为）。
	ProseDimensionReject ProseDimensionPolicy = iota
	// ProseDimensionAcceptWithWarning：仍然记一条 warning，但不降级。
	// 「降不降级」是产品判断，不是解析判断。
	ProseDimensionAcceptWithWarning
)

// Options 是 Extract 的可选行为。零值即既有行为。
type Options struct {
	ProseDimension ProseDimensionPolicy
}

// Extract 是既有入口，等价于 ExtractWithOptions(..., Options{})。
func Extract(vendor, sourceURL string, markdown []byte) []Candidate {
	return ExtractWithOptions(vendor, sourceURL, markdown, Options{})
}

// ExtractWithOptions 是带策略的入口。
func ExtractWithOptions(vendor, sourceURL string, markdown []byte, opts Options) []Candidate {
	lines := strings.Split(string(markdown), "\n")
	var out []Candidate

	// 规格表式（run-on）定价页：整页**一个 markdown 表格都没有**。这类页面
	// 走下面的块解析器会得到**零候选** —— 连 orphanCandidate 都不会触发
	// （它只对 `|` 开头的行调用），于是整页的钱静默消失。单独一条路径先处理
	// 它，详见 runsheet.go。返回 nil 表示「不是它该管的页面」，不是「查过没有」。
	out = append(out, extractRunOnSheet(vendor, sourceURL, lines)...)

	pageUnit := UnitUnknown
	if perMillionRE.MatchString(string(markdown)) {
		pageUnit = UnitPer1M
	}

	// prose 累积「上一张表结束到本行之间」的非表格文本。维度措辞
	//（"Fast mode pricing" / "with a 50% discount"）几乎总是写在表前的散文里
	// 而不是表头里。
	//
	// **必须有界。** 实测 OpenAI 定价页的第一张表在第 1029 行，而它前面
	// 一行表格都没有 ⇒ 「表前的散文」是整整 1028 行导航栏。于是侧边栏里的
	// 一个链接 `[Fast mode](…/fast-mode)` 成了「Standard 档那张表」被判不可用
	// 的理由之一。这与本包 2026-10-04 已经修过的两次误伤同族（tier 匹配
	// frontier、tool use 匹配能力罗列），但更糟：**它不是一条散文，是一整页**。
	//
	// 三道收窄，缺一不可：
	//   - 只留最近 proseWindow 行（维度措辞总在表前 1–3 行内）；
	//   - 遇到 markdown 标题就清空（新章节的表与上一章节的散文无关）；
	//   - 遇到独立的档位标签行就清空（见下方 productTierRE 的用途）。
	var prose []string
	// lastLine 记住最近一行非空散文，作为下一张表的 label。
	lastLine := ""

	i := 0
	for i < len(lines) {
		line := lines[i]
		if !markdownRowRE.MatchString(line) {
			// 非表格行：如果它自己声明了「每 1M」，那是单位上下文更新。
			// 注意：这个分支里**不能**用 continue——i++ 在分支之后，
			// 跳过去就是死循环（本包 2026-10-04 真的挂过一次）。
			if perMillionRE.MatchString(line) {
				pageUnit = UnitPer1M
			} else if t := strings.TrimSpace(line); t != "" {
				lastLine = t
				switch {
				// markdown 标题 = 新章节起点，上一章节的散文与本表无关。
				case headingRE.MatchString(t):
					prose = prose[:0]
				// 独立的档位标签（"Standard" / "Batch" / "Flex"）之前的内容是
				// UI 残留：OpenAI 的页面上是标签条 "Standard Batch Flex Fast
				// Ultrafast" 加一个孤立的 "Standard" 表示当前选中项。标签条
				// 里的 Batch/Flex 会把**每张**表都染上「维度」警告，而它们只是
				// 导航。标签行本身已经是权威的档位标识，所以把标签之前清掉。
				case isProductTierLabel(t):
					prose = append(prose[:0], t)
				default:
					prose = append(prose, t)
					if len(prose) > proseWindow {
						prose = prose[len(prose)-proseWindow:]
					}
				}
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
				prose:       strings.Join(prose, " "),
				label:       lastLine,
			}
			prose = prose[:0]
			lastLine = ""
			i += 2
			for i < len(lines) && markdownRowRE.MatchString(lines[i]) {
				if !separatorRowRE.MatchString(lines[i]) {
					block.rows = append(block.rows, rawRow{raw: lines[i], lineNo: i + 1})
				}
				i++
			}
			out = append(out, extractBlock(vendor, sourceURL, block, pageUnit, opts)...)
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
	priced, modelCol := 0, 0
	for _, c := range cells {
		switch RoleFromHeader(cleanCell(c)) {
		case RoleInput, RoleOutput, RoleCacheRead, RoleCacheWrite:
			priced++
		case RoleModel:
			modelCol++
		default:
			// 出现任何既不是维度名也不是 "Model" 的格子 ⇒ 这是数据行。
			// 数据行的首格是模型**名**（"gpt-6-astra"），它不是维度名，
			// 会被上面这条 default 挡掉。
			return false
		}
	}
	return priced > 0 && modelCol <= 1
}

// isTierHeader 判断第一层表头的一个单元格是不是**档位分组名**。
//
// 判据是两个条件同时成立：它带一个计费维度词（headerDimensionRE），且它
// **不给列义**（不是 Input/Output/Cached…）。xAI 的 "Short context"、
// OpenAI 的 "Long context" 都过；而同层的 "Model" / "Context" 都不过。
func isTierHeader(cell string) bool {
	return headerDimensionRE.MatchString(cleanCell(cell)) && headerRole(cell) == RoleOther
}

// effectiveHeader 是把两层表头拼成一条之后的列语义。
type effectiveHeader struct {
	roles []ColumnRole
	cells []string
	// tiers 与 roles 等长；只有价格列有值，其余是空串。
	tiers []string
}

// buildEffectiveHeader 把「档位分组表头 + 维度表头」两层拼成一条可用的列语义。
//
// markdown 没有 colspan，所以第一层表头的一个格子到底盖住几列，从语法上看
// 不出来。实测两家的写法是同一套语义，于是拼法也是同一套：
//
//	xAI   | Model | Context | Short context | Long context |      4 格
//	      | --- | --- | --- | --- |
//	      | Input | Cached | Output | Input | Cached | Output |     6 格 ← 维度
//	      | [grok-4.7](…) | 500k | $2.00 | $0.50 | $6.00 | …        8 格 ← 数据
//
//	OpenAI |  | Short context | Long context |                          3 格
//	       | --- | --- | --- |
//	       | Model | Input | Cached input | Cache writes | Output | …   9 格 ← 维度
//	       | gpt-6-astra | $10.00 | $1.00 | $12.50 | $50.00 | …       9 格 ← 数据
//
// 拼法：**第一层的非档位格子各占一列（空的左上角占位格不算），剩下的价格列
// 平分给各档位分组**。两例都自洽——xAI 2 个非档位列 + 2 组×3 = 8；
// OpenAI 0 个非档位列（Model 写在了第二层里）+ 2 组×4 = 9。
//
// **任何一步不整除就放弃**，退回「列数对不上」的旧行为。宁可少拼，不可拼错：
// 拼错的后果是把长上下文价挂到短上下文上，而 SSOT 的键装不下两套价。
func buildEffectiveHeader(headerCells, sub []string, dataCols int) (effectiveHeader, bool) {
	var zero effectiveHeader
	var tierIdx []int
	for j, c := range headerCells {
		if isTierHeader(c) {
			tierIdx = append(tierIdx, j)
		}
	}
	if len(tierIdx) == 0 || len(sub) == 0 || dataCols <= 0 {
		return zero, false
	}

	// 第二层自己带了一个 Model 列时，第一层的非档位列就不该再拼一次。
	subHasModel := len(sub) > 0 && RoleFromHeader(cleanCell(sub[0])) == RoleModel

	var prefixCells []string
	if !subHasModel {
		for j, c := range headerCells {
			if inTier(tierIdx, j) {
				continue
			}
			if cleanCell(c) == "" {
				continue // 左上角占位格
			}
			prefixCells = append(prefixCells, c)
		}
	}

	eff := append([]string{}, prefixCells...)
	eff = append(eff, sub...)
	if len(eff) != dataCols {
		return zero, false
	}

	h := effectiveHeader{roles: make([]ColumnRole, len(eff)), cells: eff, tiers: make([]string, len(eff))}
	for j, c := range eff {
		h.roles[j] = headerRole(c)
	}
	priceCols := 0
	for _, r := range h.roles {
		switch r {
		case RoleInput, RoleOutput, RoleCacheRead, RoleCacheWrite:
			priceCols++
		}
	}
	if priceCols == 0 || priceCols%len(tierIdx) != 0 {
		return zero, false
	}
	per := priceCols / len(tierIdx)
	seen := 0
	for _, j := range tierIdx {
		label := cleanCell(headerCells[j])
		for k := 0; k < per; k++ {
			h.tiers[priceColsSeen(h.roles, seen)] = label
			seen++
		}
	}
	return h, true
}

// inTier 判断下标是否在档位下标集合里。
func inTier(idx []int, j int) bool {
	for _, v := range idx {
		if v == j {
			return true
		}
	}
	return false
}

// priceColsSeen 返回 roles 里第 n 个价格列的下标。
func priceColsSeen(roles []ColumnRole, n int) int {
	c := 0
	for j, r := range roles {
		switch r {
		case RoleInput, RoleOutput, RoleCacheRead, RoleCacheWrite:
			if c == n {
				return j
			}
			c++
		}
	}
	return -1
}

// extractBlock 判定一张表的形态并逐行提出候选。
func extractBlock(vendor, sourceURL string, b tableBlock, pageUnit string, opts Options) []Candidate {
	var out []Candidate
	if len(b.rows) == 0 {
		return out
	}
	if isColumnOriented(b.headerCells) {
		return extractColumnOriented(vendor, sourceURL, b, pageUnit, opts)
	}

	roles := make([]ColumnRole, len(b.headerCells))
	for j, c := range b.headerCells {
		roles[j] = headerRole(c)
	}
	headerDim := headerDimensionRE.FindString(b.headerRaw)

	// 第二层表头：数据区第一行若是「整行都是维度名」，它就是第二层表头。
	//
	// 拼得起来就**照拼**（把两套分档价都提出来，各自带 Tier 标签），因为
	// 「看不到数」和「看到数但标明它有条件」对人的用处差着量级；拼不起来
	// （列数不整除、第二层认不出）就退回「列数对不上」并把原因说清。
	subHeaderLine := 0
	var eff effectiveHeader
	aligned := false
	if len(b.rows) > 0 && len(splitTableRow(b.rows[0].raw)) != len(roles) && looksLikeSubHeader(b.rows[0]) {
		subHeaderLine = b.rows[0].lineNo
		sub := splitTableRow(b.rows[0].raw)
		for _, r := range b.rows[1:] {
			if n := len(splitTableRow(r.raw)); n > 0 {
				eff, aligned = buildEffectiveHeader(b.headerCells, sub, n)
				break
			}
		}
		out = append(out, Candidate{
			Vendor: vendor, SourceURL: sourceURL, LineNo: b.rows[0].lineNo, Row: b.rows[0].raw,
			Confidence: ConfidenceUnusable,
			Warnings: []string{"this row is a second header row: every cell is a price " +
				"dimension, so the real column semantics live across two header rows"},
		})
		if aligned {
			out[len(out)-1].Warnings = append(out[len(out)-1].Warnings,
				"the two header rows were aligned (tier groups span equal column runs) and the "+
					"prices below were read — but they are TIERED, not a flat list price: "+
					"each candidate carries the tier it belongs to and must not be stored as-is")
		} else {
			out[len(out)-1].Warnings = append(out[len(out)-1].Warnings,
				"the two header rows could NOT be aligned unambiguously, so the prices below "+
					"are left unread rather than guessed at")
		}
		if headerDim != "" {
			out[len(out)-1].Warnings = append(out[len(out)-1].Warnings,
				"and the top header groups those columns by billing dimension ("+
					strings.TrimSpace(headerDim)+"), so the prices are conditional, not a flat list price")
		}
		out[len(out)-1].Warnings = append(out[len(out)-1].Warnings, tierLabelWarning(b.label)...)
	}

	for _, r := range b.rows {
		if r.lineNo == subHeaderLine {
			continue
		}
		cells := splitTableRow(r.raw)
		rowRoles, rowTiers := roles, []string(nil)
		if aligned {
			if len(cells) != len(eff.roles) {
				out = append(out, Candidate{
					Vendor: vendor, SourceURL: sourceURL, LineNo: r.lineNo, Row: r.raw,
					Confidence: ConfidenceUnusable,
					Warnings: []string{"column count " + strconv.Itoa(len(cells)) +
						" does not match the assembled two-row header's " + strconv.Itoa(len(eff.roles))},
				})
				continue
			}
			rowRoles, rowTiers = eff.roles, eff.tiers
		} else if len(cells) != len(roles) {
			w := []string{"column count " + strconv.Itoa(len(cells)) +
				" does not match the header's " + strconv.Itoa(len(roles))}
			w = append(w, tierLabelWarning(b.label)...)
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
		out = append(out, buildCandidate(vendor, sourceURL, r.raw, r.lineNo, cells, rowRoles,
			pageUnit, b.headerLine, b.prose, headerDim, b.label, rowTiers, opts))
	}
	return out
}

// extractColumnOriented 处理「模型在表头、价格在单元格」的那张表。
func extractColumnOriented(vendor, sourceURL string, b tableBlock, pageUnit string, opts Options) []Candidate {
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
				names[j], cell, pageUnit, b.headerLine, b.prose, headerDimensionRE.FindString(b.headerRaw), b.label, opts))
		}
	}
	return out
}

// buildColumnCandidate 从一个自带标签的单元格里造候选。
func buildColumnCandidate(vendor, sourceURL, rawRowText string, lineNo int, model, cell, pageUnit string, headerLine int, sectionProse, headerDim, label string, opts Options) Candidate {
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

	markUnusable(&c, cell, pageUnit, headerLine, sectionProse, headerDim, label, opts)

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

// hasModelRole 报出表头里有没有"模型"这一列。
//
// 与 buildCandidate 里那三个 has* 检查并列，但**语义不同**：那三个问的是
// 「这一行能不能定位到某模型的输入/输出价」，这个问的是「这张表有没有把
// 模型放在列上」—— 后者决定模型身份是不是根本不在表里。
func hasModelRole(header []ColumnRole) bool {
	for _, r := range header {
		if r == RoleModel {
			return true
		}
	}
	return false
}

// buildCandidate 把一行数据行变成候选，并按失效原因定级。
func buildCandidate(vendor, sourceURL, line string, lineNo int, cells []string, header []ColumnRole, pageUnit string, headerLine int, sectionProse, headerDim, label string, tiers []string, opts Options) Candidate {
	c := Candidate{
		Vendor: vendor, SourceURL: sourceURL, LineNo: lineNo, Row: line,
		Currency: currencyOf(line), Unit: UnitUnknown,
	}

	markUnusable(&c, line, pageUnit, headerLine, sectionProse, headerDim, label, opts)

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
	// ★ 「一模型一张表、模型名在章节标题里、列是计费档位」这一页形
	//   （2026-10-05 在 google-gemini.md 上实测：329 行、0 候选）
	//
	// 这类表**没有** model 列，它的列是档位（Free Tier / Paid Tier），
	// 模型身份只能来自章节标题。而 Gemini 3 家族那 250 多行，模型名在抓下来的
	// markdown 里**根本不存在**（标题被 jina 转换压平成了营销文案，第一张表
	// 上方那句 "Our most intelligent model built for speed…" 一个模型名都没有）。
	//
	// ⇒ 前面那条「header does not map to both an input and an output column」在
	// 这里**是误导的**：它把人引去调列映射，而列映射是对的，缺的是抓取保真度。
	// 人要照着它调表，只会把一张本来正确的表调坏。
	//
	// 所以单独报一条**可核验的**诊断：表头里没有 model 列、且列名是计费档位。
	// 措辞只说能从文档里核实的事实，不猜「附近大概是有名字的」。
	if !hasModelRole(header) && strings.TrimSpace(headerDim) != "" {
		c.Confidence = ConfidenceUnusable
		c.Warnings = append(c.Warnings,
			"this table has no model column and its columns are billing dimensions ("+
				strings.TrimSpace(headerDim)+"): it is a per-model price block, so the model "+
				"identity has to come from the section heading rather than from the table. "+
				"The extractor cannot attribute such a row to a model, and the gap is in the "+
				"FETCHED SNAPSHOT (per-model anchors are not preserved), not in the column "+
				"mapping — re-fetch the page keeping per-model anchors, or price the model "+
				"from its own anchor page")
	}
	if !hasInput || !hasOutput {
		c.Confidence = ConfidenceUnusable
		c.Warnings = append(c.Warnings,
			"header does not map to both an input and an output column (header line "+
				strconv.Itoa(headerLine)+")")
	}

	// 模型名取自**表头标明 Model 的那一列**，不是「第一列」。
	//
	// 实测（live-openai-pricing.md:1190）：`| Category | Model | Input | … |`
	// —— 第一列是产品分类（ChatGPT / Codex / Life Sciences）。按位置取名会得到
	// 一个叫 "ChatGPT" 的模型，而它的价其实是 chat-latest 的。
	// 表头没有 Model 列时才退回第一列（OpenAI 旧表、xAI 旧表都是 Model 打头）。
	if idx := modelColumnIndex(header); idx >= 0 && idx < len(cells) {
		c.Model, c.Description = splitIdent(cells[idx])
	} else if len(cells) > 0 {
		c.Model, c.Description = splitIdent(cells[0])
	}
	if c.Model == "" {
		c.Confidence = ConfidenceUnattributed
		c.Warnings = append(c.Warnings, "first column does not yield a model name")
	}

	extra := 0
	seenTiers := map[string]bool{}
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
			continue
		}
		// 只记**被取走的那一列**所属的档位。若把整行出现过的档位都记上，
		// Tier 会写成 "Long context | Short context" 而数字其实只来自短上下文
		// 那一组——标签比数字宽，正是最容易被误读成「两档都一样」的地方。
		if j < len(tiers) && tiers[j] != "" {
			seenTiers[tiers[j]] = true
		}
	}
	if len(seenTiers) > 0 {
		c.Tier = strings.Join(sortedKeys(seenTiers), " | ")
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

// sortedKeys 让 Tier 字段的顺序稳定——同一行里档位按列序出现，
// 但 map 无序；不排序的话同一份快照两次跑出两个不同的 JSON，
// 人就没法用 diff 看出「除了档位顺序什么都没变」。
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// modelColumnIndex 找出表头里标着 Model 的那一列；没有则返回 -1。
func modelColumnIndex(header []ColumnRole) int {
	for j, r := range header {
		if r == RoleModel {
			return j
		}
	}
	return -1
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

// tierLabelWarning 报出一张表的产品档位标签（没有就返回空）。
//
// 实测 OpenAI 定价页：同一页有四张形状**完全一样**的表，原厂把档位写成紧贴
// 表格上方的一行裸文本——"Standard"、"Batch"、"Flex"、"Fast"。不报出来，
// 人在提案里分不清哪张是哪张，只能自己回去数。
//
// 只在短行、无货币符号、且以档位词开头时才认：页面上随便一行散文都可能被
// 误认成档位，而把一句正文报成档位只会污染警告。
func isProductTierLabel(line string) bool {
	if line == "" || len(line) > 40 || currencySymbolRE.MatchString(line) {
		return false
	}
	return productTierRE.MatchString(line)
}

// isPremiumTierLabel 判断档位标签是不是**非标准档**（Batch / Flex / Fast /
// Priority / Regional / Premium）。它们都是**别的产品**的定价。
//
// 与 productTierRE 的分工：那个认「这行是不是档位标签」，这个只认「它是不是
// 打折/加价档」。判据是**否定式**的（列出非标准档），因为"标准"这个词在各家
// 页面里写法太多（Standard / Standard processing / On-demand / Pay as you go
// / Base / List），认成白名单会漏；认成非标准档则漏不到会影响正确性的那一侧
// ——真漏了也只是这张表退回保守，代价是覆盖率，不是错价。
var premiumTierRE = regexp.MustCompile(`(?i)^\s*(batch|flex|priority|fast|ultrafast|premium|regional)\b`)

func isPremiumTierLabel(label string) bool {
	if !isProductTierLabel(label) {
		return false
	}
	return premiumTierRE.MatchString(label)
}

// productTierKeyword 把一个档位标签**归一**成它的关键词，认不出时返回空串。
//
// 为什么必须归一，而不是把标签原文塞进 ProductTier：productTierRE 是
// `^\s*(standard|batch|…)\b` 这种**前缀**匹配，所以它同样接受
// "Batch API reference" 这种小标题。早先把原文直接存进 ProductTier，
// 于是同一档位的两种写法会占两个不同的键（"batch" 与 "Batch API reference"），
// 按键分组、按键去重、拿键当条件全部失准——而这正是这个字段存在的理由。
//
// **只做词内归一（小写 + 去掉连字符与空格），不合并不同关键词。**
// 刻意的：`paygo` 与 `pay-as-you-go` 是同义的两个词，合并它们很诱人，但一旦
// 原厂在两处用了这两种写法、且两处的价其实不同（分档细则不同），合并就会
// 把两个价压进一个键，触发「一个条件键对应两个价」这种最坏形状。
// 不合并的失败方向是安全的：同价分裂成两行，人看得见；而合并的失败方向是
// 静默取其一。宁可多报一行，不可悄悄少一个条件。
// 原文仍保留在 warnings 里（见 tierLabelWarning），人核对时看得到全称。
func productTierKeyword(label string) string {
	m := productTierRE.FindStringSubmatch(label)
	if m == nil {
		return ""
	}
	k := strings.ToLower(m[1])
	k = strings.ReplaceAll(k, "-", "")
	k = strings.ReplaceAll(k, " ", "")
	return k
}

func tierLabelWarning(label string) []string {
	if !isProductTierLabel(label) {
		return nil
	}
	// **只陈述，不裁决。** 定级由 isPremiumTierLabel 那一条单独负责。
	// 早先把「陈述」和「裁决」写在同一句话里，结果 Standard 档那张表也被
	// 警告「必须来自标准档」——一句对着一张好表说「你得是标准档」的话，
	// 只会让人怀疑判据本身。警告要么说事实，要么下判断，不混着说。
	return []string{"this table's product tier is labelled \"" + label + "\" directly above it"}
}

// markUnusable 打上所有与列义无关的失效原因：删除线、计费维度、上下文分档、
// 单位不是每 1M token。
func markUnusable(c *Candidate, text, pageUnit string, headerLine int, sectionProse, headerDim, label string, opts Options) {
	// 一格里有「带生效日期的调价」⇒ 这一格给不出唯一的基准价。
	// 放在 markUnusable 而不是某一条提取路径上：行导向与列导向都会经过这里。
	if currencySymbolRE.MatchString(text) && dateBoundedPriceRE.MatchString(text) {
		c.Confidence = ConfidenceUnusable
		c.Warnings = append(c.Warnings,
			"this cell states a price that changes on a date ("+
				strings.TrimSpace(dateBoundedPriceRE.FindString(text))+
				"): the vendor lists the current price and the price from that date, while a "+
				"baseline price column holds ONE number. Which one is the baseline is a pricing "+
				"decision (current list price / post-change list price / do not price this model "+
				"at all), not a parsing problem — the extractor must not pick")
	}
	// 产品档位**结构化落字段**，不只是报一句 warning。
	//
	// 这里必须在 markUnusable 里做，因为它是 label 的唯一收口：列向表格
	// （模型在表头）与行向表格（价格在一行）两条路径都经过它。早先只在
	// warning 里报，结果就是「档位已识别但结构化字段丢失」——
	// gpt-6-astra 的 5 个价跨 12 倍却长得一模一样。
	//
	// **只填字段，不在这里定级**：定级仍归 isPremiumTierLabel 那一条。
	if kw := productTierKeyword(label); kw != "" {
		c.ProductTier = kw
	}
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
		// ★ 这里**不替人决定**「这句散文是上一张表的脚注还是下一张表的维度声明」。
		// 两种情况在页面上位置完全一样（见 ProseDimensionPolicy 的注释），
		// 所以位置规则必然二选一地错。默认拒收（保守），人看过页面后用
		// ProseDimensionAcceptWithWarning 显式打开 —— 打开时**仍然记这条
		// warning**，维度词不隐藏，提案里看得见「这里有过一个维度词」。
		c.Warnings = append(c.Warnings,
			"the prose above this table names a non-standard billing dimension ("+
				strings.TrimSpace(m)+") — not the model's flat list price")
		if opts.ProseDimension != ProseDimensionAcceptWithWarning {
			c.Confidence = ConfidenceUnusable
		} else {
			c.Warnings = append(c.Warnings,
				"the prose-dimension check was DISABLED by an explicit flag, so this row is "+
					"readable — verify on the page that the prose describes the PRECEDING table "+
					"(a footnote) and not this one, because the extractor cannot tell them apart")
		}
	}
	// 报出这张表的产品档位标签。OpenAI 一页四张同形状的表，原厂把档位写在
	// 紧贴表格上方的一行裸文本里；不报出来，人在提案里分不清哪张是哪张。
	// 档位标签不只是要**报出来**，还要**定级**：OpenAI 的 "Fast" 档下面那张表
	// 与 Standard 档形状一模一样、价更高，label 也确实写着 Fast。只报不定级
	// 的话，Fast 档的 Codex 会被当成挂牌价收进提案——而 SSOT 的键只有
	// canonical 名一个，它会与 Standard 档那一条互相覆盖或取其一。
	if isPremiumTierLabel(label) {
		c.Confidence = ConfidenceUnusable
		c.Warnings = append(c.Warnings,
			"this table is the vendor's \""+label+"\" tier, not its standard list-price tier — "+
				"a baseline price must come from the standard tier")
	}
	c.Warnings = append(c.Warnings, tierLabelWarning(label)...)
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
