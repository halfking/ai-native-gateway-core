// Package probeutil provides helpers shared by all backend probe paths
// (model_probe.go, credential_probe_v2.go, providers.go diagnoseProvider).
//
// Currently the SSoT for "the provider requires an endpoint ID rather
// than a raw model name".  Used by:
//
//   - bg/model_probe.probeModel        — model-level probe (404 → skip)
//   - bg/credential_probe_v2.miniChat  — credential-level chat probe
//     (404 → classify as endpoint_id_required, do NOT mark unreachable)
//   - admin/providers.doChatProbe      — diagnose UI (404 → friendly hint)
//
// Backstory: 火山方舟 (Volcano Ark) and similar providers expose
// vendor-supplied models (minimax-m3, glm-5.1, …) only through deployment
// endpoint IDs (ep-XXXXXXXX).  Calling them by raw model name returns 404
// with error_code "InvalidEndpointOrModel.NotFound".  Naively interpreting
// that 404 as "model gone / provider gone" drives false
// broken_confirmed / unreachable states that block routing.
package probeutil

import "strings"

// EndpointIDRequiredErrCode is the canonical error_code that all probe
// paths should emit when a 404 indicates the model needs an endpoint ID.
const EndpointIDRequiredErrCode = "endpoint_id_required"

// IsEndpointIDRequiredError reports whether a 404 response body indicates
// that the provider requires a deployment endpoint ID rather than a plain
// model name.  Currently recognises:
//
//   - Volcano Ark (火山方舟): explicit error_code "InvalidEndpointOrModel"
//   - Generic phrasing: body mentions "endpoint" + ("not found" /
//     "does not exist" / "no access" / "do not have access")
//
// The function is intentionally permissive: false negatives only cause a
// transient extra failure that the consensus logic will eventually forgive;
// false positives silently turn a real failure into a configuration
// request, which the operator will notice immediately.
func IsEndpointIDRequiredError(body string) bool {
	if body == "" {
		return false
	}
	// Volcano Ark explicit code (case-sensitive: the API returns this verbatim)
	if strings.Contains(body, "InvalidEndpointOrModel") {
		return true
	}
	lbody := strings.ToLower(body)
	// Must mention "endpoint" AND a not-found phrase.  Both clauses required
	// to avoid misclassifying generic 404s like "model does not exist".
	hasEndpoint := strings.Contains(lbody, "endpoint")
	if !hasEndpoint {
		return false
	}
	switch {
	case strings.Contains(lbody, "not found"),
		strings.Contains(lbody, "does not exist"),
		strings.Contains(lbody, "no access"),
		strings.Contains(lbody, "do not have access"),
		strings.Contains(lbody, "do not have permission"):
		return true
	}
	return false
}