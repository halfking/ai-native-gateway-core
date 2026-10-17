package toolhistory

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRepairMessagesSyntheticUsesSelectedResultIDField(t *testing.T) {
	body := []byte(fixture("messages", batch("messages"), `,`+strings.ReplaceAll(result("messages", "c", "real-c"), "tool_use_id", "tool_call_id")))
	got, err := Repair(body, "messages", Options{ResultIDField: "tool_call_id"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(got, []byte(`"tool_use_id"`)) || bytes.Count(got, []byte(`"tool_call_id"`)) != 3 || bytes.Count(got, []byte(Interrupted)) != 2 {
		t.Fatalf("provider result ID field drifted: %s", got)
	}
	var root map[string]json.RawMessage
	_ = json.Unmarshal(got, &root)
	var messages []map[string]json.RawMessage
	_ = json.Unmarshal(root["messages"], &messages)
	var blocks []map[string]json.RawMessage
	_ = json.Unmarshal(messages[1]["content"], &blocks)
	for i, b := range blocks {
		var id string
		_ = json.Unmarshal(b["tool_call_id"], &id)
		if id != []string{"a", "b", "c"}[i] {
			t.Fatalf("variant result order: %s", got)
		}
	}
}

func TestRepairRejectsDuplicateTopLevelHistoryFieldsWithCalls(t *testing.T) {
	for _, p := range []string{"chat", "responses", "messages"} {
		key := "messages"
		escaped := `\u006dessages`
		if p == "responses" {
			key = "input"
			escaped = `\u0069nput`
		}
		actual := `[` + call(p, "x") + `]`
		complete := `[` + call(p, "x") + `,` + result(p, "x", "real-x") + `]`
		for _, body := range []string{
			`{"` + key + `":[],"` + key + `":` + actual + `}`,
			`{"` + key + `":` + actual + `,"` + key + `":[]}`,
			`{"` + key + `":` + complete + `,"` + key + `":` + complete + `}`,
			`{"` + key + `":[],"` + escaped + `":` + actual + `}`,
		} {
			got, err := Repair([]byte(body), p)
			if !errors.Is(err, ErrAmbiguousHistory) || string(got) != body {
				t.Fatalf("%s duplicate target field was not rejected intact: body=%s result=%s err=%v", p, body, got, err)
			}
		}
		noCalls := `{"` + key + `":[],"` + key + `":[` + user(p, "no calls") + `]}`
		got, err := Repair([]byte(noCalls), p)
		if err != nil || string(got) != noCalls {
			t.Fatalf("%s call-free input changed: %s %v", p, got, err)
		}
	}
}

func TestRepairRejectsDuplicateMessagesContentTargets(t *testing.T) {
	const visible = `[{"type":"text","text":"notice"},{"type":"tool_result","tool_use_id":"c","content":"real-c"}]`
	for _, user := range []string{
		`{"role":"user","content":[],"content":` + visible + `}`,
		`{"role":"user","content":` + visible + `,"content":[]}`,
		`{"role":"user","content":[],"\u0063ontent":` + visible + `}`,
	} {
		body := []byte(fixture("messages", batch("messages"), `,`+user))
		fixed, err := Repair(body, "messages")
		if !errors.Is(err, ErrAmbiguousHistory) || !bytes.Equal(fixed, body) {
			t.Fatalf("nested duplicate edit target was not rejected intact: got=%s err=%v", fixed, err)
		}
	}
	body := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"x","name":"Read","input":{}}],"content":[{"type":"text","text":"last value hides the call"}]}]}`)
	got, err := Repair(body, "messages")
	if !errors.Is(err, ErrAmbiguousHistory) || !bytes.Equal(got, body) {
		t.Fatalf("shadowed actual call was guessed away: %s %v", got, err)
	}
	body = []byte(`{"messages":[{"role":"user","content":[],"content":` + visible + `}]}`)
	got, err = Repair(body, "messages")
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("call-free fragment was changed: %s %v", got, err)
	}
}

func TestEveryFieldEditorRejectsDuplicateTargets(t *testing.T) {
	for _, key := range []string{"messages", "input", "content"} {
		body := []byte(`{"` + key + `":[],"` + key + `":[1]}`)
		got, err := replaceField(body, key, []byte(`[2]`))
		if !errors.Is(err, ErrAmbiguousHistory) || !bytes.Equal(got, body) {
			t.Fatalf("%s editor guessed an ambiguous member: %s %v", key, got, err)
		}
	}
}
