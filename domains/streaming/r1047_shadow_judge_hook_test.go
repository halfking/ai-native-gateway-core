package streaming

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
	"github.com/kaixuan/llm-gateway-go/internal/sanitizejudge"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// r1047 门：影子判官钩子——审核单登记后异步判读、结论回写 match_detail；
// 不触发面（容量合成规则 / 空摘录 / 占位判官 / settings 开关关闭）；
// 登记路径不因判官任何形态失败而受阻。

type recordingJudge struct {
	calls chan sanitizejudge.Request
}

func (r *recordingJudge) ShadowEnabled() bool { return true }
func (r *recordingJudge) Judge(ctx context.Context, req sanitizejudge.Request) (sanitizejudge.Verdict, error) {
	r.calls <- req
	return sanitizejudge.Verdict{Verdict: "benign", Confidence: 0.9, Reason: "命令行数字 ID", Model: "test-judge", LatencyMs: 12}, nil
}

// withJudgeShadowSwitch 把 settings.Global 钉在给定开关值上（audio_usage_gate
// 同款 fakeSettingsBackend 形状）。
func withJudgeShadowSwitch(t *testing.T, enabled bool) {
	t.Helper()
	prevGlobal := settings.Global
	t.Cleanup(func() { settings.Global = prevGlobal })
	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, fakeSettingsBackend{store: map[string][]byte{
		"sanitize.judge_shadow_enabled": []byte(map[bool]string{true: "true", false: "false"}[enabled]),
	}})
	for _, spec := range settings.ModuleSpecs() {
		registry.MustRegisterSpec(spec)
	}
	settings.Global = registry
}

func TestR1047ShadowJudgeFiresAndWritesBack(t *testing.T) {
	withJudgeShadowSwitch(t, true)
	j := &recordingJudge{calls: make(chan sanitizejudge.Request, 4)}
	SetShadowJudge(j)
	t.Cleanup(func() { SetShadowJudge(sanitizejudge.Noop()) })

	reviewID := registerPolicyBlockReview(PolicyBlockReviewInput{
		TenantID: "t-r1047", Rule: "builtin.guard.rewrite_refused",
		Reason: "test", Field: "anthropic.block.1.thinking",
		Evidence: "the variations seed 1335099688260238922 is just an id",
	})
	if reviewID == "" {
		t.Fatal("review not registered")
	}

	select {
	case req := <-j.calls:
		if req.RuleID != "builtin.guard.rewrite_refused" ||
			!strings.Contains(req.Excerpt, "1335099688260238922") {
			t.Fatalf("judge request lost context: %+v", req)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shadow judge never invoked")
	}

	// 回写落在进程内审核单的 match_detail（异步——轮询有界等待）。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range sanitizerules.ListBlockReviews("", "t-r1047", 100) {
			if strings.Contains(e.MatchDetail, "判官（影子）：verdict=benign") {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("judge verdict not written back to match_detail")
}

// TestR1047ShadowJudgeSettingsSwitchOff：开关关闭（生产默认）⇒ 判官装配
// 在场也不触发——「默认不激活」的门。
func TestR1047ShadowJudgeSettingsSwitchOff(t *testing.T) {
	withJudgeShadowSwitch(t, false)
	j := &recordingJudge{calls: make(chan sanitizejudge.Request, 4)}
	SetShadowJudge(j)
	t.Cleanup(func() { SetShadowJudge(sanitizejudge.Noop()) })

	registerPolicyBlockReview(PolicyBlockReviewInput{
		TenantID: "t-r1047-off", Rule: "pii.email",
		Reason: "x", Evidence: "a@b.com",
	})
	select {
	case <-j.calls:
		t.Fatal("switch off must not trigger judge")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestR1047ShadowJudgeSkips(t *testing.T) {
	withJudgeShadowSwitch(t, true)
	// 容量合成规则：成因是缓冲体量不是内容 ⇒ 不判读；空摘录同样不判读。
	j := &recordingJudge{calls: make(chan sanitizejudge.Request, 4)}
	SetShadowJudge(j)
	t.Cleanup(func() { SetShadowJudge(sanitizejudge.Noop()) })

	registerPolicyBlockReview(PolicyBlockReviewInput{
		TenantID: "t-r1047-skip", Rule: "builtin.guard.capacity_exceeded",
		Reason: "cap", Evidence: "whatever",
	})
	registerPolicyBlockReview(PolicyBlockReviewInput{
		TenantID: "t-r1047-skip", Rule: "pii.email", Reason: "no evidence",
	})
	select {
	case <-j.calls:
		t.Fatal("capacity rule or empty evidence should not trigger judge")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestR1047ShadowJudgeNoopNeverFires(t *testing.T) {
	withJudgeShadowSwitch(t, true)
	SetShadowJudge(sanitizejudge.Noop())
	reviewID := registerPolicyBlockReview(PolicyBlockReviewInput{
		TenantID: "t-r1047-noop", Rule: "pii.email",
		Reason: "x", Evidence: "a@b.com",
	})
	if reviewID == "" {
		t.Fatal("review registration must not depend on judge")
	}
	// 到这里没 panic / 没阻塞即过：占位判官零副作用。
}
