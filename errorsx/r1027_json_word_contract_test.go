package errorsx

import "testing"

// r1027（2026-10-14）：OpenAI json 模式契约错误的分类判据。
//
// 事故形态（raw-logs 2026-10-14T01:21，gpt-6.1-sol chat 腿）：客户端
// response_format=json_object 但提示词不含 "json" → 上游 400
// "'messages' must contain the word 'json' in some form..."。此前无分类
// 臂命中 → 落 [transient] → 同 body 盲重试 ×3 + 节点冷却 → 一次客户端
// 契约错误把后续所有无关请求拖进数分钟 no_candidate 级联。
//
// 修复：确定性客户端契约错误归 KindClientBug（fail fast、不冷却凭据）。
// 两个分类器（ClassifyErrorWithBody / ClassifyResponseBody）同臂——
// r1021 教训：两路径政策必须一致。
func TestR1027JsonModeWordRequiredIsClientBug(t *testing.T) {
	// raw-logs 捕获的两种线格式文案原文。
	cases := []struct {
		name string
		body string
	}{
		{"chat 腿 response_format", `{"error":{"message":"'messages' must contain the word 'json' in some form, to use 'response_format' of type 'json_object'.","type":"invalid_request_error","param":"messages","code":null}}`},
		{"responses 腿 text.format", `{"error":{"message":"Response input messages must contain the word 'json' in some form to use 'text.format' of type 'json_object'.","type":"invalid_request_error","param":"input","code":null}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyErrorWithBody(400, []byte(tc.body)); got != KindClientBug {
				t.Fatalf("ClassifyErrorWithBody = %v, want client_bug（transient 会冷却节点引发级联）", got)
			}
			if got := ClassifyResponseBody(400, []byte(tc.body)); got != KindClientBug {
				t.Fatalf("ClassifyResponseBody = %v, want client_bug（两分类器政策必须一致）", got)
			}
		})
	}
	// 前提在场（负向）：普通 json 话题的正文不得误判。
	benign := `{"error":{"message":"model returned invalid json payload","type":"server_error"}}`
	if got := ClassifyErrorWithBody(500, []byte(benign)); got == KindClientBug {
		t.Fatalf("benign 5xx json body mis-classified as client_bug")
	}
}
