package specboost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Enhance calls an LLM endpoint to enrich the given tool specification. The
// endpoint is expected to return a JSON object matching the prompt contract
// (see PromptTemplateV1). This is a PoC: the endpoint is mocked in tests and
// in production will be a cheap model (gpt-4o-mini / Claude Haiku).
//
// Enhance is safe for concurrent use: it holds no mutable state.
func Enhance(ctx context.Context, original ToolSpec, opts EnhanceOptions) (*EnhancedSpec, error) {
	opts = opts.withDefaults()

	prompt := buildPrompt(original)
	reqBody, err := json.Marshal(promptRequest{Prompt: prompt, Tool: original})
	if err != nil {
		return nil, fmt.Errorf("specboost: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, opts.Endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("specboost: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+opts.APIKey)

	client := &http.Client{Timeout: opts.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("specboost: llm call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("specboost: llm returned status %d: %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}

	// Cap the response size to prevent runaway LLM output from exhausting memory.
	dec := json.NewDecoder(io.LimitReader(resp.Body, int64(opts.MaxResponseBytes)))
	var llmResp llmResponse
	if err := dec.Decode(&llmResp); err != nil {
		return nil, fmt.Errorf("specboost: decode llm response (possibly truncated at %d bytes): %w",
			opts.MaxResponseBytes, err)
	}

	enhanced := ToolSpec{
		Name:        original.Name,
		Description: llmResp.Description,
		Parameters:  mergeParams(original.Parameters, llmResp.Parameters),
		Examples:    llmResp.Examples,
	}
	if enhanced.Description == "" {
		enhanced.Description = original.Description
	}

	conf := llmResp.Confidence
	if conf < 0 {
		conf = 0
	} else if conf > 1 {
		conf = 1
	}

	return &EnhancedSpec{
		Original:    original,
		Enhanced:    enhanced,
		DiffSummary: llmResp.DiffSummary,
		Confidence:  conf,
		EnhancedAt:  time.Now().UTC(),
		TemplateVer: PromptTemplateV1,
	}, nil
}

// promptRequest is the payload sent to the LLM endpoint.
type promptRequest struct {
	Prompt string   `json:"prompt"`
	Tool   ToolSpec `json:"tool"`
}

// llmResponse is the expected JSON shape returned by the LLM endpoint.
type llmResponse struct {
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Examples    []Example      `json:"examples,omitempty"`
	DiffSummary string         `json:"diff_summary"`
	Confidence  float64        `json:"confidence"`
}

// buildPrompt constructs the V1 enhancement prompt. Bump PromptTemplateV1 if
// this string changes.
func buildPrompt(t ToolSpec) string {
	var b strings.Builder
	b.WriteString("You are a tool-description enhancer. ")
	b.WriteString("Given the tool specification below, produce an improved version with:\n")
	b.WriteString("1. A clearer, more detailed description (what it does, when to use it).\n")
	b.WriteString("2. Parameter descriptions enriched with constraints and examples.\n")
	b.WriteString("3. At least one concrete usage example.\n\n")
	b.WriteString("Return JSON with fields: description, parameters, examples, diff_summary, confidence.\n\n")
	b.WriteString("Tool name: " + t.Name + "\n")
	b.WriteString("Current description: " + t.Description + "\n")
	return b.String()
}

// mergeParams merges the LLM-enhanced parameter metadata into the original.
// The LLM's values win when present; original required/constraints are kept.
func mergeParams(original map[string]ParamSpec, llmParams map[string]any) map[string]ParamSpec {
	if len(original) == 0 && len(llmParams) == 0 {
		return nil
	}
	out := make(map[string]ParamSpec, len(original))
	for k, v := range original {
		out[k] = v
	}
	for k, raw := range llmParams {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		p := out[k] // zero value if absent
		if d, ok := m["description"].(string); ok && d != "" {
			p.Description = d
		}
		if ty, ok := m["type"].(string); ok && ty != "" {
			p.Type = ty
		}
		if exs, ok := m["examples"].([]any); ok {
			p.Examples = exs
		}
		out[k] = p
	}
	return out
}
