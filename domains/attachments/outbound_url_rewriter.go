package attachments

import (
	"bytes"
	"encoding/json"
	"strings"

	sessionv2 "github.com/kaixuan/llm-gateway-go/domains/session/v2"
)

// outbound_url_rewriter.go — MM-1 (doc 19 轨道 MM)
//
// Outbound multimodal URL rewrite: inline base64 image blocks that the
// Extractor already stored (hash-deduped) are swapped for gateway URLs when
// the target provider's attachment capability prefers URL references. This
// stops large base64 payloads being re-uploaded to providers on every turn.
//
// Providers without URL support keep the original base64 inline — the
// URL-fetch fallback for them is MM-2. The rewrite is opt-in (default off)
// and only ever splices the recorded data-URI substrings, so the rest of the
// outbound body stays byte-identical.

// OutboundURLRewriter swaps stored data-URI attachments for gateway URLs.
type OutboundURLRewriter struct {
	baseURL string
}

// NewOutboundURLRewriter creates a rewriter serving URLs under baseURL
// (e.g. https://files.example.com/attachments). Path joins with "/".
func NewOutboundURLRewriter(baseURL string) *OutboundURLRewriter {
	return &OutboundURLRewriter{baseURL: strings.TrimRight(baseURL, "/")}
}

// ShouldURLRewrite reports whether the provider's attachment capability
// prefers gateway-URL references for outbound bodies (doc 19 MM-1 gate;
// providers without URL support are handled by MM-2 fallback).
func ShouldURLRewrite(provider string) bool {
	capability := sessionv2.GetProviderCapability(provider)
	return capability.SupportsHTTPSURL && capability.PreferredMode == sessionv2.RefModeGatewayURL
}

// RewriteOpenAIBody rewrites the OpenAI-format request body for one outbound
// attempt. It walks the Extractor-recorded coordinates
// (messages[MessageIndex].content[BlockIndex]), verifies the block still
// holds the recorded data URI, and splices in the gateway URL. Returns the
// body (unchanged when nothing applies) and the number of blocks rewritten.
func (r *OutboundURLRewriter) RewriteOpenAIBody(body []byte, attachments []AttachmentMetadata, provider string) ([]byte, int) {
	if len(attachments) == 0 || len(body) == 0 || !ShouldURLRewrite(provider) {
		return body, 0
	}

	var parsed struct {
		Messages []struct {
			Content []struct {
				ImageURL *struct {
					URL string `json:"url"`
				} `json:"image_url"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return body, 0
	}

	// Collect verified (data URI → gateway URL) replacements.
	replacements := make(map[string]string)
	for _, att := range attachments {
		if att.Path == "" || att.MessageIndex < 0 || att.MessageIndex >= len(parsed.Messages) {
			continue
		}
		blocks := parsed.Messages[att.MessageIndex].Content
		if att.BlockIndex < 0 || att.BlockIndex >= len(blocks) {
			continue
		}
		iu := blocks[att.BlockIndex].ImageURL
		if iu == nil || !strings.HasPrefix(iu.URL, "data:") {
			// Body drifted from the extraction snapshot: skip rather than
			// blind-splice.
			continue
		}
		replacements[iu.URL] = r.baseURL + "/" + att.Path
	}
	if len(replacements) == 0 {
		return body, 0
	}

	out := body
	rewritten := 0
	for oldURI, newURL := range replacements {
		occurrences := bytes.Count(out, []byte(oldURI))
		if occurrences == 0 {
			continue
		}
		out = bytes.ReplaceAll(out, []byte(oldURI), []byte(newURL))
		// Every occurrence of the URI belongs to a recorded block (the
		// coordinate walk verified them), so count blocks, not unique URIs.
		rewritten += occurrences
	}
	if rewritten == 0 {
		return body, 0
	}
	return out, rewritten
}

// RewriteAnthropicBody rewrites an Anthropic Messages request body for one
// outbound attempt (E-P2-3, doc 20): Anthropic-protocol clients bridged to a
// URL-mode OpenAI provider previously hit RewriteOpenAIBody's silent no-op
// (no image_url fields in the Anthropic shape). This variant walks the same
// Extractor-recorded coordinates, verifies the block is still a base64 image
// source, and swaps it for a {"type":"url","url":...} source — the exact
// shape the Anthropic→OpenAI bridge converter maps to image_url. Providers
// without URL preference keep the original base64. Returns the body
// (unchanged when nothing applies) and the number of source blocks rewritten.
func (r *OutboundURLRewriter) RewriteAnthropicBody(body []byte, attachments []AttachmentMetadata, provider string) ([]byte, int) {
	if len(attachments) == 0 || len(body) == 0 || !ShouldURLRewrite(provider) {
		return body, 0
	}

	var parsed struct {
		Messages []struct {
			Content []json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return body, 0
	}

	// Collect verified (coordinate → gateway URL) replacements.
	replacements := make(map[int]map[int]string)
	for _, att := range attachments {
		if att.Path == "" || att.MessageIndex < 0 || att.MessageIndex >= len(parsed.Messages) {
			continue
		}
		blocks := parsed.Messages[att.MessageIndex].Content
		if att.BlockIndex < 0 || att.BlockIndex >= len(blocks) {
			continue
		}
		var block struct {
			Type   string `json:"type"`
			Source *struct {
				Type string `json:"type"`
				Data string `json:"data"`
			} `json:"source"`
		}
		if err := json.Unmarshal(blocks[att.BlockIndex], &block); err != nil {
			continue
		}
		// Body drifted from the extraction snapshot: skip rather than
		// blind-splice. Only base64 sources are swapped; url sources pass
		// through untouched.
		if block.Type != "image" || block.Source == nil ||
			block.Source.Type != "base64" || block.Source.Data == "" {
			continue
		}
		if replacements[att.MessageIndex] == nil {
			replacements[att.MessageIndex] = make(map[int]string)
		}
		replacements[att.MessageIndex][att.BlockIndex] = r.baseURL + "/" + att.Path
	}
	if len(replacements) == 0 {
		return body, 0
	}

	// Apply via JSON-aware mutation; the rewritten body is re-serialized
	// (the bridge converter re-marshals downstream anyway), while the
	// no-op path above keeps the original bytes.
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return body, 0
	}
	messages, ok := root["messages"].([]any)
	if !ok {
		return body, 0
	}
	rewritten := 0
	for msgIdx, blockRepls := range replacements {
		if msgIdx >= len(messages) {
			continue
		}
		msgMap, ok := messages[msgIdx].(map[string]any)
		if !ok {
			continue
		}
		content, ok := msgMap["content"].([]any)
		if !ok {
			continue
		}
		for blockIdx, gwURL := range blockRepls {
			if blockIdx >= len(content) {
				continue
			}
			blockMap, ok := content[blockIdx].(map[string]any)
			if !ok {
				continue
			}
			blockMap["source"] = map[string]any{"type": "url", "url": gwURL}
			rewritten++
		}
	}
	if rewritten == 0 {
		return body, 0
	}
	out, err := json.Marshal(root)
	if err != nil {
		return body, 0
	}
	return out, rewritten
}
