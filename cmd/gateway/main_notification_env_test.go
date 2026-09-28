package main

// 2026-09-29 (12h 审计二十轮 P1): WeChat 渠道 env 双路径回归。
// 修复前 initApprovalNotifier 只读无前缀 WECHAT_CORP_ID/SECRET，
// config/config.go:262 声明并文档化的 LLM_GATEWAY_WECHAT_* 前缀形态
// 静默失效。回归钉死：前缀优先、旧形态回退、前缀缺 corp_id 时整体不启用。

import "testing"

func TestWeChatCredsFromEnv_PrefersPrefixedDocumentedForm(t *testing.T) {
	t.Setenv("LLM_GATEWAY_WECHAT_CORP_ID", "prefix-corp")
	t.Setenv("LLM_GATEWAY_WECHAT_CORP_SECRET", "prefix-secret")
	t.Setenv("WECHAT_CORP_ID", "legacy-corp")
	t.Setenv("WECHAT_CORP_SECRET", "legacy-secret")

	corpID, secret := wechatCredsFromEnv()
	if corpID != "prefix-corp" {
		t.Fatalf("corp id 应优先文档面前缀形态，得到 %q", corpID)
	}
	if secret != "prefix-secret" {
		t.Fatalf("corp secret 应优先文档面前缀形态，得到 %q", secret)
	}
}

func TestWeChatCredsFromEnv_FallsBackToLegacyUnprefixedForm(t *testing.T) {
	t.Setenv("LLM_GATEWAY_WECHAT_CORP_ID", "")
	t.Setenv("LLM_GATEWAY_WECHAT_CORP_SECRET", "")
	t.Setenv("WECHAT_CORP_ID", "legacy-corp")
	t.Setenv("WECHAT_CORP_SECRET", "legacy-secret")

	corpID, secret := wechatCredsFromEnv()
	if corpID != "legacy-corp" {
		t.Fatalf("前缀缺失时应回退旧形态，得到 %q", corpID)
	}
	if secret != "legacy-secret" {
		t.Fatalf("前缀缺失时 secret 应回退旧形态，得到 %q", secret)
	}
}

func TestWeChatCredsFromEnv_PrefixedIDAloneDoesNotEnableLegacyID(t *testing.T) {
	// 混配边界：只有前缀 corp_id 时，corp_id 用前缀值；secret 各自独立回退。
	// 渠道启用判定只看 corp_id 非空，混配不产生"静默换回旧 corp"的意外。
	t.Setenv("LLM_GATEWAY_WECHAT_CORP_ID", "prefix-corp")
	t.Setenv("LLM_GATEWAY_WECHAT_CORP_SECRET", "")
	t.Setenv("WECHAT_CORP_ID", "legacy-corp")
	t.Setenv("WECHAT_CORP_SECRET", "legacy-secret")

	corpID, secret := wechatCredsFromEnv()
	if corpID != "prefix-corp" {
		t.Fatalf("corp id 应取前缀形态，得到 %q", corpID)
	}
	if secret != "legacy-secret" {
		t.Fatalf("前缀 secret 缺失时应回退旧形态，得到 %q", secret)
	}
}
