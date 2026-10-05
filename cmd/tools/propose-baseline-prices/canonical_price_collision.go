package main

// 「一个 canonical 不得出现两个不同的价」这条硬不变量。
//
// # 它拦的是什么
//
// 2026-10-06 在 `-accept-dimension-prose-as-footnote` 打开时实测撞上：
// `Claude Opus 4.8` 在 anthropic 那一页出现**两次**且价不同 —— 标准表
// $5/$25，紧跟着散文段「Fast mode pricing ...」那张表 $10/$50。两条候选的
// 展示名一样、解析出的 canonical 名一样，只有价不同。
//
// # 为什么「让第一个赢」不行
//
// 现状里唯一的兜底在 `buildDraft`：`if _, dup := d.Models[canonical]; dup { refuse }`。
// 那是**位置太靠后**的兜底，而且它回答的问题也不对：
//
//   - 它在互证之后才跑，而互证会把候选塞进 `Corroborated` —— 提案里
//     `ready_to_review` **已经躺着两条同名不同价的条目**，人在 `ready_to_review`
//     里看不出这里撞了名，只看得到「两条都对得很自信的价」。
//   - 「谁赢」由**文档顺序**决定。厂商把 Fast mode 那张表放在后面，于是
//     标准价赢；哪天页面调了段落顺序，入库的基准价就跟着变，而**页面上什么都没改**。
//     一条会随排版漂移的基准价，比没有基准价坏得多：它看起来是对的。
//
// ⇒ 两条都扣下、点名、进结构化字段。SSOT 的键是 canonical_name（2026-10-04
// 决策：一个模型一个价），撞上时**正确做法是缺一条基准价，不是挑一条**。
//
// # 「同价重复」不是冲突
//
// 同一个 canonical 出现两行**完全一样**的价（两份快照都抓到了同一张表）不构成
// 冲突，数字是唯一的。这类重复留在原地：交给 `buildDraft` 的 dup 兜底，它会拒收
// 第二条并写明「duplicate canonical」—— 那句话在同价场景下是准确的。
//
// # 分档（tier）不进指纹
//
// `Tier` / `ProductTier` **刻意不参与**价格指纹：Standard 与 Batch 两档价格
// **相同**时，基准价列里那个数没有歧义。分档带来的真正危险是**数字不同**，而那
// 已经被指纹抓住了。把 tier 算进去只会把「同价不同档」也报成冲突，而报告里会出现
// 两条一模一样的价 —— 那种报告读起来像故障，人反而会去查工具。
//
// # 对账恒等式（判据钉的就是它）
//
//	进门行数 == 留下的行数 + 扣下的行数
//
// 扣下不等于丢钱：每一行连同行号、原文行、URL 都进了 `AmbiguousCanonical`。
// 「钱永不消失」在这条不变量上就落在这里 —— 一条被扣下的候选若没能从
// `ambiguous_canonical_prices` 里找回来，那才叫消失。

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/kaixuan/llm-gateway-go/internal/vendorprice"
)

// canonicalCollision 是「同一个 canonical 出现了互相冲突的价」。
type canonicalCollision struct {
	Canonical string `json:"canonical"`
	// Vendor 是第一条的厂商。同一个 canonical 被**不同厂商**用不同价声明也算
	// 冲突（SSOT 的键只有 canonical_name，不含厂商），所以这一栏只是线索，
	// 不是一个分组键。
	Vendor  string `json:"vendor,omitempty"`
	Display string `json:"display_names"`
	// Rows 是被扣下的候选行数，Distinct 是其中有几个不同的价向量。
	// Rows 与 len(Prices) 的差即同价重复的行数。
	Rows     int              `json:"rows"`
	Distinct int              `json:"distinct_prices"`
	Prices   []collisionPrice `json:"prices"`
	Reason   string           `json:"reason"`
}

// collisionPrice 是被扣下的**一行**，原样保留出处。
type collisionPrice struct {
	Vendor      string   `json:"vendor"`
	DisplayName string   `json:"display_name"`
	Input       *float64 `json:"input_per_1m,omitempty"`
	Output      *float64 `json:"output_per_1m,omitempty"`
	CacheRead   *float64 `json:"cache_read_per_1m,omitempty"`
	CacheWrit   *float64 `json:"cache_write_per_1m,omitempty"`
	Currency    string   `json:"currency,omitempty"`
	Unit        string   `json:"unit,omitempty"`
	ProductTier string   `json:"product_tier,omitempty"`
	Tier        string   `json:"tier,omitempty"`
	SourceURL   string   `json:"source_url"`
	Row         string   `json:"row"`
	LineNo      int      `json:"line_no"`
}

func toCollisionPrice(c vendorprice.Candidate) collisionPrice {
	return collisionPrice{
		Vendor: c.Vendor, DisplayName: c.Model,
		Input: c.Input, Output: c.Output,
		CacheRead: c.CacheRead, CacheWrit: c.CacheWrit,
		Currency: c.Currency, Unit: c.Unit,
		ProductTier: c.ProductTier, Tier: c.Tier,
		SourceURL: c.SourceURL, Row: c.Row, LineNo: c.LineNo,
	}
}

// priceFields 是价格指纹的**字段清单**，按这个顺序拼指纹。
//
// ⚠ 顺序即契约：指纹只在自己内部比大小，不落盘，所以顺序变了不会破坏任何
// 外部约定；但下面 `differingPriceFields` 报的字段名必须与这里的键同名，
// 人要按报告去页面上核对。
var priceFields = []struct {
	key string
	get func(collisionPrice) string
}{
	{"currency", func(p collisionPrice) string { return p.Currency }},
	{"unit", func(p collisionPrice) string { return p.Unit }},
	{"input", func(p collisionPrice) string { return sigNum(p.Input) }},
	{"output", func(p collisionPrice) string { return sigNum(p.Output) }},
	{"cache_read", func(p collisionPrice) string { return sigNum(p.CacheRead) }},
	{"cache_write", func(p collisionPrice) string { return sigNum(p.CacheWrit) }},
}

// priceSignature 是「这一行报的价」的指纹。
func priceSignature(c vendorprice.Candidate) string {
	p := toCollisionPrice(c)
	parts := make([]string, 0, len(priceFields))
	for _, f := range priceFields {
		parts = append(parts, f.get(p))
	}
	return strings.Join(parts, "\x1f")
}

// sigNum 让 **nil 与 0 可区分**。
//
// 这一条不是洁癖：nil 是「这一行没报输入价」，0 是「这一行报输入价是 0」。
// 两者塌成一个键之后，一个「只报了输出价」的行与一个「输入输出都 0」的行会被
// 判成同价重复而放行 —— 于是少了一条价冲突，报告上却写着「无冲突」。
// 判据 TestNilAndZeroAreNotTheSamePriceSignature 钉的就是这里。
func sigNum(p *float64) string {
	if p == nil {
		return "nil"
	}
	return strconv.FormatFloat(*p, 'g', -1, 64)
}

// differingPriceFields 返回 a 与 b 指纹不同的字段名（已排序，报告可复现）。
func differingPriceFields(a, b collisionPrice) []string {
	var out []string
	for _, f := range priceFields {
		if f.get(a) != f.get(b) {
			out = append(out, f.key)
		}
	}
	sort.Strings(out)
	return out
}

// distinctPrices 按**首次出现顺序**返回这组候选里几个不同的价。
func distinctPrices(group []vendorprice.Candidate) ([]collisionPrice, int) {
	seen := map[string]bool{}
	var out []collisionPrice
	for _, c := range group {
		sig := priceSignature(c)
		if seen[sig] {
			continue
		}
		seen[sig] = true
		out = append(out, toCollisionPrice(c))
	}
	return out, len(out)
}

// withholdConflictingPrices 把「同一个 canonical 有两个不同价」的候选**整组扣下**，
// 并按 canonical 名返回冲突清单。留下的候选保持原顺序。
//
// 判不出 canonical 名的候选原样放过：它们归 Unresolved 桶管（问题在名字不在价），
// 而把一堆 canonical 为空的行按空串分组，会造出一个「所有未解析的行互相冲突」的
// 假冲突。
func withholdConflictingPrices(ready []vendorprice.Candidate) ([]vendorprice.Candidate, []canonicalCollision) {
	rows := map[string][]vendorprice.Candidate{}
	var order []string
	for _, c := range ready {
		canon := resolvedCanonical(c)
		if canon == "" {
			continue
		}
		if _, seen := rows[canon]; !seen {
			order = append(order, canon)
		}
		rows[canon] = append(rows[canon], c)
	}

	blocked := map[string]bool{}
	var conflicts []canonicalCollision
	for _, canon := range order {
		group := rows[canon]
		if len(group) < 2 {
			continue
		}
		prices, distinct := distinctPrices(group)
		if distinct < 2 {
			continue // 同价重复：数字唯一，不是冲突
		}
		blocked[canon] = true
		conflicts = append(conflicts, newCollision(canon, group, prices, distinct))
	}
	if len(conflicts) == 0 {
		return ready, nil
	}

	kept := make([]vendorprice.Candidate, 0, len(ready))
	for _, c := range ready {
		if canon := resolvedCanonical(c); canon != "" && blocked[canon] {
			continue
		}
		kept = append(kept, c)
	}
	return kept, conflicts
}

func newCollision(canon string, group []vendorprice.Candidate, prices []collisionPrice, distinct int) canonicalCollision {
	// 报出**到底哪些字段不同**：只说「价不一样」，人得自己回页面比对三张表。
	var diffs []string
	seenDiff := map[string]bool{}
	for i := 0; i < len(prices); i++ {
		for j := i + 1; j < len(prices); j++ {
			for _, f := range differingPriceFields(prices[i], prices[j]) {
				if !seenDiff[f] {
					seenDiff[f] = true
					diffs = append(diffs, f)
				}
			}
		}
	}
	sort.Strings(diffs)

	displays := map[string]bool{}
	for _, c := range group {
		if c.Model != "" {
			displays[c.Model] = true
		}
	}
	var names []string
	for d := range displays {
		names = append(names, d)
	}
	sort.Strings(names)

	diff := "no field differs"
	if len(diffs) > 0 {
		diff = "differing field(s): " + strings.Join(diffs, ", ")
	}

	return canonicalCollision{
		Canonical: canon,
		Vendor:    group[0].Vendor,
		Display:   strings.Join(names, " / "),
		Rows:      len(group),
		Distinct:  distinct,
		Prices:    prices,
		Reason: fmt.Sprintf(
			"%d row(s) on the vendor page(s) claim canonical %q with %d different price vectors (%s). "+
				"The baseline price SSOT holds ONE price per model, so this column has no correct "+
				"value here: taking the first row would make the recorded price a function of where "+
				"the vendor happened to place the table, and re-ordering the page would silently "+
				"change it. All %d row(s) are withheld and listed in this entry for a human to pick. "+
				"If the rows are really two billing dimensions, the fix is two canonical model "+
				"names (or a dimension column in the SSOT) — a schema decision this tool must not "+
				"make on its own.",
			len(group), canon, distinct, diff, len(group)),
	}
}

// Report 是给人看的那一段（写进 stderr）。提案里已有结构化的 Prices，
// 这里负责让「不用打开 JSON 就知道该去看哪两行」。
func (c canonicalCollision) Report() string {
	var b strings.Builder
	fmt.Fprintf(&b, "[price-conflict] %s\n", c.Canonical)
	fmt.Fprintf(&b, "    %s\n", c.Reason)
	for _, p := range c.Prices {
		fmt.Fprintf(&b, "    %-14s %s/%s %s  (%s:%d)\n",
			p.Vendor, sigNum(p.Input), sigNum(p.Output), p.Currency, shortSource(p.SourceURL), p.LineNo)
	}
	return b.String()
}

func shortSource(u string) string {
	u = strings.TrimPrefix(u, "https://")
	u = strings.TrimPrefix(u, "http://")
	if i := strings.Index(u, "/"); i > 0 {
		u = u[:i]
	}
	if u == "" {
		return "?"
	}
	return u
}
