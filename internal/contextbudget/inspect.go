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
	external      bool
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
		info.external = info.external || nonempty(root[key])
	}
	if prompt, ok := root["prompt"].(map[string]any); ok && nonempty(prompt["id"]) {
		info.external = true // Provider-managed prompt templates are not inline text.
	}
	for _, key := range []string{"messages", "input", "contents", "system", "systemInstruction", "system_instruction", "documents", "images", "attachments", "files"} {
		value := root[key]
		if key == "images" || key == "attachments" || key == "files" {
			info.external = info.external || nonempty(value)
		} else {
			info.external = info.external || externalContent(value)
		}
	}
	info.external = info.external || externalTools(root["tools"])
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

func externalContent(value any) bool {
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			if externalContent(item) {
				return true
			}
		}
	case map[string]any:
		typ, _ := v["type"].(string)
		switch typ {
		case "image", "image_url", "input_image", "input_audio", "audio", "audio_url", "video", "video_url", "input_video", "file", "input_file", "redacted_thinking", "item_reference":
			return true
		case "document":
			source, _ := v["source"].(map[string]any)
			if source["type"] != "text" {
				return true
			}
		}
		for key, child := range v {
			if (typ == "tool_use" || typ == "function_call") && (key == "input" || key == "arguments") {
				continue // Tool arguments describe data; the provider does not fetch it.
			}
			switch key {
			case "inlineData", "inline_data", "fileData", "file_data", "file_id", "image_url", "input_audio", "audio", "audio_url", "video_url", "images", "encrypted_content":
				if nonempty(child) {
					return true
				}
			}
			if externalContent(child) {
				return true
			}
		}
	}
	return false
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
