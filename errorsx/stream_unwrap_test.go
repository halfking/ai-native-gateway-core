package errorsx

import "testing"

// 2026-10-13 Vapeur 流式包裹事故判据（errorsx 层）。
//
// 事故链：gpt-5.6-sol 流式请求 → chat 腿 400「Function tools with
// reasoning_effort are not supported … use /v1/responses」→ 模式回退到
// /v1/responses（正当）→ Vapeur 把内层 400（temperature 拒绝）包成
// HTTP 502 stream_error（message="400|{...}"）→ KindUpstreamDown →
// 盲重试三连 502 → node_probe_state 3 连败 + 300s 退避 → 候选清空。
//
// bodyVapeurStreamWrapped 是事故现场 executor 收到的 502 体形状
// （外层错误体逐字节还原自 gateway-8782.log 785ca12e 的 goal_retry 报文）。
const bodyVapeurStreamWrapped = `{"error":{"code":"stream_error","type":"server_error","message":"400|{\n  \"error\": {\n    \"message\": \"Unsupported parameter: 'temperature' is not supported with this model.\",\n    \"type\": \"invalid_request_error\",\n    \"code\": \"invalid_request\"\n  }\n}"}}`

func TestUnwrapStreamWrapped4xx_ExtractsEmbedded400(t *testing.T) {
	st, embedded, ok := UnwrapStreamWrapped4xx([]byte(bodyVapeurStreamWrapped))
	if !ok {
		t.Fatal("wrapper must be recognized")
	}
	if st != 400 {
		t.Fatalf("embedded status = %d, want 400", st)
	}
	if !unsupportedParamRe.Match(embedded) {
		t.Fatal("embedded body must classify as unsupported param (premise present)")
	}
}

func TestClassifyErrorWithBody_StreamWrapped4xxIsClientBug(t *testing.T) {
	// 修复前：502 → KindUpstreamDown（transient，盲重试 + 节点冷却）。
	if got := ClassifyErrorWithBody(502, []byte(bodyVapeurStreamWrapped)); got != KindClientBug {
		t.Fatalf("wrapped 400 on 502: got %s, want client_bug", got)
	}
	if got := ClassifyResponseBody(502, []byte(bodyVapeurStreamWrapped)); got != KindClientBug {
		t.Fatalf("ClassifyResponseBody: got %s, want client_bug", got)
	}
}

func TestUnwrapStreamWrapped4xx_NegativeShapes(t *testing.T) {
	negatives := [][]byte{
		// 真 5xx 内层不解包（仍是节点/上游故障）。
		[]byte(`{"error":{"code":"stream_error","message":"500|{\"error\":{\"message\":\"boom\"}}"}}`),
		// message 非包裹形态。
		[]byte(`{"error":{"message":"upstream blew up"}}`),
		[]byte(`{"error":{"message":"400|not json at all"}}`),
		// 数字开头但非 4xx。
		[]byte(`{"error":{"message":"302|{\"a\":1}"}}`),
		// 非法 JSON。
		[]byte(`not json`),
	}
	for i, b := range negatives {
		if _, _, ok := UnwrapStreamWrapped4xx(b); ok {
			t.Fatalf("negative %d must not unwrap", i)
		}
	}
	// 真 5xx 内层包裹体维持 upstream_down 语义。
	if got := ClassifyErrorWithBody(502, []byte(`{"error":{"code":"stream_error","message":"500|{\"error\":{\"message\":\"boom\"}}"}}`)); got != KindUpstreamDown {
		t.Fatalf("embedded 5xx: got %s, want upstream_down", got)
	}
}

// 2026-10-13 claude 轮补充：内层 5xx 也要解（Vapeur 对 claude 系流式回
// 502 包 502——内层是能力裁决文案），分类落到 KindUnsupportedFeature
// 而非 upstream_down 盲重试。
const bodyVapeurStreamWrapped502 = `{"error":{"code":"stream_error","type":"server_error","message":"502|{\"error\":{\"message\":\"该供应商不支持 Responses API 流式请求\",\"type\":\"server_error\"}}"}}`

func TestClassify_StreamWrapped5xxVendorVerdictIsUnsupportedFeature(t *testing.T) {
	if got := ClassifyErrorWithBody(502, []byte(bodyVapeurStreamWrapped502)); got != KindUnsupportedFeature {
		t.Fatalf("wrapped 502 vendor verdict: got %s, want unsupported_feature", got)
	}
	// 宽解包返回内层 502；4xx 形态（reqprobe 门用）不认它。
	if st, _, ok := UnwrapStreamWrapped([]byte(bodyVapeurStreamWrapped502)); !ok || st != 502 {
		t.Fatalf("wide unwrap: st=%d ok=%v, want 502/true", st, ok)
	}
	if _, _, ok := UnwrapStreamWrapped4xx([]byte(bodyVapeurStreamWrapped502)); ok {
		t.Fatal("4xx unwrap must not claim an inner-502 wrapper")
	}
}

func TestClassify_OperationNotAllowedIsUnsupportedFeature(t *testing.T) {
	// 双层嵌套形态（2026-10-13 claude-haiku 流式实测）：
	// {"code":4006,"message":"{\"message\":\"Operation not allowed\"}"}
	nested := []byte(`{"code":4006,"message":"{\"message\":\"Operation not allowed\"}","data":null}`)
	if got := ClassifyErrorWithBody(400, nested); got != KindUnsupportedFeature {
		t.Fatalf("nested operation not allowed: got %s, want unsupported_feature", got)
	}
	body := []byte(`{"message":"Operation not allowed"}`)
	if got := ClassifyErrorWithBody(400, body); got != KindUnsupportedFeature {
		t.Fatalf("operation not allowed: got %s, want unsupported_feature", got)
	}
	if got := ClassifyResponseBody(400, body); got != KindUnsupportedFeature {
		t.Fatalf("ClassifyResponseBody: got %s, want unsupported_feature", got)
	}
	// 5xx 上的同文案不进该分支（门是 400/403）。
	if got := ClassifyErrorWithBody(500, body); got == KindUnsupportedFeature {
		t.Fatal("5xx must not hit the 400/403-gated branch")
	}
}
