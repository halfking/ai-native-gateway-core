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
// SQL filter semantics (loadCandidatesByModalityDB):
//   - '' or 'text'  → no modality filter
//   - 'vision'      → modality IN ('vision', 'multimodal')
//   - 'audio'       → modality IN ('audio',  'multimodal')
//   - 'video'       → modality = 'video'
//   - 'multimodal'  → modality = 'multimodal'
//   - 'embedding'   → modality = 'embedding'

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
