package requestfact

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

var (
	ErrUnsupportedVersion = errors.New("requestfact: unsupported version")
	ErrIntegrityMismatch  = errors.New("requestfact: payload hash mismatch")
)

// Encode validates envelope metadata and emits the archive JSON document.
// PayloadSHA256 covers the canonical V1 payload fields, excluding envelope
// metadata, the hash field, and additive fields unknown to this codec version.
func Encode(envelope RequestArchiveEnvelope) ([]byte, error) {
	if err := envelope.validateMetadata(); err != nil {
		return nil, err
	}
	payload, err := marshalPayload(envelope.Payload)
	if err != nil {
		return nil, err
	}
	envelope.PayloadSHA256 = payloadSHA256(payload)
	return json.Marshal(envelope)
}

// Decode accepts additive JSON fields but rejects unsupported required versions,
// malformed core content, and a mismatching payload hash.
func Decode(body []byte) (*RequestArchiveEnvelope, error) {
	var envelope RequestArchiveEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("requestfact: decode envelope: %w", err)
	}
	if err := envelope.Validate(); err != nil {
		return nil, err
	}
	return &envelope, nil
}

// Validate checks the envelope version, archive state, payload, supplied body
// digests, and the required payload digest without persisting anything.
func (e RequestArchiveEnvelope) Validate() error {
	if err := e.validateMetadata(); err != nil {
		return err
	}
	payload, err := marshalPayload(e.Payload)
	if err != nil {
		return err
	}
	if e.PayloadSHA256 == "" || !constantTimeHexEqual(e.PayloadSHA256, payloadSHA256(payload)) {
		return ErrIntegrityMismatch
	}
	return nil
}

func (e RequestArchiveEnvelope) validateMetadata() error {
	if e.EnvelopeVersion != EnvelopeVersionV1 || e.PayloadVersion != PayloadVersionV1 ||
		e.CodecVersion != CodecVersionV1 || e.ProjectionEventVersion != ProjectionEventVersionV1 {
		return fmt.Errorf("%w: envelope=%d payload=%d codec=%d projection_event=%d",
			ErrUnsupportedVersion, e.EnvelopeVersion, e.PayloadVersion, e.CodecVersion, e.ProjectionEventVersion)
	}
	if e.CapturedAt.IsZero() {
		return errors.New("requestfact: captured_at required")
	}
	if e.Archive.Attempts < 0 {
		return errors.New("requestfact: archive attempts cannot be negative")
	}
	if !validArchiveState(e.Archive.State) {
		return fmt.Errorf("requestfact: unsupported archive state %q", e.Archive.State)
	}
	if err := e.Payload.Validate(); err != nil {
		return err
	}
	return e.Payload.VerifyBodyHashes()
}

func (f CanonicalRequestFact) Validate() error {
	if f.Identity.TenantID == "" || f.Identity.RequestID == "" {
		return errors.New("requestfact: tenant_id and request_id required")
	}
	if f.Lifecycle.Status == "" || f.Lifecycle.CreatedAt.IsZero() || f.Lifecycle.CompletedAt.IsZero() {
		return errors.New("requestfact: terminal lifecycle status, created_at, and completed_at required")
	}
	if f.Lifecycle.CompletedAt.Before(f.Lifecycle.CreatedAt) {
		return errors.New("requestfact: completed_at precedes created_at")
	}
	if !f.Lifecycle.StartedAt.IsZero() && f.Lifecycle.StartedAt.Before(f.Lifecycle.CreatedAt) {
		return errors.New("requestfact: started_at precedes created_at")
	}
	if !f.Lifecycle.StartedAt.IsZero() && f.Lifecycle.CompletedAt.Before(f.Lifecycle.StartedAt) {
		return errors.New("requestfact: completed_at precedes started_at")
	}
	if f.Routing.ClientProtocol == "" || f.Routing.ClientModel == "" {
		return errors.New("requestfact: client protocol and model required")
	}
	if err := validateRequiredDocument("request.raw_body", f.Request.RawBody); err != nil {
		return err
	}
	if err := validateRequiredDocument("request.canonical_ir", f.Request.CanonicalIR); err != nil {
		return err
	}
	for _, document := range []struct {
		name string
		body json.RawMessage
	}{
		{"request.extensions", f.Request.Extensions},
		{"upstream.raw_body", f.Upstream.RawBody},
		{"upstream.canonical_ir", f.Upstream.CanonicalIR},
		{"upstream.extensions", f.Upstream.Extensions},
		{"response.raw_body", f.Response.RawBody},
		{"response.canonical_ir", f.Response.CanonicalIR},
		{"response.client_ir", f.Response.ClientIR},
		{"response.stream_summary", f.Response.StreamSummary},
		{"response.stream_chunks", f.Response.StreamChunks},
		{"response.extensions", f.Response.Extensions},
		{"attachments", f.Attachments},
		{"extensions", f.Extensions},
	} {
		if err := validateOptionalDocument(document.name, document.body); err != nil {
			return err
		}
	}
	for _, warning := range f.Warnings {
		if warning.Code == "" || warning.Field == "" {
			return errors.New("requestfact: warning code and field required")
		}
	}
	return validateIntegrity(f.Integrity)
}

// BodySHA256 returns the lowercase SHA-256 digest for a canonical JSON body.
// Callers pass only valid JSON documents; Encode validates documents before it
// verifies supplied digests.
func BodySHA256(body []byte) string {
	canonical, err := canonicalJSON(body)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// VerifyBodyHashes checks each supplied body digest against its corresponding
// raw document. Empty optional documents may have no digest; a supplied digest
// for an empty document is rejected.
func (f CanonicalRequestFact) VerifyBodyHashes() error {
	checks := []struct {
		name string
		body json.RawMessage
		hash string
	}{
		{"request_body_sha256", f.Request.RawBody, f.Integrity.RequestBodySHA256},
		{"outbound_body_sha256", f.Upstream.RawBody, f.Integrity.OutboundBodySHA256},
		{"response_body_sha256", f.Response.RawBody, f.Integrity.ResponseBodySHA256},
	}
	for _, check := range checks {
		if check.hash == "" {
			continue
		}
		if len(check.body) == 0 {
			return fmt.Errorf("%w: %s supplied without body", ErrIntegrityMismatch, check.name)
		}
		if subtle.ConstantTimeCompare([]byte(check.hash), []byte(BodySHA256(check.body))) != 1 {
			return fmt.Errorf("%w: %s mismatch", ErrIntegrityMismatch, check.name)
		}
	}
	return nil
}

func marshalPayload(payload CanonicalRequestFact) ([]byte, error) {
	if err := payload.Validate(); err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("requestfact: encode payload: %w", err)
	}
	return canonicalJSON(body)
}

func canonicalJSON(body []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("requestfact: canonicalize JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("requestfact: canonicalize JSON: multiple values")
		}
		return nil, fmt.Errorf("requestfact: canonicalize JSON: %w", err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("requestfact: marshal canonical JSON: %w", err)
	}
	return canonical, nil
}

func validateRequiredDocument(name string, body json.RawMessage) error {
	if len(body) == 0 || !json.Valid(body) || isJSONNull(body) {
		return fmt.Errorf("requestfact: %s must be non-null valid JSON", name)
	}
	return nil
}

func validateOptionalDocument(name string, body json.RawMessage) error {
	if len(body) == 0 {
		return nil
	}
	if !json.Valid(body) || isJSONNull(body) {
		return fmt.Errorf("requestfact: %s must be non-null valid JSON when present", name)
	}
	return nil
}

func isJSONNull(body json.RawMessage) bool {
	return strings.TrimSpace(string(body)) == "null"
}

func validateIntegrity(integrity Integrity) error {
	for _, hash := range []string{integrity.RequestBodySHA256, integrity.OutboundBodySHA256, integrity.ResponseBodySHA256} {
		if hash == "" {
			continue
		}
		if len(hash) != sha256.Size*2 {
			return errors.New("requestfact: body hash must be SHA-256 hex")
		}
		if _, err := hex.DecodeString(hash); err != nil {
			return errors.New("requestfact: body hash must be SHA-256 hex")
		}
	}
	return nil
}

func validArchiveState(state ArchiveState) bool {
	return state == ArchiveStateActive || state == ArchiveStateTerminalPending || state == ArchiveStateCleanupPending
}

func payloadSHA256(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func constantTimeHexEqual(left, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

// NewEnvelope constructs the V1 metadata required by the archive codec.
func NewEnvelope(capturedAt time.Time, state ArchiveState, fact CanonicalRequestFact) RequestArchiveEnvelope {
	return RequestArchiveEnvelope{
		EnvelopeVersion:        EnvelopeVersionV1,
		PayloadVersion:         PayloadVersionV1,
		CodecVersion:           CodecVersionV1,
		ProjectionEventVersion: ProjectionEventVersionV1,
		CapturedAt:             capturedAt,
		Archive:                ArchiveMetadata{State: state},
		Payload:                fact,
	}
}
