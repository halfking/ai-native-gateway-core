package requestfact

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// Golden round-trip corpus for the Phase 1 request fact contract.
//
// Matrix: four client protocols (openai-chat / anthropic-messages /
// gemini-generate / openai-responses) crossed with scenario families
// (text, multimodal, tool_calls, tool_result, thinking+signature,
// provider-raw, unknown-block, sse-stream-fragments). Every corpus entry
// travels the full pipeline:
//
//	raw protocol body -> protocol parser -> BuildClientContentDocument
//	-> CanonicalRequestFact -> Encode -> Decode -> field-level assertions,
//	with an additional ir.EncodeRequestDocument/DecodeRequestDocument
// round-trip asserted alongside.
//
// RawMessage payloads inside the canonical IR document are key-sorted by the
// canonical JSON step, so IR equivalence is asserted on canonical encodings
// and on named fields, never on raw byte identity of nested documents.

const (
	goldenScenarioText         = "text"
	goldenScenarioMultimodal   = "multimodal"
	goldenScenarioToolCalls    = "tool_calls"
	goldenScenarioToolResult   = "tool_result"
	goldenScenarioThinking     = "thinking+signature"
	goldenScenarioProviderRaw  = "provider-raw"
	goldenScenarioUnknownBlock = "unknown-block"
	goldenScenarioSSE          = "sse-stream-fragments"
)

type goldenResponseFixture struct {
	rawBody       string
	clientIR      string
	streamSummary string
	streamChunks  string
}

type goldenCorpusCase struct {
	name     string
	scenario string
	protocol string
	model    string
	body     string
	parse    func(body []byte) (*ir.InternalRequest, error)

	// extensions are archive-side request document extensions, distinct from
	// the IR Extensions produced by protocol parsers for unknown fields.
	extensions string

	response *goldenResponseFixture

	// verify asserts scenario-specific semantics on the IR decoded from the
	// archived canonical IR document.
	verify func(t *testing.T, req *ir.InternalRequest)

	// verifyEnvelope optionally asserts response-side semantics on the
	// decoded archive envelope (used by the SSE scenario).
	verifyEnvelope func(t *testing.T, envelope *RequestArchiveEnvelope)
}

func goldenCapturedAt() time.Time {
	return time.Date(2026, time.August, 27, 10, 0, 5, 0, time.UTC)
}

func (c goldenCorpusCase) requestID() string {
	return "req-golden-" + c.protocol + "-" + c.name
}

func (c goldenCorpusCase) buildFact(t *testing.T, document ContentDocument) CanonicalRequestFact {
	t.Helper()
	createdAt := time.Date(2026, time.August, 27, 10, 0, 0, 0, time.UTC)
	startedAt := createdAt.Add(time.Second)
	completedAt := startedAt.Add(2 * time.Second)
	success := true
	fact := CanonicalRequestFact{
		Identity: Identity{
			TenantID:        "tenant-golden",
			RequestID:       c.requestID(),
			SessionID:       "session-golden",
			TurnID:          "turn-" + c.name,
			TaskID:          "task-golden",
			ParentRequestID: "parent-golden",
		},
		Lifecycle: Lifecycle{
			Status:      "completed",
			Success:     &success,
			CreatedAt:   createdAt,
			StartedAt:   startedAt,
			CompletedAt: completedAt,
		},
		Routing: Routing{
			ClientProtocol:   c.protocol,
			ClientModel:      c.model,
			OutboundProtocol: c.protocol,
			OutboundModel:    c.model,
			ProviderID:       "provider-golden",
			CredentialID:     "credential-golden",
			CanonicalModel:   "canonical-" + c.model,
			ConversionPath:   c.protocol + "->" + c.protocol,
		},
		Request: document,
		Usage:   Usage{PromptTokens: 12, CompletionTokens: 7, TotalTokens: 19, ReasoningTokens: 3, Cost: 0.002, Currency: "USD"},
		Timeline: Timeline{
			T0: createdAt, T1: startedAt, T2: startedAt.Add(50 * time.Millisecond), T3: completedAt,
			TTFTMillis: 50, LatencyMillis: 2000,
		},
		Attachments: json.RawMessage(`[{"id":"attachment-golden-1","type":"image"}]`),
		Warnings: []ConversionWarning{{
			Code:      WarningOptionalFieldOmitted,
			Field:     "upstream.extensions",
			Detail:    "same-protocol passthrough",
			Retryable: false,
		}},
		Integrity: Integrity{RequestBodySHA256: BodySHA256([]byte(c.body))},
	}
	if c.response != nil {
		fact.Response = ResponseContent{
			RawBody:       json.RawMessage(c.response.rawBody),
			ClientIR:      json.RawMessage(c.response.clientIR),
			StreamSummary: json.RawMessage(c.response.streamSummary),
			StreamChunks:  json.RawMessage(c.response.streamChunks),
		}
		fact.Integrity.ResponseBodySHA256 = BodySHA256([]byte(c.response.rawBody))
	}
	return fact
}

func TestGoldenCorpusMatrixCoverage(t *testing.T) {
	t.Parallel()

	protocols := []string{ir.ProtocolOpenAIChat, ir.ProtocolAnthropicMessages, ir.ProtocolGeminiGenerate, ir.ProtocolOpenAIResponses}
	coreScenarios := []string{
		goldenScenarioText, goldenScenarioMultimodal, goldenScenarioToolCalls, goldenScenarioToolResult,
	}
	seen := make(map[string]bool)
	for _, c := range goldenCorpus() {
		key := c.protocol + "/" + c.scenario
		if seen[key] {
			t.Fatalf("duplicate corpus cell %q", key)
		}
		seen[key] = true
		if c.parse == nil || c.verify == nil {
			t.Fatalf("corpus cell %q missing parse or verify hook", key)
		}
	}
	for _, protocol := range protocols {
		for _, scenario := range coreScenarios {
			if !seen[protocol+"/"+scenario] {
				t.Errorf("corpus is missing required cell %s/%s", protocol, scenario)
			}
		}
	}
	for _, scenario := range []string{
		goldenScenarioThinking, goldenScenarioProviderRaw, goldenScenarioUnknownBlock, goldenScenarioSSE,
	} {
		covered := false
		for _, protocol := range protocols {
			if seen[protocol+"/"+scenario] {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("scenario %q is not covered by any protocol", scenario)
		}
	}
}

func TestGoldenCorpusRequestArchiveRoundTrip(t *testing.T) {
	t.Parallel()

	for _, c := range goldenCorpus() {
		t.Run(c.protocol+"/"+c.name, func(t *testing.T) {
			t.Parallel()

			// 1. Corpus body -> structured parser output.
			parsed, err := c.parse([]byte(c.body))
			if err != nil {
				t.Fatalf("parse %s body: %v", c.protocol, err)
			}

			// 2. Parser output -> requestfact content document.
			var extensions json.RawMessage
			if c.extensions != "" {
				extensions = json.RawMessage(c.extensions)
			}
			document, err := BuildClientContentDocument([]byte(c.body), c.protocol, parsed, extensions)
			if err != nil {
				t.Fatalf("BuildClientContentDocument() error = %v", err)
			}

			// 3. IR document round-trip through ir.EncodeRequestDocument.
			encodedIR, err := ir.EncodeRequestDocument(parsed)
			if err != nil {
				t.Fatalf("ir.EncodeRequestDocument() error = %v", err)
			}
			if !bytes.Equal(document.CanonicalIR, encodedIR) {
				t.Fatalf("builder canonical IR = %s, want direct ir.EncodeRequestDocument output %s", document.CanonicalIR, encodedIR)
			}
			decodedIR, err := ir.DecodeRequestDocument(encodedIR)
			if err != nil {
				t.Fatalf("ir.DecodeRequestDocument() error = %v", err)
			}
			roundTripIR, err := ir.EncodeRequestDocument(decodedIR)
			if err != nil {
				t.Fatalf("re-encode decoded IR: %v", err)
			}
			if !bytes.Equal(roundTripIR, encodedIR) {
				t.Fatalf("IR document is not round-trip stable: got %s, want %s", roundTripIR, encodedIR)
			}
			if got, want := decodedIR.SourceProtocol, parsed.SourceProtocol; got != want {
				t.Fatalf("decoded IR source protocol = %q, want %q", got, want)
			}
			if got, want := len(decodedIR.Messages), len(parsed.Messages); got != want {
				t.Fatalf("decoded IR message count = %d, want %d", got, want)
			}

			// 4. Fact -> Encode -> Decode with field-level assertions.
			envelope := NewEnvelope(goldenCapturedAt(), ArchiveStateActive, c.buildFact(t, document))
			encoded, err := Encode(envelope)
			if err != nil {
				t.Fatalf("Encode() error = %v", err)
			}
			decoded, err := Decode(encoded)
			if err != nil {
				t.Fatalf("Decode() error = %v", err)
			}
			goldenAssertDecodedFact(t, decoded, envelope)

			// 5. The archived canonical IR must still decode to the parser
			// output and satisfy the scenario semantics.
			archivedIR, err := ir.DecodeRequestDocument(decoded.Payload.Request.CanonicalIR)
			if err != nil {
				t.Fatalf("DecodeRequestDocument(archived IR) error = %v", err)
			}
			archivedEncoded, err := ir.EncodeRequestDocument(archivedIR)
			if err != nil {
				t.Fatalf("EncodeRequestDocument(archived IR) error = %v", err)
			}
			if !bytes.Equal(archivedEncoded, encodedIR) {
				t.Fatalf("archived IR diverged from parser output: got %s, want %s", archivedEncoded, encodedIR)
			}
			c.verify(t, archivedIR)
			if c.verifyEnvelope != nil {
				c.verifyEnvelope(t, decoded)
			}
		})
	}
}

func goldenAssertDecodedFact(t *testing.T, decoded *RequestArchiveEnvelope, want RequestArchiveEnvelope) {
	t.Helper()

	if decoded.EnvelopeVersion != CurrentEnvelopeVersion || decoded.PayloadVersion != CurrentPayloadVersion ||
		decoded.CodecVersion != CurrentCodecVersion || decoded.ProjectionEventVersion != CurrentProjectionEventVersion {
		t.Fatalf("decoded versions = %d/%d/%d/%d, want all %d",
			decoded.EnvelopeVersion, decoded.PayloadVersion, decoded.CodecVersion, decoded.ProjectionEventVersion,
			CurrentEnvelopeVersion)
	}
	if !decoded.CapturedAt.Equal(goldenCapturedAt()) {
		t.Fatalf("decoded captured_at = %v, want %v", decoded.CapturedAt, goldenCapturedAt())
	}
	if decoded.Archive.State != ArchiveStateActive {
		t.Fatalf("decoded archive state = %q, want %q", decoded.Archive.State, ArchiveStateActive)
	}
	if decoded.Archive.Attempts != want.Archive.Attempts {
		t.Fatalf("decoded archive attempts = %d, want %d", decoded.Archive.Attempts, want.Archive.Attempts)
	}
	if len(decoded.PayloadSHA256) != 64 {
		t.Fatalf("decoded payload hash %q is not SHA-256 hex", decoded.PayloadSHA256)
	}

	wantIdentity := want.Payload.Identity
	gotIdentity := decoded.Payload.Identity
	if gotIdentity != wantIdentity {
		t.Fatalf("decoded identity = %+v, want %+v", gotIdentity, wantIdentity)
	}

	gotLifecycle, wantLifecycle := decoded.Payload.Lifecycle, want.Payload.Lifecycle
	if gotLifecycle.Status != wantLifecycle.Status {
		t.Fatalf("decoded lifecycle status = %q, want %q", gotLifecycle.Status, wantLifecycle.Status)
	}
	if gotLifecycle.Success == nil || *gotLifecycle.Success != true || wantLifecycle.Success == nil || *wantLifecycle.Success != true {
		t.Fatalf("decoded lifecycle success = %v, want true (both non-nil)", gotLifecycle.Success)
	}
	for name, pair := range map[string][2]time.Time{
		"created_at":   {gotLifecycle.CreatedAt, wantLifecycle.CreatedAt},
		"started_at":   {gotLifecycle.StartedAt, wantLifecycle.StartedAt},
		"completed_at": {gotLifecycle.CompletedAt, wantLifecycle.CompletedAt},
	} {
		if !pair[0].Equal(pair[1]) {
			t.Fatalf("decoded lifecycle %s = %v, want %v", name, pair[0], pair[1])
		}
	}

	gotRouting, wantRouting := decoded.Payload.Routing, want.Payload.Routing
	if gotRouting != wantRouting {
		t.Fatalf("decoded routing = %+v, want %+v", gotRouting, wantRouting)
	}

	gotRequest, wantRequest := decoded.Payload.Request, want.Payload.Request
	if gotRequest.Protocol != wantRequest.Protocol {
		t.Fatalf("decoded request protocol = %q, want %q", gotRequest.Protocol, wantRequest.Protocol)
	}
	if !bytes.Equal(gotRequest.RawBody, wantRequest.RawBody) {
		t.Fatalf("decoded request raw body = %s, want %s", gotRequest.RawBody, wantRequest.RawBody)
	}
	if !bytes.Equal(gotRequest.CanonicalIR, wantRequest.CanonicalIR) {
		t.Fatalf("decoded request canonical IR = %s, want %s", gotRequest.CanonicalIR, wantRequest.CanonicalIR)
	}
	goldenJSONEqual(t, "request.extensions", gotRequest.Extensions, wantRequest.Extensions)

	if want.Payload.Response.RawBody != nil {
		gotResponse, wantResponse := decoded.Payload.Response, want.Payload.Response
		goldenJSONEqual(t, "response.raw_body", gotResponse.RawBody, wantResponse.RawBody)
		goldenJSONEqual(t, "response.client_ir", gotResponse.ClientIR, wantResponse.ClientIR)
		goldenJSONEqual(t, "response.stream_summary", gotResponse.StreamSummary, wantResponse.StreamSummary)
		goldenJSONEqual(t, "response.stream_chunks", gotResponse.StreamChunks, wantResponse.StreamChunks)
	}

	gotUsage, wantUsage := decoded.Payload.Usage, want.Payload.Usage
	if gotUsage != wantUsage {
		t.Fatalf("decoded usage = %+v, want %+v", gotUsage, wantUsage)
	}

	gotTimeline, wantTimeline := decoded.Payload.Timeline, want.Payload.Timeline
	for name, pair := range map[string][2]time.Time{
		"t0": {gotTimeline.T0, wantTimeline.T0},
		"t1": {gotTimeline.T1, wantTimeline.T1},
		"t2": {gotTimeline.T2, wantTimeline.T2},
		"t3": {gotTimeline.T3, wantTimeline.T3},
	} {
		if !pair[0].Equal(pair[1]) {
			t.Fatalf("decoded timeline %s = %v, want %v", name, pair[0], pair[1])
		}
	}
	if gotTimeline.TTFTMillis != wantTimeline.TTFTMillis || gotTimeline.LatencyMillis != wantTimeline.LatencyMillis {
		t.Fatalf("decoded timeline latencies = %d/%d, want %d/%d",
			gotTimeline.TTFTMillis, gotTimeline.LatencyMillis, wantTimeline.TTFTMillis, wantTimeline.LatencyMillis)
	}

	goldenJSONEqual(t, "attachments", decoded.Payload.Attachments, want.Payload.Attachments)
	if got, want := decoded.Payload.Warnings, want.Payload.Warnings; !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded warnings = %#v, want %#v", got, want)
	}
	if got, want := decoded.Payload.Integrity, want.Payload.Integrity; got != want {
		t.Fatalf("decoded integrity = %+v, want %+v", got, want)
	}
	if err := decoded.Payload.VerifyBodyHashes(); err != nil {
		t.Fatalf("decoded payload body hashes do not verify: %v", err)
	}
}

func TestGoldenCorpusDecodeToleratesAdditiveUnknownFields(t *testing.T) {
	t.Parallel()

	for _, c := range goldenCorpus() {
		t.Run(c.protocol+"/"+c.name, func(t *testing.T) {
			t.Parallel()

			parsed, err := c.parse([]byte(c.body))
			if err != nil {
				t.Fatalf("parse %s body: %v", c.protocol, err)
			}
			document, err := BuildClientContentDocument([]byte(c.body), c.protocol, parsed, nil)
			if err != nil {
				t.Fatalf("BuildClientContentDocument() error = %v", err)
			}
			encoded, err := Encode(NewEnvelope(goldenCapturedAt(), ArchiveStateActive, c.buildFact(t, document)))
			if err != nil {
				t.Fatalf("Encode() error = %v", err)
			}

			var body map[string]any
			if err := json.Unmarshal(encoded, &body); err != nil {
				t.Fatal(err)
			}
			future := map[string]any{"introduced_by": "future codec", "flags": []any{true, 7}}
			body["x_future_field"] = future
			body["archive"].(map[string]any)["x_future_field"] = "future archive metadata"
			payload := body["payload"].(map[string]any)
			payload["x_future_field"] = "future payload field ignored by v1"
			payload["request"].(map[string]any)["x_future_field"] = future
			withUnknownFields, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}

			decoded, err := Decode(withUnknownFields)
			if err != nil {
				t.Fatalf("Decode(additive fields) error = %v", err)
			}
			if got, want := decoded.Payload.Identity.RequestID, c.requestID(); got != want {
				t.Fatalf("known field identity.request_id lost = %q, want %q", got, want)
			}
			if got, want := decoded.Payload.Routing.ClientProtocol, c.protocol; got != want {
				t.Fatalf("known field routing.client_protocol lost = %q, want %q", got, want)
			}
			goldenJSONEqual(t, "request.raw_body", decoded.Payload.Request.RawBody, json.RawMessage(c.body))
			if !bytes.Equal(decoded.Payload.Request.CanonicalIR, document.CanonicalIR) {
				t.Fatalf("known field request.canonical_ir changed under additive fields:\n got %s\nwant %s",
					decoded.Payload.Request.CanonicalIR, document.CanonicalIR)
			}
			archivedIR, err := ir.DecodeRequestDocument(decoded.Payload.Request.CanonicalIR)
			if err != nil {
				t.Fatalf("DecodeRequestDocument after additive injection: %v", err)
			}
			archivedEncoded, err := ir.EncodeRequestDocument(archivedIR)
			if err != nil {
				t.Fatal(err)
			}
			parserEncoded, err := ir.EncodeRequestDocument(parsed)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(archivedEncoded, parserEncoded) {
				t.Fatalf("archived IR diverged after additive injection: got %s, want %s", archivedEncoded, parserEncoded)
			}
		})
	}
}

func TestGoldenCorpusDecodeRejectsTamperedPayload(t *testing.T) {
	t.Parallel()

	mutations := []struct {
		name  string
		apply func(t *testing.T, document map[string]any)
	}{
		{
			name: "tampered routing.client_model keeps original hash",
			apply: func(t *testing.T, document map[string]any) {
				document["payload"].(map[string]any)["routing"].(map[string]any)["client_model"] = "gpt-tampered"
			},
		},
		{
			name: "tampered request.protocol keeps original hash",
			apply: func(t *testing.T, document map[string]any) {
				document["payload"].(map[string]any)["request"].(map[string]any)["protocol"] = "tampered-protocol"
			},
		},
		{
			name: "tampered request.raw_body keeps original hash",
			apply: func(t *testing.T, document map[string]any) {
				document["payload"].(map[string]any)["request"].(map[string]any)["raw_body"] = map[string]any{"model": "tampered"}
			},
		},
	}

	for _, c := range goldenCorpus() {
		for _, mutation := range mutations {
			t.Run(c.protocol+"/"+c.name+"/"+mutation.name, func(t *testing.T) {
				t.Parallel()

				parsed, err := c.parse([]byte(c.body))
				if err != nil {
					t.Fatalf("parse %s body: %v", c.protocol, err)
				}
				document, err := BuildClientContentDocument([]byte(c.body), c.protocol, parsed, nil)
				if err != nil {
					t.Fatalf("BuildClientContentDocument() error = %v", err)
				}
				encoded, err := Encode(NewEnvelope(goldenCapturedAt(), ArchiveStateActive, c.buildFact(t, document)))
				if err != nil {
					t.Fatalf("Encode() error = %v", err)
				}
				var body map[string]any
				if err := json.Unmarshal(encoded, &body); err != nil {
					t.Fatal(err)
				}
				mutation.apply(t, body)
				tampered, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := Decode(tampered); !errors.Is(err, ErrIntegrityMismatch) {
					t.Fatalf("Decode(tampered payload) error = %v, want ErrIntegrityMismatch", err)
				}
			})
		}
	}
}

// TestGoldenDecodeRejectsRawJSONDefects feeds hand-written archive JSON (not
// produced by Encode) so that null core fields, duplicate keys, multiple
// top-level values, and a wrong payload hash are all exercised on the decode
// side. Errors must be classifiable, not opaque.
func TestGoldenDecodeRejectsRawJSONDefects(t *testing.T) {
	t.Parallel()

	const handWrittenEnvelope = `{"envelope_version":1,"payload_version":1,"codec_version":1,"projection_event_version":1,` +
		`"captured_at":"2026-08-27T10:00:05Z","archive":{"state":"active"},` +
		`"payload":{"identity":{"tenant_id":"tenant-raw","request_id":"req-raw"},` +
		`"lifecycle":{"status":"completed","created_at":"2026-08-27T10:00:00Z","completed_at":"2026-08-27T10:00:02Z"},` +
		`"routing":{"client_protocol":"openai-chat","client_model":"gpt-4o-mini"},` +
		`"request":{"raw_body":{},"canonical_ir":{}}},` +
		`"payload_sha256":"0000000000000000000000000000000000000000000000000000000000000000"}`

	testCases := []struct {
		name   string
		mutate func(t *testing.T, raw string) string
		assert func(t *testing.T, err error)
	}{
		{
			name: "null captured_at",
			mutate: func(t *testing.T, raw string) string {
				return goldenReplaceOnce(t, raw, `"captured_at":"2026-08-27T10:00:05Z"`, `"captured_at":null`)
			},
			assert: func(t *testing.T, err error) {
				if err == nil || errors.Is(err, ErrIntegrityMismatch) || !strings.Contains(err.Error(), "captured_at required") {
					t.Fatalf("Decode(null captured_at) error = %v, want captured_at required validation error", err)
				}
			},
		},
		{
			name: "null archive state",
			mutate: func(t *testing.T, raw string) string {
				return goldenReplaceOnce(t, raw, `"state":"active"`, `"state":null`)
			},
			assert: func(t *testing.T, err error) {
				if err == nil || !strings.Contains(err.Error(), "unsupported archive state") {
					t.Fatalf("Decode(null state) error = %v, want unsupported archive state error", err)
				}
			},
		},
		{
			name: "null identity tenant_id",
			mutate: func(t *testing.T, raw string) string {
				return goldenReplaceOnce(t, raw, `"tenant_id":"tenant-raw"`, `"tenant_id":null`)
			},
			assert: func(t *testing.T, err error) {
				if err == nil || !strings.Contains(err.Error(), "tenant_id and request_id required") {
					t.Fatalf("Decode(null tenant_id) error = %v, want identity validation error", err)
				}
			},
		},
		{
			name: "null payload_sha256",
			mutate: func(t *testing.T, raw string) string {
				return goldenReplaceOnce(t, raw,
					`"payload_sha256":"0000000000000000000000000000000000000000000000000000000000000000"`,
					`"payload_sha256":null`)
			},
			assert: func(t *testing.T, err error) {
				if !errors.Is(err, ErrIntegrityMismatch) {
					t.Fatalf("Decode(null payload_sha256) error = %v, want ErrIntegrityMismatch", err)
				}
			},
		},
		{
			name: "duplicate envelope_version keys last wins unsupported",
			mutate: func(t *testing.T, raw string) string {
				return goldenReplaceOnce(t, raw, `"envelope_version":1,`, `"envelope_version":1,"envelope_version":2,`)
			},
			assert: func(t *testing.T, err error) {
				if !errors.Is(err, ErrUnsupportedVersion) {
					t.Fatalf("Decode(duplicate envelope_version) error = %v, want ErrUnsupportedVersion", err)
				}
			},
		},
		{
			name:   "wrong payload_sha256",
			mutate: func(t *testing.T, raw string) string { return raw },
			assert: func(t *testing.T, err error) {
				if !errors.Is(err, ErrIntegrityMismatch) {
					t.Fatalf("Decode(wrong hash) error = %v, want ErrIntegrityMismatch", err)
				}
			},
		},
		{
			name:   "multiple top-level JSON values",
			mutate: func(t *testing.T, raw string) string { return raw + "\n" + handWrittenEnvelope },
			assert: func(t *testing.T, err error) {
				if err == nil || !strings.Contains(err.Error(), "decode envelope") {
					t.Fatalf("Decode(trailing JSON value) error = %v, want envelope decode error", err)
				}
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			_, err := Decode([]byte(testCase.mutate(t, handWrittenEnvelope)))
			if err == nil {
				t.Fatal("Decode() error = nil, want structured rejection")
			}
			testCase.assert(t, err)
		})
	}
}

// TestGoldenDecodeRejectsDuplicateKeysWithValidHash duplicates a core key in
// an otherwise valid encoded archive (correct hash) so the rejection is
// attributable to the duplicate key itself, not to a stale hash.
func TestGoldenDecodeRejectsDuplicateKeysWithValidHash(t *testing.T) {
	t.Parallel()

	parsed, err := ir.ParseOpenAI([]byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	document, err := BuildClientContentDocument([]byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}`),
		ir.ProtocolOpenAIChat, parsed, nil)
	if err != nil {
		t.Fatal(err)
	}
	fact := goldenCorpusCase{
		name: "duplicate-keys", scenario: goldenScenarioText,
		protocol: ir.ProtocolOpenAIChat, model: "gpt-4o-mini",
		body: `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}`,
	}.buildFact(t, document)
	encoded, err := Encode(NewEnvelope(goldenCapturedAt(), ArchiveStateActive, fact))
	if err != nil {
		t.Fatal(err)
	}

	var probe RequestArchiveEnvelope
	if err := json.Unmarshal(encoded, &probe); err != nil {
		t.Fatal(err)
	}

	duplicatedModel := goldenReplaceOnce(t, string(encoded),
		`"client_model":"gpt-4o-mini"`, `"client_model":"gpt-4o-mini","client_model":"gpt-tampered"`)
	if _, err := Decode([]byte(duplicatedModel)); !errors.Is(err, ErrIntegrityMismatch) {
		t.Fatalf("Decode(duplicate client_model) error = %v, want ErrIntegrityMismatch", err)
	}

	duplicatedHash := goldenReplaceOnce(t, string(encoded),
		`"payload_sha256":"`+probe.PayloadSHA256+`"`,
		`"payload_sha256":"`+probe.PayloadSHA256+`","payload_sha256":""`)
	if _, err := Decode([]byte(duplicatedHash)); !errors.Is(err, ErrIntegrityMismatch) {
		t.Fatalf("Decode(duplicate payload_sha256) error = %v, want ErrIntegrityMismatch", err)
	}
}

func goldenReplaceOnce(t *testing.T, body, old, replacement string) string {
	t.Helper()
	if count := strings.Count(body, old); count != 1 {
		t.Fatalf("golden corpus raw JSON surgery: expected exactly one occurrence of %s, found %d", old, count)
	}
	return strings.Replace(body, old, replacement, 1)
}

// goldenJSONEqual compares two raw JSON documents by value so key ordering and
// whitespace differences (introduced by the canonical encoder) do not fail.
func goldenJSONEqual(t *testing.T, field string, got, want json.RawMessage) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decoded %s is not valid JSON: %v (%s)", field, err, got)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("want %s is not valid JSON: %v (%s)", field, err, want)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("decoded %s = %s, want %s", field, got, want)
	}
}

func goldenFindMessage(req *ir.InternalRequest, role string) (ir.Message, bool) {
	for _, message := range req.Messages {
		if message.Role == role {
			return message, true
		}
	}
	return ir.Message{}, false
}

func goldenFindBlock(req *ir.InternalRequest, blockType string) (ir.ContentBlock, bool) {
	for _, message := range req.Messages {
		for _, block := range message.Content {
			if block.Type == blockType {
				return block, true
			}
		}
	}
	return ir.ContentBlock{}, false
}

func goldenRequireText(t *testing.T, block ir.ContentBlock, want string) {
	t.Helper()
	if block.Type != "text" || block.Text != want {
		t.Fatalf("text block = {type:%q text:%q}, want {type:text text:%q}", block.Type, block.Text, want)
	}
}

func goldenAssertStreamChunks(t *testing.T, envelope *RequestArchiveEnvelope, wantChunks int, wantFragments ...string) {
	t.Helper()
	chunks := envelope.Payload.Response.StreamChunks
	if len(chunks) == 0 {
		t.Fatal("response.stream_chunks is empty")
	}
	var events []json.RawMessage
	if err := json.Unmarshal(chunks, &events); err != nil {
		t.Fatalf("response.stream_chunks is not a JSON array: %v", err)
	}
	if got := len(events); got != wantChunks {
		t.Fatalf("stream chunk count = %d, want %d", got, wantChunks)
	}
	for _, fragment := range wantFragments {
		if !bytes.Contains(chunks, []byte(fragment)) {
			t.Fatalf("stream chunks %s do not contain fragment %q", chunks, fragment)
		}
	}
}

func goldenCorpus() []goldenCorpusCase {
	corpus := []goldenCorpusCase{
		// ─── openai-chat ───
		{
			name: "text", scenario: goldenScenarioText,
			protocol: ir.ProtocolOpenAIChat, model: "gpt-4o-mini",
			body:  `{"model":"gpt-4o-mini","messages":[{"role":"system","content":"You are terse."},{"role":"user","content":"hello"}],"max_tokens":64}`,
			parse: ir.ParseOpenAI,
			verify: func(t *testing.T, req *ir.InternalRequest) {
				if req.Model != "gpt-4o-mini" {
					t.Fatalf("model = %q, want gpt-4o-mini", req.Model)
				}
				if req.System == nil || req.System.Content != "You are terse." {
					t.Fatalf("system prompt = %#v, want content You are terse.", req.System)
				}
				if req.MaxTokens != 64 {
					t.Fatalf("max_tokens = %d, want 64", req.MaxTokens)
				}
				message, ok := goldenFindMessage(req, "user")
				if !ok || len(message.Content) != 1 {
					t.Fatalf("user message = %#v", message)
				}
				goldenRequireText(t, message.Content[0], "hello")
			},
		},
		{
			name: "multimodal", scenario: goldenScenarioMultimodal,
			protocol: ir.ProtocolOpenAIChat, model: "gpt-4o",
			body:  `{"model":"gpt-4o","messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"https://example.invalid/cat.png","detail":"high"}},{"type":"input_audio","input_audio":{"data":"aGVsbG8=","format":"wav"}}]}]}`,
			parse: ir.ParseOpenAI,
			verify: func(t *testing.T, req *ir.InternalRequest) {
				image, ok := goldenFindBlock(req, "image")
				if !ok || image.Image == nil {
					t.Fatalf("image block = %#v", image)
				}
				if image.Image.URL != "https://example.invalid/cat.png" || image.Image.Detail != "high" {
					t.Fatalf("image source = %#v, want url + detail high", image.Image)
				}
				audio, ok := goldenFindBlock(req, "input_audio")
				if !ok || audio.InputAudio == nil {
					t.Fatalf("input_audio block = %#v", audio)
				}
				if audio.InputAudio.Data != "aGVsbG8=" || audio.InputAudio.Format != "wav" {
					t.Fatalf("input audio = %#v, want base64 wav payload", audio.InputAudio)
				}
				message, _ := goldenFindMessage(req, "user")
				goldenRequireText(t, message.Content[0], "describe")
			},
		},
		{
			name: "tool_calls", scenario: goldenScenarioToolCalls,
			protocol: ir.ProtocolOpenAIChat, model: "gpt-4o",
			body:  `{"model":"gpt-4o","messages":[{"role":"user","content":"weather?"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_oc_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"SF\"}"}}]}],"tools":[{"type":"function","function":{"name":"get_weather","description":"Get weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}],"tool_choice":"auto"}`,
			parse: ir.ParseOpenAI,
			verify: func(t *testing.T, req *ir.InternalRequest) {
				assistant, ok := goldenFindMessage(req, "assistant")
				if !ok || len(assistant.ToolCalls) != 1 {
					t.Fatalf("assistant message = %#v, want one tool call", assistant)
				}
				call := assistant.ToolCalls[0]
				if call.ID != "call_oc_1" || call.Type != "function" ||
					call.Function.Name != "get_weather" || call.Function.Arguments != `{"city":"SF"}` {
					t.Fatalf("tool call = %#v, want call_oc_1/get_weather/{\"city\":\"SF\"}", call)
				}
				if len(req.Tools) != 1 || req.Tools[0].Name != "get_weather" {
					t.Fatalf("tools = %#v, want get_weather", req.Tools)
				}
				goldenJSONEqual(t, "tools[0].parameters", req.Tools[0].Parameters,
					json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`))
				if req.ToolChoice == nil || req.ToolChoice.Type != "auto" {
					t.Fatalf("tool_choice = %#v, want auto", req.ToolChoice)
				}
			},
		},
		{
			name: "tool_result", scenario: goldenScenarioToolResult,
			protocol: ir.ProtocolOpenAIChat, model: "gpt-4o",
			body:  `{"model":"gpt-4o","messages":[{"role":"user","content":"weather?"},{"role":"tool","tool_call_id":"call_oc_1","name":"get_weather","content":"18C and foggy"}]}`,
			parse: ir.ParseOpenAI,
			verify: func(t *testing.T, req *ir.InternalRequest) {
				message, ok := goldenFindMessage(req, "tool")
				if !ok {
					t.Fatal("tool role message missing")
				}
				if message.ToolCallID != "call_oc_1" || message.Name != "get_weather" {
					t.Fatalf("tool message = %+v, want call_oc_1/get_weather", message)
				}
				if len(message.Content) != 1 {
					t.Fatalf("tool message content = %#v", message.Content)
				}
				goldenRequireText(t, message.Content[0], "18C and foggy")
			},
		},
		{
			name: "provider_raw", scenario: goldenScenarioProviderRaw,
			protocol: ir.ProtocolOpenAIChat, model: "gpt-4o-mini",
			body:       `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"x_minimax_bot_setting":{"chars":[{"name":"A"}]}}`,
			parse:      ir.ParseOpenAI,
			extensions: `{"x_archive_note":{"origin":"golden-corpus"}}`,
			verify: func(t *testing.T, req *ir.InternalRequest) {
				raw, ok := req.Extensions["x_minimax_bot_setting"]
				if !ok {
					t.Fatalf("IR extensions = %#v, want x_minimax_bot_setting", req.Extensions)
				}
				goldenJSONEqual(t, "extensions.x_minimax_bot_setting", raw, json.RawMessage(`{"chars":[{"name":"A"}]}`))
			},
		},
		{
			name: "unknown_block", scenario: goldenScenarioUnknownBlock,
			protocol: ir.ProtocolOpenAIChat, model: "gpt-4o",
			body:  `{"model":"gpt-4o","messages":[{"role":"user","content":[{"type":"text","text":"go"},{"type":"web_search_call","id":"ws_1","status":"completed"}]}]}`,
			parse: ir.ParseOpenAI,
			verify: func(t *testing.T, req *ir.InternalRequest) {
				block, ok := goldenFindBlock(req, "web_search_call")
				if !ok {
					t.Fatalf("unknown block web_search_call missing from %#v", req.Messages)
				}
				raw, ok := block.RawContent.(string)
				if !ok || !strings.Contains(raw, `"ws_1"`) || !strings.Contains(raw, `"web_search_call"`) {
					t.Fatalf("unknown block raw content = %#v, want replayable JSON with id and type", block.RawContent)
				}
			},
		},
		goldenSSECase(ir.ProtocolOpenAIChat, "gpt-4o",
			`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}],"stream":true}`,
			goldenResponseFixture{
				rawBody:       `{"id":"chatcmpl-oc-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":7,"total_tokens":19}}`,
				clientIR:      `{"role":"assistant","content":[{"type":"text","text":"Hello"}]}`,
				streamSummary: `{"chunk_count":4,"finish_reason":"stop","aggregated_text":"Hello"}`,
				streamChunks: `[{"id":"chatcmpl-oc-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]},` +
					`{"id":"chatcmpl-oc-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hel"},"finish_reason":null}]},` +
					`{"id":"chatcmpl-oc-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":null}]},` +
					`{"id":"chatcmpl-oc-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}]`,
			},
			func(t *testing.T, envelope *RequestArchiveEnvelope) {
				goldenAssertStreamChunks(t, envelope, 4, `"content":"Hel"`, `"finish_reason":"stop"`)
			}),

		// ─── anthropic-messages ───
		{
			name: "text", scenario: goldenScenarioText,
			protocol: ir.ProtocolAnthropicMessages, model: "claude-sonnet-4-5",
			body:  `{"model":"claude-sonnet-4-5","max_tokens":128,"system":"Be brief.","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`,
			parse: ir.ParseAnthropic,
			verify: func(t *testing.T, req *ir.InternalRequest) {
				if req.Model != "claude-sonnet-4-5" || req.MaxTokens != 128 {
					t.Fatalf("model/max_tokens = %q/%d", req.Model, req.MaxTokens)
				}
				if req.System == nil || req.System.Content != "Be brief." {
					t.Fatalf("system prompt = %#v, want Be brief.", req.System)
				}
				message, ok := goldenFindMessage(req, "user")
				if !ok || len(message.Content) != 1 {
					t.Fatalf("user message = %#v", message)
				}
				goldenRequireText(t, message.Content[0], "hello")
			},
		},
		{
			name: "multimodal", scenario: goldenScenarioMultimodal,
			protocol: ir.ProtocolAnthropicMessages, model: "claude-sonnet-4-5",
			body:  `{"model":"claude-sonnet-4-5","max_tokens":256,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aVJCT1I="}},{"type":"text","text":"what is this"}]}]}`,
			parse: ir.ParseAnthropic,
			verify: func(t *testing.T, req *ir.InternalRequest) {
				image, ok := goldenFindBlock(req, "image")
				if !ok || image.Image == nil {
					t.Fatalf("image block = %#v", image)
				}
				if image.Image.Type != "base64" || image.Image.MediaType != "image/png" || image.Image.Data != "aVJCT1I=" {
					t.Fatalf("image source = %#v, want base64 png payload", image.Image)
				}
				message, _ := goldenFindMessage(req, "user")
				goldenRequireText(t, message.Content[1], "what is this")
			},
		},
		{
			name: "tool_calls", scenario: goldenScenarioToolCalls,
			protocol: ir.ProtocolAnthropicMessages, model: "claude-sonnet-4-5",
			body:  `{"model":"claude-sonnet-4-5","max_tokens":256,"messages":[{"role":"user","content":[{"type":"text","text":"weather?"}]},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_am_1","name":"get_weather","input":{"city":"SF"}}]}],"tools":[{"name":"get_weather","description":"Get weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}]}`,
			parse: ir.ParseAnthropic,
			verify: func(t *testing.T, req *ir.InternalRequest) {
				block, ok := goldenFindBlock(req, "tool_use")
				if !ok || block.ToolUse == nil {
					t.Fatalf("tool_use block = %#v", block)
				}
				if block.ToolUse.ID != "toolu_am_1" || block.ToolUse.Name != "get_weather" {
					t.Fatalf("tool use = %#v, want toolu_am_1/get_weather", block.ToolUse)
				}
				goldenJSONEqual(t, "tool_use.input", block.ToolUse.Input, json.RawMessage(`{"city":"SF"}`))
				assistant, _ := goldenFindMessage(req, "assistant")
				if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].ID != "toolu_am_1" ||
					assistant.ToolCalls[0].Function.Name != "get_weather" {
					t.Fatalf("mirrored tool calls = %#v, want toolu_am_1/get_weather", assistant.ToolCalls)
				}
				if len(req.Tools) != 1 || req.Tools[0].Name != "get_weather" {
					t.Fatalf("tools = %#v, want get_weather", req.Tools)
				}
			},
		},
		{
			name: "tool_result", scenario: goldenScenarioToolResult,
			protocol: ir.ProtocolAnthropicMessages, model: "claude-sonnet-4-5",
			body:  `{"model":"claude-sonnet-4-5","max_tokens":256,"messages":[{"role":"user","content":[{"type":"text","text":"weather?"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_am_1","content":[{"type":"text","text":"18C"}],"is_error":false}]}]}`,
			parse: ir.ParseAnthropic,
			verify: func(t *testing.T, req *ir.InternalRequest) {
				block, ok := goldenFindBlock(req, "tool_result")
				if !ok || block.ToolResult == nil {
					t.Fatalf("tool_result block = %#v", block)
				}
				if block.ToolResult.ToolUseID != "toolu_am_1" || block.ToolResult.IsError {
					t.Fatalf("tool result = %#v, want toolu_am_1 not error", block.ToolResult)
				}
				if len(block.ToolResult.Content) != 1 {
					t.Fatalf("tool result content = %#v", block.ToolResult.Content)
				}
				goldenRequireText(t, block.ToolResult.Content[0], "18C")
			},
		},
		{
			name: "thinking_signature", scenario: goldenScenarioThinking,
			protocol: ir.ProtocolAnthropicMessages, model: "claude-sonnet-4-5",
			body:  `{"model":"claude-sonnet-4-5","max_tokens":1024,"thinking":{"type":"enabled","budget_tokens":512},"messages":[{"role":"user","content":[{"type":"text","text":"weather?"}]},{"role":"assistant","content":[{"type":"thinking","thinking":"Check the weather API.","signature":"c2lnLWFtLTE="},{"type":"text","text":"Checking."}]}]}`,
			parse: ir.ParseAnthropic,
			verify: func(t *testing.T, req *ir.InternalRequest) {
				if req.Thinking == nil || req.Thinking.Type != "enabled" || req.Thinking.BudgetTokens != 512 {
					t.Fatalf("thinking config = %#v, want enabled/512", req.Thinking)
				}
				block, ok := goldenFindBlock(req, "thinking")
				if !ok || block.Thinking == nil {
					t.Fatalf("thinking block = %#v", block)
				}
				if block.Thinking.Thinking != "Check the weather API." || block.Thinking.Signature != "c2lnLWFtLTE=" {
					t.Fatalf("thinking block = %#v, want reasoning text plus signature", block.Thinking)
				}
			},
		},
		{
			name: "provider_raw", scenario: goldenScenarioProviderRaw,
			protocol: ir.ProtocolAnthropicMessages, model: "claude-sonnet-4-5",
			body:       `{"model":"claude-sonnet-4-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"x_context_management":{"edits":[{"type":"clear_tool_uses_20250919"}]}}`,
			parse:      ir.ParseAnthropic,
			extensions: `{"x_archive_note":{"origin":"golden-corpus"}}`,
			verify: func(t *testing.T, req *ir.InternalRequest) {
				raw, ok := req.Extensions["x_context_management"]
				if !ok {
					t.Fatalf("IR extensions = %#v, want x_context_management", req.Extensions)
				}
				goldenJSONEqual(t, "extensions.x_context_management", raw,
					json.RawMessage(`{"edits":[{"type":"clear_tool_uses_20250919"}]}`))
			},
		},
		{
			name: "unknown_block", scenario: goldenScenarioUnknownBlock,
			protocol: ir.ProtocolAnthropicMessages, model: "claude-sonnet-4-5",
			body:  `{"model":"claude-sonnet-4-5","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"go"},{"type":"server_tool_use","id":"srvtoolu_1","name":"code_search","input":{"q":"codec"}}]}]}`,
			parse: ir.ParseAnthropic,
			verify: func(t *testing.T, req *ir.InternalRequest) {
				block, ok := goldenFindBlock(req, "server_tool_use")
				if !ok {
					t.Fatalf("unknown block server_tool_use missing from %#v", req.Messages)
				}
				raw, ok := block.RawContent.(string)
				if !ok || !strings.Contains(raw, `"srvtoolu_1"`) || !strings.Contains(raw, `"server_tool_use"`) {
					t.Fatalf("unknown block raw content = %#v, want replayable JSON with id and type", block.RawContent)
				}
			},
		},
		goldenSSECase(ir.ProtocolAnthropicMessages, "claude-sonnet-4-5",
			`{"model":"claude-sonnet-4-5","max_tokens":128,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}],"stream":true}`,
			goldenResponseFixture{
				rawBody:       `{"id":"msg_am_1","type":"message","stop_reason":"end_turn","content":[{"type":"thinking","thinking":"Check the weather.","signature":"c2lnLWFtLTE="},{"type":"text","text":"18C"}],"usage":{"input_tokens":12,"output_tokens":7}}`,
				clientIR:      `{"role":"assistant","content":[{"type":"thinking","thinking":"Check the weather.","signature":"c2lnLWFtLTE="},{"type":"text","text":"18C"}]}`,
				streamSummary: `{"chunk_count":9,"stop_reason":"end_turn","blocks":["thinking","text"]}`,
				streamChunks: `[{"type":"message_start","message":{"id":"msg_am_1","usage":{"input_tokens":12,"output_tokens":1}}},` +
					`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}},` +
					`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Check the weather."}},` +
					`{"type":"content_block_stop","index":0},` +
					`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}},` +
					`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"18C"}},` +
					`{"type":"content_block_stop","index":1},` +
					`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}},` +
					`{"type":"message_stop"}]`,
			},
			func(t *testing.T, envelope *RequestArchiveEnvelope) {
				goldenAssertStreamChunks(t, envelope, 9, `"thinking_delta"`, `"text_delta"`, `"stop_reason":"end_turn"`)
			}),

		// ─── gemini-generate ───
		// Note: the gemini parser drops unknown `parts` entries silently, so the
		// unknown-block scenario is covered by the block-preserving protocols
		// above; gemini additive tolerance is exercised through the additive
		// decode test applied to every corpus case.
		{
			name: "text", scenario: goldenScenarioText,
			protocol: ir.ProtocolGeminiGenerate, model: "gemini-2.5-flash",
			body:  `{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"generationConfig":{"maxOutputTokens":64,"temperature":0.7}}`,
			parse: ir.ParseGemini,
			verify: func(t *testing.T, req *ir.InternalRequest) {
				if req.Temperature == nil || *req.Temperature != 0.7 {
					t.Fatalf("temperature = %v, want 0.7", req.Temperature)
				}
				if req.MaxTokens != 64 {
					t.Fatalf("max tokens = %d, want 64", req.MaxTokens)
				}
				message, ok := goldenFindMessage(req, "user")
				if !ok || len(message.Content) != 1 {
					t.Fatalf("user message = %#v", message)
				}
				goldenRequireText(t, message.Content[0], "hello")
			},
		},
		{
			name: "multimodal", scenario: goldenScenarioMultimodal,
			protocol: ir.ProtocolGeminiGenerate, model: "gemini-2.5-flash",
			body:  `{"contents":[{"role":"user","parts":[{"text":"describe"},{"inlineData":{"mimeType":"image/png","data":"aVJCT1I="}},{"inlineData":{"mimeType":"audio/wav","data":"YXVkaW8="}}]}]}`,
			parse: ir.ParseGemini,
			verify: func(t *testing.T, req *ir.InternalRequest) {
				image, ok := goldenFindBlock(req, "image")
				if !ok || image.Image == nil {
					t.Fatalf("image block = %#v", image)
				}
				if image.Image.Type != "base64" || image.Image.MediaType != "image/png" || image.Image.Data != "aVJCT1I=" {
					t.Fatalf("image source = %#v, want base64 png payload", image.Image)
				}
				audio, ok := goldenFindBlock(req, "audio")
				if !ok || audio.Audio == nil {
					t.Fatalf("audio block = %#v", audio)
				}
				if audio.Audio.MediaType != "audio/wav" || audio.Audio.Data != "YXVkaW8=" {
					t.Fatalf("audio source = %#v, want base64 wav payload", audio.Audio)
				}
				message, _ := goldenFindMessage(req, "user")
				goldenRequireText(t, message.Content[0], "describe")
			},
		},
		{
			name: "tool_calls", scenario: goldenScenarioToolCalls,
			protocol: ir.ProtocolGeminiGenerate, model: "gemini-2.5-flash",
			body:  `{"contents":[{"role":"user","parts":[{"text":"weather?"}]},{"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"city":"SF"}}}]}],"tools":[{"functionDeclarations":[{"name":"get_weather","description":"Get weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}]}]}`,
			parse: ir.ParseGemini,
			verify: func(t *testing.T, req *ir.InternalRequest) {
				block, ok := goldenFindBlock(req, "tool_use")
				if !ok || block.ToolUse == nil {
					t.Fatalf("tool_use block = %#v", block)
				}
				if block.ToolUse.Name != "get_weather" {
					t.Fatalf("tool use name = %q, want get_weather", block.ToolUse.Name)
				}
				goldenJSONEqual(t, "tool_use.input", block.ToolUse.Input, json.RawMessage(`{"city":"SF"}`))
				if len(req.Tools) != 1 || req.Tools[0].Name != "get_weather" {
					t.Fatalf("tools = %#v, want get_weather", req.Tools)
				}
			},
		},
		{
			name: "tool_result", scenario: goldenScenarioToolResult,
			protocol: ir.ProtocolGeminiGenerate, model: "gemini-2.5-flash",
			body:  `{"contents":[{"role":"user","parts":[{"text":"weather?"}]},{"role":"user","parts":[{"functionResponse":{"name":"get_weather","response":{"temperature":"18C","fog":true}}}]}]}`,
			parse: ir.ParseGemini,
			verify: func(t *testing.T, req *ir.InternalRequest) {
				block, ok := goldenFindBlock(req, "tool_result")
				if !ok || block.ToolResult == nil {
					t.Fatalf("tool_result block = %#v", block)
				}
				if block.ToolResult.ToolUseID == "" {
					t.Fatalf("tool result id = %q, want synthesized id", block.ToolResult.ToolUseID)
				}
				goldenJSONEqual(t, "tool_result.gemini_response", block.ToolResult.GeminiResponse,
					json.RawMessage(`{"temperature":"18C","fog":true}`))
				if len(block.ToolResult.Content) != 1 {
					t.Fatalf("tool result content = %#v", block.ToolResult.Content)
				}
				goldenJSONEqual(t, "tool_result.content[0].text", json.RawMessage(block.ToolResult.Content[0].Text),
					json.RawMessage(`{"temperature":"18C","fog":true}`))
			},
		},
		{
			name: "provider_raw", scenario: goldenScenarioProviderRaw,
			protocol: ir.ProtocolGeminiGenerate, model: "gemini-2.5-flash",
			body:       `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"x_vertex_session":{"ttl":"3600s"}}`,
			parse:      ir.ParseGemini,
			extensions: `{"x_archive_note":{"origin":"golden-corpus"}}`,
			verify: func(t *testing.T, req *ir.InternalRequest) {
				raw, ok := req.Extensions["x_vertex_session"]
				if !ok {
					t.Fatalf("IR extensions = %#v, want x_vertex_session", req.Extensions)
				}
				goldenJSONEqual(t, "extensions.x_vertex_session", raw, json.RawMessage(`{"ttl":"3600s"}`))
			},
		},
		goldenSSECase(ir.ProtocolGeminiGenerate, "gemini-2.5-flash",
			`{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"generationConfig":{"maxOutputTokens":64}}`,
			goldenResponseFixture{
				rawBody:       `{"candidates":[{"content":{"parts":[{"text":"Hello."}],"role":"model"},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":7,"totalTokenCount":19}}`,
				clientIR:      `{"role":"assistant","content":[{"type":"text","text":"Hello."}]}`,
				streamSummary: `{"chunk_count":3,"finish_reason":"STOP"}`,
				streamChunks: `[{"candidates":[{"content":{"parts":[{"text":"Hel"}],"role":"model"},"index":0}],"usageMetadata":{"promptTokenCount":12}},` +
					`{"candidates":[{"content":{"parts":[{"text":"lo"}],"role":"model"},"index":0}],"usageMetadata":{"candidatesTokenCount":4}},` +
					`{"candidates":[{"content":{"parts":[{"text":"."}],"role":"model"},"finishReason":"STOP","index":0}],"usageMetadata":{"candidatesTokenCount":7}}]`,
			},
			func(t *testing.T, envelope *RequestArchiveEnvelope) {
				goldenAssertStreamChunks(t, envelope, 3, `"text":"Hel"`, `"finishReason":"STOP"`)
			}),

		// ─── openai-responses ───
		{
			name: "text", scenario: goldenScenarioText,
			protocol: ir.ProtocolOpenAIResponses, model: "gpt-4o",
			body:  `{"model":"gpt-4o","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}],"max_output_tokens":64}`,
			parse: ir.ParseResponses,
			verify: func(t *testing.T, req *ir.InternalRequest) {
				if req.Model != "gpt-4o" || req.MaxTokens != 64 {
					t.Fatalf("model/max tokens = %q/%d", req.Model, req.MaxTokens)
				}
				message, ok := goldenFindMessage(req, "user")
				if !ok || len(message.Content) != 1 {
					t.Fatalf("user message = %#v", message)
				}
				goldenRequireText(t, message.Content[0], "hello")
			},
		},
		{
			name: "multimodal", scenario: goldenScenarioMultimodal,
			protocol: ir.ProtocolOpenAIResponses, model: "gpt-4o",
			body:  `{"model":"gpt-4o","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"describe"},{"type":"input_image","image_url":"https://example.invalid/cat.png","detail":"high"}]}]}`,
			parse: ir.ParseResponses,
			verify: func(t *testing.T, req *ir.InternalRequest) {
				image, ok := goldenFindBlock(req, "image")
				if !ok || image.Image == nil {
					t.Fatalf("image block = %#v", image)
				}
				if image.Image.URL != "https://example.invalid/cat.png" || image.Image.Detail != "high" {
					t.Fatalf("image source = %#v, want url + detail high", image.Image)
				}
				message, _ := goldenFindMessage(req, "user")
				goldenRequireText(t, message.Content[0], "describe")
			},
		},
		{
			name: "tool_calls", scenario: goldenScenarioToolCalls,
			protocol: ir.ProtocolOpenAIResponses, model: "gpt-4o",
			body:  `{"model":"gpt-4o","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"weather?"}]},{"type":"function_call","name":"get_weather","arguments":"{\"city\":\"SF\"}","call_id":"call_or_1"}],"tools":[{"type":"function","name":"get_weather","description":"Get weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}]}`,
			parse: ir.ParseResponses,
			verify: func(t *testing.T, req *ir.InternalRequest) {
				assistant, ok := goldenFindMessage(req, "assistant")
				if !ok || len(assistant.ToolCalls) != 1 {
					t.Fatalf("assistant message = %#v, want one tool call", assistant)
				}
				call := assistant.ToolCalls[0]
				if call.ID != "call_or_1" || call.Function.Name != "get_weather" || call.Function.Arguments != `{"city":"SF"}` {
					t.Fatalf("tool call = %#v, want call_or_1/get_weather/{\"city\":\"SF\"}", call)
				}
				if len(req.Tools) != 1 || req.Tools[0].Name != "get_weather" {
					t.Fatalf("tools = %#v, want get_weather", req.Tools)
				}
			},
		},
		{
			name: "tool_result", scenario: goldenScenarioToolResult,
			protocol: ir.ProtocolOpenAIResponses, model: "gpt-4o",
			body:  `{"model":"gpt-4o","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"weather?"}]},{"type":"function_call_output","call_id":"call_or_1","output":"18C and foggy"}]}`,
			parse: ir.ParseResponses,
			verify: func(t *testing.T, req *ir.InternalRequest) {
				message, ok := goldenFindMessage(req, "tool")
				if !ok {
					t.Fatal("tool role message missing")
				}
				if message.ToolCallID != "call_or_1" {
					t.Fatalf("tool message id = %q, want call_or_1", message.ToolCallID)
				}
				block, ok := goldenFindBlock(req, "tool_result")
				if !ok || block.ToolResult == nil {
					t.Fatalf("tool_result block = %#v", block)
				}
				if block.ToolResult.ToolUseID != "call_or_1" || len(block.ToolResult.Content) != 1 {
					t.Fatalf("tool result = %#v, want call_or_1 with text content", block.ToolResult)
				}
				goldenRequireText(t, block.ToolResult.Content[0], "18C and foggy")
			},
		},
		{
			name: "thinking_signature", scenario: goldenScenarioThinking,
			protocol: ir.ProtocolOpenAIResponses, model: "gpt-5",
			body:  `{"model":"gpt-5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"weather?"}]},{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Need the weather tool."}],"encrypted_content":"cGFkZHk="}],"reasoning":{"effort":"medium"}}`,
			parse: ir.ParseResponses,
			verify: func(t *testing.T, req *ir.InternalRequest) {
				if req.Reasoning == nil || req.Reasoning.Effort != "medium" {
					t.Fatalf("reasoning config = %#v, want effort medium", req.Reasoning)
				}
				assistant, ok := goldenFindMessage(req, "assistant")
				if !ok || len(assistant.Content) != 1 {
					t.Fatalf("assistant message = %#v, want one reasoning item", assistant)
				}
				raw, ok := assistant.Content[0].RawContent.(string)
				if !ok || !strings.Contains(raw, `"rs_1"`) || !strings.Contains(raw, `"encrypted_content"`) {
					t.Fatalf("reasoning item raw content = %#v, want replayable JSON with id and encrypted content",
						assistant.Content[0].RawContent)
				}
			},
		},
		{
			name: "provider_raw", scenario: goldenScenarioProviderRaw,
			protocol: ir.ProtocolOpenAIResponses, model: "gpt-4o",
			body:       `{"model":"gpt-4o","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"x_allow_fallback":{"mode":"never"}}`,
			parse:      ir.ParseResponses,
			extensions: `{"x_archive_note":{"origin":"golden-corpus"}}`,
			verify: func(t *testing.T, req *ir.InternalRequest) {
				raw, ok := req.Extensions["x_allow_fallback"]
				if !ok {
					t.Fatalf("IR extensions = %#v, want x_allow_fallback", req.Extensions)
				}
				goldenJSONEqual(t, "extensions.x_allow_fallback", raw, json.RawMessage(`{"mode":"never"}`))
			},
		},
		{
			name: "unknown_block", scenario: goldenScenarioUnknownBlock,
			protocol: ir.ProtocolOpenAIResponses, model: "gpt-4o",
			body:  `{"model":"gpt-4o","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"go"},{"type":"custom_future_item","payload":{"note":"future"}}]}]}`,
			parse: ir.ParseResponses,
			verify: func(t *testing.T, req *ir.InternalRequest) {
				message, ok := goldenFindMessage(req, "user")
				if !ok || len(message.Content) != 2 {
					t.Fatalf("user message = %#v, want two content blocks", message)
				}
				block := message.Content[1]
				if block.Type != "custom_future_item" {
					t.Fatalf("unknown block type = %q, want custom_future_item", block.Type)
				}
				raw, ok := block.RawContent.(string)
				if !ok || !strings.Contains(raw, `"custom_future_item"`) || !strings.Contains(raw, `"note"`) {
					t.Fatalf("unknown block raw content = %#v, want replayable JSON", block.RawContent)
				}
			},
		},
		goldenSSECase(ir.ProtocolOpenAIResponses, "gpt-4o",
			`{"model":"gpt-4o","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}],"stream":true}`,
			goldenResponseFixture{
				rawBody:       `{"id":"resp_or_1","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":12,"output_tokens":7,"total_tokens":19}}`,
				clientIR:      `{"role":"assistant","content":[{"type":"text","text":"Hello"}]}`,
				streamSummary: `{"chunk_count":3,"event_types":["response.output_text.delta","response.completed"]}`,
				streamChunks: `[{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"Hel"},` +
					`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"lo"},` +
					`{"type":"response.completed","response":{"id":"resp_or_1","usage":{"input_tokens":12,"output_tokens":7}}}]`,
			},
			func(t *testing.T, envelope *RequestArchiveEnvelope) {
				goldenAssertStreamChunks(t, envelope, 3, `"delta":"Hel"`, `"response.completed"`)
			}),
	}
	return corpus
}

func goldenSSECase(protocol, model, body string, response goldenResponseFixture,
	verifyEnvelope func(t *testing.T, envelope *RequestArchiveEnvelope)) goldenCorpusCase {
	var parse func([]byte) (*ir.InternalRequest, error)
	switch protocol {
	case ir.ProtocolOpenAIChat:
		parse = ir.ParseOpenAI
	case ir.ProtocolAnthropicMessages:
		parse = ir.ParseAnthropic
	case ir.ProtocolGeminiGenerate:
		parse = ir.ParseGemini
	case ir.ProtocolOpenAIResponses:
		parse = ir.ParseResponses
	}
	return goldenCorpusCase{
		name:           "sse_stream_fragments",
		scenario:       goldenScenarioSSE,
		protocol:       protocol,
		model:          model,
		body:           body,
		parse:          parse,
		response:       &response,
		verifyEnvelope: verifyEnvelope,
		verify: func(t *testing.T, req *ir.InternalRequest) {
			if len(req.Messages) != 1 {
				t.Fatalf("streaming request messages = %#v, want single user turn", req.Messages)
			}
		},
	}
}
