package executors

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/attachments"
	"github.com/kaixuan/llm-gateway-go/domains/identity"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// MM-1/MM-2 outbound attachment transforms at the EXECUTOR level (doc 20
// E-P2-6 + A 接线): the candidate loop must leave the outbound body
// byte-identical when both multimodal flags are off (default wiring:
// AttachmentURLRewriter == nil && AttachmentURLFetchFallback == nil), and
// must apply the rewrites per-candidate when they are wired on.

// captureServer records every upstream request body.
type captureServer struct {
	srv  *httptest.Server
	mu   sync.Mutex
	body []byte
}

func newCaptureServer() *captureServer {
	cs := &captureServer{}
	cs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		cs.mu.Lock()
		cs.body = b
		cs.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n"))
	}))
	return cs
}

func (cs *captureServer) captured() []byte {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.body
}

// The body already carries stream_options so the legacy stream_options
// injection is a no-op and the multimodal pipeline is the only variable.
const mmFlagOffBody = `{"model":"gpt-5.6-luna","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":[{"type":"text","text":"what is this?"},{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}}]}]}`

// mmLegacyOutboundGolden pins the byte-exact upstream body the LEGACY
// pipeline (no attachment hooks) produces for mmFlagOffBody: the legacy
// transforms re-marshal the body (sorted keys), and E-P2-6 requires the
// flag-off path to stay byte-identical to that legacy output.
const mmLegacyOutboundGolden = `{"messages":[{"content":[{"text":"what is this?","type":"text"},{"image_url":{"url":"data:image/png;base64,QUJD"},"type":"image_url"}],"role":"user"}],"model":"gpt-5.6-luna","stream":true,"stream_options":{"include_usage":true}}`

func mmAttachmentMetadata() []attachments.AttachmentMetadata {
	return []attachments.AttachmentMetadata{
		{MessageIndex: 0, BlockIndex: 1, Path: "2026/08/a1/b2/hash1.png", Hash: "hash1", Status: attachments.AttachmentStatusStored},
	}
}

func mmExecParams(body []byte, cand provider.Candidate) *ExecParams {
	return &ExecParams{
		W:                  httptest.NewRecorder(),
		R:                  httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes:          body,
		IsStream:           true,
		ClientProtocol:     "openai-completions",
		ClientModel:        "gpt-5.6-luna",
		ClientID:           identity.ClientIdentity{IdentityHash: "test"},
		Candidates:         []provider.Candidate{cand},
		Policy:             &provider.Policy{TierFallbackMax: 4, RetryPerCredential: 0},
		AttachmentMetadata: mmAttachmentMetadata(),
	}
}

// E-P2-6 (doc 20): 开关关闭（默认 wiring，rewriter 与 fetch fallback 均为
// nil）时，executor 出站 body 必须与旧管线输出逐字节一致（golden 钉住），
// 多模态 base64 载荷保持原样。
func TestExecuteAttachmentOutbound_FlagOffIsByteIdentical(t *testing.T) {
	cs := newCaptureServer()
	defer cs.srv.Close()

	exec := newOverloadTestExecutor() // both attachment hooks nil = flags off
	cand := overloadTestCandidate(cs.srv.URL)

	if _, err := exec.Execute(mmExecParams([]byte(mmFlagOffBody), cand)); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	got := cs.captured()
	if len(got) == 0 {
		t.Fatal("upstream was never called")
	}
	if !bytes.Equal(got, []byte(mmLegacyOutboundGolden)) {
		t.Fatalf("flag-off outbound body drifted from legacy bytes:\n got: %s\nwant: %s", got, mmLegacyOutboundGolden)
	}
	if !bytes.Contains(got, []byte("data:image/png;base64,QUJD")) {
		t.Fatalf("flag-off must keep the inline base64 payload verbatim, got: %s", got)
	}
}

// anthropicToOpenAIStubForBridge mimics streaming.ConvertAnthropicBodyToOpenAI
// for the multimodal blocks under test (executors tests cannot import the
// streaming package — import cycle). Mapping mirrors convertImageBlock:
// source{type:url} → image_url{url}, source{type:base64} → data URI.
func anthropicToOpenAIStubForBridge(bodyBytes []byte) ([]byte, error) {
	var req struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				Source *struct {
					Type      string `json:"type"`
					URL       string `json:"url"`
					MediaType string `json:"media_type"`
					Data      string `json:"data"`
				} `json:"source"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		return nil, err
	}
	out := map[string]any{"model": req.Model, "stream": req.Stream}
	var msgs []any
	for _, m := range req.Messages {
		var parts []any
		for _, b := range m.Content {
			switch {
			case b.Type == "text":
				parts = append(parts, map[string]any{"type": "text", "text": b.Text})
			case b.Type == "image" && b.Source != nil && b.Source.Type == "url":
				parts = append(parts, map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": b.Source.URL},
				})
			case b.Type == "image" && b.Source != nil && b.Source.Type == "base64":
				parts = append(parts, map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": "data:" + b.Source.MediaType + ";base64," + b.Source.Data},
				})
			}
		}
		msgs = append(msgs, map[string]any{"role": m.Role, "content": parts})
	}
	out["messages"] = msgs
	return json.Marshal(out)
}

// E-P2-3 (doc 20): Anthropic 协议客户端 + URL 型 OpenAI 供应商——
// RewriteAnthropicBody 在桥接转换前把 base64 source 换成网关 URL source，
// 桥接转换后上游收到 image_url 网关 URL（此前是静默 no-op 保 base64）。
func TestExecuteAttachmentOutbound_AnthropicBridgeRewritesToGatewayURL(t *testing.T) {
	cs := newCaptureServer()
	defer cs.srv.Close()

	exec := newOverloadTestExecutor()
	exec.AttachmentURLRewriter = attachments.NewOutboundURLRewriter("https://files.example.com/attachments")
	exec.AnthropicToOpenAI = anthropicToOpenAIStubForBridge
	cand := overloadTestCandidate(cs.srv.URL)

	body := []byte(`{"model":"gpt-5.6-luna","stream":true,"max_tokens":1024,"messages":[{"role":"user","content":[{"type":"text","text":"what is this?"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"QUJD"}}]}]}`)
	params := mmExecParams(body, cand)
	params.ClientProtocol = "anthropic-messages"

	if _, err := exec.Execute(params); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	got := cs.captured()
	if len(got) == 0 {
		t.Fatal("upstream was never called")
	}
	if want := "https://files.example.com/attachments/2026/08/a1/b2/hash1.png"; !bytes.Contains(got, []byte(want)) {
		t.Fatalf("bridged outbound body should carry the gateway URL %q, got: %s", want, got)
	}
	if bytes.Contains(got, []byte("QUJD")) {
		t.Fatalf("bridged outbound body should not carry the inline base64 payload, got: %s", got)
	}
}

// executorFetchStub satisfies attachments.AttachmentContentFetcher.
type executorFetchStub struct{}

func (executorFetchStub) LoadAttachment(relPath string) ([]byte, string, error) {
	if relPath == "2026/08/a1/b2/hash1.png" {
		return []byte("PNGDATA1"), "image/png", nil
	}
	return nil, "", fmt.Errorf("not found: %s", relPath)
}

// MM-2 executor 接线：deepseek（矩阵判定不支持 URL source）收到引用网关
// URL 的 OpenAI body 时，出站前取回内联为 base64 data URI。
func TestExecuteAttachmentOutbound_FetchFallbackInlinesGatewayURL(t *testing.T) {
	cs := newCaptureServer()
	defer cs.srv.Close()

	exec := newOverloadTestExecutor()
	exec.AttachmentURLFetchFallback = attachments.NewURLFetchFallback(
		"https://files.example.com/attachments", executorFetchStub{})
	cand := overloadTestCandidate(cs.srv.URL)
	cand.CatalogCode = "deepseek"

	body := []byte(`{"model":"deepseek-vl","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://files.example.com/attachments/2026/08/a1/b2/hash1.png"}}]}]}`)
	params := mmExecParams(body, cand)
	params.ClientModel = "deepseek-vl"
	params.AttachmentMetadata = nil // 网关 URL 由客户端引用，无本轮提取元数据

	if _, err := exec.Execute(params); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	got := cs.captured()
	if len(got) == 0 {
		t.Fatal("upstream was never called")
	}
	if want := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("PNGDATA1")); !bytes.Contains(got, []byte(want)) {
		t.Fatalf("outbound body should inline the fetched attachment as %q, got: %s", want, got)
	}
	if bytes.Contains(got, []byte("https://files.example.com/attachments/2026/08/a1/b2/hash1.png")) {
		t.Fatalf("outbound body should not keep the gateway URL for a non-URL provider, got: %s", got)
	}
}
