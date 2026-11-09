package executors

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/provider/catalog"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

// 2026-11-10 审计 P1-A 的回归门：**接线点**测试。
//
// ★ 为什么必须是接线点测试（而不是只测 ValidateOpenAIChatRequest 这个
// 纯函数）：本门的判据是 `ClientProtocol` 的**字符串口径**，而
// internal/protocolformat/validate_test.go 只测纯函数 ⇒ 口径差永远抓不到。
//
// ★ 缺陷形态：门比较 `params.ClientProtocol == "openai-completions"`
// （catalog 枚举），而生产唯一来源 handler.go:4594-4598 的
// ir.DetectProtocol(bodyBytes) 对 chat 体返回 **"openai-chat"**
// （internal/ir/types.go:33）⇒ 生产（IR 开启，默认）下该门**恒不触发**。
//
// ★ 判据：同一个畸形请求，在「生产形态的 ClientProtocol（openai-chat）」
// 下必须被门拦下并回 4xx；在修复前它会**穿过门**去打上游。
func TestExecutor_OpenAIChatFormatGateFiresUnderIRDetectedProtocol(t *testing.T) {
	var upstreamHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer srv.Close()

	// ★ 生产形态：IR 开启 ⇒ ClientProtocol 由 ir.DetectProtocol 判定。
	// 一个 chat 形状的 body ⇒ "openai-chat"。
	body := []byte(`{"model":"m","messages":[{"role":"not-a-legal-role","content":"hi"}]}`)
	detected, _, err := ir.DetectProtocol(body)
	if err != nil {
		t.Fatalf("DetectProtocol: %v", err)
	}
	if detected != ir.ProtocolOpenAIChat {
		t.Fatalf("DetectProtocol(chat body) = %q, want %q — the premise of this gate changed", detected, ir.ProtocolOpenAIChat)
	}

	e := newTestExecutor()
	e.UpstreamTimeout = 5 * time.Second
	params := &ExecParams{
		R:              httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("{}")),
		W:              httptest.NewRecorder(),
		BodyBytes:      body,
		IsStream:       false,
		ClientModel:    "m",
		OutboundModel:  "m",
		Model:          "m",
		RequestID:      "format-gate-wiring",
		ClientProtocol: detected, // ★ 生产形态
	}
	cand := provider.Candidate{ProviderID: 14, CredentialID: 21, RawModel: "m", BaseURL: srv.URL, Protocol: catalog.ProtocolOpenAICompletions, APIKey: "k"}

	_, execErr := e.executeOpenAI(params, cand, 1, time.Now(), nil)
	if execErr == nil {
		t.Fatal("the format gate must reject a malformed chat body under the IR-detected protocol")
	}
	if upstreamHits != 0 {
		t.Fatalf("upstream was called %d times — the gate let a malformed body through", upstreamHits)
	}
	// 2026-11-10 审计 P1-B：请求侧错误必须带 4xx 状态码（否则客户端收到
	// 502/503「上游过载」⇒ SDK 对一个永久畸形的请求退避重试）。
	var ue *upstreampkg.Error
	if !errors.As(execErr, &ue) {
		t.Fatalf("expected an upstream error carrying the status code, got %T: %v", execErr, execErr)
	}
	if ue.StatusCode != http.StatusBadRequest {
		t.Fatalf("client-side format rejection StatusCode = %d, want %d (400)", ue.StatusCode, http.StatusBadRequest)
	}
}
