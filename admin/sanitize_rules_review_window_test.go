package admin

// 审核单**归因读面**的逐单窗口判据（2026-10-31 拦截归因轮）。
//
// ## 这组用例钉的是什么
//
// 211 生产实测：★ 一批 **24 条**待审拦截单横跨 **40 小时**（10-29 08:45 →
// 10-31 01:03），★ 而该时间跨度内 egress 规则事件有 **56 行** ⇒ ★★ 旧读法
// （整批一个窗口 + `ORDER BY last_seen DESC LIMIT 50`）★★ ⇒ ★★ 50 条预算被
// ★★ **最近**的事件吃光 ⇒ ★★ 早期的那批审核单**一条候选都没有**，且「撞上限」
// ★★ 被记成**整批**截断 ⇒ ★★ 归因档位一律降级成「unknown（请人工判断）」★★
// ⇒ ★★ 读时归因——★ 整个「让历史空白单可判读」的机制——在这批数据上**等于
// ★★ 没生效** ★★。
//
// ⇒ 修法：**每个审核单一个窗口**，一次往返（LATERAL），**逐单预算**。
//
// ## 契约
//
//	① 窗口数 == 审核单数（★ 每单一个窗口，★ 不是整批一个 ★）
//	② 每个窗口是「该单的 created_at ± 15 分钟」
//	③ 候选按窗口 Index 归组（★ 不串单 ★）
//	④ 截断标记是**逐单**的（★ 撞预算只影响那一条 ★）

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

// TestLoadBlockReviewCandidateRulesUsesPerReviewWindow 钉住 ①②③。
func TestLoadBlockReviewCandidateRulesUsesPerReviewWindow(t *testing.T) {
	base := time.Date(2026, 10, 29, 8, 45, 1, 0, time.UTC)
	late := time.Date(2026, 10, 31, 1, 3, 57, 0, time.UTC)
	reviews := []sanitizerules.BlockReview{
		{ID: 1, TenantID: "default", CreatedAt: base},
		{ID: 2, TenantID: "default", CreatedAt: late},
	}

	var gotWindows []sanitizerules.EgressRuleEventWindow
	var gotTenants []string
	h := &Handler{
		blockReviewCandidateReader: func(_ context.Context, tenants []string, windows []sanitizerules.EgressRuleEventWindow, _ int) (map[int][]sanitizerules.EgressRuleEventInWindow, error) {
			gotTenants = tenants
			gotWindows = windows
			out := map[int][]sanitizerules.EgressRuleEventInWindow{}
			for _, w := range windows {
				// ★ 每个窗口回一条候选（★ 用来验证「不串单」★）。
				out[w.Index] = []sanitizerules.EgressRuleEventInWindow{{
					RuleID:   "pii.server_ip",
					Action:   "exempted",
					LastSeen: w.From.Add(time.Minute),
				}}
			}
			return out, nil
		},
	}

	got := h.loadBlockReviewCandidateRules(context.Background(), reviews)
	if len(gotWindows) != len(reviews) {
		t.Fatalf("窗口数 = %d，期望 %d（★ 每单一个窗口，★ 不是整批一个 ★）", len(gotWindows), len(reviews))
	}
	if len(gotTenants) != 1 || gotTenants[0] != "default" {
		t.Fatalf("租户集合 = %v，期望 [default]", gotTenants)
	}
	for _, w := range gotWindows {
		if w.To.Sub(w.From) != 2*blockReviewCandidateWindow {
			t.Fatalf("窗口宽度 = %v，期望 ±%v", w.To.Sub(w.From), blockReviewCandidateWindow)
		}
	}
	// ★ 每个窗口必须落在**它自己那一单**的时刻附近（★ 不许串单 ★）。
	if !gotWindows[0].From.Equal(base.Add(-blockReviewCandidateWindow)) ||
		!gotWindows[0].To.Equal(base.Add(blockReviewCandidateWindow)) {
		t.Fatalf("第一条窗口 = [%v,%v]，期望围绕 %v", gotWindows[0].From, gotWindows[0].To, base)
	}
	if !gotWindows[1].From.Equal(late.Add(-blockReviewCandidateWindow)) ||
		!gotWindows[1].To.Equal(late.Add(blockReviewCandidateWindow)) {
		t.Fatalf("第二条窗口 = [%v,%v]，期望围绕 %v", gotWindows[1].From, gotWindows[1].To, late)
	}
	if len(got) != 2 {
		t.Fatalf("候选集数 = %d，期望 2", len(got))
	}
	for id, set := range got {
		if len(set.Items) != 1 || set.Items[0].RuleID != "pii.server_ip" {
			t.Fatalf("审核单 %d 的候选 = %+v，期望 pii.server_ip", id, set.Items)
		}
		if set.Truncated {
			t.Fatalf("审核单 %d 未撞预算，不该标截断", id)
		}
	}
}

// TestLoadBlockReviewCandidateRulesMarksTruncationPerReview 钉住 ④：
// ★ 截断是**逐单**的 ⇒ ★ 一单撞预算不影响别的单 ★。
func TestLoadBlockReviewCandidateRulesMarksTruncationPerReview(t *testing.T) {
	base := time.Date(2026, 10, 29, 8, 45, 1, 0, time.UTC)
	reviews := []sanitizerules.BlockReview{
		{ID: 1, TenantID: "default", CreatedAt: base},
		{ID: 2, TenantID: "default", CreatedAt: base.Add(48 * time.Hour)},
	}
	h := &Handler{
		blockReviewCandidateReader: func(_ context.Context, _ []string, windows []sanitizerules.EgressRuleEventWindow, limit int) (map[int][]sanitizerules.EgressRuleEventInWindow, error) {
			out := map[int][]sanitizerules.EgressRuleEventInWindow{}
			for _, w := range windows {
				hits := make([]sanitizerules.EgressRuleEventInWindow, 0, limit)
				for i := 0; i < limit; i++ {
					hits = append(hits, sanitizerules.EgressRuleEventInWindow{RuleID: "r"})
				}
				out[w.Index] = hits
			}
			return out, nil
		},
	}
	got := h.loadBlockReviewCandidateRules(context.Background(), reviews)
	for id, set := range got {
		if !set.Truncated {
			t.Fatalf("审核单 %d 撞了逐单预算，必须标截断（★ 不许把「可能不完整」说成「没有内容规则阻断」★）", id)
		}
	}
}

// TestDeriveBlockReviewAttributionGuardTierNamesFailureForms 钉住归因措辞：
// ★ 守卫侧失败**不止**缓冲越界一种 ⇒ ★ 措辞不许只说「缓冲越界」★
// ⇒ ★★ 否则审核员会照着「容量」去调上限，而真因是「这条 lane 不许改写」★★
// ⇒ ★★ 而且守卫侧失败**不等于**误报 ⇒ ★★ 不许说「可直接标误报」★★。
func TestDeriveBlockReviewAttributionGuardTierNamesFailureForms(t *testing.T) {
	kind, text := deriveBlockReviewAttribution(
		sanitizerules.BlockReview{},
		blockReviewContext{ErrorKind: "output_policy_blocked"},
		nil, false)
	if kind != "guard" {
		t.Fatalf("kind = %q，期望 guard", kind)
	}
	for _, want := range []string{"缓冲", "不许安全改写", "不是内容规则命中"} {
		if !strings.Contains(text, want) {
			t.Fatalf("守卫侧归因必须点明失败形态（含 %q）: %q", want, text)
		}
	}
	if strings.Contains(text, "可直接标「误报」") {
		t.Fatalf("守卫侧失败**不等于**误报（★ 命中敏感内容却不能安全改写时，阻断是正确行为 ★）⇒ 不许说「可直接标误报」: %q", text)
	}
}
