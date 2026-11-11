package attachments

import (
	"encoding/json"
	"strings"

	sessionv2 "github.com/kaixuan/llm-gateway-go/domains/session/v2"
)

// outbound_url_rewriter.go — MM-1 (doc 19 轨道 MM)
//
// Outbound multimodal URL rewrite: inline base64 blocks that the
// Extractor already stored (hash-deduped) are swapped for gateway URLs when
// the target provider's attachment capability prefers URL references. This
// stops large base64 payloads being re-uploaded to providers on every turn.
//
// Providers without URL support keep the original base64 inline — the
// URL-fetch fallback for them is MM-2. The rewrite is opt-in (default off)
// and only ever touches the **attachment-carrying fields** it can prove
// correspond to a recorded attachment (see attachment_fields.go).

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
//
// ★ 2026-11-11（附件域审计轮）：本函数改走 attachment_fields.go 的**共享字段表**，
//   修掉「只改写 image_url ⇒ 文档/视频每次请求全量重传」这一缺陷：
//   ★ 旧实现只读 `image_url.url` ⇒ `file.file_data`（文档/PDF）与
//   `video_url.url`（视频）**原样**留在 body 里 ⇒ ⇒ 而该功能存在的理由正是
//   「别每次请求都把大载荷重传给上游」⇒ ⇒ 对这两类附件**完全失效**。
//   ⇒ 现在三类附件都走同一张字段表 ⇒ ⇒ 一处修改两边同时生效。
//
// ★ 诚实边界：★ 有替换时 body 整体重新序列化（★ 键序规范化、空白去掉 ★）
//   ⇒ **不是**逐字节保持；★ 但只有**被证明对应到落盘记录**的附件字段会变，
//   ★ 其余字段原值不变（★ 数字用 json.Number 承载 ⇒ 不被改写 ★）。
func (r *OutboundURLRewriter) RewriteOpenAIBody(body []byte, attachments []AttachmentMetadata, provider string) ([]byte, int) {
	if len(attachments) == 0 || len(body) == 0 || !ShouldURLRewrite(provider) {
		return body, 0
	}

	// 坐标 → 落盘记录（★ 只认有 Path 的记录 ⇒ 没有路径就没有可替换的 URL ★）。
	byCoord := make(map[[2]int]AttachmentMetadata, len(attachments))
	for _, att := range attachments {
		if att.Path == "" {
			continue
		}
		byCoord[[2]int{att.MessageIndex, att.BlockIndex}] = att
	}

	out, rewritten, err := rewriteChatAttachmentFields(body,
		func(ref attachmentSlotRef, value string) string {
			// Body drifted from the extraction snapshot: skip rather than
			// blind-splice. Only base64 sources are swapped; url sources pass
			// through untouched.
			if !strings.HasPrefix(value, "data:") {
				return ""
			}
			att, ok := byCoord[[2]int{ref.Container, ref.Block}]
			if !ok {
				return ""
			}
			// ★ 类型闸：★ 坐标对上但类型对不上 ⇒ body 在落盘后被改过 ⇒ 跳过。
			if att.Type != "" {
				want, ok := chatBlockAttachmentType[blockTypeOfPath(ref)]
				if ok && want != att.Type {
					return ""
				}
			}
			return r.baseURL + "/" + att.Path
		})
	if err != nil {
		return body, 0
	}
	return out, rewritten
}

// blockTypeOfPath 从字段路径反推块类型（★ 字段表与块类型是一一对应的 ⇒
// ★ 这里用路径首段即可（image_url / file / video_url）★）。
func blockTypeOfPath(ref attachmentSlotRef) string {
	if len(ref.Path) == 0 {
		return ""
	}
	return ref.Path[0]
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
//
// ★ 2026-11-11（附件域审计轮）：★ 本函数原先**只处理 image 块** ⇒ ⇒
//   Anthropic 客户端的 **document 块**（PDF）**永远不被换成 URL** ⇒ ⇒
//   每次请求都把整份 PDF 的 base64 重传给上游 ⇒ ⇒ 与本功能的存在理由相悖。
//   ⇒ 现在 image 与 document **都**处理（★ Anthropic 的 document source 同样
//   支持 {"type":"url","url":...} ⇒ ★ 与本仓 serialize_anthropic.go 自己
//   产出的 document 形态一致 ★）。
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
		// ★ 2026-11-11：★ image 与 document 都算（★ 此前只有 image ★）。
		if (block.Type != "image" && block.Type != "document") ||
			block.Source == nil ||
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
