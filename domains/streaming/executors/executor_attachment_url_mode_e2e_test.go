package executors

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/attachments"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// executor_attachment_url_mode_e2e_test.go — MM-4 (doc 19 §3 轨道 MM)
//
// multimodal-e2e 框架的 URL 模式用例（附件以网关 URL 形式传入请求），
// 覆盖 MM-2 URL 拉取回退的端到端分支。与既有 MM-1/MM-2 用例同框架：
// 走 Execute 公共请求路径（/v1/chat/completions 的 http.Request +
// ResponseRecorder），mock 边界与 executor_attachment_outbound_test.go
// 一致——仅 mock 上游（captureServer SSE），其余全为真实生产代码。
//
// Matrix mapping（docs/multimodal-testing/00-test-plan.md §3，续 T-21）：
//
//	T-22 URL 模式 · 回退关闭直通      → TestExecuteAttachmentURLMode_FlagOffPassthroughGatewayURL
//	                                     （回退开·非 URL 供应商内联正例 = 既有
//	                                       TestExecuteAttachmentOutbound_FetchFallbackInlinesGatewayURL，
//	                                       登记为 T-23，不在本文件重复）
//	T-24 URL 模式 · `..` 穿越拒绝     → TestExecuteAttachmentURLMode_TraversalPathRejectedKeepURL
//	T-25 URL 模式 · 取回失败 best-effort → TestExecuteAttachmentURLMode_FetchFailureKeepsURLBestEffort
//	T-26 URL 模式 · 外部 URL 不取回    → TestExecuteAttachmentURLMode_ExternalURLNotFetched
//	T-27 URL 模式 · 重复 URL 单次取回  → TestExecuteAttachmentURLMode_DuplicateGatewayURLSingleFetch
//	T-28 URL 模式 · URL 型供应商直通  → TestExecuteAttachmentURLMode_URLProviderPassthroughNoFetch
//
// 开关语义对齐生产 wiring（cmd/gateway/main.go）：
// LLM_GATEWAY_ATTACHMENT_URL_FETCH_FALLBACK 关（默认）= 两个附件 hook 均为
// nil；开（且 PUBLIC_BASE_URL 已配置）= AttachmentURLFetchFallback 非 nil。

const urlModeBase = "https://files.example.com/attachments"
const urlModeStoredPath = "2026/08/a1/b2/hash1.png"
const urlModeGatewayURL = urlModeBase + "/" + urlModeStoredPath

// urlModeFetchStub is a concurrency-safe AttachmentContentFetcher that
// records every LoadAttachment call so tests can pin "never fetched"
// (traversal / external URL / flag-off) and "fetched exactly once"
// (duplicate-URL dedupe) branches.
type urlModeFetchStub struct {
	mu      sync.Mutex
	calls   []string
	content map[string][]byte
	mime    map[string]string
	errAll  bool // every load fails (best-effort branch)
}

func (s *urlModeFetchStub) LoadAttachment(relPath string) ([]byte, string, error) {
	s.mu.Lock()
	s.calls = append(s.calls, relPath)
	s.mu.Unlock()
	if s.errAll {
		return nil, "", fmt.Errorf("stub: storage unavailable: %s", relPath)
	}
	content, ok := s.content[relPath]
	if !ok {
		return nil, "", fmt.Errorf("stub: not found: %s", relPath)
	}
	return content, s.mime[relPath], nil
}

func (s *urlModeFetchStub) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *urlModeFetchStub) calledPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// urlModeBody builds a streaming OpenAI body whose user turn references
// gateway attachment URLs. stream_options is pre-set so the legacy
// stream_options injection is a no-op and the multimodal pipeline is the
// only variable (same convention as mmFlagOffBody).
func urlModeBody(model string, urls ...string) []byte {
	blocks := `[{"type":"text","text":"what is this?"}`
	for _, u := range urls {
		blocks += `,{"type":"image_url","image_url":{"url":"` + u + `"}}`
	}
	blocks += `]`
	return []byte(`{"model":"` + model + `","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":` + blocks + `}]}`)
}

// urlModeParams mirrors mmExecParams but drops the extraction metadata:
// in URL mode the client itself references the gateway URL, so there is
// no per-turn AttachmentMetadata (same as the MM-2 wiring test).
func urlModeParams(body []byte, cand provider.Candidate) *ExecParams {
	p := mmExecParams(body, cand)
	p.AttachmentMetadata = nil
	return p
}

// T-22: 回退关闭（默认 wiring，LLM_GATEWAY_ATTACHMENT_URL_FETCH_FALLBACK
// 未开启，AttachmentURLFetchFallback == nil）时，即使目标供应商矩阵判定
// 不支持 URL source（deepseek），网关 URL 引用也必须原样出站——不取回、
// 不内联、不吞请求。
func TestExecuteAttachmentURLMode_FlagOffPassthroughGatewayURL(t *testing.T) {
	cs := newCaptureServer()
	defer cs.srv.Close()

	exec := newOverloadTestExecutor() // fetch fallback nil = flag off (default)
	cand := overloadTestCandidate(cs.srv.URL)
	cand.CatalogCode = "deepseek"

	params := urlModeParams(urlModeBody("deepseek-vl", urlModeGatewayURL), cand)
	params.ClientModel = "deepseek-vl"

	if _, err := exec.Execute(params); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	got := cs.captured()
	if len(got) == 0 {
		t.Fatal("upstream was never called")
	}
	if !strings.Contains(string(got), urlModeGatewayURL) {
		t.Fatalf("flag-off must pass the gateway URL through verbatim, got: %s", got)
	}
	if strings.Contains(string(got), "data:image/png;base64,") {
		t.Fatalf("flag-off must not inline attachments as base64, got: %s", got)
	}
}

// T-24: `..` 路径穿越拒绝——relPath 含 ".." 的网关 URL 不触发存储取回，
// 原样保留继续请求（sha256 路径本不含 ".."，此分支防御构造 URL）。
func TestExecuteAttachmentURLMode_TraversalPathRejectedKeepURL(t *testing.T) {
	cs := newCaptureServer()
	defer cs.srv.Close()

	fetcher := &urlModeFetchStub{content: map[string][]byte{
		"etc/passwd": []byte("SHOULD-NOT-BE-LOADED"),
	}}
	exec := newOverloadTestExecutor()
	exec.AttachmentURLFetchFallback = attachments.NewURLFetchFallback(urlModeBase, fetcher)
	cand := overloadTestCandidate(cs.srv.URL)
	cand.CatalogCode = "deepseek"

	traversalURL := urlModeBase + "/2026/08/a1/../../../etc/passwd"
	params := urlModeParams(urlModeBody("deepseek-vl", traversalURL), cand)
	params.ClientModel = "deepseek-vl"

	if _, err := exec.Execute(params); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if n := fetcher.callCount(); n != 0 {
		t.Fatalf("traversal URL must never reach storage fetch, got calls: %v", fetcher.calledPaths())
	}
	got := cs.captured()
	if len(got) == 0 {
		t.Fatal("upstream was never called (traversal rejection must not swallow the request)")
	}
	if !strings.Contains(string(got), traversalURL) {
		t.Fatalf("traversal URL must be kept verbatim (best-effort), got: %s", got)
	}
	if strings.Contains(string(got), "SHOULD-NOT-BE-LOADED") {
		t.Fatalf("traversal URL content must never be inlined, got: %s", got)
	}
}

// T-25: 取回失败 best-effort——存储读失败时保留原网关 URL 继续请求，
// 客户端仍收到正常 SSE 流（不吞请求、不 5xx）。
func TestExecuteAttachmentURLMode_FetchFailureKeepsURLBestEffort(t *testing.T) {
	cs := newCaptureServer()
	defer cs.srv.Close()

	fetcher := &urlModeFetchStub{errAll: true}
	exec := newOverloadTestExecutor()
	exec.AttachmentURLFetchFallback = attachments.NewURLFetchFallback(urlModeBase, fetcher)
	cand := overloadTestCandidate(cs.srv.URL)
	cand.CatalogCode = "deepseek"

	params := urlModeParams(urlModeBody("deepseek-vl", urlModeGatewayURL), cand)
	params.ClientModel = "deepseek-vl"

	if _, err := exec.Execute(params); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if n := fetcher.callCount(); n != 1 {
		t.Fatalf("fetch failure path should attempt exactly one storage load, got %d", n)
	}
	got := cs.captured()
	if len(got) == 0 {
		t.Fatal("upstream was never called (fetch failure must not swallow the request)")
	}
	if !strings.Contains(string(got), urlModeGatewayURL) {
		t.Fatalf("fetch failure must keep the original gateway URL, got: %s", got)
	}
	if rec, ok := params.W.(*httptest.ResponseRecorder); ok {
		if body := rec.Body.String(); !strings.Contains(body, "hi") {
			t.Fatalf("client should still receive the streamed response, got: %q", body)
		}
	}
}

// T-26: 非网关 base URL 前缀的外部 URL 不取回（回退只接受自身 base URL
// 前缀，取回走自身存储不外联），URL 原样出站。
func TestExecuteAttachmentURLMode_ExternalURLNotFetched(t *testing.T) {
	cs := newCaptureServer()
	defer cs.srv.Close()

	fetcher := &urlModeFetchStub{content: map[string][]byte{
		urlModeStoredPath: []byte("PNGDATA1"),
	}}
	exec := newOverloadTestExecutor()
	exec.AttachmentURLFetchFallback = attachments.NewURLFetchFallback(urlModeBase, fetcher)
	cand := overloadTestCandidate(cs.srv.URL)
	cand.CatalogCode = "deepseek"

	externalURL := "https://elsewhere.example.com/cat.png"
	params := urlModeParams(urlModeBody("deepseek-vl", externalURL), cand)
	params.ClientModel = "deepseek-vl"

	if _, err := exec.Execute(params); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if n := fetcher.callCount(); n != 0 {
		t.Fatalf("external URL must never trigger a storage fetch, got calls: %v", fetcher.calledPaths())
	}
	got := cs.captured()
	if len(got) == 0 {
		t.Fatal("upstream was never called")
	}
	if !strings.Contains(string(got), externalURL) {
		t.Fatalf("external URL must pass through untouched, got: %s", got)
	}
}

// T-27: 同一网关 URL 重复出现（会话历史重放）——按 relPath 去重，存储只
// 取回一次，两处引用全部内联为 data URI。
func TestExecuteAttachmentURLMode_DuplicateGatewayURLSingleFetch(t *testing.T) {
	cs := newCaptureServer()
	defer cs.srv.Close()

	fetcher := &urlModeFetchStub{
		content: map[string][]byte{urlModeStoredPath: []byte("PNGDATA1")},
		mime:    map[string]string{urlModeStoredPath: "image/png"},
	}
	exec := newOverloadTestExecutor()
	exec.AttachmentURLFetchFallback = attachments.NewURLFetchFallback(urlModeBase, fetcher)
	cand := overloadTestCandidate(cs.srv.URL)
	cand.CatalogCode = "deepseek"

	params := urlModeParams(urlModeBody("deepseek-vl", urlModeGatewayURL, urlModeGatewayURL), cand)
	params.ClientModel = "deepseek-vl"

	if _, err := exec.Execute(params); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if n := fetcher.callCount(); n != 1 {
		t.Fatalf("duplicate gateway URL must dedupe to exactly one storage fetch, got %d", n)
	}
	got := string(cs.captured())
	if got == "" {
		t.Fatal("upstream was never called")
	}
	if strings.Contains(got, urlModeGatewayURL) {
		t.Fatalf("non-URL provider should not keep the gateway URL, got: %s", got)
	}
	dataURI := "data:image/png;base64,UE5HREFUQTE=" // base64("PNGDATA1")
	if c := strings.Count(got, dataURI); c != 2 {
		t.Fatalf("both duplicate references should be inlined, want 2 occurrences of %q, got %d in: %s", dataURI, c, got)
	}
}

// T-28: 回退开启但目标供应商矩阵判定支持 URL source（openai）——网关 URL
// 直通不取回（回退只对 SupportsHTTPSURL=false 的供应商生效）。
func TestExecuteAttachmentURLMode_URLProviderPassthroughNoFetch(t *testing.T) {
	cs := newCaptureServer()
	defer cs.srv.Close()

	fetcher := &urlModeFetchStub{content: map[string][]byte{
		urlModeStoredPath: []byte("PNGDATA1"),
	}}
	exec := newOverloadTestExecutor()
	exec.AttachmentURLFetchFallback = attachments.NewURLFetchFallback(urlModeBase, fetcher)
	cand := overloadTestCandidate(cs.srv.URL) // CatalogCode "openai"

	params := urlModeParams(urlModeBody("gpt-4o-mini", urlModeGatewayURL), cand)
	params.ClientModel = "gpt-4o-mini"

	if _, err := exec.Execute(params); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if n := fetcher.callCount(); n != 0 {
		t.Fatalf("URL-capable provider must not trigger storage fetches, got calls: %v", fetcher.calledPaths())
	}
	got := cs.captured()
	if len(got) == 0 {
		t.Fatal("upstream was never called")
	}
	if !strings.Contains(string(got), urlModeGatewayURL) {
		t.Fatalf("URL-capable provider should receive the gateway URL verbatim, got: %s", got)
	}
}
