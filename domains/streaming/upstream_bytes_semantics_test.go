package streaming

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
)

type upstreamBytesToolCall struct {
	ID, Name, Arguments string
	Input               json.RawMessage
	Order               int
}

// Inspect actual protocol fields, including exact argument/result values and
// call→result→follow-up ordering. Marker text in unknown fields cannot satisfy
// this oracle, and a golden update cannot approve a partially lost tool round.
func upstreamBytesToolRound(body []byte, protocol string) error {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return err
	}
	conversation := envelope["messages"]
	if protocol == "responses" {
		conversation = envelope["input"]
	}
	// Wire snake_case names do not match Go field names automatically.
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(conversation, &items); err != nil {
		return err
	}
	calls := map[string]upstreamBytesToolCall{}
	results := map[string]string{}
	resultOrder, followOrder := -1, -1
	order := 0
	text := func(raw json.RawMessage) string { var value string; _ = json.Unmarshal(raw, &value); return value }
	for _, item := range items {
		order++
		role, typ := text(item["role"]), text(item["type"])
		switch protocol {
		case "chat":
			var toolCalls []struct {
				ID       string
				Function struct{ Name, Arguments string }
			}
			_ = json.Unmarshal(item["tool_calls"], &toolCalls)
			for _, tc := range toolCalls {
				calls[tc.ID] = upstreamBytesToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments, Order: order}
			}
			if role == "tool" {
				results[text(item["tool_call_id"])] = text(item["content"])
				resultOrder = order
			}
		case "responses":
			if typ == "function_call" {
				id := text(item["call_id"])
				calls[id] = upstreamBytesToolCall{ID: id, Name: text(item["name"]), Arguments: text(item["arguments"]), Order: order}
			}
			if typ == "function_call_output" {
				results[text(item["call_id"])] = text(item["output"])
				resultOrder = order
			}
		case "messages":
			var blocks []map[string]json.RawMessage
			_ = json.Unmarshal(item["content"], &blocks)
			for _, block := range blocks {
				order++
				switch text(block["type"]) {
				case "tool_use":
					id := text(block["id"])
					calls[id] = upstreamBytesToolCall{ID: id, Name: text(block["name"]), Input: block["input"], Order: order}
				case "tool_result":
					results[text(block["tool_use_id"])] = text(block["content"])
					resultOrder = order
				case "text":
					if role == "user" && text(block["text"]) == "follow-up 😀" {
						followOrder = order
					}
				}
			}
		}
		if role == "user" && text(item["content"]) == "follow-up 😀" {
			followOrder = order
		}
		var blocks []map[string]json.RawMessage
		_ = json.Unmarshal(item["content"], &blocks)
		for _, block := range blocks {
			if role == "user" && text(block["text"]) == "follow-up 😀" {
				followOrder = order
			}
		}
	}
	call, exists := calls["call_1"]
	if !exists || call.Name != "Read" {
		return fmt.Errorf("actual Read/call_1 tool call lost: %+v", calls)
	}
	arguments := call.Input
	if arguments == nil {
		arguments = []byte(call.Arguments)
	}
	var args map[string]any
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.UseNumber()
	if err := decoder.Decode(&args); err != nil {
		return fmt.Errorf("tool arguments invalid: %s", arguments)
	}
	if args["path"] != "/a b/漢字" || args["n"] != json.Number("9007199254740993") {
		return fmt.Errorf("tool arguments changed: %s", arguments)
	}
	if results["call_1"] != "tool-result \\\"quoted\\\"\nline" {
		return fmt.Errorf("tool result changed: %q", results["call_1"])
	}
	if !(call.Order < resultOrder && resultOrder < followOrder) {
		return fmt.Errorf("tool round order invalid: call=%d result=%d follow=%d", call.Order, resultOrder, followOrder)
	}
	return nil
}

func upstreamBytesClientTerminal(rec *httptest.ResponseRecorder, protocol string, streamed bool) error {
	body := rec.Body.String()
	if !strings.Contains(body, "ok") || strings.Contains(body, "\"error\"") || strings.Contains(body, "response.failed") {
		return fmt.Errorf("invalid client response: %s", body)
	}
	if !streamed {
		if !json.Valid(rec.Body.Bytes()) {
			return fmt.Errorf("invalid client JSON: %s", body)
		}
		var envelope map[string]json.RawMessage
		_ = json.Unmarshal(rec.Body.Bytes(), &envelope)
		required := map[string][]string{"chat": {"choices"}, "responses": {"output", "status"}, "messages": {"content", "stop_reason"}}[protocol]
		for _, key := range required {
			if len(envelope[key]) == 0 {
				return fmt.Errorf("client %s missing %s: %s", protocol, key, body)
			}
		}
		return nil
	}
	terminal := map[string]string{"chat": "data: [DONE]", "responses": "event: response.completed", "messages": "event: message_stop"}[protocol]
	if strings.Count(body, terminal) != 1 {
		return fmt.Errorf("missing/duplicate client terminal %q: %s", terminal, body)
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data != "[DONE]" && !json.Valid([]byte(data)) {
				return fmt.Errorf("invalid SSE data: %s", data)
			}
		}
	}
	return nil
}
