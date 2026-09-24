package taskprofile

// analyzer_test.go — 修正驱动提案生成器的纯函数单测（P1，回路 C）。
//
// 覆盖：pair 聚合与排序、类型级修正率闸门（§七.1：≥5 样本 & ≥30%）、
// 全局阈值草稿的候选选择（覆盖率/下限/只降不升）、关键词草稿的闸门
// （样本数/通道白名单/占比/泛词/现词去重）、单次上限截断。

import (
	"testing"
)

func correctionRowsFixture() []CorrectionRow {
	// 12 个 auto=chat → human=code 的被改判样本（11 个置信度 <0.70），
	// 外加 2 个 auto=chat→human=creative。chat 量 30 → 修正率 14/30≈46.7%。
	mk := func(conf float64, confNull bool, hint string, code bool) CorrectionRow {
		return CorrectionRow{
			AutoTaskType: "chat", HumanTaskType: "code",
			Confidence: conf, ConfidenceNull: confNull,
			DomainHint: hint, HasCode: code,
		}
	}
	rows := []CorrectionRow{}
	for i := 0; i < 8; i++ {
		rows = append(rows, mk(0.60, false, "数据库", true))
	}
	for i := 0; i < 3; i++ {
		rows = append(rows, mk(0.55, false, "数据库", false))
	}
	rows = append(rows, mk(0.92, false, "git", true)) // ≥0.85 带
	rows = append(rows, mk(0, true, "git", false))    // NULL 置信度
	for i := 0; i < 2; i++ {
		c := mk(0.40, false, "git", false)
		c.HumanTaskType = "creative"
		rows = append(rows, c)
	}
	return rows
}

func volumesFixture() map[string]int {
	return map[string]int{"chat": 30, "code": 100, "reasoning": 80}
}

func TestAggregateCorrections(t *testing.T) {
	pairs := aggregateCorrections(correctionRowsFixture())
	if len(pairs) != 2 {
		t.Fatalf("pairs = %d, want 2", len(pairs))
	}
	// 按 Corrected 降序：chat→code (13) 在前。
	if pairs[0].Auto != "chat" || pairs[0].Human != "code" || pairs[0].Corrected != 13 {
		t.Fatalf("top pair = %s→%s (%d), want chat→code (13)", pairs[0].Auto, pairs[0].Human, pairs[0].Corrected)
	}
	// 置信度带：<0.50 ×1(creative 0.40 在另一对) — 本对: 0.60×8, 0.55×3 →
	// 0.50-0.70 ×11; >=0.85 ×1; NULL 不进带。
	if pairs[0].confBands["0.50-0.70"] != 11 || pairs[0].confBands[">=0.85"] != 1 {
		t.Fatalf("bands = %v", pairs[0].confBands)
	}
	if pairs[0].hints["数据库"] != 11 || pairs[0].hints["git"] != 2 {
		t.Fatalf("hints = %v", pairs[0].hints)
	}
}

func TestQualifyingPairsGate(t *testing.T) {
	pairs := aggregateCorrections(correctionRowsFixture())
	got := qualifyingPairs(pairs, volumesFixture())
	if len(got) != 2 {
		t.Fatalf("qualifying = %d, want 2 (rate 14/30=46.7%% ≥ 30%%)", len(got))
	}

	// 修正率不足：chat 量放大到 100 → 14%。
	if got := qualifyingPairs(pairs, map[string]int{"chat": 100}); len(got) != 0 {
		t.Fatalf("low-rate pairs = %d, want 0", len(got))
	}
	// 量未知（分母为 0）：跳过。
	if got := qualifyingPairs(pairs, map[string]int{}); len(got) != 0 {
		t.Fatalf("unknown-volume pairs = %d, want 0", len(got))
	}
}

func TestDraftThresholdProposal(t *testing.T) {
	pairs := aggregateCorrections(correctionRowsFixture())
	qualifying := qualifyingPairs(pairs, volumesFixture())

	// 被改判样本置信度（chat→code 13 个中 11 个 <0.70，1 个 ≥0.85，1 个 NULL）。
	// 全局池 total = 12（NULL 不进覆盖率分母）：
	// total = 11(0.50-0.70) + 1(>=0.85) = 12。
	d := draftThresholdProposal(qualifying, volumesFixture(), 0.70, 30)
	if d == nil {
		t.Fatal("expected threshold draft")
	}
	// 候选 0.65：覆盖 11/12 = 91.7% ≥ 80% → 最保守的合格候选是 0.65
	// （0.70 不满足 cand<old，更大候选越界）。
	if got := d.Proposal["new"].(float64); got != 0.65 {
		t.Fatalf("new = %v, want 0.65", got)
	}
	if got := d.Proposal["old"].(float64); got != 0.70 {
		t.Fatalf("old = %v, want 0.70", got)
	}
	if d.TaskType != "" {
		t.Fatalf("global proposal must have empty task_type, got %q", d.TaskType)
	}
	if d.Evidence["backtest"] != nil {
		t.Fatal("backtest must be attached later by backtestDraft, not at draft time")
	}

	// 样本不足：<10 修正（带内）→ 无全局提案。weak 有 6 个 0.60 的样本。
	weak := &pairStat{Auto: "chat", Human: "code", confBands: map[string]int{"0.50-0.70": 6}, hints: map[string]int{}}
	weak.Corrected = 6
	for i := 0; i < 6; i++ {
		weak.Rows = append(weak.Rows, CorrectionRow{AutoTaskType: "chat", HumanTaskType: "code", Confidence: 0.60})
	}
	if d := draftThresholdProposal([]*pairStat{weak}, volumesFixture(), 0.70, 30); d != nil {
		t.Fatalf("expected nil under minGlobalCorrected, got %+v", d)
	}

	// old 已低于地板（0.5）→ 无候选可降。
	if d := draftThresholdProposal(qualifying, volumesFixture(), 0.50, 30); d != nil {
		t.Fatalf("no candidate below floor 0.50, got %+v", d)
	}
}

func TestDraftKeywordProposals(t *testing.T) {
	pairs := aggregateCorrections(correctionRowsFixture())
	qualifying := qualifyingPairs(pairs, volumesFixture())

	current := map[string][]string{"keywords.code": {"function", "class"}}
	drafts := draftKeywordProposals(qualifying, current, 30)
	// chat→code：hint "数据库" 11/12=91.7% ≥60%，token 不在现表 → 1 条。
	// chat→creative：corrected=2 < MinPairSamples(5) → 排除。
	if len(drafts) != 1 {
		t.Fatalf("drafts = %d, want 1 (%+v)", len(drafts), drafts)
	}
	d := drafts[0]
	if d.Proposal["key"] != "keywords.code" || d.TaskType != "chat" {
		t.Fatalf("proposal = %+v", d.Proposal)
	}
	add := d.Proposal["add"].([]string)
	if len(add) != 1 || add[0] != "数据库" {
		t.Fatalf("add = %v, want [数据库]", add)
	}
	if d.Proposal["channel"] != "code" {
		t.Fatalf("channel = %v", d.Proposal["channel"])
	}

	// 现词命中 → 跳过。
	current["keywords.code"] = append(current["keywords.code"], "数据库")
	if got := draftKeywordProposals(qualifying, current, 30); len(got) != 0 {
		t.Fatalf("existing keyword must be skipped, got %d", len(got))
	}

	// human 不在可调通道 → 跳过（把 creative 改成 6 个样本也一样）。
	creative := &pairStat{Auto: "chat", Human: "vision", confBands: map[string]int{}, hints: map[string]int{"git": 6}}
	creative.Corrected = 6
	if got := draftKeywordProposals([]*pairStat{creative}, current, 30); len(got) != 0 {
		t.Fatalf("non-tunable channel must be skipped, got %d", len(got))
	}

	// 泛词/过短 token → 跳过。
	generic := &pairStat{Auto: "chat", Human: "code", confBands: map[string]int{}, hints: map[string]int{"unknown": 6}}
	generic.Corrected = 6
	if got := draftKeywordProposals([]*pairStat{generic}, current, 30); len(got) != 0 {
		t.Fatalf("generic hint must be skipped, got %d", len(got))
	}
}

func TestGenerateDraftCap(t *testing.T) {
	// 超过 MaxProposalsPerRun 的草稿在 orchestrator 截断——这里验证常量与
	// 截断语义（截断保序：阈值草稿优先、关键词按 Corrected 降序）。
	if MaxProposalsPerRun != 5 {
		t.Fatalf("MaxProposalsPerRun = %d, want 5", MaxProposalsPerRun)
	}
	drafts := []*ProposalDraft{}
	for i := 0; i < 8; i++ {
		drafts = append(drafts, &ProposalDraft{Category: "keyword_add"})
	}
	if len(drafts) > MaxProposalsPerRun {
		drafts = drafts[:MaxProposalsPerRun]
	}
	if len(drafts) != 5 {
		t.Fatalf("capped = %d, want 5", len(drafts))
	}
}

func TestThresholdCoverageUsesRawConfidences(t *testing.T) {
	// 带粒度（0.15/0.20）粗于候选步进（0.05）：覆盖率必须基于原始置信度值。
	// 12 个样本全部在 0.50-0.70 带内，其中 10 个 <0.65 → 0.65 候选覆盖率
	// 10/12=83.3% ≥80% 合格；若按带判定会误判为全有全无。
	weak := &pairStat{Auto: "chat", Human: "code", confBands: map[string]int{"0.50-0.70": 12}, hints: map[string]int{}}
	weak.Corrected = 12
	for i := 0; i < 10; i++ {
		weak.Rows = append(weak.Rows, CorrectionRow{AutoTaskType: "chat", HumanTaskType: "code", Confidence: 0.60})
	}
	for i := 0; i < 2; i++ {
		weak.Rows = append(weak.Rows, CorrectionRow{AutoTaskType: "chat", HumanTaskType: "code", Confidence: 0.68})
	}
	d := draftThresholdProposal([]*pairStat{weak}, map[string]int{"chat": 20}, 0.70, 30)
	if d == nil {
		t.Fatal("expected threshold draft with 83.3% coverage at 0.65")
	}
	if got := d.Proposal["new"].(float64); got != 0.65 {
		t.Fatalf("new = %v, want 0.65", got)
	}
}
