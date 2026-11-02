// Package bg — credential_probe_modality_scope_test.go
//
// 2026-11-02（模态权威源轮）钉测：**凭据级探针不得用自己的探针把整个凭据
// 判死**。
//
// ★ 事故形态（F27）：
//
//	凭据的探针模型是 embedding（credentials.default_probe_model 指向它，
//	或它为空而 fallbackProbeModel 取到字典序最小的绑定模型）
//	⇒ 凭据探针发 **chat 形态** {"model","messages"}
//	⇒ 上游 400（Ollama 实测 "does not support chat"）
//	⇒ classifyProbeFailure 的 default 分支
//	⇒ HealthStatus/AvailabilityState = "unreachable" + 5 分钟 recover
//	⇒ writeHealth 改**凭据级**状态
//	⇒ **整凭据不可路由 ⇒ 该凭据上所有模型对所有租户 503**
//
// ★ 本轮两件事：
//
//	① 形态：凭据探针按**权威模态**选形态（embedding ⇒ /embeddings +
//	   {"model","input"}），回包用 embeddings 的 wire 校验；
//	② 兜底：上游在生成通道上回了请求侧 4xx ⇒ 只记观察，**不改凭据状态**
//	   （ObservationOnly）——即便形态仍不匹配，也不会连坐兄弟模型。
package bg

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCredentialProbeUsesEmbeddingsShape 钉 ① 的**判据**：探针模型是
// embedding（按权威模态或名推断）⇒ 凭据探针改用 embeddings 形态；
// 文本模型 ⇒ 仍是 chat 形态；messages / responses 协议 ⇒ 与模态无关。
func TestCredentialProbeUsesEmbeddingsShape(t *testing.T) {
	cases := []struct {
		name       string
		probeModel string
		modality   string
		protocol   string
		want       bool
	}{
		{"权威模态 embedding（名字无 embed 字样）", "bge-m3", "embedding", "openai-completions", true},
		{"权威模态 embedding（ollama 形态）", "qwen3-embedding:0.6b", "embedding", "openai-completions", true},
		{"名推断兜底（名字含 embed）", "snowflake/arctic-embed-l", "", "openai-completions", true},
		{"文本模型（权威 text 不覆盖名推断）", "gpt-5.6-terra", "text", "openai-completions", false},
		{"文本模型", "qwen2.5:7b", "", "openai-completions", false},
		{"messages 协议与模态无关", "bge-m3", "embedding", "anthropic-messages", false},
		{"responses 协议与模态无关", "bge-m3", "embedding", "openai-responses", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := credentialProbeUsesEmbeddings(tc.probeModel, tc.modality, tc.protocol)
			if got != tc.want {
				t.Fatalf("credentialProbeUsesEmbeddings(%q, %q, %q) = %v, want %v",
					tc.probeModel, tc.modality, tc.protocol, got, tc.want)
			}
		})
	}
}

// TestCredentialProbeModalityScope 钉 ②：
// 生成通道上的请求侧 4xx 不得摘掉整凭据。
func TestCredentialProbeRequestRejectionIsNotCredentialWide(t *testing.T) {
	cases := []struct {
		name string
		msg  string
	}{
		{"chat 400", `chat status 400: {"error":{"message":"does not support chat","type":"invalid_request_error"}}`},
		{"chat 422", `chat status 422: {"error":{"message":"unprocessable"}}`},
		{"responses 400", `responses status 400: {"error":{"message":"Unsupported parameter"}}`},
		{"messages 400", `messages status 400: {"error":{"message":"bad request"}}`},
		{"embeddings 404", `embeddings status 404: {"error":"not_found"}`},
		{"wrapped chat 400", `chat unreachable (after 1 attempts): chat status 400: {"error":{"message":"x"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pr := classifyProbeFailure(tc.msg)
			if pr.AvailabilityState == "unreachable" {
				t.Fatalf("请求侧 4xx 把凭据判成 unreachable ⇒ 该凭据上所有模型 503：%q => %+v", tc.msg, pr)
			}
			if !pr.ObservationOnly {
				t.Fatalf("请求侧 4xx 不是 observation-only ⇒ 会写绑定/凭据状态：%q => %+v", tc.msg, pr)
			}
			if pr.HealthStatus != "warning" {
				t.Fatalf("HealthStatus = %q, want warning", pr.HealthStatus)
			}
		})
	}
}

// TestCredentialProbeCredentialScopedChannelsUnchanged 钉「凭据级通道没被
// 一起放过」：
//
//	· /v1/models 通道的 404 = provider 拒绝为该账号枚举任何模型
//	  （鉴权吊销 / scope 撤销）⇒ **凭据级**，不得当成请求侧；
//	· chat 通道的裸 404 = 端点路径错 ⇒ 该凭据上所有模型都探不通
//	  ⇒ **凭据级**（既有裁决，行为不动）；
//	· 401/403 仍是鉴权（凭据级）。
func TestCredentialProbeCredentialScopedChannelsUnchanged(t *testing.T) {
	cases := []struct {
		name string
		msg  string
	}{
		{"models endpoint 404", `models endpoint unreachable (after 1 attempts): status 404: {"error":"not_found"}`},
		{"chat generic 404", `chat status 404: {"error":"not_found"}`},
		{"chat 401", `chat status 401: {"error":{"message":"unauthorized"}}`},
		{"chat 403 ambiguous", `chat status 403: {"error":{"message":"Forbidden"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pr := classifyProbeFailure(tc.msg)
			if pr.ObservationOnly {
				t.Fatalf("凭据级证据被当成请求侧放过 ⇒ 该凭据的兄弟模型被连坐：%q => %+v", tc.msg, pr)
			}
			if pr.AvailabilityState == "ready" {
				t.Fatalf("凭据级证据被降级成 ready ⇒ 凭据级处置丢失：%q => %+v", tc.msg, pr)
			}
		})
	}
}

// TestCredentialProbeAuthBodyWinsOverRequestRejection 钉「凭据级证据优先」：
// 部分中转用 400 报 "Incorrect API key provided" ⇒ 那是鉴权失败，不是
// 请求侧拒绝。
func TestCredentialProbeAuthBodyWinsOverRequestRejection(t *testing.T) {
	msg := `chat status 400: {"error":{"message":"Incorrect API key provided"}}`
	pr := classifyProbeFailure(msg)
	if pr.ObservationOnly {
		t.Fatalf("400 + key 无效正文被当成请求侧 ⇒ 凭据级鉴权失败被放过：%+v", pr)
	}
}

// TestMiniEmbeddingUsesEmbeddingsShapeAndWire 钉 ①：embeddings 腿发
// {"model","input"} 到 /embeddings，并用 embeddings 的 wire 校验回包。
func TestMiniEmbeddingUsesEmbeddingsShapeAndWire(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		buf := make([]byte, 512)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"embedding":[0.1,0.2]}]}`))
	}))
	defer srv.Close()

	c := &CredentialProbeV2{}
	ok, msg := c.miniEmbedding(t.Context(), srv.Client(), "sk-test", "bge-m3", srv.URL+"/embeddings")
	if !ok {
		t.Fatalf("miniEmbedding failed: %q", msg)
	}
	if !strings.HasSuffix(gotPath, "/embeddings") {
		t.Errorf("embeddings 腿打的是 %q", gotPath)
	}
	if !strings.Contains(gotBody, `"input"`) {
		t.Errorf("embeddings 载荷缺 input 键: %q", gotBody)
	}
	if strings.Contains(gotBody, "messages") {
		t.Errorf("embeddings 载荷仍是 chat 形态: %q", gotBody)
	}
}

// TestMiniEmbeddingHTTPFailureKeepsChannel 钉「embeddings 腿的失败带自己的
// 通道名」：分类层据此区分「embeddings 端点不提供」（不摘整凭据）与
// 「chat 端点路径错」（凭据级）。
func TestMiniEmbeddingHTTPFailureKeepsChannel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		fmt.Fprint(w, `{"error":"not_found"}`)
	}))
	defer srv.Close()

	c := &CredentialProbeV2{}
	ok, msg := c.miniEmbedding(t.Context(), srv.Client(), "sk-test", "bge-m3", srv.URL+"/embeddings")
	if ok {
		t.Fatal("404 必须算失败")
	}
	if !strings.HasPrefix(msg, "embeddings status 404:") {
		t.Fatalf("embeddings 腿的失败没带通道名: %q", msg)
	}
	// 分类：不得摘掉整凭据。
	pr := classifyProbeFailure(msg)
	if pr.AvailabilityState == "unreachable" || !pr.ObservationOnly {
		t.Fatalf("embeddings 端点 404 被当成凭据级 ⇒ 该凭据上所有模型 503：%+v", pr)
	}
}

// TestProbeModelModalityFallsBackWhenUnavailable 钉「权威模态取不到时的
// 兜底」：DB 不可用 ⇒ 返回 "" ⇒ 调用方按名推断，探针形态永远有兜底。
func TestProbeModelModalityFallsBackWhenUnavailable(t *testing.T) {
	c := &CredentialProbeV2{}
	if got := c.probeModelModality(context.Background(), 1, "bge-m3"); got != "" {
		t.Fatalf("无 DB 时 probeModelModality = %q, want \"\"", got)
	}
}
