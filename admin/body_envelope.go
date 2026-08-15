package admin

// CO-5 (M2) summary mode read-side adapter. When sessions_v2.enabled=true
// and sessions_v2.request_bodies_full=false, the telemetry write path
// (domains/hooks/observability/telemetry/body_summary.go) persists a digest
// envelope into request_logs_bodies_hot instead of the full body:
//
//	{"_gw_body_summary":{"mode":"digest","bytes":N,"sha256":"…","head":"…","head_truncated":bool}}
//
// Admin readers that LEFT JOIN request_logs_bodies_with_current_month used
// to receive the raw envelope JSON and degrade (doc 23 §5 P2-C1). This file
// carries the admin-side detection helpers so those readers can consume the
// retained head instead. The no-payload sentinels ("", "null", "{}") never
// carry the envelope key, so they flow through detection unchanged — the
// CO-5 passthrough semantics are preserved by construction.

import (
	"encoding/json"
	"fmt"
	"strings"
)

// bodyEnvelope is the admin-side view of the _gw_body_summary digest
// envelope. It mirrors telemetry.gwBodySummary (kept as a local mirror so
// the admin package does not depend on the telemetry write-path internals).
type bodyEnvelope struct {
	Mode          string `json:"mode"`
	Bytes         int    `json:"bytes"`
	SHA256        string `json:"sha256"`
	Head          string `json:"head"`
	HeadTruncated bool   `json:"head_truncated"`
}

// bodyEnvelopeOuter is the persisted envelope document. Summary is a
// pointer so a present-but-null key (e.g. {"_gw_body_summary":null}) is
// distinguishable from a real envelope and treated as a non-envelope.
type bodyEnvelopeOuter struct {
	Summary *bodyEnvelope `json:"_gw_body_summary"`
}

// detectBodyEnvelope reports whether raw is a digest envelope written by
// summary mode, returning the parsed envelope when it is. Anything that is
// not a JSON object carrying a non-null "_gw_body_summary" key — including
// the ""/"null"/"{}" sentinels, truncated JSON and plain request/response
// bodies — returns false so callers keep their current behaviour.
func detectBodyEnvelope(raw string) (bodyEnvelope, bool) {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "{") {
		return bodyEnvelope{}, false
	}
	var outer bodyEnvelopeOuter
	if err := json.Unmarshal([]byte(trimmed), &outer); err != nil || outer.Summary == nil {
		return bodyEnvelope{}, false
	}
	return *outer.Summary, true
}

// unwrapBody parses body once and returns both the envelope (nil when the
// body is not an envelope) and the document JSON extraction should run
// against, so callers never pay for double detection. Semantics are the
// composition of envelopeOf and headForExtraction: nil body yields
// (nil, nil), a non-envelope body yields (nil, body) untouched, an envelope
// yields the parsed envelope plus its head — or nil head when the head was
// truncated mid-document (invalid JSON; callers fall back to previews).
func unwrapBody(body *string) (*bodyEnvelope, *string) {
	if body == nil {
		return nil, nil
	}
	env, ok := detectBodyEnvelope(*body)
	if !ok {
		return nil, body
	}
	if !json.Valid([]byte(env.Head)) {
		return &env, nil
	}
	head := env.Head
	return &env, &head
}

// envelopeOf is the *string variant of detectBodyEnvelope; nil stays nil.
func envelopeOf(body *string) *bodyEnvelope {
	env, _ := unwrapBody(body)
	return env
}

// headForExtraction returns the body document JSON extraction should run
// against. For a non-envelope body the pointer is returned unchanged (the
// flag-off / request_bodies_full path is byte-for-byte identical). For an
// envelope the retained head is substituted; a head that was truncated
// mid-document is not valid JSON, so it is dropped (nil) and callers fall
// back to the stored previews instead of feeding partial JSON into the
// extractor (which would trip the "unparseable body" data-loss warning).
func headForExtraction(body *string) *string {
	_, head := unwrapBody(body)
	return head
}

// displayNote is the visible marker appended to content derived from an
// envelope so admin UIs can tell a downsampled body from a full one.
func (e *bodyEnvelope) displayNote() string {
	if e == nil {
		return ""
	}
	if e.HeadTruncated {
		return fmt.Sprintf("[已摘要化: 原始 %d bytes, head 已截断]", e.Bytes)
	}
	return fmt.Sprintf("[已摘要化: 原始 %d bytes]", e.Bytes)
}
