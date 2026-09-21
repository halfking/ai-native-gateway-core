package ir

import (
	"encoding/json"
	"testing"
)

func TestRequestDocumentPreservesProtocolParserOutput(t *testing.T) {
	testCases := []struct {
		name      string
		body      []byte
		parse     func([]byte) (*InternalRequest, error)
		serialize func(*InternalRequest) ([]byte, error)
	}{
		{
			name:      "openai chat",
			body:      []byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}],"temperature":0.2,"x_future":{"large":9007199254740993}}`),
			parse:     ParseOpenAI,
			serialize: SerializeOpenAI,
		},
		{
			name:      "anthropic messages",
			body:      []byte(`{"model":"claude-sonnet-4-20250514","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}],"x_future":{"large":9007199254740993}}`),
			parse:     ParseAnthropic,
			serialize: SerializeAnthropic,
		},
		{
			name:      "gemini generate",
			body:      []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"generationConfig":{"temperature":0.2},"x_future":{"large":9007199254740993}}`),
			parse:     ParseGemini,
			serialize: SerializeGemini,
		},
		{
			name:      "openai responses",
			body:      []byte(`{"model":"gpt-4o-mini","input":[{"role":"user","content":"hello"}],"reasoning":{"effort":"low"},"x_future":{"large":9007199254740993}}`),
			parse:     ParseResponses,
			serialize: SerializeResponsesRequest,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			parsed, err := testCase.parse(testCase.body)
			if err != nil {
				t.Fatalf("parse() error = %v", err)
			}
			document, err := EncodeRequestDocument(parsed)
			if err != nil {
				t.Fatalf("EncodeRequestDocument() error = %v", err)
			}
			restored, err := DecodeRequestDocument(document)
			if err != nil {
				t.Fatalf("DecodeRequestDocument() error = %v", err)
			}
			serialized, err := testCase.serialize(restored)
			if err != nil {
				t.Fatalf("serialize() error = %v", err)
			}
			if !json.Valid(serialized) {
				t.Fatalf("serialize() returned invalid JSON: %s", serialized)
			}
			if got, want := restored.SourceProtocol, parsed.SourceProtocol; got != want {
				t.Errorf("SourceProtocol = %q, want %q", got, want)
			}
			if len(parsed.Extensions) > 0 && len(restored.Extensions) == 0 {
				t.Error("extensions were lost across request document codec")
			}
		})
	}
}
