// Package endpointselect implements the multi-protocol endpoint selection
// algorithm that drives the streaming dispatcher's outbound candidate
// decision. The selector is the SSoT for "which (baseurl, vendor_native)
// pair does this request go to?" — the rest of the dispatch pipeline
// reads a Decision and applies it.
//
// # Why a dedicated package
//
// Before r0924 the dispatcher switched on the parsed protocol against the
// candidate's primary Protocol field directly. This forced a "one base
// URL, one wire format" assumption that breaks the supplier-protocol-
// optimization roadmap: clients that speak Ollama native (/api/chat +
// options + keep_alive) cannot land on a supplier that fronts its Ollama
// deployment behind OpenAI-compatible chat (/v1/chat/completions) — the
// dispatcher would silently downgrade their Ollama-private fields into
// the OpenAI serializer's parameter guard and Ollama would reject the
// request.
//
// The fix lives in §2.5 / §3.5 of docs/供应商协议优化-实施规划.md:
// endpoints now carry an explicit `protocol` + `vendor_native` tuple, and
// Select() finds the tuple that best matches the inbound (wantProtocol,
// wantFamily). The protocol-match rule is INTENTIONALLY higher priority
// than the family match because protocols and the serializer/parameter
// guard are 1:1 — same protocol means same serializer, so picking the
// same-protocol endpoint over a different-vendor_native alternative
// preserves parameter fidelity even when the family attribution is
// imperfect.
//
// # Decision stages
//
// Stage 1A — protocol == wantProtocol AND vendor_native == wantFamily
//
//	→ Passthrough=true (byte-faithful wire match).
//
// Stage 1B — protocol == wantProtocol AND wantFamily != "" AND
//
//	         vendor_native != wantFamily
//	→ Passthrough=false (same protocol, but the upstream is not actually
//	  the family the model belongs to; fall back to IR bridging).
//
// Stage 1C — protocol == wantProtocol AND wantFamily == ""
//
//	→ Passthrough=false (same protocol, family unknown — IR bridging is
//	  the safe default; the model might be reclassified later).
//
// Stage 2 — vendor_native == wantFamily AND protocol != wantProtocol
//
//	→ Passthrough=false (family match is a weaker signal than protocol
//	  match; IR bridging preserves the contract).
//
// Stage 3 — vendor_native == wantFamily AND wantFamily == ""
//
//	→ identical to Stage 1C fallback; classified for symmetry.
//
// Stage 4 — fallback to the candidate's primary (BaseURL, Protocol).
//
//	→ Passthrough=false when wantProtocol == candidate.Protocol,
//	  else Passthrough=false (still IR bridging but the URL/format
//	  pair is the historical default).
//
// Feature flags (off by default until P4 gradual rollout):
//   - FF_ENDPOINT_SELECTOR: switch the dispatcher to use Select() instead
//     of the legacy direct-on-Protocol switch.
//   - FF_OLLAMA_NATIVE: enable the new ollama-native case in dispatch.
//
// RESERVED(r0924 supplier-protocol-optimization §3.5): the Decision
// struct fields are wire-stable. Existing dispatchers (8a0bd5c95 era)
// already log decision.MatchRule / decision.Protocol / decision.VendorNative
// as span attributes; the new type plugs into those loggers without
// requiring a logging refresh.
package endpointselect

import (
	"sort"
)

// Protocol is the canonical outbound protocol string. We deliberately
// re-declare the enum here (rather than importing catalog.ProtocolXxx)
// so the selector stays importable from internal/* packages without
// dragging the catalog dependency graph in.
//
// The constants MUST stay in lock-step with provider/catalog/protocol.go.
type Protocol string

const (
	ProtocolOpenAICompletions Protocol = "openai-completions"
	ProtocolOpenAIResponses   Protocol = "openai-responses"
	ProtocolAnthropicMessages Protocol = "anthropic-messages"
	ProtocolGeminiGenerate    Protocol = "gemini-generate"
	ProtocolOllamaNative      Protocol = "ollama-native"
)

// irToCatalogProtocol normalizes ir-namespace protocol strings
// (internal/ir/types.go: openai-chat / ollama-chat / ...) into the
// catalog namespace (the Protocol constants above, i.e. the
// provider_catalog.protocol CHECK five-value enum). Catalog-namespace
// values pass through unchanged, so the mapping is idempotent.
//
// Background (r0924 fix-a task 5): Select()'s wantProtocol comes from
// inbound protocol detection (ir namespace, e.g. "ollama-chat") while
// EndpointLite.Protocol is catalog namespace (e.g. "ollama-native").
// The two spaces were compared as raw strings and never equal, so every
// ir-protocol request silently fell through to Stage 4. This table
// mirrors the dual-enum precedent in internal/paramreg/dialect.go
// (protocolToDialect); values that are identical in both spaces
// ("anthropic-messages", ...) still go through the map so there is a
// single normalization entry point.
//
// CONTRACT (lockstep): every ir protocol enum value MUST appear in this
// table and MUST map to one of the five catalog values — pinned
// table-driven by the lockstep test in selector_test.go. Adding a new ir
// protocol without extending this table silently pins it to Stage 4.
var irToCatalogProtocol = map[string]string{
	// ir → catalog
	"openai-chat": "openai-completions",
	"ollama-chat": "ollama-native",
	// catalog values pass through (including the three shared strings)
	"openai-completions": "openai-completions",
	"openai-responses":   "openai-responses",
	"anthropic-messages": "anthropic-messages",
	"gemini-generate":    "gemini-generate",
	"ollama-native":      "ollama-native",
}

// NormalizeWantProtocol normalizes the inbound wantProtocol from the ir
// namespace into the catalog namespace; unknown values pass through
// unchanged (preserves legacy behavior during gradual rollout).
func NormalizeWantProtocol(p Protocol) Protocol {
	if c, ok := irToCatalogProtocol[string(p)]; ok {
		return Protocol(c)
	}
	return p
}

// MatchRule names which stage produced the decision. Stable string values;
// they appear in span attributes and metrics labels.
const (
	MatchStage1A = "stage1a" // protocol + family match, passthrough
	MatchStage1B = "stage1b" // protocol match, family mismatch (known)
	MatchStage1C = "stage1c" // protocol match, family unknown
	MatchStage2  = "stage2"  // family match, protocol mismatch
	MatchStage3  = "stage3"  // family match, family empty (alias of 1C)
	MatchStage4  = "stage4"  // legacy fallback to primary endpoint
)

// Decision is the result of a Select() call. EndpointID is the row id of
// the chosen endpoint (0 = legacy fallback to cand.BaseURL /
// cand.Protocol). Passthrough=true means the dispatcher should forward the
// raw client bytes to the upstream without IR-bridging (reserved for
// future P5; today Passthrough is always false because no executor yet
// implements byte-level passthrough).
type Decision struct {
	Protocol     Protocol
	BaseURL      string
	EndpointID   int64 // 0 = primary fallback
	VendorNative string
	Passthrough  bool
	MatchRule    string
	Reason       string
}

// EndpointLite is the per-endpoint row projection used by Select(). The
// candidate owns a slice of these (NativeEndpoints). The dispatcher does
// not need to materialize a SQL row — any provider row that exposes the
// protocol/base_url/vendor_native/weight quadruple fits.
type EndpointLite struct {
	ID           int64
	Protocol     string // catalog enum: openai-completions / ollama-native / ...
	BaseURL      string // upstream base URL (may equal cand.BaseURL for primary)
	IsPrimary    bool
	VendorNative string // free-string, validated against discovery family SSOT
	Enabled      bool   // false = disabled by operator, skip
	Weight       int    // traffic-share tiebreaker: higher weight = higher priority (provider semantics, r0924 fix-a task 6b)
	HealthStatus string // informational; "unknown" skips the gate
}

// CandidateLite is the SSOT shape Select() needs. provider.Candidate fits
// the contract via an adapter (providerAdapter) defined in the same
// package — keeping the dependency surface narrow lets Select() be unit-
// tested without dragging the full provider client.
type CandidateLite struct {
	ProviderID      int
	CredentialID    int
	BaseURL         string // primary base URL (legacy / Stage 4 fallback)
	Protocol        string // primary protocol (legacy / Stage 4 fallback)
	VendorNative    string // family the candidate's primary endpoint advertises
	NativeEndpoints []EndpointLite
}

// Select runs the four-stage decision algorithm described in
// docs/供应商协议优化-实施规划.md §3.5.
//
// Parameters:
//   - cand: the routing candidate (provider + native endpoints).
//   - wantProtocol: the inbound protocol as recognized by DetectProtocol
//     (or the URL-hint fallback).
//   - wantFamily: the model's canonical family (from
//     discovery.InferFamily(stdName)); may be empty when unknown.
//
// Behavior:
//   - Endpoints with Enabled=false are never picked.
//   - Disabled endpoints stay in the candidate (so the caller can log
//     them) but are skipped during selection.
//   - Ties on MatchRule are broken by EndpointLite.Weight descending
//     (higher weight = higher priority), matching the provider-side
//     weight semantics (provider/client.go capacity-weighted LB,
//     executors/router.go weighted first-choice promotion): weight is a
//     traffic share, so the bigger share wins the tie.
//   - The primary endpoint (IsPrimary=true) is preferred within a stage
//     when two endpoints match.
//
// Returns MatchStage4 with EndpointID=0 when no stage 1-3 endpoint
// matches — this is the legacy-compatible fallback that preserves the
// pre-r0924 dispatch behavior byte-for-byte.
func Select(cand CandidateLite, wantProtocol Protocol, wantFamily string) Decision {
	// r0924 fix-a task 5: wantProtocol arrives in the ir namespace
	// ("openai-chat"/"ollama-chat"/...) while endpoint rows carry catalog
	// namespace values. Normalize once at the entry so Stage 1-2 compare
	// like-for-like instead of always missing into Stage 4.
	wantProtocol = NormalizeWantProtocol(wantProtocol)

	// Stage 1 — protocol match (always wins over family match).
	// Sort endpoints by (isPrimary desc, weight desc, id asc) to keep
	// ordering deterministic across runs; tie-breaking only affects log
	// emission because Stage 1A-1C share the same endpoint row when only
	// one matches.
	primary := pickBest(cand.NativeEndpoints, wantProtocol, wantFamily)
	if primary != nil {
		// Subdivide Stage 1 by vendor_native match quality.
		switch {
		case wantFamily == "":
			return stage1c(cand, primary)
		case primary.VendorNative == wantFamily:
			return stage1a(cand, primary)
		default:
			return stage1b(cand, primary, wantFamily)
		}
	}

	// Stage 2 — family match but protocol mismatch.
	if wantFamily != "" {
		famHit := pickFamilyHit(cand.NativeEndpoints, wantFamily, wantProtocol)
		if famHit != nil && len(famHit.Endpoints) > 0 {
			// r0924 fix-a task 6a: the family group used to be consumed in
			// slice order (Endpoints[0] of the DB-returned order). Sort with
			// the same priority comparator as Stage 1 so the pick is
			// deterministic and respects primary/weight.
			sortEndpointsByPriority(famHit.Endpoints)
			ep := famHit.Endpoints[0]
			return Decision{
				Protocol:     Protocol(ep.Protocol),
				BaseURL:      ep.BaseURL,
				EndpointID:   ep.ID,
				VendorNative: ep.VendorNative,
				Passthrough:  false,
				MatchRule:    MatchStage2,
				Reason:       "family hit, protocol mismatch",
			}
		}
	}

	// Stage 3 — family requested but empty (caller signals "I don't
	// know"). Today this collapses to Stage 4 (legacy fallback) — we
	// keep the slot explicit so a future caller can distinguish "I
	// gave up" from "I never tried".
	_ = MatchStage3 // reserved for symmetry

	// Stage 4 — legacy fallback to the candidate's primary endpoint.
	return Decision{
		Protocol:     Protocol(cand.Protocol),
		BaseURL:      cand.BaseURL,
		EndpointID:   0,
		VendorNative: cand.VendorNative,
		Passthrough:  false,
		MatchRule:    MatchStage4,
		Reason:       "no native endpoint matched; legacy primary fallback",
	}
}

// stage1a: protocol AND family both match. Passthrough=true is the
// byte-faithful wire path (reserved for future P5).
func stage1a(cand CandidateLite, ep *EndpointLite) Decision {
	return Decision{
		Protocol:     Protocol(ep.Protocol),
		BaseURL:      ep.BaseURL,
		EndpointID:   ep.ID,
		VendorNative: ep.VendorNative,
		Passthrough:  true,
		MatchRule:    MatchStage1A,
		Reason:       "protocol + family match (passthrough eligible)",
	}
}

// stage1b: protocol matches, family known but mismatches the endpoint's
// vendor_native declaration. We still pick the same-protocol endpoint
// (preserves serializer/paramguard) but disable passthrough because the
// IR bridging may need to massage fields the vendor expects.
func stage1b(cand CandidateLite, ep *EndpointLite, wantFamily string) Decision {
	return Decision{
		Protocol:     Protocol(ep.Protocol),
		BaseURL:      ep.BaseURL,
		EndpointID:   ep.ID,
		VendorNative: ep.VendorNative,
		Passthrough:  false,
		MatchRule:    MatchStage1B,
		Reason:       "protocol match; family " + wantFamily + " != vendor_native " + ep.VendorNative + " (IR bridging required)",
	}
}

// stage1c: protocol matches, family is unknown. Pick the same-protocol
// endpoint, disable passthrough as a safety net.
func stage1c(cand CandidateLite, ep *EndpointLite) Decision {
	return Decision{
		Protocol:     Protocol(ep.Protocol),
		BaseURL:      ep.BaseURL,
		EndpointID:   ep.ID,
		VendorNative: ep.VendorNative,
		Passthrough:  false,
		MatchRule:    MatchStage1C,
		Reason:       "protocol match; family unknown (IR bridging required)",
	}
}

// endpointPriorityLess is the single priority comparator for endpoint
// ordering, shared by Stage 1 (pickBest) and Stage 2 (family-hit pick):
// primary first, then weight DESCENDING — r0924 fix-a task 6b: weight is
// a traffic share on the provider side (provider/client.go
// applyCapacityWeightedLB, executors/router.go promoteWeightedCandidate:
// higher weight = larger share), so the higher weight must win the tie.
// ID ascending is the final deterministic tiebreak.
func endpointPriorityLess(a, b EndpointLite) bool {
	if a.IsPrimary != b.IsPrimary {
		return a.IsPrimary
	}
	if a.Weight != b.Weight {
		return a.Weight > b.Weight
	}
	return a.ID < b.ID
}

// sortEndpointsByPriority orders a slice in-place by the shared endpoint
// priority comparator.
func sortEndpointsByPriority(eps []EndpointLite) {
	sort.Slice(eps, func(i, j int) bool {
		return endpointPriorityLess(eps[i], eps[j])
	})
}

// pickBest returns the highest-priority endpoint that matches
// wantProtocol AND is Enabled, or nil when no endpoint matches.
func pickBest(eps []EndpointLite, wantProtocol Protocol, wantFamily string) *EndpointLite {
	if len(eps) == 0 {
		return nil
	}
	matched := make([]EndpointLite, 0, len(eps))
	for _, ep := range eps {
		if !ep.Enabled {
			continue
		}
		if Protocol(ep.Protocol) != wantProtocol {
			continue
		}
		matched = append(matched, ep)
	}
	if len(matched) == 0 {
		return nil
	}
	// Sort: primary first, then by weight descending (higher weight =
	// higher priority — provider traffic-share semantics, r0924 fix-a
	// task 6b).
	sortEndpointsByPriority(matched)
	ep := matched[0]
	return &ep
}

// familyHit groups endpoints by vendor_native.
type familyHit struct {
	VendorNative string
	Endpoints    []EndpointLite
}

// pickFamilyHit returns the family-grouped match for Stage 2. We pick
// the vendor_native with the most enabled endpoints (deterministic
// across re-routes) and break ties by the highest weight (provider
// traffic-share semantics, r0924 fix-a task 6b — the old rule preferred
// the LOWEST weight, the inverse of the provider-side contract).
func pickFamilyHit(eps []EndpointLite, wantFamily string, wantProtocol Protocol) *familyHit {
	byFamily := map[string][]EndpointLite{}
	for _, ep := range eps {
		if !ep.Enabled {
			continue
		}
		if ep.VendorNative != wantFamily {
			continue
		}
		if Protocol(ep.Protocol) == wantProtocol {
			// Same family + same protocol — handled in Stage 1.
			continue
		}
		byFamily[ep.VendorNative] = append(byFamily[ep.VendorNative], ep)
	}
	if len(byFamily) == 0 {
		return nil
	}
	// Pick the vendor_native with the most endpoints, ties broken by
	// max weight descending.
	type cand struct {
		vendor string
		eps    []EndpointLite
		count  int
		maxW   int
	}
	cands := make([]cand, 0, len(byFamily))
	for v, list := range byFamily {
		maxW := list[0].Weight
		for _, e := range list[1:] {
			if e.Weight > maxW {
				maxW = e.Weight
			}
		}
		cands = append(cands, cand{vendor: v, eps: list, count: len(list), maxW: maxW})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].count != cands[j].count {
			return cands[i].count > cands[j].count
		}
		if cands[i].maxW != cands[j].maxW {
			return cands[i].maxW > cands[j].maxW
		}
		return cands[i].vendor < cands[j].vendor
	})
	top := cands[0]
	return &familyHit{VendorNative: top.vendor, Endpoints: top.eps}
}
