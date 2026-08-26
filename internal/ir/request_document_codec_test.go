package ir

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func TestRequestDocumentRoundTripPreservesRichIR(t *testing.T) {
	index := 2
	request := &InternalRequest{
		Model:          "model-a",
		SourceProtocol: ProtocolOpenAIChat,
		TargetProvider: "minimax",
		Messages: []Message{{
			Role: "assistant",
			Content: []ContentBlock{
				{Type: "text", Text: "inspect"},
				{Type: "image", Image: &ImageSource{Type: "url", URL: "https://example.invalid/image.png"}},
				{Type: "thinking", Thinking: &ThinkingBlock{Thinking: "reason", Signature: "signature"}},
				{Type: "unknown_provider_block", Index: &index, RawContent: json.RawMessage(`{"future":9007199254740993}`)},
			},
			RawContent: json.RawMessage(`[{"type":"provider_native","large":9007199254740993}]`),
		}},
		Tools: []ToolDefinition{{
			Type: "computer_20250124",
			Raw:  json.RawMessage(`{"type":"computer_20250124","display_width_px":1024}`),
		}},
		Extensions: map[string]json.RawMessage{
			"x-provider-option": json.RawMessage(`{"large":9007199254740993}`),
		},
	}
	request.Messages[0].Content = append(request.Messages[0].Content, ContentBlock{
		Type: "tool_result",
		ToolResult: &ToolResult{
			ToolUseID:      "call-1",
			GeminiResponse: json.RawMessage(`{"large":9007199254740993}`),
		},
	})

	encoded, err := EncodeRequestDocument(request)
	if err != nil {
		t.Fatalf("EncodeRequestDocument() error = %v", err)
	}
	decoded, err := DecodeRequestDocument(encoded)
	if err != nil {
		t.Fatalf("DecodeRequestDocument() error = %v", err)
	}
	reencoded, err := EncodeRequestDocument(decoded)
	if err != nil {
		t.Fatalf("EncodeRequestDocument(decoded) error = %v", err)
	}
	if !bytes.Equal(reencoded, encoded) {
		t.Fatalf("re-encoded document = %s, want %s", reencoded, encoded)
	}
	if got, want := decoded.TargetProvider, "minimax"; got != want {
		t.Errorf("TargetProvider = %q, want %q", got, want)
	}
	if got, ok := decoded.Messages[0].Content[3].RawContent.(string); !ok || got != `{"future":9007199254740993}` {
		t.Fatalf("unknown block raw = %#v (%T), want JSON string", decoded.Messages[0].Content[3].RawContent, decoded.Messages[0].Content[3].RawContent)
	}
	if !bytes.Contains(encoded, []byte(`"version":1`)) || !bytes.Contains(encoded, []byte(`"kind":"internal_request"`)) {
		t.Fatalf("encoded document lacks versioned envelope: %s", encoded)
	}
}

func TestRequestDocumentNormalizesJSONRawContentForReplay(t *testing.T) {
	request := &InternalRequest{
		Model:          "gpt-4o-mini",
		SourceProtocol: ProtocolOpenAIChat,
		Messages: []Message{{
			Role: "user",
			Content: []ContentBlock{{
				Type:       "provider_native_future",
				RawContent: json.RawMessage(`{"type":"provider_native_future","payload":{"large":9007199254740993}}`),
			}},
		}},
	}
	document, err := EncodeRequestDocument(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRequestDocument(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded.Messages[0].Content[0].RawContent.(string); !ok {
		t.Fatalf("decoded RawContent type = %T, want string", decoded.Messages[0].Content[0].RawContent)
	}
	serialized, err := SerializeOpenAI(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(serialized, []byte(`"provider_native_future"`)) {
		t.Fatalf("SerializeOpenAI() dropped raw block: %s", serialized)
	}
}

func TestRequestDocumentRejectsInvalidEnvelope(t *testing.T) {
	testCases := []struct {
		name string
		body []byte
		want error
	}{
		{name: "empty", body: nil, want: ErrInvalidRequestDocument},
		{name: "null", body: []byte("null"), want: ErrInvalidRequestDocument},
		{name: "malformed", body: []byte(`{"version":`), want: ErrInvalidRequestDocument},
		{name: "unsupported version", body: []byte(`{"version":2,"kind":"internal_request","payload":{}}`), want: ErrUnsupportedRequestDocumentVersion},
		{name: "wrong kind", body: []byte(`{"version":1,"kind":"other","payload":{}}`), want: ErrInvalidRequestDocument},
		{name: "missing payload", body: []byte(`{"version":1,"kind":"internal_request"}`), want: ErrInvalidRequestDocument},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := DecodeRequestDocument(testCase.body)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("DecodeRequestDocument() error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestRequestDocumentRejectsMalformedRawPayload(t *testing.T) {
	testCases := []struct {
		name    string
		request *InternalRequest
	}{
		{
			name:    "message raw content",
			request: &InternalRequest{Messages: []Message{{Role: "user", RawContent: json.RawMessage(`{"broken"`)}}},
		},
		{
			name:    "provider tool raw",
			request: &InternalRequest{Tools: []ToolDefinition{{Type: "computer", Raw: json.RawMessage(`{"broken"`)}}},
		},
		{
			name:    "tool input",
			request: &InternalRequest{Messages: []Message{{Content: []ContentBlock{{Type: "tool_use", ToolUse: &ToolUse{Input: json.RawMessage(`{"broken"`)}}}}}},
		},
		{
			name:    "MCP tool config",
			request: &InternalRequest{MCPServers: []MCPServer{{ToolConfig: json.RawMessage(`{"broken"`)}}},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := EncodeRequestDocument(testCase.request); !errors.Is(err, ErrInvalidRequestDocument) {
				t.Fatalf("EncodeRequestDocument() error = %v, want ErrInvalidRequestDocument", err)
			}
		})
	}
}

func TestRequestDocumentDecodeAcceptsAdditiveFields(t *testing.T) {
	encoded, err := EncodeRequestDocument(&InternalRequest{Model: "model", SourceProtocol: ProtocolOpenAIChat})
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	document["future_envelope_field"] = true
	document["payload"].(map[string]any)["future_payload_field"] = "ignored"
	modified, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRequestDocument(modified)
	if err != nil {
		t.Fatalf("DecodeRequestDocument() error = %v", err)
	}
	if got, want := decoded.Model, "model"; got != want {
		t.Errorf("Model = %q, want %q", got, want)
	}
}
