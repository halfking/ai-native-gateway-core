package toolhistory

import (
	"encoding/json"
	"strings"
	"testing"
)

func fixture(protocol, calls, results string) string {
	switch protocol {
	case "chat":
		return `{"model":"m","unknown":{"integer":9007199254740993},"messages":[` + calls + results + `]}`
	case "responses":
		return `{"model":"m","input":[` + calls + results + `]}`
	default:
		return `{"model":"m","messages":[` + calls + results + `]}`
	}
}
func call(protocol, id string) string {
	switch protocol {
	case "chat":
		return `{"role":"assistant","tool_calls":[{"id":"` + id + `","type":"function","function":{"name":"Read","arguments":"{}"}}]}`
	case "responses":
		return `{"type":"function_call","call_id":"` + id + `","name":"Read","arguments":"{}"}`
	default:
		return `{"role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"Read","input":{}}]}`
	}
}
func batch(protocol string) string {
	switch protocol {
	case "chat":
		return `{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"Read","arguments":"{}"}},{"id":"b","type":"function","function":{"name":"Read","arguments":"{}"}},{"id":"c","type":"function","function":{"name":"Read","arguments":"{}"}}]}`
	case "responses":
		return call(protocol, "a") + `,` + call(protocol, "b") + `,` + call(protocol, "c")
	default:
		return `{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"Read","input":{}},{"type":"tool_use","id":"b","name":"Read","input":{}},{"type":"tool_use","id":"c","name":"Read","input":{}}]}`
	}
}
func result(protocol, id, text string) string {
	switch protocol {
	case "chat":
		return `{"role":"tool","tool_call_id":"` + id + `","content":"` + text + `","unknown":9007199254740993}`
	case "responses":
		return `{"type":"function_call_output","call_id":"` + id + `","output":"` + text + `","unknown":9007199254740993}`
	default:
		return `{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":"` + text + `","unknown":9007199254740993}]}`
	}
}
func user(protocol, text string) string {
	if protocol == "messages" {
		return `{"role":"user","content":[{"type":"text","text":"` + text + `"}]}`
	}
	return `{"role":"user","content":"` + text + `"}`
}
func resultIDs(t *testing.T, body []byte, protocol string) []string {
	t.Helper()
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatal(err)
	}
	key := "messages"
	if protocol == "responses" {
		key = "input"
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(root[key], &messages); err != nil {
		t.Fatal(err)
	}
	var ids []string
	stringOf := func(raw json.RawMessage) string { var s string; _ = json.Unmarshal(raw, &s); return s }
	for _, m := range messages {
		if protocol == "chat" && stringOf(m["role"]) == "tool" {
			ids = append(ids, stringOf(m["tool_call_id"]))
		}
		if protocol == "responses" && strings.HasSuffix(stringOf(m["type"]), "_call_output") {
			ids = append(ids, stringOf(m["call_id"]))
		}
		if protocol == "messages" {
			var blocks []map[string]json.RawMessage
			_ = json.Unmarshal(m["content"], &blocks)
			for _, b := range blocks {
				if stringOf(b["type"]) == "tool_result" {
					ids = append(ids, stringOf(b["tool_use_id"]))
				}
			}
		}
	}
	return ids
}
