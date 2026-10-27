// Package sessionid is the single source of truth for gateway session
// identity resolution (W1.1 / W1.2).
//
// Gate facts (sessionid.go):
//  1. Callers: session_routing.go wrappers (derive/extract*); handler.go:2787-2796;
//     responses.go:449; messages.go:459; goalrun_handler.go:122; main_pipeline ~779.
//  2. No prior internal/sessionid package (Glob empty); logic lived in session_routing.go.
//  3. No production data I/O; Provenance is an in-memory enum for logs/events.
//  4. User: "新建 internal/sessionid… Resolve… 优先级 body>X-Gw>legacy/native>assignment>provisional…"
//
// # Priority (behaviour locked — A6)
//
//  1. body session_id / aliases     (ProvenanceBody)
//  2. X-Gw-Session-Id               (ProvenanceHeaderGw)
//  3. legacy / native header table  (ProvenanceHeaderNative)
//  4. assignment                    (caller — ProvenanceAssignment)
//  5. provisional                   (caller — ProvenanceProvisional; this-round only)
//
// Resolve covers steps 1–3 and applies the bare-UUID → gw_<id> mapping.
// Steps 4–5 stay in the streaming handlers (CreateV2 / recent-session reuse /
// early-failure mint). Do NOT import magpie's "never invent an id" rule to
// remove provisional/assignment — those are intentional gateway semantics.
//
// # Not this package
//
// domains/memory/task_id.go is TaskID, not session identity. Session body
// persistence / admin replay belong to package 02.
package sessionid

import (
	"net/http"
	"strings"
)

// Provenance names where a resolved session id came from. Callers that apply
// assignment or provisional must set those values themselves for logs/events.
type Provenance string

const (
	ProvenanceNone         Provenance = ""
	ProvenanceBody         Provenance = "body"
	ProvenanceHeaderGw     Provenance = "header_gw"
	ProvenanceHeaderNative Provenance = "header_native"
	ProvenanceAssignment   Provenance = "assignment"
	ProvenanceProvisional  Provenance = "provisional"
)

// DeriveGatewayID maps a non-gateway client session identity onto the
// canonical gateway namespace. Clients in the wild (e.g. ZCode's per-session
// bare-UUID x-session-id) send a STABLE id with no gw_/gt_/gs_ prefix; leaving
// it unmapped caused CreateV2 to mint a fresh gw_<uuid> per request. Deriving
// "gw_" + <client id> keeps the gateway namespace canonical while making the
// mapping deterministic. Input must already be sanitized.
func DeriveGatewayID(sessionID string) string {
	if sessionID == "" ||
		strings.HasPrefix(sessionID, "gw_") ||
		strings.HasPrefix(sessionID, "gt_") ||
		strings.HasPrefix(sessionID, "gs_") {
		return sessionID
	}
	return "gw_" + sessionID
}

// Resolve returns the derived gateway session id and its provenance from
// body + headers only. Empty id with ProvenanceNone means the caller should
// fall through to assignment, then provisional.
func Resolve(headers http.Header, body []byte) (string, Provenance) {
	if id := FromBody(body); id != "" {
		return DeriveGatewayID(id), ProvenanceBody
	}
	if id, prov := FromHeaders(headers); id != "" {
		return DeriveGatewayID(id), prov
	}
	return "", ProvenanceNone
}

// ResolveRequest is a convenience over Resolve for *http.Request callers.
func ResolveRequest(r *http.Request, body []byte) (string, Provenance) {
	if r == nil {
		return Resolve(nil, body)
	}
	return Resolve(r.Header, body)
}
