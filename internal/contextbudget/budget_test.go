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
	// 2026-11-13 改判（r1050，opus-4-8 反复 422 根修）：输入预算不再叠加
	// 半窗帽（旧断言：128000→64000、256000→128000、8193→4096 均为 W/2）。
	// 新语义：W − 预留 − 安全边际（输出预留另有 body 侧半窗封顶保底，见
	// TestBodyOutputCeilingCappedAtHalfWindow）⇒ 无预留时输入可用全窗
	//（仍受 MaxInputTokens 全局帽约束）。
	for _, tc := range []struct {
		spec Spec
		want int
	}{
		{Spec{ContextWindow: 128000}, 128000},
		{Spec{ContextWindow: 256000}, 256000},
		{Spec{ContextWindow: 1000000}, 500000},
		{Spec{ContextWindow: 2000000}, 500000},
		{Spec{ContextWindow: 1000000, MaxInputTokens: 32000}, 32000},
		{Spec{ContextWindow: 128000, OutputReserve: 100000, SafetyMargin: 1000}, 27000},
		{Spec{ContextWindow: 8193}, 8193},
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
	// 2026-11-10 审计 A8：回落分支同样尊重显式 per-bucket 上限——
	// 配了更紧桶限但窗口未知的桶不得拿到 500k 全局值；未设置（0/负）
	// 仍回落全局上限。
	if got, err := Resolve(Spec{MaxInputTokens: 32000, MaxJSONBytes: 1 << 20}); err != nil ||
		got.InputTokens != 32000 || got.JSONBytes != 1<<20 {
		t.Fatalf("unknown window must honor explicit bucket caps, got %+v, %v", got, err)
	}
	if got, err := Resolve(Spec{MaxInputTokens: -5, MaxJSONBytes: -1}); err != nil ||
		got.InputTokens != MaxInputTokens || got.JSONBytes != DefaultMaxJSONBytes {
		t.Fatalf("unknown window with negative caps must fall back to global caps, got %+v, %v", got, err)
	}
	for _, spec := range []Spec{{ContextWindow: 8192, OutputReserve: 8192}, {ContextWindow: 8192, SafetyMargin: -1}} {
		if _, err := Resolve(spec); err == nil {
			t.Fatalf("accepted invalid or exhausted spec %+v", spec)
		}
	}
}

func TestCheckFinalBodyAndOutputReserve(t *testing.T) {
	// 2026-11-11 改判（211 ZCode 连续 422 根修）：body 的 max_tokens 按半窗
	// 封顶后才并入预留。旧断言 1192（=8192−7000 全额扣除）钉的是「输出
	// 上界当预留」的旧语义；新语义下 body 侧 ask 无法把输入预算压到半窗
	// 以下 ⇒ 小 ask（2000）与无 ask 同形（4096）。ask>半窗的封顶形状见
	// TestBodyOutputCeilingCappedAtHalfWindow。
	body := []byte(`{"model":"model","messages":[{"role":"user","content":"hello"}],"max_tokens":2000}`)
	got, err := Check(body, Spec{ContextWindow: 8192})
	// r1050：无半窗帽后，2000 的 ask 全额扣除 ⇒ 8192−2000 = 6192（旧 4096）。
	if err != nil || got.Budget.InputTokens != 6192 || got.Tokens <= 0 || got.BodySHA256 == "" {
		t.Fatalf("Check = %+v, %v", got, err)
	}
	_, err = Check(body, Spec{ContextWindow: 8192, MaxJSONBytes: 10})
	var budgetErr *Error
	if !errors.As(err, &budgetErr) || budgetErr.Kind != BytesExceeded {
		t.Fatalf("want typed bytes error, got %v", err)
	}
	// 2026-11-10 改判（容量裁决归上游）：nil 计数器的字节上界只做 advisory，
	// 不再触发全局 token 上限拒绝（旧断言 want global token cap）。字节
	// 资源上限与格式/外部引用 fail-closed 不变；小窗口桶同理放行，超窗由
	// 上游 context_length 错误经网关既有恢复链处理。
	large := []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("x", 500001) + `"}]}`)
	gotLarge, err := Check(large, Spec{ContextWindow: 2000000})
	// 无空白 blob 按字节/5 计：500001/5 ≈ 100k（而非字节直计的 500k）。
	if err != nil || gotLarge.Tokens < 90000 || gotLarge.Tokens > 110000 {
		t.Fatalf("blob estimate out of calibrated band: %+v, %v", gotLarge, err)
	}
	// 已知小窗口保持执法：128-token 桶装不下 ~173-token 估计的请求体
	// （校准估计器计数），拒绝是正确行为——tiering 只对「窗口未知」advisory。
	smallBody := []byte(`{"model":"model","messages":[{"role":"user","content":"hi"}]}`)
	if _, err := Check(smallBody, Spec{ContextWindow: 128}); err == nil {
		t.Fatal("known tiny bucket must still reject a body its window cannot fit")
	}
}

func TestThinkingIsIncludedInOutputLimit(t *testing.T) {
	// 2026-11-11 改判：body 侧输出参数（max_tokens / thinking.budget_tokens）
	// 合并取 max 后再按半窗封顶 ⇒ 任何 body 输出参数组合都不得把输入预算
	// 压到半窗以下。旧断言 2192（=8192−6000 全额扣除）钉的是旧语义；新
	// 断言：带与不带 thinking.budget_tokens 的预算同形（=半窗 4096）——
	// 「thinking 不被二次扣除」由「取 max + 封顶」的结构保证（若被加总，
	// 6000+4000=10000 > 4096 半窗，预算仍是 4096，不会更小）。
	body := []byte(`{"messages":[{"role":"user","content":"hi"}],"max_tokens":6000,"thinking":{"type":"enabled","budget_tokens":4000}}`)
	got, err := Check(body, Spec{ContextWindow: 8192})
	if err != nil || got.Budget.InputTokens != 4096 {
		t.Fatalf("thinking budget tightened the input budget below half window: %+v, %v", got, err)
	}
	noThinking := []byte(`{"messages":[{"role":"user","content":"hi"}],"max_tokens":6000}`)
	got2, err := Check(noThinking, Spec{ContextWindow: 8192})
	if err != nil || got2.Budget.InputTokens != got.Budget.InputTokens {
		t.Fatalf("thinking budget changed the input budget: %+v vs %+v, %v", got.Budget, got2.Budget, err)
	}
}

// TestBodyOutputCeilingCappedAtHalfWindow 钉 2026-11-11 211 生产事故形状：
// claude-sonnet-5（窗口 200k）× ZCode max_tokens=128000。
// 旧行为：预留全额扣除 ⇒ 输入预算 = min(100k, 200k−128k) = 72000 ⇒ ~76.8k
// 估计值被拒 ⇒ fit 字节目标 216KB 装不下受保护区 ⇒ ErrProtectedContext 422。
// 新行为：body 侧预留封顶半窗 ⇒ 输入预算恒 ≥ 半窗；ask ≤ 半窗时行为与
// 旧版一致（window/2 本就是 min 独立项）。桶侧显式配置的 OutputReserve
// 不受封顶（仍全额扣除，配置语义不变）。
func TestBodyOutputCeilingCappedAtHalfWindow(t *testing.T) {
	// 事故形状：max_tokens=128000 > 半窗 100000 ⇒ 预算取 100000（而非 72000）。
	incident := []byte(`{"model":"claude-sonnet-5","max_tokens":128000,"messages":[{"role":"user","content":"hi"}]}`)
	got, err := Check(incident, Spec{ContextWindow: 200000})
	if err != nil || got.Budget.InputTokens != 100000 {
		t.Fatalf("incident shape must resolve to half-window input budget, got %+v, %v", got.Budget, err)
	}
	// 小于半窗的 ask：r1050 后全额扣除 ⇒ 200000−32000 = 168000（旧 100000
	// 半窗帽）。这正是 opus-4-8 反复 422 事故的解锁形状：ZCode 固定载荷
	// （tools ~225KB + system ≈ 70k token）+ 会话历史 ≈ 109k 估计值在旧
	// 100k 预算下被拒且 fit 不可行，新预算 168k 放行。
	small := []byte(`{"model":"m","max_tokens":32000,"messages":[{"role":"user","content":"hi"}]}`)
	got, err = Check(small, Spec{ContextWindow: 200000})
	if err != nil || got.Budget.InputTokens != 168000 {
		t.Fatalf("sub-half ask must free the unsubtracted window (r1050), got %+v, %v", got.Budget, err)
	}
	// 无 max_tokens：全窗可用（仍受 MaxInputTokens 全局帽；估不准的残余归
	// 上游 context_length 错误 + 网关恢复链处理——2026-11-10 分层执法哲学）。
	none := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	got, err = Check(none, Spec{ContextWindow: 200000})
	if err != nil || got.Budget.InputTokens != 200000 {
		t.Fatalf("no explicit ask frees the full window (r1050), got %+v, %v", got.Budget, err)
	}
	// 桶侧配置（spec 直配）不走封顶：128k 窗口配 100k 预留 ⇒ 27k−margin。
	confBudget, err := Resolve(Spec{ContextWindow: 128000, OutputReserve: 100000, SafetyMargin: 1000})
	if err != nil || confBudget.InputTokens != 27000 {
		t.Fatalf("operator-configured reserve must keep full subtraction, got %+v, %v", confBudget, err)
	}
	// 事故形状的终态判据：估计 ~76.8k 的事故请求在新预算下必须**放行**
	//（旧预算 72000 会以 TokensExceeded 拒绝）。
	admitted := []byte(`{"model":"claude-sonnet-5","max_tokens":128000,"messages":[{"role":"user","content":"` +
		strings.Repeat("word ", 53800) + `"}]}`)
	if _, err := Check(admitted, Spec{ContextWindow: 200000}); err != nil {
		t.Fatalf("incident-shaped body (~77k est tokens) must be admitted under half-window budget: %v", err)
	}
}

func TestTextProtocolsAreCountedConservatively(t *testing.T) {
	// 2026-11-10：默认计数器换成校准估计器（chars/3.5，blob bytes/5）。
	// 旧断言 Tokens >= len(body) 钉的是「字节=token」上界——对 CJK 高估
	// ~3x，正是 422 事故的执法误差来源。现断言估计值落在标定区间：
	// 不低于 runes/7（防低估），不超过字节总数（防高估失控）。
	cjk := `{"messages":[{"role":"user","content":"` + strings.Repeat("你好", 3000) + `"}]}`
	ascii := `{"messages":[{"role":"user","content":"` + strings.Repeat("x", 9000) + `"}]}`
	for _, tc := range []struct {
		body   string
		lo, hi int
	}{
		{cjk, 3000, len(cjk)},
		{ascii, 1200, len(ascii)},
	} {
		got, err := Check([]byte(tc.body), Spec{ContextWindow: 128000})
		if err != nil || got.Tokens < tc.lo || got.Tokens > tc.hi {
			t.Fatalf("estimate out of calibrated band: got=%d band=[%d,%d] err=%v", got.Tokens, tc.lo, tc.hi, err)
		}
	}
	for _, body := range []string{
		`{"messages":[{"role":"system","content":"never erase"},{"role":"user","content":"你好👋"}],"tools":[{"type":"function","function":{"name":"file","parameters":{"properties":{"image_url":{"type":"string"}}}}}]}`,
		`{"system":"system","messages":[{"role":"user","content":[{"type":"text","text":"你好"}]}]}`,
		`{"instructions":"rules","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`,
		`{"systemInstruction":{"parts":[{"text":"rules"}]},"contents":[{"role":"user","parts":[{"text":"hello"}]}],"generationConfig":{"maxOutputTokens":2048}}`,
		`{"prompt":"hello","images":[]}`,
	} {
		got, err := Check([]byte(body), Spec{ContextWindow: 128000})
		if err != nil || got.Tokens <= 0 {
			t.Fatalf("protocol scaffolding under-counted: %+v, %v", got, err)
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
	if err != nil || got.Tokens < 1000+len(body) || got.Counter != "calibrated_json_estimator" {
		t.Fatalf("trusted external count must stay in the advisory receipt: %+v, %v", got, err)
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
