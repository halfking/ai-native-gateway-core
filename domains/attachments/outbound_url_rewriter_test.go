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
