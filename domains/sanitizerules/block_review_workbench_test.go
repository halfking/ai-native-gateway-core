package sanitizerules

// 拦截审核工作台的判据（2026-10-29 输出合规误报轮）。
//
// ## 这道门钉什么
//
// ① **标注**与放行状态**正交**：标注不改变 approve/reject 的语义，但会被记下来
//    （「这次拦得对不对」是规则优化的唯一输入）。
// ② **自动优化**：同租户同规则的误报标注累计到阈值 ⇒ 自动写入该租户该规则的
//    7 天放行（与人工批准**同键同语义** ⇒ 撤销/过期/可见性同一条路）。
// ③ **证据有界**：摘录不得超过 4 KiB（管理面不该变成载荷仓库）。
// ④ **读数的分母**：误报率的分母是**有判断的记录数**，不是总量。

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// reviewSeq 给每条测试审核单一个唯一的请求 ID。
var reviewSeqCounter atomic.Int64

func reviewSeq() int64 { return reviewSeqCounter.Add(1) }

func resetWorkbenchState(t *testing.T) {
	t.Helper()
	ResetBlockReviews()
	t.Cleanup(ResetBlockReviews)
	prev := EgressReleases()
	SetEgressReleases(nil)
	t.Cleanup(func() { SetEgressReleases(prev) })
}

// addLabeledReview 登记一条审核单并给它打上标注（内存路径）。
//
// ★ 请求 ID 必须**逐条不同** —— 登记侧的去重键是
//   (tenant, scope, rule, digest, request_id)，请求 ID 相同 ⇒ 复用同一单
//   ⇒ 误报计数永远停在 1（这条用例第一版就踩了这个坑）。
func addLabeledReview(t *testing.T, tenant, rule, label string) BlockReview {
	t.Helper()
	entry := AddBlockReview(BlockReviewInput{
		TenantID: tenant, ClientType: "zcode", Scope: "egress",
		Rule: rule, Reason: "reason", RequestID: fmt.Sprintf("req-%s-%s-%d", rule, label, reviewSeq()),
	})
	if entry.ID == 0 {
		t.Fatal("登记审核单失败")
	}
	if label == "" {
		return entry
	}
	labeled, err := LabelBlockReview(context.Background(), entry.ID, label, "标注", "tester")
	if err != nil {
		t.Fatalf("标注失败: %v", err)
	}
	return labeled
}

// TestBlockReviewLabelIsIndependentOfDecision 钉住「标注与放行状态正交」。
func TestBlockReviewLabelIsIndependentOfDecision(t *testing.T) {
	resetWorkbenchState(t)
	entry := addLabeledReview(t, "tenant-x", "builtin.egress_sink", BlockReviewLabelFalsePositive)
	if entry.Label != BlockReviewLabelFalsePositive {
		t.Fatalf("标注未落库: %+v", entry)
	}
	if entry.Status != BlockReviewPending {
		t.Fatalf("标注不应改变放行状态: %+v", entry)
	}
	if entry.LabeledBy != "tester" || entry.LabeledAt.IsZero() {
		t.Fatalf("标注人/时间缺失: %+v", entry)
	}
}

// TestBlockReviewLabelRejectsUnknownLabel 钉住标注闭集。
func TestBlockReviewLabelRejectsUnknownLabel(t *testing.T) {
	resetWorkbenchState(t)
	entry := AddBlockReview(BlockReviewInput{
		TenantID: "tenant-y", ClientType: "zcode", Scope: "egress",
		Rule: "builtin.egress_sink", RequestID: "req-closed-set",
	})
	if _, err := LabelBlockReview(context.Background(), entry.ID, "随便写", "", "tester"); err == nil {
		t.Fatal("未知标注必须被拒绝（否则读数里会出现无法归类的桶）")
	}
}

// TestBlockReviewAutoRelaxAfterThreshold 钉住自动优化：
// 同租户同规则的误报标注累计到阈值 ⇒ 自动写入 7 天放行（source=auto_label）。
//
// ★ 判据包含「阈值前不放行」——否则这条用例测不出「累计」这个语义。
func TestBlockReviewAutoRelaxAfterThreshold(t *testing.T) {
	resetWorkbenchState(t)
	threshold := AutoRelaxThreshold()
	if threshold <= 0 {
		t.Skip("自动优化阈值被设为 0（关闭），本用例不适用")
	}
	for i := 0; i < threshold-1; i++ {
		addLabeledReview(t, "tenant-auto", "builtin.egress_sink", BlockReviewLabelFalsePositive)
	}
	if EgressReleaseAllowed("tenant-auto", "egress", "builtin.egress_sink", "") {
		t.Fatalf("未达阈值不得自动放行（阈值 %d）", threshold)
	}
	addLabeledReview(t, "tenant-auto", "builtin.egress_sink", BlockReviewLabelFalsePositive)
	if !EgressReleaseAllowed("tenant-auto", "egress", "builtin.egress_sink", "") {
		t.Fatal("达到阈值后必须自动放行（规则级、租户级）")
	}
	releases := EgressReleases()
	found := false
	for _, rel := range releases {
		if rel.TenantID == "tenant-auto" && rel.Rule == "builtin.egress_sink" {
			found = true
			if rel.Source != ReleaseSourceAutoLabel {
				t.Errorf("自动放行的来源应为 auto_label，实际 %q", rel.Source)
			}
			if ttl := time.Until(rel.ExpiresAt); ttl < 6*24*time.Hour || ttl > 8*24*time.Hour {
				t.Errorf("自动放行的 TTL 应为 7 天，实际 %v", ttl)
			}
		}
	}
	if !found {
		t.Fatalf("未找到自动放行记录: %+v", releases)
	}
}

// TestBlockReviewAutoRelaxIsTenantScoped 钉住自动放行的**收窄面**：
// 别的租户不受影响（规则是全局的，但误报是租户事实）。
func TestBlockReviewAutoRelaxIsTenantScoped(t *testing.T) {
	resetWorkbenchState(t)
	threshold := AutoRelaxThreshold()
	if threshold <= 0 {
		t.Skip("自动优化阈值被设为 0（关闭），本用例不适用")
	}
	for i := 0; i < threshold; i++ {
		addLabeledReview(t, "tenant-noisy", "builtin.egress_sink", BlockReviewLabelFalsePositive)
	}
	if !EgressReleaseAllowed("tenant-noisy", "egress", "builtin.egress_sink", "") {
		t.Fatal("触发租户应当被自动放行")
	}
	if EgressReleaseAllowed("tenant-quiet", "egress", "builtin.egress_sink", "") {
		t.Fatal("自动放行不得波及其他租户")
	}
}

// TestBlockReviewAutoRelaxSkipsSyntheticRules 钉住「合成规则不参与自动放行」。
//
// builtin.guard.capacity_exceeded 是**基础设施性**规则（缓冲越界），不是内容
// 规则 ⇒ 放行它对业务没有意义（容量问题靠上限/开关解决）。
func TestBlockReviewAutoRelaxSkipsSyntheticRules(t *testing.T) {
	resetWorkbenchState(t)
	threshold := AutoRelaxThreshold()
	if threshold <= 0 {
		t.Skip("自动优化阈值被设为 0（关闭），本用例不适用")
	}
	for i := 0; i < threshold; i++ {
		addLabeledReview(t, "tenant-cap", "builtin.guard.capacity_exceeded", BlockReviewLabelFalsePositive)
	}
	if EgressReleaseAllowed("tenant-cap", "egress", "builtin.guard.capacity_exceeded", "") {
		t.Fatal("合成规则不得参与自动放行")
	}
}

// TestBlockReviewEvidenceIsBounded 钉住证据的有界性。
func TestBlockReviewEvidenceIsBounded(t *testing.T) {
	resetWorkbenchState(t)
	long := strings.Repeat("x", BlockReviewEvidenceMaxBytes*3)
	entry := AddBlockReview(BlockReviewInput{
		TenantID: "tenant-ev", ClientType: "zcode", Scope: "egress",
		Rule: "builtin.egress_sink", RequestID: "req-ev", Evidence: long,
		EvidenceKind: BlockReviewEvidenceToolArgument,
	})
	if len(entry.Evidence) > BlockReviewEvidenceMaxBytes {
		t.Fatalf("证据摘录超长: %d > %d", len(entry.Evidence), BlockReviewEvidenceMaxBytes)
	}
	if !entry.EvidenceTruncated {
		t.Fatal("超长证据必须标记为已截断（审核员要知道自己看的是片段）")
	}
	if entry.EvidenceKind != BlockReviewEvidenceToolArgument {
		t.Fatalf("证据种类缺失: %+v", entry)
	}
}

// TestBlockReviewInsightsCountsLabels 钉住读数口径：误报率的分母是**有判断的
// 记录数**，不是总量。
func TestBlockReviewInsightsCountsLabels(t *testing.T) {
	resetWorkbenchState(t)
	addLabeledReview(t, "tenant-ins", "builtin.egress_sink", BlockReviewLabelFalsePositive)
	addLabeledReview(t, "tenant-ins", "builtin.egress_sink", BlockReviewLabelTruePositive)
	// 一条**没有标注**的记录（没人看过的）——它不得把误报率稀释成假绿。
	AddBlockReview(BlockReviewInput{
		TenantID: "tenant-ins", ClientType: "zcode", Scope: "egress",
		Rule: "builtin.egress_sink", RequestID: "req-unlabeled",
	})
	insights, err := BlockReviewInsights("tenant-ins", 10)
	if err != nil {
		t.Fatalf("读数失败: %v", err)
	}
	if len(insights) != 1 {
		t.Fatalf("读数条数 = %d，期望 1", len(insights))
	}
	insight := insights[0]
	if insight.Total != 3 || insight.Labeled != 2 {
		t.Fatalf("计数不对: %+v", insight)
	}
	if insight.FalsePositive != 1 || insight.TruePositive != 1 {
		t.Fatalf("标注分桶不对: %+v", insight)
	}
	if insight.FalsePositiveR != 0.5 {
		t.Fatalf("误报率的分母必须是【有判断的记录数】（1/2），实际 %v", insight.FalsePositiveR)
	}
}

// TestBlockReviewInsightsIncludesUnattributed 钉住读数口径：**没有规则归因**
// 的拦截记录**必须进读数**（否则它们在读数上「不存在」）。
//
// ★ 生产实测（211，2026-10-29）：22 条审核单的 rule_id 全为空（拦截点没说话）
// ⇒ 旧口径（WHERE rule_id <> ''）把它们全藏起来 ⇒ 读数长成「没有数据」，
// 而真相是「22 条没有归因」。这正是误报轮最该被看见的信号。
func TestBlockReviewInsightsIncludesUnattributed(t *testing.T) {
	resetWorkbenchState(t)
	// 一条有规则的 + 一条没有归因的（Rule 为空 ⇒ 拦截点没说话）。
	AddBlockReview(BlockReviewInput{
		TenantID: "tenant-unattr", ClientType: "codex", Scope: "egress",
		Rule: "builtin.egress_sink", RequestID: "req-with-rule",
	})
	AddBlockReview(BlockReviewInput{
		TenantID: "tenant-unattr", ClientType: "codex", Scope: "egress",
		Rule: "", RequestID: "req-no-rule",
	})
	insights, err := BlockReviewInsights("tenant-unattr", 10)
	if err != nil {
		t.Fatalf("读数失败: %v", err)
	}
	if len(insights) != 2 {
		t.Fatalf("读数条数 = %d，期望 2（有规则 + 无归因各一条），实际 %+v", len(insights), insights)
	}
	var unattributed *BlockReviewRuleInsight
	for i := range insights {
		if insights[i].Rule == unattributedRuleBucket {
			unattributed = &insights[i]
		}
	}
	if unattributed == nil {
		t.Fatalf("没有归因的那条必须出现在读数里（桶名 %q），实际 %+v", unattributedRuleBucket, insights)
	}
	if unattributed.Total != 1 {
		t.Fatalf("无归因桶的拦截次数 = %d，期望 1", unattributed.Total)
	}
}

// TestBlockReviewInsightsExcludeDegraded 钉住读数口径（迁移 926）：降级放行单
// 是「守卫让步」的遥测，不是规则反馈 ⇒ 不进按规则聚合的读数
//（211 实测 24h 内 226/226 条全是降级单 ⇒ 不排除的话输入面只剩噪音）。
func TestBlockReviewInsightsExcludeDegraded(t *testing.T) {
	resetWorkbenchState(t)
	AddBlockReview(BlockReviewInput{
		TenantID: "tenant-deg", ClientType: "zcode", Scope: "egress",
		Rule: "builtin.egress_sink", RequestID: "req-real-block",
	})
	AddBlockReview(BlockReviewInput{
		TenantID: "tenant-deg", ClientType: "zcode", Scope: "egress",
		Rule: "builtin.guard.capacity_exceeded", RequestID: "req-degraded-1",
		Degraded: true,
	})
	insights, err := BlockReviewInsights("tenant-deg", 10)
	if err != nil {
		t.Fatalf("读数失败: %v", err)
	}
	if len(insights) != 1 || insights[0].Rule != "builtin.egress_sink" {
		t.Fatalf("降级单必须被读数排除，实际: %+v", insights)
	}
}

// TestCountPendingBlockDecisionsExcludesDegraded 钉住待审数口径（迁移 926）：
// 「待人工决断」= pending 且**非降级**。降级单对应的响应已成功送达客户端，
// 没有任何决断可做 —— 把它计进待审数，徽标整天闪假警报
//（211 实测：24h 内 226/226 条全是降级单）。
func TestCountPendingBlockDecisionsExcludesDegraded(t *testing.T) {
	resetWorkbenchState(t)
	AddBlockReview(BlockReviewInput{
		TenantID: "tenant-cnt", ClientType: "zcode", Scope: "egress",
		Rule: "builtin.egress_sink", RequestID: "req-real",
	})
	AddBlockReview(BlockReviewInput{
		TenantID: "tenant-cnt", ClientType: "zcode", Scope: "egress",
		Rule: "builtin.guard.capacity_exceeded", RequestID: "req-deg",
		Degraded: true,
	})
	if got := CountBlockReviews(BlockReviewPending, "tenant-cnt"); got != 2 {
		t.Fatalf("CountBlockReviews 的旧口径应仍数到 2（全部 pending），实际 %d", got)
	}
	if got := CountPendingBlockDecisions("tenant-cnt"); got != 1 {
		t.Fatalf("待决断数必须排除降级单（=1），实际 %d", got)
	}
}

// TestAddBlockReviewCarriesDegradedFlag 钉住登记链：Degraded 从入参进记录
//（DB 路径与内存回退路径都必须携带 —— 登记方只写一次，两边不许分叉）。
func TestAddBlockReviewCarriesDegradedFlag(t *testing.T) {
	resetWorkbenchState(t)
	blocked := AddBlockReview(BlockReviewInput{
		TenantID: "tenant-flag", ClientType: "zcode", Scope: "egress",
		Rule: "builtin.egress_sink", RequestID: "req-a",
	})
	degraded := AddBlockReview(BlockReviewInput{
		TenantID: "tenant-flag", ClientType: "zcode", Scope: "egress",
		Rule: "builtin.guard.capacity_exceeded", RequestID: "req-b",
		Degraded: true,
	})
	if blocked.Degraded {
		t.Fatal("硬阻断路径不得带降级标记")
	}
	if !degraded.Degraded {
		t.Fatal("降级路径登记的记录必须带 Degraded=true")
	}
}
