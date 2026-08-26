package requestfact

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestEncodeDecodeRoundTripPreservesRichDocuments(t *testing.T) {
	t.Parallel()

	envelope := NewEnvelope(time.Date(2026, time.August, 26, 12, 0, 3, 0, time.UTC), ArchiveStateTerminalPending, richFact())
	envelope.Archive.Attempts = 2
	envelope.Payload.Warnings = []ConversionWarning{{
		Code:      WarningOptionalFieldOmitted,
		Field:     "response.stream_chunks",
		Detail:    "non-stream response",
		Retryable: false,
	}}

	encoded, err := Encode(envelope)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if decoded.PayloadSHA256 == "" {
		t.Fatal("Decode() payload hash is empty")
	}
	if got, want := string(decoded.Payload.Request.CanonicalIR), string(envelope.Payload.Request.CanonicalIR); got != want {
		t.Fatalf("request canonical IR = %s, want %s", got, want)
	}
	if got, want := string(decoded.Payload.Response.StreamChunks), string(envelope.Payload.Response.StreamChunks); got != want {
		t.Fatalf("stream chunks = %s, want %s", got, want)
	}
	if got, want := decoded.Payload.Warnings, envelope.Payload.Warnings; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("warnings = %#v, want %#v", got, want)
	}
}

func TestEncodeProducesDeterministicPayloadHash(t *testing.T) {
	t.Parallel()

	fact := richFact()
	first, err := Encode(NewEnvelope(time.Date(2026, time.August, 26, 12, 0, 3, 0, time.UTC), ArchiveStateActive, fact))
	if err != nil {
		t.Fatalf("first Encode() error = %v", err)
	}
	second, err := Encode(NewEnvelope(time.Date(2026, time.August, 26, 12, 0, 4, 0, time.UTC), ArchiveStateActive, fact))
	if err != nil {
		t.Fatalf("second Encode() error = %v", err)
	}

	var firstEnvelope, secondEnvelope RequestArchiveEnvelope
	if err := json.Unmarshal(first, &firstEnvelope); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second, &secondEnvelope); err != nil {
		t.Fatal(err)
	}
	if firstEnvelope.PayloadSHA256 != secondEnvelope.PayloadSHA256 {
		t.Fatalf("payload hash changed with envelope metadata: %q != %q", firstEnvelope.PayloadSHA256, secondEnvelope.PayloadSHA256)
	}
}

func TestEncodePreservesNumericJSONValues(t *testing.T) {
	t.Parallel()

	fact := richFact()
	fact.Request.RawBody = json.RawMessage(`{"large":9007199254740993}`)
	fact.Integrity.RequestBodySHA256 = BodySHA256(fact.Request.RawBody)
	encoded, err := Encode(NewEnvelope(time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC), ArchiveStateActive, fact))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(decoded.Payload.Request.RawBody), `{"large":9007199254740993}`; got != want {
		t.Fatalf("raw body = %s, want %s", got, want)
	}
}

func TestBodySHA256IgnoresJSONFormatting(t *testing.T) {
	t.Parallel()

	first := []byte(`{"first":1,"second":2}`)
	second := []byte("{ \n \t\"second\" : 2, \"first\" : 1 }")
	if got, want := BodySHA256(second), BodySHA256(first); got != want {
		t.Fatalf("BodySHA256() = %q, want %q", got, want)
	}
}

func TestEncodeRejectsBodyHashMismatch(t *testing.T) {
	t.Parallel()

	fact := richFact()
	fact.Integrity.ResponseBodySHA256 = BodySHA256([]byte(`{"id":"different"}`))
	if _, err := Encode(NewEnvelope(time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC), ArchiveStateActive, fact)); !errors.Is(err, ErrIntegrityMismatch) {
		t.Fatalf("Encode() error = %v, want ErrIntegrityMismatch", err)
	}
}

func TestDecodeRejectsUnsupportedVersionAndTampering(t *testing.T) {
	t.Parallel()

	encoded, err := Encode(NewEnvelope(time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC), ArchiveStateActive, richFact()))
	if err != nil {
		t.Fatal(err)
	}

	var version map[string]any
	if err := json.Unmarshal(encoded, &version); err != nil {
		t.Fatal(err)
	}
	version["codec_version"] = CodecVersionV1 + 1
	unsupported, err := json.Marshal(version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(unsupported); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("Decode(unsupported version) error = %v, want ErrUnsupportedVersion", err)
	}

	var tampered map[string]any
	if err := json.Unmarshal(encoded, &tampered); err != nil {
		t.Fatal(err)
	}
	payload := tampered["payload"].(map[string]any)
	request := payload["request"].(map[string]any)
	request["raw_body"] = `{"model":"tampered"}`
	tamperedBody, err := json.Marshal(tampered)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(tamperedBody); !errors.Is(err, ErrIntegrityMismatch) {
		t.Fatalf("Decode(tampered) error = %v, want ErrIntegrityMismatch", err)
	}
}

func TestDecodeAcceptsAdditiveUnknownFields(t *testing.T) {
	t.Parallel()

	encoded, err := Encode(NewEnvelope(time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC), ArchiveStateActive, richFact()))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	document["future_envelope_field"] = map[string]any{"enabled": true}
	document["payload"].(map[string]any)["future_payload_field"] = "ignored by v1"
	withUnknownFields, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(withUnknownFields); err != nil {
		t.Fatalf("Decode(additive fields) error = %v", err)
	}
}

func TestDecodeAcceptsV1PayloadWithOmittedOptionalFields(t *testing.T) {
	t.Parallel()

	fact := richFact()
	fact.Upstream = ContentDocument{}
	fact.Response = ResponseContent{}
	fact.Usage = Usage{}
	fact.Timeline = Timeline{}
	fact.Lifecycle.StartedAt = time.Time{}
	fact.Lifecycle.DeadlineAt = time.Time{}
	fact.Integrity = Integrity{RequestBodySHA256: fact.Integrity.RequestBodySHA256}
	envelope := NewEnvelope(time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC), ArchiveStateActive, fact)
	encoded, err := Encode(envelope)
	if err != nil {
		t.Fatal(err)
	}

	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	payload := document["payload"].(map[string]any)
	for _, key := range []string{"upstream", "response", "usage", "timeline"} {
		if _, exists := payload[key]; exists {
			t.Fatalf("optional payload key %q was serialized", key)
		}
	}
	lifecycle := payload["lifecycle"].(map[string]any)
	for _, key := range []string{"deadline_at", "started_at"} {
		if _, exists := lifecycle[key]; exists {
			t.Fatalf("optional lifecycle key %q was serialized", key)
		}
	}
	archive := document["archive"].(map[string]any)
	if _, exists := archive["persisted_at"]; exists {
		t.Fatal("optional archive persisted_at was serialized")
	}
	if _, err := Decode(encoded); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
}

func TestEnvelopeValidateRequiresPayloadHash(t *testing.T) {
	t.Parallel()

	envelope := NewEnvelope(time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC), ArchiveStateActive, richFact())
	if err := envelope.Validate(); !errors.Is(err, ErrIntegrityMismatch) {
		t.Fatalf("Validate() error = %v, want ErrIntegrityMismatch", err)
	}
	encoded, err := Encode(envelope)
	if err != nil {
		t.Fatal(err)
	}
	var encodedEnvelope RequestArchiveEnvelope
	if err := json.Unmarshal(encoded, &encodedEnvelope); err != nil {
		t.Fatal(err)
	}
	if err := encodedEnvelope.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestEncodeRejectsMissingOrInvalidCoreDocuments(t *testing.T) {
	t.Parallel()

	for name, mutate := range map[string]func(*CanonicalRequestFact){
		"missing raw body":            func(fact *CanonicalRequestFact) { fact.Request.RawBody = nil },
		"null canonical IR":           func(fact *CanonicalRequestFact) { fact.Request.CanonicalIR = json.RawMessage("null") },
		"malformed optional response": func(fact *CanonicalRequestFact) { fact.Response.RawBody = json.RawMessage(`{"broken"`) },
		"invalid lifecycle order": func(fact *CanonicalRequestFact) {
			fact.Lifecycle.CompletedAt = fact.Lifecycle.CreatedAt.Add(-time.Second)
		},
	} {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fact := richFact()
			mutate(&fact)
			if _, err := Encode(NewEnvelope(time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC), ArchiveStateActive, fact)); err == nil {
				t.Fatal("Encode() error = nil, want validation failure")
			}
		})
	}
}

func TestEnvelopeValidationRejectsInvalidArchiveMetadata(t *testing.T) {
	t.Parallel()

	base := NewEnvelope(time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC), ArchiveStateActive, richFact())
	base.Archive.State = ArchiveState("unknown")
	if err := base.Validate(); err == nil {
		t.Fatal("Validate() error = nil for unknown archive state")
	}
	base = NewEnvelope(time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC), ArchiveStateActive, richFact())
	base.Archive.Attempts = -1
	if err := base.Validate(); err == nil {
		t.Fatal("Validate() error = nil for negative attempts")
	}
}

func TestFactValidationPermitsStructuredWarnings(t *testing.T) {
	t.Parallel()

	fact := richFact()
	fact.Warnings = []ConversionWarning{{Code: WarningLossReported, Field: "upstream.extensions", Retryable: true}}
	if err := fact.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	fact.Warnings[0].Field = ""
	if err := fact.Validate(); err == nil {
		t.Fatal("Validate() error = nil for malformed warning")
	}
}

func richFact() CanonicalRequestFact {
	createdAt := time.Date(2026, time.August, 26, 11, 59, 0, 0, time.UTC)
	startedAt := createdAt.Add(time.Second)
	completedAt := startedAt.Add(2 * time.Second)
	return CanonicalRequestFact{
		Identity:  Identity{TenantID: "tenant-a", RequestID: "req-00000001", SessionID: "session-a", TurnID: "turn-a", TaskID: "task-a"},
		Lifecycle: Lifecycle{Status: "completed", CreatedAt: createdAt, StartedAt: startedAt, CompletedAt: completedAt},
		Routing:   Routing{ClientProtocol: "openai-chat", ClientModel: "gpt-test", OutboundProtocol: "anthropic-messages", OutboundModel: "claude-test", ConversionPath: "openai-chat->anthropic-messages"},
		Request: ContentDocument{
			RawBody:     json.RawMessage(`{"model":"gpt-test","messages":[{"role":"user","content":[{"type":"text","text":"hello"},{"type":"image_url","image_url":{"url":"https://example.invalid/image.png"}}]}]}`),
			CanonicalIR: json.RawMessage(`{"model":"gpt-test","messages":[{"role":"user","content":[{"type":"text","text":"hello"},{"type":"image","source":{"type":"url","url":"https://example.invalid/image.png"}}]}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}],"extensions":{"provider_raw":{"future_field":true}}}`),
			Protocol:    "openai-chat",
			Extensions:  json.RawMessage(`{"x-client-option":{"trace":true}}`),
		},
		Upstream: ContentDocument{
			RawBody:     json.RawMessage(`{"model":"claude-test","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`),
			CanonicalIR: json.RawMessage(`{"messages":[{"role":"user","content":[{"type":"text","text":"hello"}],"tool_calls":[{"id":"call-1","function":{"name":"lookup","arguments":"{}"}}]}]}`),
			Protocol:    "anthropic-messages",
		},
		Response: ResponseContent{
			RawBody:       json.RawMessage(`{"id":"resp-1","content":[{"type":"thinking","thinking":"reasoning","signature":"sig"},{"type":"tool_use","id":"call-1","name":"lookup","input":{}}]}`),
			CanonicalIR:   json.RawMessage(`{"id":"resp-1","content":[{"type":"thinking","thinking":"reasoning","signature":"sig"},{"type":"tool_use","id":"call-1","name":"lookup","input":{}}],"extensions":{"provider_raw":{"unknown_block":true}}}`),
			ClientIR:      json.RawMessage(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-1","function":{"name":"lookup","arguments":"{}"}}]}}]}`),
			StreamSummary: json.RawMessage(`{"chunks":2,"finish_reason":"tool_calls"}`),
			StreamChunks:  json.RawMessage(`[{"type":"message_start"},{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"reasoning"}}]`),
		},
		Usage:       Usage{PromptTokens: 12, CompletionTokens: 7, TotalTokens: 19, ReasoningTokens: 3, Cost: 0.001, Currency: "USD"},
		Timeline:    Timeline{T0: createdAt, T1: startedAt, T9: completedAt, TTFTMillis: 100, LatencyMillis: 2000},
		Attachments: json.RawMessage(`[{"id":"attachment-1","type":"image"}]`),
		Extensions:  json.RawMessage(`{"compression":{"applied":false},"ursm":{"outcome":"success"}}`),
		Integrity: Integrity{
			RequestBodySHA256:  BodySHA256([]byte(`{"model":"gpt-test","messages":[{"role":"user","content":[{"type":"text","text":"hello"},{"type":"image_url","image_url":{"url":"https://example.invalid/image.png"}}]}]}`)),
			OutboundBodySHA256: BodySHA256([]byte(`{"model":"claude-test","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)),
			ResponseBodySHA256: BodySHA256([]byte(`{"id":"resp-1","content":[{"type":"thinking","thinking":"reasoning","signature":"sig"},{"type":"tool_use","id":"call-1","name":"lookup","input":{}}]}`)),
		},
	}
}
