package attachments

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MM-2 (doc 19 §3): URL 拉取回退。目标 provider 不支持 url source（矩阵
// SupportsHTTPSURL=false，如 gemini/ollama/deepseek/未知）而出站 body 引用
// 了网关附件 URL 时，网关从自身存储取回内容、重新内联为 base64 data URI
// 发给上游（provider-url-support-matrix.md §3 设计约束：取回走
// Storage.LoadAttachment，不发起外网请求；失败 best-effort 保留原引用）。

func TestShouldFetchFallback(t *testing.T) {
	tests := []struct {
		provider string
		want     bool
	}{
		// 矩阵判定不支持 URL source（必须回退 inline base64）。
		{"gemini", true},
		{"google", true},
		{"deepseek", true},
		{"ollama", true},
		{"together", true},
		{"baichuan", true},
		{"some-unknown-vendor", true},
		// 矩阵判定支持 URL source（URL 直通，不回退）。
		{"openai", false},
		{"anthropic", false},
		{"glm", false},
		{"qwen", false},
		{"doubao", false},
		{"moonshot", false},
	}
	for _, tt := range tests {
		if got := ShouldFetchFallback(tt.provider); got != tt.want {
			t.Errorf("ShouldFetchFallback(%q) = %v, want %v", tt.provider, got, tt.want)
		}
	}
}

// stubFetcher maps storage-relative paths to stored content for tests.
type stubFetcher struct {
	content map[string][]byte
	mime    map[string]string
	errs    map[string]error
	calls   []string
}

func (f *stubFetcher) LoadAttachment(relPath string) ([]byte, string, error) {
	f.calls = append(f.calls, relPath)
	if err, ok := f.errs[relPath]; ok {
		return nil, "", err
	}
	content, ok := f.content[relPath]
	if !ok {
		return nil, "", errors.New("stub: not found: " + relPath)
	}
	return content, f.mime[relPath], nil
}

const fallbackTestBody = `{
  "model": "gemini-2.5-flash",
  "messages": [
    {"role": "user", "content": [
      {"type": "text", "text": "what is this?"},
      {"type": "image_url", "image_url": {"url": "https://files.example.com/attachments/2026/08/a1/b2/hash1.png"}},
      {"type": "image_url", "image_url": {"url": "https://files.example.com/attachments/2026/08/c3/d4/hash2.jpg"}},
      {"type": "image_url", "image_url": {"url": "https://elsewhere.example.com/cat.png"}},
      {"type": "image_url", "image_url": {"url": "data:image/png;base64,QUJD"}}
    ]}
  ]
}`

// 非 URL 供应商：body 中的网关 URL 必须被取回并内联为 data URI，
// 外部 URL 与已有 data URI 保持不变。
func TestURLFetchFallbackInlinesGatewayURLsForNonURLProviders(t *testing.T) {
	fetcher := &stubFetcher{
		content: map[string][]byte{
			"2026/08/a1/b2/hash1.png": []byte("PNGDATA1"),
			"2026/08/c3/d4/hash2.jpg": []byte("JPGDATA2"),
		},
		mime: map[string]string{
			"2026/08/a1/b2/hash1.png": "image/png",
			"2026/08/c3/d4/hash2.jpg": "image/jpeg",
		},
	}
	fb := NewURLFetchFallback("https://files.example.com/attachments", fetcher)
	out, n := fb.InlineOpenAIBody([]byte(fallbackTestBody), "gemini")
	assert.Equal(t, 2, n, "both gateway-URL blocks should be inlined")

	var body struct {
		Messages []struct {
			Content []struct {
				Type     string `json:"type"`
				ImageURL *struct {
					URL string `json:"url"`
				} `json:"image_url"`
			} `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(out, &body))
	blocks := body.Messages[0].Content
	require.Len(t, blocks, 5)
	assert.Equal(t, "data:image/png;base64,"+base64.StdEncoding.EncodeToString([]byte("PNGDATA1")), blocks[1].ImageURL.URL)
	assert.Equal(t, "data:image/jpeg;base64,"+base64.StdEncoding.EncodeToString([]byte("JPGDATA2")), blocks[2].ImageURL.URL)
	// 外部 URL 与既有 data URI 不动。
	assert.Equal(t, "https://elsewhere.example.com/cat.png", blocks[3].ImageURL.URL)
	assert.Equal(t, "data:image/png;base64,QUJD", blocks[4].ImageURL.URL)
	// 文本块不动。
	assert.Equal(t, "text", blocks[0].Type)
}

// URL 型供应商（矩阵 SupportsHTTPSURL=true）：网关 URL 直通，不取回。
func TestURLFetchFallbackNoOpForURLProviders(t *testing.T) {
	fetcher := &stubFetcher{
		content: map[string][]byte{"2026/08/a1/b2/hash1.png": []byte("PNGDATA1")},
		mime:    map[string]string{"2026/08/a1/b2/hash1.png": "image/png"},
	}
	fb := NewURLFetchFallback("https://files.example.com/attachments", fetcher)
	for _, provider := range []string{"openai", "glm", "qwen", "anthropic"} {
		out, n := fb.InlineOpenAIBody([]byte(fallbackTestBody), provider)
		assert.Equal(t, 0, n, "%s should not be inlined", provider)
		assert.Contains(t, string(out), "https://files.example.com/attachments/2026/08/a1/b2/hash1.png")
	}
	assert.Empty(t, fetcher.calls, "URL providers must not trigger storage fetches")
}

// 取回失败：保留原网关 URL（best-effort，不吞请求）。
func TestURLFetchFallbackKeepsURLOnFetchFailure(t *testing.T) {
	fetcher := &stubFetcher{errs: map[string]error{
		"2026/08/a1/b2/hash1.png": errors.New("disk error"),
	}}
	fb := NewURLFetchFallback("https://files.example.com/attachments", fetcher)
	out, n := fb.InlineOpenAIBody([]byte(fallbackTestBody), "gemini")
	assert.Equal(t, 0, n)
	assert.Equal(t, fallbackTestBody, string(out), "body must be returned unchanged")
}

// 内容超出 provider inline 上限：保留 URL，不内联。
func TestURLFetchFallbackKeepsURLOverProviderInlineLimit(t *testing.T) {
	// unknown provider 默认 MaxInlineBytes=5MB，用 6MB 内容触发上限。
	huge := make([]byte, 6*1024*1024)
	fetcher := &stubFetcher{
		content: map[string][]byte{"2026/08/a1/b2/hash1.png": huge},
		mime:    map[string]string{"2026/08/a1/b2/hash1.png": "image/png"},
	}
	fb := NewURLFetchFallback("https://files.example.com/attachments", fetcher)
	body := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://files.example.com/attachments/2026/08/a1/b2/hash1.png"}}]}]}`
	out, n := fb.InlineOpenAIBody([]byte(body), "some-unknown-vendor")
	assert.Equal(t, 0, n)
	assert.Equal(t, body, string(out))
}

// body 不含网关 URL 时逐字节不变返回。
func TestURLFetchFallbackNoGatewayURLsIsByteIdentical(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"plain text"}]}`
	fb := NewURLFetchFallback("https://files.example.com/attachments", &stubFetcher{})
	out, n := fb.InlineOpenAIBody([]byte(body), "gemini")
	assert.Equal(t, 0, n)
	assert.Equal(t, body, string(out))
}

// `..` 路径穿越拒绝（MM-4）：relPath 含 ".." 的网关 URL 不触发存储取回，
// 原样保留（sha256 存储路径不含 ".."，此分支防御构造 URL）。
func TestURLFetchFallbackRejectsTraversalPaths(t *testing.T) {
	fetcher := &stubFetcher{
		content: map[string][]byte{"etc/passwd": []byte("SHOULD-NOT-BE-LOADED")},
	}
	fb := NewURLFetchFallback("https://files.example.com/attachments", fetcher)
	for _, rel := range []string{
		"../../etc/passwd",
		"2026/08/a1/../../../etc/passwd",
		"..",
	} {
		body := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://files.example.com/attachments/` + rel + `"}}]}]}`
		out, n := fb.InlineOpenAIBody([]byte(body), "deepseek")
		assert.Equal(t, 0, n, "traversal relPath %q must not be inlined", rel)
		assert.Equal(t, body, string(out), "traversal relPath %q must keep the body unchanged", rel)
	}
	assert.Empty(t, fetcher.calls, "traversal paths must never reach storage")
}

// 空 relPath（URL 即 base 前缀 + "/"）同样不取回、不改写。
func TestURLFetchFallbackRejectsEmptyRelPath(t *testing.T) {
	fetcher := &stubFetcher{
		content: map[string][]byte{"2026/08/a1/b2/hash1.png": []byte("PNGDATA1")},
	}
	fb := NewURLFetchFallback("https://files.example.com/attachments", fetcher)
	body := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://files.example.com/attachments/"}}]}]}`
	out, n := fb.InlineOpenAIBody([]byte(body), "deepseek")
	assert.Equal(t, 0, n)
	assert.Equal(t, body, string(out))
	assert.Empty(t, fetcher.calls)
}

// 同一 URL 在多个块出现（会话历史重放）：全部内联。
func TestURLFetchFallbackDuplicateURLsAllInlined(t *testing.T) {
	url := "https://files.example.com/attachments/2026/08/a1/b2/hash1.png"
	body := `{"messages":[` +
		`{"role":"user","content":[{"type":"image_url","image_url":{"url":"` + url + `"}}]},` +
		`{"role":"assistant","content":"ok"},` +
		`{"role":"user","content":[{"type":"image_url","image_url":{"url":"` + url + `"}}]}]}`

	fetcher := &stubFetcher{
		content: map[string][]byte{"2026/08/a1/b2/hash1.png": []byte("PNGDATA1")},
		mime:    map[string]string{"2026/08/a1/b2/hash1.png": "image/png"},
	}
	fb := NewURLFetchFallback("https://files.example.com/attachments", fetcher)
	out, n := fb.InlineOpenAIBody([]byte(body), "deepseek")
	assert.Equal(t, 2, n)
	assert.NotContains(t, string(out), url)
	assert.Equal(t, 2, strings.Count(string(out), "data:image/png;base64,"))
	assert.Len(t, fetcher.calls, 1, "duplicate URL must dedupe to one storage fetch")
}
