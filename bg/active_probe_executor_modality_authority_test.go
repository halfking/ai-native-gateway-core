// Package bg — active_probe_executor_modality_authority_test.go
//
// 2026-11-02（模态权威源轮）钉测：**网关轮（RunGateway）的探针形态与回包
// 校验必须跟着「权威模态」走，不是跟着「模型名」或「请求协议」走**。
//
// ★ 两条缺陷（同一族，都是「只换了一半」）：
//
//	F26：形态只从**模型名**推导 ⇒ bge-m3 / e5-large / m3e-base 这类名字里
//	     没有 embedding 字样的 embedding 模型仍拿 chat 形态 ⇒ 打
//	     /v1/chat/completions ⇒ 上游 400 ⇒ **健康**绑定被记 http_400，
//	     且直连腿与网关腿对同一个 (凭据,模型) 给出**相反**结论。
//	F25：回包用 **chat 的 wire** 校验 ⇒ embeddings 的
//	     {"object":"list","data":[{"embedding":[…]}]} 恒被读成「无输出」
//	     ⇒ 每次都记 gateway_err_code='probe_response_inconclusive'
//	     ⇒ 管理面探针看板对**健康** embedding 绑定显示网关失败。
//
// ★ 权威模态 = models_canonical.modality（bg/node_probe.go 的直连腿
//
//	resolveDirectTarget 在同一条 JOIN 里就带着它；本轮给
//	ActiveProbeExecutor.LoadTarget 也带上，**不额外花一次查询**）。
//
// ★ 口径（与直连腿同源）：DB 的**正向**模态（embedding / audio）覆盖名推断；
//
//	DB 说 text/空 ⇒ 用名推断兜底（effectiveProbeModality）。
package bg

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRunGatewayEmbeddingUsesAuthoritativeModality 钉 F26：
// 权威模态是 embedding（模型名里**没有** embedding 字样）⇒ 网关轮必须
// 打 /embeddings 端点、发 {"model","input"} 载荷。
func TestRunGatewayEmbeddingUsesAuthoritativeModality(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotPath = r.URL.Path
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"embedding":[0.1,0.2]}]}`))
	}))
	defer srv.Close()

	e := &ActiveProbeExecutor{}
	e.SetGateway(srv.URL, "test-key", srv.Client())

	// 模型名里没有 "embed" 字样 ⇒ 只能靠权威模态才能判成 embedding。
	res := e.RunGateway(t.Context(), &ProbeTarget{
		CredentialID: 9, ProviderID: 1,
		RawModel: "bge-m3", Protocol: "openai-completions",
		Modality: "embedding",
	})
	if res == nil || res.Status != ProbeStatusSuccess {
		t.Fatalf("RunGateway(embedding, 权威模态) status = %+v, want success", res)
	}
	if !strings.HasSuffix(gotPath, "/embeddings") {
		t.Errorf("网关轮打的是 %q，want /embeddings（形态没跟权威模态走）", gotPath)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("gateway probe body is not JSON: %q", gotBody)
	}
	if _, ok := payload["input"]; !ok {
		t.Errorf("embedding 探针载荷缺 input 键: %q", gotBody)
	}
	if _, ok := payload["messages"]; ok {
		t.Errorf("embedding 探针载荷仍是 chat 形态: %q", gotBody)
	}
}

// TestRunGatewayEmbeddingResponseValidatedWithEmbeddingsWire 钉 F25：
// 回包必须用 **embeddings 的 wire** 校验 ⇒ 健康响应判 ok，而不是
// 「无输出」（probe_response_inconclusive）。
//
// ★ 回归形态：旧实现把协议参数写死成 "chat" ⇒ embeddings 的
//
//	{"object":"list","data":[…]} 在 chat 语义下恒为 FormatUnknown ⇒
//	每一次 embedding 网关探针都记 gateway_err_code='probe_response_inconclusive'
//	/ last_gateway_ok=false ⇒ 管理面看板对**健康**绑定显示网关失败。
func TestRunGatewayEmbeddingResponseValidatedWithEmbeddingsWire(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"embedding":[0.1,0.2,0.3]}]}`))
	}))
	defer srv.Close()

	e := &ActiveProbeExecutor{}
	e.SetGateway(srv.URL, "test-key", srv.Client())

	res := e.RunGateway(t.Context(), &ProbeTarget{
		CredentialID: 9, ProviderID: 1,
		RawModel: "bge-m3", Protocol: "openai-completions",
		Modality: "embedding",
	})
	if res == nil {
		t.Fatal("RunGateway returned nil")
	}
	if res.Status == ProbeStatusSkipped && strings.Contains(res.ErrMsg, "no complete assistant") {
		t.Fatalf("健康 embeddings 响应被判成「无输出」(inconclusive): status=%q err=%q "+
			"⇒ 校验用的 wire 没跟着发的形态走", res.Status, res.ErrMsg)
	}
	if res.Status != ProbeStatusSuccess {
		t.Fatalf("RunGateway(embeddings) status = %q err=%q, want success", res.Status, res.ErrMsg)
	}
}

// TestRunGatewayTextModelKeepsChatWire 钉「没有把 chat 侧改坏」：
// 文本模型仍走 chat 端点 + chat wire（既有行为不得变）。
func TestRunGatewayTextModelKeepsChatWire(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"probe","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer srv.Close()

	e := &ActiveProbeExecutor{}
	e.SetGateway(srv.URL, "test-key", srv.Client())

	res := e.RunGateway(t.Context(), &ProbeTarget{
		CredentialID: 8, ProviderID: 1,
		RawModel: "gpt-5.6-terra", Protocol: "openai-completions",
		Modality: "text",
	})
	if res == nil || res.Status != ProbeStatusSuccess {
		t.Fatalf("RunGateway(text) status = %+v, want success", res)
	}
	if strings.HasSuffix(gotPath, "/embeddings") {
		t.Errorf("文本模型被打到 %q —— text 不该覆盖名推断（口径：正向模态才覆盖）", gotPath)
	}
}

// TestRunGatewayModalityFallsBackToModelName 钉「权威模态取不到时的兜底」：
// Modality 为空 ⇒ 退回按模型名推断 ⇒ 名字里带 embedding 的模型仍走
// embedding 端点（探针形态永远有兜底，不会因为一次查询失败就判错）。
func TestRunGatewayModalityFallsBackToModelName(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"embedding":[0.1,0.2]}]}`))
	}))
	defer srv.Close()

	e := &ActiveProbeExecutor{}
	e.SetGateway(srv.URL, "test-key", srv.Client())

	res := e.RunGateway(t.Context(), &ProbeTarget{
		CredentialID: 9, ProviderID: 1,
		RawModel: "snowflake/arctic-embed-l", Protocol: "openai-completions",
		// Modality 留空 = 权威模态没取到
	})
	if res == nil || res.Status != ProbeStatusSuccess {
		t.Fatalf("RunGateway(名推断兜底) status = %+v, want success", res)
	}
	if !strings.HasSuffix(gotPath, "/embeddings") {
		t.Errorf("名推断兜底没生效，打的是 %q", gotPath)
	}
}
