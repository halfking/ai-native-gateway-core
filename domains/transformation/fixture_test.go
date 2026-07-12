package transformation

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestValidateFixtureMetadata(t *testing.T) {
	meta, err := LoadFixtureMetadata(filepath.Join("testdata", "ir_golden", "openai_to_anthropic", "_fixture_meta.json"))
	if err != nil {
		t.Fatalf("LoadFixtureMetadata: %v", err)
	}
	if err := ValidateFixtureMetadata(meta); err != nil {
		t.Fatalf("ValidateFixtureMetadata: %v", err)
	}
}

func TestGoldenFixture_OpenAIToAnthropic(t *testing.T) {
	runGoldenFixture(t, "openai_to_anthropic", "openai-chat", "anthropic-messages")
}

func TestGoldenFixture_AnthropicToOpenAI(t *testing.T) {
	runGoldenFixture(t, "anthropic_to_openai", "anthropic-messages", "openai-chat")
}

func runGoldenFixture(t *testing.T, fixtureName, clientProtocol, upstreamProtocol string) {
	t.Helper()
	dir := filepath.Join("testdata", "ir_golden", fixtureName)
	meta, err := LoadFixtureMetadata(filepath.Join(dir, "_fixture_meta.json"))
	if err != nil {
		t.Fatalf("LoadFixtureMetadata: %v", err)
	}
	if err := ValidateFixtureMetadata(meta); err != nil {
		t.Fatalf("ValidateFixtureMetadata: %v", err)
	}

	request, err := os.ReadFile(filepath.Join(dir, "request.json"))
	if err != nil {
		t.Fatalf("read request fixture: %v", err)
	}
	expected, err := os.ReadFile(filepath.Join(dir, "expected.json"))
	if err != nil {
		t.Fatalf("read expected fixture: %v", err)
	}

	env := newEnvelope(clientProtocol, upstreamProtocol, string(request), meta.Model)
	actual, err := NewIRTransport().Convert(context.Background(), env)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	assertJSONEqual(t, actual, expected)
}

func assertJSONEqual(t *testing.T, actual, expected []byte) {
	t.Helper()
	var actualValue any
	if err := json.Unmarshal(actual, &actualValue); err != nil {
		t.Fatalf("actual JSON: %v", err)
	}
	var expectedValue any
	if err := json.Unmarshal(expected, &expectedValue); err != nil {
		t.Fatalf("expected JSON: %v", err)
	}
	if !reflect.DeepEqual(actualValue, expectedValue) {
		t.Fatalf("fixture output mismatch\nactual: %s\nexpected: %s", actual, expected)
	}
}

func TestValidateFixtureMetadata_RejectsMissingRequiredField(t *testing.T) {
	err := ValidateFixtureMetadata(FixtureMetadata{
		SourceURL:        "https://platform.openai.com/docs/api-reference/chat",
		CapturedAt:       "2026-07-12",
		APIVersion:       "2026-07",
		Model:            "gpt-4o",
		ProviderProfile:  "openai-chat",
		RedactionVersion: "v1",
	})
	if err == nil {
		t.Fatal("ValidateFixtureMetadata should reject incomplete metadata")
	}
}
