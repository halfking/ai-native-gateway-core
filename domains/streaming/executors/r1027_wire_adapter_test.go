package executors

// r1027（2026-10-14）：native Responses 透传腿的 chat 风格参数线格式
// 适配判据。
//
// 事故形态（raw-logs 2026-10-14T01:03-01:05 四条，grok-4.6 经 vapeur，
// 上游逐条点名 "moved to ..."）：
//
//	"Unsupported parameter: 'reasoning_effort'. In the Responses API,
//	 this parameter has moved to 'reasoning.effort'."
//	"'response_format' … moved to 'text.format'."
//	"'max_completion_tokens' … moved to 'max_output_tokens'."
//	"Unknown parameter: 'logprobs'."
//
// 这些是网关侧线格式适配缺口（IR 转换路径正确、native 透传腿缺失），
// 不是供应商错误。
import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/identity"
)

// r1027CapturedRespLegBodies 取自 raw-logs 捕获的 battery resp 腿请求
// 原文（四条失败形态各一）。
var r1027CapturedRespLegBodies = []string{
	`{"model":"grok-4.6","stream":true,"reasoning_effort":"xhigh","input":[{"role":"user","content":"回复 ok"}]}`,
	`{"model":"grok-4.6","stream":true,"response_format":{"type":"json_object"},"input":[{"role":"user","content":"回复 ok"}]}`,
	`{"model":"grok-4.6","stream":true,"max_completion_tokens":1000,"input":[{"role":"user","content":"回复 ok"}]}`,
	`{"model":"grok-4.6","stream":true,"logprobs":true,"top_logprobs":3,"input":[{"role":"user","content":"回复 ok"}]}`,
}

// TestR1027NativeLegAdaptsChatStyleParams：executeOpenAI native 腿同层
// 驱动——四条真实捕获形态出站前必须完成线格式适配。
func TestR1027NativeLegAdaptsChatStyleParams(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantSub    string // 适配后的在场证据
		wantGone   string // chat 形态键必须消失
		wantAbsent string // 适配产物不应误增的键（负向守卫）
	}{
		{"reasoning_effort→reasoning.effort", r1027CapturedRespLegBodies[0], `"reasoning":{"effort":"`, `"reasoning_effort"`, ""},
		{"response_format→text.format", r1027CapturedRespLegBodies[1], `"text":{"format":`, `"response_format"`, ""},
		{"max_completion_tokens→max_output_tokens", r1027CapturedRespLegBodies[2], `"max_output_tokens":1000`, `"max_completion_tokens"`, ""},
		{"logprobs 系删除", r1027CapturedRespLegBodies[3], `"input"`, `"logprobs"`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var upstreamBody string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				buf := make([]byte, 1<<18)
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
				ResponsesBodyBytes: []byte(tc.body),
				ClientProtocol:     "openai-responses",
				ClientModel:        "grok-4.6",
				ClientID:           identity.ClientIdentity{IdentityHash: "r1027-adapt"},
			}, verifiedVapeurCandidate(upstream.URL, "grok-4.6"), 0, time.Now(), nil)
			if err != nil {
				t.Fatalf("executeOpenAI() error = %v", err)
			}
			if !strings.Contains(upstreamBody, tc.wantSub) {
				t.Fatalf("adapted marker %q missing in upstream body: %s", tc.wantSub, upstreamBody[:min(len(upstreamBody), 250)])
			}
			if strings.Contains(upstreamBody, tc.wantGone) {
				t.Fatalf("chat-style key %q still on responses wire: %s", tc.wantGone, upstreamBody[:min(len(upstreamBody), 250)])
			}
			if tc.wantAbsent != "" && strings.Contains(upstreamBody, tc.wantAbsent) {
				t.Fatalf("unexpected key %q in upstream body: %s", tc.wantAbsent, upstreamBody[:min(len(upstreamBody), 250)])
			}
		})
	}
}

// TestR1027AdapterDoesNotOverrideExplicitTargetForm：客户端已用目标形态
// （reasoning/text/max_output_tokens 在场）时不覆盖——显式形态优先，
// chat 形态冗余键仅删除。
func TestR1027AdapterDoesNotOverrideExplicitTargetForm(t *testing.T) {
	var upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1<<18)
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
		ResponsesBodyBytes: []byte(`{"model":"grok-4.6","stream":false,"reasoning":{"effort":"high"},"reasoning_effort":"low","input":[{"role":"user","content":"回复 ok"}]}`),
		ClientProtocol:     "openai-responses",
		ClientModel:        "grok-4.6",
		ClientID:           identity.ClientIdentity{IdentityHash: "r1027-nooverride"},
	}, verifiedVapeurCandidate(upstream.URL, "grok-4.6"), 0, time.Now(), nil)
	if err != nil {
		t.Fatalf("executeOpenAI() error = %v", err)
	}
	if !strings.Contains(upstreamBody, `"effort":"high"`) {
		t.Fatalf("explicit reasoning.effort=high was overridden: %s", upstreamBody[:min(len(upstreamBody), 250)])
	}
	if strings.Contains(upstreamBody, `"reasoning_effort"`) {
		t.Fatalf("redundant chat-form key survived: %s", upstreamBody[:min(len(upstreamBody), 250)])
	}
}
