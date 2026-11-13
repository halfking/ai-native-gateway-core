// Package bg — r1054_probe_quota_group_test.go
//
// 2026-11-14（suyun 轮）探针侧钉测：两个 new_api 家族 403 形态在
// classifyProbeFailure 上的新裁决。
//
// ★ 生产取证（211）：
//
//	· cred 71（claude-nomix）：探针模型 403「无权访问 claude不降智稳定版
//	  分组」连败把**整凭据** auth_failed 按探针退避梯推到 24h
//	  （availability_recover_at=2026-11-15 00:21:34），而同凭据 opus-5-5
//	  探针 95 连胜完全健康 ⇒ resolve 页对健康模型显示「鉴权失败」一整天。
//	  ⇒ 修复后：BindingOnly —— 只翻探针模型那一对的绑定，凭据保持 ready。
//	· cred 39（claude）：探针/真实请求 403「预扣费额度失败
//	  insufficient_user_quota」被判 auth_failed（15 分钟自愈循环）。
//	  ⇒ 修复后：isProbeQuotaFailure 命中（errorsx 判 quota_balance）⇒
//	  suspended + balance_exhausted，BalanceQuotaProbe 探活恢复。
package bg

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// TestR1054ProbeGroupAccess403IsBindingOnly：分组权限 403 ⇒ 只翻绑定。
func TestR1054ProbeGroupAccess403IsBindingOnly(t *testing.T) {
	msg := `chat status 403: {"error":{"code":"","message":"无权访问 claude不降智稳定版 分组 (request id: x)","type":"new_api_error"}}`
	pr := classifyProbeFailure(msg)
	if !pr.BindingOnly {
		t.Fatalf("分组权限 403 必须 BindingOnly（此前整凭据 auth_failed 退避 24h）：%+v", pr)
	}
	if pr.AvailabilityState != "ready" {
		t.Fatalf("分组权限 403 不得改凭据级 availability_state（兄弟模型探针连胜），got=%q", pr.AvailabilityState)
	}
	if pr.HealthStatus != "warning" {
		t.Fatalf("HealthStatus = %q, want warning", pr.HealthStatus)
	}
	if pr.StateReasonCode != "model_binding_error" {
		t.Fatalf("StateReasonCode = %q, want model_binding_error", pr.StateReasonCode)
	}
}

// TestR1054ProbeInsufficientUserQuotaIsQuotaNotAuth：余额预扣 403 ⇒ quota 族
// （与既有 211 钉测 creds67-70 的 quota_permanent 同族同判）。
func TestR1054ProbeInsufficientUserQuotaIsQuotaNotAuth(t *testing.T) {
	msg := `chat status 403: {"error":{"message":"预扣费额度失败, 用户剩余额度: ¥1.474664, 需要预扣费额度: ¥2.497896 (request id: x)","type":"new_api_error","param":"","code":"insufficient_user_quota"}}`
	pr := classifyProbeFailure(msg)
	if pr.AvailabilityState == "auth_failed" {
		t.Fatalf("余额预扣 403 被判 auth_failed（误分类复发）：%+v", pr)
	}
	if pr.QuotaState != "permanently_exhausted" {
		t.Fatalf("QuotaState = %q, want permanently_exhausted（quota 族，BalanceQuotaProbe 探活恢复）", pr.QuotaState)
	}
	if pr.AvailabilityState != "suspended" {
		t.Fatalf("AvailabilityState = %q, want suspended（等 BalanceQuotaProbe 探活恢复）", pr.AvailabilityState)
	}
	if pr.StateReasonCode != string(errorsx.KindQuotaPermanent) {
		t.Fatalf("StateReasonCode = %q, want %q", pr.StateReasonCode, string(errorsx.KindQuotaPermanent))
	}
}

// TestR1054ProbeAmbiguous403StillCredentialWide：歧义 403（无分组/无 quota
// 形态）保持凭据级 auth —— 修复不得放宽真鉴权失败。
func TestR1054ProbeAmbiguous403StillCredentialWide(t *testing.T) {
	pr := classifyProbeFailure(`chat status 403: {"error":{"message":"Forbidden"}}`)
	if pr.AvailabilityState == "ready" {
		t.Fatalf("歧义 403 被降级成 ready ⇒ 凭据级鉴权处置丢失：%+v", pr)
	}
	if pr.BindingOnly {
		t.Fatalf("歧义 403 不得 BindingOnly：%+v", pr)
	}
}
