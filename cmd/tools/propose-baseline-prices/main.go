// Command propose-baseline-prices 从原厂定价页快照里提出**价格候选**，
// 输出一份待人确认的提案文件。
//
// # 它不做什么（这是本工具存在的理由）
//
// 它**不写** bg/data/model_baseline_prices.json，也不碰数据库。
// 提案的每一条都带 `reviewed: false` 与原始行文本，SSOT 只接受人确认过的
// 条目。理由在 internal/vendorprice 的包头：把自动提取的数直接写进计费
// 系统，是在用一个从未经核实的数字去改「我们按什么价卖」。
//
// # 用法
//
//	go run ./cmd/tools/propose-baseline-prices \
//	  -raw docs/02-resources/research/pricing/raw \
//	  -out /tmp/baseline-proposal.json
//
// # 抓取怎么办
//
// 抓取用仓里既有的 scripts/fetch-pricing.sh（docs/02-resources/research/
// pricing/scripts/），它经 r.jina.ai 把原厂定价页转成 markdown 存进 raw/。
// 抓取与解析刻意分开：解析必须能对着**历史快照**被测试，否则每次改解析
// 都要联网，而联网的测试结果不可复现。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/kaixuan/llm-gateway-go/internal/vendorprice"
	"github.com/kaixuan/llm-gateway-go/modelname"
)

// vendorPage 把快照文件名映射到 (厂商, 原厂定价页 URL)。
//
// 刻意手写而不是从文件里猜：source_url 是 SSOT 的出处字段，猜出来的 URL
// 不可审计，而不可审计的出处正是现状那张手工价格表漂移到没人知道的原因。
var vendorPage = map[string]struct{ Vendor, URL string }{
	// ★ 2026-10-06 订正三家的 URL。订正的依据是「哪个 URL 才是**定价页**」，
	//   不是「哪个 URL 的快照产出高」——后者会把 models 页选成价目来源。
	//
	//   anthropic：原先写的是 `…/about-claude/models/overview`（**模型总览**页），
	//     而仓里的快照 `URL Source` 是 `…/about-claude/pricing`（**定价页**）。
	//     ⇒ 映射错了、快照是对的。改映射。那一页产出 16 条可用价（全场最高），
	//     而 models/overview 那页只有 4.5KB / 12 张表 —— 换过去会**少**。
	//
	//   openai：仓里的 `openai.md` 抓的是 `…/docs/models`（47,490 字节、
	//     **0 张表格**、1 行含 `$`），而 pricing 页 61,525 字节、92 张表格、
	//     54 行含 `$`。⇒ 快照错了，抓的是 models 页。已重抓。
	//
	//   google：仓里的快照 `URL Source` 是 `ai.google.dev/pricing`，与本映射
	//     原先写的 `…/gemini-api/docs/pricing` 都不是同一个。已按定价页重抓。
	//
	// ★ 改这张表时必须同时改三处：抓取脚本
	//   docs/02-resources/research/pricing/scripts/fetch-pricing.sh 的 fetch 清单、
	//   raw/{vendor}.md 的 `URL Source`、以及这张表。判据
	//   url_consistency_test.go 会核这三者，不一致就红。
	"anthropic.md":     {"anthropic", "https://docs.anthropic.com/en/docs/about-claude/pricing"},
	"openai.md":        {"openai", "https://platform.openai.com/docs/pricing"},
	"google-gemini.md": {"google", "https://ai.google.dev/gemini-api/docs/pricing"},
	"deepseek.md":      {"deepseek", "https://api-docs.deepseek.com/quick_start/pricing"},
	"xai.md":           {"xai", "https://docs.x.ai/docs/pricing"},
	"zhipu.md":         {"zhipu", "https://open.bigmodel.cn/pricing"},
	"MiniMax-paygo.md": {"minimax", "https://platform.minimax.io/docs/guides/pricing-paygo"},
	"mistral.md":       {"mistral", "https://docs.mistral.ai/getting-started/models/models_overview"},
	"doubao.md":        {"doubao", "https://www.volcengine.com/docs/82379/1544106"},

	// openrouter.md **故意不在**这张表里。OpenRouter 是聚合/中转站，它公布
	// 的价是它自己的转售价，不是原厂标准价。把它当原厂源，等于把「供应商
	// 实际价」混进「基准价」——而基准价存在的全部意义就是给供应商实际价当
	// 参照物。它只能进对账侧（观测源），不能进权威面。抓取脚本
	// fetch-pricing.sh 自己也把它标注成「聚合站（交叉验证）」。
}

// livePages 是钉在 internal/vendorprice/testdata/ 下的实抓夹具。
//
// 单独列一张表而不是并进 vendorPage：它们是**测试夹具**，留在提案工具的
// 默认语料里会让人以为「当前正在看的原厂页面」就是它们。实抓页面随时会变，
// 夹具不会——两者混在一起，出处那一栏就又不可审计了。
//
//	用法：go run ./cmd/tools/propose-baseline-prices \
//		  -raw internal/vendorprice/testdata -out /tmp/live.json
var livePages = map[string]struct{ Vendor, URL string }{
	"live-anthropic-models-overview.md": {"anthropic", "https://docs.anthropic.com/en/docs/about-claude/models/overview"},
	"live-openai-pricing.md":            {"openai", "https://platform.openai.com/docs/pricing"},
	"live-xai-pricing.md":               {"xai", "https://docs.x.ai/developers/models"},
	"live-deepseek-pricing.md":          {"deepseek", "https://api-docs.deepseek.com/quick_start/pricing"},
}

// proposal 是输出文件。字段名与 bg.BaselinePrice 对齐，方便人把确认过的
// 条目搬进 SSOT。
type proposal struct {
	GeneratedAt string `json:"generated_at"`
	// Notice 写进文件本身而不只是日志：提案会被拷到工单、聊天、issue 里，
	// 脱离本工具的上下文。
	Notice         string                  `json:"notice"`
	Reviewed       bool                    `json:"reviewed"`
	Counts         map[string]int          `json:"counts_by_confidence"`
	ReadyToReview  []vendorprice.Candidate `json:"ready_to_review"`
	NeedsHumanEyes []vendorprice.Candidate `json:"needs_human_eyes"`

	// Corroborated 只在 --corroborate 开启时有内容：每条都过了
	// 「厂商展示名 → canonical 名」解析与「第二个独立信源」对账两道。
	Corroborated []corroborated `json:"corroborated,omitempty"`
	// Unresolved 是解析不出 canonical 名的展示名。**必须单独列出来**：
	// 混进 needs_human_eyes 会让人以为问题在价格，其实问题在名字。
	Unresolved []unresolved `json:"unresolved_names,omitempty"`

	// StaleSnapshots 记每份快照的新鲜度结论。★ 它必须是**结构化字段**而不是
	// 只写 stderr：提案会被拷进工单与聊天，只写日志的那部分证据就丢了，
	// 而「这份价来自一个 4 个月前的缓存渲染」正是审阅时最该先看到的一句。
	StaleSnapshots []snapshotNote `json:"snapshot_freshness,omitempty"`

	// CanonicalList 是 --canonical 名单的出处。**必须写进文件**：
	// 一份没有出处的提案里，那些 "resolved to canonical X" 的判词是没有
	// 依据的——margin 判据只在名单完整时成立，而提案会被拷到工单、issue、
	// 聊天里，脱离本工具的上下文。
	CanonicalList string `json:"canonical_list,omitempty"`
	// CanonicalDuplicates 是名单里「同一个模型的两种写法」。它必须进文件：
	// 判据说是「歧义已挡下」，人看到的是这句话；真正的根因在名单里，
	// 不写下来就得等下一次再撞一次。
	CanonicalDuplicates []string `json:"canonical_near_duplicates,omitempty"`
	// AmbiguousCanonical 是「同一个 canonical 出现了互相冲突的价」。
	// ★ 这些条目**不在** ready_to_review 里，也**不在** needs_human_eyes 里：
	//   放进 needs_human_eyes 会被 groupWarnings 按 warning 前缀归类，而它们
	//   没有自己的 warning 前缀，会落进某个不相干的族里冒充别的理由。
	//   所以它单独成栏：SSOT 是「一个模型一个价」，撞上时正确结果是**缺一条
	//   基准价**，不是「挑一条」——挑的那条会随页面排版漂移。
	//   实测触发例：anthropic 页的 Claude Opus 4.8 在标准表与 Fast mode 表里
	//   各有一行，$5/$25 与 $10/$50。
	AmbiguousCanonical []canonicalCollision `json:"ambiguous_canonical_prices,omitempty"`
	// CorroborationRefused 说明互证为什么没做（或做了）。
	CorroborationRefused string `json:"corroboration_refused,omitempty"`
	// RejectionReasons 是 needs_human_eyes 的**按原因归类**，让人先处理成
	// 片的某一类而不是逐条读。2026-10-04 实测：一页实抓快照产出 112 条不可用、
	// 30 种理由，没有它就只能逐条扫；而逐条扫的代价正是这套流程要替人省掉的。
	RejectionReasons []rejectionReason `json:"rejection_reasons,omitempty"`
}

// rejectionReason 是「一类被拒的价格」而不是「一条被拒的价格」。
//
// ★ Count 不可相加：一条候选可以同时命中好几类，所以**加总会得到一个不存在的数**。
// 要答「有多少条不可用」用 counts_by_confidence；这里答的是「哪一类最该先修」。
//
// ⚠ 这里**刻意不抄一份归类清单当文档**：2026-10-05 我就是从下面这段注释里抄了
// matcher 措辞，而它引的是一份**过期**快照（少了 "billing" 一词），结果新类表里
// 有一条 matcher 永远不命中。类表是真实的唯一出处；清单随时可由
// `go run ./cmd/tools/propose-baseline-prices -raw internal/vendorprice/testdata`
// 重新生成，判据 TestRejectionReasonFamiliesCoverRealFixtures 把它钉住。
type rejectionReason struct {
	// Reason 是归类键，取 warning 的**前半句**（见 groupWarnings 的约定说明）。
	Reason string `json:"reason"`
	// Count 是命中该类的候选数。与别的类会重叠。
	Count int `json:"count"`
	// Examples 是最多 3 个示例展示名，让人一眼认出「是哪几条」，不必去翻全表。
	Examples []string `json:"examples,omitempty"`
	// Why 是这一类的共同成因与下一步动作（见 reasonFamily.Why）。
	// 只有**归族**得到的类有值；`unclassified: ` 的类没有 —— 那正是需要人看的信号。
	Why string `json:"why,omitempty"`
}

// rejectionReasonOverlapNote 是随提案与类型带出去的那句提醒。
//
// 单独立成常量（而不是只写在注释里）是为了让判据能**钉住它**：一排数字摆在
// 面前，人第一反应就是加总，而加总会得到一个不存在的数（实测 112 条里前六类
// 合计 149）。注释会被人跳过，写进 notice 才会跟着文件走。
const rejectionReasonOverlapNote = "rejection_reasons 各类计数互相重叠、一条候选可命中多类，所以不可相加"

// reasonFamily 把若干条**措辞不同但成因相同**的 warning 归到同一个可行动的类别。
//
// # 为什么要有这张表（2026-10-05 实测）
//
// 拿 4 份实抓夹具跑真实提取器，归类出 **32 类 / 112 条**，而其中有一处**当下就
// 活着的碎裂**：
//
//	prices in this row are billed per per_minute, not per nM tokens   7
//	rejected by the proposal tool: unit is per_minute, and a baseline …  7
//
// 同一个成因（这一行的计价单位不是每百万 token）被拆成**两个**键，各 7 条。
// `warningReasonKey` 的数字归一化救不了它 —— 因为单位名是**词**不是数字。
// 5 个单位因此变成 10 个键，4 个档位同理变成 8 个键。
//
// 后果不是「多了一行」：要答「因为单位是 per_minute 被拒的有多少条」的人必须自己
// 7+7，而输出里**没有任何东西**告诉他这是一类。而这一列存在的全部意义就是
// 「决定先修哪一类」。
//
// # 为什么是**显式表**而不是再加一层归一化
//
// `warningReasonKey` 已经是在文本启发式上叠归一化（截破折号、剥括注、数字换 n、
// 去 and）。再加一层「单位名换 u、档位名换 t」只会让启发式更长，而它的失效方式
// 仍然是**静默的**：有人改一句措辞，键就碎成两个，两边计数各自变小，看着仍正常。
//
// 显式表把这个失效方式换掉了：表里没有的一律落到 `unclassified:` 前缀的显式桶，
// 并且计数单独汇总 —— **新增 warning 类型会变成一个看得见的桶**，而不是一个
// 悄悄长出来的类名。
//
// 这也是通往「给 warning 配稳定机器码」那一步的**小形态**：那张改造要覆盖 20 处
// 措辞的发出点；这张表只覆盖「已经能聚成可行动类别」的那些，且与发出点解耦 ——
// 措辞微调不会打碎它，只有真正的新类别才需要在这里加一行。
type reasonFamily struct {
	// Key 是进 JSON 的稳定键。改动会让 diff 里两版提案的键对不上，属于**有意的**破坏。
	Key string
	// Why 是人看的一句话：这类的共同成因与下一步动作。
	Why string
	// Match 对**归一化后的 head**（见 warningReasonKey）做子串匹配，命中任一即归入本族。
	// 刻意用子串而不是等值：归一化后同一族的措辞仍会差几个词。
	Match []string
}

var reasonFamilies = []reasonFamily{
	{
		Key: "priced in a unit that is not per-1M-tokens",
		Why: "这一行按 per_minute / per_hour / per_image / per_second / per_char / per_nk_calls 等单位计价，" +
			"而基准价列的语义是「每百万 token 的美元」。两者的量纲不同，换算需要原厂给出" +
			"每单位的 token 数 —— 那是原厂页才有的信息，抓取拿不到。",
		// 两条措辞都归到一族：行级判定与工具级判定说的是同一件事。
		Match: []string{"prices in this row are billed per ", "rejected by the proposal tool: unit is "},
	},
	{
		Key: "table is a non-standard tier (Batch / Fast / Flex / Ultrafast)",
		Why: "这张表是厂商的非标准档位而不是标准 list price，基准价必须取标准档。" +
			"下一步是找同一厂商的标准档表，或确认该模型确实只有这一档。",
		Match: []string{
			"this table's product tier is labelled ",
			"this table is the vendor's ",
		},
	},
	{
		// ★ 2026-10-05 在活的 ai.google.dev/pricing 上实测加的。
		//
		// 这一族与其它所有族的**下一步动作都不同**：其余的要么改代码、要么改抓取，
		// 这一族是**定价决策** —— 厂商说了「这个价到 X 日，之后是 Y」，而基准价列
		// 只能装一个数。用当前价还是调价后的价、还是干脆不收这个模型，这是人和
		// 成本口径的事。任何自动化挑一个，都是替厂商做了那个决策。
		Key: "cell states a price that changes on a date (baseline is a pricing decision)",
		Why: "这一格里有两个价：当前价，和某个生效日之后的价（例：$0.75 through " +
			"December 31, 2026 / $1.50 starting January 1, 2027）。基准价列的语义是" +
			"**一个**数，所以「基准价取哪个」是**定价决策**而不是解析问题。" +
			"下一步：定口径（当前挂牌价 / 调价后挂牌价 / 暂不收这个模型），" +
			"并把口径写进 SSOT 的说明里 —— 否则下一次调价又要重新决定一次。",
		Match: []string{
			"this cell states a price that changes on a date",
		},
	},
	{
		// ★ 2026-10-05 在 google-gemini.md 上实测加的：329 行、0 候选。
		//
		// 它与上面「列结构对不上」那族**必须分开**，因为两者的下一步动作相反：
		// 那族是「表头列数与角色对不上，去调列映射」；这一族是「列映射是对的，
		// 缺的是抓取没保住每模型的锚点」。合并的话，人会照着「调列映射」去改一张
		// 本来正确的表。
		Key: "per-model price block: the model name is not in the table (fetch lost the anchor)",
		Why: "这张表没有 model 列，列是计费档位（Free Tier / Paid Tier），模型身份只可能来自" +
			"章节标题。Gemini 3 家族那 250 多行的模型名在抓下来的 markdown 里**根本不存在**" +
			"（jina 转换把标题压平成了营销文案）⇒ 提取器无法把任何一行归到某个模型。" +
			"下一步**不是**改列映射：重新抓取该厂商页并保留每模型锚点，" +
			"或改从每个模型自己的锚点页取价。",
		Match: []string{
			"this table has no model column and its columns are billing dimensions",
		},
	},
	{
		Key: "price is dimension-conditional (context length / modality / tier within one table)",
		Why: "这一行或这张表的价格**有条件**（上下文长度、模态、档位），不是单一 list price。" +
			"基准价取哪个条件下的值需要人决定口径。",
		Match: []string{
			"the table header names a non-standard billing dimension",
			"the prose above this table names a non-standard billing dimension",
			"row prices a non-standard billing dimension",
			"the header names a billing dimension",
			"the top header groups those columns by billing dimension",
		},
	},
	{
		Key: "column structure is not one price per (model, in/out)",
		Why: "这张表的列结构与「一行模型 × 一个输入价一个输出价」对不上（列数不匹配、" +
			"同角色多列、多行表头）。要拿到基准价得先把列语义对齐。",
		Match: []string{
			"header does not map to both an input and an output column",
			"column count n does not match the header's n",
			"row has n extra priced column(s) of the same role",
			"the two header rows were aligned",
			"this row is a second header row: every cell is a price dimension",
		},
	},
	{
		Key:   "row is context-tiered or per-token mix",
		Why:   "这一行是上下文分档价而不是单一口径。基准价取哪一档需要人决定口径。",
		Match: []string{"row is context-tiered or per-token"},
	},
	{
		Key:   "struck-through (superseded) price in the row",
		Why:   "这一行含有被划掉的旧价，**不可用**。需要取现行价。",
		Match: []string{"row contains a struck-through price"},
	},
	// ↓ 2026-10-06 新增：规格表式（run-on）定价页的两种拒收。
	//
	// 起因是 `internal/vendorprice/runsheet.go` 那条新路径：deepseek 那一页
	// **整页没有 markdown 表格**，走块解析器得到零候选 —— 连 orphanCandidate
	// 都不触发（它只对 `|` 开头的行调用），于是整页的钱在提案里**无处可查**。
	// 新路径把它读出来了，于是「读不出来」也变成了一种必须能命名的状态。
	{
		Key: "run-on price sheet: this line's price is not attributable to a column",
		Why: "这个页面是**规格表式**（竖排 run-on，整页没有 markdown 表格），而这一行的" +
			"金额个数与页面列头声明的模型数对不上，或列归属无法证明。列错位意味着价会挂到" +
			"**另一个模型**头上 —— 比读不出价坏得多，所以这里宁可不取。下一步：要么等这个" +
			"版式有明确的列头约定，要么人工核对这一行的金额与模型顺序。",
		Match: []string{
			"this line carries money but was not parsed",
			"amount(s) but the page's",
		},
	},
	{
		Key: "run-on price sheet: price is time-of-day tiered (peak / off-peak)",
		Why: "这个页面按**时段**分档（OFF-PEAK / PEAK）发布价格。基准价列的语义是" +
			"「每百万 token 的单一 list price」，而峰谷价是**两套**挂牌价，取哪一套是" +
			"**产品决策**（成本核算要不要按时段加权？），解析器不替人做。⚠ 注意本族在" +
			"**只出现一档**时同样拒收：厂商哪天撤掉 PEAK 那一组，「峰谷价」就长得和" +
			"「挂牌价」一模一样，那时收进去的就是错价。",
		Match: []string{
			"this line names a time-of-day billing dimension",
			"this page's run-on sheet gives input and output prices on separate lines",
		},
	},
}

// classifyReason 把归一化后的 head 归到某个族。
//
// 返回 ok=false 表示「表里没有这一类」—— 调用方**必须**把它落到显式的
// unclassified 桶，而不是拿 head 本身当类名（那正是 2026-10-05 之前的行为：
// 一个新措辞会悄悄长出一个新类名，而它的计数小得看不出异常）。
func classifyReason(head string) (key, why string, ok bool) {
	for _, f := range reasonFamilies {
		for _, m := range f.Match {
			if strings.Contains(head, m) {
				return f.Key, f.Why, true
			}
		}
	}
	return "", "", false
}

// groupWarnings 按 warning 的成因归类。
//
// 三层，缺一层就会静默碎裂：
//
//  1. **归一化**（warningReasonKey）：截解释、剥括注、数字换 n、去 and。
//  2. **归族**（classifyReason）：把措辞不同、成因相同的 warning 并成一类。
//     没有这一层时，同一个「单位不是 per-1M token」的成因会按行级判定与工具级
//     判定拆成两个键（2026-10-05 实测：32 类里光这一处就多出 5 个键）。
//  3. **未归族显式化**：表里没有的一律用 `unclassified: ` 前缀单独成桶。
//     刻意**不**拿 head 当类名 —— 那正是碎裂的来源。
func groupWarnings(cands []vendorprice.Candidate, maxExamples int) []rejectionReason {
	counts := map[string]int{}
	examples := map[string][]string{}
	why := map[string]string{}

	for _, c := range cands {
		// 一条候选可能因为同一个理由命中多次（不同列/不同表），
		// 按**去重后的理由集**计数，否则计数会被重复放大。
		seen := map[string]bool{}
		for _, w := range c.Warnings {
			head := warningReasonKey(w)
			if head == "" {
				continue
			}
			key, reason, ok := classifyReason(head)
			if !ok {
				key = unclassifiedPrefix + head
			} else {
				why[key] = reason
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			counts[key]++
			if len(examples[key]) < maxExamples {
				examples[key] = append(examples[key], c.Model)
			}
		}
	}

	out := make([]rejectionReason, 0, len(counts))
	for reason, n := range counts {
		out = append(out, rejectionReason{
			Reason: reason, Count: n, Examples: examples[reason], Why: why[reason],
		})
	}
	// 排序：先按数量降序（最该先修的在最前），同数按原因名稳定排序，
	// 否则每次运行顺序都变，diff 出来的两版提案没法比。
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Reason < out[j].Reason
	})
	return out
}

// unclassifiedPrefix 标出「成因表里没有」的那些类。
//
// 它存在是为了让**新增的 warning 类型变成一个看得见的桶**。没有前缀时，一个新
// 措辞会悄悄用 head 自己的句子当类名，计数通常很小，读提案的人看不出异常。
const unclassifiedPrefix = "unclassified: "

// warningReasonKey 取 warning 的前半句，作为归类键。
//
// 三处归一化，缺一个就会把一类拆成两类：
//
//  1. 截到第一个 " — "（陈述 vs 解释的分界，见 groupWarnings 的约定说明）。
//
//  2. 剥掉括注 —— 里面的值是变量：…"dimension (Short context)" 与
//     …"dimension (Long context)" 属于同一类。
//
//  3. **把数字串换成 n** —— 实测踩到的："row has 4 extra priced column(s)" 与
//     "row has 3 extra priced column(s)"、"column count 2 does not match the
//     header's 4" 与 "…3…5" 原本各被拆成独立的一类。而拆开之后**每一类的
//     计数都变小，看着仍「正常」**，于是没人发现归类已经碎了。
//
//  4. 去掉开头的 "and "：有些 warning 是接在别的 warning 之后的续写
//     （"…these prices are conditional, not a flat list price" 前面那句），
//     以连词开头当类名读起来像半句话。
var digitsRunRE = regexp.MustCompile(`\d+`)

func warningReasonKey(w string) string {
	head := w
	if i := strings.Index(head, " — "); i >= 0 {
		head = head[:i]
	}
	head = strings.TrimSpace(head)
	if i := strings.Index(head, " ("); i >= 0 {
		head = strings.TrimSpace(head[:i])
	}
	head = digitsRunRE.ReplaceAllString(head, "n")
	head = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(head), "and "))
	return head
}

// corroborated 是一条过了两道关的条目。
type corroborated struct {
	Vendor      string        `json:"vendor"`
	DisplayName string        `json:"display_name"`
	Canonical   string        `json:"canonical"`
	MatchScore  float64       `json:"match_score"`
	VendorPage  *baselineSide `json:"vendor_page"`
	Observed    *baselineSide `json:"observed"`
	Verdict     string        `json:"verdict"`
	SourceURL   string        `json:"source_url"`
	ObservedURL string        `json:"observed_source_url"`
	// ★ 下面三个字段是 2026-10-05 补的，补的理由是**接缝**：
	//
	// corroborated 是整个提案里**唯一**带 canonical 名的一节（ready_to_review
	// 只有厂商展示名），也就是唯一能当 SSOT 键的那一节。而它此前只带
	// input/output —— 没有币种、没有缓存读写价。⇒ 证据最强的这一节**恰好写不
	// 出**一条能通过 bg.BaselinePrice.validate 的 SSOT 条目（validate 要求
	// currency 非空，而 2026-10-05 起 currency 必填）。
	//
	// 数据本来就在手上（构造这一节的 Candidate 上四个价 + Currency 都有），
	// 只是没往结构体里带。
	Currency  string   `json:"currency,omitempty"`
	CacheRead *float64 `json:"cache_read_per_1m,omitempty"`
	CacheWrit *float64 `json:"cache_write_per_1m,omitempty"`
	// Row / LineNo 是厂商页上那几行。带下来，人核对时能直接跳到原文，
	// 不用在整页 markdown 里搜模型名。
	Row    string `json:"row,omitempty"`
	LineNo int    `json:"line_no,omitempty"`
}

type baselineSide struct {
	Input  *float64 `json:"input_per_1m,omitempty"`
	Output *float64 `json:"output_per_1m,omitempty"`
}

// ---------------------------------------------------------------------------
// 提案 → SSOT 草稿
// ---------------------------------------------------------------------------

// draftCatalog 故意**复制** bg.baselineCatalogFile 的形状，而不是 import bg：
// 那个包是运行时库，本工具是一次性 CLI，import 它会把整个网关拖进构建。
// 代价是形状有两份 —— 所以由**本包**的 draft_ssot_test.go
// （TestDraftIsAcceptedByTheAuthoritativeGate）把本工具的输出喂给 bg 的
// BaselinePrice 与 bg 自己的 Validate，把「两份形状一致」变成被测出来的事实。
//
// ⚠ 位置订正（2026-10-06）：这段注释此前写的是 `bg/draft_ssot_test.go`，
// 而**那个文件不存在**（全仓只有这一行提到它）。它之所以在**本包**而不在 bg
// 包里，有个很直接的理由：判据 import 了 bg，所以它属于「能 import bg 的
// 那一侧」。指错位置的真实代价是它把找它的下一个人引向一个空目录 ——
// 我自己为此白找了三轮。
//
// ⚠ 而「形状一致」与「这一份草案可入库」不是同一件事：上面那条判据用的是
//
//	`proposalWith(2)` 构造出来的提案，验的是接缝；真跑 raw/ 的那份产物要走
//	`bg` 的**真加载器**（loadBaselineCatalog，内部逐条 validate）。后者由
//	`bg/ssot_draft_ingest_test.go` 按需验（设 LLM_GATEWAY_SSOT_DRAFT 才跑）。
type draftCatalog struct {
	GeneratedAt string `json:"generated_at"`
	// Draft 恒为 true：这份文件是**草稿**，不是权威面。权威面是
	// bg/data/model_baseline_prices.json，它要由人逐条确认后合入。
	Draft   bool                 `json:"draft"`
	Refused []refusedEntry       `json:"refused,omitempty"`
	Models  map[string]draftLine `json:"models"`
	// Why 恒非空。**空草稿必须能自辩**：models 为空有两种完全不同的原因 ——
	// 「原厂页上一条可用价都没有」和「互证根本没跑成」，而只看 models 分不出来。
	// 后者尤其危险：人打开一个空草稿会读成「这个 vendor 没有价」，于是去填 SSOT
	// 或干脆放弃这一家 —— 而真相是互证源没连上。
	Why string `json:"why"`
}

type draftLine struct {
	InputPer1M      *float64 `json:"input_per_1m"`
	OutputPer1M     *float64 `json:"output_per_1m"`
	CacheReadPer1M  *float64 `json:"cache_read_per_1m"`
	CacheWritePer1M *float64 `json:"cache_write_per_1m"`
	Currency        string   `json:"currency"`
	Vendor          string   `json:"vendor"`
	Source          string   `json:"source"`
	SourceURL       string   `json:"source_url"`
	FetchedAt       string   `json:"fetched_at"`
}

type refusedEntry struct {
	Canonical string `json:"canonical"`
	Display   string `json:"display_name,omitempty"`
	Reason    string `json:"reason"`
}

// buildDraft 把互证通过的那一节转成 SSOT 草稿。
//
// ★ 每一处「不能确定」都是**拒收并点名**，不是省略、更不是编造：
//
//	· 缺 --fetched-at ⇒ 全部拒收。快照文件名是 {vendor}.md（无日期），
//	  文件 mtime 不是「何时从原厂页取到的」这个事实 —— 用 mtime 填它，
//	  等于把「文件什么时候被复制过」写进权威面。
//	· 币种为空 ⇒ 拒收并点名这一条。与 validate 的 currency 必填同一个理由。
//	· 两源不一致（verdict=sources_disagree）⇒ 拒收：那正是要人裁决的事。
func buildDraft(p *proposal, fetchedAt string) draftCatalog {
	d := draftCatalog{
		GeneratedAt: p.GeneratedAt,
		Draft:       true,
		Models:      map[string]draftLine{},
	}
	// 严重度顺序：先说互证有没有跑成，再说有没有候选。
	switch {
	case len(p.Corroborated) == 0 && p.CorroborationRefused != "":
		d.Why = "EMPTY because corroboration did not run: " + p.CorroborationRefused +
			" — do NOT read this as \"the vendor pages have no usable price\""
	case len(p.Corroborated) == 0 && p.CanonicalList == "":
		d.Why = "EMPTY because no canonical list was supplied (-canonical), so no vendor " +
			"display name could be resolved to an SSOT key"
	case len(p.Corroborated) == 0:
		d.Why = "EMPTY because candidates were extracted but none was corroborated; read " +
			"unresolved_names in the proposal for why each name failed to resolve"
	default:
		d.Why = "each entry below was extracted from the vendor page AND agreed with an " +
			"independent observation source; a human still has to check each source_url"
	}
	for _, e := range p.Corroborated {
		refuse := func(reason string) {
			d.Refused = append(d.Refused, refusedEntry{
				Canonical: e.Canonical, Display: e.DisplayName, Reason: reason})
		}
		if fetchedAt == "" {
			refuse("no --fetched-at given: the raw snapshots are named {vendor}.md with no date, " +
				"so the fetch time is a fact only a human can state. Refusing rather than " +
				"substituting the file mtime, which records when the file was copied, not fetched")
			continue
		}
		if e.Verdict != "corroborated" {
			refuse("verdict is " + e.Verdict + ": the vendor page and the machine-readable " +
				"source disagree, which is exactly the decision a human has to make")
			continue
		}
		if e.Currency == "" {
			refuse("the candidate carries no currency. A price whose currency is unknown cannot " +
				"be compared against a supplier price, and guessing USD states a fact the vendor " +
				"page never said")
			continue
		}
		if e.VendorPage == nil || e.VendorPage.Input == nil || e.VendorPage.Output == nil {
			refuse("the candidate has no input/output price, so there is nothing to record")
			continue
		}
		if _, dup := d.Models[e.Canonical]; dup {
			refuse("another corroborated entry already claims this canonical name; the SSOT is " +
				"one price per model, so a second write would silently overwrite the first")
			continue
		}
		d.Models[e.Canonical] = draftLine{
			InputPer1M: e.VendorPage.Input, OutputPer1M: e.VendorPage.Output,
			CacheReadPer1M: e.CacheRead, CacheWritePer1M: e.CacheWrit,
			Currency: e.Currency, Vendor: e.Vendor,
			Source:    "vendor pricing page row " + strconv.Itoa(e.LineNo) + ": " + e.Row,
			SourceURL: e.SourceURL, FetchedAt: fetchedAt,
		}
	}
	return d
}

// unresolved 是解析不出 canonical 名的展示名。
type unresolved struct {
	Vendor      string  `json:"vendor"`
	DisplayName string  `json:"display_name"`
	BestScore   float64 `json:"best_score"`
	Reason      string  `json:"reason"`
}

func main() {
	rawDir := flag.String("raw", "docs/02-resources/research/pricing/raw", "dir with the vendor page markdown snapshots")
	out := flag.String("out", "", "write the proposal JSON here (default: stdout)")
	emitSSOT := flag.String("emit-ssot", "",
		"also write an SSOT draft from the corroborated set to this path (never the SSOT itself)")
	fetchedAt := flag.String("fetched-at", "",
		"RFC3339 timestamp of when the vendor pages were fetched; required by -emit-ssot")
	canonicalFile := flag.String("canonical", "",
		"file with one models_canonical.canonical_name per line; enables display-name resolution")
	maxSnapshotAge := flag.Float64("max-snapshot-age-days", 30,
		"refuse a snapshot whose content (per its own Published Time header) is older than this, "+
			"measured against -fetched-at; 0 disables the gate. r.jina.ai serves cached renders "+
			"with HTTP 200 and self-consistent content, so without this a stale snapshot yields "+
			"a wrong price that looks completely normal")
	proseFootnote := flag.Bool("accept-dimension-prose-as-footnote", false,
		"treat a billing-dimension word in the prose above a table as a footnote about the "+
			"PRECEDING table instead of a declaration about this one, so the table is read. "+
			"Off by default: the two cases are positionally identical on real pages (MiniMax's "+
			"'Priority ... 1.5x standard' is a footnote; anthropic's 'Fast mode pricing ...' is a "+
			"declaration), so only a human who has looked at the page may turn this on. The "+
			"dimension word is still recorded in the output either way")
	corroborate := flag.Bool("corroborate", false,
		"also fetch the machine-readable source and cross-check each price (network)")
	flag.Parse()

	vendorOptions := vendorprice.Options{}
	if *proseFootnote {
		vendorOptions.ProseDimension = vendorprice.ProseDimensionAcceptWithWarning
	}

	// 断言的抓取时间先解析出来：新鲜度门要拿它与快照自己声明的发布时间做落差。
	// 解析失败不致命 —— 门会把这种情况判成「无法交叉校验」并拒收（见
	// judgeSnapshotAge：fetchedAt 为零值时返回 ageStale），而不是默认放行。
	var assertedFetch time.Time
	if *fetchedAt != "" {
		parsed, perr := time.Parse(time.RFC3339, *fetchedAt)
		if perr != nil {
			fmt.Fprintf(os.Stderr, "[fetched-at] %q is not RFC3339: %v — the snapshot freshness "+
				"gate cannot cross-check it, so every dated snapshot will be REFUSED rather than "+
				"taken on trust\n", *fetchedAt, perr)
		} else {
			assertedFetch = parsed
		}
	}

	entries, err := os.ReadDir(*rawDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read raw dir: %v\n", err)
		os.Exit(1)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	p := proposal{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Notice: "未经人确认的价格不得进 SSOT。ready_to_review 里的条目仍需逐条对照 " +
			"source_url 核对；needs_human_eyes 里的条目**不可用**，原因见 warnings；" +
			"rejection_reasons 是 needs_human_eyes 的按原因归类，用来决定先修哪一类。" +
			"⚠ ambiguous_canonical_prices 是**既不在上面两栏、也没被丢弃**的条目：" +
			"同一个 canonical 有两个不同的价，SSOT 一个模型只有一个价，所以整组扣下等人裁决" +
			"（counts_by_confidence 仍把它们算在 table_row 里，别拿它当 ready 的条数）。⚠ " +
			rejectionReasonOverlapNote + "；要答「共多少条不可用」请用 counts_by_confidence。",
		Reviewed: false,
		Counts:   map[string]int{},
	}

	// livePages 并进查找表：文件名冲突时以 vendorPage 为准。
	pageOf := func(name string) (struct{ Vendor, URL string }, bool) {
		if vp, ok := vendorPage[name]; ok {
			return vp, true
		}
		vp, ok := livePages[name]
		return vp, ok
	}

	for _, name := range names {
		vp, known := pageOf(name)
		if !known {
			// 认识不了快照就不猜厂商与 URL：宁可不提，也不给一个错的出处。
			fmt.Fprintf(os.Stderr,
				"[skip] %s: not an originating vendor in the map — refusing to invent a "+
					"source_url (an aggregator/relay price is a SUPPLIER price, not a baseline)\n", name)
			continue
		}
		raw, err := os.ReadFile(filepath.Join(*rawDir, name))
		if err != nil {
			fmt.Fprintf(os.Stderr, "[skip] %s: %v\n", name, err)
			continue
		}
		// ★ 快照新鲜度门（2026-10-06）。放在 Extract **之前**：内容过期的快照
		//   解析得再对也是**错价**，而错价比「没有价」坏得多 —— 后者会触发
		//   baseline_price_missing 去报，前者会安静地进账本。
		if *maxSnapshotAge > 0 {
			verdict, published, ageDays := judgeSnapshotAge(raw, assertedFetch, *maxSnapshotAge)
			switch verdict {
			case ageStale:
				fmt.Fprintf(os.Stderr, "[stale] %s\n", describeSnapshotAge(verdict, published, ageDays, name))
				p.StaleSnapshots = append(p.StaleSnapshots, snapshotNote{
					File: name, Verdict: "stale", ContentAgeDays: round1(ageDays),
					Published: fmtTime(published),
				})
				continue
			case ageNoEvidence:
				fmt.Fprintf(os.Stderr, "[no-evidence] %s\n", describeSnapshotAge(verdict, published, ageDays, name))
				p.StaleSnapshots = append(p.StaleSnapshots, snapshotNote{
					File: name, Verdict: "no_published_time",
				})
			}
		}
		for _, c := range vendorprice.ExtractWithOptions(vp.Vendor, vp.URL, raw, vendorOptions) {
			c = applyUnitGate(c)
			p.Counts[c.Confidence]++
			switch c.Confidence {
			case vendorprice.ConfidenceTableRow:
				p.ReadyToReview = append(p.ReadyToReview, c)
			default:
				p.NeedsHumanEyes = append(p.NeedsHumanEyes, c)
			}
		}
	}

	var canon canonicalList
	if *canonicalFile != "" {
		cl, err := readCanonicalNames(*canonicalFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read canonical list: %v\n", err)
			os.Exit(1)
		}
		canon = cl
		// 名单完整性是 margin 判据的前提，残缺会让「自信地解析错」变成默认结果。
		if cl.DeclaredCount > 0 && cl.DeclaredCount != len(cl.Names) {
			fmt.Fprintf(os.Stderr,
				"[canonical] WARNING: file declares %d names but %d were read — the file looks "+
					"truncated. A partial list makes the margin test meaningless (a missing rival is "+
					"not evidence of a win), so every resolution below is unverified.\n",
				cl.DeclaredCount, len(cl.Names))
		}
		if !cl.Verified() {
			fmt.Fprintf(os.Stderr,
				"[canonical] no provenance header in %s. Display-name resolution still runs, but "+
					"--corroborate is REFUSED: with an unverified list a wrong canonical silently "+
					"looks up a DIFFERENT model in the observation source, and \"two independent "+
					"sources agree\" would then be false. Add a header:\n"+
					"    # source: models_canonical @ <host> (SELECT canonical_name FROM models_canonical)\n"+
					"    # exported_at: 2026-10-04T14:00:00Z\n"+
					"    # count: 1234\n", *canonicalFile)
		} else {
			fmt.Fprintf(os.Stderr, "[canonical] %s\n", canon.Provenance())
		}
		if dups := nearDuplicateWarnings(cl.Names); len(dups) > 0 {
			fmt.Fprintf(os.Stderr,
				"[canonical] WARNING: %d group(s) of near-duplicate canonical names — the same model "+
					"written more than one way. These are the only input that drives the margin rule to "+
					"collapse to 0.00, where the winner is decided by alphabetical tie-break rather than "+
					"evidence. Dedupe the export; do not rely on the margin guard:\n", len(dups))
			for _, d := range dups {
				fmt.Fprintf(os.Stderr, "    %s\n", d)
			}
			p.CanonicalDuplicates = dups
		}
		ready := p.ReadyToReview
		p.ReadyToReview = nil
		for _, c := range ready {
			resolved, unres := resolveCanonical(c, cl.Names)
			if resolved == nil {
				p.Unresolved = append(p.Unresolved, unres)
				continue
			}
			p.ReadyToReview = append(p.ReadyToReview, *resolved)
		}
		// ★ 撞名不变量必须在**互证之前**：SSOT 的键是 canonical_name，
		//   同一个名字带两个不同的价进来，后面没有任何一道门能把它变回
		//   「一个模型一个价」——互证只会给两条都盖上「corroborated」。
		p.ReadyToReview, p.AmbiguousCanonical = withholdConflictingPrices(p.ReadyToReview)
		if len(p.AmbiguousCanonical) > 0 {
			for _, c := range p.AmbiguousCanonical {
				fmt.Fprint(os.Stderr, c.Report())
			}
			fmt.Fprintf(os.Stderr,
				"[price-conflict] %d canonical name(s) withheld from ready_to_review because the "+
					"vendor page(s) give them more than one price; see ambiguous_canonical_prices "+
					"in the proposal\n", len(p.AmbiguousCanonical))
		}
	}

	if *canonicalFile != "" {
		p.CanonicalList = canon.Provenance()
	}
	if *corroborate {
		switch {
		case *canonicalFile == "":
			p.CorroborationRefused = "no --canonical list was supplied, so display names were " +
				"never resolved to canonical names; the observation source is keyed by canonical " +
				"name, so a lookup by display name would be meaningless"
		case !canon.Verified():
			p.CorroborationRefused = "the canonical list carries no provenance header, so its " +
				"completeness is unknown. A wrong canonical would look up a DIFFERENT model in the " +
				"observation source and produce a false \"two independent sources agree\" verdict. " +
				"This is the exact shape of bug the baseline-price SSOT must not accept."
		default:
			if err := crossCheck(&p); err != nil {
				fmt.Fprintf(os.Stderr, "corroboration skipped: %v\n", err)
				p.CorroborationRefused = "the observation source could not be read: " + err.Error()
			}
		}
	}

	// 归类必须在**互证之后**算：互证会把 ready_to_review 里的条目挪进
	// needs_human_eyes（没解析出 canonical 名的那些），那些理由只有挪进去之后
	// 才存在。先算就会漏掉它们。
	p.RejectionReasons = groupWarnings(p.NeedsHumanEyes, 3)

	body, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal proposal: %v\n", err)
		os.Exit(1)
	}
	body = append(body, '\n')

	// SSOT 草稿与提案**一起**落盘：只写提案不写草稿，等于把最后一段
	// 「从判词到权威记录」的路留给手抄，而那段路没有任何东西守着。
	if *emitSSOT != "" {
		draft := buildDraft(&p, *fetchedAt)
		draftBody, err := json.MarshalIndent(draft, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "marshal ssot draft: %v\n", err)
			os.Exit(1)
		}
		draftBody = append(draftBody, '\n')
		if err := os.WriteFile(*emitSSOT, draftBody, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write ssot draft: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "ssot draft written to %s: %d entr(ies), %d refused\n",
			*emitSSOT, len(draft.Models), len(draft.Refused))
		for _, r := range draft.Refused {
			fmt.Fprintf(os.Stderr, "  refused %s: %s\n", r.Canonical, r.Reason)
		}
	}

	if *out == "" {
		_, _ = os.Stdout.Write(body)
		return
	}
	if err := os.WriteFile(*out, body, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write proposal: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "proposal written to %s\n", *out)
	fmt.Fprintf(os.Stderr, "counts by confidence: %v\n", p.Counts)
}

// applyUnitGate 在**消费侧**独立复核单位。
//
// 提取器（internal/vendorprice）已经把非 token 单位判成 unusable，这里再判
// 一次，因为这是唯一会把候选带进 SSOT 形状的出口。两道判据互不代替：提取器
// 改版、页面改版、或将来有人放宽提取器口径时，这一道还在。
//
// 为什么值得单独一道：一张图的 $0.002 填进「每 1M token」的价格列，偏差是
// 四个数量级，而它在提案 JSON 里**看起来和真价没有任何区别**——字段名是
// `input_per_1m`，值是 0.002，没有一处写着「这其实是每张图」。
//
// 注意它拦的是**单位已知且不是 token**。单位为空（""）时放行：提取器在
// 页面与单元格都没说单位时给出空串，那种情况它自己就判了 unusable。
func applyUnitGate(c vendorprice.Candidate) vendorprice.Candidate {
	if c.Unit == "" || c.Unit == vendorprice.UnitPer1M {
		return c
	}
	c.Confidence = vendorprice.ConfidenceUnusable
	c.Warnings = append(c.Warnings,
		"rejected by the proposal tool: unit is "+c.Unit+
			", and a baseline price column holds USD per 1M tokens")
	return c
}

// canonicalList 是 --canonical 指向的名单，连同它的**出处**。
type canonicalList struct {
	Names []string
	// Source 是名单从哪来、什么时候导出的（来自文件头的元数据）。
	// 空串 = 名单没有出处。
	Source string
	// ExportedAt 同上。
	ExportedAt string
	// DeclaredCount 是文件头里声明的条数，用来发现「头写了 300 条、
	// 实际只解析出 12 条」这种截断。
	DeclaredCount int
}

// Provenance 返回一行人类可读的名单出处，写进提案文件。
func (c canonicalList) Provenance() string {
	if c.Source == "" && c.ExportedAt == "" {
		return "NONE — this file carries no provenance header; see the notice at the top of this proposal"
	}
	return fmt.Sprintf("%s (exported %s, %d names)", c.Source, c.ExportedAt, len(c.Names))
}

// Verified 名单是否可信到可以用来做双源互证。
func (c canonicalList) Verified() bool { return c.Source != "" }

// canonicalHeaderRE 认文件头里的出处声明。
//
// **为什么必须有它**：margin 判据（最高分比第二名高 0.05）只有在清单
// **完整**时才成立。清单缺条目时，margin 不是「有证据的领先」，而是
// 「对手不在场」——
//
//	完整清单：[claude-opus-4-8, claude-opus-4-7, …]  "Claude Opus 5.5" → 0.90 vs 0.88，margin 0.02 ⇒ 报歧义
//	残缺清单：[claude-sonnet-4, gpt-5, deepseek-v4]   "Claude Opus 5.5" → 自信地挂到某一个上
//
// 仓里 tests/local/models/canonical_models.py 就是这样一份残缺清单（9 个
// canonical 名，且是 claude-sonnet-4 / gpt-5 这种旧代）。实测拿它解析当前
// 页面上的 7 个展示名：**7 个全部落到 unresolved**（best match 0.60/0.84，
// 低于 0.90 下限）——也就是说下限挡住了「自信地挂错」。
//
// 但**不可审计**这一点无论如何都在：提案会被拷到工单、issue、聊天里，脱离本
// 工具的上下文，而文件里没有一行写着「这批 canonical 判词是用哪份名单、什么
// 时候导出的算出来的」。半个月后没人能复查。
//
// 所以规则是：没出处的名单可以拿来看，**但不许用来出「双源互证」判词**——
// canonical 一旦错了，观察源里查到的就是**另一个模型**，而 models.dev 对很多
// 模型都有价，于是「两个独立信源都说这个数」会是**假的一致**，而 SSOT 只收
// 互证过的条目。这是「看起来被核对过」那一类错里最贵的一种。
var (
	canonicalHeaderRE   = regexp.MustCompile(`(?i)^#\s*source\s*:\s*(.+)$`)
	canonicalExportedRE = regexp.MustCompile(`(?i)^#\s*exported_at\s*:\s*(.+)$`)
	canonicalCountRE    = regexp.MustCompile(`(?i)^#\s*count\s*:\s*(\d+)\s*$`)
)

// nearDuplicateRE 把 canonical 名折叠成「忽略形态差异」的比较键。
//
// 只做大小写与 .-_ 的折叠，**不做语义归一**（那是 modelname 的活）。它要回答的
// 只有一个问题：这两行会不会拿到同一个展示名？
var nearDuplicateRE = regexp.MustCompile(`[._-]`)

// nearDuplicateKey 生成比较键。
func nearDuplicateKey(name string) string {
	return nearDuplicateRE.ReplaceAllString(strings.ToLower(name), "-")
}

// nearDuplicateWarnings 找出名单里「同一个模型的两种写法」。
//
// 为什么值得单独报：这类重复是 margin 判据唯一的触发条件（见 resolveCanonical
// 的注释 (2)），而它的后果是**字母序 tie-break** 决定价挂到哪一行。所以
// 「margin 挡住了」只是一个症状，根因在名单——把根因报出来，人去修名单，
// 而不是下次再靠 margin 挡一次。
func nearDuplicateWarnings(names []string) []string {
	groups := map[string][]string{}
	for _, n := range names {
		k := nearDuplicateKey(n)
		groups[k] = append(groups[k], n)
	}
	var keys []string
	for k, g := range groups {
		if len(g) > 1 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		g := groups[k]
		sort.Strings(g)
		out = append(out, fmt.Sprintf("%s: %s", k, strings.Join(g, ", ")))
	}
	return out
}

// readCanonicalNames 读 canonical 名清单（每行一个，# 开头为注释/元数据）。
//
// 元数据三件套（source / exported_at / count）都是可选的，但**有没有**决定
// 这份名单能不能用来出互证判词——见 canonicalHeaderRE 的注释。
func readCanonicalNames(path string) (canonicalList, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return canonicalList{}, err
	}
	var cl canonicalList
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			switch {
			case canonicalHeaderRE.MatchString(line):
				cl.Source = strings.TrimSpace(canonicalHeaderRE.FindStringSubmatch(line)[1])
			case canonicalExportedRE.MatchString(line):
				cl.ExportedAt = strings.TrimSpace(canonicalExportedRE.FindStringSubmatch(line)[1])
			case canonicalCountRE.MatchString(line):
				cl.DeclaredCount, _ = strconv.Atoi(canonicalCountRE.FindStringSubmatch(line)[1])
			}
			continue
		}
		cl.Names = append(cl.Names, line)
	}
	return cl, nil
}

// resolveCanonical 把厂商展示名解析成 models_canonical.canonical_name。
//
// 为什么必须走 modelname.BestStandardModelMatch 而不是自己写个
// 「空格换横线、点换横线」的函数：仓里两个规范化函数都**明确声明不做**
// 这件事（modelname/normalize.go 的包注释：it does NOT convert
// "claude-opus-4-6" ↔ "claude-opus-4.6"）。"Claude Opus 4.8" 到
// "claude-opus-4-8" 的映射是跨形态的，只能由仓自己的匹配器带着
// **真实的 canonical 名清单**来判，而且它给得出分数——分数低就该报给人，
// 不该硬认。
// 展示名 → canonical 的接受判据是**两个条件同时成立**，缺一不可：
//
//  1. 最高分不低于 resolutionScoreFloor —— 低于它连相关性都不够；
//  2. 最高分比第二名高出至少 resolutionMargin —— **这一条才是关键**。
//
// ★ 两道判据各自的承重范围（2026-10-04 实测，不是推演）
//
// 拿真实展示名 × 真实 canonical 名单量了匹配器，结论和「margin 是关键那条」
// 这个直觉**不一样**，也不像它的反面那么绝对。分两种名单：
//
// (1) 干净、去重的名单 —— 承重的是**下限** 0.90：
//
//	"Claude Opus 4.8" → 0.90 claude-opus-4-8  | 2nd 0.68  margin 0.22
//	"GPT-4o mini"     → 0.90 gpt-4o-mini      | 2nd 0.84  margin 0.06
//	"GPT-5 Codex"     → 0.90 gpt-5-codex     | 2nd 0.84  margin 0.06
//
//	最小 margin 是 0.06，**没有一个组合落到 0.05 以下**。名单被截断时也不会
//	「自信地挂错」：只有 claude-opus-4-8 时，"Claude Fable 5" ⇒ no match(0.60)，
//	不是错挂到 opus。所以在这个场景下 margin 是纵深防御。
//
// (2) 名单里**同时存在同一个模型的两种形态** —— 承重的是**margin**：
//
//	仓里 modelname/normalize.go 明确声明**不做** "claude-opus-4-8" ↔
//	"claude-opus-4.8" 的跨形态归一。于是只要 models_canonical 里两行都存在：
//
//	名单 [claude-opus-4-8, claude-opus-4.8]  "Claude Opus 4.8"
//	  → 0.90 "claude-opus-4-8"  /  0.90 "claude-opus-4.8"   margin 0.00
//
//	两个同分，胜负由**字母序 tie-break** 决定（`-` < `.`），与证据无关。
//	没有 margin 这条，价就挂在其中一行、另一行空着——而人在提案里看到的是
//	「已解析，0.90 分」。这正是本工具最早撞上的那一类错。
//
// ⇒ **两道都不能动**：下限挡住「名字对不上」，margin 挡住「同一个模型两种
// 写法」。放松任何一道都是错价入口。导出名单前先去重（见 nearDuplicateWarnings）。
//
// margin 的另一面：真正有歧义的展示名被**报成 unresolved 并附上第二名**，
// 人看一眼就能裁决，而机器不替它猜。
const (
	resolutionScoreFloor = 0.90
	resolutionMargin     = 0.05
)

// resolutionForm 是**一个**送去打分的展示名形态，以及它的判词。
type resolutionForm struct {
	ident     string // 实际送去 MatchStandardModels 的字符串
	dropped   string // 被剥掉的尾部注解内容（空串 = 原样形态）
	canonical string
	score     float64
	accepted  bool
	reason    string // 未被接受时的原因（人可读）
}

// trailingQualifier 剥掉展示名**尾部成对**的 (...) 注解，返回注解内容与剩下的
// 标识。第二个返回值为 false 表示「没有可剥的尾注解」。
//
// 括号用深度扫描配对，不是一个 `strings.Index`。
//
// ★ 如实标注：这是**防御**，不是对已观测问题的修复。2026-10-05 实测真实总体
//
//	（提案里 2,002 条带尾注解的展示名），深度扫描与朴素首括号两种配对
//	**结果完全相同，0 处差异** —— 尾部那一对总是全串的第一个 `(`。
//	两者才会分叉的形状是 `Model (preview) Opus 4 (deprecated)` 这种「名字自己
//	里就带括号」，真实页面里 0 例。选深度扫描是因为它按构造正确、且分叉时
//	方向是"剥最后一对"（注解）而不是"剥第一对"（可能劈坏名字），代价 20 行。
//	下面的判据里有**一个能分开两种配对的样本**，所以这个选择是被钉住的，
//	不是碰巧——否则哪天有人换成朴素写法，判据不会响，那才是真的静默。
//
// 刻意**只剥一层、且只剥尾部**：剥多层等于开始猜厂商的命名习惯，而那种猜错
// 的后果是价挂到错的 canonical 上。
func trailingQualifier(display string) (qualifier, ident string, ok bool) {
	s := strings.TrimSpace(display)
	if !strings.HasSuffix(s, ")") {
		return "", s, false
	}
	depth := 0
	for i := len(s) - 1; i >= 0; i-- {
		switch s[i] {
		case ')':
			depth++
		case '(':
			depth--
			if depth == 0 {
				// 位置 0 意味着整串就是一个括号（如 "(preview)"），
				// 剥完什么都不剩 —— 那不是注解，是整个名字。
				if i == 0 {
					return "", s, false
				}
				inner := strings.TrimSpace(s[i+1 : len(s)-1])
				if inner == "" {
					return "", s, false
				}
				return inner, strings.TrimSpace(s[:i]), true
			}
		}
	}
	// 括号不成对：不动它。
	return "", s, false
}

// buildResolutionForms 列出要打分的形态：原样，以及（若有）剥掉尾注解后的形态。
//
// ★ 为什么把「哪个括号是注解」的决定权交给打分器，而不是提取器或关键词表：
//
// 实测（2026-10-05，真实 960 名单 × 真实页面）里带括号的展示名有 67 条，
// 而**决定它是不是模型名的一部分的是名单，不是词表**：
//
//	Claude Haiku 3.5 (retired, except on Bedrock and Vertex AI)
//	    原样 0.84 拒 → 剥掉 0.90 claude-haiku-3-5   ← 注解，剥对了
//	Lyria 3 Clip Preview (30s)
//	    原样 0.84 拒 → 剥掉 0.90 lyria-3-clip-preview ← 看着像产品名的一部分，
//	                                                      剥掉**也是对的**
//
// 我原本以为 `(30s)` 这种是产品名的一部分、剥了会造出另一个产品，量完之后
// 这个担心不成立：目录里就有 `lyria-3-clip-preview`，剥对了。
//
// ⇒ 关键词表（"deprecated"/"retired"/"limited availability" 算注解，
// "30s"/"Full Song" 不算）会在下一个厂商换一种措辞时静默失效，而且失效方向
// 是「该剥的没剥」——价永远进不了 SSOT。形态交给分数，两种情形都判对。
func buildResolutionForms(display string) []resolutionForm {
	forms := []resolutionForm{{ident: strings.TrimSpace(display)}}
	if qualifier, ident, ok := trailingQualifier(display); ok {
		forms = append(forms, resolutionForm{ident: ident, dropped: qualifier})
	}
	return forms
}

// scoreResolutionForm 对**一个**形态跑那两道判据（下限 + margin）。
func scoreResolutionForm(f resolutionForm, canonicalNames []string) resolutionForm {
	ranked := modelname.MatchStandardModels(f.ident, canonicalNames)
	if len(ranked) == 0 {
		f.reason = "no canonical name reached the match score floor"
		return f
	}
	best := ranked[0]
	f.canonical, f.score = best.Name, best.Score
	if best.Score < resolutionScoreFloor {
		f.reason = fmt.Sprintf("best match %q scored %.2f, below the %.2f floor",
			best.Name, best.Score, resolutionScoreFloor)
		return f
	}
	if len(ranked) > 1 {
		runnerUp := ranked[1]
		if best.Score-runnerUp.Score < resolutionMargin {
			f.reason = fmt.Sprintf("ambiguous: %q scored %.2f but %q scored %.2f "+
				"(margin %.2f < %.2f) — a human must pick",
				best.Name, best.Score, runnerUp.Name, runnerUp.Score,
				best.Score-runnerUp.Score, resolutionMargin)
			return f
		}
	}
	f.accepted = true
	return f
}

// formLabel 把形态写成人能读的标签，用于理由与 warning。
func formLabel(f resolutionForm) string {
	if f.dropped == "" {
		return fmt.Sprintf("display name %q", f.ident)
	}
	return fmt.Sprintf("display name %q with the trailing qualifier %q dropped", f.ident, f.dropped)
}

// multiModelCanonicals 回答「这个单元格里的价格属于哪几个模型」。
//
// 判据是**逐段解析**而不是查词表：把展示名按 " / " 切开，每段各自走**原样形态**
// 的匹配器，返回解析到**不同** canonical 的那些段。返回 ≥2 个时，调用方必须拒收。
//
// 为什么不用词表（"and"、"、"、"vs" 之类）：词表要靠真实总体量出来才可信，而
// 实测全量语料（10 份实抓快照 575 条候选）里这种形态只有 anthropic Fast mode
// 那 1 条。逐段解析让「哪些段能解析」由**名单**说了算 —— 名单是本工具已有的
// 权威输入，而一份手写分隔符词表是新的猜测。
//
// 刻意**不剥尾注解**：剥注解会引入第二重猜测，而这里只需要知道「有几段是不同
// 的模型」，原样匹配足够。
func multiModelCanonicals(c vendorprice.Candidate, canonicalNames []string) []string {
	parts := strings.Split(c.Model, " / ")
	if len(parts) < 2 {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		f := scoreResolutionForm(resolutionForm{ident: part}, canonicalNames)
		if !f.accepted || seen[f.canonical] {
			continue
		}
		seen[f.canonical] = true
		out = append(out, f.canonical)
	}
	return out
}

func resolveCanonical(c vendorprice.Candidate, canonicalNames []string) (*vendorprice.Candidate, unresolved) {
	u := unresolved{Vendor: c.Vendor, DisplayName: c.Model}
	if strings.TrimSpace(c.Model) == "" {
		u.Reason = "candidate has no display name to resolve"
		return nil, u
	}
	if len(canonicalNames) == 0 {
		u.Reason = "empty canonical name list"
		return nil, u
	}
	// ★ 一个单元格点名**多个**模型 ⇒ 拒收，而不是挑一个。
	//
	// 2026-10-06 实测（anthropic 价目页 Fast mode 那张表）：
	//
	//	| Claude Opus 4.6 / Claude Opus 4.7 | $30 / MTok | $150 / MTok |
	//
	// 这一行的展示名原样进匹配器，**分数 0.90、accepted、reason 为空**，
	// 解析成 `claude-opus-4-7` —— 4.6 被静默丢掉，且提案里看不出任何异常。
	// 后果是 6 倍：那一行的 $30/$150 会成为 claude-opus-4-7 的基准价
	// （同一页 line 162 的标准价是 $5/$25），而 claude-opus-4-6 仍拿 $5/$25
	// —— 同一张页面里的两个模型被记成两套价。
	//
	// ⚠ 这条**今天还是潜伏的**：Fast mode 那张表在默认口径下被散文维度护栏
	// 拒收，只有 `-accept-dimension-prose-as-footnote` 打开时才走到这里。
	// 但那是一个开关就能引爆的雷，而 6 倍误差正好打在「准确控制模型实际成本」
	// 这个目标上 —— 所以按失败关闭处理。
	//
	// 判据用哪一段不必猜：把单元格按 " / " 切开，**逐段**走原样形态的匹配，
	// 只有当 ≥2 段各自解析到**不同**的 canonical 时才拒收。只有一段能解析、
	// 或几段都指向同一个 canonical 时照常走 —— 那不是「一行多个模型」。
	// 切不开的名字（名字里本来就有斜杠的）也不受影响。
	if names := multiModelCanonicals(c, canonicalNames); len(names) > 1 {
		u.Reason = "the vendor row names " + strconv.Itoa(len(names)) + " models (" +
			strings.Join(names, ", ") + ") inside one cell, while the baseline price column " +
			"holds ONE price per model. Attributing the row to a single one of them records a " +
			"price for a model the row says something different about; a human must split the " +
			"row (or wait for the vendor to publish one row per model)"
		return nil, u
	}

	forms := buildResolutionForms(c.Model)
	var scored []resolutionForm
	var accepted []resolutionForm
	best := resolutionForm{score: -1}
	for _, f := range forms {
		f = scoreResolutionForm(f, canonicalNames)
		if f.score > best.score {
			best = f
		}
		scored = append(scored, f)
		if f.accepted {
			accepted = append(accepted, f)
		}
	}
	u.BestScore = best.score

	// 形态只有一个时，判词必须与「只有一种形态」的老路径**逐字一致** ——
	// 下限、margin 两条判据各自的判据钉的就是那句话的形状。
	if len(scored) == 1 {
		if !scored[0].accepted {
			u.Reason = scored[0].reason
			return nil, u
		}
		return resolvedCandidate(c, scored[0]), unresolved{}
	}

	if len(accepted) == 0 {
		// 一个都没中：报**最好那一次**的分数，并把试过的形态都写出来。
		// 只写「below the floor」而不写试过什么，人会以为试的就是原样名字。
		var tried []string
		for _, f := range scored {
			tried = append(tried, f.reason)
		}
		u.Reason = fmt.Sprintf("%s; tried both forms and the best was: %s",
			strings.Join(tried, " | "), best.reason)
		return nil, u
	}

	// 两个形态都过线但指向**不同的** canonical：这是真的歧义，必须拒收。
	// 单看任一个形态都是"自信"的，所以只有并排比才能发现 —— 而这正是价会
	// 挂到错模型上的那一种。
	names := map[string]bool{}
	for _, f := range accepted {
		names[f.canonical] = true
	}
	if len(names) > 1 {
		var parts []string
		for _, f := range accepted {
			parts = append(parts, fmt.Sprintf("%s → %q at %.2f", formLabel(f), f.canonical, f.score))
		}
		u.Reason = "ambiguous: the two display-name forms cleared the floor but resolved to " +
			"DIFFERENT canonical names, so a human must pick — " + strings.Join(parts, "; ")
		return nil, u
	}

	// 两个形态都过线且指向同一个：取分数高的那个（它们必然是同一个名字）。
	win := accepted[0]
	if len(accepted) > 1 && accepted[1].score > win.score {
		win = accepted[1]
	}
	return resolvedCandidate(c, win), unresolved{}
}

// resolvedCandidate 把解析结果记进 Warnings 当作人可见的证据，不替换 Model
// 字段——原展示名要留着，回页面核对时看得见。
//
// ★ warning 的前缀 `display name %q resolved to canonical %q (score %.2f)` 是
// **跨文件的契约**，不是给人看的措辞：resolvedCanonical 靠 "resolved to
// canonical \"" 这个子串把 canonical 读回来，buildDraft 再用它当 SSOT 的键。
// 改这个前缀等于让所有已解析条目静默丢掉 SSOT 键。新信息只能**追加在后面**。
func resolvedCandidate(c vendorprice.Candidate, f resolutionForm) *vendorprice.Candidate {
	note := fmt.Sprintf("display name %q resolved to canonical %q (score %.2f)",
		c.Model, f.canonical, f.score)
	if f.dropped != "" {
		note += fmt.Sprintf("; matched on %q — the trailing %q is an annotation, not part of the name",
			f.ident, f.dropped)
	}
	c.Warnings = append(c.Warnings, note)
	return &c
}

// crossCheck 给每条已解析的候选配一个第二信源，并判它们是否一致。
//
// **它不改任何价格**，只是把「一个页面上的数」变成「两个独立信源都说这个
// 数」。一致 ⇒ 提案的价值从「一个来源」升到「互证」；不一致 ⇒ 那是给人看的
// 信号，恰恰是这个工具最该产出的东西。
func crossCheck(p *proposal) error {
	// 记下本轮**实际过手**的条数。不能用 len(p.NeedsHumanEyes) 报：那里面还
	// 混着本轮之前就在的条目（unusable 的行），拿它当「过了多少条」会把两个
	// 完全不同的数报成一个 —— 而且报得偏大，看着像干了很多。
	examined := len(p.ReadyToReview)

	observed, url, at, err := bg.FetchMachineReadablePrices(context.Background(), nil)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "[corroborate] observation source %s (%d providers, observed_at %s)\n",
		url, len(observed), at.Format(time.RFC3339))

	var kept []vendorprice.Candidate
	for _, c := range p.ReadyToReview {
		canonical := resolvedCanonical(c)
		if canonical == "" {
			// 没有 canonical ⇒ 观察源按厂商+模型名查不到（或查到的不是同一个
			// 模型）。不猜，留给人。
			//
			// ★ 必须说清「为什么落到 needs_human_eyes」：提案的 notice 写着那个
			// 桶里的条目「**不可用**」，而这一条**恰恰是好价**——它只是展示名没
			// 在 canonical 名单里找到对应行。旁边那条（no observation）反倒写了
			// 理由，只有这里一声不吭，于是「名字没匹配上」与「划线原价 / 单位
			// 不是 per 1M」这类**真不可用**在产出里长得一模一样：看提案的人只能
			// 逐条回页面才知道，而那正是这套流程要替人省掉的活。
			//
			// ⇒ 把「这不是价格的问题，是名字没解析出来」写在条目上。
			p.NeedsHumanEyes = append(p.NeedsHumanEyes, withWarning(c,
				"display name "+strconv.Quote(c.Model)+
					" did not resolve to any canonical name in the supplied list — the price may "+
					"well be correct and usable; the name is what needs a human decision "+
					"(fix the list, or add this model to models_canonical first)"))
			continue
		}
		obs, ok := observed.LookupObservation(c.Vendor, canonical)
		if !ok {
			p.NeedsHumanEyes = append(p.NeedsHumanEyes,
				withWarning(c, "no observation for "+c.Vendor+"/"+canonical+
					" in the machine-readable source — single-sourced, not corroborated"))
			continue
		}
		entry := corroborated{
			Vendor: c.Vendor, DisplayName: c.Model, Canonical: canonical,
			MatchScore: resolutionScoreOf(c),
			VendorPage: &baselineSide{Input: c.Input, Output: c.Output},
			Observed:   &baselineSide{Input: obs.InputPer1M, Output: obs.OutputPer1M},
			SourceURL:  c.SourceURL, ObservedURL: url,
			Currency:  c.Currency,
			CacheRead: c.CacheRead, CacheWrit: c.CacheWrit,
			Row: c.Row, LineNo: c.LineNo,
			Verdict: "corroborated",
		}
		if !pricesAgree(c.Input, c.Output, obs.InputPer1M, obs.OutputPer1M) {
			entry.Verdict = "sources_disagree"
		}
		p.Corroborated = append(p.Corroborated, entry)

		// ★ 被互证的候选必须**留在 ready_to_review**（这就是 kept 的用途）。
		//
		// 互证是**附加证据**，不是「已入库」的许可：提案自己的 notice 写着
		// 「ready_to_review 里的条目仍需逐条对照 source_url 核对」，SSOT 的
		// 第三步也是「人确认后搬进 models」。所以人要逐条过的那份清单就是
		// ready_to_review —— 互证跑完把它清空，等于把待办清单删了，让人去读
		// 另一个数组，判词也就和条目分了家。
		//
		// 这一行原本不存在（2026-10-04 实测）：`kept` 声明了、三条分支都
		// continue、最后 `p.ReadyToReview = kept` ⇒ 恒为 nil ⇒ 互证一旦成功，
		// ready_to_review 永远是空的。整条互证路径此前**零测试覆盖**，所以
		// 它能一直活着。判词同时写进 warnings，让人不用离开清单就能看到。
		kept = append(kept, withWarning(c,
			"cross-checked against "+url+": "+entry.Verdict+
				" (observed input="+formatPriceOrNil(obs.InputPer1M)+
				" output="+formatPriceOrNil(obs.OutputPer1M)+")"))
	}
	if len(p.Corroborated) == 0 {
		// 两种「零互证」成因不同，指路也不同，混成一句话会让人以为工具坏了：
		//
		//	examined == 0  ⇒ 候选在**更早**的名字解析那步就全被挡住了（它们的
		//	                展示名没在这份 canonical 名单里匹配上），压根没走到互证。
		//	                该看的是 unresolved_names，不是 needs_human_eyes。
		//	examined > 0   ⇒ 名字解析过了，但观察源里查不到这个 (厂商, 模型)。
		//	                该看的是 needs_human_eyes 里那几条的单源理由。
		//
		// 无论哪种，ready_to_review 空了这件事**都**要说明，否则它会被读成
		// 「没有待办」。
		//
		// ★ 这里**不能** return error：调用方会把任何 error 记成
		// `corroboration_refused = "the observation source could not be read"`，
		// 而源明明读到了。那是一句假话，且比沉默更坏（人会去查网络，而问题在
		// canonical 名单或展示名上）。
		if examined == 0 {
			fmt.Fprintf(os.Stderr,
				"[corroborate] WARNING: %d candidate(s) could not be cross-checked because NONE of "+
					"them resolved to a canonical name — they are in unresolved_names (fix or extend the "+
					"canonical list, or add those models to models_canonical first). ready_to_review is "+
					"empty because nothing reached the cross-check, NOT because nothing needs review\n",
				len(p.Unresolved))
		} else {
			fmt.Fprintf(os.Stderr,
				"[corroborate] WARNING: examined %d candidate(s) against %s but corroborated NONE — "+
					"the observation source has no matching (vendor, model) price; the candidates are "+
					"in needs_human_eyes with per-item reasons. ready_to_review is empty because "+
					"nothing could be cross-checked, NOT because nothing needs review\n",
				examined, url)
		}
	}
	p.ReadyToReview = kept
	return nil
}

// formatPriceOrNil 让「观察源没有这个数」在告警里显形，而不是印出一个 0。
//
// 价格 0 与「没有价格」在页面上长得一模一样，而这里 0 是一个需要人看的异常值
// （原厂极少有真正 0 元的档位），把它印成 0 会让人以为源说了「免费」。
func formatPriceOrNil(v *float64) string {
	if v == nil {
		return "(absent)"
	}
	return strconv.FormatFloat(*v, 'f', -1, 64)
}

// resolutionScoreOf 从候选的 Warnings 里取回解析分数。
//
// 分数也要回显：一条 0.90 与一条 0.99 的解析在看提案的人眼里不该长得
// 一样，而输出里写 score=0 比不写更糟。
func resolutionScoreOf(c vendorprice.Candidate) float64 {
	marker := "(score "
	for _, w := range c.Warnings {
		if i := strings.Index(w, marker); i >= 0 {
			rest := w[i+len(marker):]
			if j := strings.Index(rest, ")"); j > 0 {
				if v, err := strconv.ParseFloat(rest[:j], 64); err == nil {
					return v
				}
			}
		}
	}
	return 0
}

// resolvedCanonical 从候选的 Warnings 里取回解析出的 canonical 名。
//
// 解析结果走 Warnings 而不是新建字段，是为了让单源路径与互证路径共用同
// 一个候选结构；代价是这里要把它读回来。
func resolvedCanonical(c vendorprice.Candidate) string {
	for _, w := range c.Warnings {
		marker := "resolved to canonical \""
		if i := strings.Index(w, marker); i >= 0 {
			rest := w[i+len(marker):]
			if j := strings.Index(rest, "\""); j > 0 {
				return rest[:j]
			}
		}
	}
	return ""
}

func withWarning(c vendorprice.Candidate, msg string) vendorprice.Candidate {
	c.Warnings = append(c.Warnings, msg)
	return c
}

// pricesAgree 用与 bg.ReconcileBaselinePrice 同一套容限口径。
func pricesAgree(a, b, c, d *float64) bool {
	const tol = 2.0
	near := func(x, y *float64) bool {
		if x == nil || y == nil {
			return true // 缺一侧不判不一致
		}
		if *x == 0 {
			return *y == 0
		}
		pct := (*y - *x) / *x * 100
		return pct < tol && pct > -tol
	}
	return near(a, c) && near(b, d)
}
