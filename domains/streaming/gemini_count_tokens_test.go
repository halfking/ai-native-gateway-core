package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// ★★★ 本文件钉住的是 2026-10-29「Gemini countTokens 轮」的核心契约：
// **回答的是「网关真正会发出去的那份文本」有多少 token**，而不是
// 「客户端原文」有多少 token。
//
// ★ 为什么这件事值得用这么多门：上一轮把 :countTokens 登记成「唯一诚实的
// 实现是转发上游让厂商算」（审计档 §17）。本轮把那个理由**推翻了**——
// 转发算的是**客户端原文**，而网关发出去的是**IR 归一后的那份** ⇒
// 转发得到的数字描述的是一个**不会发生的请求** ⇒ ⇒ 于是「口径」成了
// 本功能唯一的真值 ⇒ 口径必须被门钉住，否则下一个人会顺手改成「估原文」
// （看起来更自然、实际上答非所问）。

// countTokensVerifier 是一个**会拒绝错误 key** 的校验器替身。
//
// ★ 仓内既有的 stubKeyVerifier 对任何 key 都返回成功（handoff_confirmation_test.go），
// ⇒ 它钉不住「错误 key 必须被拒」这一条 ⇒ 本文件自带一个会拒的。
type countTokensVerifier struct {
	goodKey string
	info    *authentication.KeyInfo
}

func (v *countTokensVerifier) Enabled() bool { return v != nil && v.info != nil }

func (v *countTokensVerifier) Verify(_ context.Context, rawKey string) (*authentication.KeyInfo, error) {
	if v.goodKey != "" && rawKey != v.goodKey {
		return nil, &authentication.InvalidKeyError{}
	}
	return v.info, nil
}

func (v *countTokensVerifier) VerifyByID(_ context.Context, _ int) (*authentication.KeyInfo, error) {
	return v.info, nil
}

func (v *countTokensVerifier) CheckBudget(_ context.Context, _ int) error { return nil }

func (v *countTokensVerifier) LookupKeyMeta(_ context.Context, _ string) (*authentication.KeyLookupMeta, error) {
	return nil, nil
}

// countTokensHandler builds a GeminiHandler whose ChatHandler carries the
// given verifier (nil = lite mode: no DB verifier).
//
// ★ ChatHandler 里**没有** executor / provider ⇒ ⇒ 若 countTokens 误走
// 生成链路，它会因「无候选」而失败 ⇒ ⇒ 「200」这件事本身就证明了
// countTokens **不碰上游**（见 TestServeCountTokens_DoesNotTouchTheChatPipeline）。
func countTokensHandler(v requestKeyVerifier, staticKey string) *GeminiHandler {
	ch := &ChatHandler{}
	ch.keyVerifier = v
	ch.staticDataPlaneKey = staticKey
	return &GeminiHandler{chatHandler: ch}
}

// countTokensBody is a minimal Gemini-native request body.
func countTokensBody(text string) []byte {
	return []byte(fmt.Sprintf(`{"contents":[{"role":"user","parts":[{"text":%q}]}]}`, text))
}

// postCountTokens issues the request and returns the recorder.
func postCountTokens(h *GeminiHandler, path string, body []byte, setup func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	if setup != nil {
		setup(r)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// decodeGeminiError asserts the response carries the VENDOR error envelope
// ({"error":{"code","message","status"}}) and returns the message.
//
// ★ 为什么门要管错误信封的**形状**：原生客户端（google-genai SDK）按厂商
// 形状解析错误 ⇒ 网关给一个 OpenAI 形状的错误，客户端会在离病因很远的地方
// 炸掉（LESSONS「离病因很远的地方炸掉 = 形状不对」那一族）。
func decodeGeminiError(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var payload struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("error body is not JSON: %v\n%s", err, rec.Body.String())
	}
	if payload.Error.Message == "" {
		t.Fatalf("response is not a Gemini-native error envelope: %s", rec.Body.String())
	}
	if payload.Error.Status == "" {
		t.Errorf("Gemini error envelope has no status name: %s", rec.Body.String())
	}
	if payload.Error.Code != rec.Code {
		t.Errorf("error envelope code = %d, want the HTTP status %d", payload.Error.Code, rec.Code)
	}
	return payload.Error.Message
}

// decodeTotalTokens asserts the response carries the VENDOR success envelope
// ({"totalTokens": N}) and returns N.
func decodeTotalTokens(t *testing.T, rec *httptest.ResponseRecorder) int {
	t.Helper()
	var payload struct {
		TotalTokens *int `json:"totalTokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("response is not JSON: %v\n%s", err, rec.Body.String())
	}
	if payload.TotalTokens == nil {
		t.Fatalf("response has no totalTokens: %s", rec.Body.String())
	}
	return *payload.TotalTokens
}

// TestServeCountTokens_VendorShape pins the success envelope.
//
// ★ The vendor's :countTokens answers {"totalTokens": N} (docs/vendor-formats/
// gemini.md's model-list naming). An OpenAI-shaped body would fail at the
// native client's unmarshal step.
func TestServeCountTokens_VendorShape(t *testing.T) {
	h := countTokensHandler(nil, "")
	rec := postCountTokens(h, "/v1beta/models/gemini-2.5-pro:countTokens", countTokensBody("hello world"), nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	total := decodeTotalTokens(t, rec)
	if total <= 0 {
		t.Errorf("totalTokens = %d, want a positive estimate for a non-empty prompt", total)
	}
}

// ★★★ TestServeCountTokens_EstimatesTheNormalizedBodyNotTheRawBody is the
// gate for the round's central decision.
//
// ★ The gateway sends the IR-NORMALIZED body upstream (Gemini → IR → OpenAI),
// never the client's raw Gemini body. ⇒ The honest count is the one for the
// body that will actually be sent.
//
// ★ The teeth of this gate: estimatePromptTokens over the RAW Gemini body is
// **0** (a Gemini body has no "messages" field ⇒ the estimator finds nothing)
// ⇒ ⇒ a regression that estimates the raw body would answer 0 ⇒ this gate
// goes red immediately.
func TestServeCountTokens_EstimatesTheNormalizedBodyNotTheRawBody(t *testing.T) {
	body := countTokensBody("hello world")

	// The expected value, computed independently through the SAME two steps
	// the handler takes (ParseGemini → SerializeOpenAI) plus the same
	// estimator the handler uses.
	irReq, err := ir.ParseGemini(body)
	if err != nil {
		t.Fatalf("ParseGemini: %v", err)
	}
	normalized, err := ir.SerializeOpenAI(irReq)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	want := estimatePromptTokens(normalized)
	if want <= 0 {
		t.Fatalf("the normalized body estimates to %d — the test premise is broken", want)
	}
	if rawEstimate := estimatePromptTokens(body); rawEstimate != 0 {
		t.Fatalf("the raw Gemini body now estimates to %d (expected 0) — "+
			"this gate no longer discriminates the two口径", rawEstimate)
	}

	h := countTokensHandler(nil, "")
	rec := postCountTokens(h, "/v1beta/models/gemini-2.5-pro:countTokens", body, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := decodeTotalTokens(t, rec); got != want {
		t.Errorf("totalTokens = %d, want %d (the estimate of the NORMALIZED body)", got, want)
	}
}

// TestServeCountTokens_GrowsWithThePrompt is the sanity gate: a longer prompt
// must cost more tokens. (A constant answer would satisfy every other gate.)
func TestServeCountTokens_GrowsWithThePrompt(t *testing.T) {
	h := countTokensHandler(nil, "")
	short := decodeTotalTokens(t, postCountTokens(h, "/v1beta/models/gemini-2.5-pro:countTokens", countTokensBody("hi"), nil))
	long := decodeTotalTokens(t, postCountTokens(h, "/v1beta/models/gemini-2.5-pro:countTokens", countTokensBody(strings.Repeat("token ", 200)), nil))
	if long <= short {
		t.Errorf("a 200x longer prompt estimated %d tokens, not more than the short one (%d)", long, short)
	}
}

// TestServeCountTokens_ServedOnBothVendorPrefixes pins that the GA path
// (/v1/models/{m}:countTokens) serves the action too — the vendor documents
// both prefixes, and the Gemini handler already mounts both.
func TestServeCountTokens_ServedOnBothVendorPrefixes(t *testing.T) {
	h := countTokensHandler(nil, "")
	for _, path := range []string{
		"/v1beta/models/gemini-2.5-pro:countTokens",
		"/v1/models/gemini-2.5-pro:countTokens",
	} {
		rec := postCountTokens(h, path, countTokensBody("hello"), nil)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, body=%s", path, rec.Code, rec.Body.String())
			continue
		}
		if total := decodeTotalTokens(t, rec); total <= 0 {
			t.Errorf("%s: totalTokens = %d, want > 0", path, total)
		}
	}
}

// TestServeCountTokens_RequiresCredential pins the authentication gate.
//
// ★ The endpoint costs the gateway its own compute, so an unauthenticated
// caller must not be able to drive it.
func TestServeCountTokens_RequiresCredential(t *testing.T) {
	v := &countTokensVerifier{goodKey: "sk-good", info: &authentication.KeyInfo{ID: 7, TenantID: "t"}}
	h := countTokensHandler(v, "")

	rec := postCountTokens(h, "/v1beta/models/gemini-2.5-pro:countTokens", countTokensBody("hello"), nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
	}
	decodeGeminiError(t, rec)
}

// TestServeCountTokens_AcceptsGeminiCredentialForms pins that the vendor's
// own credential shapes work on this route too.
//
// ★ A native Gemini client presents its key as "x-goog-api-key" or "?key="
// (docs/vendor-formats/gemini.md §HTTP 形态). If the countTokens route only
// accepted Bearer, the native client would authenticate on generateContent
// and fail here — the "half-fixed" shape this repo already fixed once
// (audit 2026-10-26).
func TestServeCountTokens_AcceptsGeminiCredentialForms(t *testing.T) {
	v := &countTokensVerifier{goodKey: "sk-good", info: &authentication.KeyInfo{ID: 7, TenantID: "t"}}
	h := countTokensHandler(v, "")

	cases := []struct {
		name  string
		setup func(*http.Request)
	}{
		{"x-goog-api-key header", func(r *http.Request) { r.Header.Set("x-goog-api-key", "sk-good") }},
		{"key query parameter", func(r *http.Request) { r.URL.RawQuery = "key=sk-good" }},
		{"bearer header", func(r *http.Request) { r.Header.Set("Authorization", "Bearer sk-good") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postCountTokens(h, "/v1beta/models/gemini-2.5-pro:countTokens", countTokensBody("hello"), tc.setup)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
			}
			if total := decodeTotalTokens(t, rec); total <= 0 {
				t.Errorf("totalTokens = %d, want > 0", total)
			}
		})
	}
}

// TestServeCountTokens_RejectsWrongCredential pins that a wrong key is
// rejected (the verifier's verdict is honored, not swallowed).
func TestServeCountTokens_RejectsWrongCredential(t *testing.T) {
	v := &countTokensVerifier{goodKey: "sk-good", info: &authentication.KeyInfo{ID: 7, TenantID: "t"}}
	h := countTokensHandler(v, "")

	rec := postCountTokens(h, "/v1beta/models/gemini-2.5-pro:countTokens", countTokensBody("hello"),
		func(r *http.Request) { r.Header.Set("Authorization", "Bearer sk-wrong") })
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
	}
	decodeGeminiError(t, rec)
}

// TestServeCountTokens_RejectsNonPost pins the method gate.
//
// ★ The vendor's countTokens is POST-only. A GET on the same path must not
// be silently treated as a count.
func TestServeCountTokens_RejectsNonPost(t *testing.T) {
	h := countTokensHandler(nil, "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1beta/models/gemini-2.5-pro:countTokens", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405 (body=%s)", rec.Code, rec.Body.String())
	}
	decodeGeminiError(t, rec)
}

// TestServeCountTokens_InvalidBodyIsVendorShaped400 pins that a malformed
// body produces the VENDOR error envelope (not an OpenAI-shaped one).
func TestServeCountTokens_InvalidBodyIsVendorShaped400(t *testing.T) {
	h := countTokensHandler(nil, "")
	rec := postCountTokens(h, "/v1beta/models/gemini-2.5-pro:countTokens", []byte("{not json"), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	decodeGeminiError(t, rec)
}

// TestServeCountTokens_StaticKeyFallback pins the lite-mode (no DB verifier)
// fallback: the deployed static data-plane key is required.
//
// ★ Why this gate exists: the sibling action (generateContent) requires the
// static key in lite mode (ChatHandler's staticDataPlaneKey branch). Without
// the same rule here, one deployment would have "generateContent needs the
// static key, countTokens does not" ⇒ a real authentication hole on a route
// that costs the gateway compute.
func TestServeCountTokens_StaticKeyFallback(t *testing.T) {
	// No verifier (lite mode) + a deployed static key.
	h := countTokensHandler(nil, "sk-static")

	rec := postCountTokens(h, "/v1beta/models/gemini-2.5-pro:countTokens", countTokensBody("hello"),
		func(r *http.Request) { r.Header.Set("Authorization", "Bearer sk-wrong") })
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for a wrong static key (body=%s)", rec.Code, rec.Body.String())
	}
	decodeGeminiError(t, rec)

	rec = postCountTokens(h, "/v1beta/models/gemini-2.5-pro:countTokens", countTokensBody("hello"),
		func(r *http.Request) { r.Header.Set("Authorization", "Bearer sk-static") })
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for the correct static key (body=%s)", rec.Code, rec.Body.String())
	}
}

// ★ TestServeCountTokens_DoesNotTouchTheChatPipeline pins that the count
// path never enters the generation pipeline.
//
// ★ The ChatHandler in these tests has NO executor and NO provider ⇒ if the
// countTokens action fell through to the chat path, it would fail with
// "no candidate" (503) instead of answering 200 ⇒ ⇒ the 200 IS the proof.
//
// ★ Why it matters: the generation pipeline would (a) call the upstream,
// (b) charge the caller, (c) produce a response — none of which is what
// "count the tokens" means.
func TestServeCountTokens_DoesNotTouchTheChatPipeline(t *testing.T) {
	h := countTokensHandler(nil, "")
	if h.chatHandler == nil {
		t.Fatal("test premise broken: no chat handler")
	}
	if h.chatHandler.executor != nil {
		t.Fatal("test premise broken: the chat handler has an executor — the gate would not discriminate")
	}
	rec := postCountTokens(h, "/v1beta/models/gemini-2.5-pro:countTokens", countTokensBody("hello"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 without any routing (body=%s)", rec.Code, rec.Body.String())
	}
}
