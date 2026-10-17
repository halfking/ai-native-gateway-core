package toolhistory

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestRepairPartialResultsPreservesCallOrderPayloadAndText(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "messages"} {
		t.Run(protocol, func(t *testing.T) {
			realC, realA := result(protocol, "c", `c-\u003c-9007199254740993`), result(protocol, "a", "a-real")
			body := []byte(fixture(protocol, batch(protocol), `,`+realC+`,`+user(protocol, "notice-one")+`,`+realA+`,`+user(protocol, "notice-two")))
			before := append([]byte(nil), body...)
			fixed, err := Repair(body, protocol)
			if err != nil {
				t.Fatal(err)
			}
			if got := resultIDs(t, fixed, protocol); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
				t.Fatalf("result order=%v body=%s", got, fixed)
			}
			if !bytes.Contains(fixed, []byte(batch(protocol))) {
				t.Fatalf("actual call bytes changed or were deleted: %s", fixed)
			}
			if bytes.Count(fixed, []byte(Interrupted)) != 1 {
				t.Fatalf("missing exactly one interrupted result: %s", fixed)
			}
			if !bytes.Contains(fixed, []byte(`c-\u003c-9007199254740993`)) || !bytes.Contains(fixed, []byte(`"unknown":9007199254740993`)) {
				t.Fatalf("real result bytes lost: %s", fixed)
			}
			if strings.Index(string(fixed), "notice-one") < strings.Index(string(fixed), "a-real") || strings.Index(string(fixed), "notice-two") < strings.Index(string(fixed), "notice-one") {
				t.Fatalf("mixed text moved before results or lost order: %s", fixed)
			}
			if protocol == "messages" && !bytes.Contains(fixed, []byte(`"is_error":true`)) {
				t.Fatalf("synthetic result is not an error: %s", fixed)
			}
			again, err := Repair(fixed, protocol)
			if err != nil || !bytes.Equal(again, fixed) {
				t.Fatalf("not idempotent: %s err=%v", again, err)
			}
			if !bytes.Equal(body, before) {
				t.Fatal("mutated caller buffer")
			}
		})
	}
}
func TestRepairCompleteResultsAndFragmentsAreByteNoop(t *testing.T) {
	for _, p := range []string{"chat", "responses", "messages"} {
		body := []byte(fixture(p, call(p, "x"), `,`+result(p, "x", "")))
		body = bytes.ReplaceAll(body, []byte(`},{`), []byte("},\n {"))
		fixed, err := Repair(body, p)
		if err != nil || !bytes.Equal(fixed, body) {
			t.Fatalf("%s complete empty result rewritten: %s, %v", p, fixed, err)
		}
		body = []byte(fixture(p, batch(p), `,`+result(p, "c", "c")+`,`+result(p, "a", "a")+`,`+result(p, "b", "b")))
		fixed, err = Repair(body, p)
		if err != nil || !bytes.Equal(fixed, body) {
			t.Fatalf("%s complete unordered results rewritten: %s, %v", p, fixed, err)
		}
	}
	for _, body := range []string{`not JSON`, `{"input":"hello"}`, `{"previous_response_id":"r","input":[{"type":"function_call_output","call_id":"external","output":"kept"}]}`} {
		got, err := Repair([]byte(body), "responses")
		if err != nil || string(got) != body {
			t.Fatalf("fragment changed: %s %v", got, err)
		}
	}
}
func TestRepairRepeatedIDsAreScopedToBatch(t *testing.T) {
	for _, p := range []string{"chat", "responses", "messages"} {
		t.Run(p, func(t *testing.T) {
			body := []byte(fixture(p, call(p, "same"), `,`+user(p, "first stopped")+`,`+call(p, "same")+`,`+result(p, "same", "second-real")))
			got, err := Repair(body, p)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Count(got, []byte(Interrupted)) != 1 || !reflect.DeepEqual(resultIDs(t, got, p), []string{"same", "same"}) {
				t.Fatalf("later result answered earlier call: %s", got)
			}
			if bytes.Index(got, []byte(Interrupted)) > bytes.Index(got, []byte("first stopped")) {
				t.Fatalf("synthetic crossed batch boundary: %s", got)
			}
		})
	}
}
func TestRepairRejectsAmbiguityWithoutDeletingCalls(t *testing.T) {
	for _, p := range []string{"chat", "responses", "messages"} {
		var calls string
		if p == "responses" {
			calls = call(p, "x") + `,` + call(p, "x")
		} else {
			var m map[string]json.RawMessage
			_ = json.Unmarshal([]byte(call(p, "x")), &m)
			key := "tool_calls"
			if p == "messages" {
				key = "content"
			}
			var items []json.RawMessage
			_ = json.Unmarshal(m[key], &items)
			items = append(items, items[0])
			m[key], _ = json.Marshal(items)
			raw, _ := json.Marshal(m)
			calls = string(raw)
		}
		body := []byte(fixture(p, calls, ""))
		got, err := Repair(body, p)
		if !errors.Is(err, ErrAmbiguousHistory) || !bytes.Equal(got, body) {
			t.Fatalf("%s ambiguous call IDs lost: %s err=%v", p, got, err)
		}
	}
}

func TestRepairMessagesPreservesMixedImagesAndResultBlocks(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"Read","input":{}},{"type":"tool_use","id":"b","name":"Read","input":{}}]},{"role":"user","content":[{"type":"text","text":"before"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"QUJD"}},{"type":"tool_result","tool_use_id":"b","content":[{"type":"image","source":{"type":"url","url":"https://example.test/image"}},{"type":"text","text":"real-b"}]},{"type":"text","text":"after"}]}]}`)
	got, err := Repair(body, "messages")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resultIDs(t, got, "messages"), []string{"a", "b"}) || bytes.Count(got, []byte(`"type":"image"`)) != 2 || !bytes.Contains(got, []byte(`"data":"QUJD"`)) || !bytes.Contains(got, []byte(`"text":"after"`)) {
		t.Fatalf("mixed result/media/text lost: %s", got)
	}
	if bytes.Index(got, []byte("before")) < bytes.Index(got, []byte("real-b")) || bytes.Index(got, []byte("before")) > bytes.Index(got, []byte("after")) {
		t.Fatalf("mixed block ordering lost: %s", got)
	}
}
func TestRepairCustomCallsAndActualTopLevelField(t *testing.T) {
	custom := []byte(`{"input":[{"type":"custom_tool_call","call_id":"x","name":"apply_patch","input":"raw input"}]}`)
	got, err := Repair(custom, "responses")
	if err != nil || !bytes.Contains(got, []byte(`"type":"custom_tool_call_output"`)) {
		t.Fatalf("custom call not answered: %s %v", got, err)
	}
	calls := `[` + call("chat", "x") + `]`
	body := []byte(`{ "shadow":` + calls + `, "messages":` + calls + `, "integer": 9007199254740993, "escaped":"\u003c" }`)
	got, err = Repair(body, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(got, []byte(`{ "shadow":`+calls+`, "messages":`)) || !bytes.HasSuffix(got, []byte(`, "integer": 9007199254740993, "escaped":"\u003c" }`)) {
		t.Fatalf("unrelated top-level bytes changed: %s", got)
	}
	if len(resultIDs(t, got, "chat")) != 1 {
		t.Fatalf("replaced the shadow instead of the actual field: %s", got)
	}
}
func TestRepairSplitAssistantRecordsRetainEveryCallAndMetadata(t *testing.T) {
	for _, p := range []string{"chat", "messages"} {
		first, second := call(p, "a"), call(p, "b")
		body := []byte(fixture(p, first+`,`+second, `,`+result(p, "b", "real-b")))
		got, err := Repair(body, p)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(got, []byte(first)) || !bytes.Contains(got, []byte(second)) || !reflect.DeepEqual(resultIDs(t, got, p), []string{"a", "b"}) {
			t.Fatalf("%s split calls lost: %s", p, got)
		}
		again, err := Repair(got, p)
		if err != nil || !bytes.Equal(again, got) {
			t.Fatalf("%s split repair not idempotent: %s %v", p, again, err)
		}
	}
}
func TestRepairRejectsRepeatedAnswersAndStopsAtAssistantBoundary(t *testing.T) {
	for _, p := range []string{"chat", "responses", "messages"} {
		body := []byte(fixture(p, call(p, "x"), `,`+result(p, "x", "one")+`,`+result(p, "x", "two")))
		got, err := Repair(body, p)
		if !errors.Is(err, ErrAmbiguousHistory) || !bytes.Equal(got, body) {
			t.Fatalf("%s repeated answers silently consumed: %s %v", p, got, err)
		}
		body = []byte(fixture(p, call(p, "x"), `,{"role":"assistant","content":"new semantic turn"},`+call(p, "x")+`,`+result(p, "x", "real-later")))
		got, err = Repair(body, p)
		if err != nil || bytes.Count(got, []byte(Interrupted)) != 1 || bytes.Index(got, []byte(Interrupted)) > bytes.Index(got, []byte("new semantic turn")) {
			t.Fatalf("%s result crossed assistant boundary: %s %v", p, got, err)
		}
	}
}
