package mockprobe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/auth"
	"github.com/kaixuan/llm-gateway-go/internal/providers/mock"
)

// R64（2026-09-25）：探测请求必须自标来源（X-LLM-Origin-Stage/Actor）。
// 探测走网关自身 /mock/v1/* 端点（不写 request_logs，见 R63），本打标是
// 防御性/语义自洽性质；本测试锁定头的存在与取值，防未来回归。
func TestClientProbeSendsOriginHeaders(t *testing.T) {
	var gotStage, gotActor, gotAuth string
	sawRequest := false
	mux := http.NewServeMux()
	mux.Handle("/mock/v1/chat/completions/fast", auth.MockEndpoint(mock.CodeFast, func(w http.ResponseWriter, r *http.Request) {
		sawRequest = true
		gotStage = r.Header.Get("X-LLM-Origin-Stage")
		gotActor = r.Header.Get("X-LLM-Origin-Actor")
		gotAuth = r.Header.Get("Authorization")
		mock.ChatCompletionsFast()(w, r)
	}))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL)
	res := c.Probe(context.Background(), mock.CodeFast, false)
	if !res.OK {
		t.Fatalf("probe failed: %+v", res)
	}
	if !sawRequest {
		t.Fatal("probe request never reached the handler")
	}
	if gotStage != OriginStageSelfCheck {
		t.Fatalf("X-LLM-Origin-Stage = %q, want %q", gotStage, OriginStageSelfCheck)
	}
	if gotActor != OriginActorMockProbeClient {
		t.Fatalf("X-LLM-Origin-Actor = %q, want %q", gotActor, OriginActorMockProbeClient)
	}
	if gotAuth != "Bearer "+auth.MockProbeClientToken {
		t.Fatalf("Authorization = %q, want Bearer %q", gotAuth, auth.MockProbeClientToken)
	}
}
