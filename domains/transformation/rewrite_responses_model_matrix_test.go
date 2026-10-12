package transformation

// 请求格式交叉测试矩阵 A5.2（docs/audit/2026-10-13-请求格式交叉测试矩阵.md）：
// responses 入向 × openai-responses 上游（Vapeur 形态，SupportsNativeResponses
// 门开）走原始 body + RewriteResponsesModel（executor_chat.go:567-599）。
// 断言：除 model 字段被改写为出站模型名外，body 其余部分逐字节不变——
// 这就是「同格式全样保留」在 R1 路径上的实现方式。
import (
	"encoding/json"
	"testing"
)

func TestMatrix_R1_ResponsesNativePassthrough_ModelRewriteOnly(t *testing.T) {
	original := []byte(`{"model":"gpt-5.6-luna",` +
		`"instructions":"be brief",` +
		`"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],` +
		`"reasoning":{"effort":"medium","summary":"auto"},` +
		`"text":{"verbosity":"low"},` +
		`"vendor_responses_future_param":{"nested":{"a":[1,2]}},` +
		`"store":true,"temperature":0.5}`)

	rewritten := RewriteResponsesModel(original, "gpt-5.6-luna-relay")

	var origMap, rewMap map[string]json.RawMessage
	if err := json.Unmarshal(original, &origMap); err != nil {
		t.Fatalf("original not json: %v", err)
	}
	if err := json.Unmarshal(rewritten, &rewMap); err != nil {
		t.Fatalf("rewritten not json: %v", err)
	}
	if string(rewMap["model"]) != `"gpt-5.6-luna-relay"` {
		t.Errorf("model = %s, want gpt-5.6-luna-relay", rewMap["model"])
	}
	// 除 model 外的每个键，原始 JSON 片段必须逐字节一致。
	delete(origMap, "model")
	for k, v := range origMap {
		got, ok := rewMap[k]
		if !ok {
			t.Errorf("rewritten body lost %q", k)
			continue
		}
		if string(got) != string(v) {
			t.Errorf("rewritten body changed %q: %s -> %s", k, v, got)
		}
	}
	if len(rewMap) != len(origMap)+1 {
		t.Errorf("rewritten body has unexpected extra keys: %d vs %d+1", len(rewMap), len(origMap))
	}
}
