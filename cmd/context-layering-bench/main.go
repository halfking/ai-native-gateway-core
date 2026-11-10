// Command context-layering-bench —— 「总结摘要 + 分层加载」可行性 bench。
//
// 它回答一个具体问题：**把「注意力放在最近的任务、顶层只放每一步的目标与
// 结果、每一步的细节放进记忆按需加载」这套做法，落到本仓现有能力上可行吗？**
//
// 本 bench 用仓内已导出的真实组件跑，不新造机制：
//
//	domains/hooks/compression.AnalyzeTasks      真实的步边界识别器
//	domains/hooks/compression.NewEstimator      真实的窗口判据
//	domains/hooks/compression.RebuildOpenAIAfterSummary
//	                                           真实的「摘要置顶 + 尾部保留」装配器
//	                                           （同包，故由同包的守护测试钉桩）
//
// 用法：
//
//	go run ./cmd/context-layering-bench
//	go run ./cmd/context-layering-bench -steps 16 -filler 4000 -json
//
// 退出码恒为 0（本工具只报告，不判定）；判定由 main_test.go 承担。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
)

// ★ 窗口口径（2026-11-10 订正）：本 bench 有**两个**互不相同的窗口，
//
//	会话窗口（Window）：会话模型本身能吃下的上下文（生产 ≈ 200K）。
//	本地处理窗口（LocalWindow）：网关用来做「步边界识别 / 记忆处理」的
//	  本地模型的窗口（≈ 32K）。
//
//	★ 2026-11-10 之前本 bench 只有 32K 一个窗口，于是把「本地处理窗口」
//	  误当成了「会话窗口」，得出「现状 LLM 摘要也超窗 1.34×」的结论。
//	  那个结论在 32K 下成立、在 200K 下**不成立**（摘要 44,055 token 装得下）
//	  ⇒ 已订正。真正成立的约束是：喂给**本地 32K 模型**的表示必须装得下
//	  —— 分层基座 8,861 装得下，LLM 摘要 44,055 装不下。
func main() {
	steps := flag.Int("steps", 12, "合成会话的步数")
	filler := flag.Int("filler", 2500, "每条工具输出的字节数（模拟文件/命令输出）")
	tail := flag.Int("tail", 4, "分层上下文保留的最近原始消息条数")
	window := flag.Int("window", DefaultSessionWindow, "会话模型的上下文窗口（token）")
	localWindow := flag.Int("local-window", DefaultLocalWindow, "本地识别/记忆处理模型的窗口（token）")
	asJSON := flag.Bool("json", false, "以 JSON 输出")
	flag.Parse()

	rep := Run(Params{
		Steps:       *steps,
		Filler:      *filler,
		Tail:        *tail,
		Window:      *window,
		LocalWindow: *localWindow,
	})

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
		return
	}
	fmt.Print(rep.Render())
}

// ── 窗口口径常量 ────────────────────────────────────────────────────────────

const (
	// DefaultSessionWindow 是生产会话模型的上下文窗口（≈200K）。
	DefaultSessionWindow = 200000
	// DefaultLocalWindow 是网关做「步边界识别 / 记忆处理」的本地模型窗口（≈32K）。
	DefaultLocalWindow = 32768
)

// Params 是一次 bench 的参数。
//
// ★ Window 与 LocalWindow 是**两个**窗口，不要混用（见 main() 的口径说明）。
type Params struct {
	// Steps 是合成会话的步数。
	Steps int
	// Filler 是每条工具输出的字节数。
	Filler int
	// Tail 是分层上下文保留的最近原始消息条数。
	Tail int
	// Window 是会话模型的上下文窗口。
	Window int
	// LocalWindow 是本地识别/记忆处理模型的窗口（0 ⇒ 取 DefaultLocalWindow）。
	LocalWindow int
}

// BudgetCheck 是一条「某个表示能不能装进某个窗口」的判据。
type BudgetCheck struct {
	// Name 是表示名。
	Name string `json:"name"`
	// Tokens 是该表示的 token 量。
	Tokens int `json:"tokens"`
	// Fits 是否装得进该窗口。
	Fits bool `json:"fits"`
	// Headroom 是剩余 token（负数 = 超出多少）。
	Headroom int `json:"headroom"`
}

// BudgetRow 是「一个窗口 × 一组表示」的装载判据。
type BudgetRow struct {
	// Window 是窗口大小（token）。
	Window int `json:"window"`
	// Role 是窗口的角色：session（会话模型）/ local_processing（本地识别/记忆处理模型）。
	Role string `json:"role"`
	// Checks 是各表示的装载判据。
	Checks []BudgetCheck `json:"checks"`
}

// LocaleReport 是一个语种下的分层可行性读数。
type LocaleReport struct {
	Locale string `json:"locale"`

	Steps    int `json:"steps"`
	Turns    int `json:"turns"`
	Messages int `json:"messages"`

	// ── token 计量 ──
	FullTokens    int `json:"full_tokens"`
	TrimTokens    int `json:"trim_tokens"`
	SummaryTokens int `json:"llm_summary_tokens"`
	IndexTokens   int `json:"index_tokens"`
	TailTokens    int `json:"tail_tokens"`
	ReloadTokens  int `json:"reload_tokens"`
	LayeredTokens int `json:"layered_tokens"`
	// LayeredBaseTokens 是分层基座（索引 + 尾部，不含按需明细）的 token 量。
	//
	// ★ 它是喂给**本地识别/记忆处理模型**的那份表示 ⇒ 它的窗口约束
	//   是 LocalWindow（32K），不是会话窗口（200K）。
	LayeredBaseTokens int `json:"layered_base_tokens"`

	// ── 装载判据（按窗口分角色）──
	Budgets []BudgetRow `json:"budgets"`
	// ReloadableStepsLocal 是在**本地处理窗口**内，分层基座之上还能
	// 再按需加载几步明细。
	ReloadableStepsLocal int `json:"reloadable_steps_local"`

	// ── 质量 ──
	IndexKeyRecall float64 `json:"index_key_recall"`

	// ── 步边界识别（真实 AnalyzeTasks）──
	BoundaryAlign    float64 `json:"boundary_align"`
	BoundaryCoverage float64 `json:"boundary_coverage"`
	BoundaryFound    int     `json:"boundary_found"`
	BoundaryGround   int     `json:"boundary_ground_truth"`

	// ── 按需加载 ──
	DetailRecoverable bool `json:"detail_recoverable"`
	ReloadableSteps   int  `json:"reloadable_steps"`

	// ── 一次真实的「按需加载」场景 ──
	ReloadStepIndex      int `json:"reload_step_index"`
	ReloadOneStepTokens  int `json:"reload_one_step_tokens"`
	LayeredWithReload    int `json:"layered_with_reload_tokens"`
	FullDetailStepsInWin int `json:"full_detail_steps_within_window"`

	// ── 存储放大（理想 delta vs 生产实际形态）──
	IdealDeltaBytes int     `json:"ideal_delta_bytes"`
	ProdActualBytes int     `json:"prod_actual_bytes"`
	StorageAmplif   float64 `json:"storage_amplification_x"`
	HistoryBytes    int     `json:"history_bytes"`
	ProdVsHistoryX  float64 `json:"prod_total_vs_final_history_x"`
}

// Report 是一次 bench 的完整读数。
type Report struct {
	Window int `json:"window"`
	// LocalWindow 是本地识别/记忆处理模型的窗口。
	LocalWindow int            `json:"local_window"`
	TailMsgs    int            `json:"tail_msgs"`
	Locales     []LocaleReport `json:"locales"`
}

// Run 跑一遍 bench。
func Run(p Params) Report {
	if p.LocalWindow <= 0 {
		p.LocalWindow = DefaultLocalWindow
	}
	rep := Report{Window: p.Window, LocalWindow: p.LocalWindow, TailMsgs: p.Tail}
	for _, loc := range []struct {
		name string
		tmpl []stepSpec
	}{{"en", enTemplates}, {"zh", cnTemplates}} {
		rep.Locales = append(rep.Locales, runLocale(loc.name, loc.tmpl, p))
	}
	return rep
}

// runLocale 跑一个语种。
func runLocale(locale string, tmpl []stepSpec, p Params) LocaleReport {
	specs := buildSpecs(tmpl, p.Steps)
	turns := buildSession(specs, p.Filler)
	full := turns[len(turns)-1].body

	sums := buildStepSummaries(turns)
	details := buildStepDetails(turns)

	plan := assembleLayered(full, sums, details, p.Tail, nil, p.Window)

	lr := LocaleReport{
		Locale:            locale,
		Steps:             len(specs),
		Turns:             len(turns),
		Messages:          len(full),
		FullTokens:        plan.FullTokens,
		TrimTokens:        plan.TrimTokens,
		SummaryTokens:     plan.SummaryTokens,
		IndexTokens:       plan.IndexTokens,
		TailTokens:        plan.TailTokens,
		ReloadTokens:      plan.ReloadTokens,
		LayeredTokens:     plan.TotalTokens,
		LayeredBaseTokens: plan.IndexTokens + plan.TailTokens,
		IndexKeyRecall:    indexKeyRecall(sums, specs),
		ReloadableSteps:   len(plan.Reloadable),
	}

	// ── 步边界识别：真实 AnalyzeTasks ──
	lr.BoundaryAlign, lr.BoundaryCoverage, lr.BoundaryFound, lr.BoundaryGround =
		measureBoundaries(full, len(specs))

	// ── 按需加载：逐字节还原某一步的明细 ──
	lr.DetailRecoverable = verifyReload(details, turns)

	// ── 一次真实的「按需加载」场景：模型点名要中间某一步的细节 ──
	if len(details) > 0 {
		target := details[len(details)/2]
		lr.ReloadStepIndex = target.StepIndex
		lr.ReloadOneStepTokens = target.Tokens
		lr.LayeredWithReload = plan.IndexTokens + plan.TailTokens + target.Tokens
	}
	// 窗口预算内能【同时】完整放下几步的明细（与「索引 + 按需」的替代读数对照）。
	used := plan.IndexTokens + plan.TailTokens
	for _, d := range details {
		if used+d.Tokens > p.Window {
			break
		}
		used += d.Tokens
		lr.FullDetailStepsInWin++
	}

	// ── 本地处理窗口内的按需加载余量 ──
	usedLocal := plan.IndexTokens + plan.TailTokens
	for _, d := range details {
		if usedLocal+d.Tokens > p.LocalWindow {
			break
		}
		usedLocal += d.Tokens
		lr.ReloadableStepsLocal++
	}

	// ── 存储放大 ──
	ideal, prod := storageAmplification(turns)
	hist := len(joinText(full))
	lr.IdealDeltaBytes = ideal
	lr.ProdActualBytes = prod
	lr.HistoryBytes = hist
	if ideal > 0 {
		lr.StorageAmplif = float64(prod) / float64(ideal)
	}
	if hist > 0 {
		lr.ProdVsHistoryX = float64(prod) / float64(hist)
	}

	// ── 装载判据：会话窗口 vs 本地处理窗口 ──
	lr.Budgets = []BudgetRow{
		{
			Window: p.Window,
			Role:   "session",
			Checks: []BudgetCheck{
				{"完整历史", lr.FullTokens, lr.FullTokens <= p.Window, p.Window - lr.FullTokens},
				{"现状LLM摘要", lr.SummaryTokens, lr.SummaryTokens <= p.Window, p.Window - lr.SummaryTokens},
				{"机械裁剪(drop-oldest)", lr.TrimTokens, lr.TrimTokens <= p.Window, p.Window - lr.TrimTokens},
				{"分层基座(索引+尾部)", lr.LayeredBaseTokens, lr.LayeredBaseTokens <= p.Window, p.Window - lr.LayeredBaseTokens},
				{"分层+按需一步", lr.LayeredWithReload, lr.LayeredWithReload <= p.Window, p.Window - lr.LayeredWithReload},
			},
		},
		{
			Window: p.LocalWindow,
			Role:   "local_processing",
			Checks: []BudgetCheck{
				{"完整历史", lr.FullTokens, lr.FullTokens <= p.LocalWindow, p.LocalWindow - lr.FullTokens},
				{"现状LLM摘要", lr.SummaryTokens, lr.SummaryTokens <= p.LocalWindow, p.LocalWindow - lr.SummaryTokens},
				{"机械裁剪(drop-oldest)", lr.TrimTokens, lr.TrimTokens <= p.LocalWindow, p.LocalWindow - lr.TrimTokens},
				{"分层基座(索引+尾部)", lr.LayeredBaseTokens, lr.LayeredBaseTokens <= p.LocalWindow, p.LocalWindow - lr.LayeredBaseTokens},
				{"分层+按需一步", lr.LayeredWithReload, lr.LayeredWithReload <= p.LocalWindow, p.LocalWindow - lr.LayeredWithReload},
			},
		},
	}
	return lr
}

// measureBoundaries 用真实的 compression.AnalyzeTasks 测量步边界识别质量。
//
// 真值：每一步的【目标消息下标】（合成器给出，由 " (#N)" 序号判别，
// 与 AnalyzeTasks 内部的英文模式识别完全独立）。
//
// ★ 两个指标（缺一不可，单看任一都会误判）：
//
//	对齐率（align）：检测出的 span 的 EndIdx+1 是否**恰好**是某个目标下标。
//	  AnalyzeTasks 的 span 构造是 [prevSignal+1, signalIdx-1]
//	  ⇒ 它的右端天然对齐「下一个目标」⇒ 这才是「边界找准没」的判据。
//	覆盖率（coverage）：每一步的**内容段**（目标之后到下一个目标之前）
//	  是否被某个检测出的 span 覆盖 ≥50%。
//	  这一项是必要的：现有构造把【触发边界的那条目标消息本身】排除在
//	  span 之外（StartIdx = prevSignal+1），⇒ 覆盖率能暴露这个缺口。
func measureBoundaries(full []wireMsg, steps int) (align, coverage float64, found, ground int) {
	goalIdxs := []int{}
	for i, m := range full {
		if msgRole(m) == "user" && isStepGoal(msgText(m)) {
			goalIdxs = append(goalIdxs, i)
		}
	}
	ground = len(goalIdxs)

	res := compression.AnalyzeTasks(full)
	if res == nil || len(res.Boundaries) == 0 {
		return 0, 0, 0, ground
	}
	found = len(res.Boundaries)

	goalSet := map[int]bool{}
	for _, g := range goalIdxs {
		goalSet[g] = true
	}
	aligned := 0
	for _, b := range res.Boundaries {
		if goalSet[b.EndIdx+1] {
			aligned++
		}
	}
	align = float64(aligned) / float64(found)

	covered := 0
	for si, g := range goalIdxs {
		end := len(full) - 1
		if si+1 < len(goalIdxs) {
			end = goalIdxs[si+1] - 1
		}
		contentStart, contentEnd := g+1, end
		if contentStart > contentEnd {
			covered++ // 该步没有内容段（只有目标），无内容可丢
			continue
		}
		spanLen := contentEnd - contentStart + 1
		best := 0
		for _, b := range res.Boundaries {
			if ov := overlapLen(b.StartIdx, b.EndIdx, contentStart, contentEnd); ov > best {
				best = ov
			}
		}
		if float64(best) >= 0.5*float64(spanLen) {
			covered++
		}
	}
	coverage = float64(covered) / float64(ground)
	return align, coverage, found, ground
}

// overlapLen 求两个闭区间的交集长度。
func overlapLen(a1, a2, b1, b2 int) int {
	lo := a1
	if b1 > lo {
		lo = b1
	}
	hi := a2
	if b2 < hi {
		hi = b2
	}
	if hi < lo {
		return 0
	}
	return hi - lo + 1
}

// isStepGoal 判定一条 user 消息是否是某一步的目标（合成器的真值口径）。
//
// 合成器给每条目标消息都追加了 " (#N)" 序号 ⇒ 用它做真值判据，
// 与 AnalyzeTasks 内部的英文模式识别完全独立。
func isStepGoal(text string) bool {
	for i := len(text) - 1; i >= 0; i-- {
		if text[i] == ')' {
			return true
		}
		if text[i] == '(' {
			return false
		}
	}
	return false
}

// verifyReload 验证「按步号重载明细」能逐字节还原该步在历史中的内容。
func verifyReload(details []stepDetail, turns []turnRec) bool {
	for _, t := range turns {
		got, ok := reloadStep(details, t.stepIdx+1)
		if !ok {
			return false
		}
		want := joinText(stepMessages(turns, t.stepIdx))
		if got != want {
			return false
		}
	}
	return true
}

// stepMessages 取某一步在历史中的全部消息（真值口径：从轮序列里按步收集）。
func stepMessages(turns []turnRec, stepIdx int) []wireMsg {
	var out []wireMsg
	for _, t := range turns {
		if t.stepIdx != stepIdx {
			continue
		}
		out = append(out, t.delta...)
		out = append(out, t.resp...)
	}
	return out
}

// Render 把读数渲染成人类可读的报告。
func (r Report) Render() string {
	out := ""
	out += "══ 上下文分层加载可行性 bench ══\n"
	out += fmt.Sprintf("会话窗口=%d token   本地识别/记忆处理窗口=%d token   尾部保留=%d 条消息\n\n",
		r.Window, r.LocalWindow, r.TailMsgs)
	for _, l := range r.Locales {
		out += fmt.Sprintf("── 语种 %s（%d 步 / %d 轮 / %d 条消息）──\n", l.Locale, l.Steps, l.Turns, l.Messages)
		out += fmt.Sprintf("  token: 完整历史=%d  机械裁剪=%d  LLM摘要=%d  分层基座=%d  分层合计=%d\n",
			l.FullTokens, l.TrimTokens, l.SummaryTokens, l.LayeredBaseTokens, l.LayeredTokens)
		out += fmt.Sprintf("        其中 L0索引=%d  尾部=%d  重载明细=%d\n",
			l.IndexTokens, l.TailTokens, l.ReloadTokens)
		out += fmt.Sprintf("  L0 索引关键标识符保留率 = %.1f%%\n", l.IndexKeyRecall*100)
		out += fmt.Sprintf("  步边界识别（真实 AnalyzeTasks）：对齐=%.1f%% 覆盖=%.1f%% (识别 %d / 真值 %d)\n",
			l.BoundaryAlign*100, l.BoundaryCoverage*100, l.BoundaryFound, l.BoundaryGround)
		out += fmt.Sprintf("  按需重载逐字节还原 = %v   会话窗口内可再按需加载 %d 步（本地窗口内 %d 步）\n",
			l.DetailRecoverable, l.ReloadableSteps, l.ReloadableStepsLocal)
		out += fmt.Sprintf("  按需加载场景：点名要步%d ⇒ 代价 %d token，分层合计 %d（对比完整历史 %d）\n",
			l.ReloadStepIndex, l.ReloadOneStepTokens, l.LayeredWithReload, l.FullTokens)
		out += fmt.Sprintf("  会话窗口内可【同时】完整放下 %d 步明细\n", l.FullDetailStepsInWin)
		out += fmt.Sprintf("  存储：理想delta=%dB  生产实际=%dB (放大 %.1f×，二次方系数 prod/最终历史=%.1f×)\n",
			l.IdealDeltaBytes, l.ProdActualBytes, l.StorageAmplif, l.ProdVsHistoryX)
		for _, b := range l.Budgets {
			out += fmt.Sprintf("  【%s 窗口 %d token】\n", b.Role, b.Window)
			for _, c := range b.Checks {
				mark := "装得下"
				if !c.Fits {
					mark = fmt.Sprintf("装不下(超 %d)", -c.Headroom)
				}
				out += fmt.Sprintf("    %-24s %8d token  %s\n", c.Name, c.Tokens, mark)
			}
		}
		out += "\n"
	}
	return out
}
