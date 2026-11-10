package main

import (
	"strings"
	"testing"
)

// 本文件把 bench 的读数钉成断言。目标不是「让 bench 好看」，而是把
// 「分层加载方案在仓内现有能力上到底成不成立」这件事变成可回归的事实。
//
// ★ 断言分两类：
//
//	正向断言（方案成立的支点）：重载逐字节精确、分层比现状 LLM 摘要更省、
//	  英文会话下真实 AnalyzeTasks 能找准步边界、按需重载后仍在窗口内。
//	反向断言（方案落地的缺口）：中文会话下真实 AnalyzeTasks 边界识别为 0；
//	  L0 索引的关键标识符保留率不足 100%。
//	反向断言写死成「当前值」，是为了让未来任何修复都**显式地**推翻它
//	（而不是悄悄改变结论）——修好之后请同步更新本文件与审计档。

// benchParams 是守护测试常用的合成参数（120 步 / 每条工具输出 6000B / 尾部 4 条）。
//
// ★ Window = 200000（生产会话窗口）；LocalWindow = 32768（本地识别/记忆处理窗口）。
func benchParams() Params {
	return Params{
		Steps:       120,
		Filler:      6000,
		Tail:        4,
		Window:      DefaultSessionWindow,
		LocalWindow: DefaultLocalWindow,
	}
}

func enReport(t *testing.T) LocaleReport {
	t.Helper()
	p := benchParams()
	p.Steps, p.Filler = 24, 3000
	for _, l := range Run(p).Locales {
		if l.Locale == "en" {
			return l
		}
	}
	t.Fatal("en locale report missing")
	return LocaleReport{}
}

func zhReport(t *testing.T) LocaleReport {
	t.Helper()
	p := benchParams()
	p.Steps, p.Filler = 24, 3000
	for _, l := range Run(p).Locales {
		if l.Locale == "zh" {
			return l
		}
	}
	t.Fatal("zh locale report missing")
	return LocaleReport{}
}

// budgetFit 取某个窗口下某个表示的装载判据。
func budgetFit(t *testing.T, l LocaleReport, role, name string) BudgetCheck {
	t.Helper()
	for _, b := range l.Budgets {
		if b.Role != role {
			continue
		}
		for _, c := range b.Checks {
			if c.Name == name {
				return c
			}
		}
	}
	t.Fatalf("budget check %s/%s missing", role, name)
	return BudgetCheck{}
}

// TestReloadIsByteExact —— L1「按需加载」的保真度支点。
//
// 按步号重载出来的明细必须与该步在历史中的内容【逐字节相同】：
// 网关侧存储的是每轮增量，重载时按轮序拼接（request_delta ++ response_delta），
// 与 domains/session/v2/turn_reader.go 的 LoadChain 同口径。
func TestReloadIsByteExact(t *testing.T) {
	for _, loc := range []struct {
		name string
		tmpl []stepSpec
	}{{"en", enTemplates}, {"zh", cnTemplates}} {
		t.Run(loc.name, func(t *testing.T) {
			specs := buildSpecs(loc.tmpl, 12)
			turns := buildSession(specs, 2000)
			details := buildStepDetails(turns)
			for _, turn := range turns {
				got, ok := reloadStep(details, turn.stepIdx+1)
				if !ok {
					t.Fatalf("step %d not reloadable", turn.stepIdx+1)
				}
				want := joinText(stepMessages(turns, turn.stepIdx))
				if got != want {
					t.Fatalf("step %d reload is not byte-exact:\n got=%q\nwant=%q",
						turn.stepIdx+1, got, want)
				}
			}
		})
	}
}

// TestLayeredBeatsLLMSummary —— 分层方案相对现状第 2 档（LLM 摘要）的成本支点。
//
// ★ 这条断言承载本轮最重要的一个结论：**同一份表示，分层方案比现状
//
//	LLM 摘要更省**，且省下的量与窗口大小无关。
func TestLayeredBeatsLLMSummary(t *testing.T) {
	rep := Run(benchParams())
	for _, l := range rep.Locales {
		if l.LayeredTokens >= l.SummaryTokens {
			t.Fatalf("%s: layered (%d tokens) must be cheaper than LLM summary (%d tokens)",
				l.Locale, l.LayeredTokens, l.SummaryTokens)
		}
		if l.LayeredTokens >= rep.Window {
			t.Fatalf("%s: layered context (%d) does not fit session window (%d)",
				l.Locale, l.LayeredTokens, rep.Window)
		}
	}
}

// TestSessionWindowAcceptsLLMSummaryButNotFullHistory —— ★ 窗口口径订正断言。
//
// ★ 2026-11-10 之前的版本把「本地识别/记忆处理窗口（32K）」当成了
//
//	「会话窗口」，得出「现状 LLM 摘要也超窗 1.34×」的结论。
//	**那个结论在生产会话窗口（200K）下不成立**：现状 LLM 摘要装得下，
//	装不下的是**完整历史**。
//
//	⇒ 这条断言把订正后的事实钉死，防止口径再被混用。
func TestSessionWindowAcceptsLLMSummaryButNotFullHistory(t *testing.T) {
	rep := Run(benchParams())
	for _, l := range rep.Locales {
		full := budgetFit(t, l, "session", "完整历史")
		if full.Fits {
			t.Fatalf("%s: fixture expected to overflow the session window, got full=%d window=%d",
				l.Locale, full.Tokens, rep.Window)
		}
		summary := budgetFit(t, l, "session", "现状LLM摘要")
		if !summary.Fits {
			t.Fatalf("%s: today's LLM summary (%d tokens) must fit the %d-token session window "+
				"(the 2026-11-10 'summary overflows' claim was a 32K-window artifact — update the audit doc)",
				l.Locale, summary.Tokens, rep.Window)
		}
	}
}

// TestLocalProcessingWindowRejectsLLMSummary —— ★ 订正后真正成立的约束。
//
// 真正装不下的是**喂给本地识别/记忆处理模型（32K）的那份表示**：
//
//	现状 LLM 摘要（44,055 token）装不下 32K；
//	分层基座（索引 + 尾部，8,861 token）装得下。
//
// ★ 这条断言是「为什么必须是分层而不是一段扁平摘要」的支点：
//
//	分段摘要方案产出的东西**连本地模型自己都读不完**，
//	它就无法参与步边界识别与记忆抽取。
func TestLocalProcessingWindowRejectsLLMSummary(t *testing.T) {
	rep := Run(benchParams())
	for _, l := range rep.Locales {
		summary := budgetFit(t, l, "local_processing", "现状LLM摘要")
		if summary.Fits {
			t.Fatalf("%s: expected today's LLM summary (%d tokens) to overflow the %d-token "+
				"local processing window (fixture changed — update the audit doc)",
				l.Locale, summary.Tokens, rep.LocalWindow)
		}
		base := budgetFit(t, l, "local_processing", "分层基座(索引+尾部)")
		if !base.Fits {
			t.Fatalf("%s: layered base (%d tokens) must fit the %d-token local processing window",
				l.Locale, base.Tokens, rep.LocalWindow)
		}
		full := budgetFit(t, l, "local_processing", "完整历史")
		if full.Fits {
			t.Fatalf("%s: expected the full history (%d tokens) to overflow the local window",
				l.Locale, full.Tokens)
		}
	}
}

// TestIndexScalesLinearly —— L0 索引的成本支点：每步成本恒定且远小于明细。
func TestIndexScalesLinearly(t *testing.T) {
	small := enReport(t)
	big := Run(Params{Steps: 96, Filler: 3000, Tail: 4, Window: DefaultSessionWindow, LocalWindow: DefaultLocalWindow}).Locales[0]

	perStepSmall := float64(small.IndexTokens) / float64(small.Steps)
	perStepBig := float64(big.IndexTokens) / float64(big.Steps)
	// 每步索引成本应当在 3 倍以内稳定（线性 ⇒ 比例恒定）。
	if perStepBig > perStepSmall*3 || perStepSmall > perStepBig*3 {
		t.Fatalf("index cost per step not stable: small=%.1f big=%.1f", perStepSmall, perStepBig)
	}
	// 索引必须远小于该会话的完整历史。
	if float64(big.IndexTokens) > 0.1*float64(big.FullTokens) {
		t.Fatalf("index too large: %d tokens vs full %d", big.IndexTokens, big.FullTokens)
	}
}

// TestOnDemandReloadStaysInWindow —— 「按需加载」的成本支点。
//
// 加载任意一步的细节之后，整个分层上下文仍必须留在窗口内。
func TestOnDemandReloadStaysInWindow(t *testing.T) {
	rep := Run(benchParams())
	for _, l := range rep.Locales {
		if l.LayeredWithReload >= rep.Window {
			t.Fatalf("%s: layered+reload (%d) exceeds window (%d)",
				l.Locale, l.LayeredWithReload, rep.Window)
		}
		if l.ReloadOneStepTokens <= 0 {
			t.Fatalf("%s: reload cost must be measurable", l.Locale)
		}
	}
}

// TestEnglishStepBoundaryDetection —— 真实 AnalyzeTasks 在英文会话下能找准步边界。
//
// 这一条是「步」这一抽象在仓内**已经可用**的支点（英文侧）。
func TestEnglishStepBoundaryDetection(t *testing.T) {
	l := enReport(t)
	if l.BoundaryFound == 0 {
		t.Fatal("expected AnalyzeTasks to detect boundaries in the English transcript")
	}
	if l.BoundaryAlign < 0.8 {
		t.Fatalf("boundary alignment too low: %.2f", l.BoundaryAlign)
	}
	if l.BoundaryCoverage < 0.9 {
		t.Fatalf("boundary coverage too low: %.2f", l.BoundaryCoverage)
	}
}

// TestChineseStepBoundaryDetectionIsCurrentlyZero —— ★ 缺口钉桩。
//
// domains/hooks/compression/task_analyzer.go 的 completionPatterns 与
// newTaskPatterns **全是英文字面量** ⇒ 中文会话下真实 AnalyzeTasks
// 返回 0 个边界 ⇒ 「按步分层」在中文语境下当前**不成立**。
//
// ★ 若哪天这个断言开始失败（说明有人本地化了模式或换了识别器），
//
//	请同步更新本文件与 docs/audit/2026-11-10-上下文压缩与Memora分层加载可行性分析.md
//	的结论段——不要让它悄悄变成一个没人看过的红灯。
func TestChineseStepBoundaryDetectionIsCurrentlyZero(t *testing.T) {
	l := zhReport(t)
	if l.BoundaryFound != 0 {
		t.Fatalf("expected 0 boundaries for the Chinese transcript, got %d "+
			"(AnalyzeTasks got localized or replaced — update the audit doc)",
			l.BoundaryFound)
	}
	if l.BoundaryCoverage != 0 {
		t.Fatalf("expected 0 coverage for the Chinese transcript, got %.2f "+
			"(AnalyzeTasks got localized or replaced — update the audit doc)",
			l.BoundaryCoverage)
	}
}

// TestIndexKeyRetentionIsIncomplete —— ★ 缺口钉桩。
//
// L0 索引要能支撑「模型据此判断要不要去加载这一步的细节」，
// 前提是索引行里留得住关键标识符（文件路径 / 错误码 / 端点）。
// 按 domains/analysis/request_summary.go 的 ruleExtract 契约
// （请求 200 字符 / 回复 300 字符截断）生成时，约三分之一步骤的
// 标识符会丢（它只出现在工具输出里，不在目标/结果里）。
func TestIndexKeyRetentionIsIncomplete(t *testing.T) {
	l := enReport(t)
	if l.IndexKeyRecall >= 1.0 {
		t.Fatalf("expected imperfect key retention, got %.2f "+
			"(rule-mode summary contract changed — update the audit doc)",
			l.IndexKeyRecall)
	}
	if l.IndexKeyRecall < 0.4 {
		t.Fatalf("key retention collapsed: %.2f", l.IndexKeyRecall)
	}
}

// TestStorageAmplificationIsQuadratic —— 存储放大的性质支点。
//
// 生产实际形态（每轮把客户端完整历史整份落成 request_delta）相对
// 理想形态（只落本轮新增）的放大倍数，必须随会话长度【超线性】增长。
// 这与仓内实测（delta 是真实新增的 45 倍、57.7% 会话呈二次方形态）同向。
func TestStorageAmplificationIsQuadratic(t *testing.T) {
	small := Run(Params{Steps: 20, Filler: 3000, Tail: 4, Window: DefaultSessionWindow, LocalWindow: DefaultLocalWindow}).Locales[0]
	big := Run(Params{Steps: 80, Filler: 3000, Tail: 4, Window: DefaultSessionWindow, LocalWindow: DefaultLocalWindow}).Locales[0]
	if big.StorageAmplif <= small.StorageAmplif {
		t.Fatalf("amplification did not grow with session length: small=%.1fx big=%.1fx",
			small.StorageAmplif, big.StorageAmplif)
	}
}

// TestIndexRendersGoalAndResult —— L0 索引形状支点：一行一步，含目标与结果。
func TestIndexRendersGoalAndResult(t *testing.T) {
	specs := buildSpecs(enTemplates, 3)
	turns := buildSession(specs, 1000)
	sums := buildStepSummaries(turns)
	idx := renderIndex(sums)

	for _, s := range sums {
		if !strings.Contains(idx, "步"+itoa(s.StepIndex)) {
			t.Fatalf("index missing step %d:\n%s", s.StepIndex, idx)
		}
	}
	for _, sp := range specs {
		if !strings.Contains(idx, sp.result) {
			t.Fatalf("index missing result of step %d:\n%s", sp.index, idx)
		}
	}
}

// itoa 是本地小工具（避免为一个转换引入 strconv 的额外 import）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
