// Package streaming — preferred_credential.go
//
// 2026-08-23: request-side extension that lets an admin / developer force a
// particular credential_id on the next chat request. This is invaluable for
// QA: e.g. reproducing a customer-reported failure against credential "hzx-2"
// without manually reshuffling the routing scores. The extension is read by
// the v2 pipeline preflight wrapper (cmd/gateway/main_pipeline.go) and
// propagated into env.Metadata["preferred_credential"], which the existing
// StickyRouter (domains/routing/sticky_router.go:34) already consumes.
//
// Three ways to set it, in priority order (header > JSON body > nothing):
//
//  1. HTTP header   X-LLMGW-Preferred-Credential: 42
//  2. JSON body     { "metadata": { "preferred_credential": "42" } }   (OpenAI
//                   Chat Completions / Responses API — both tolerate unknown
//                   fields in metadata)
//  3. JSON body     { "metadata": { "preferred_credential": "42" } }   (Anthropic
//                   Messages — typed *anthropicMeta; extended below)
//
// SECURITY: This is a routing override. If any client could set it, every
// caller could blackhole any credential by always selecting the worst one,
// or evade health-driven cool-down. The header and body field are therefore
// only honoured when the request's Authorization Bearer token matches the
// static admin token (cfg.AdminAPIKey, env LLM_GATEWAY_ADMIN_API_KEY). The
// comparison uses crypto/subtle.ConstantTimeCompare so an unauthenticated
// probe cannot extract the token by timing.
//
// In production we expect the admin token to live behind a private network
// path (e.g. an internal SSH tunnel), so the wire-level auth check is
// defence-in-depth, not the primary gate.
package streaming

import (
	"crypto/subtle"
	"encoding/json"
	"strconv"
	"strings"
)

// PreferredCredentialHeader is the HTTP header that overrides routing for
// the next request. The header is consumed once per request by the v2
// pipeline preflight wrapper.
const PreferredCredentialHeader = "X-LLMGW-Preferred-Credential"

// ExtractPreferredCredential inspects the request for the override and
// returns the credential id as a string (kept as a string because the
// routing layer's metadata map uses any->any and the StickyRouter does a
// type assertion to string).
//
// Behaviour:
//   - Header read: any non-empty value is captured; validity (numeric id)
//     is enforced separately by the StickyRouter (the value just needs to
//     match an existing candidate).
//   - Body read: the metadata.preferred_credential field is parsed from the
//     request body. The body is already buffered upstream of this call (the
//     v2 pipeline reads it once and exposes it via rawBody), so re-reading
//     is safe and offline.
//   - Admin gate: if any of (header, body) yields a non-empty value, the
//     admin bearer token must match adminAPIKey. Mismatch returns "" and
//     the caller should log the rejection.
//
// The function never errors — the worst case is "no override", which the
// caller treats as the existing default routing path.
func ExtractPreferredCredential(headerValue string, body []byte, adminBearer, adminAPIKey string) string {
	want := strings.TrimSpace(headerValue)
	if want == "" && len(body) > 0 {
		want = preferredCredentialFromBody(body)
	}
	if want == "" {
		return ""
	}
	if !adminAuthorized(adminBearer, adminAPIKey) {
		return ""
	}
	want = strings.TrimPrefix(want, "#")
	if _, err := strconv.ParseInt(want, 10, 64); err != nil {
		return ""
	}
	return want
}

// preferredCredentialFromBody returns the value of
// `metadata.preferred_credential` (string) from a chat / anthropic /
// responses request body. It does NOT enforce any schema beyond that:
// models, messages, stream, tools, etc. are ignored.
//
// The metadata field is OpenAI's official extension slot ("developer-defined
// string key-value pairs") and Anthropic exposes the same key in its
// messages.metadata; Responses API passes unknown fields through to its
// `Extra` bucket where we read them via `metadata.preferred_credential`.
func preferredCredentialFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var envelope struct {
		Metadata json.RawMessage `json:"metadata"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || len(envelope.Metadata) == 0 {
		return ""
	}
	var md map[string]any
	if err := json.Unmarshal(envelope.Metadata, &md); err != nil {
		return ""
	}
	v, ok := md["preferred_credential"]
	if !ok {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		// Tolerate numeric literals in JSON.
		return strconv.FormatInt(int64(t), 10)
	}
	return ""
}

// adminAuthorized returns true iff the caller presented a bearer token
// that matches the static admin token. The comparison is constant-time
// to avoid leaking the admin token through timing differences.
func adminAuthorized(bearer, adminAPIKey string) bool {
	if adminAPIKey == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(bearer), []byte(adminAPIKey)) == 1
}