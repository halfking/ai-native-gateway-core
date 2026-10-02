package sanitize

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const maxNativeToolInputDepth = 256

func hasReservedPlaceholder(body []byte) bool {
	if bytes.Contains(body, []byte("{SENSITIVE:")) {
		return true
	}
	if !bytes.Contains(body, []byte("\\u")) {
		return false
	}
	var decoded any
	if err := json.Unmarshal(body, &decoded); err == nil {
		// Arguments can be JSON serialized inside an outer string; inspect
		// those strings as well as ordinary JSON values.
		return containsResidualPlaceholder(decoded, 0)
	}
	return looksLikeUnparsedMarker(body)
}

// A recognized response may carry opaque vendor or media fields. We leave
// those fields untouched, but never let an internal marker survive in the
// final client response. Tool argument strings get one extra escape probe
// because their JSON is itself embedded as a string in the outer response.
func containsResidualPlaceholder(value any, depth int) bool {
	if depth > maxNativeToolInputDepth {
		return true
	}
	switch typed := value.(type) {
	case string:
		return strings.Contains(typed, "{SENSITIVE:") || looksLikeUnparsedMarker([]byte(typed))
	case json.Number:
		return false
	case map[string]any:
		for key, child := range typed {
			if strings.Contains(key, "{SENSITIVE:") || containsResidualPlaceholder(child, depth+1) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsResidualPlaceholder(child, depth+1) {
				return true
			}
		}
	}
	return false
}

func restoreNativeTextField(ctx context.Context, s *Sanitizer, object map[string]any, field string, sm SanitizeMap) (bool, error) {
	value, ok := object[field].(string)
	if !ok || !strings.Contains(value, "{SENSITIVE:") {
		return false, nil
	}
	restored, err := s.RestoreOutputOrMask(ctx, value, sm)
	if err != nil {
		return false, err
	}
	if strings.Contains(restored, "{SENSITIVE:") {
		return false, fmt.Errorf("sanitize restore: incomplete placeholder in %s", field)
	}
	if restored == value {
		return false, nil
	}
	object[field] = restored
	return true, nil
}

func (it *SanitizeRestoreInterceptor) restoreNativeMessagesBody(ctx context.Context, root map[string]any, sm SanitizeMap) (bool, bool, error) {
	if typ, _ := root["type"].(string); typ != "message" {
		return false, false, nil
	}
	changed := false
	if didChange, err := restoreNativeTextField(ctx, it.sanitizer, root, "refusal", sm); err != nil {
		return false, true, err
	} else {
		changed = changed || didChange
	}
	if content, ok := root["content"].([]any); ok {
		didChange, err := it.restoreNativeContentBlocks(ctx, content, sm)
		return changed || didChange, true, err
	}
	if didChange, err := restoreNativeTextField(ctx, it.sanitizer, root, "content", sm); err != nil {
		return false, true, err
	} else {
		changed = changed || didChange
	}
	return changed, true, nil
}

func (it *SanitizeRestoreInterceptor) restoreNativeResponsesBody(ctx context.Context, root map[string]any, sm SanitizeMap) (bool, bool, error) {
	if object, _ := root["object"].(string); object != "response" {
		return false, false, nil
	}
	changed, err := restoreNativeTextField(ctx, it.sanitizer, root, "output_text", sm)
	if err != nil {
		return false, true, err
	}
	output, _ := root["output"].([]any)
	for _, rawItem := range output {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := item["type"].(string)
		switch typ {
		case "message":
			if didChange, err := restoreNativeTextField(ctx, it.sanitizer, item, "refusal", sm); err != nil {
				return false, true, err
			} else {
				changed = changed || didChange
			}
			if content, ok := item["content"].([]any); ok {
				didChange, err := it.restoreNativeContentBlocks(ctx, content, sm)
				if err != nil {
					return false, true, err
				}
				changed = changed || didChange
			}
		case "function_call":
			didChange, err := it.restoreNativeArgumentField(ctx, item, "arguments", sm, true)
			if err != nil {
				return false, true, err
			}
			changed = changed || didChange
		case "custom_tool_call":
			didChange, err := it.restoreNativeArgumentField(ctx, item, "input", sm, false)
			if err != nil {
				return false, true, err
			}
			changed = changed || didChange
		case "reasoning":
			if content, ok := item["content"].([]any); ok {
				didChange, err := it.restoreNativeContentBlocks(ctx, content, sm)
				if err != nil {
					return false, true, err
				}
				changed = changed || didChange
			}
			if summary, ok := item["summary"].([]any); ok {
				didChange, err := it.restoreNativeContentBlocks(ctx, summary, sm)
				if err != nil {
					return false, true, err
				}
				changed = changed || didChange
			}
		case "mcp_call":
			didChange, err := it.restoreNativeArgumentField(ctx, item, "arguments", sm, false)
			if err != nil {
				return false, true, err
			}
			changed = changed || didChange
			didChange, err = restoreNativeTextField(ctx, it.sanitizer, item, "output", sm)
			if err != nil {
				return false, true, err
			}
			changed = changed || didChange
		case "web_search_call":
			if action, ok := item["action"].(map[string]any); ok {
				didChange, err := restoreNativeTextField(ctx, it.sanitizer, action, "query", sm)
				if err != nil {
					return false, true, err
				}
				changed = changed || didChange
				if results, ok := action["results"]; ok {
					// 输入侧 action.results 口径的镜像：字符串数组/{text} 对象
					// 数组逐值还原（第三十一轮 §四#7）。
					restored, didChange, err := restoreNativeNestedValue(ctx, it.sanitizer, results, sm, 0)
					if err != nil {
						return false, true, err
					}
					if didChange {
						action["results"] = restored
						changed = true
					}
				}
			}
		case "tool_search_call":
			// function_call.arguments 的单字段同族（第三十一轮 §四#7）：
			// JSON-string 形态，非法 JSON 拒绝还原（requireJSON=true）。
			didChange, err := it.restoreNativeArgumentField(ctx, item, "arguments", sm, true)
			if err != nil {
				return false, true, err
			}
			changed = changed || didChange
		case "mcp_approval_request":
			// mcp_call.arguments 的单字段同族（第三十一轮 §四#7）：非法 JSON
			// 退化为整串文本还原（requireJSON=false）。
			didChange, err := it.restoreNativeArgumentField(ctx, item, "arguments", sm, false)
			if err != nil {
				return false, true, err
			}
			changed = changed || didChange
		case "file_search_call":
			for _, field := range []string{"queries", "results"} {
				value, ok := item[field]
				if !ok {
					continue
				}
				restored, didChange, err := restoreNativeNestedValue(ctx, it.sanitizer, value, sm, 0)
				if err != nil {
					return false, true, err
				}
				if didChange {
					item[field] = restored
					changed = true
				}
			}
		case "local_shell_call_output", "apply_patch_call_output":
			didChange, err := restoreNativeTextField(ctx, it.sanitizer, item, "output", sm)
			if err != nil {
				return false, true, err
			}
			changed = changed || didChange
		case "shell_call_output", "code_interpreter_call":
			for _, field := range []string{"outputs", "code"} {
				value, ok := item[field]
				if !ok {
					continue
				}
				restored, didChange, err := restoreNativeNestedValue(ctx, it.sanitizer, value, sm, 0)
				if err != nil {
					return false, true, err
				}
				if didChange {
					item[field] = restored
					changed = true
				}
			}
		case "apply_patch_call":
			if action, ok := item["action"].(map[string]any); ok {
				didChange, err := restoreNativeTextField(ctx, it.sanitizer, action, "content", sm)
				if err != nil {
					return false, true, err
				}
				changed = changed || didChange
			}
		case "shell_call", "local_shell_call", "computer_call":
			// 请求侧配对项：action 子树（command[]/text/env）与输入侧
			// sanitizeShellActionCallItem 镜像，递归恢复全部字符串叶子
			//（第三十轮，2026-10-02）。
			if action, ok := item["action"]; ok {
				restored, didChange, err := restoreNativeNestedValue(ctx, it.sanitizer, action, sm, 0)
				if err != nil {
					return false, true, err
				}
				if didChange {
					item["action"] = restored
					changed = true
				}
			}
		}
	}
	return changed, true, nil
}

func (it *SanitizeRestoreInterceptor) restoreNativeContentBlocks(ctx context.Context, blocks []any, sm SanitizeMap) (bool, error) {
	changed := false
	for _, rawBlock := range blocks {
		block, ok := rawBlock.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := block["type"].(string)
		switch typ {
		case "text", "input_text", "output_text", "reasoning_text", "summary_text":
			didChange, err := restoreNativeTextField(ctx, it.sanitizer, block, "text", sm)
			if err != nil {
				return false, err
			}
			changed = changed || didChange
		case "refusal":
			for _, field := range []string{"refusal", "text"} {
				didChange, err := restoreNativeTextField(ctx, it.sanitizer, block, field, sm)
				if err != nil {
					return false, err
				}
				changed = changed || didChange
			}
		case "thinking":
			thinking, _ := block["thinking"].(string)
			if signature, _ := block["signature"].(string); signature != "" && strings.Contains(thinking, "{SENSITIVE:") {
				return false, errors.New("sanitize restore: signed thinking contains placeholder")
			}
			didChange, err := restoreNativeTextField(ctx, it.sanitizer, block, "thinking", sm)
			if err != nil {
				return false, err
			}
			changed = changed || didChange
		case "tool_use":
			if _, ok := block["input"].(string); ok {
				didChange, err := it.restoreNativeArgumentField(ctx, block, "input", sm, false)
				if err != nil {
					return false, err
				}
				changed = changed || didChange
			} else if input, exists := block["input"]; exists {
				_, didChange, err := restoreNativeNestedValue(ctx, it.sanitizer, input, sm, 0)
				if err != nil {
					return false, err
				}
				changed = changed || didChange
			}
		case "output_audio":
			didChange, err := restoreNativeTextField(ctx, it.sanitizer, block, "transcript", sm)
			if err != nil {
				return false, err
			}
			changed = changed || didChange
		}
	}
	return changed, nil
}

// Function-call arguments are a JSON string: restore its values after parsing,
// then serialize again so quotes/backslashes in plaintext remain valid JSON.
// Custom tool input may instead be freeform text, which is restored directly.
func (it *SanitizeRestoreInterceptor) restoreNativeArgumentField(ctx context.Context, object map[string]any, field string, sm SanitizeMap, requireJSON bool) (bool, error) {
	value, ok := object[field].(string)
	if !ok || !hasReservedPlaceholder([]byte(value)) {
		return false, nil
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var decoded any
	err := decoder.Decode(&decoded)
	if err == nil {
		var trailing any
		if trailingErr := decoder.Decode(&trailing); trailingErr != io.EOF {
			err = fmt.Errorf("sanitize restore: trailing tool argument JSON: %v", trailingErr)
		}
	}
	if err != nil {
		if requireJSON {
			return false, fmt.Errorf("sanitize restore: invalid tool argument JSON: %w", err)
		}
		return restoreNativeTextField(ctx, it.sanitizer, object, field, sm)
	}
	var changed bool
	decoded, changed, err = restoreNativeNestedValue(ctx, it.sanitizer, decoded, sm, 0)
	if err != nil || !changed {
		return false, err
	}
	serialized, err := json.Marshal(decoded)
	if err != nil {
		return false, err
	}
	object[field] = string(serialized)
	return true, nil
}

func restoreNativeNestedValue(ctx context.Context, s *Sanitizer, value any, sm SanitizeMap, depth int) (any, bool, error) {
	if depth > maxNativeToolInputDepth {
		return nil, false, errors.New("sanitize restore: tool input nesting exceeds limit")
	}
	switch current := value.(type) {
	case string:
		if !strings.Contains(current, "{SENSITIVE:") {
			return current, false, nil
		}
		restored, err := s.RestoreOutputOrMask(ctx, current, sm)
		if err != nil {
			return nil, false, err
		}
		if strings.Contains(restored, "{SENSITIVE:") {
			return nil, false, errors.New("sanitize restore: incomplete placeholder in tool input")
		}
		return restored, restored != current, nil
	case map[string]any:
		if nativeMediaObject(current) {
			return current, false, nil
		}
		changed := false
		for key, child := range current {
			if strings.Contains(key, "{SENSITIVE:") {
				return nil, false, errors.New("sanitize restore: placeholder in tool input key")
			}
			restored, didChange, err := restoreNativeNestedValue(ctx, s, child, sm, depth+1)
			if err != nil {
				return nil, false, err
			}
			if didChange {
				current[key] = restored
				changed = true
			}
		}
		return current, changed, nil
	case []any:
		changed := false
		for index, child := range current {
			restored, didChange, err := restoreNativeNestedValue(ctx, s, child, sm, depth+1)
			if err != nil {
				return nil, false, err
			}
			if didChange {
				current[index] = restored
				changed = true
			}
		}
		return current, changed, nil
	default:
		return value, false, nil
	}
}

func nativeMediaObject(object map[string]any) bool {
	typ, _ := object["type"].(string)
	switch typ {
	case "image", "image_url", "input_image", "output_image", "audio", "input_audio", "output_audio", "video", "file", "input_file", "document":
		return true
	}
	return false
}
