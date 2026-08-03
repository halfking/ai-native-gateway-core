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

func TestGoldenFixture_OpenAIToGemini(t *testing.T) {
	runGoldenFixture(t, "openai_to_gemini", "openai-chat", "gemini-generate")
}

func TestGoldenFixture_GeminiToOpenAI(t *testing.T) {
	runGoldenFixture(t, "gemini_to_openai", "gemini-generate", "openai-chat")
}

// TestGoldenFixture_OpenAIToResponses exercises the OpenAI Chat → Responses
// request direction (SerializeResponsesRequest, spec §7.1 IR main-path
// extension, 2026-08-02). IRTransport.serializeRequest now routes
// "openai-responses" to ir.SerializeResponsesRequest; the fixture pair was
// previously committed as "unsupported" and is now a passing golden test.
func TestGoldenFixture_OpenAIToResponses(t *testing.T) {
	runGoldenFixture(t, "openai_to_responses", "openai-chat", "openai-responses")
}

// TestGoldenFixture_AnthropicToResponses exercises the Anthropic Messages →
// Responses request direction (same serializer as above; cross-protocol loss
// reporting is exercised because the source is Anthropic).
func TestGoldenFixture_AnthropicToResponses(t *testing.T) {
	runGoldenFixture(t, "anthropic_to_responses", "anthropic-messages", "openai-responses")
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

// runGoldenFixtureUnsupported validates metadata for a fixture whose direction
// IRTransport.Convert cannot yet perform (e.g. Responses as a request target).
// The fixture is committed for traceability; the test skips with a documented
// reason rather than failing the suite.
func runGoldenFixtureUnsupported(t *testing.T, fixtureName string) {
	t.Helper()
	dir := filepath.Join("testdata", "ir_golden", fixtureName)
	meta, err := LoadFixtureMetadata(filepath.Join(dir, "_fixture_meta.json"))
	if err != nil {
		t.Fatalf("LoadFixtureMetadata: %v", err)
	}
	if err := ValidateFixtureMetadata(meta); err != nil {
		t.Fatalf("ValidateFixtureMetadata: %v", err)
	}
	if meta.FixtureKind != "unsupported" {
		t.Fatalf("fixture %s expected fixture_kind=unsupported, got %q", fixtureName, meta.FixtureKind)
	}
	t.Skipf("fixture %s is unsupported: %s", fixtureName, meta.UnsupportedReason)
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
