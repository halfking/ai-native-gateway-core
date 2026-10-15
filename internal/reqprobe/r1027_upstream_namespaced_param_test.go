package reqprobe

import "testing"

// r1027（2026-10-14）：上游侧命名空间参数名 + 双端点文案的方向甄别。
//
// 两个生产实测形态（raw-logs 2026-10-14T00:50-51，本地网关 2614）：
//
//  1. Bedrock 后端 effort 值拒绝（claude-opus-5-5 经 vapeur chat 腿）：
//     "output_config.effort: Input should be 'low', 'medium', 'high',
//     'xhigh' or 'max'"——参数名在冒号前且无引号、且是我方线体上不存在
//     的上游命名空间名。此前三处断链：hints 不认 "Input should be"、
//     点名正则提不出无引号冒号前缀名、named 路径同名假设被命名空间
//     打破 → 整条 400 判不出 → 不记录（管理面表格无此条）、不剥参
//     重试 → 三候选连败整流 503。
//  2. gpt-6.1-sol tools+effort（chat 腿）："Function tools with
//     reasoning_effort are not supported for gpt-6.1-sol in
//     /v1/chat/completions. To use function tools, use /v1/responses or
//     set reasoning_effort to 'none'."——文案同时含 chat 端点提及与
//     responses 指令，旧 chat-first 裸子串匹配把 suggest_mode 误判为
//     chat（记录 23 实证）；补救句 "set reasoning_effort to 'none'"
//     是参数名的可靠来源。
func TestR1027BedrockEffortValueRejectionNamesAndStrips(t *testing.T) {
	body := []byte(`{"error":{"message":"InvokeModelWithResponseStream: operation error Bedrock Runtime: InvokeModelWithResponseStream, https response error StatusCode: 400, RequestID: 5fa15ee3-e5c9-41bf-a389-aebc4a6d595a, ValidationException: output_config.effort: Input should be 'low', 'medium', 'high', 'xhigh' or 'max'","type":"invalid_request_error"},"type":"error"}`)
	// 生产出站体形态（raw-logs dda1fc7f93ad）：chat 线体顶层带
	// reasoning_effort=minimal。
	outbound := []byte(`{"model":"claude-opus-5-5","stream":true,"stream_options":{"include_usage":true},"reasoning_effort":"minimal","messages":[{"role":"user","content":"回复 ok"}]}`)

	d, ok := Diagnose(Input{HTTPStatus: 400, ErrorBody: body, OutboundBody: outbound, ErrorKind: "client_bug", Protocol: "openai-chat"})
	if !ok || d.Trigger != TriggerParamRejected {
		t.Fatalf("diag = %+v ok=%v, want param_rejected（此前整条判不出：不记录不剥参）", d, ok)
	}
	// 前提在场：上游点名 output_config.effort，经别名映射必须落到我方
	// 线体键 reasoning_effort 并真的剥得掉。
	if got := StrippableParams(outbound, d.Param); len(got) != 1 || got[0] != "reasoning_effort" {
		t.Fatalf("strippable = %v (diag.Param=%q), want [reasoning_effort]", got, d.Param)
	}
	stripped, removed := StripParams(outbound, d.Param)
	if len(removed) != 1 || removed[0] != "reasoning_effort" {
		t.Fatalf("removed = %v, want [reasoning_effort]", removed)
	}
	if string(stripped) == string(outbound) {
		t.Fatalf("strip was a no-op")
	}
}

// responses 线体（嵌套 reasoning）同样能剥：别名表两个载体都覆盖。
func TestR1027BedrockEffortAliasCoversNestedReasoning(t *testing.T) {
	outbound := []byte(`{"model":"claude-opus-5-5","stream":true,"reasoning":{"effort":"minimal"},"input":[{"role":"user","content":"hi"}]}`)
	got := StrippableParams(outbound, "output_config.effort")
	if len(got) != 1 || got[0] != "reasoning" {
		t.Fatalf("strippable = %v, want [reasoning]", got)
	}
}

func TestR1027GptSolToolsEffortSuggestsResponses(t *testing.T) {
	body := []byte(`{"error":{"message":"Function tools with reasoning_effort are not supported for gpt-6.1-sol in /v1/chat/completions. To use function tools, use /v1/responses or set reasoning_effort to 'none'.","type":"invalid_request_error","param":"reasoning_effort","code":null}}`)
	outbound := []byte(`{"model":"gpt-6.1-sol","stream":true,"tools":[{"type":"function","function":{"name":"read_file"}}],"reasoning_effort":"medium","messages":[{"role":"user","content":"hi"}]}`)

	d, ok := Diagnose(Input{HTTPStatus: 400, ErrorBody: body, OutboundBody: outbound, ErrorKind: "transient", Protocol: "openai-completions"})
	if !ok {
		t.Fatalf("diag not interested, want mode_mismatch or param_rejected")
	}
	// 方向甄别：responses 指令句（use /v1/responses）必须压过 chat 端点
	// 的陈述性提及——suggest_mode=responses（此前误判 chat，记录 23）。
	if d.Trigger == TriggerModeMismatch && d.SuggestMode != "responses" {
		t.Fatalf("suggest_mode = %q, want responses（responses 指令句优先于 chat 提及）", d.SuggestMode)
	}
	// 无论判成哪类 trigger，补救句点名的 reasoning_effort 必须剥得掉。
	if got := StrippableParams(outbound, d.Param); len(got) != 1 || got[0] != "reasoning_effort" {
		t.Fatalf("strippable = %v (Param=%q), want [reasoning_effort]", got, d.Param)
	}
}

// 守卫：既有 chat 方向裁决不回归——「不支持 Responses API」族仍判 chat。
func TestR1027ChatDirectionVerdictsUnchanged(t *testing.T) {
	cases := []struct {
		name string
		msg  string
	}{
		{"vapeur cjk 不支持 responses api", `{"error":{"message":"该供应商不支持 Responses API","code":4006}}`},
		{"use the chat completions", `{"error":{"message":"This model only works if you use the chat completions endpoint"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, ok := Diagnose(Input{HTTPStatus: 400, ErrorBody: []byte(tc.msg), OutboundBody: []byte(`{"model":"m","messages":[]}`), ErrorKind: "client_bug", Protocol: "openai-completions"})
			if !ok || d.Trigger != TriggerModeMismatch || d.SuggestMode != "chat" {
				t.Fatalf("diag = %+v ok=%v, want mode_mismatch/chat", d, ok)
			}
		})
	}
}
