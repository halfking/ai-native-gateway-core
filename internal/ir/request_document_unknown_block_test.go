package ir

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestRequestDocumentPreservesParserProducedUnknownBlock(t *testing.T) {
	body := []byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":[{"type":"provider_native_future","payload":{"large":9007199254740993}}]}]}`)
	parsed, err := ParseOpenAI(body)
	if err != nil {
		t.Fatalf("ParseOpenAI() error = %v", err)
	}
	if len(parsed.Messages) != 1 || len(parsed.Messages[0].Content) != 1 {
		t.Fatalf("parsed messages = %#v", parsed.Messages)
	}
	parsedRaw, ok := parsed.Messages[0].Content[0].RawContent.(string)
	if !ok {
		t.Fatalf("parser raw content type = %T, want string", parsed.Messages[0].Content[0].RawContent)
	}

	document, err := EncodeRequestDocument(parsed)
	if err != nil {
		t.Fatalf("EncodeRequestDocument() error = %v", err)
	}
	decoded, err := DecodeRequestDocument(document)
	if err != nil {
		t.Fatalf("DecodeRequestDocument() error = %v", err)
	}
	decodedRaw, ok := decoded.Messages[0].Content[0].RawContent.(string)
	if !ok || decodedRaw != parsedRaw {
		t.Fatalf("decoded raw content = %#v (%T), want parser output %q", decoded.Messages[0].Content[0].RawContent, decoded.Messages[0].Content[0].RawContent, parsedRaw)
	}
	serialized, err := SerializeOpenAI(decoded)
	if err != nil {
		t.Fatalf("SerializeOpenAI() error = %v", err)
	}
	var serializedObject map[string]json.RawMessage
	if err := json.Unmarshal(serialized, &serializedObject); err != nil {
		t.Fatal(err)
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(serializedObject["messages"], &messages); err != nil {
		t.Fatal(err)
	}
	var content []map[string]json.RawMessage
	if err := json.Unmarshal(messages[0]["content"], &content); err != nil {
		t.Fatal(err)
	}
	if len(content) != 1 || !bytes.Contains(content[0]["type"], []byte(`"provider_native_future"`)) {
		t.Fatalf("serialized unknown block = %s", content[0])
	}
	var parserBlock map[string]json.RawMessage
	if err := json.Unmarshal([]byte(parsedRaw), &parserBlock); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content[0]["payload"], parserBlock["payload"]) {
		t.Fatalf("serialized unknown payload = %s, want parser payload %s", content[0]["payload"], parserBlock["payload"])
	}
}
