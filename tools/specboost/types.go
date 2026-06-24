// Package specboost enriches tool specifications (OpenAPI / MCP schema) using
// an LLM. The goal is to improve function-calling accuracy: Google's internal
// data shows that richer tool descriptions can boost accuracy by up to 56%.
//
// This is a standalone PoC (NOW-3). It does NOT call a real LLM — the HTTP
// endpoint is mocked via httptest.NewServer. Real LLM integration is a
// separate PR (Q4 C1-1).
//
// Domain boundary (see docs/产品方案/2026-06-23-llmgw-domain-architecture-refactor.md):
//   - specboost OWNS: prompt construction, LLM response parsing, diff
//   - specboost does NOT own: tool storage (registry/), LLM routing (relay/)
//
// Prompt template versioning: the const PromptTemplateV1 below is the
// contract. If the prompt changes, bump the version so callers can attribute
// quality differences to the right template.
package specboost

import "time"

// PromptTemplateV1 is the prompt template identifier. Any change to the
// enhancement prompt MUST bump this version (V2, V3, ...) so that A/B
// comparisons and regressions are attributable.
const PromptTemplateV1 = "specboost-prompt-v1"

// ToolSpec is the normalized tool specification consumed by Enhance. It is
// derived from registry.Tool but flattened for LLM consumption.
type ToolSpec struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Parameters  map[string]ParamSpec `json:"parameters,omitempty"`
	Examples    []Example            `json:"examples,omitempty"`
}

// ParamSpec describes a single tool parameter.
type ParamSpec struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Examples    []any  `json:"examples,omitempty"`
}

// Example is a concrete usage example of the tool.
type Example struct {
	Input  map[string]any `json:"input,omitempty"`
	Output any            `json:"output,omitempty"`
}

// EnhancedSpec is the result of Enhance: the original spec, the enhanced
// spec, a human-readable diff summary from the LLM, and a confidence score.
type EnhancedSpec struct {
	Original    ToolSpec  `json:"original"`
	Enhanced    ToolSpec  `json:"enhanced"`
	DiffSummary string    `json:"diff_summary"`
	Confidence  float64   `json:"confidence"` // [0,1], LLM self-reported
	EnhancedAt  time.Time `json:"enhanced_at"`
	TemplateVer string    `json:"template_ver"` // e.g. PromptTemplateV1
}

// EnhanceOptions controls the enhancement behavior.
type EnhanceOptions struct {
	// Endpoint is the LLM HTTP endpoint URL. In PoC this is an httptest server.
	Endpoint string
	// APIKey is sent as Bearer token to the LLM endpoint.
	APIKey string
	// MaxResponseBytes caps the LLM response size (default 4096). Prevents
	// runaway responses from blowing up memory.
	MaxResponseBytes int
	// Timeout for the LLM HTTP call (default 10s).
	Timeout time.Duration
}

// withDefaults returns a copy of opts with zero values replaced by defaults.
func (o EnhanceOptions) withDefaults() EnhanceOptions {
	out := o
	if out.MaxResponseBytes == 0 {
		out.MaxResponseBytes = 4096
	}
	if out.Timeout == 0 {
		out.Timeout = 10 * time.Second
	}
	return out
}
