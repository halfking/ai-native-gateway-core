// bg/pricing_settlement_index.go — 内部结算价指数（以最便宜模型为 100 基数）
//
// # ⚠ 状态：**尚未接线**（2026-10-06 批判审计实测）
//
// `BuildSettlementIndex` 的**生产调用点 = 0**（全仓 grep，排除本文件与测试）。
// 本文件 402 行判据全绿，但**没有任何生产代码调用它** —— 结算价指数目前
// **不进任何运行路径**，不参与计费、不写库、不进 HTTP 响应。
//
// 为什么把这句话写在文件头而不是等接线时再说：402 行绿色判据 + 一份读起来
// 「像已交付」的文档注释，合起来足以让后继者断定「结算价已上线」。
// **判据绿 ≠ 功能在跑**。同批审计还发现本仓另有一处同型问题：第 18 条健康
// 检查与 6 条真库判据在裸 `go test ./bg/` 下会静默 SKIP（详见
// docs/db-changelog.md 对应小节）—— 「全绿」只能覆盖真正跑过的那部分。
//
// 接线的正确位置在成本核算侧（把指数喂给内部结算），届时本文件不应再改，
// 只应被调用。接线前请一并把本段改成接线说明，别直接删——删掉就又回到
// 「没人知道它是死代码还是半成品」。
//
// # 这一层是什么
//
// 决策（2026-10-06，用户指定）：内部结算**不直接用绝对价**，而是给每个模型一个
// 相对指数 —— **同一计费方向里最便宜的那个模型 = 100**，其余按原厂标准价的
// 倍率放大：
//
//	input_index  = 100 × (本模型 input 原厂价  / 全体最低 input 原厂价)
//	output_index = 100 × (本模型 output 原厂价 / 全体最低 output 原厂价)
//
// 「计费方向」（input / output）**各自取自己的最低价**做基数。理由是实测事实：
// 13 个已互证的模型里，output/input 的挂牌倍率并不统一 —— MiniMax 全系 4×、
// Claude 全系 5×、gpt-5.3-codex 8×。若强行合成一个数字，就必须引入一个
// 「输入:输出 = ? : ?」的配比假设，而这个假设会变成**第二个 SSOT**：配比一改，
// 所有模型的结算价全变。分两档则不需要任何配比假设，两个数都能单独对账。
//
// # 为什么这层不是「原厂价换个单位」
//
// 因为绝对价是**厂的**，指数是**我们的**。同一张原厂价表：
//
//	· 供应商采购谈判关心的是「我付了多少」（绝对价 × 实付倍率）；
//	· 内部结算关心的是「这个模型相对最便宜的模型贵多少」（指数）。
//
// 后者才是可以写进合同、可以对客户解释、对模型升降级不敏感的那个数 ——
// 新上一个更便宜的模型时，所有既有模型的绝对价一个都没变，而指数会整体重算。
// 把它做成可重算的**派生量**，而不是另存一份快照，是这一层唯一正确的形态：
// 存快照就会与原厂价漂移，而漂移在计费系统里是直接的钱。
//
// # 三条不可省的纪律
//
//  1. **指数是派生量，不是存储事实。** 它由 baseline_* 四列现算。
//     任何时候都可以重算，任何时候都不该有一张「结算指数表」等着被人手工维护。
//
//  2. **免费与缺价一律「不可结算」，不猜。** 基准价 = 0（厂商确认免费）与
//     基准价 IS NULL（还没定价）在迁移 826 里是刻意分开的两个事实；在这一层
//     它们**同样**不给指数。理由：0 做分母是未定义，NULL 做「按 0 结算」是把
//     「我们不知道」写成「它不要钱」。两者都比「明确标成不可结算」更贵。
//     —— 这与本仓对币种的处置同源：未知币种拒绝兜底成 USD，因为兜底是**断言**。
//
//  3. **币种不同就是不可结算。** 拿 CNY 的价除以 USD 的最低价，出来的指数
//     长得极像真数字，而它是纯噪声。与 v_supplier_price_vs_baseline 的
//     currency_comparable 同一处置：不可比要显式可见，不能折叠成 0 或 NULL 后
//     与「真的没有价」混作一类。
package bg

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// SettlementIndexBase 是基数的取值。选 100 而不是 1：指数会被印在结算单上，
// 「这个模型是最便宜的 10 倍」比「0.1 倍」少一层换算，而 100 让整数情形
// （3×/5×/8×）保持整数。
const SettlementIndexBase = 100.0

// settlementIndexTolerance 是判定「一个价等于基数」的容限。
//
// 为什么需要它：最低价是**取自样本**的，实测里 minimax-m2 / m2.1 / m2.5
// 三者 input 同为 0.30。若用浮点相等判「基数模型自己 ⇒ 指数 100.00」，
// 任何一个四舍五入的尾差都会让它算出 99.999999999，而结算单上的 100 与
// 99.999999999 是两回事：前者是口径声明，后者是待排查的数。
//
// 1e-9 相对容限足够宽（实测最小非零倍率差是 2×，远大于它），又足够窄
// （1e-9 内的差异不可能来自任何真实的定价差异，只可能来自浮点表示）。
const settlementIndexTolerance = 1e-9

// SettlementIndexVerdict 是每个方向各自的结论。
//
// 刻意把方向做成**字段**而不是行：input 与 output 可以一个可结算、另一个不可
// （例如某模型只公开了 input 价），这种形态在「一行一结论」的表里会被迫
// 二选一，而两个字段能如实表达。
type SettlementIndexVerdict string

const (
	// IndexOK：这一档可结算，Index 有效。
	IndexOK SettlementIndexVerdict = "ok"
	// IndexNoBaseline：清单里没有这个模型，或这一档没有价（NULL）。
	IndexNoBaseline SettlementIndexVerdict = "no_baseline"
	// IndexFreeBaseline：原厂确认这一档免费（基准价 = 0）。**不给指数**：
	// 0 做分母无定义，而把它记成 0 会把「白给」与「还没定价」并成一类。
	IndexFreeBaseline SettlementIndexVerdict = "free_baseline"
	// IndexCurrencyMismatch：币种与该方向的最低价不同 ⇒ 倍率是纯噪声。
	IndexCurrencyMismatch SettlementIndexVerdict = "currency_mismatch"
	// IndexNoUniverse：该方向**一个**可比的价都没有 ⇒ 没有基数可除。
	// 与 IndexNoBaseline 区分开：前者是「普遍没定价」，后者是「就它没定价」。
	IndexNoUniverse SettlementIndexVerdict = "no_universe"
)

// SettlementAxis 是单档（input 或 output）的结算指数。
type SettlementAxis struct {
	// Index 是结算指数；仅在 Verdict == IndexOK 时有意义，否则为 nil。
	Index *float64
	// Verdict 永远非空：**不可结算也必须有明确判词**，否则报表上它与
	// 「忘了填」同形。
	Verdict SettlementIndexVerdict
	// Reason 是给读报表的人的一句话。
	Reason string
	// PricePer1M 是本模型这一档的原厂价（未参与计算，仅供核对）。
	PricePer1M *float64
	// Currency 是本档基准价的币种。
	Currency string
	// BaselineModel 是这一档的基数模型（最便宜者）。空 = 该方向没有可比价。
	BaselineModel string
	// BaselinePricePer1M 是基数模型这一档的原厂价。
	BaselinePricePer1M *float64
	// BaseCurrency 是基数模型那一档的币种。与 Currency 分开是因为两者
	// **可能不同** —— 那正是 currency_mismatch 判词要表达的事实，而把它
	// 折进一个字段就等于把「不可比」变成「比过了，一致」。
	BaseCurrency string
}

// SettlementIndex 是一个模型的完整结算指数（两档各自独立）。
type SettlementIndex struct {
	Model      string
	Input      SettlementAxis
	Output     SettlementAxis
	BaselineAt time.Time
	Settled    bool
}

// settleAxis 是纯计算：给定本档价与本方向的基数，返回一个 SettlementAxis。
//
// 抽成纯函数是因为这一层有四条**互不相同**的不可结算判据，而它们在报表上
// 若折叠成同一个 NULL 就会全部变成「忘了填」。每条都有独立判词，是这一层
// 与「乘法算不出来就是空」的主要区别。
func settleAxis(currency string, price *float64, baseModel string, basePrice *float64, baseCurrency string) SettlementAxis {
	ax := SettlementAxis{Verdict: IndexOK, PricePer1M: price, Currency: currency,
		BaselineModel: baseModel, BaselinePricePer1M: basePrice, BaseCurrency: baseCurrency}

	// 顺序有意义：先判「整个方向没有可比价」，再判「就它没有价」。
	// 反过来的话，样本为空时每条都会报 no_baseline，而真实原因是
	// 「一个价都没采到」—— 两种情况要动的地方完全不同。
	if basePrice == nil || *basePrice <= 0 {
		ax.Verdict = IndexNoUniverse
		ax.Reason = "no comparable baseline price exists on this side in the catalog, " +
			"so there is no base to divide by"
		ax.Index = nil
		return ax
	}
	if price == nil {
		ax.Verdict = IndexNoBaseline
		ax.Reason = "this model has no baseline price on this side"
		ax.Index = nil
		return ax
	}
	if *price == 0 {
		ax.Verdict = IndexFreeBaseline
		ax.Reason = "the original vendor lists this model as free on this side; a free model has " +
			"no index, because 0 is an undefined divisor and recording 0 here would merge " +
			"\"free\" with \"not priced yet\""
		ax.Index = nil
		return ax
	}
	if *price < 0 {
		ax.Verdict = IndexNoBaseline
		ax.Reason = "this model has a negative baseline price, which is not a price"
		ax.Index = nil
		return ax
	}
	// 币种一致性。两个非空条件都在守卫里，与 pricing_baseline_sync.go 的
	// currency_mismatch 同一处置：任一侧为空时，这次比较从未发生在同一种
	// 货币里，而 SSOT 的 validate 已挡住空币种入库。
	if currency != "" && baseCurrency != "" && currency != baseCurrency {
		ax.Verdict = IndexCurrencyMismatch
		ax.Reason = fmt.Sprintf("this price is in %s while the cheapest comparable price is in %s, "+
			"so the ratio between them is an artefact of the exchange rate rather than a real index",
			currency, baseCurrency)
		ax.Index = nil
		return ax
	}

	idx := SettlementIndexBase * (*price) / (*basePrice)
	// 基数模型自己必须精确落在 100。
	//
	// ★ 这段归一**不是**为「同价相除」的常见情况准备的 —— 实测（2026-10-06，
	// Go 1.x / IEEE754 双精度）`100*a/a` 在 a ∈ {0.1, 0.3, 1.2, 7, 1e-7} 上
	// **逐位等于 100**（先乘后除是精确的），所以对 0.30 那种真实价位它是
	// 死代码。真正会触发的是**极端价位**：a = 1e-300 时 `100*a/a` =
	// 100.00000000000001。
	//
	// 为什么仍然留着：100 与 100.00000000000001 在结算单上是两回事 ——
	// 前者是「它就是基准」这个口径声明，后者是一个需要解释的数，而**没有**
	// 一个运营会为后者去查 IEEE754。触发概率低不等于可以把它印出去。
	// 而它必须是**有判据**的，否则下一个读代码的人会当它是无用的装饰删掉 ——
	// 删掉之后对真实价位确实没有可见变化，于是删除看起来是无害的，直到
	// 某个极小价位的模型进了清单。
	if math.Abs(idx-SettlementIndexBase) < SettlementIndexBase*settlementIndexTolerance {
		idx = SettlementIndexBase
	}
	ax.Index = &idx
	return ax
}

// BuildSettlementIndex 从 SSOT 清单算出每个模型的结算指数。
//
// 基数规则（用户指定 + 2026-10-06 澄清）：**input 与 output 各自取本方向
// 最便宜的模型 = 100**。所以两个方向的基数模型**可以不是同一个** ——
// 当某厂商只公开 input 价、另一家只公开 output 价时，这恰恰是对的口径。
//
// 纯函数：不碰数据库、不读文件。调用方负责把清单准备好。
//
// ⚠ 目前**没有调用方**（见文件头「状态：尚未接线」）。这句「调用方负责」描述的是
// 契约要求，不是现状。
func BuildSettlementIndex(catalog map[string]BaselinePrice) map[string]SettlementIndex {
	out := make(map[string]SettlementIndex, len(catalog))

	// 先各自选出两个基数。选基数必须**先于**计算，否则「最便宜的那个模型
	// 的指数」要等自己被选中才知道 —— 而它必须是 100，不能由它自己算出别的数。
	inBase, inBasePrice, inBaseCurrency := cheapestBaseline(catalog, func(p BaselinePrice) *float64 { return p.InputPer1M })
	outBase, outBasePrice, outBaseCurrency := cheapestBaseline(catalog, func(p BaselinePrice) *float64 { return p.OutputPer1M })

	for name, p := range catalog {
		si := SettlementIndex{
			Model:  name,
			Input:  settleAxis(p.Currency, p.InputPer1M, inBase, inBasePrice, inBaseCurrency),
			Output: settleAxis(p.Currency, p.OutputPer1M, outBase, outBasePrice, outBaseCurrency),
		}
		si.Settled = si.Input.Verdict == IndexOK && si.Output.Verdict == IndexOK
		if t, err := p.FetchedAtTime(); err == nil {
			si.BaselineAt = t
		}
		out[name] = si
	}
	return out
}

// cheapestBaseline 选出某一方向最便宜的模型。
//
// 三条挑选规则，逐条都有代价：
//
//	· **跳过 nil 与 0**：0 是「原厂确认免费」，它做基数会让所有别的模型都
//	  拿一个免费模型当 100 基准 —— 而那不是「最便宜的**可比**模型」，
//	  是「最不该当基准的那个」。同样不能用 0 当分母。
//	· **跳过负价**：那不是价。
//	· **同价时按 canonical 名排序取第一个**：同价即同指数，基准模型是谁不
//	  影响任何数字，所以确定性挑选只为了让输出稳定（否则每次跑可能换一个
//	  名字写进台账，而没有任何数字变化）。
//
// 币种返回的是**被选中那条**的币种，混币种比较在这里就已经发生了 —— 但
// 结算层的 currency_mismatch 判词会把受影响的那一条标出来，见 settleAxis。
func cheapestBaseline(catalog map[string]BaselinePrice, pick func(BaselinePrice) *float64) (string, *float64, string) {
	names := make([]string, 0, len(catalog))
	for n := range catalog {
		names = append(names, n)
	}
	sort.Strings(names) // 确定性：同价时按名字定胜负

	var (
		bestName string
		best     *float64
		bestCur  string
	)
	for _, n := range names {
		p := catalog[n]
		v := pick(p)
		if v == nil || *v <= 0 {
			continue
		}
		if best == nil || *v < *best {
			bestName, best, bestCur = n, v, p.Currency
		}
	}
	return bestName, best, bestCur
}
