package executors

import (
	"encoding/json"
	"log/slog"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// applyInlineValidation applies format validation to OpenAI request JSON.
// Used in legacy path where IR converter may not be initialized.
//
// 2026-07-12: Created to ensure tool_call_id_mismatch prevention works
// even when e.IR is nil (which is the default in production).
func applyInlineValidation(bodyBytes []byte) []byte {
	// Parse JSON into ir.InternalRequest
	var rawReq struct {
		Model       string              `json:"model"`
		Messages    []ir.Message        `json:"messages"`
		Temperature *float64            `json:"temperature,omitempty"`
		MaxTokens   *int                `json:"max_tokens,omitempty"`
		Stream      *bool               `json:"stream,omitempty"`
		Tools       []ir.ToolDefinition `json:"tools,omitempty"`
		ToolChoice  *ir.ToolChoice      `json:"tool_choice,omitempty"`
	}

	if err := json.Unmarshal(bodyBytes, &rawReq); err != nil {
		slog.Warn("applyInlineValidation: JSON parse failed, skipping validation",
			"error", err.Error(),
		)
		return bodyBytes
	}

	// Validate and fix
	irReq := &ir.InternalRequest{
		Model:    rawReq.Model,
		Messages: rawReq.Messages,
		Tools:    rawReq.Tools,
	}
	if rawReq.Temperature != nil {
		irReq.Temperature = rawReq.Temperature
	}
	if rawReq.MaxTokens != nil {
		irReq.MaxTokens = *rawReq.MaxTokens
	}
	if rawReq.Stream != nil {
		irReq.Stream = *rawReq.Stream
	}
	if rawReq.ToolChoice != nil {
		irReq.ToolChoice = rawReq.ToolChoice
	}

	irReq = ir.ValidateAndFixRequest(irReq)

	// Serialize back to JSON
	fixed := map[string]interface{}{
		"model":    irReq.Model,
		"messages": irReq.Messages,
	}
	if irReq.Temperature != nil {
		fixed["temperature"] = irReq.Temperature
	}
	if irReq.MaxTokens != 0 {
		fixed["max_tokens"] = irReq.MaxTokens
	}
	if irReq.Stream {
		fixed["stream"] = irReq.Stream
	}
	if len(irReq.Tools) > 0 {
		fixed["tools"] = irReq.Tools
	}
	if irReq.ToolChoice != nil {
		fixed["tool_choice"] = irReq.ToolChoice
	}

	fixedBytes, err := json.Marshal(fixed)
	if err != nil {
		slog.Warn("applyInlineValidation: JSON marshal failed, using original",
			"error", err.Error(),
		)
		return bodyBytes
	}

	return fixedBytes
}
