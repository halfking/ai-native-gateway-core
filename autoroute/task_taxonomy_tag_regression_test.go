package autoroute

import (
	"context"
	"testing"
	"time"
)

// R77 审计 D11-L5：族谱/版本分类标签被误当作能力词表在场，导致 48h 热度坍缩。
//
// 线上实测（249 例 E2E，HEAD 2a4cd7a4a 旁挂实例）：坍缩 13/249 全部且仅是
// task_type=code_audit（13/13），其余 10 个 task_type 全部 3 候选。code_audit
// 的唯一候选是 minimax-m3 / credential 21，composite=50 / price=50 /
// quality=0 / reliability=0 / tier 为空——正是 recommend_v2.go 的 48h
// popularity fallback 返回的单元素切片形态。
//
// 根因不是「库没有 code 能力词表」这么简单，而是判定口径串了：
// requiredTagsForTask(code_audit) = [code, review, security] 写的是**能力**
// 词表；TaskVocabularyRepresented 用子串匹配去问「池里有没有这个词」，于是
// 线上真实存在的这些**分类标签**全部命中：
//
//	family:codegemma      family:codex        family:starcoder2
//	version:codestral-latest                version:gpt-4o-audio-preview
//
// 模型族名叫 "codex" / 版本名叫 "codestral" 并不能证明库里存在 code/review/
// security 的**能力分类法**。于是 represented 被判 true，中性化被跳过
// （recommend_v2.go:144），打分留在真实值上；而 top 候选 minimax-m3 不带任何
// code 标签 → MatchScore 0 < 30 → recommend_v2.go:339 的热度坍缩守卫命中 →
// 候选多样性 3 → 1。这正是 §2.2 记录的「坍缩放大限流」的复现条件。
//
// 关键在于这是**代码口径缺陷**，不是此前登记的「~870 模型待标注」数据活：
// 不管给多少模型打 cap 标签，只要库里存在一个叫 codex 的族，review/security
// 这两个 required 词就永远无法被独立验证是否为真词表，判定就会持续被族名
// 挟持。

// TestTaskVocabularyRepresented_IgnoresTaxonomyTags 钉住判定口径：分类标签
// （family:/version:/modality:）不得让能力词表被判为「在场」。
func TestTaskVocabularyRepresented_IgnoresTaxonomyTags(t *testing.T) {
	// 线上真实标签：全部是族谱/版本分类标签，没有任何 cap:* 能力标签。
	taxonomyOnly := []Candidate{
		{CanonicalID: 1, CanonicalName: "codex", Tags: []string{"family:codex", "modality:text"}},
		{CanonicalID: 2, CanonicalName: "codegemma", Tags: []string{"family:codegemma", "modality:text"}},
		{CanonicalID: 3, CanonicalName: "codestral", Tags: []string{"version:codestral-latest", "modality:text"}},
		{CanonicalID: 4, CanonicalName: "plain", Tags: []string{"cap:long-context", "modality:text"}},
	}

	for _, task := range []TaskType{TaskCode, TaskCodeAudit} {
		if TaskVocabularyRepresented(task, taxonomyOnly) {
			t.Errorf("TaskVocabularyRepresented(%s, taxonomy-only pool) = true; "+
				"family:/version: 分类标签不是能力词表，族名叫 codex 不能证明库里有 code 能力分类法",
				task)
		}
	}
}

// TestTaskVocabularyRepresented_StillHonoursCapabilityTags 是上面那条的反向
// 护栏：真正的 cap:* 能力标签必须仍然被判为在场，否则会把「词表缺失中性化」
// 与「词表在场但 winner 低于 30 仍须坍缩」两条既有契约一起打死。
func TestTaskVocabularyRepresented_StillHonoursCapabilityTags(t *testing.T) {
	cases := []struct {
		task TaskType
		tags []string
		want bool
	}{
		{TaskReasoning, []string{"cap:reasoning", "modality:text"}, true},
		{TaskVision, []string{"cap:vision", "modality:multimodal"}, true},
		{TaskAgent, []string{"cap:tool-use", "cap:function-call"}, true},
		{TaskLongContext, []string{"cap:long-context"}, true},
		{TaskCode, []string{"cap:code", "modality:text"}, true},
		{TaskCodeAudit, []string{"cap:code-review", "modality:text"}, true},
		{TaskCode, []string{"family:codex"}, false},
		{TaskCodeAudit, []string{"family:codex", "version:codestral-latest"}, false},
	}
	for _, tc := range cases {
		pool := []Candidate{{CanonicalID: 1, Tags: tc.tags}}
		if got := TaskVocabularyRepresented(tc.task, pool); got != tc.want {
			t.Errorf("TaskVocabularyRepresented(%s, %v) = %v, want %v",
				tc.task, tc.tags, got, tc.want)
		}
	}
}

// TestRecommendV2_CodeAuditTaxonomyOnlyPoolDoesNotCollapse 是端到端钉桩：池里
// 只有族谱标签声称 code 能力时，code_audit 不得坍缩成 48h 热度单模型。
// 修复前该用例红（candidates_top3 只有 1 项、Composite=50、MatchScore 低），
// 修复后保持 >=2 候选，让首选被限流时仍有退路。
func TestRecommendV2_CodeAuditTaxonomyOnlyPoolDoesNotCollapse(t *testing.T) {
	now := time.Now()
	idx := &Index{
		entries: []Candidate{
			{CanonicalID: 1, CanonicalName: "codex", CredentialID: 11, Tier: "primary", PopularityScore: 99,
				Tags: []string{"family:codex", "modality:text"}, SuccessRate: 0.95, P95LatencyMs: 900,
				ProviderCategory: "official", UnitPriceInPer1M: 1, UnitPriceOutPer1M: 2, ReleasedAt: &now},
			{CanonicalID: 2, CanonicalName: "codegemma", CredentialID: 12, Tier: "primary", PopularityScore: 95,
				Tags: []string{"family:codegemma", "modality:text"}, SuccessRate: 0.94, P95LatencyMs: 950,
				ProviderCategory: "official", UnitPriceInPer1M: 1, UnitPriceOutPer1M: 2, ReleasedAt: &now},
			{CanonicalID: 3, CanonicalName: "generalist", CredentialID: 13, Tier: "primary", PopularityScore: 90,
				Tags: []string{"cap:long-context", "modality:text"}, SuccessRate: 0.93, P95LatencyMs: 1000,
				ProviderCategory: "official", UnitPriceInPer1M: 1, UnitPriceOutPer1M: 2, ReleasedAt: &now},
			{CanonicalID: 4, CanonicalName: "plain", CredentialID: 14, Tier: "primary", PopularityScore: 85,
				Tags: []string{"modality:text"}, SuccessRate: 0.92, P95LatencyMs: 1050,
				ProviderCategory: "official", UnitPriceInPer1M: 1, UnitPriceOutPer1M: 2, ReleasedAt: &now},
		},
		lastRefresh: now,
	}

	got := idx.RecommendV2(context.Background(), TaskCodeAudit,
		ClassificationSignals{EstimatedTokens: 500}, ProfileSmart, "", 3)

	if len(got) == 0 {
		t.Fatal("code_audit returned no candidates")
	}
	if isFallbackWinner(got) {
		t.Fatalf("code_audit collapsed to the 48h popularity fallback (composite=50, unscored): %+v",
			got[0].Candidate.CanonicalName)
	}
	if len(got) < 2 {
		t.Fatalf("code_audit candidate pool collapsed to %d; a rate-limited winner needs a failover ladder, got %+v",
			len(got), got[0].Candidate.CanonicalName)
	}
	// 词表缺失时 MatchScore 必须是中性 0.5×100，不能留在 0——0 读起来像
	// 「这个模型不适合该任务」的能力判决，而不是「这套分类法没有这个词」。
	for _, sc := range got {
		if sc.Breakdown.MatchScore < 30 {
			t.Errorf("candidate %q match score %v reads as a capability verdict; "+
				"absent vocabulary must be neutralised (R73 D11#1), not left below the 30 collapse boundary",
				sc.Candidate.CanonicalName, sc.Breakdown.MatchScore)
		}
	}
}
