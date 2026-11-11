package executors

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// format_gate_system_role_test.go — 2026-11-11（ZCode system-role 事故轮）：
// 出站格式门（protocolformat.ValidateAnthropicRequest）必须放行
// messages 数组里的 role=system 消息，且**原样保留**它。
//
// 事故本体（211 生产实证）：ZCode 经 /v1/messages 发来的请求里
// messages[1].role = "system" ⇒ 门按「官方最小形状」拒之 ⇒ 请求从未出网 ⇒
// 客户端拿到 503「All providers unavailable」（第三层修复前）/ 400
// client_bug（第三层修复后）。
//
// ★ 为什么不按「官方形状」把它归一化到顶层 system 字段——211 真凭据直连
//   apiclaude 实测（claude-fable-5-2）：
//
//	messages 内 role=system ⇒ 200，且模型**确实读到**了 system 内容；
//	顶层 system 字段        ⇒ 200，但该上游**直接丢弃**顶层 system
//	                        （可验证指令未被遵守）。
//
//   ⇒ 归一化会**丢掉 system 语义** ⇒ 只能放行原样。
//
// 本文件钉住两件事：
//  ① 门放行 system-role 体（不返回 client_bug）；
//  ② system 消息在出站体里**仍然存在**（没被静默丢弃）——否则「放行」会
//     变成「悄悄把系统提示扔了」，那比拒绝更糟。

// zcodeSystemRoleBody 是 ZCode 实际流量的形状：一条带 <system-reminder>
// 块的 user 消息 + 一条显式的 system 消息。
const zcodeSystemRoleBody = `{"model":"claude-fable-5-2","max_tokens":128000,"messages":[` +
	`{"role":"user","content":[{"type":"text","text":"<system-reminder>\nproject instructions\n</system-reminder>"}]},` +
	`{"role":"system","content":"You are ZCode."}]}`

func TestPrepareAnthropicRequestBody_SystemRoleAccepted(t *testing.T) {
	cand := provider.Candidate{
		ProviderID:   587,
		CredentialID: 31,
		Protocol:     "anthropic-messages",
		RawModel:     "claude-fable-5-2",
	}
	params := &ExecParams{
		R:              httptest.NewRequest("POST", "/v1/messages", nil),
		ClientModel:    "claude-fable-5-2",
		OutboundModel:  "claude-fable-5-2",
		ClientProtocol: "anthropic-messages",
	}

	executor := &Executor{}
	out, err := executor.prepareAnthropicRequestBody(params, cand, []byte(zcodeSystemRoleBody))
	if err != nil {
		t.Fatalf("system-role body must pass the format gate, got: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("outbound body is empty")
	}
	if !strings.Contains(string(out), `"role":"system"`) {
		t.Fatalf("the system turn must be preserved in the outbound body, got: %s", out)
	}
}

// TestPrepareAnthropicRequestBody_GateStillRejectsForeignRole is the negative
// control: widening the role closure to user|assistant|system must not make
// the gate a rubber stamp — a genuinely foreign role (anthropic has no "tool"
// role) must still be rejected locally with KindClientBug.
func TestPrepareAnthropicRequestBody_GateStillRejectsForeignRole(t *testing.T) {
	body := []byte(`{"model":"claude-fable-5-2","max_tokens":1024,"messages":[{"role":"tool","content":"x"}]}`)
	cand := provider.Candidate{
		ProviderID:   587,
		CredentialID: 31,
		Protocol:     "anthropic-messages",
		RawModel:     "claude-fable-5-2",
	}
	params := &ExecParams{
		R:              httptest.NewRequest("POST", "/v1/messages", nil),
		ClientModel:    "claude-fable-5-2",
		OutboundModel:  "claude-fable-5-2",
		ClientProtocol: "anthropic-messages",
	}

	executor := &Executor{}
	out, err := executor.prepareAnthropicRequestBody(params, cand, body)
	if err == nil {
		t.Fatalf("a foreign role must still be rejected locally, got body=%s", out)
	}
	if kind := classifyExecError(err); kind != "client_bug" {
		t.Fatalf("classifyExecError = %q, want client_bug", kind)
	}
}
