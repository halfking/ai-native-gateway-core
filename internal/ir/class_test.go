package ir

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// V6-W1.6 T1 (docs/架构优化v6/09-ir-class-journal-decoupling.md §R8):
// RequestClass is gateway-internal metadata. ClassOf derives the class from
// DueAt: zero or past → immediate, future → scheduled.

func TestClassOfBoundaries(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		due  time.Time
		want RequestClass
	}{
		{"zero due → immediate", time.Time{}, ClassImmediate},
		{"past due → immediate", now.Add(-time.Millisecond), ClassImmediate},
		{"now due → immediate", now, ClassImmediate},
		{"future due → scheduled", now.Add(time.Minute), ClassScheduled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassOf(tc.due); got != tc.want {
				t.Fatalf("ClassOf(%v) = %q, want %q", tc.due, got, tc.want)
			}
		})
	}
}

// The four serializers must never emit Class/DueAt: they are gateway-internal
// fields (E10), and the serializers are explicitly-fielded so internal
// metadata cannot leak upstream. Golden-style: serialize a request that
// carries the fields and assert the output is byte-identical to one that
// does not.
func TestSerializersDoNotLeakRequestClass(t *testing.T) {
	base := &InternalRequest{
		Model:       "gpt-test",
		MaxTokens:   128,
		Stream:      false,
		Messages:    []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
		SourceProtocol: ProtocolOpenAIChat,
	}
	stamped := *base
	stamped.Class = ClassScheduled
	stamped.DueAt = time.Now().Add(time.Hour).UTC()

	type ser struct {
		name string
		fn   func(*InternalRequest) ([]byte, error)
	}
	sers := []ser{
		{"openai", SerializeOpenAI},
		{"anthropic", SerializeAnthropic},
		{"gemini", SerializeGemini},
		{"responses", SerializeResponsesRequest},
	}
	for _, s := range sers {
		t.Run(s.name, func(t *testing.T) {
			plain, err := s.fn(base)
			if err != nil {
				t.Fatalf("serialize base: %v", err)
			}
			out, err := s.fn(&stamped)
			if err != nil {
				t.Fatalf("serialize stamped: %v", err)
			}
			if string(plain) != string(out) {
				t.Fatalf("serializer output differs when Class/DueAt set:\nbase:    %s\nstamped: %s", plain, out)
			}
			var probe map[string]json.RawMessage
			if err := json.Unmarshal(out, &probe); err != nil {
				t.Fatalf("unmarshal output: %v", err)
			}
			for _, key := range []string{"class", "due_at", "request_class"} {
				if _, ok := probe[key]; ok {
					t.Fatalf("serializer leaked internal field %q in output: %s", key, out)
				}
			}
			if strings.Contains(string(out), "scheduled") && !strings.Contains(string(plain), "scheduled") {
				t.Fatalf("class value leaked into output: %s", out)
			}
		})
	}
}
