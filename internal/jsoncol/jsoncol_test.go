package jsoncol

import (
	"bytes"
	"encoding/json"
	"testing"
)

type payload struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func TestDecode(t *testing.T) {
	t.Run("valid json decodes", func(t *testing.T) {
		var got payload
		if !Decode("op.valid", []byte(`{"name":"a","count":2}`), &got) {
			t.Fatalf("valid json must report true")
		}
		if got.Name != "a" || got.Count != 2 {
			t.Fatalf("decode mismatch: %+v", got)
		}
	})

	t.Run("empty raw is not an error and leaves zero value", func(t *testing.T) {
		got := payload{Name: "untouched", Count: 9}
		if !Decode("op.empty", nil, &got) {
			t.Fatalf("nil raw must report true (not bad data)")
		}
		if got.Name != "untouched" || got.Count != 9 {
			t.Fatalf("empty raw must not modify dst, got %+v", got)
		}
		if !Decode("op.zerolen", []byte{}, &got) {
			t.Fatalf("zero-length raw must report true")
		}
		if got.Count != 9 {
			t.Fatalf("zero-length raw must not modify dst, got %+v", got)
		}
	})

	t.Run("malformed json reports false and leaves zero value", func(t *testing.T) {
		got := payload{Name: "preset", Count: 5}
		if Decode("op.bad", []byte(`{not json`), &got) {
			t.Fatalf("malformed json must report false")
		}
		if got.Count != 5 {
			t.Fatalf("dst must be left as the caller's value on failure, got %+v", got)
		}
	})

	t.Run("type mismatch is a failure, not a silent partial", func(t *testing.T) {
		var got payload
		// count 是字符串而非数字：整条记录不可用
		if Decode("op.mismatch", []byte(`{"name":"a","count":"two"}`), &got) {
			t.Fatalf("type mismatch must report false, not a partially-populated struct")
		}
	})
}

// TestDecode_DistinguishesEmptyFromCorrupt 钉住本包存在的理由：调用方必须
// 能区分「列本来就是空的」与「列有内容但坏了」。两者都返回 true/false 的
// 不同组合，若实现把空 raw 也判成失败，这个区分就消失了。
func TestDecode_DistinguishesEmptyFromCorrupt(t *testing.T) {
	var a, b payload
	emptyOK := Decode("op.empty", nil, &a)
	corruptOK := Decode("op.corrupt", []byte(`{oops`), &b)
	if !emptyOK {
		t.Errorf("empty raw must not be reported as corrupt")
	}
	if corruptOK {
		t.Errorf("corrupt raw must be reported as corrupt")
	}
}

// TestDecode_PreservesUnknownFields 确认这不是「严格模式」：jsonb 里的
// 额外键不应让整条记录判为坏数据（那会让滚动升级期的旧数据整片变零值）。
func TestDecode_PreservesUnknownFields(t *testing.T) {
	var got payload
	raw, _ := json.Marshal(map[string]any{
		"name": "a", "count": 1, "future_field": []int{1, 2, 3},
	})
	if !Decode("op.extra", raw, &got) {
		t.Fatalf("unknown fields must not fail the decode")
	}
	if !bytes.Contains(raw, []byte("future_field")) {
		t.Fatalf("fixture sanity")
	}
	if got.Name != "a" || got.Count != 1 {
		t.Fatalf("known fields must still decode: %+v", got)
	}
}
