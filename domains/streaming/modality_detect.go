package streaming

import (
	"encoding/json"
	"strings"
)

// Routing modality constants. The handler uses these to derive the
// routing modality from the raw request body, so that vision/audio/video
// requests are routed only to providers that can serve them
// (audit-modality-routing, 2026-07-15).
//
// Resolution rule:
//
//	text-only request       → "text"
//	  (any provider can serve pure text; SQL filter accepts everything)
//	image block present     → "vision"
//	audio block present     → "audio"
//	video block present     → "video"
//	multiple modalities     → highest-priority wins (video > audio > vision)
//
// ★判据的【唯一实现】不在本文件，也不在本注释里：
//
//	· Go 判据 = provider.ModalityAccepts
//	· SQL 形态 = provider.ModalityAcceptArmsSQL（唯一片段；路由器的候选集
//	  查询与零候选建议列表都从它渲染）
//
// ⇒ 本文件此前抄了一份「SQL filter semantics」表（”/text→全放行、
//
//	vision→IN(vision,multimodal)、…）⇒ ★那是**第六份表达**，而且**已经
//	过期过一次**（2026-10-27 那轮把 video 补进接受臂时，这张表没跟着改，
//	直到 10-28 的审计轮才发现）⇒ ★ 「注释是事实源」的反面：**注释里抄一份
//	判据 = 判据多一个会漂的副本** ⇒ 现在删掉抄写，改为指认唯一实现。
//
// ★ 需要知道「某个请求模态会被路由到哪里」时，去读 provider/modality_accept.go
//
//	的 ModalityAccepts（它上面有完整的形态表与每一条的理由）。
//
// ★ 2026-10-26 补记（保留历史）：当时 'video' 这一行写的是 `modality = 'video'`
//
//	—— 在2026-10-26 那轮修好之前它是对的，那轮把 video 补进了分级模态门的
//	接受臂，于是这份抄本变成了过期副本。**根因就是「抄一份」**。
//
// ★ multimodal 的登记语义（text+image+audio+video）见
//
//	modelname/modality_defaults.go —— 也就是说：**登记为 multimodal 的模型
//	本来就该接 video 请求**，此前只有精确 `modality='video'` 一条臂 ⇒
//	任何带视频块的请求对全部 multimodal 模型 503（真机复现）。
const (
	modalityText       = "text"
	modalityVision     = "vision"
	modalityAudio      = "audio"
	modalityVideo      = "video"
	modalityMultimodal = "multimodal"
)

// detectRequestModality walks the OpenAI/Anthropic/Gemini-style message
// content blocks in bodyBytes and returns the routing modality string
// (text/vision/audio/video/multimodal) that GetCandidatesByModality will
// use to filter candidates.
//
// Conservative: returns "text" when it cannot conclusively prove a
// non-text modality is present, so pure-text traffic is never throttled.
// Empty/invalid bodies return "text".
//
// Detection priority: video > audio > vision. Mixed-modality requests
// (e.g. image+audio) return the highest-priority one; SQL filter will
// also accept multimodal-only providers for the matched dimension.
func detectRequestModality(bodyBytes []byte) string {
	if len(bodyBytes) == 0 {
		return modalityText
	}

	var body struct {
		Messages []json.RawMessage `json:"messages"`
		Contents []json.RawMessage `json:"contents"` // Gemini native
		// ★2026-10-28: OpenAI Responses API 的 input[]。
		//
		// 此前检测器只认 messages[] 与 Gemini 的 contents[]，于是
		// /v1/responses 的带图请求被判为 **text** ⇒ 模态门对这条路径
		// 从不生效 ⇒ 带图请求原样发给上游，由上游回 400
		// （生产实测：glm-5.3-flash 收到 image 块，上游智谱回 400，
		//   网关把它记成 provider_unavailable 503「All providers unavailable」
		//   —— 把「上游不收这个模态」说成了「所有供应商都不可用」，
		//   同样是误导，而且比模态不匹配更难排查）。
		//
		// input[] 的条目结构与 messages[] 相同（role + content），
		// 所以复用同一条遍历路径。
		Input []json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		return modalityText
	}

	hasVision, hasAudio, hasVideo := false, false, false

	// Walk each top-level message (OpenAI / Anthropic shape).
	for _, raw := range body.Messages {
		var msg struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		if len(msg.Content) == 0 {
			continue
		}
		walkContent(msg.Content, &hasVision, &hasAudio, &hasVideo)
	}

	// Walk each Responses API input[] entry. Same shape as messages[]:
	// {role, content}. A bare string entry (the "input": "hello" shorthand)
	// carries no media and is skipped by the content check.
	for _, raw := range body.Input {
		var msg struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		if len(msg.Content) == 0 {
			continue
		}
		walkContent(msg.Content, &hasVision, &hasAudio, &hasVideo)
	}

	// Walk each Gemini-native contents[] entry. parts[] inside carries
	// the typed block; we walk each part directly because Gemini parts
	// don't have an outer {content:...} wrapper.
	for _, raw := range body.Contents {
		var entry struct {
			Role  string            `json:"role"`
			Parts []json.RawMessage `json:"parts"`
		}
		if err := json.Unmarshal(raw, &entry); err != nil {
			continue
		}
		for _, p := range entry.Parts {
			walkContent(p, &hasVision, &hasAudio, &hasVideo)
		}
	}

	switch {
	case hasVideo:
		return modalityVideo
	case hasAudio:
		return modalityAudio
	case hasVision:
		return modalityVision
	default:
		return modalityText
	}
}

// walkContent inspects a single content field (string, array of blocks,
// or a single block) and updates hasVision/hasAudio/hasVideo when it
// sees a matching block type. Anthropic-style tool_result.content is
// an array nested under the outer tool_result block; walkBlock recurses
// into the source/content sub-fields.
func walkContent(raw json.RawMessage, hasVision, hasAudio, hasVideo *bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return
	}

	// Plain string → no blocks.
	if trimmed[0] == '"' {
		return
	}

	// Array of blocks.
	if trimmed[0] == '[' {
		var blocks []json.RawMessage
		if err := json.Unmarshal(raw, &blocks); err != nil {
			return
		}
		for _, b := range blocks {
			walkBlock(b, hasVision, hasAudio, hasVideo)
		}
		return
	}

	// Single object — treat as a block (covers Gemini native parts and
	// Anthropic-style single-block content).
	walkBlock(raw, hasVision, hasAudio, hasVideo)
}

// walkBlock inspects one content block. We support both typed blocks
// (OpenAI image_url, Anthropic image) and Gemini inlineData/fileData
// (which use a `mimeType` field instead of `type` to declare the
// media kind).
func walkBlock(raw json.RawMessage, hasVision, hasAudio, hasVideo *bool) {
	var block struct {
		Type       string          `json:"type"`
		Source     json.RawMessage `json:"source"`      // Anthropic image/document
		ImageURL   json.RawMessage `json:"image_url"`   // OpenAI
		InputAudio json.RawMessage `json:"input_audio"` // OpenAI
		VideoURL   json.RawMessage `json:"video_url"`   // OpenAI
		File       json.RawMessage `json:"file"`        // OpenAI file
		InlineData json.RawMessage `json:"inlineData"`  // Gemini
		FileData   json.RawMessage `json:"fileData"`    // Gemini
	}
	if err := json.Unmarshal(raw, &block); err != nil {
		return
	}

	switch strings.ToLower(block.Type) {
	case "image", "image_url", "input_image":
		*hasVision = true
		return
	case "audio", "input_audio":
		*hasAudio = true
		return
	case "video", "video_url":
		*hasVideo = true
		return
	case "file", "input_file":
		if hasFileImageHint(block.File) {
			*hasVision = true
		}
		return
	case "tool_result", "tool_use", "text", "":
		// Recurse into source/content to find nested media.
		// (Anthropic tool_result.content is an array of nested blocks.)
	default:
		// Unknown type; fall through to recurse into Source for
		// safety (Anthropic-style content blocks).
	}

	// Anthropic-style nested content (tool_result.content may be a
	// string or an array of blocks).
	if len(block.Source) > 0 {
		walkContent(block.Source, hasVision, hasAudio, hasVideo)
	}

	// Gemini native inlineData: no `type` field, only `mimeType`. We
	// derive modality from MIME prefix.
	if mime := readMimeType(block.InlineData); mime != "" {
		switch {
		case strings.HasPrefix(mime, "image/"):
			*hasVision = true
		case strings.HasPrefix(mime, "audio/"):
			*hasAudio = true
		case strings.HasPrefix(mime, "video/"):
			*hasVideo = true
		}
	}
	if mime := readMimeType(block.FileData); mime != "" {
		switch {
		case strings.HasPrefix(mime, "image/"):
			*hasVision = true
		case strings.HasPrefix(mime, "audio/"):
			*hasAudio = true
		case strings.HasPrefix(mime, "video/"):
			*hasVideo = true
		}
	}
}

// readMimeType extracts the mimeType field from a Gemini inlineData /
// fileData object. Returns "" if the field is absent or malformed.
func readMimeType(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var obj struct {
		MimeType string `json:"mimeType"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	return strings.ToLower(obj.MimeType)
}

// hasFileImageHint returns true when an OpenAI file block carries an
// image MIME hint. Used as a weak signal to prefer vision-capable
// providers; non-image MIME hints fall back to "text" routing because
// OpenAI file blocks are generic (pdf/audio/etc.) and we do not want to
// over-constrain.
func hasFileImageHint(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var f struct {
		Filename string `json:"filename"`
		MimeType string `json:"mime_type"`
		FileData string `json:"file_data"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return false
	}
	if strings.HasPrefix(strings.ToLower(f.MimeType), "image/") {
		return true
	}
	if strings.HasPrefix(strings.ToLower(f.FileData), "data:image/") {
		return true
	}
	return false
}
