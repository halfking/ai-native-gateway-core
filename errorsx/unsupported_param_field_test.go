package errorsx

// unsupported_param_field_test.go — P0-1 RefusedOptionalParam 判据。
// 样本来源：raw-logs 2026-10-13 vapeur 实测（stream_options / temperature /
// thinking 三案）+ pydantic v2 官方错误形态 + OpenAI 经典文案。
// 判据纪律：真实形状回放 + 畸形不提取（防注入）+ 提取与分类分层
// （这里只测提取，黑名单在 paramrefusal/executor 层判）。

import "testing"

func TestRefusedOptionalParamVapeurSamples(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			// raw-logs 2026-10-13T19:36 vapeur/grok-4.6 responses 腿实测形态。
			name: "vapeur_unknown_parameter_dotted_path",
			body: `{"error":{"message":"Unknown parameter: 'stream_options.include_usage'.","type":"invalid_request_error","code":"invalid_request"}}`,
			want: "stream_options.include_usage",
		},
		{
			// raw-logs 2026-10-13 vapeur/grok-4.6 probe 形态。
			name: "vapeur_unknown_parameter_thinking",
			body: `{"error":{"message":"Unknown parameter: 'thinking'.","type":"invalid_request_error","param":"thinking","code":"unknown_parameter"}}`,
			want: "thinking",
		},
		{
			// 2026-10-13 Vapeur gpt-5.6-sol temperature 事故实测。
			name: "vapeur_unsupported_parameter_temperature",
			body: `{"error":{"message":"Unsupported parameter: 'temperature' is not supported with this model.","type":"invalid_request_error","code":"invalid_request"}}`,
			want: "temperature",
		},
		{
			// kimi-k3 聚合中转形态：复数带括号 + 反引号。
			name: "kimi_unsupported_parameters_plural",
			body: `{"error":{"message":"Validation: Unsupported parameter(s): ` + "`web_search_options`" + `","type":"invalid_request_error"}}`,
			want: "web_search_options",
		},
		{
			// OpenAI 经典文案：无引号。
			name: "openai_unrecognized_argument",
			body: `{"error":{"message":"Unrecognized request argument supplied: reasoning_effort","type":"invalid_request_error"}}`,
			want: "reasoning_effort",
		},
		{
			// 中转泛化形态。
			name: "relay_unsupported_field",
			body: `{"error":{"message":"Unsupported request field 'metadata' provided","type":"invalid_request_error"}}`,
			want: "metadata",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := RefusedOptionalParam([]byte(tc.body))
			if !ok {
				t.Fatalf("RefusedOptionalParam(%s) not ok, want field %q", tc.name, tc.want)
			}
			if got != tc.want {
				t.Fatalf("RefusedOptionalParam = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRefusedOptionalParamUnicodeEscaped(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			// \u0027 是转义的单引号：解码前正则的引号锚定全部断链。
			name: "escaped_quotes",
			body: `{"error":{"message":"Unknown parameter: \u0027service_tier\u0027.","type":"invalid_request_error"}}`,
			want: "service_tier",
		},
		{
			// 整个关键词都被转义（\u0055 = 'U'）。
			name: "escaped_keyword",
			body: `{"error":{"message":"\u0055nknown parameter: 'prediction'.","type":"invalid_request_error"}}`,
			want: "prediction",
		},
		{
			// 中文中转 + 转义引号的混合形态。
			name: "cjk_relay_escaped",
			body: `{"error":{"message":"\u672a\u77e5\u53c2\u6570: \u0027logprobs\u0027 \u4e0d\u88ab\u652f\u6301","type":"invalid_request_error"}}`,
			want: "", // 中文「未知参数:」不在词表内——如实不提取
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := RefusedOptionalParam([]byte(tc.body))
			if tc.want == "" {
				if ok {
					t.Fatalf("RefusedOptionalParam unexpectedly extracted %q", got)
				}
				return
			}
			if !ok || got != tc.want {
				t.Fatalf("RefusedOptionalParam = (%q, %v), want (%q, true)", got, ok, tc.want)
			}
		})
	}
}

func TestRefusedOptionalParamPydanticForms(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			// pydantic v2 FastAPI detail 数组形态（结构化 loc 提取）。
			name: "pydantic_loc_array",
			body: `{"detail":[{"type":"extra_forbidden","loc":["body","user_pref"],"msg":"Extra inputs are not permitted","input":"x"}]}`,
			want: "user_pref",
		},
		{
			// code 键变体（部分中转把 type 改写成 code）。
			name: "pydantic_code_key",
			body: `{"detail":[{"code":"extra_forbidden","loc":["body","account_id"],"msg":"Extra inputs are not permitted"}]}`,
			want: "account_id",
		},
		{
			// 深层嵌套 loc（取最后元素）。
			name: "pydantic_deep_loc",
			body: `{"detail":[{"type":"extra_forbidden","loc":["body","reasoning","effort"],"msg":"Extra inputs are not permitted"}]}`,
			want: "effort",
		},
		{
			// pydantic 文本形态（非 JSON 包裹或消息内嵌）。
			name: "pydantic_text_form",
			body: `{"error":{"message":"Extra inputs are not permitted (name 'session_hint')","type":"validation_error"}}`,
			want: "session_hint",
		},
		{
			// 截断的 pydantic JSON（结构化解析失败）→ 裸文本兜底路径。
			name: "pydantic_truncated_text_fallback",
			body: `{"detail":[{"type":"extra_forbidden","loc":["body","trunc_`,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := RefusedOptionalParam([]byte(tc.body))
			if tc.want == "" {
				if ok {
					t.Fatalf("unexpectedly extracted %q", got)
				}
				return
			}
			if !ok || got != tc.want {
				t.Fatalf("RefusedOptionalParam = (%q, %v), want (%q, true)", got, ok, tc.want)
			}
		})
	}
}

func TestRefusedOptionalParamMalformedBodies(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "empty", body: ``},
		{name: "not_json", body: `upstream exploded`},
		{name: "no_param_named", body: `{"error":{"message":"This model's maximum context length is 200 tokens.","type":"invalid_request_error"}}`},
		{name: "auth_shape", body: `{"error":{"message":"Invalid API key","type":"auth_error"}}`},
		{name: "param_without_name", body: `{"error":{"message":"Unknown parameter.","type":"invalid_request_error"}}`},
		{
			// 路径穿越样名字：字符集校验拒绝。
			name: "path_traversal",
			body: `{"error":{"message":"Unknown parameter: '../../etc/passwd'.","type":"invalid_request_error"}}`,
		},
		{
			// 控制字符 / 换行注入。
			name: "control_chars",
			body: `{"error":{"message":"Unknown parameter: 'a\nb'.","type":"invalid_request_error"}}`,
		},
		{
			// 超长名（65 字符）。
			name: "too_long",
			body: `{"error":{"message":"Unknown parameter: '` + stringsRepeat("a", 65) + `'.","type":"invalid_request_error"}}`,
		},
		{
			// 纯数字开头（字段名必须字母/下划线开头，防把端口号当字段）。
			name: "numeric_name",
			body: `{"error":{"message":"Unknown parameter: '8080'.","type":"invalid_request_error"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := RefusedOptionalParam([]byte(tc.body))
			if ok {
				t.Fatalf("RefusedOptionalParam unexpectedly extracted %q from %q", got, tc.name)
			}
		})
	}
}

// TestRefusedOptionalParamExtractsCoreFields：黑名单字段（messages 等）
// 提取层**必须能提出来**（前提在场）——「不重试」的裁决在 paramrefusal /
// executor 层，不在提取层。提取层若把核心字段也拒了，黑名单测试就失去了
// 前提在场的配对（判据纪律第 2 条）。
func TestRefusedOptionalParamExtractsCoreFields(t *testing.T) {
	body := `{"error":{"message":"Unknown parameter: 'messages'.","type":"invalid_request_error"}}`
	got, ok := RefusedOptionalParam([]byte(body))
	if !ok || got != "messages" {
		t.Fatalf("RefusedOptionalParam = (%q, %v), want (\"messages\", true) — extraction must stay neutral, blacklist lives in paramrefusal", got, ok)
	}
}

// TestRefusedOptionalParamDoesNotChangeErrorKinds：P0-1 约束——提取 API
// 不得改变既有 ErrorKind 字符串值（DB 持久化契约）。抽两类实锤。
func TestRefusedOptionalParamDoesNotChangeErrorKinds(t *testing.T) {
	body := []byte(`{"error":{"message":"Unknown parameter: 'thinking'.","type":"invalid_request_error","code":"invalid_request"}}`)
	if got := ClassifyErrorWithBody(400, body); got != KindClientBug {
		t.Fatalf("ClassifyErrorWithBody(400, unknown-parameter body) = %q, want %q (classification unchanged)", got, KindClientBug)
	}
	plain := []byte(`{"error":{"message":"plain transient noise","type":"server_error"}}`)
	// 该文案不匹配任何 4xx 专用正则时维持默认（不因本文件新增词表而漂移）。
	_, _ = RefusedOptionalParam(body)
	_, _ = RefusedOptionalParam(plain)
	if got := ClassifyErrorWithBody(400, body); got != KindClientBug {
		t.Fatalf("classification drifted after extraction calls: %q", got)
	}
}

func stringsRepeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
