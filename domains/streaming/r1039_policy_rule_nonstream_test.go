package streaming

// r1039（D3，2026-10-20）：**非流式**的策略拦截响应必须带上规则名。
//
// ## 现状（D3 的发现）
//
// 流式终帧早就带规则名：
//
//	data: {"error":{"type":"api_error","code":"output_policy_blocked",
//	        "message":"Response blocked by output policy","rule":"builtin.egress_sink"}}
//
// 非流式 403 原来只有：
//
//	{"error":{"message":"Response blocked by output policy","type":"policy_violation",
//	          "code":"output_policy_blocked","request_id":"…"}}
//
// ⇒ 同一个拦截，两条路径给客户端的信息量不同；非流式那条**说不出因为什么
// 规则被拦**，客户端因此无法提示用户「这次调用被拦了，要不要继续执行」
// （owner 口径 2026-10-20）。
//
// ## 契约
//
//   1. 非流式 403 的 error 对象带 rule（与流式终帧**同一个标识**）。
//   2. 另带 reason（人类可读）与 field（出问题的参数位置）。
//   3. **既有键原样保留**（message / type / code / request_id）⇒ 向后兼容。
//   4. 拿不出规则名时**不出现**这些键（不出现空串）。
//   5. Anthropic 形状沿用既有约定（顶层 type=error）。

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

// r1039DecodeErrorBody 解出响应体里的 error 对象。
func r1039DecodeErrorBody(t *testing.T, body string) map[string]any {
	t.Helper()
	var probe struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &probe); err != nil {
		t.Fatalf("响应体不是合法 JSON: %v\n%s", err, body)
	}
	if probe.Error == nil {
		t.Fatalf("响应体里没有 error 对象: %s", body)
	}
	return probe.Error
}

// TestR1039NonStreamPolicyBlockCarriesRule 是主契约：非流式 403 带规则名。
func TestR1039NonStreamPolicyBlockCarriesRule(t *testing.T) {
	const rule = "builtin.egress_sink"
	rec := httptest.NewRecorder()
	writeOutputPolicyBlockedJSON("openai", rec, 403, "req-1",
		"Response blocked by output policy", "policy_violation", "output_policy_blocked",
		rule, "the tool call appears to send data to an external destination (rule "+rule+") at $.command", "$.command", "", "")

	if rec.Code != 403 {
		t.Fatalf("状态码 = %d，期望 403", rec.Code)
	}
	errObj := r1039DecodeErrorBody(t, rec.Body.String())
	if got, _ := errObj["rule"].(string); got != rule {
		t.Errorf("error.rule = %q，期望 %q", got, rule)
	}
	reason, _ := errObj["reason"].(string)
	if reason == "" {
		t.Error("error.reason 缺失：客户端拿不到可读的拦截原因")
	}
	if field, _ := errObj["field"].(string); field != "$.command" {
		t.Errorf("error.field = %q，期望 $.command", field)
	}
}

// TestR1039NonStreamPolicyBlockKeepsLegacyShape 钉住**向后兼容**：
// 既有键一个都不能少（客户端与既有测试都依赖它们）。
func TestR1039NonStreamPolicyBlockKeepsLegacyShape(t *testing.T) {
	rec := httptest.NewRecorder()
	writeOutputPolicyBlockedJSON("openai", rec, 403, "req-1",
		"Response blocked by output policy", "policy_violation", "output_policy_blocked",
		"builtin.egress_sink", "reason text", "$.command", "", "")
	errObj := r1039DecodeErrorBody(t, rec.Body.String())
	// 2026-10-28 拦截审核轮：message 文本现在携带规则/请求 ID/审核状态
	// （按最弱客户端设计），不再是裸常量——既有判据从「逐字相等」放宽为
	// 「稳定前缀仍在」（客户端做子串匹配的既有用法不破）。
	for key, want := range map[string]string{
		"message":    "Response blocked by output policy",
		"type":       "policy_violation",
		"code":       "output_policy_blocked",
		"request_id": "req-1",
	} {
		got, _ := errObj[key].(string)
		if key == "message" {
			if !strings.Contains(got, want) {
				t.Errorf("error.message = %q，应包含稳定前缀 %q", got, want)
			}
			continue
		}
		if got != want {
			t.Errorf("error.%s = %q，期望 %q（既有键不能丢）", key, got, want)
		}
	}
}

// TestR1039NonStreamPolicyBlockRuleMatchesStreamingFrame 钉住「两条路径同一个
// 标识」——这是 D3 的核心：客户端只该认一个规则名。
func TestR1039NonStreamPolicyBlockRuleMatchesStreamingFrame(t *testing.T) {
	const rule = "builtin.egress_sink"
	frame := string(nativeStreamPolicyFailureFrame(r1037ProtoDefault, rule, "", ""))
	rec := httptest.NewRecorder()
	writeOutputPolicyBlockedJSON("openai", rec, 403, "req-1",
		"Response blocked by output policy", "api_error", "output_policy_blocked", rule, "", "", "", "")
	errObj := r1039DecodeErrorBody(t, rec.Body.String())
	got, _ := errObj["rule"].(string)
	if got == "" {
		t.Fatal("非流式响应缺规则名")
	}
	if !strings.Contains(frame, `"rule":"`+got+`"`) {
		t.Errorf("流式终帧与非流式响应的规则名不一致: frame=%s body=%s", frame, got)
	}
}

// TestR1039NonStreamPolicyBlockWithoutRuleOmitsKeys 钉住边界：拿不出规则名时
// 不出现这些键（空串会让客户端以为「有规则但叫不出名字」）。
func TestR1039NonStreamPolicyBlockWithoutRuleOmitsKeys(t *testing.T) {
	rec := httptest.NewRecorder()
	writeOutputPolicyBlockedJSON("openai", rec, 403, "req-1",
		"Response blocked by output policy", "api_error", "output_policy_blocked", "", "", "", "", "")
	errObj := r1039DecodeErrorBody(t, rec.Body.String())
	for _, key := range []string{"rule", "reason", "field"} {
		if _, ok := errObj[key]; ok {
			t.Errorf("无规则名时不应出现 %q 键: %+v", key, errObj)
		}
	}
	// 前提仍在场：既有键仍在（失败信封形状没变）。
	if errObj["code"] != "output_policy_blocked" {
		t.Errorf("既有键 code 丢失: %+v", errObj)
	}
}

// TestR1039AnthropicPolicyBlockCarriesRule 钉住 Anthropic 形状：顶层
// type=error，error 对象里带规则名。
func TestR1039AnthropicPolicyBlockCarriesRule(t *testing.T) {
	rec := httptest.NewRecorder()
	writeOutputPolicyBlockedJSON("anthropic", rec, 403, "req-1",
		"Response blocked by output policy", "policy_violation", "output_policy_blocked",
		"builtin.egress_sink", "reason text", "$.command", "", "")
	var probe struct {
		Type  string         `json:"type"`
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &probe); err != nil {
		t.Fatalf("响应体不是合法 JSON: %v\n%s", err, rec.Body.String())
	}
	if probe.Type != "error" {
		t.Errorf("顶层 type = %q，期望 error", probe.Type)
	}
	if got, _ := probe.Error["rule"].(string); got != "builtin.egress_sink" {
		t.Errorf("error.rule = %q，期望 builtin.egress_sink", got)
	}
	if _, ok := probe.Error["message"]; !ok {
		t.Error("error.message 丢失（既有键）")
	}
}

// TestR1039PolicyBlockMetadataFromInterceptorResult 钉住元数据取值的边界：
// 缺项返回空串，缺元数据不 panic。
func TestR1039PolicyBlockMetadataFromInterceptorResult(t *testing.T) {
	rule, reason, field, digest := policyBlockMetadataFrom(nil)
	if rule != "" || reason != "" || field != "" || digest != "" {
		t.Errorf("nil 结果应返回全空: %q %q %q %q", rule, reason, field, digest)
	}
	rule, reason, field, digest = policyBlockMetadataFrom(&response.InterceptResult{})
	if rule != "" || reason != "" || field != "" || digest != "" {
		t.Errorf("无元数据应返回全空: %q %q %q %q", rule, reason, field, digest)
	}
}

// TestR1039StreamPolicyFrameCarriesReason 钉住流式终帧的**原因短语**。
//
// r1037 的终帧只带 rule；r1039/D4 要求客户端能据此提示用户「要不要继续」
// ⇒ 补一个可读短语。既有形状不变：没有原因时**不出现** reason 键。
func TestR1039StreamPolicyFrameCarriesReason(t *testing.T) {
	const reason = "output policy blocked stream lane (rule builtin.egress_sink) at $.command"
	for _, protocol := range []string{r1037ProtoAnthropic, r1037ProtoResponses, r1037ProtoDefault} {
		frame := string(nativeStreamPolicyFailureFrame(protocol, "builtin.egress_sink", "", "", reason))
		if !strings.Contains(frame, "builtin.egress_sink") {
			t.Errorf("%s 终帧缺少规则名: %s", protocol, frame)
		}
		if !strings.Contains(frame, "reason") {
			t.Errorf("%s 终帧缺少可读原因: %s", protocol, frame)
		}
		// 帧必须仍是合法 JSON（客户端按 JSON 解析，坏帧会被整帧丢掉）。
		for _, line := range strings.Split(frame, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			payload := strings.TrimPrefix(line, "data: ")
			if payload == "[DONE]" {
				continue
			}
			var probe map[string]any
			if err := json.Unmarshal([]byte(payload), &probe); err != nil {
				t.Errorf("%s 终帧的 data 不是合法 JSON: %v\n%s", protocol, err, payload)
			}
		}
	}
	// 既有形状不变：没有原因时不出现 reason 键（r1037 的契约）。
	frame := string(nativeStreamPolicyFailureFrame(r1037ProtoDefault, "builtin.egress_sink", "", ""))
	if strings.Contains(frame, "\"reason\"") {
		t.Errorf("无原因时不应出现 reason 字段: %s", frame)
	}
}

// TestR1039StreamPolicyFrameEscapesReason 钉住「原因短语里的引号不破坏帧」。
//
// 原因短语可能带参数位置与模型写的内容；不转义 ⇒ 整帧非法 JSON ⇒ 客户端
// 把它丢掉 ⇒ 表现与「空响应」一样（211 事故的形态）。
func TestR1039StreamPolicyFrameEscapesReason(t *testing.T) {
	const reason = `blocked at $.cmd with "quotes" and a newline` + "\n"
	frame := string(nativeStreamPolicyFailureFrame(r1037ProtoDefault, "builtin.egress_sink", "", "", reason))
	for _, line := range strings.Split(frame, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			continue
		}
		var probe map[string]any
		if err := json.Unmarshal([]byte(payload), &probe); err != nil {
			t.Fatalf("含特殊字符的原因让终帧变成非法 JSON: %v\n%s", err, payload)
		}
	}
}
