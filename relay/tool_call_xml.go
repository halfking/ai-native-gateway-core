package relay

import (
	"encoding/json"
	"html"
	"regexp"
	"strings"
	"time"
)

var (
	xmlToolCallRE = regexp.MustCompile(`(?s)<tool_call>\s*<function=([A-Za-z_][\w.-]*)>(.*?)</function>\s*</tool_call>`)
	xmlParamRE    = regexp.MustCompile(`(?s)<parameter=([A-Za-z_][\w.-]*)>(.*?)</parameter>`)

	// minimaxStyleRE matches the MiniMax M2.7 tool-call XML shape:
	//   <minimax:tool_call>
	//   <invoke name="func">
	//   <parameter name="arg">value</parameter>
	//   </invoke>
	//   </minimax:tool_call>
	// We saw this in production request_logs id 30089 (2026-06-11) when
	// MiniMax M2.7 falls back to XML when its native tool_use wire format
	// is unavailable.
	minimaxToolCallRE = regexp.MustCompile(`(?s)<minimax:tool_call>\s*<invoke\s+name="([A-Za-z_][\w.-]*)">(.*?)</invoke>\s*</minimax:tool_call>`)
	minimaxParamRE    = regexp.MustCompile(`(?s)<parameter\s+name="([A-Za-z_][\w.-]*)">(.*?)</parameter>`)
)

// CoerceXMLToolCallsInChatResponse is the exported alias used by
// cmd/gateway/main.go to wire XML tool-call coercion into the routing
// executor's non-streaming response post-processor.  See
// coerceXMLToolCallsInChatResponse for the implementation.
func CoerceXMLToolCallsInChatResponse(body []byte, toolsRequested bool) []byte {
	return coerceXMLToolCallsInChatResponse(body, toolsRequested)
}

func coerceXMLToolCallsInChatResponse(body []byte, toolsRequested bool) []byte {
	// Match either the Xiaomi MiMo/generic <tool_call><function=...> shape
	// or the MiniMax M2.7 <minimax:tool_call> shape before doing any
	// JSON parsing — the per-shape regex itself only runs in parseXMLToolCalls.
	if !toolsRequested {
		return body
	}
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "<tool_call>") && !strings.Contains(bodyStr, "<minimax:tool_call>") {
		return body
	}
	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		return body
	}
	choices, ok := resp["choices"].([]any)
	if !ok {
		return body
	}
	modified := false
	for _, rawChoice := range choices {
		choice, ok := rawChoice.(map[string]any)
		if !ok {
			continue
		}
		message, ok := choice["message"].(map[string]any)
		if !ok || message["tool_calls"] != nil {
			continue
		}
		content, ok := message["content"].(string)
		if !ok {
			continue
		}
		remaining, toolCalls := parseXMLToolCalls(content)
		if len(toolCalls) == 0 {
			continue
		}
		if remaining == "" {
			message["content"] = nil
		} else {
			message["content"] = remaining
		}
		message["tool_calls"] = toolCalls
		choice["finish_reason"] = "tool_calls"
		modified = true
	}
	if !modified {
		return body
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return body
	}
	return out
}

func coerceXMLToolCallsInStreamLine(line string, toolsRequested bool) string {
	if !toolsRequested || !strings.HasPrefix(line, "data: ") {
		return line
	}
	if !strings.Contains(line, "<tool_call>") && !strings.Contains(line, "<minimax:tool_call>") {
		return line
	}
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
	if payload == "[DONE]" {
		return line
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		return line
	}
	choices, ok := obj["choices"].([]any)
	if !ok {
		return line
	}
	modified := false
	for _, rawChoice := range choices {
		choice, ok := rawChoice.(map[string]any)
		if !ok {
			continue
		}
		delta, ok := choice["delta"].(map[string]any)
		if !ok || delta["tool_calls"] != nil {
			continue
		}
		content, ok := delta["content"].(string)
		if !ok {
			continue
		}
		remaining, toolCalls := parseXMLToolCalls(content)
		if len(toolCalls) == 0 {
			continue
		}
		if remaining == "" {
			delete(delta, "content")
		} else {
			delta["content"] = remaining
		}
		for idx, toolCall := range toolCalls {
			toolCall["index"] = idx
		}
		delta["tool_calls"] = toolCalls
		choice["finish_reason"] = "tool_calls"
		modified = true
	}
	if !modified {
		return line
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return line
	}
	return "data: " + string(out) + "\n"
}

func parseXMLToolCalls(text string) (string, []map[string]any) {
	// Try the Xiaomi MiMo / generic shape first.
	if strings.Contains(text, "<tool_call>") && strings.Contains(text, "<function=") {
		if remaining, calls := parseXMLWith(text, xmlToolCallRE, xmlParamRE); len(calls) > 0 {
			return remaining, calls
		}
	}
	// Fall back to the MiniMax M2.7 shape: <minimax:tool_call>...<invoke name="X">...</invoke>...</minimax:tool_call>.
	if strings.Contains(text, "<minimax:tool_call>") && strings.Contains(text, "<invoke ") {
		if remaining, calls := parseXMLWith(text, minimaxToolCallRE, minimaxParamRE); len(calls) > 0 {
			return remaining, calls
		}
	}
	return text, nil
}

// parseXMLWith runs the supplied tool-call + parameter regexes against
// text and returns the leading/trailing free text plus the parsed calls.
// It is the shared inner loop for parseXMLToolCalls.
func parseXMLWith(text string, callRE, paramRE *regexp.Regexp) (string, []map[string]any) {
	matches := callRE.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text, nil
	}
	var toolCalls []map[string]any
	var builder strings.Builder
	cursor := 0
	for i, match := range matches {
		builder.WriteString(text[cursor:match[0]])
		cursor = match[1]
		name := strings.TrimSpace(text[match[2]:match[3]])
		body := text[match[4]:match[5]]
		params := map[string]any{}
		for _, pm := range paramRE.FindAllStringSubmatchIndex(body, -1) {
			key := strings.TrimSpace(body[pm[2]:pm[3]])
			value := strings.TrimSpace(html.UnescapeString(body[pm[4]:pm[5]]))
			params[key] = value
		}
		args, _ := json.Marshal(params)
		toolCalls = append(toolCalls, map[string]any{
			"id":   strings.ReplaceAll("call_"+time.Now().UTC().Format("20060102150405.000000000")+"_"+string(rune('a'+i)), ".", ""),
			"type": "function",
			"function": map[string]any{
				"name":      name,
				"arguments": string(args),
			},
		})
	}
	builder.WriteString(text[cursor:])
	return strings.TrimSpace(builder.String()), toolCalls
}
