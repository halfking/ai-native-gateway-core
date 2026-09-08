package requestfact

// metadata_projection_test.go covers the gateway metadata block added for
// audit 2026-09-08 #5: the IR-level carriers, the gateway injection, the
// IR → fact projection, and the codec compatibility rules (old data without
// the block still decodes; the zero block is omitted from the wire).

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

func TestInjectGatewayMetadataStampsIRCarrier(t *testing.T) {
	t.Parallel()

	parsed := &ir.InternalRequest{Model: "model"}
	InjectGatewayMetadata(parsed, GatewayMetadata{
		Project:   "proj-1",
		Tags:      []string{"team-a", "prod"},
		TurnTotal: 7,
	})
	if parsed.Metadata == nil {
		t.Fatal("InjectGatewayMetadata left Metadata nil")
	}
	if parsed.Metadata.Project != "proj-1" || parsed.Metadata.TurnTotal != 7 {
		t.Fatalf("carrier = %+v, want project proj-1 turn_total 7", parsed.Metadata)
	}
	if !reflect.DeepEqual(parsed.Metadata.Tags, []string{"team-a", "prod"}) {
		t.Fatalf("carrier tags = %#v, want [team-a prod]", parsed.Metadata.Tags)
	}

	// Empty gateway context must not materialize a carrier block.
	untouched := &ir.InternalRequest{Model: "model"}
	InjectGatewayMetadata(untouched, GatewayMetadata{})
	if untouched.Metadata != nil {
		t.Fatalf("empty gateway context materialized Metadata: %+v", untouched.Metadata)
	}

	// nil IR is a no-op.
	InjectGatewayMetadata(nil, GatewayMetadata{Project: "proj-1"})
}

func TestProjectRequestMetadataSources(t *testing.T) {
	t.Parallel()

	// Gateway-injected carriers plus the OpenAI `user` fallback.
	parsed := &ir.InternalRequest{User: "openai-user"}
	InjectGatewayMetadata(parsed, GatewayMetadata{Project: "proj-1", Tags: []string{"t1"}, TurnTotal: 4})
	got := ProjectRequestMetadata(parsed)
	want := Metadata{Project: "proj-1", Tags: []string{"t1"}, UserID: "openai-user", TurnTotal: 4}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("projection = %+v, want %+v", got, want)
	}

	// Anthropic metadata.user_id wins over the normalized User field.
	anthro := &ir.InternalRequest{
		User:     "openai-user",
		Metadata: &ir.Metadata{UserID: "anthropic-user"},
	}
	if got := ProjectRequestMetadata(anthro); got.UserID != "anthropic-user" {
		t.Fatalf("user_id = %q, want anthropic-user", got.UserID)
	}

	// Projected tags must not alias the IR carrier slice.
	got.Tags[0] = "mutated"
	if parsed.Metadata.Tags[0] != "t1" {
		t.Fatal("projection aliases the IR Tags slice")
	}

	// nil IR → zero block.
	if got := ProjectRequestMetadata(nil); !got.IsZero() {
		t.Fatalf("nil IR projection = %+v, want zero", got)
	}
}

func TestFactMetadataRoundTripAndWireOmission(t *testing.T) {
	t.Parallel()

	fact := richFact()
	fact.Metadata = Metadata{Project: "proj-rt", Tags: []string{"a", "b"}, UserID: "user-rt", TurnTotal: 9}
	encoded, err := Encode(NewEnvelope(goldenCapturedAt(), ArchiveStateActive, fact))
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if !reflect.DeepEqual(decoded.Payload.Metadata, fact.Metadata) {
		t.Fatalf("decoded metadata = %+v, want %+v", decoded.Payload.Metadata, fact.Metadata)
	}
	if !strings.Contains(string(encoded), `"metadata":{`) {
		t.Fatal("populated metadata block missing from encoded payload")
	}
}

func TestZeroMetadataBlockOmittedAndLegacyPayloadDecodes(t *testing.T) {
	t.Parallel()

	// Legacy simulation: a fact whose metadata block is zero encodes without
	// the metadata key at all — exactly the shape pre-#5 data has — and must
	// still decode under the current codec.
	fact := richFact()
	if !fact.Metadata.IsZero() {
		t.Fatalf("richFact metadata = %+v, want zero", fact.Metadata)
	}
	encoded, err := Encode(NewEnvelope(goldenCapturedAt(), ArchiveStateActive, fact))
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	if _, exists := document["payload"].(map[string]any)["metadata"]; exists {
		t.Fatal("zero metadata block was serialized")
	}
	if _, err := Decode(encoded); err != nil {
		t.Fatalf("Decode(legacy payload without metadata) error = %v", err)
	}

	// Forward direction: a payload carrying the metadata block plus an
	// unknown future field must json-decode cleanly (old readers ignore
	// unknown keys, so new data stays readable by pre-#5 code paths). The
	// codec then rejects it on the payload hash — integrity, not shape.
	withMetadata := richFact()
	withMetadata.Metadata = Metadata{Project: "proj-fwd"}
	forwardEncoded, err := Encode(NewEnvelope(goldenCapturedAt(), ArchiveStateActive, withMetadata))
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	var forwardDocument map[string]any
	if err := json.Unmarshal(forwardEncoded, &forwardDocument); err != nil {
		t.Fatal(err)
	}
	payload := forwardDocument["payload"].(map[string]any)
	payload["future_field"] = "ignored"
	forwardBytes, err := json.Marshal(forwardDocument)
	if err != nil {
		t.Fatal(err)
	}
	var oldReaderView RequestArchiveEnvelope
	if err := json.Unmarshal(forwardBytes, &oldReaderView); err != nil {
		t.Fatalf("json unmarshal of payload with unknown field failed: %v", err)
	}
	if oldReaderView.Payload.Metadata.Project != "proj-fwd" {
		t.Fatalf("metadata survived unmarshal = %+v", oldReaderView.Payload.Metadata)
	}
	// Additive fields are excluded from the V1 payload hash by design, so the
	// codec still accepts the document.
	if _, err := Decode(forwardBytes); err != nil {
		t.Fatalf("Decode(payload with additive field) error = %v", err)
	}
}
