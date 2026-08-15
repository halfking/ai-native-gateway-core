package attachments

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MM-1 (doc 19 轨道 MM): outbound multimodal URL rewrite. Inline base64
// image blocks recorded by the Extractor are swapped for gateway URLs when
// the target provider's attachment capability prefers URL references.
// Providers without URL support keep the original base64 (fetch-fallback is
// MM-2).

const mmTestBody = `{
  "model": "gpt-4o",
  "messages": [
    {"role": "user", "content": [
      {"type": "text", "text": "what is this?"},
      {"type": "image_url", "image_url": {"url": "data:image/png;base64,QUJD"}},
      {"type": "image_url", "image_url": {"url": "data:image/jpeg;base64,REVG"}},
      {"type": "image_url", "image_url": {"url": "https://example.com/cat.png"}}
    ]}
  ]
}`

func mmTestAttachments() []AttachmentMetadata {
	return []AttachmentMetadata{
		{MessageIndex: 0, BlockIndex: 1, Path: "2026/08/a1/b2/hash1.png", Hash: "hash1", Status: AttachmentStatusStored},
		{MessageIndex: 0, BlockIndex: 2, Path: "2026/08/c3/d4/hash2.jpg", Hash: "hash2", Status: AttachmentStatusStored},
	}
}

func TestOutboundURLRewriterRewritesDataURLProviders(t *testing.T) {
	r := NewOutboundURLRewriter("https://files.example.com/attachments")
	out, n := r.RewriteOpenAIBody([]byte(mmTestBody), mmTestAttachments(), "openai")
	assert.Equal(t, 2, n, "both recorded data-URI blocks should be rewritten")

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
	require.Len(t, blocks, 4)
	assert.Equal(t, "https://files.example.com/attachments/2026/08/a1/b2/hash1.png", blocks[1].ImageURL.URL)
	assert.Equal(t, "https://files.example.com/attachments/2026/08/c3/d4/hash2.jpg", blocks[2].ImageURL.URL)
	// Non-data-URI references are untouched.
	assert.Equal(t, "https://example.com/cat.png", blocks[3].ImageURL.URL)
	// Text blocks are untouched.
	assert.Equal(t, "text", blocks[0].Type)
}

func TestOutboundURLRewriterKeepsBase64ForNonURLProviders(t *testing.T) {
	r := NewOutboundURLRewriter("https://files.example.com/attachments")
	for _, provider := range []string{"anthropic", "gemini", "deepseek"} {
		out, n := r.RewriteOpenAIBody([]byte(mmTestBody), mmTestAttachments(), provider)
		assert.Equal(t, 0, n, "%s should not be rewritten", provider)
		assert.Contains(t, string(out), "data:image/png;base64,QUJD")
	}
}

func TestOutboundURLRewriterNoOpWithoutAttachments(t *testing.T) {
	r := NewOutboundURLRewriter("https://files.example.com/attachments")
	out, n := r.RewriteOpenAIBody([]byte(mmTestBody), nil, "openai")
	assert.Equal(t, 0, n)
	assert.Equal(t, mmTestBody, string(out), "body must be returned unchanged")
}

// Identical data URIs in two blocks dedupe to one stored file; both client
// blocks must still be rewritten to that URL.
func TestOutboundURLRewriterDuplicateDataURIsBothRewritten(t *testing.T) {
	body := strings.Replace(mmTestBody,
		"data:image/jpeg;base64,REVG", "data:image/png;base64,QUJD", 1)
	atts := []AttachmentMetadata{
		{MessageIndex: 0, BlockIndex: 1, Path: "2026/08/a1/b2/hash1.png", Status: AttachmentStatusStored},
		{MessageIndex: 0, BlockIndex: 2, Path: "2026/08/a1/b2/hash1.png", Status: AttachmentStatusStored},
	}
	r := NewOutboundURLRewriter("https://gw/att")
	out, n := r.RewriteOpenAIBody([]byte(body), atts, "glm")
	assert.Equal(t, 2, n)
	assert.NotContains(t, string(out), "base64,")
	assert.Equal(t, 2, strings.Count(string(out), "https://gw/att/2026/08/a1/b2/hash1.png"))
}

// A coordinate that no longer matches a data-URI block (body was modified
// after extraction) must be skipped, never blindly spliced.
func TestOutboundURLRewriterSkipsMismatchedCoordinates(t *testing.T) {
	atts := []AttachmentMetadata{
		{MessageIndex: 0, BlockIndex: 3, Path: "2026/08/a1/b2/hash1.png", Status: AttachmentStatusStored},
	}
	r := NewOutboundURLRewriter("https://gw/att")
	out, n := r.RewriteOpenAIBody([]byte(mmTestBody), atts, "openai")
	assert.Equal(t, 0, n)
	assert.Contains(t, string(out), "data:image/png;base64,QUJD")
}

// ── E-P2-3 (doc 20)：Anthropic→OpenAI 桥多模态改写 ──────────────────────
//
// Anthropic 协议客户端的多模态请求路由到 URL 型 OpenAI 供应商时，
// RewriteOpenAIBody 解析不到 image_url 而静默 no-op（MM-1 收益未覆盖该
// 桥）。RewriteAnthropicBody 在桥接转换前把 Anthropic source 块
// （type=base64）按 Extractor 记录坐标替换为 type=url 的网关 URL，随后
// streaming.ConvertAnthropicBodyToOpenAI 的 convertImageBlock 会把
// source{type:url} 映射为 image_url{url}。

const anthropicMMTestBody = `{
  "model": "claude-sonnet-4",
  "max_tokens": 1024,
  "messages": [
    {"role": "user", "content": [
      {"type": "text", "text": "what is this?"},
      {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "QUJD"}},
      {"type": "image", "source": {"type": "base64", "media_type": "image/jpeg", "data": "REVG"}},
      {"type": "image", "source": {"type": "url", "url": "https://elsewhere.example.com/cat.png"}}
    ]}
  ]
}`

func anthropicMMTestAttachments() []AttachmentMetadata {
	return []AttachmentMetadata{
		{MessageIndex: 0, BlockIndex: 1, Path: "2026/08/a1/b2/hash1.png", Hash: "hash1", Status: AttachmentStatusStored},
		{MessageIndex: 0, BlockIndex: 2, Path: "2026/08/c3/d4/hash2.jpg", Hash: "hash2", Status: AttachmentStatusStored},
	}
}

// URL 型供应商：记录坐标上的 base64 source 块被替换为
// {"type":"url","url":<网关URL>}（即桥接转换器 convertImageBlock 的
// url 分支所需形状），非 base64 source 与文本块不动。
func TestOutboundURLRewriterRewritesAnthropicSourcesForURLProviders(t *testing.T) {
	r := NewOutboundURLRewriter("https://files.example.com/attachments")
	out, n := r.RewriteAnthropicBody([]byte(anthropicMMTestBody), anthropicMMTestAttachments(), "openai")
	assert.Equal(t, 2, n, "both recorded base64 source blocks should be rewritten")

	var body struct {
		Messages []struct {
			Content []struct {
				Type   string `json:"type"`
				Source *struct {
					Type      string `json:"type"`
					URL       string `json:"url"`
					MediaType string `json:"media_type"`
					Data      string `json:"data"`
				} `json:"source"`
			} `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(out, &body))
	blocks := body.Messages[0].Content
	require.Len(t, blocks, 4)

	assert.Equal(t, "text", blocks[0].Type)

	src1 := blocks[1].Source
	require.NotNil(t, src1)
	assert.Equal(t, "url", src1.Type, "base64 source must flip to url source")
	assert.Equal(t, "https://files.example.com/attachments/2026/08/a1/b2/hash1.png", src1.URL)
	assert.Empty(t, src1.Data, "base64 payload must be dropped")
	assert.Empty(t, src1.MediaType)

	src2 := blocks[2].Source
	require.NotNil(t, src2)
	assert.Equal(t, "url", src2.Type)
	assert.Equal(t, "https://files.example.com/attachments/2026/08/c3/d4/hash2.jpg", src2.URL)

	// 既有 url source（外部 URL）不动。
	src3 := blocks[3].Source
	require.NotNil(t, src3)
	assert.Equal(t, "url", src3.Type)
	assert.Equal(t, "https://elsewhere.example.com/cat.png", src3.URL)
}

// 非 URL 型供应商：保持 base64（fail-safe，与 RewriteOpenAIBody 同口径）。
func TestOutboundURLRewriterKeepsAnthropicBase64ForNonURLProviders(t *testing.T) {
	r := NewOutboundURLRewriter("https://files.example.com/attachments")
	for _, provider := range []string{"anthropic", "gemini", "deepseek", "minimax"} {
		out, n := r.RewriteAnthropicBody([]byte(anthropicMMTestBody), anthropicMMTestAttachments(), provider)
		assert.Equal(t, 0, n, "%s should not be rewritten", provider)
		assert.Equal(t, anthropicMMTestBody, string(out), "%s body must be byte-identical", provider)
	}
}

// 坐标漂移（body 在提取后被改动）：跳过该坐标，绝不盲改。
func TestOutboundURLRewriterAnthropicSkipsMismatchedCoordinates(t *testing.T) {
	atts := []AttachmentMetadata{
		{MessageIndex: 0, BlockIndex: 0, Path: "2026/08/a1/b2/hash1.png", Status: AttachmentStatusStored},
		{MessageIndex: 0, BlockIndex: 3, Path: "2026/08/a1/b2/hash1.png", Status: AttachmentStatusStored},
	}
	r := NewOutboundURLRewriter("https://files.example.com/attachments")
	out, n := r.RewriteAnthropicBody([]byte(anthropicMMTestBody), atts, "openai")
	assert.Equal(t, 0, n)
	assert.Equal(t, anthropicMMTestBody, string(out), "mismatched coordinates must leave the body untouched")
}

// 无附件元数据时逐字节不变返回。
func TestOutboundURLRewriterAnthropicNoOpWithoutAttachments(t *testing.T) {
	r := NewOutboundURLRewriter("https://files.example.com/attachments")
	out, n := r.RewriteAnthropicBody([]byte(anthropicMMTestBody), nil, "openai")
	assert.Equal(t, 0, n)
	assert.Equal(t, anthropicMMTestBody, string(out))
}
