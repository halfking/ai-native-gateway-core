package executors

// r1026b（2026-10-13 审计轮）：native Responses 透传腿的 stream_options
// 泄漏判据。
//
// 事故形态（raw-logs 2026-10-13T19:36:12 实证，request_id 13c96a5d 同族）：
// responses 客户端在 /v1/responses 体里带 stream_options.include_usage →
// 原生透传体原样送达 vapeur → 400 "Unknown parameter:
// 'stream_options.include_usage'" → response.failed provider_unavailable。
// extensions_restore 的 r1026 守卫只覆盖 IR 序列化路径；native 腿保留
// 客户端原文，不经 IR —— 这是第三个 choke point。
//
// 判据纪律：同层驱动（executeOpenAI 的 native 分支 + httptest 假上游），
// 请求体取 raw-logs 捕获的真实形状；前提在场（ResponsesBodyBytes 必须含
// stream_options，否则「没看见」假绿）。
import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/identity"
)

// r1026bCapturedResponsesBody 是 raw-logs（2026-10-13T19:36:12）捕获的
// responses 客户端 client_request 原文（携 stream_options），原样回放。
const r1026bCapturedResponsesBody = `{"model":"grok-4.6","stream":true,"stream_options":{"include_usage":true},"input":[{"role":"user","content":"回复 ok"}]}`

func TestR1026BNativeResponsesLegStripsStreamOptions(t *testing.T) {
	if !strings.Contains(r1026bCapturedResponsesBody, `"stream_options"`) {
		t.Fatalf("premise missing: captured body must contain stream_options")
	}

	var upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1<<16)
		n, _ := r.Body.Read(buf)
		upstreamBody = strings.TrimSpace(string(buf[:n]))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(upstreamResponsesOK))
	}))
	defer upstream.Close()

	clientRec := httptest.NewRecorder()
	_, err := newOverloadTestExecutor().executeOpenAI(&ExecParams{
		R:                  httptest.NewRequest(http.MethodPost, "/v1/responses", nil),
		W:                  clientRec,
		BodyBytes:          []byte(`{"model":"grok-4.6","messages":[{"role":"user","content":"回复 ok"}],"stream":false}`),
		ResponsesBodyBytes: []byte(r1026bCapturedResponsesBody),
		ClientProtocol:     "openai-responses",
		ClientModel:        "grok-4.6",
		ClientID:           identity.ClientIdentity{IdentityHash: "r1026b-native-strip"},
	}, verifiedVapeurCandidate(upstream.URL, "grok-4.6"), 0, time.Now(), nil)
	if err != nil {
		t.Fatalf("executeOpenAI() error = %v", err)
	}

	if strings.Contains(upstreamBody, "stream_options") {
		t.Fatalf("native responses upstream body still carries stream_options: %s", upstreamBody)
	}
	if !strings.Contains(upstreamBody, `"input"`) {
		t.Fatalf("upstream body lost the input payload (over-strip): %s", upstreamBody)
	}
	if !strings.Contains(upstreamBody, `"grok-4.6"`) {
		t.Fatalf("upstream body lost the model name: %s", upstreamBody)
	}
}

// TestR1026BReactiveRetryStripsStreamOptions：responses 客户端、能力未知
// （无 proactive 升级）→ chat 腿先被上游以「requires the responses api」
// 拒 → reactive 重试腿换 /v1/responses。重试体来自 ResponsesBodyBytes
// （客户端原文），必须同样过 stream_options strip（r1026b 一致性补齐：
// 与 native 分支同源同治，否则重试腿绕过 choke point）。
//
// 判据纪律：顶层键 JSON 判定（非字符串包含——正文提及不算泄漏，
// 2026-10-13 晚 raw-logs 复核的假阳性教训）。
func TestR1026BReactiveRetryStripsStreamOptions(t *testing.T) {
	var bodies []string
	var paths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1<<18)
		n, _ := r.Body.Read(buf)
		bodies = append(bodies, strings.TrimSpace(string(buf[:n])))
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"This model requires the Responses API. Use the Responses API.","code":4006}}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(upstreamResponsesOK))
		}
	}))
	defer upstream.Close()

	// 能力未知（SupportsNativeResponsesKnown=false）→ 不做 proactive，
	// chat 腿先行，才会触发 reactive responses 重试。
	cand := verifiedVapeurCandidate(upstream.URL, "grok-4.6")
	cand.SupportsNativeResponses = false
	cand.SupportsNativeResponsesKnown = false
	cand.SupportsNativeResponsesStream = false

	clientRec := httptest.NewRecorder()
	_, err := newOverloadTestExecutor().executeOpenAI(&ExecParams{
		R:                  httptest.NewRequest(http.MethodPost, "/v1/responses", nil),
		W:                  clientRec,
		BodyBytes:          []byte(`{"model":"grok-4.6","messages":[{"role":"user","content":"回复 ok"}],"stream":false}`),
		ResponsesBodyBytes: []byte(r1026bCapturedResponsesBody),
		ClientProtocol:     "openai-responses",
		ClientModel:        "grok-4.6",
		ClientID:           identity.ClientIdentity{IdentityHash: "r1026b-reactive-strip"},
	}, cand, 0, time.Now(), nil)
	if err != nil {
		t.Fatalf("executeOpenAI() error = %v", err)
	}
	if len(paths) < 2 || !strings.HasSuffix(paths[len(paths)-1], "/responses") {
		t.Fatalf("upstream paths = %v, want chat attempt then /responses retry", paths)
	}
	retryBody := bodies[len(bodies)-1]
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(retryBody), &obj); err != nil {
		t.Fatalf("retry body not JSON: %s", retryBody[:min(len(retryBody), 300)])
	}
	if _, ok := obj["stream_options"]; ok {
		t.Fatalf("reactive retry body carries top-level stream_options: %s", retryBody[:min(len(retryBody), 300)])
	}
	if _, ok := obj["input"]; !ok {
		t.Fatalf("retry body lost input payload (over-trim): %s", retryBody[:min(len(retryBody), 300)])
	}
}
