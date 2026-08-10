// Package gateway provides event field extraction for outbox events.
//
// This file implements Phase 2 Step 3: extracting the 10 missing fields
// for request.completed.v1 events according to the Gateway → ASM contract.
//
// Refs: docs/omni-ref2/03-GATEWAY-EVENT-FIELD-MAPPING.md
//
//	docs/omni-ref2/02-CROSS-REPO-EVENT-CONTRACT.md §3
package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
)

// extractRequestCompletedPayload extracts the complete payload for request.completed.v1
// according to the ASM contract.
//
// Phase 2 Step 3: This function replaces the partial payload in main_pipeline.go:845
// with the complete 11-field contract-compliant payload.
//
// Missing fields implemented (10):
//   - turn_no, correlation_id, idempotency_key, provider, status
//   - token_usage, latency_ms, body_refs
//
// Removed fields (1):
//   - user_content (violates contract - prompt text must not be in payload)
func extractRequestCompletedPayload(
	env *domain.PipelineRequest,
	requestID string,
	startTime time.Time,
) map[string]any {
	if env == nil {
		return nil
	}

	// Calculate latency
	latencyMs := int(time.Since(startTime).Milliseconds())

	// Extract provider from routing decision
	provider := extractProvider(env)

	// Extract model
	model := extractModel(env)

	// Convert HTTP status code to contract status enum
	status := httpCodeToStatus(env.StatusCode)

	// Extract token usage from response or metadata
	tokenUsage := extractTokenUsage(env)

	// Generate correlation_id and idempotency_key
	correlationID := requestID + "-corr"
	idempotencyKey := requestID + "-idem"

	// Extract turn_no (from session cache or default to 1)
	turnNo := extractTurnNo(env)

	// Generate body_refs (internal references)
	bodyRefs := map[string]string{
		"prompt_ref":   fmt.Sprintf("internal://body/%s/prompt", requestID),
		"response_ref": fmt.Sprintf("internal://body/%s/response", requestID),
	}

	// Build contract-compliant payload
	payload := map[string]any{
		"session_id":      env.SessionID,
		"turn_no":         turnNo,
		"request_id":      requestID,
		"correlation_id":  correlationID,
		"idempotency_key": idempotencyKey,
		"provider":        provider,
		"model":           model,
		"status":          status,
		"token_usage":     tokenUsage,
		"latency_ms":      latencyMs,
		"body_refs":       bodyRefs,
	}

	return payload
}

// extractProvider extracts the provider name from the routing decision.
//
// Priority:
//  1. env.SelectedProvider (if routing completed)
//  2. env.Metadata["provider"] (fallback)
//  3. "unknown" (if routing failed)
func extractProvider(env *domain.PipelineRequest) string {
	if env.SelectedProvider != nil && env.SelectedProvider.Name != "" {
		return env.SelectedProvider.Name
	}
	if provider, ok := env.Metadata["provider"].(string); ok && provider != "" {
		return provider
	}
	return "unknown"
}

// extractModel extracts the model name.
//
// Priority:
//  1. env.Metadata["model"] (set by routing/relay)
//  2. "unknown" (if not set)
func extractModel(env *domain.PipelineRequest) string {
	if model, ok := env.Metadata["model"].(string); ok && model != "" {
		return model
	}
	return "unknown"
}

// httpCodeToStatus converts HTTP status code to contract status enum.
//
// Contract status values (02-CROSS-REPO-EVENT-CONTRACT.md §3):
//   - "succeeded" (2xx)
//   - "failed" (4xx, 5xx)
//   - "timeout" (specific timeout errors)
func httpCodeToStatus(statusCode int) string {
	if statusCode >= 200 && statusCode < 300 {
		return "succeeded"
	}
	if statusCode == 408 || statusCode == 504 {
		return "timeout"
	}
	return "failed"
}

// extractTokenUsage extracts token usage from response or metadata.
//
// This attempts to parse token usage from:
//  1. env.Metadata["token_usage"] (if already extracted)
//  2. env.UpstreamResponse (parse JSON for usage field)
//  3. Default to zero values if not available
func extractTokenUsage(env *domain.PipelineRequest) map[string]int {
	// Try metadata first
	if usage, ok := env.Metadata["token_usage"].(map[string]int); ok {
		return usage
	}

	// Try parsing from upstream response
	if len(env.UpstreamResponse) > 0 {
		var resp struct {
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(env.UpstreamResponse, &resp); err == nil {
			if resp.Usage.TotalTokens > 0 {
				return map[string]int{
					"prompt_tokens":     resp.Usage.PromptTokens,
					"completion_tokens": resp.Usage.CompletionTokens,
					"total_tokens":      resp.Usage.TotalTokens,
				}
			}
		}
	}

	// Default to zero
	return map[string]int{
		"prompt_tokens":     0,
		"completion_tokens": 0,
		"total_tokens":      0,
	}
}

// extractTurnNo extracts the turn number for this session.
//
// Priority:
//  1. env.Metadata["turn_no"] (if session cache provides it)
//  2. Default to 1 (first turn)
//
// TODO(Phase 3): Integrate with session cache to get accurate turn count.
func extractTurnNo(env *domain.PipelineRequest) int {
	if turnNo, ok := env.Metadata["turn_no"].(int); ok && turnNo > 0 {
		return turnNo
	}
	// Default to 1 (first turn)
	return 1
}
