package ir

import (
	"encoding/json"
	"strings"
)

// RequestCapabilities describes media requirements found in a request body.
type RequestCapabilities struct {
	HasText     bool
	HasImage    bool
	HasAudio    bool
	HasVideo    bool
	HasDocument bool
}

// PrimaryModality returns the routing modality for the request.
// Video, audio, and image requests take precedence over text. Documents
// remain text-routed until the provider schema has a document capability.
func (c RequestCapabilities) PrimaryModality() string {
	switch {
	case c.HasVideo:
		return "video"
	case c.HasAudio:
		return "audio"
	case c.HasImage:
		return "vision"
	default:
		return "text"
	}
}

// DetectRequestCapabilities scans OpenAI, Anthropic, Gemini, and Responses
// request shapes without changing the request body.
func DetectRequestCapabilities(body []byte) RequestCapabilities {
	var root map[string]json.RawMessage
	if len(body) == 0 || json.Unmarshal(body, &root) != nil {
		return RequestCapabilities{}
	}

	var caps RequestCapabilities
	walkRawArray(root["messages"], func(raw json.RawMessage) {
		var message map[string]json.RawMessage
		if json.Unmarshal(raw, &message) != nil {
			return
		}
		walkContentRaw(message["content"], &caps)
	})
	walkRawArray(root["contents"], func(raw json.RawMessage) {
		var content struct {
			Parts []json.RawMessage `json:"parts"`
		}
		if json.Unmarshal(raw, &content) != nil {
			return
		}
		for _, part := range content.Parts {
			walkBlockRaw(part, &caps)
		}
	})
	walkRawArray(root["input"], func(raw json.RawMessage) {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil {
			return
		}
		if content, ok := item["content"]; ok {
			walkContentRaw(content, &caps)
			return
		}
		walkBlockRaw(raw, &caps)
	})

	return caps
}

func walkRawArray(raw json.RawMessage, visit func(json.RawMessage)) {
	if len(raw) == 0 || string(raw) == "null" {
		return
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return
	}
	for _, item := range items {
		visit(item)
	}
}

func walkContentRaw(raw json.RawMessage, caps *RequestCapabilities) {
	if len(raw) == 0 || string(raw) == "null" {
		return
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if text != "" {
			caps.HasText = true
		}
		return
	}
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) == nil {
		for _, block := range blocks {
			walkBlockRaw(block, caps)
		}
		return
	}
	walkBlockRaw(raw, caps)
}

func walkBlockRaw(raw json.RawMessage, caps *RequestCapabilities) {
	var block map[string]json.RawMessage
	if json.Unmarshal(raw, &block) != nil {
		return
	}

	var blockType string
	_ = json.Unmarshal(block["type"], &blockType)
	switch strings.ToLower(blockType) {
	case "text", "input_text":
		caps.HasText = true
	case "image", "image_url", "input_image":
		caps.HasImage = true
	case "audio", "input_audio":
		caps.HasAudio = true
	case "video", "video_url", "input_video":
		caps.HasVideo = true
	case "document", "file", "input_file":
		caps.HasDocument = true
		if nested, ok := block["file"]; ok {
			walkFileMime(nested, caps)
		}
		if nested, ok := block["input_file"]; ok {
			walkFileMime(nested, caps)
		}
	default:
		if nested, ok := block["content"]; ok {
			walkContentRaw(nested, caps)
		}
		if nested, ok := block["source"]; ok {
			walkContentRaw(nested, caps)
		}
	}

	for _, key := range []string{"inlineData", "fileData"} {
		if nested, ok := block[key]; ok {
			walkMimeObject(nested, caps)
		}
	}
}

func walkFileMime(raw json.RawMessage, caps *RequestCapabilities) {
	var file map[string]json.RawMessage
	if json.Unmarshal(raw, &file) != nil {
		return
	}
	if mime, ok := stringField(file["mime_type"]); ok {
		markMime(mime, caps)
	}
	if data, ok := stringField(file["file_data"]); ok && strings.HasPrefix(strings.ToLower(data), "data:") {
		if comma := strings.IndexByte(data, ','); comma > len("data:") {
			markMime(strings.TrimPrefix(strings.SplitN(data[:comma], ";", 2)[0], "data:"), caps)
		}
	}
}

func walkMimeObject(raw json.RawMessage, caps *RequestCapabilities) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return
	}
	if mime, ok := stringField(obj["mimeType"]); ok {
		markMime(mime, caps)
	}
}

func stringField(raw json.RawMessage) (string, bool) {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, value != ""
}

func markMime(mime string, caps *RequestCapabilities) {
	mime = strings.ToLower(strings.TrimSpace(mime))
	switch {
	case strings.HasPrefix(mime, "image/"):
		caps.HasImage = true
	case strings.HasPrefix(mime, "audio/"):
		caps.HasAudio = true
	case strings.HasPrefix(mime, "video/"):
		caps.HasVideo = true
	case mime != "":
		caps.HasDocument = true
	}
}
