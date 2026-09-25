package settings

import (
	"os"
	"strconv"
	"strings"
)

// P4FeatureFlags controls the r0924 supplier-protocol-optimization P4
// (endpoint selector + Ollama native) rollout.
//
// All flags default to false/disabled to ensure zero impact on existing
// routing until explicitly enabled. When disabled, the dispatcher continues
// using legacy BaseURL/Protocol from provider.Candidate (Stage 4 fallback).
//
// Design: docs/供应商协议优化-实施规划.md P4.1-P4.3
type P4FeatureFlags struct {
	// EndpointSelectorEnabled is the master switch for endpointselect.Select()
	// integration in executor_dispatch.go. When false, the dispatcher uses the
	// legacy candidate.BaseURL and candidate.Protocol without modification
	// (Stage 4 fallback behavior).
	//
	// Default: false (selector disabled, legacy routing)
	// Env: FF_ENDPOINT_SELECTOR=true
	EndpointSelectorEnabled bool

	// OllamaNativeEnabled enables the ollama-native protocol executor.
	// When false, the dispatch switch rejects an ollama-native primary
	// with KindUnsupportedFeature; it does not send OpenAI wire to /api/chat.
	// Selector also excludes optional ollama-native endpoints.
	//
	// Default: false (ollama-native executor disabled)
	// Env: FF_OLLAMA_NATIVE=true
	OllamaNativeEnabled bool
}

// GetP4Flags returns the P4 feature flags, read from environment variables
// (FF_ENDPOINT_SELECTOR, FF_OLLAMA_NATIVE) with default=false.
//
// Usage in executor_dispatch.go:
//
//	p4flags := settings.GetP4Flags()
//	if p4flags.EndpointSelectorEnabled {
//	    decision := endpointselect.Select(...)
//	    cand.BaseURL = decision.BaseURL
//	    cand.Protocol = decision.Protocol
//	}
func GetP4Flags() *P4FeatureFlags {
	return &P4FeatureFlags{
		EndpointSelectorEnabled: envBoolP4("FF_ENDPOINT_SELECTOR", false),
		OllamaNativeEnabled:     envBoolP4("FF_OLLAMA_NATIVE", false),
	}
}

// envBoolP4 parses a boolean env var; empty or invalid → defaultValue.
// Duplicated from routing_opt_feature_flags.go to avoid cross-package import
// cycles (settings is a leaf package).
func envBoolP4(key string, defaultValue bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return defaultValue
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return defaultValue
	}
	return v
}
