package streaming

import (
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"strings"
)

// CNYToUSDFX converts native CNY upstream cost to USD for KPI aggregation.
// Matches pricing research docs (2026-06-12-cny-fix).
const CNYToUSDFX = 7.2

var errNotFound = errors.New("key not found")

type UsageData struct {
	PromptTokens     *int
	CompletionTokens *int
	CacheReadTokens  *int
	CacheWriteTokens *int
	ReasoningTokens  *int
	CacheMissTokens  *int
	ProviderTokens   *int
}

// Wave4-D4 (2026-09-22): the usage field-variant table — the single place
// mapping vendor wire names to gateway slots. The streaming extractor
// (ExtractUsageFromChunk) and the non-streaming one
// (handler.go extractTokensFromResponseBody) both resolve slots through
// lookupUsageInt, so a new vendor field name lands in exactly one list.
// Order matters: first match wins, matching the historical per-site
// precedence (direct Anthropic/OpenAI names before *_details fallbacks).
type usagePath []string

var (
	usagePromptPaths     = []usagePath{{"prompt_tokens"}, {"input_tokens"}}
	usageCompletionPaths = []usagePath{{"completion_tokens"}, {"output_tokens"}}
	usageCacheReadPaths  = []usagePath{
		{"cache_read_input_tokens"},
		{"cache_read_tokens"},
		{"prompt_tokens_details", "cached_tokens"},
		{"input_token_details", "cache_read"},
	}
	usageCacheWritePaths = []usagePath{
		{"cache_creation_input_tokens"},
		{"cache_write_tokens"},
		{"input_token_details", "cache_creation"},
	}
)

// lookupUsageInt resolves the first path in paths that yields a number.
// A path is either ["key"] (top-level usage field) or
// ["detail_object", "key"] (nested prompt_tokens_details /
// input_token_details family). Numbers are read as float64 then truncated
// so both integer and "12.0"-shaped JSON count.
func lookupUsageInt(usage map[string]json.RawMessage, paths []usagePath) (int, bool) {
	for _, path := range paths {
		switch len(path) {
		case 1:
			raw, ok := usage[path[0]]
			if !ok {
				continue
			}
			var f float64
			if err := json.Unmarshal(raw, &f); err != nil {
				continue
			}
			return int(f), true
		case 2:
			raw, ok := usage[path[0]]
			if !ok {
				continue
			}
			var detail map[string]json.RawMessage
			if err := json.Unmarshal(raw, &detail); err != nil {
				continue
			}
			raw, ok = detail[path[1]]
			if !ok {
				continue
			}
			var f float64
			if err := json.Unmarshal(raw, &f); err != nil {
				continue
			}
			return int(f), true
		}
	}
	return 0, false
}

func ExtractUsageFromChunk(payload string) UsageData {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		return UsageData{}
	}

	usageRaw, ok := obj["usage"]
	if !ok {
		return UsageData{}
	}

	var usage map[string]json.RawMessage
	if err := json.Unmarshal(usageRaw, &usage); err != nil {
		return UsageData{}
	}

	result := UsageData{}

	// prompt/completion/cache slots resolve through the shared variant
	// table (Wave4-D4); Anthropic-native names are the fallback tails.
	if v, ok := lookupUsageInt(usage, usagePromptPaths); ok {
		result.PromptTokens = &v
	}
	if v, ok := lookupUsageInt(usage, usageCompletionPaths); ok {
		result.CompletionTokens = &v
	}
	if v, ok := lookupUsageInt(usage, usageCacheReadPaths); ok {
		result.CacheReadTokens = &v
	}
	if v, ok := lookupUsageInt(usage, usageCacheWritePaths); ok {
		result.CacheWriteTokens = &v
	}

	// Reasoning usage is reported by OpenAI-compatible providers under either
	// completion_tokens_details or a provider-specific top-level field.
	if detail, err := objVal(usage, "completion_tokens_details"); err == nil {
		if v, err := intValue(detail, "reasoning_tokens"); err == nil {
			result.ReasoningTokens = &v
		}
	}
	if result.ReasoningTokens == nil {
		for _, key := range []string{"reasoning_tokens", "reasoning_token_count"} {
			if v, err := intValue(usage, key); err == nil {
				result.ReasoningTokens = &v
				break
			}
		}
	}

	// DeepSeek/Doubao-compatible endpoints may expose cache hit/miss as
	// separate counters. Keep miss separate because it is normally billed at
	// the regular input rate rather than the cache-read rate.
	for _, key := range []string{"prompt_cache_miss_tokens", "cache_miss_tokens"} {
		if v, err := intValue(usage, key); err == nil {
			result.CacheMissTokens = &v
			break
		}
	}

	// Doubao Seed usage is provider billing evidence, not a generic token
	// replacement. Preserve it separately until the provider rate card maps it.
	for _, key := range []string{"seed_token_usage", "seed_tokens"} {
		if v, err := intValue(usage, key); err == nil {
			result.ProviderTokens = &v
			break
		}
	}

	// total_tokens fallback: if we have total but missing prompt/completion,
	// infer the missing side. R57 D4：与非流式 extractTokensFromResponseBody
	// 双向推断对称化——原条件 `(P==nil||C==nil) && P==nil` 塌缩成 P==nil 单
	// 向，completion-only usage（部分上游流式尾块只报 completion+total）永
	// 远推不出 prompt，计费相邻面流式/非流式口径不一致。
	if result.PromptTokens == nil || result.CompletionTokens == nil {
		if total, err := intValue(usage, "total_tokens"); err == nil && total > 0 {
			if result.PromptTokens == nil && result.CompletionTokens != nil && total > *result.CompletionTokens {
				pt := total - *result.CompletionTokens
				result.PromptTokens = &pt
			} else if result.CompletionTokens == nil && result.PromptTokens != nil && total > *result.PromptTokens {
				ct := total - *result.PromptTokens
				result.CompletionTokens = &ct
			}
		}
	}

	return result
}

// ExtractDoubaoUsageFromChunk extracts only fields that are valid for the
// official Doubao profile. The catalog guard prevents a volcengine-coding
// aggregate response from being interpreted as Doubao usage.
func ExtractDoubaoUsageFromChunk(payload string, catalogCode string) UsageData {
	if strings.ToLower(strings.TrimSpace(catalogCode)) != "doubao" {
		return UsageData{}
	}
	return ExtractUsageFromChunk(payload)
}

type CostInput struct {
	PromptTokens     *float64
	CompletionTokens *float64
	CacheReadTokens  *float64
	CacheWriteTokens *float64
	PriceIn          *float64
	PriceOut         *float64
	CacheReadPrice   *float64
	CacheWritePrice  *float64
}

// CacheTokenConventions 回答一个问题：**上游报的 prompt_tokens 含不含
// cache token？** 两种厂商口径相反，而下面那段 cache 扣减公式只对其中一种成立。
//
//	ConventionPromptIncludesCache（OpenAI 等）：
//	    prompt_tokens ⊇ cached_tokens。cache 那一部分已经按**输入原价**计过了，
//	    所以要先减掉原价、再按缓存价重算 —— 现有公式就是这个口径。
//	ConventionPromptExcludesCache（Anthropic 等）：
//	    input_tokens **不含** cache_read_input_tokens，两者是并列的。
//	    此时再减就是**平白扣掉一批从未计入的 token** ⇒ promptCost 变负。
//
// ★ 2026-10-06 真库实测（127.0.0.1:5432，只读）：
//
//	request_logs  cost_usd < 0                        = 1,628 行（−$4.79）
//	其中 cache_read_tokens > prompt_tokens 的          = 1,628（全部）
//	同口径行数                                        = 11,837
//	那批行里 cache 占 token 的比例                    = 96.2%（4.53M vs 179K）
//	归属                                               = 全部 apiclaude（Anthropic 中转）
//	对照组：正成本行里 cache ≤ prompt 的                = 6,061，cache 平均占 prompt 的 78.8%
//
// 对照组那 6,061 行正是 OpenAI 口径的样子：cache 是 prompt 的一个**子集**。
// 而 apiclaude 那 11,837 行里 cache 是 prompt 的 25 倍 —— 那不是「折扣很给力」，
// 是**两个并列的数被当成了包含关系**。
//
// ★ 口径怎么来：2026-10-06 起由**上游协议**自动判定，见
// CacheTokenConventionForProtocol（本文件下方）。
//
// 原来这里写的是「本枚举不是用来自动判定协议的：仓库里没有可靠的协议标记能一路
// 带到成本计算点（usage 的 variant 表 usagePromptPaths 把 input_tokens 与
// prompt_tokens 映射到同一个槽，信息在这一步就丢了）」，并据此把「显式声明
// 口径」留给运营。
//
// ⚠ **那个前提经实测是错的**，别再照它推理：
//   - variant 表丢信息是真的，但它只管 usage 的**字段名归一**；
//     成本计算点要的并不是那个槽，而是**这次请求走的哪个上游协议** ——
//     那个量以 `provider.Candidate.Protocol` 的形式**完整活到了**调用点
//     （domains/streaming/handler.go 的 AssignRequestCost 调用处），
//     且候选 SQL 有 `COALESCE(p.protocol,”) <> ”` 保证非空。
//   - 所以「必须人工声明」不是技术限制，而是当时没去找这个字段。
//     改成按协议判定之后，零值的语义（不知道 ⇒ 原有行为）仍然保留给未知协议。
//
// ⚠ 它改的是**未来**新记账的金额；已落库的负成本行不会被改
// （recorded_cost_is_negative 负责报，回补与否是运营决定）。
type CacheTokenConvention int

const (
	// PromptConventionUnset = 不知道出处的口径，退回 OpenAI 口径（原有行为）。
	PromptConventionUnset CacheTokenConvention = iota
	// ConventionPromptIncludesCache = OpenAI 口径：cache ⊆ prompt。
	ConventionPromptIncludesCache
	// ConventionPromptExcludesCache = Anthropic 口径：cache ∥ prompt（并列）。
	ConventionPromptExcludesCache
)

func (c CacheTokenConvention) String() string {
	switch c {
	case ConventionPromptIncludesCache:
		return "prompt_includes_cache"
	case ConventionPromptExcludesCache:
		return "prompt_excludes_cache"
	default:
		return "unset"
	}
}

// CacheTokenConventionForProtocol 从**上游协议**判定 token 口径。
//
// # 2026-10-06 修正：本函数替代了「没有可靠标记，只能人工声明」
//
// 本文件上方那段注释原来写着「仓库里没有可靠的协议标记能一路带到成本计算点
// （usage 的 variant 表把 input_tokens 与 prompt_tokens 映射到同一个槽）」——
// **那条阻塞是错的**，实测否掉了它：
//
//   - 候选 SQL（provider/client.go:1129）select 出 `p.protocol`，
//     同一行还 select 出 `unit_price_*_per_1m`；两条都解码进 provider.Candidate
//     （Protocol 在 :113，四个价字段在 :158-161）。
//   - 该 SQL 另有 `AND COALESCE(p.protocol,”) <> ”`（:1140）⇒ **每个候选行
//     必然带协议**，不是「有时有」。
//   - 真库 `providers.protocol` 只有三个取值、**零 NULL**（54 openai-completions /
//     4 anthropic-messages / 2 openai-responses）。
//     而 cache > prompt 的异常**完全集中在 anthropic-messages**：
//     openai-completions 0.0%（75,461 行）、anthropic-messages 88.1%（12,112 行，
//     cache/prompt 均值 57.03）、openai-responses 51.9%（仅 27 行，样本不足以下结论）。
//     ⇒ 协议是**完美判别式**，不是弱相关。
//   - 钱上面的方向也是明确的，但**比一开始以为的小得多**。先量清楚再动手，
//     否则会拿着一个假数字去论证（我拿过）：
//     · 一度以为「Anthropic 口径能救回 $51.86」——**错**。那个数建立在
//     「OpenAI 口径会把这些行算成负数」之上，而实测**不会**：
//     下面那道 subset 护栏（cacheReadCount <= promptCount）在 cache > prompt 时
//     已经**跳过扣减**了。真库实测：4 组负成本行**全部**满足
//     cache_read > prompt（cache_subset_of_prompt 全为 0），缓存价本身是正的 5
//     ⇒ 负成本的来源是**现网二进制还没有那道护栏**（10-04 构建），
//     而不是口径。工作区里的护栏已经修好它。
//     · 按协议接线后的**真实**影响：只有「cache ⊆ prompt 且协议 = anthropic-messages」
//     的行会变，近 30 天是 **8 行 / $5.28**。
//     （对照：openai-completions 有 5,958 行落在扣减生效区，$744.39 ——
//     那里扣减是**对的**，所以本函数刻意不碰它。）
//     ⇒ 所以这个函数的价值不在救回多少钱，而在于**把语义写清楚**：
//     subset 护栏是「数值上像子集才当子集」的**代理**，
//     而协议是**契约**。两者会在两个方向上分歧：OpenAI 遇到 cache > prompt、
//     Anthropic 遇到 cache ≤ prompt。
//
// # 保守边界（这是它安全的原因）
//
// **只对 anthropic-messages 显式声明**，其余一律返回 PromptConventionUnset
// （= 退回原有 OpenAI 行为）。所以这个改动在结构上**不可能**影响那 56 家
// 非 Anthropic 供应商的已记账金额 —— 它只改本该改的那一个协议。
// 新协议值出现时也自动落回原有行为，而不是被猜成 Anthropic。
//
// ⚠ 它改变的是**未来**新记账的金额；已经落在库里的负成本行不会被改
// （那由 recorded_cost_is_negative 检查报出，回补与否是运营决定）。
func CacheTokenConventionForProtocol(protocol string) CacheTokenConvention {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "anthropic-messages":
		return ConventionPromptExcludesCache
	default:
		// 未知/空协议 → 不猜。PromptConventionUnset 的语义就是
		// 「不知道，按原有行为算」，这正是这里要的。
		return PromptConventionUnset
	}
}

func CalcCost(input CostInput) *float64 {
	return calcCostWithConvention(input, PromptConventionUnset)
}

// calcCostWithConvention 是带口径的本体。CalcCost 是它的零值包装。
func calcCostWithConvention(input CostInput, conv CacheTokenConvention) *float64 {
	if input.PromptTokens == nil && input.CompletionTokens == nil {
		return nil
	}

	priceIn := floatPtr(input.PriceIn, 0)
	priceOut := floatPtr(input.PriceOut, 0)
	if priceIn == 0 && priceOut == 0 {
		return nil
	}

	promptCount := floatPtr(input.PromptTokens, 0)
	cacheReadCount := floatPtr(input.CacheReadTokens, 0)
	cacheWriteCount := floatPtr(input.CacheWriteTokens, 0)

	promptCost := promptCount * priceIn

	// ★ cache 的**原价扣减**只在 OpenAI 口径下成立（cache ⊆ prompt）。
	//
	// Anthropic 口径下 prompt 与 cache 是**并列**的两个数，cache 从来没被按
	// 输入原价计过 ⇒ 「先减原价再按缓存价重算」减掉的是一批不存在的账单，
	// 减到 cache >> prompt 时就把 promptCost 推成负数。
	//
	// 口径为 unset（默认）时按 OpenAI 算：这是**原有行为**，不静默改动任何
	// 已记账的金额。要按 Anthropic 口径算必须由调用方显式声明。
	//
	// ⚠️ 这里的 `!= nil` 刻意**不是** `> 0`（2026-10-06 修正，判据抓到的）。
	//   `> 0` 把「负的缓存价」当成「没配缓存价」⇒ 整段 cache 成本**不计**。
	//   在 OpenAI 口径下那无害（cache ⊆ prompt，已按输入价计过），但在
	//   Anthropic 口径下 cache 与 prompt 并列 ⇒ 跳过就等于**白送**。
	//   负价本身是数据错误（831 的 CHECK 未上生产时可能存在），正确出口是
	//   下面那道 `total < 0` 守卫把它变成 nil（算不出来），而不是静默免费。
	// discountable = 「cache 是 prompt 的子集」⇒ 那一部分已经按输入原价计过，
	// 需要先减原价再按缓存价重算。
	//
	// ★ 这个 gate 里有**两个**条件，第二个是 `cacheReadCount <= promptCount`，
	// 且它**不是**冗余：它是在「口径没说清」时拦住公式超扣的唯一一道。
	//
	// 2026-10-06 实测（这条判据第一次跑就抓到的）：我起初只写了
	// `applies := conv != ConventionPromptExcludesCache` 一个条件，于是
	// 显式 Anthropic 口径那档算出来是 0.02987484，而期望是 0.07557984 ——
	// 差的正好是 41550×1.10 = 0.045705，也就是**整段 cache 成本**。
	// 根因：把「不原价扣减」与「不按缓存价计」当成了同一件事。
	// 它们是两件事：不原价扣减之后，cache 那一部分**仍然要按自己的单价入账**
	// ——否则 Anthropic 口径下 4.5M cache token 会变成完全免费。
	//
	// 「cache 是 prompt 的子集」这个判据（subset）是个**纯算术事实**，
	// 不是厂商身份：OpenAI 口径下 cache ⊆ prompt 恒成立（真库对照组 6,061 行
	// 全部满足，cache 平均占 prompt 的 78.8%），所以对 OpenAI 流量零影响；
	// 而 cache > prompt 只可能出现在并列口径下（真库 11,837 行，cache 是
	// prompt 的 25 倍）。⇒ 未声明口径的生产路径也能算对，不必等口径上线。
	discountable := conv != ConventionPromptExcludesCache
	subset := cacheReadCount <= promptCount

	if input.CacheReadPrice != nil && cacheReadCount > 0 {
		if discountable && subset {
			promptCost -= cacheReadCount * priceIn
		}
		// 无论是否原价扣减，cache 那一部分都要按缓存价入账。
		promptCost += cacheReadCount * *input.CacheReadPrice
	}

	if input.CacheWritePrice != nil && cacheWriteCount > 0 {
		if discountable && cacheWriteCount <= promptCount {
			promptCost -= cacheWriteCount * priceIn
		}
		promptCost += cacheWriteCount * *input.CacheWritePrice
	}

	completionCount := floatPtr(input.CompletionTokens, 0)
	total := (promptCost + completionCount*priceOut) / 1_000_000.0

	if math.IsNaN(total) || math.IsInf(total, 0) {
		return nil
	}

	// ★ 负成本不是「便宜」，是**算错了** —— 而两种「修法」都是撒谎：
	//   - 截到 0：等于断言「这次请求免费」。它会让一次算错的请求在成本
	//     报表里变成一条**便宜的**记录，比错报高价更难被发现（毛利虚高、
	//     偏差视图看不出异常），而且 `supplier_price_missing_from_cost`
	//     也不会响，因为价格列是有值的。
	//   - 返回负数：让负值流进台账，任何求和都被污染。
	// ⇒ 这里选 nil：与「没有价格」同一个出口，语义是「**算不出来**」，
	// 而不是「算出来是 0」。telemetry 会记成 cost_usd IS NULL，
	// 「这一段没有成本记录」因此是可查的，而不是伪装成免费。
	//
	// ── 这个分支为什么会被走到（2026-10-06 真库实测）────────────────
	// 上面那段「cache 从 prompt 里减掉」的公式**假定 prompt_tokens 含
	// cache token**（OpenAI 口径：prompt_tokens ⊇ cached_tokens）。但
	// Anthropic 口径相反：input_tokens **不含** cache_read_input_tokens
	// （见 internal/ir/response.go 对 CacheReadInputTokens 的抽取）。
	// 同一份公式喂两种口径 ⇒ cache 一大，promptCost 就被减成负数。
	//
	// 实测 apiclaude（Anthropic 协议的中转）30 天：11,837 行满足
	// cache_read_tokens > prompt_tokens，cache 占这批 token 的 **96.2%**
	// （4,530,529 vs prompt 179,144），其中 1,628 行记成了**负成本**。
	// ⇒ 那一整段 cache 流量在账上≈免费。
	//
	// 真正的修法是**按协议归一化 token 口径**（Anthropic 口径要把 cache
	// 并进 prompt 再进这条公式，或给公式一个显式的口径入参）—— 那会
	// 改动已记账的金额，属于运营决定，不在这里悄悄改。
	// 这一行只保证「不再产出负数」，并留下唯一可定位的信号。
	if total < 0 {
		// ★ 把**假定**的口径写进日志。2026-10-06 实测：
		//   `Convention` 已铺到 CostPriceInput，但生产侧**没有任何调用方设置它**
		//   （handler.go 的 AssignRequestCost 调用不传）⇒ 永远是零值。
		//   这一点必须出现在日志里，否则读日志的人会以为「已经按协议归一化过了」。
		//   `conv.String()` 在此之前只有判据在用（生产零调用者）⇒ 它是死代码，
		//   补上这个调用既是让日志说话，也是让这条路径真的活起来。
		slog.Warn("cost computation negative — token 口径与本公式的假定不一致，"+
			"本次不记成本（按「算不出来」处理，不按免费处理）",
			"prompt_tokens", promptCount,
			"completion_tokens", completionCount,
			"cache_read_tokens", cacheReadCount,
			"cache_write_tokens", cacheWriteCount,
			"price_in_per_1m", priceIn,
			"cache_read_price", floatPtr(input.CacheReadPrice, 0),
			"assumed_convention", conv.String(),
			"total_before_guard", total)
		return nil
	}

	total = math.Round(total*1e8) / 1e8
	return &total
}

// CostPriceInput carries token usage and offer pricing for request-log cost fields.
type CostPriceInput struct {
	PromptTokens     *int
	CompletionTokens *int
	CacheReadTokens  *int
	CacheWriteTokens *int
	PriceIn          *float64
	PriceOut         *float64
	CacheReadPrice   *float64
	CacheWritePrice  *float64
	Currency         string
	// Convention 声明上游的 token 口径。零值 = 不知道 = 退回 OpenAI 口径
	// （原有行为）。见 CacheTokenConvention 的注释。
	Convention CacheTokenConvention
}

// AssignRequestCost fills cost_usd / cost_display / cost_currency for telemetry.
// USD offers write cost_usd only; non-USD writes native cost_display plus USD KPI
// via CNYToUSDFX (7.2). Returns all nil when pricing or tokens are insufficient.
func AssignRequestCost(in CostPriceInput) (costUSD, costDisplay *float64, costCurrency *string) {
	if in.PromptTokens == nil && in.CompletionTokens == nil {
		return nil, nil, nil
	}

	native := calcCostWithConvention(CostInput{
		PromptTokens:     intPtrToFloatPtr(in.PromptTokens),
		CompletionTokens: intPtrToFloatPtr(in.CompletionTokens),
		CacheReadTokens:  intPtrToFloatPtr(in.CacheReadTokens),
		CacheWriteTokens: intPtrToFloatPtr(in.CacheWriteTokens),
		PriceIn:          in.PriceIn,
		PriceOut:         in.PriceOut,
		CacheReadPrice:   in.CacheReadPrice,
		CacheWritePrice:  in.CacheWritePrice,
	}, in.Convention)
	if native == nil {
		return nil, nil, nil
	}

	curr := strings.ToUpper(strings.TrimSpace(in.Currency))
	if curr == "" || curr == "USD" {
		return native, nil, nil
	}

	display := native
	currency := strings.TrimSpace(in.Currency)
	if currency == "" {
		currency = in.Currency
	}
	currencyCopy := currency
	usd := math.Round((*native/CNYToUSDFX)*1e8) / 1e8
	return &usd, display, &currencyCopy
}

func intPtrToFloatPtr(p *int) *float64 {
	if p == nil {
		return nil
	}
	v := float64(*p)
	return &v
}

func intValue(m map[string]json.RawMessage, key string) (int, error) {
	raw, ok := m[key]
	if !ok {
		return 0, errNotFound
	}
	var v int
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, err
	}
	return v, nil
}

func objVal(m map[string]json.RawMessage, key string) (map[string]json.RawMessage, error) {
	raw, ok := m[key]
	if !ok {
		return nil, errNotFound
	}
	var v map[string]json.RawMessage
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}

func floatPtr(p *float64, def float64) float64 {
	if p != nil {
		return *p
	}
	return def
}
