package main

import (
	"encoding/json"
	"testing"
)

func mustReq(t *testing.T, s string) json.RawMessage {
	t.Helper()
	return json.RawMessage(s)
}

func TestParseRequestMessages(t *testing.T) {
	msgs, err := ParseRequestMessages(mustReq(t, `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]}`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Role != "user" || msgs[0].Content != "hi" {
		t.Fatalf("unexpected: %+v", msgs)
	}

	// bare array form
	bare, err := ParseRequestMessages(mustReq(t, `[{"role":"system","content":"s"}]`))
	if err != nil || len(bare) != 1 || bare[0].Role != "system" {
		t.Fatalf("bare array parse failed: %v %+v", err, bare)
	}
}

func TestParseResponseMessages(t *testing.T) {
	msgs, err := ParseResponseMessages(mustReq(t, `{"choices":[{"message":{"role":"assistant","content":"reply"}}]}`))
	if err != nil || len(msgs) != 1 || msgs[0].Content != "reply" {
		t.Fatalf("choices parse failed: %v %+v", err, msgs)
	}

	single, err := ParseResponseMessages(mustReq(t, `{"role":"assistant","content":"solo"}`))
	if err != nil || len(single) != 1 || single[0].Content != "solo" {
		t.Fatalf("single message parse failed: %v %+v", err, single)
	}
}

func TestDeriveTurnDeltas(t *testing.T) {
	full := [][]Msg{
		{{Role: "system", Content: "sys"}, {Role: "user", Content: "q1"}},
		{{Role: "system", Content: "sys"}, {Role: "user", Content: "q1"}, {Role: "assistant", Content: "a1"}, {Role: "user", Content: "q2"}},
		{{Role: "system", Content: "sys"}, {Role: "user", Content: "q1"}, {Role: "assistant", Content: "a1"}, {Role: "user", Content: "q2"}, {Role: "assistant", Content: "a2"}, {Role: "user", Content: "q3"}},
	}
	resp := [][]Msg{
		{{Role: "assistant", Content: "a1"}},
		{{Role: "assistant", Content: "a2"}},
		{},
	}

	reqD, respD := DeriveTurnDeltas(full, resp)
	if len(reqD) != 3 || len(respD) != 3 {
		t.Fatalf("unexpected lens %d/%d", len(reqD), len(respD))
	}

	// turn 0 delta = the user message (no previous; system is not part of delta)
	if len(reqD[0]) != 1 || reqD[0][0].Content != "q1" {
		t.Fatalf("turn0 req delta = %+v, want [q1]", reqD[0])
	}
	// turn 1 delta = only the new user message (assistant a1 is the previous response)
	if len(reqD[1]) != 1 || reqD[1][0].Content != "q2" {
		t.Fatalf("turn1 req delta = %+v, want [q2]", reqD[1])
	}
	// turn 2 delta = only the new user message q3 (assistant a2 is the previous response)
	if len(reqD[2]) != 1 || reqD[2][0].Content != "q3" {
		t.Fatalf("turn2 req delta = %+v, want [q3]", reqD[2])
	}
	if respD[0][0].Content != "a1" || respD[1][0].Content != "a2" {
		t.Fatalf("response deltas wrong: %+v", respD)
	}
	if len(respD[2]) != 0 {
		t.Fatalf("turn2 resp delta should be empty, got %+v", respD[2])
	}
}

func TestSubtractMessagesHandlesDuplicates(t *testing.T) {
	full := []Msg{{Role: "user", Content: "same"}, {Role: "user", Content: "same"}, {Role: "user", Content: "new"}}
	prev := []Msg{{Role: "user", Content: "same"}}
	got := subtractMessages(full, prev)
	// one "same" remains (carried forward), plus "new"
	if len(got) != 2 || got[0].Content != "same" || got[1].Content != "new" {
		t.Fatalf("dup handling wrong: %+v", got)
	}
}
