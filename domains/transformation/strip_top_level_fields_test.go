package transformation

// strip_top_level_fields_test.go — P0-1 StripTopLevelJSONFields 判据：
// 删顶层键、嵌套同名键不误删、无目标键原样返回、非法输入原样返回。

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestStripTopLevelJSONFieldsRemovesTargets(t *testing.T) {
	body := []byte(`{"model":"grok-4.6","messages":[{"role":"user","content":"hi"}],"metadata":{"user_id":"u1"},"service_tier":"auto"}`)
	out := StripTopLevelJSONFields(body, []string{"metadata", "service_tier"})
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("output not valid JSON: %s", out)
	}
	if _, ok := obj["metadata"]; ok {
		t.Fatalf("metadata not stripped: %s", out)
	}
	if _, ok := obj["service_tier"]; ok {
		t.Fatalf("service_tier not stripped: %s", out)
	}
	// 未点名键保持原值。
	if string(obj["model"]) != `"grok-4.6"` {
		t.Fatalf("model changed: %s", out)
	}
	if _, ok := obj["messages"]; !ok {
		t.Fatalf("messages lost: %s", out)
	}
}

func TestStripTopLevelJSONFieldsKeepsNestedSameNameKeys(t *testing.T) {
	// messages[1] 里嵌套的 metadata 不能被误删——只剥顶层。
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi","metadata":{"keep":true}}],"metadata":{"drop":true}}`)
	out := StripTopLevelJSONFields(body, []string{"metadata"})
	if bytes.Contains(out, []byte(`"drop":true`)) {
		t.Fatalf("top-level metadata not stripped: %s", out)
	}
	if !bytes.Contains(out, []byte(`"keep":true`)) {
		t.Fatalf("nested metadata collateral damage: %s", out)
	}
}

func TestStripTopLevelJSONFieldsNoTargetsReturnsOriginal(t *testing.T) {
	body := []byte(`{"model":"m","messages":[]}`)
	for _, fields := range [][]string{
		nil,
		{},
		{"nonexistent"},
		{""},
	} {
		out := StripTopLevelJSONFields(body, fields)
		if !bytes.Equal(out, body) {
			t.Fatalf("fields %v: body changed: %s", fields, out)
		}
	}
}

func TestStripTopLevelJSONFieldsInvalidJSONReturnsOriginal(t *testing.T) {
	body := []byte(`not json at all`)
	out := StripTopLevelJSONFields(body, []string{"metadata"})
	if !bytes.Equal(out, body) {
		t.Fatalf("invalid JSON body changed: %s", out)
	}
	// JSON 数组（非对象）同样原样返回。
	arr := []byte(`[1,2,3]`)
	if out := StripTopLevelJSONFields(arr, []string{"metadata"}); !bytes.Equal(out, arr) {
		t.Fatalf("array body changed: %s", out)
	}
}

func TestStripTopLevelJSONFieldsSubsetStrips(t *testing.T) {
	// 多字段里只有部分在场：在场者被剥，结果与原 body 可区分（调用方靠
	// bytes.Equal 检测「确实发生了剔除」——前提在场判定的基础）。
	body := []byte(`{"model":"m","logprobs":true,"metadata":{}}`)
	out := StripTopLevelJSONFields(body, []string{"metadata", "prediction"})
	if bytes.Equal(out, body) {
		t.Fatalf("present field was not stripped (premise detection would break): %s", out)
	}
	if bytes.Contains(out, []byte("logprobs")) == false {
		t.Fatalf("unrelated field lost: %s", out)
	}
}
