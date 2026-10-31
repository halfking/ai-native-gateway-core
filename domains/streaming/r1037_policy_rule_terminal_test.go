package streaming

// r1037（2026-10-20，211）：策略拦截的**终帧**必须说清「因为什么规则」。
//
// ## 事故
//
// 211（llm.itestu.cn）上 zcode 用 minimax-m3 干活，模型要 curl 下载 Android SDK
// 镜像，防外传守卫把整条工具调用掐掉 → 流被终止 → 客户端只看到
//
//	provider=new-provider-2 model=minimax-m3 reason=empty_model_response
//
// zcode 侧的判定是「模型没返回文本、没返回工具调用、也没有 usage」⇒ 空响应。
// 网关侧当时的终帧只有一句 "Response blocked by output policy"，**没有规则名**，
// 客户端既没法把它和上游故障区分开，运维在日志里也看不到是哪条规则。
//
// ## 契约
//
//  1. 三种协议形状的终帧都要带 `"rule"`（规则 ID）。
//  2. openai-responses 形状的 response 对象要带 id/object/status ——
//     客户端的解析器按 response 对象校验，缺字段会让**整帧被丢掉**，
//     那正是「客户端只看到空响应」的成因之一。
//  3. 帧必须是合法 JSON（客户端按 JSON 解析）。
//  4. 没有规则名时仍然渲染终帧（形状合法、码可机器分类），只是没有 rule 字段。

import (
	"encoding/json"
	"strings"
	"testing"
)

// r1037ProtocolShapes 是三种协议形状的终帧。
const (
	r1037ProtoAnthropic = "anthropic-messages"
	r1037ProtoResponses = "openai-responses"
	r1037ProtoDefault   = "openai-chat"
)

// TestR1037PolicyFailureFrameCarriesRule 钉住「因为什么规则被拦」。
func TestR1037PolicyFailureFrameCarriesRule(t *testing.T) {
	const rule = "builtin.network_destination"
	for _, protocol := range []string{r1037ProtoAnthropic, r1037ProtoResponses, r1037ProtoDefault} {
		frame := string(nativeStreamPolicyFailureFrame(protocol, rule, "", ""))
		if !strings.Contains(frame, rule) {
			t.Errorf("%s 终帧缺少规则名 %q: %s", protocol, rule, frame)
		}
		if !strings.Contains(frame, "output_policy_blocked") {
			t.Errorf("%s 终帧缺少可机器分类的 code: %s", protocol, frame)
		}
	}
}

// TestR1037PolicyFailureFrameIsValidJSON 钉住「帧必须能被解析」。
//
// 终帧写坏一个逗号，客户端就会整帧丢弃——表现与「空响应」一模一样。
func TestR1037PolicyFailureFrameIsValidJSON(t *testing.T) {
	for _, protocol := range []string{r1037ProtoAnthropic, r1037ProtoResponses, r1037ProtoDefault} {
		frame := string(nativeStreamPolicyFailureFrame(protocol, "builtin.network_destination", "", ""))
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
}

// TestR1037ResponsesFailureFrameCarriesResponseIdentity 钉住 openai-responses
// 形状的 response 对象字段完整性。
//
// 211 现场的 zcode 走的就是 /v1/responses；客户端解析器按 response 对象校验，
// 缺 id/object/status 会让整帧被丢掉——表现仍是「空响应」。
func TestR1037ResponsesFailureFrameCarriesResponseIdentity(t *testing.T) {
	frame := string(nativeStreamPolicyFailureFrame(r1037ProtoResponses, "builtin.network_destination", "", ""))
	var payload struct {
		Type     string `json:"type"`
		Response struct {
			ID     string `json:"id"`
			Object string `json:"object"`
			Status string `json:"status"`
			Error  struct {
				Code    string `json:"code"`
				Message string `json:"message"`
				Rule   string `json:"rule"`
			} `json:"error"`
		} `json:"response"`
	}
	for _, line := range strings.Split(frame, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload); err != nil {
			t.Fatalf("终帧 data 解析失败: %v\n%s", err, frame)
		}
	}
	if payload.Type != "response.failed" {
		t.Errorf("事件类型 = %q，期望 response.failed", payload.Type)
	}
	if payload.Response.ID == "" {
		t.Error("response.id 缺失：客户端按 response 对象校验，缺字段会整帧丢弃")
	}
	if payload.Response.Object != "response" {
		t.Errorf("response.object = %q，期望 response", payload.Response.Object)
	}
	if payload.Response.Status != "failed" {
		t.Errorf("response.status = %q，期望 failed", payload.Response.Status)
	}
	if payload.Response.Error.Rule != "builtin.network_destination" {
		t.Errorf("response.error.rule = %q，期望 builtin.network_destination", payload.Response.Error.Rule)
	}
}

// TestR1037PolicyFailureFrameWithoutRuleStillRenders 钉住边界：拿不出规则名时
// 仍然要渲染终帧。渲染不出来 = 客户端看到连接被截断 = 又变成「空响应」。
func TestR1037PolicyFailureFrameWithoutRuleStillRenders(t *testing.T) {
	for _, protocol := range []string{r1037ProtoAnthropic, r1037ProtoResponses, r1037ProtoDefault} {
		frame := string(nativeStreamPolicyFailureFrame(protocol, "", "", ""))
		if !strings.Contains(frame, "output_policy_blocked") {
			t.Errorf("%s 无规则名时终帧缺失: %s", protocol, frame)
		}
		if strings.Contains(frame, `"rule"`) {
			t.Errorf("%s 无规则名时不应出现 rule 字段: %s", protocol, frame)
		}
	}
}
