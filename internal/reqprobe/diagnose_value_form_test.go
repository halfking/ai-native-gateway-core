package reqprobe

import "testing"

// 2026-10-13 Vapeur chat 腿值拒绝形态判据（reqprobe 层）。
//
// 事故链：chat 腿 400 body {"code":4006,"message":"Unsupported value:
// 'temperature' does not support 0.2 with this model. Only the default
// (1) value is supported."}——原 namedParamPatterns 提不出参数名
// （值前缀形态无 "for"、动词表缺 "does not support"）→ 剥参重试不触发
// → 误走模式回退烧掉一跳。
func TestDiagnoseVapeurChatLegValueFormNamesParam(t *testing.T) {
	body := []byte(`{"code":4006,"message":"Unsupported value: 'temperature' does not support 0.2 with this model. Only the default (1) value is supported.","data":null}`)
	outbound := []byte(`{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"hi"}],"temperature":0.2}`)
	in := Input{HTTPStatus: 400, ErrorBody: body, OutboundBody: outbound, ErrorKind: "client_bug", Protocol: "openai-responses"}
	d, ok := Diagnose(in)
	if !ok || d.Trigger != TriggerParamRejected {
		t.Fatalf("diag = %+v ok=%v, want param_rejected", d, ok)
	}
	if d.Param != "temperature" {
		t.Fatalf("param = %q, want temperature", d.Param)
	}
	// 前提在场：点名后真的剥得掉（与 skeletonParams 修复联动）。
	if stripped := StrippableParams(outbound, d.Param); len(stripped) != 1 || stripped[0] != "temperature" {
		t.Fatalf("strippable = %v, want [temperature]", stripped)
	}
}

// 同族「值不支持」变体：OpenAI 官方 effort 形态（无冒号、有 for）不回归。
func TestDiagnoseValueFormsNoRegression(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"for-form", `{"error":{"message":"Invalid value for 'reasoning_effort': 'x-high' is not one of ...","type":"invalid_request_error"}}`, "reasoning_effort"},
		{"does-not-support", `{"error":{"message":"'top_k' does not support 20 with this model.","type":"invalid_request_error"}}`, "top_k"},
		// 2026-10-13 Vapeur claude 家族（claude-opus-5-5 事故现场）：中转把
		// Anthropic 原生 400 嵌进顶层 message 字符串——反引号参数名 +
		// "may only be set to" 动词族（temperature+thinking 冲突）。
		{"claude-backtick-may-only", `{"code":4006,"message":"{\"message\":\"` + "`temperature`" + ` may only be set to 1 when thinking is enabled or in adaptive mode. Please consult our documentation at https://platform.claude.com/docs\"}"}`, "temperature"},
	}
	for _, c := range cases {
		in := Input{HTTPStatus: 400, ErrorBody: []byte(c.body), OutboundBody: []byte(`{"reasoning_effort":"x-high","top_k":20,"temperature":0.2}`), ErrorKind: "transient"}
		d, ok := Diagnose(in)
		if !ok || d.Param != c.want {
			t.Fatalf("%s: diag = %+v ok=%v, want param=%s", c.name, d, ok, c.want)
		}
	}
}
