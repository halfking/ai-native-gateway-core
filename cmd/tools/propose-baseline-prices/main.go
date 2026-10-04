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
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
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

	for _, name := range names {
		vp, known := vendorPage[name]
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

	if *canonicalFile != "" {
		names, err := readCanonicalNames(*canonicalFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read canonical list: %v\n", err)
			os.Exit(1)
		}
		ready := p.ReadyToReview
		p.ReadyToReview = nil
		for _, c := range ready {
			resolved, unres := resolveCanonical(c, names)
			if resolved == nil {
				p.Unresolved = append(p.Unresolved, unres)
				continue
			}
			p.ReadyToReview = append(p.ReadyToReview, *resolved)
		}
	}

	if *corroborate {
		if err := crossCheck(&p); err != nil {
			fmt.Fprintf(os.Stderr, "corroboration skipped: %v\n", err)
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

// readCanonicalNames 读 canonical 名清单（每行一个，# 开头为注释）。
func readCanonicalNames(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read-only
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out, sc.Err()
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
// 只用绝对下限会交叉挂错，实测就是这样：canonical 清单里同时存在
// claude-fable-5 与 claude-opus-4-8 时，多个展示名都拿到 0.90，而 0.90
// 正是仓里匹配器的**跨形态匹配**分（MatchStandardModels 的文档：1.0 =
// 完全一致；0.9+ = 跨形态/有界错字匹配）。它们是**并列**，差异来自
// tie-break 的字母序而不是证据 ⇒ Fable 的价被挂到 Opus 上、Opus 的价被
// 挂到 Fable 上，而两边都显示 0.90 分「已解析」。在计费语境下这是最坏的
// 一类错：它看起来是被核对过的。
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
