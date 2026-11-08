package bg

import (
	"context"
	"os"
	"strings"
	"testing"
)

// 2026-11-09（minimax 套餐恢复感知轮 F-A）测试族。
//
// 事故：cred 41/42 的 default_probe_model=minimax-m3 不在该账号 Token
// Plan 覆盖内，凭据级探针每小时吃 429-2056（「请升级 Token Plan 套餐或
// 购买积分」），凭据级挂起整天 —— 而套餐覆盖模型一直在承接真实流量。
// 修复：配额族失败 ⇒ 对备选绑定模型各试一发（回退梯）。

// TestCredentialProbeModelFallbackTrigger 钉触发集：配额族（402 / 429-配额 /
// 403-余额形态）触发；鉴权失败、5xx、404-模型不触发（404 已有 BindingOnly
// 语义，鉴权换模型无用）。
func TestCredentialProbeModelFallbackTrigger(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{
			name: "minimax 429 token plan cap (2056)",
			msg:  `chat status 429: {"type":"error","error":{"type":"rate_limit_error","message":"已达到 Token Plan 用量上限：请升级 Token Plan 套餐或购买积分补充用量。 (2056)","http_code":"429"}}`,
			want: true,
		},
		{
			name: "minimax 402 insufficient balance",
			msg:  `chat status 402: {"type":"error","error":{"type":"insufficient_balance_error","message":"insufficient balance (1008)","http_code":"402"}}`,
			want: true,
		},
		{
			name: "403 INSUFFICIENT_BALANCE body",
			msg:  `status 403: {"code":"INSUFFICIENT_BALANCE","message":"Insufficient account balance"}`,
			want: true,
		},
		{
			name: "401 auth failure — no fallback",
			msg:  `status 401: {"error":{"message":"invalid api key"}}`,
			want: false,
		},
		{
			name: "502 upstream fault — no fallback",
			msg:  `chat status 502: {"error":{"message":"bad gateway"}}`,
			want: false,
		},
		{
			name: "404 model not served — BindingOnly owns it",
			msg:  `chat status 404: {"error":{"message":"model not found"}}`,
			want: false,
		},
	}
	for _, tc := range cases {
		if got := credentialProbeModelFallbackTrigger(tc.msg); got != tc.want {
			t.Errorf("%s: trigger=%v want %v", tc.name, got, tc.want)
		}
	}
}

// TestCredentialProbeV2_ProbeChatFallbackModels 行为测试：主探针模型配额族
// 失败 ⇒ 依次尝试备选模型；第一个成功 ⇒ true；全部失败 ⇒ false 且不吞主
// 归因（调用方仍返回原 errMsg）。
func TestCredentialProbeV2_ProbeChatFallbackModels(t *testing.T) {
	newSnapshot := func() v2Snapshot {
		return v2Snapshot{ID: 42, DefaultProbeModel: "minimax-m3"}
	}

	t.Run("first fallback succeeds", func(t *testing.T) {
		c := NewCredentialProbeV2(nil, nil)
		tried := make([]string, 0, 4)
		c.chatFallbackModelsFn = func(ctx context.Context, credID int, exclude string, limit int) []string {
			if credID != 42 || exclude != "minimax-m3" || limit != probeFallbackModelLimit {
				t.Errorf("loader args: credID=%d exclude=%q limit=%d", credID, exclude, limit)
			}
			return []string{"MiniMax-M2.5", "MiniMax-M3.1-Flash"}
		}
		ok := c.probeChatFallbackModels(context.Background(), newSnapshot(), func(model string) (bool, string) {
			tried = append(tried, model)
			if model == "MiniMax-M2.5" {
				return true, ""
			}
			return false, "chat status 429: quota"
		}, "chat status 429: token plan cap")
		if !ok {
			t.Fatalf("fallback must succeed when a candidate model succeeds")
		}
		if len(tried) != 1 || tried[0] != "MiniMax-M2.5" {
			t.Fatalf("fallback must stop at first success, tried=%v", tried)
		}
	})

	t.Run("all fallbacks fail — caller keeps primary error", func(t *testing.T) {
		c := NewCredentialProbeV2(nil, nil)
		c.chatFallbackModelsFn = func(ctx context.Context, credID int, exclude string, limit int) []string {
			return []string{"MiniMax-M2", "MiniMax-M2.1", "MiniMax-M2.7"}
		}
		ok := c.probeChatFallbackModels(context.Background(), newSnapshot(), func(model string) (bool, string) {
			return false, "chat status 402: insufficient balance"
		}, "chat status 429: token plan cap")
		if ok {
			t.Fatalf("fallback must report failure when every candidate fails")
		}
	})

	t.Run("no candidates — no-op", func(t *testing.T) {
		c := NewCredentialProbeV2(nil, nil)
		c.chatFallbackModelsFn = func(ctx context.Context, credID int, exclude string, limit int) []string {
			return nil
		}
		ok := c.probeChatFallbackModels(context.Background(), newSnapshot(), func(model string) (bool, string) {
			t.Fatalf("runChatModel must not be called without candidates")
			return false, ""
		}, "chat status 402: insufficient balance")
		if ok {
			t.Fatalf("no candidates must be a no-op failure")
		}
	})

	t.Run("nil db without seam — safe no-op", func(t *testing.T) {
		c := NewCredentialProbeV2(nil, nil)
		ok := c.probeChatFallbackModels(context.Background(), newSnapshot(), func(model string) (bool, string) {
			t.Fatalf("runChatModel must not be called without db or seam")
			return false, ""
		}, "chat status 402: insufficient balance")
		if ok {
			t.Fatalf("nil db without seam must be a safe no-op")
		}
	})
}

// TestCredentialProbeV2_ProbeCredentialWithCapability_QuotaFallbackLadder
// 结构钉：probeCredentialWithCapability 的 fail-fast 分支必须挂回退梯，
// 且回退失败必须保留主失败归因原文（不重包 "chat unreachable (after N
// attempts)" —— 分类器与 isPerModelChatFailure 的 chat 级前缀判据依赖它）。
func TestCredentialProbeV2_ProbeCredentialWithCapability_QuotaFallbackLadder(t *testing.T) {
	src, err := os.ReadFile("credential_probe_v2.go")
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	body := string(src)
	fidx := strings.Index(body, "func (c *CredentialProbeV2) probeCredentialWithCapability")
	if fidx < 0 {
		t.Fatalf("probeCredentialWithCapability not found")
	}
	fend := strings.Index(body[fidx+1:], "\nfunc ")
	fn := body[fidx : fidx+1+fend]

	if !strings.Contains(fn, "if credentialProbeModelFallbackTrigger(errMsg) {") {
		t.Fatalf("fail-fast branch must invoke the fallback ladder")
	}
	if !strings.Contains(fn, "return false, errMsg") {
		t.Fatalf("non-retryable failure must return the primary errMsg verbatim (classification depends on it)")
	}
	if strings.Count(fn, "runChatModel(s.DefaultProbeModel)") != 1 {
		t.Fatalf("primary probe must go through runChatModel(s.DefaultProbeModel) exactly once")
	}
	// 回退梯挂在 shouldRetryChatErrMsg 的 fail-fast 之内（429-配额 / 402 都
	// 是 fail-fast 形态 —— 若挂在重试循环之后，触发集永远够不着）。
	failFastIdx := strings.Index(fn, "if !shouldRetryChatErrMsg(errMsg) {")
	fbIdx := strings.Index(fn, "credentialProbeModelFallbackTrigger(errMsg)")
	if failFastIdx < 0 || fbIdx < 0 || fbIdx < failFastIdx {
		t.Fatalf("fallback ladder must live inside the fail-fast branch")
	}
}
