package contextbudget

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

type inspection struct {
	nodes         int
	outputReserve int
	// external: the body references or carries anything beyond plain text.
	external bool
	// remote: external state the gateway cannot see the bytes of (URLs to
	// fetch, file ids, provider-managed state). Remote external input needs a
	// trusted token bound or it fails closed. Inline media (data: URLs /
	// inline base64) is external but NOT remote — the gateway sees every byte
	// and the calibrated estimator meters it through the blob branch.
	remote bool
}

func invalid(detail string) error { return &Error{Kind: InvalidRequest, Detail: detail} }

func inspect(body []byte) (inspection, error) {
	var info inspection
	if len(body) > DefaultMaxJSONBytes || !utf8.Valid(body) {
		return info, invalid("invalid UTF-8 or inspection capacity exceeded")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	v, err := readValue(dec, 0, &info.nodes)
	if err != nil {
		return info, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return info, invalid("trailing JSON data")
	}
	root, ok := v.(map[string]any)
	if !ok {
		return info, invalid("request must be a JSON object")
	}
	for _, key := range []string{"max_tokens", "max_output_tokens", "max_completion_tokens", "num_predict"} {
		if raw, exists := root[key]; exists {
			n, err := nonnegativeInt(raw)
			if err != nil {
				return info, err
			}
			info.outputReserve = max(info.outputReserve, n)
		}
	}
	for key, fields := range map[string][]string{"generationConfig": {"maxOutputTokens"}, "generation_config": {"max_output_tokens"}, "thinking": {"budget_tokens"}, "options": {"num_predict"}} {
		if block, ok := root[key].(map[string]any); ok {
			for _, field := range fields {
				if raw, exists := block[field]; exists {
					n, err := nonnegativeInt(raw)
					if err != nil {
						return info, err
					}
					info.outputReserve = max(info.outputReserve, n)
				}
			}
		}
	}
	for _, key := range []string{"previous_response_id", "conversation", "cachedContent", "cached_content", "cache_id"} {
		if nonempty(root[key]) {
			info.external = true
			info.remote = true // provider-held conversation state the gateway cannot count
		}
	}
	if prompt, ok := root["prompt"].(map[string]any); ok && nonempty(prompt["id"]) {
		info.external = true // Provider-managed prompt templates are not inline text.
		info.remote = true
	}
	for _, key := range []string{"messages", "input", "contents", "system", "systemInstruction", "system_instruction", "documents", "images", "attachments", "files"} {
		value := root[key]
		if key == "images" || key == "attachments" || key == "files" {
			// 2026-11-14：顶层 images/attachments/files 是无 type 的 ad-hoc 形状，
			// 元素可能是 URL 也可能是内联数据——无法可靠区分 ⇒ 保持 fail-closed。
			if nonempty(value) {
				info.external = true
				info.remote = true
			}
		} else {
			inline, remote := classifyExternal(value)
			info.external = info.external || inline || remote
			info.remote = info.remote || remote
		}
	}
	if externalTools(root["tools"]) {
		info.external = true
		info.remote = true // upstream fetches/executes these; bytes never transit the gateway
	}
	return info, nil
}

func nonnegativeInt(v any) (int, error) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, invalid("output token reservation must be an integer")
	}
	value, err := strconv.ParseInt(string(n), 10, strconv.IntSize)
	if err != nil || value < 0 {
		return 0, invalid("output token reservation is invalid")
	}
	return int(value), nil
}

// Token traversal rejects duplicate keys and limits both nesting and syntax
// nodes. Decoder.Decode(&wholeRequest) would hide duplicate-key ambiguity.
func readValue(dec *json.Decoder, depth int, nodes *int) (any, error) {
	*nodes++
	if depth > 128 || *nodes > 100000 {
		return nil, invalid("JSON structure exceeds inspection capacity")
	}
	token, err := dec.Token()
	if err != nil {
		return nil, invalid("invalid JSON")
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return token, nil
	}
	switch delim {
	case '{':
		object := make(map[string]any)
		for dec.More() {
			token, err := dec.Token()
			key, ok := token.(string)
			if err != nil || !ok {
				return nil, invalid("invalid JSON object key")
			}
			if _, exists := object[key]; exists {
				return nil, invalid("duplicate JSON object key")
			}
			value, err := readValue(dec, depth+1, nodes)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		if end, err := dec.Token(); err != nil || end != json.Delim('}') {
			return nil, invalid("unterminated JSON object")
		}
		return object, nil
	case '[':
		var array []any
		for dec.More() {
			value, err := readValue(dec, depth+1, nodes)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		if end, err := dec.Token(); err != nil || end != json.Delim(']') {
			return nil, invalid("unterminated JSON array")
		}
		return array, nil
	default:
		return nil, invalid("unexpected JSON delimiter")
	}
}

func nonempty(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case string:
		return v != ""
	case []any:
		return len(v) != 0
	case map[string]any:
		return len(v) != 0
	default:
		return true
	}
}

// classifyExternal walks a content value and splits external state into the
// two classes that CheckWithCounter must treat differently:
//
//	inline  — the gateway sees every byte (data: URLs, inline base64 payloads).
//	          The calibrated estimator meters these through its blob branch
//	          (≥4KiB no-whitespace strings at bytes/5), so they are countable
//	          without any external bound. Over-counting is the known direction:
//	          vision tokens cost ~1k for a photo whose base64 blob estimates
//	          tens of thousands — conservative, never under.
//	remote  — the gateway cannot see the bytes (http(s) media URLs, file ids,
//	          provider-managed conversation/prompt/reasoning state). A trusted
//	          bound is required or the request fails closed.
//
// 2026-11-13（211 实证，ZCode 报障 gpt-6 图片 422）：旧实现把 data: URL 与
// https URL 一起 fail-closed（"media or remote context requires a trusted
// token bound"），而 ExternalInputTokens 全仓没有生产填充点 ⇒ **任何**直发
// 带图请求必 422——modality 门放行了也没用。安全不变量的原文是「remote
// state the gateway cannot see」fail-closed；inline 数据不是 remote state，
// 把它一起拒掉是分类错误，不是安全语义。
func classifyExternal(value any) (inline, remote bool) {
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			i, r := classifyExternal(item)
			inline, remote = inline || i, remote || r
			if inline && remote {
				return true, true
			}
		}
	case map[string]any:
		typ, _ := v["type"].(string)
		switch typ {
		case "image", "input_image":
			// Anthropic image: source{type: base64|url}; Responses input_image:
			// image_url string / file_id.
			if i, r := classifySource(v["source"]); i || r {
				return i, r
			}
			i, r := classifyImageURLValue(v["image_url"])
			if i || r {
				return i, r
			}
			if nonempty(v["file_id"]) {
				return false, true
			}
		case "image_url":
			// OpenAI chat: {"type":"image_url","image_url":{"url":...}}.
			return classifyImageURLValue(v["image_url"])
		case "input_audio", "audio":
			// Inline base64 payload {"input_audio":{"data":...,"format":...}}
			// is gateway-visible; {"audio":{"id":...}} is provider state.
			if payload, ok := v["input_audio"].(map[string]any); ok && nonempty(payload["data"]) {
				return true, false
			}
			if nonempty(v["data"]) && typ == "input_audio" {
				return true, false
			}
			if nonempty(v["id"]) {
				return false, true
			}
		case "audio_url", "video", "video_url", "input_video":
			for _, key := range []string{"url", "audio_url", "video_url", "video", "input_video"} {
				if i, r := classifyImageURLValue(v[key]); i || r {
					return i, r
				}
			}
		case "file", "input_file":
			// 2026-11-14 audit B-P2-4: OpenAI Responses `input_file.file_data`
			// is an INLINE base64 data URL (bytes visible to the gateway),
			// not provider-managed state — only `file_id` is remote. Routing a
			// data: URL here classified the request remote ⇒ Uncountable 422
			// for every inline-file request, the r1052 incident shape's
			// responses-file sibling (same class as image_url data: URLs,
			// which classify inline below).
			if nonempty(v["file_id"]) {
				return false, true
			}
			if i, r := classifyImageURLValue(v["file_data"]); i || r {
				return i, r
			}
			if i, r := classifySource(v["source"]); i || r {
				return i, r
			}
		case "document":
			return classifySource(v["source"])
		case "redacted_thinking", "item_reference":
			// Opaque provider-managed state; the gateway cannot interpret it.
			return false, true
		case "reasoning":
			if nonempty(v["encrypted_content"]) {
				return false, true
			}
		}
		for key, child := range v {
			if (typ == "tool_use" || typ == "function_call") && (key == "input" || key == "arguments") {
				continue // Tool arguments describe data; the provider does not fetch it.
			}
			switch key {
			case "inlineData", "inline_data":
				// Gemini inline bytes (base64 in "data") are gateway-visible.
				if nonempty(child) {
					inline = true
				}
			case "fileData", "file_data", "file_id", "images":
				// images（嵌套 ad-hoc 形状，元素形态不可靠）保持 fail-closed。
				if nonempty(child) {
					remote = true
				}
			case "input_audio", "audio":
				i, r := classifyAudioValue(child)
				inline, remote = inline || i, remote || r
			case "audio_url", "video_url":
				i, r := classifyImageURLValue(child)
				inline, remote = inline || i, remote || r
			case "image_url":
				i, r := classifyImageURLValue(child)
				inline, remote = inline || i, remote || r
			case "encrypted_content":
				if nonempty(child) {
					remote = true
				}
			}
			i, r := classifyExternal(child)
			inline, remote = inline || i, remote || r
		}
	}
	return inline, remote
}

// classifyImageURLValue classifies an image_url payload: a data: URL carries
// its bytes inline; anything else (http(s) URL, bare id, unknown shape) is
// remote because the upstream fetches or resolves it outside the gateway's view.
func classifyImageURLValue(value any) (inline, remote bool) {
	switch v := value.(type) {
	case string:
		if v == "" {
			return false, false
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(v)), "data:") {
			return true, false
		}
		return false, true
	case map[string]any:
		return classifyImageURLValue(v["url"])
	case []any:
		for _, item := range v {
			i, r := classifyImageURLValue(item)
			inline, remote = inline || i, remote || r
		}
		return inline, remote
	}
	if nonempty(value) {
		return false, true
	}
	return false, false
}

// classifyAudioValue classifies an audio payload: inline base64 data is
// gateway-visible; provider ids / URLs are remote state.
func classifyAudioValue(value any) (inline, remote bool) {
	m, ok := value.(map[string]any)
	if !ok {
		if nonempty(value) {
			return false, true
		}
		return false, false
	}
	if s, ok := m["data"].(string); ok && s != "" {
		return true, false
	}
	if nonempty(m["id"]) || nonempty(m["url"]) {
		return false, true
	}
	if nonempty(m) {
		return false, true // unknown shape — fail closed
	}
	return false, false
}

// classifySource classifies Anthropic-style source blocks
// ({type: base64, data} = inline; {type: url, url} = remote; {type: text} = text).
func classifySource(source any) (inline, remote bool) {
	m, ok := source.(map[string]any)
	if !ok {
		if nonempty(source) {
			return false, true
		}
		return false, false
	}
	switch m["type"] {
	case "base64":
		if nonempty(m["data"]) {
			return true, false
		}
		return false, false
	case "url":
		return classifyImageURLValue(m["url"])
	case "text":
		return false, false
	}
	if nonempty(m) {
		return false, true // unknown source shape — fail closed
	}
	return false, false
}

func externalTools(value any) bool {
	tools, _ := value.([]any)
	for _, tool := range tools {
		definition, _ := tool.(map[string]any)
		typ, _ := definition["type"].(string)
		for _, prefix := range []string{"web_search", "web_fetch", "file_search", "code_interpreter", "code_execution", "mcp"} {
			if strings.HasPrefix(typ, prefix) {
				return true
			}
		}
		for _, key := range []string{"googleSearch", "google_search", "googleSearchRetrieval", "retrieval", "urlContext", "url_context"} {
			if _, exists := definition[key]; exists {
				return true
			}
		}
	}
	return false
}
