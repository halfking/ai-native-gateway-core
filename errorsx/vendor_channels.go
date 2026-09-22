package errorsx

import "strings"

// Vendor error-channel single table (Wave4-D3, 2026-09-22).
//
// Two OpenAI-compatible vendor error channels used to be maintained as
// separate hand-written tables in three files — internal/ir/response.go
// (non-stream GLM finish_reason switch), streaming/anthropic_stream.go
// (streaming GLM finish_reason switch), streaming/responses_bridge.go
// (openaiFinishReasonIsError bool set) — plus the MiniMax base_resp
// status-code table in internal/vendorstrip. They live here so a new vendor
// value or misspelling lands in exactly one place.
//
// Two tiers with different parse consequences:
//   - Vendor FAILURE channel: finish_reason is not a completion state at all
//     (Zhipu GLM abuses it as an error signal); the body carries no usable
//     completion and parsing must fail.
//   - Abnormal completion: termination was abnormal (refusal, content filter,
//     opaque error) but the body remains parseable — presentation layers map
//     it to incomplete/content_filter; only "render as clean success" is
//     forbidden.

// finishReasonFailureKinds are the canonical GLM error-channel values
// (2026-08-28 spec audit, commit ae1ecaedf lineage).
var finishReasonFailureKinds = map[string]ErrorKind{
	"network_error":                 KindNetwork,
	"sensitive":                     KindContentFilter,
	"model_context_window_exceeded": KindContextLength,
}

// finishReasonAbnormalCompletionKinds are abnormal-but-parseable terminations.
var finishReasonAbnormalCompletionKinds = map[string]ErrorKind{
	"content_filter":          KindContentFilter,
	"refusal":                 KindContentFilter,
	"error":                   KindUpstreamDown,
	"context_length_exceeded": KindContextLength,
}

// finishReasonMisspellings are observed upstream misspellings of failure
// values (production 2026-09: "exceeded"-family emitted as "exeated").
// Exact-matched after the canonical table misses.
var finishReasonMisspellings = map[string]ErrorKind{
	"exeated":      KindContextLength,
	"exeeded":      KindContextLength,
	"exceded":      KindContextLength,
	"sensetive":    KindContentFilter,
	"network_eror": KindNetwork,
}

// finishReasonFailureStems form the containment fallback for prefixed
// composites of misspelled tails ("model_context_window_exeated"). Stems are
// chosen so canonical non-failure values never match: in particular
// "context_length_exceeded" contains none of these (the bare "exceeded" stem
// would, and is therefore deliberately absent).
var finishReasonFailureStems = []struct {
	stem string
	kind ErrorKind
}{
	{"network_err", KindNetwork},
	{"sensitiv", KindContentFilter},
	{"sensetiv", KindContentFilter},
	{"context_window", KindContextLength},
	{"exeated", KindContextLength},
	{"exeeded", KindContextLength},
	{"exceded", KindContextLength},
}

// FinishReasonVendorFailureKind reports (kind, true) when fr is a vendor
// error-channel finish_reason — a failure masquerading as a completion state.
// Misspelling tolerance applies: upstreams occasionally emit typo'd variants,
// and failing closed (treating the typo as the failure it encodes) is safer
// than rendering a bogus success.
func FinishReasonVendorFailureKind(fr string) (ErrorKind, bool) {
	normalized := strings.ToLower(strings.TrimSpace(fr))
	if kind, ok := finishReasonFailureKinds[normalized]; ok {
		return kind, true
	}
	if kind, ok := finishReasonMisspellings[normalized]; ok {
		return kind, true
	}
	for _, s := range finishReasonFailureStems {
		if strings.Contains(normalized, s.stem) {
			return s.kind, true
		}
	}
	return "", false
}

// FinishReasonAbnormalKind reports (kind, true) when fr signals an abnormal
// termination: the vendor failure channel PLUS abnormal-but-parseable
// completion states. Callers use this to refuse rendering a clean success
// terminal; body parsing may still proceed. Misspelling tolerance covers the
// failure-channel values only.
func FinishReasonAbnormalKind(fr string) (ErrorKind, bool) {
	normalized := strings.ToLower(strings.TrimSpace(fr))
	if kind, ok := finishReasonAbnormalCompletionKinds[normalized]; ok {
		return kind, true
	}
	return FinishReasonVendorFailureKind(normalized)
}

// MiniMaxBaseRespStatusCodeKind preserves the established MiniMax
// base_resp.status_code → gateway-kind map (moved verbatim from
// internal/vendorstrip.ClassifyMiniMaxStatusCode; the JSON parsing of the
// base_resp envelope stays in vendorstrip).
func MiniMaxBaseRespStatusCodeKind(code int) ErrorKind {
	switch code {
	case 0:
		return ""
	case 1002:
		return KindRateLimit
	case 1004:
		return KindAuth
	case 1008:
		return KindQuota
	case 1027:
		return KindContentFilter
	case 1039:
		return KindContextLength
	case 1001:
		return KindTimeout
	case 2013:
		return KindClientBug
	case 1000, 1013:
		return KindUpstreamDown
	default:
		return KindUpstreamDown
	}
}
