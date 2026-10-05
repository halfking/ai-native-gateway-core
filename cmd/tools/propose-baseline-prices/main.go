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
	"anthropic.md":     {"anthropic", "https://docs.anthropic.com/en/docs/about-claude/models/overview"},
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

	// CanonicalList 是 --canonical 名单的出处。**必须写进文件**：
	// 一份没有出处的提案里，那些 "resolved to canonical X" 的判词是没有
	// 依据的——margin 判据只在名单完整时成立，而提案会被拷到工单、issue、
	// 聊天里，脱离本工具的上下文。
	CanonicalList string `json:"canonical_list,omitempty"`
	// CanonicalDuplicates 是名单里「同一个模型的两种写法」。它必须进文件：
	// 判据说是「歧义已挡下」，人看到的是这句话；真正的根因在名单里，
	// 不写下来就得等下一次再撞一次。
	CanonicalDuplicates []string `json:"canonical_near_duplicates,omitempty"`
	// CorroborationRefused 说明互证为什么没做（或做了）。
	CorroborationRefused string `json:"corroboration_refused,omitempty"`
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
}

type baselineSide struct {
	Input  *float64 `json:"input_per_1m,omitempty"`
	Output *float64 `json:"output_per_1m,omitempty"`
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
	canonicalFile := flag.String("canonical", "",
		"file with one models_canonical.canonical_name per line; enables display-name resolution")
	corroborate := flag.Bool("corroborate", false,
		"also fetch the machine-readable source and cross-check each price (network)")
	flag.Parse()

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
			"source_url 核对；needs_human_eyes 里的条目**不可用**，原因见 warnings。",
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
		for _, c := range vendorprice.Extract(vp.Vendor, vp.URL, raw) {
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

	body, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal proposal: %v\n", err)
		os.Exit(1)
	}
	body = append(body, '\n')

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
	ranked := modelname.MatchStandardModels(c.Model, canonicalNames)
	if len(ranked) == 0 {
		u.Reason = "no canonical name reached the match score floor"
		return nil, u
	}
	best := ranked[0]
	u.BestScore = best.Score
	if best.Score < resolutionScoreFloor {
		u.Reason = fmt.Sprintf("best match %q scored %.2f, below the %.2f floor",
			best.Name, best.Score, resolutionScoreFloor)
		return nil, u
	}
	if len(ranked) > 1 {
		runnerUp := ranked[1]
		if best.Score-runnerUp.Score < resolutionMargin {
			u.Reason = fmt.Sprintf("ambiguous: %q scored %.2f but %q scored %.2f "+
				"(margin %.2f < %.2f) — a human must pick",
				best.Name, best.Score, runnerUp.Name, runnerUp.Score,
				best.Score-runnerUp.Score, resolutionMargin)
			return nil, u
		}
	}
	// 解析成功：把 canonical 名记进 Warnings 当作人可见的证据，不替换
	// Model 字段——原展示名要留着，回页面核对时看得见。
	c.Warnings = append(c.Warnings,
		fmt.Sprintf("display name %q resolved to canonical %q (score %.2f)", c.Model, best.Name, best.Score))
	return &c, unresolved{}
}

// crossCheck 给每条已解析的候选配一个第二信源，并判它们是否一致。
//
// **它不改任何价格**，只是把「一个页面上的数」变成「两个独立信源都说这个
// 数」。一致 ⇒ 提案的价值从「一个来源」升到「互证」；不一致 ⇒ 那是给人看的
// 信号，恰恰是这个工具最该产出的东西。
func crossCheck(p *proposal) error {
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
			p.NeedsHumanEyes = append(p.NeedsHumanEyes, c)
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
			Verdict: "corroborated",
		}
		if !pricesAgree(c.Input, c.Output, obs.InputPer1M, obs.OutputPer1M) {
			entry.Verdict = "sources_disagree"
		}
		p.Corroborated = append(p.Corroborated, entry)
	}
	p.ReadyToReview = kept
	return nil
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
