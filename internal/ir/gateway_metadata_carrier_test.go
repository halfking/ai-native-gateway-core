package ir

// gateway_metadata_carrier_test.go covers the gateway session/project context
// carriers added to Metadata for audit 2026-09-08 #5: they must survive the
// request-document codec roundtrip and must NEVER leak into any upstream
// serialization (only user_id / request_id / Other are wire-emitted).

import (
	"bytes"
	"strings"
	"testing"
)

func gatewayCarrierMetadata() *Metadata {
	return &Metadata{
		UserID:    "user-1",
		Project:   "proj-gateway",
		Tags:      []string{"team-a", "prod"},
		TurnTotal: 5,
	}
}

func TestRequestDocumentRoundTripKeepsGatewayMetadata(t *testing.T) {
	t.Parallel()

	req := &InternalRequest{
		Model:          "model-x",
		SourceProtocol: ProtocolOpenAIChat,
		Metadata:       gatewayCarrierMetadata(),
	}
	encoded, err := EncodeRequestDocument(req)
	if err != nil {
		t.Fatalf("EncodeRequestDocument() error = %v", err)
	}
	decoded, err := DecodeRequestDocument(encoded)
	if err != nil {
		t.Fatalf("DecodeRequestDocument() error = %v", err)
	}
	if decoded.Metadata == nil {
		t.Fatal("decoded Metadata is nil")
	}
	got, want := decoded.Metadata, gatewayCarrierMetadata()
	if got.UserID != want.UserID || got.Project != want.Project || got.TurnTotal != want.TurnTotal {
		t.Fatalf("decoded metadata = %+v, want %+v", got, want)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "team-a" || got.Tags[1] != "prod" {
		t.Fatalf("decoded tags = %#v, want [team-a prod]", got.Tags)
	}
}

// TestSerializersNeverEmitGatewayMetadata guards the no-leak contract: the
// gateway-injected Project/Tags/TurnTotal carriers are requestfact-only and
// must not appear in any outbound protocol body.
func TestSerializersNeverEmitGatewayMetadata(t *testing.T) {
	t.Parallel()

	req := &InternalRequest{
		Model:          "model-x",
		SourceProtocol: ProtocolOpenAIChat,
		User:           "user-1",
		Metadata:       gatewayCarrierMetadata(),
		Messages: []Message{{
			Role:    "user",
			Content: []ContentBlock{{Type: "text", Text: "hi"}},
		}},
	}

	for name, serialize := range map[string]func(*InternalRequest) ([]byte, error){
		"openai-chat":        SerializeOpenAI,
		"anthropic-messages": SerializeAnthropic,
		"gemini-generate":    SerializeGemini,
		"openai-responses":   SerializeResponsesRequest,
	} {
		body, err := serialize(req)
		if err != nil {
			t.Fatalf("%s: serialize error = %v", name, err)
		}
		for _, leaked := range []string{"proj-gateway", "team-a", `"turn_total":5`, "turn_total"} {
			if bytes.Contains(body, []byte(leaked)) {
				t.Fatalf("%s: outbound body leaked gateway metadata %q: %s", name, leaked, body)
			}
		}
	}
}

// TestResponsesMetadataOmissionKeepsUserID sanity-checks that the existing
// user_id passthrough is unaffected by the new carriers.
func TestResponsesMetadataOmissionKeepsUserID(t *testing.T) {
	t.Parallel()

	body, err := SerializeResponsesRequest(&InternalRequest{
		Model:          "model-x",
		SourceProtocol: ProtocolOpenAIResponses,
		Metadata:       gatewayCarrierMetadata(),
		Messages: []Message{{
			Role:    "user",
			Content: []ContentBlock{{Type: "text", Text: "hi"}},
		}},
	})
	if err != nil {
		t.Fatalf("serialize error = %v", err)
	}
	if !strings.Contains(string(body), `"user_id":"user-1"`) {
		t.Fatalf("responses metadata lost user_id: %s", body)
	}
}
