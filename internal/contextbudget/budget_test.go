package contextbudget

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestResolveBucketBudget(t *testing.T) {
	for _, tc := range []struct {
		spec Spec
		want int
	}{
		{Spec{ContextWindow: 128000}, 64000},
		{Spec{ContextWindow: 256000}, 128000},
		{Spec{ContextWindow: 1000000}, 500000},
		{Spec{ContextWindow: 2000000}, 500000},
		{Spec{ContextWindow: 1000000, MaxInputTokens: 32000}, 32000},
		{Spec{ContextWindow: 128000, OutputReserve: 100000, SafetyMargin: 1000}, 27000},
		{Spec{ContextWindow: 8193}, 4096},
	} {
		got, err := Resolve(tc.spec)
		if err != nil || got.InputTokens != tc.want || got.JSONBytes != DefaultMaxJSONBytes {
			t.Fatalf("Resolve(%+v) = %+v, %v; want %d tokens", tc.spec, got, err, tc.want)
		}
	}
	// 2026-11-10 改判（opus-5-5 422 事故）：未知窗口不再硬拒（旧断言：
	// Resolve({}) 必须报 UnknownContext），而是回落包级保守上限
	// （MaxInputTokens / DefaultMaxJSONBytes）——生产 ~90% 模型
	// context_window=NULL，旧的 fail-closed 把数据缺口放大成全平台
	// pre-dispatch 拒绝（ZCode empty_model_response）。「绝不当无限」
	// 的设计意图保持：全局上限仍然封顶，已知窗口的精确算术不变。
	for _, spec := range []Spec{{}, {ContextWindow: -1}} {
		got, err := Resolve(spec)
		if err != nil || got.InputTokens != MaxInputTokens || got.JSONBytes != DefaultMaxJSONBytes {
			t.Fatalf("unknown-window spec %+v must resolve to global caps, got %+v, %v", spec, got, err)
		}
	}
	for _, spec := range []Spec{{ContextWindow: 8192, OutputReserve: 8192}, {ContextWindow: 8192, SafetyMargin: -1}} {
		if _, err := Resolve(spec); err == nil {
			t.Fatalf("accepted invalid or exhausted spec %+v", spec)
		}
	}
}

func TestCheckFinalBodyAndOutputReserve(t *testing.T) {
	body := []byte(`{"model":"model","messages":[{"role":"user","content":"hello"}],"max_tokens":7000}`)
	got, err := Check(body, Spec{ContextWindow: 8192})
	if err != nil || got.Budget.InputTokens != 1192 || got.Tokens <= 0 || got.BodySHA256 == "" {
		t.Fatalf("Check = %+v, %v", got, err)
	}
	_, err = Check(body, Spec{ContextWindow: 8192, MaxJSONBytes: 10})
	var budgetErr *Error
	if !errors.As(err, &budgetErr) || budgetErr.Kind != BytesExceeded {
		t.Fatalf("want typed bytes error, got %v", err)
	}
	large := []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("x", 500001) + `"}]}`)
	_, err = Check(large, Spec{ContextWindow: 2000000})
	if !errors.As(err, &budgetErr) || budgetErr.Kind != TokensExceeded {
		t.Fatalf("want global token cap, got %v", err)
	}
	_, err = Check(body, Spec{ContextWindow: 128})
	if err == nil {
		t.Fatal("small failover bucket accepted the larger-bucket body")
	}
}

func TestThinkingIsIncludedInOutputLimit(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}],"max_tokens":6000,"thinking":{"type":"enabled","budget_tokens":4000}}`)
	got, err := Check(body, Spec{ContextWindow: 8192})
	if err != nil || got.Budget.InputTokens != 2192 {
		t.Fatalf("thinking was counted twice: %+v, %v", got, err)
	}
}

func TestTextProtocolsAreCountedConservatively(t *testing.T) {
	for _, body := range []string{
		`{"messages":[{"role":"system","content":"never erase"},{"role":"user","content":"你好👋"}],"tools":[{"type":"function","function":{"name":"file","parameters":{"properties":{"image_url":{"type":"string"}}}}}]}`,
		`{"system":"system","messages":[{"role":"user","content":[{"type":"text","text":"你好"}]}]}`,
		`{"instructions":"rules","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`,
		`{"systemInstruction":{"parts":[{"text":"rules"}]},"contents":[{"role":"user","parts":[{"text":"hello"}]}],"generationConfig":{"maxOutputTokens":2048}}`,
		`{"prompt":"hello","images":[]}`,
	} {
		got, err := Check([]byte(body), Spec{ContextWindow: 128000})
		if err != nil || got.Tokens < len(body) {
			t.Fatalf("under-counted visible text/schema: %+v, %v", got, err)
		}
	}
}

func TestUncountedExternalContextFailsClosed(t *testing.T) {
	for _, body := range []string{
		`{"input":"hi","previous_response_id":"resp_1"}`,
		`{"input":"hi","conversation":{"id":"conv_1"}}`,
		`{"contents":[],"cachedContent":"cachedContents/one"}`,
		`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.invalid/image"}}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"AA=="}}]}]}`,
		`{"contents":[{"parts":[{"inlineData":{"mimeType":"image/png","data":"AA=="}}]}]}`,
		`{"prompt":"hi","images":["AA=="]}`,
		`{"input":"hi","tools":[{"type":"web_search"}]}`,
		`{"input":[{"type":"reasoning","encrypted_content":"opaque"}]}`,
		`{"input":[{"type":"item_reference","id":"item_1"}]}`,
		`{"prompt":{"id":"pmpt_1"},"input":"hi"}`,
		`{"messages":[{"role":"assistant","audio":{"id":"audio_1"}}]}`,
		`{"messages":[{"role":"user","content":[{"type":"audio_url","url":"https://example.invalid/audio"}]}]}`,
	} {
		_, err := Check([]byte(body), Spec{ContextWindow: 128000})
		var budgetErr *Error
		if !errors.As(err, &budgetErr) || budgetErr.Kind != Uncountable {
			t.Fatalf("external state/media accepted or misclassified: %s: %v", body, err)
		}
	}
	body := []byte(`{"input":"hi","previous_response_id":"resp_1"}`)
	got, err := Check(body, Spec{ContextWindow: 128000, ExternalInputKnown: true, ExternalInputTokens: 1000})
	if err != nil || got.Tokens < 1000+len(body) {
		t.Fatalf("trusted external count missing: %+v, %v", got, err)
	}
}

func TestRejectAmbiguousOrMalformedJSON(t *testing.T) {
	for _, body := range []string{`{"messages":[]}{}`, `{"messages":[],"messages":[]}`, `{"messages":[}`, `[]`, `{"max_tokens":1.5}`, `{"max_tokens":-1}`, `{"max_output_tokens":999999999999999999999999}`} {
		if _, err := Check([]byte(body), Spec{ContextWindow: 128000}); err == nil {
			t.Fatalf("accepted malformed/ambiguous body %s", body)
		}
	}
}

func TestCheckRequestPreservesReaderAndRechecksMutation(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	req, err := http.NewRequestWithContext(WithSpec(context.Background(), Spec{ContextWindow: 128000}), "POST", "https://example.invalid", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	got, err := CheckRequest(req)
	if err != nil || got.BodySHA256 == "" {
		t.Fatalf("CheckRequest = %+v, %v", got, err)
	}
	actual, _ := io.ReadAll(req.Body)
	if !bytes.Equal(actual, body) {
		t.Fatal("checker consumed sending body")
	}
	req, _ = http.NewRequestWithContext(req.Context(), "POST", "https://example.invalid", bytes.NewReader(body))
	// A serializer replaced Body without updating GetBody. The sending reader,
	// not the stale replay closure, must determine the authorization receipt.
	req.Body = io.NopCloser(strings.NewReader(`{"messages":[{"role":"user","content":"` + strings.Repeat("x", 500000) + `"}]}`))
	if _, err := CheckRequest(req); err == nil {
		t.Fatal("stale GetBody authorized a different actual sending body")
	}
	req, _ = http.NewRequestWithContext(req.Context(), "POST", "https://example.invalid", strings.NewReader(`{"messages":[{"role":"user","content":"`+strings.Repeat("x", 500000)+`"}]}`))
	if _, err := CheckRequest(req); err == nil {
		t.Fatal("changed request reused earlier authorization")
	}
	req.GetBody = nil
	if _, err := CheckRequest(req); err == nil {
		t.Fatal("non-replayable guarded body was silently accepted")
	}
}

func TestRequestWithoutSpecKeepsLegacyComponentBehavior(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://example.invalid", strings.NewReader(`{"input":"hi"}`))
	if _, err := CheckRequest(req); err != nil {
		t.Fatalf("standalone component behavior changed without a bound spec: %v", err)
	}
}
