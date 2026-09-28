// providercap — ResponsesUnsupportedError 回归（2026-09-28 vapeur 轮）。
//
// 2026-09-28 实测 vapeur（多厂商聚合中转）：
//   - claude 系 /v1/responses → 400 {"error":{"message":"该供应商不支持
//     Responses API","type":"invalid_request_error","code":"unsupported_operation"}}
//   - qwen/doubao 系 /v1/responses → 502 {"error":{"message":"QWEN provider
//     does not support the Responses API (/responses). Please use
//     /v1/chat/completions instead.",...}}
//   - 同批模型 /v1/chat/completions 全部 200
//
// 而合规 Responses API 的参数拒绝（"Unsupported parameter: 'messages'"、
// "Invalid 'max_output_tokens'"）不是能力缺口，绝不能误判。
package providercap

import "testing"

func TestResponsesUnsupportedError(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{
			name:   "vapeur claude 400 CJK",
			status: 400,
			body:   `{"error":{"message":"该供应商不支持 Responses API","type":"invalid_request_error","code":"unsupported_operation"}}`,
			want:   true,
		},
		{
			name:   "vapeur qwen 502 english",
			status: 502,
			body:   `{"error":{"message":"QWEN provider does not support the Responses API (/responses). Please use /v1/chat/completions instead.","type":"server_error","code":"provider_error"}}`,
			want:   true,
		},
		{
			name:   "doubao 502",
			status: 502,
			body:   `{"error":{"message":"DOUBAO provider does not support the Responses API (/responses). Please use /v1/chat/completions instead.","type":"server_error","code":"provider_error"}}`,
			want:   true,
		},
		{
			name:   "generic english not supported",
			status: 400,
			body:   `{"error":{"message":"The Responses API is not supported for this model."}}`,
			want:   true,
		},
		{
			name:   "compliant param rejection: unsupported parameter messages",
			status: 400,
			body:   `{"error":{"message":"Unsupported parameter: 'messages'. In the Responses API, this parameter has moved to 'input'.","type":"invalid_request_error"}}`,
			want:   false,
		},
		{
			// 十九轮：not-support 语序的参数拒绝此前漏拦（"not support" 分支
			// 无参数名词排除），会多打一发 chat 探针并写下误导性能力缺口注记。
			name:   "compliant param rejection: does-not-support phrasing",
			status: 400,
			body:   `{"error":{"message":"The Responses API does not support the 'messages' parameter.","type":"invalid_request_error"}}`,
			want:   false,
		},
		{
			name:   "compliant param rejection: CJK 不支持该参数",
			status: 400,
			body:   `{"error":{"message":"Responses API 不支持该参数，请改用 input"}}`,
			want:   false,
		},
		{
			// 终判兜底：真裁决即使带 parameter 字样，只要带 chat/completions
			// 重定向仍按能力缺口处理（真实中转裁决几乎都带重定向）。
			name:   "genuine verdict wins via redirect despite param word",
			status: 502,
			body:   `{"error":{"message":"QWEN provider does not support the Responses API. Parameter passthrough differs; please use /v1/chat/completions instead."}}`,
			want:   true,
		},
		{
			name:   "compliant param rejection: max_output_tokens floor",
			status: 400,
			body:   `{"error":{"message":"Invalid 'max_output_tokens': integer below minimum value. Expected >= 16.","type":"invalid_request_error"}}`,
			want:   false,
		},
		{
			name:   "no responses api mention",
			status: 400,
			body:   `{"error":{"message":"model not found"}}`,
			want:   false,
		},
		{
			name:   "2xx never matches",
			status: 200,
			body:   `{"error":{"message":"该供应商不支持 Responses API"}}`,
			want:   false,
		},
		{
			name:   "5xx internal without verdict",
			status: 500,
			body:   `{"error":{"message":"internal server error"}}`,
			want:   false,
		},
		{
			name:   "empty body",
			status: 400,
			body:   "",
			want:   false,
		},
	}
	for _, tc := range cases {
		if got := ResponsesUnsupportedError(tc.status, tc.body); got != tc.want {
			t.Errorf("%s: ResponsesUnsupportedError(%d, …) = %v, want %v", tc.name, tc.status, got, tc.want)
		}
	}
}
