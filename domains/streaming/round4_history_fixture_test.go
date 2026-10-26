package streaming

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const round4HistoryChat = `{"model":"m","messages":[{"role":"user","content":"start"},{"role":"assistant","tool_calls":[{"id":"A","type":"function","function":{"name":"Read","arguments":"{\"n\":9007199254740993}"}},{"id":"B","type":"function","function":{"name":"Read","arguments":"{}"}},{"id":"C","type":"function","function":{"name":"Read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"C","content":"actual-c"},{"role":"user","content":"notice-preserved"},{"role":"tool","tool_call_id":"A","content":"actual-a"},{"role":"user","content":"followup-preserved"}],"tools":[{"type":"function","function":{"name":"Read","parameters":{"type":"object"}}}]}`
const round4HistoryResponses = `{"model":"m","input":[{"type":"message","role":"user","content":"start"},{"type":"function_call","call_id":"A","name":"Read","arguments":"{\"n\":9007199254740993}"},{"type":"function_call","call_id":"B","name":"Read","arguments":"{}"},{"type":"function_call","call_id":"C","name":"Read","arguments":"{}"},{"type":"function_call_output","call_id":"C","output":"actual-c"},{"type":"message","role":"user","content":"notice-preserved"},{"type":"function_call_output","call_id":"A","output":"actual-a"},{"type":"message","role":"user","content":"followup-preserved"}],"tools":[{"type":"function","name":"Read","parameters":{"type":"object"}}]}`
const round4HistoryMessages = `{"model":"m","max_tokens":128,"messages":[{"role":"user","content":"start"},{"role":"assistant","content":[{"type":"tool_use","id":"A","name":"Read","input":{"n":9007199254740993}},{"type":"tool_use","id":"B","name":"Read","input":{}},{"type":"tool_use","id":"C","name":"Read","input":{}}]},{"role":"user","content":[{"type":"text","text":"notice-preserved"},{"type":"tool_result","tool_use_id":"C","content":"actual-c"},{"type":"tool_result","tool_use_id":"A","content":"actual-a"},{"type":"text","text":"followup-preserved"}]}],"tools":[{"name":"Read","input_schema":{"type":"object"}}]}`

func round4HistoryString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

// This oracle reads the actual wire grammar, independently of IR/toolhistory.
func round4HistoryWireError(body []byte, protocol string) error {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return err
	}
	array := envelope["messages"]
	if protocol == "responses" {
		array = envelope["input"]
	}
	var records []map[string]json.RawMessage
	if err := json.Unmarshal(array, &records); err != nil {
		return err
	}
	var calls, results, events []string
	outputs := make(map[string]json.RawMessage)
	errorB := false
	addCall := func(id string, raw json.RawMessage) error {
		calls = append(calls, id)
		if id == "A" && !bytes.Contains(raw, []byte("9007199254740993")) {
			return fmt.Errorf("A argument precision lost: %s", raw)
		}
		return nil
	}
	addResult := func(id string, output json.RawMessage) {
		results = append(results, id)
		events = append(events, "result:"+id)
		outputs[id] = output
	}
	notice := func(raw json.RawMessage) {
		if bytes.Contains(raw, []byte("notice-preserved")) {
			events = append(events, "notice")
		}
	}
	for _, record := range records {
		switch protocol {
		case "chat":
			var toolCalls []map[string]json.RawMessage
			_ = json.Unmarshal(record["tool_calls"], &toolCalls)
			for _, call := range toolCalls {
				if err := addCall(round4HistoryString(call["id"]), call["function"]); err != nil {
					return err
				}
			}
			if round4HistoryString(record["role"]) == "tool" {
				addResult(round4HistoryString(record["tool_call_id"]), record["content"])
			} else {
				notice(record["content"])
			}
		case "responses":
			switch round4HistoryString(record["type"]) {
			case "function_call":
				if err := addCall(round4HistoryString(record["call_id"]), record["arguments"]); err != nil {
					return err
				}
			case "function_call_output":
				addResult(round4HistoryString(record["call_id"]), record["output"])
			default:
				notice(record["content"])
			}
		case "messages":
			var blocks []map[string]json.RawMessage
			if json.Unmarshal(record["content"], &blocks) != nil {
				notice(record["content"])
				continue
			}
			for _, block := range blocks {
				switch round4HistoryString(block["type"]) {
				case "tool_use":
					if err := addCall(round4HistoryString(block["id"]), block["input"]); err != nil {
						return err
					}
				case "tool_result":
					id := round4HistoryString(block["tool_use_id"])
					addResult(id, block["content"])
					if id == "B" {
						errorB = bytes.Equal(block["is_error"], []byte("true"))
					}
				default:
					notice(block["text"])
				}
			}
		}
	}
	if strings.Join(calls, ",") != "A,B,C" || strings.Join(results, ",") != "A,B,C" {
		return fmt.Errorf("call/result order: calls=%v results=%v", calls, results)
	}
	if strings.Join(events, ",") != "result:A,result:B,result:C,notice" {
		return fmt.Errorf("notice displaced or lost: %v", events)
	}
	for id, text := range map[string]string{"A": "actual-a", "C": "actual-c", "B": "interrupted"} {
		if !bytes.Contains(bytes.ToLower(outputs[id]), []byte(text)) {
			return fmt.Errorf("%s result lost: %s", id, outputs[id])
		}
	}
	if protocol == "messages" && !errorB {
		return fmt.Errorf("synthetic B missing is_error")
	}
	if !bytes.Contains(body, []byte("followup-preserved")) {
		return fmt.Errorf("followup text lost")
	}
	return nil
}
