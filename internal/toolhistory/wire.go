package toolhistory

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type wireCall struct{ id, kind string }
type wirePart struct {
	raw    json.RawMessage
	id     string
	result bool
}
type wireEntry struct {
	duplicateContent bool
	raw              json.RawMessage
	role             string
	calls            []wireCall
	parts            []wirePart
}

func appendAnswers(out, answers []json.RawMessage, protocol string) []json.RawMessage {
	if protocol == "messages" {
		return append(out, append(append([]byte(`{"role":"user","content":`), rawArray(answers)...), '}'))
	}
	return append(out, answers...)
}

func readEntry(raw json.RawMessage, protocol string) wireEntry {
	e := wireEntry{raw: raw}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return e
	}
	e.role = rawString(obj["role"])
	kind := rawString(obj["type"])
	if protocol == "responses" {
		if kind == "function_call" || kind == "custom_tool_call" {
			e.calls = []wireCall{{rawString(obj["call_id"]), kind}}
			return e
		}
		if kind == "function_call_output" || kind == "custom_tool_call_output" {
			e.parts = []wirePart{{raw, rawString(obj["call_id"]), true}}
			return e
		}
	}
	if protocol == "chat" {
		if e.role == "assistant" {
			var calls []map[string]json.RawMessage
			_ = json.Unmarshal(obj["tool_calls"], &calls)
			for _, c := range calls {
				e.calls = append(e.calls, wireCall{rawString(c["id"]), "function_call"})
			}
		}
		if e.role == "tool" {
			e.parts = []wirePart{{raw, rawString(obj["tool_call_id"]), true}}
		}
	}
	if protocol == "messages" {
		contents := fieldValues(raw, "content")
		e.duplicateContent = len(contents) > 1
		// Inspect shadowed content too, so a later duplicate member cannot
		// hide an actual call and evade the call-aware ambiguity rejection.
		if e.duplicateContent && e.role == "assistant" {
			for _, content := range contents[:len(contents)-1] {
				var blocks []json.RawMessage
				_ = json.Unmarshal(content, &blocks)
				for _, b := range blocks {
					var block map[string]json.RawMessage
					_ = json.Unmarshal(b, &block)
					if rawString(block["type"]) == "tool_use" {
						e.calls = append(e.calls, wireCall{rawString(block["id"]), "function_call"})
					}
				}
			}
		}
		var blocks []json.RawMessage
		_ = json.Unmarshal(obj["content"], &blocks)
		for _, b := range blocks {
			var block map[string]json.RawMessage
			_ = json.Unmarshal(b, &block)
			kind := rawString(block["type"])
			if e.role == "assistant" && kind == "tool_use" {
				e.calls = append(e.calls, wireCall{rawString(block["id"]), "function_call"})
			}
			if e.role == "user" {
				id := rawString(block["tool_use_id"])
				if id == "" {
					id = rawString(block["tool_call_id"])
				}
				e.parts = append(e.parts, wirePart{b, id, kind == "tool_result"})
			}
		}
	}
	return e
}

func synthetic(call wireCall, protocol, resultIDField string) json.RawMessage {
	id, _ := json.Marshal(call.id)
	text, _ := json.Marshal(Interrupted)
	switch protocol {
	case "messages":
		return []byte(`{"type":"tool_result","` + resultIDField + `":` + string(id) + `,"is_error":true,"content":` + string(text) + `}`)
	case "responses":
		kind := call.kind + "_output"
		return []byte(`{"type":"` + kind + `","call_id":` + string(id) + `,"output":` + string(text) + `}`)
	default:
		return []byte(`{"role":"tool","tool_call_id":` + string(id) + `,"content":` + string(text) + `}`)
	}
}
func rawString(raw json.RawMessage) string { var s string; _ = json.Unmarshal(raw, &s); return s }
func rawArray(items []json.RawMessage) []byte {
	var b bytes.Buffer
	b.WriteByte('[')
	for i, item := range items {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(item)
	}
	b.WriteByte(']')
	return b.Bytes()
}

// InputOffset locates the actual field value; bytes.Index could replace an
// identical value nested in an unrelated extension before this field.
func replaceField(raw []byte, key string, value []byte) ([]byte, error) {
	if len(fieldValues(raw, key)) > 1 {
		return raw, fmt.Errorf("%w: repeated edit target %q", ErrAmbiguousHistory, key)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if _, err := d.Token(); err != nil {
		return raw, nil
	}
	for d.More() {
		field, err := d.Token()
		if err != nil {
			return raw, nil
		}
		var old json.RawMessage
		if d.Decode(&old) != nil {
			return raw, nil
		}
		if field == key {
			end := int(d.InputOffset())
			start := end - len(old)
			out := make([]byte, 0, len(raw)-len(old)+len(value))
			out = append(out, raw[:start]...)
			out = append(out, value...)
			return append(out, raw[end:]...), nil
		}
	}
	return raw, nil
}

// Duplicate object members have no unambiguous effective history. Inspect all
// occurrences before editing; Unmarshal and field replacement must not disagree
// about which array is effective. Call-free input stays with existing validation.
func duplicatedHistoryWithCalls(body []byte, key, protocol string) bool {
	values := fieldValues(body, key)
	if len(values) < 2 {
		return false
	}
	for _, value := range values {
		var entries []json.RawMessage
		if json.Unmarshal(value, &entries) != nil {
			continue
		}
		for _, entry := range entries {
			if len(readEntry(entry, protocol).calls) > 0 {
				return true
			}
		}
	}
	return false
}

// fieldValues is shared by every edit target and decodes escaped member names.
// The editor itself also checks it, so a new call site cannot silently pick a
// different duplicate member than Unmarshal's effective value.
func fieldValues(body []byte, key string) []json.RawMessage {
	d := json.NewDecoder(bytes.NewReader(body))
	if _, err := d.Token(); err != nil {
		return nil
	}
	var values []json.RawMessage
	for d.More() {
		field, err := d.Token()
		if err != nil {
			return nil
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil
		}
		if field == key {
			values = append(values, value)
		}
	}
	return values
}
