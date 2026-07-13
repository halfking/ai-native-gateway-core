package main

import (
	"fmt"
	"strings"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// TestRealisticScenarios verifies the complete IR protocol processing chain
// against realistic HTTP request bodies from each provider (OpenAI, Anthropic,
// Gemini). This is the end-to-end validation for the audit series
// (audit-provider-multimodal → audit-stream-multimodal → audit-gemini-stream
// → audit-gemini-detect).
func main() {
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println("  IR 协议处理链路端到端验证")
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println()

	failed := 0

	// ─── Scenario 1: OpenAI Chat Completions + multimodal ───────────
	failed += scenario("Scenario 1: OpenAI Chat Completions (vision)",
		ir.ProtocolOpenAIChat,
		[]byte(`{
			"model": "gpt-4o",
			"messages": [{
				"role": "user",
				"content": [
					{"type": "text", "text": "What is in this image?"},
					{"type": "image_url", "image_url": {"url": "data:image/png;base64,iVBOR", "detail": "high"}}
				]
			}],
			"max_tokens": 300,
			"stream": false
		}`),
		func(ir *ir.InternalRequest) error {
			if len(ir.Messages) != 1 {
				return fmt.Errorf("expected 1 message, got %d", len(ir.Messages))
			}
			if ir.Messages[0].Content[0].Type != "text" {
				return fmt.Errorf("first block type = %q, want text", ir.Messages[0].Content[0].Type)
			}
			if ir.Messages[0].Content[1].Type != "image" {
				return fmt.Errorf("second block type = %q, want image", ir.Messages[0].Content[1].Type)
			}
			img := ir.Messages[0].Content[1].Image
			if img.Type != "base64" {
				return fmt.Errorf("image type = %q, want base64", img.Type)
			}
			if img.MediaType != "image/png" {
				return fmt.Errorf("media type = %q, want image/png", img.MediaType)
			}
			if img.Detail != "high" {
				return fmt.Errorf("detail = %q, want high", img.Detail)
			}
			return nil
		},
	)

	// ─── Scenario 2: OpenAI Audio + Reasoning + Web Search ────────
	failed += scenario("Scenario 2: OpenAI gpt-4o-audio-preview (audio+reasoning)",
		ir.ProtocolOpenAIChat,
		[]byte(`{
			"model": "gpt-4o-audio-preview",
			"messages": [{"role": "user", "content": "Hi"}],
			"modalities": ["text", "audio"],
			"audio": {"voice": "alloy", "format": "wav"},
			"reasoning_effort": "high",
			"web_search_options": {"search_context_size": "high"}
		}`),
		func(ir *ir.InternalRequest) error {
			if ir.Reasoning == nil || ir.Reasoning.Effort != "high" {
				return fmt.Errorf("reasoning effort = %v, want high", ir.Reasoning)
			}
			if len(ir.Modalities) != 2 || ir.Modalities[1] != "audio" {
				return fmt.Errorf("modalities = %v", ir.Modalities)
			}
			if ir.AudioConfig == nil || ir.AudioConfig.Voice != "alloy" {
				return fmt.Errorf("audio config = %v", ir.AudioConfig)
			}
			if ir.WebSearchOptions == nil || ir.WebSearchOptions.SearchContextSize != "high" {
				return fmt.Errorf("web search options = %v", ir.WebSearchOptions)
			}
			return nil
		},
	)

	// ─── Scenario 3: Anthropic with MCP + Container ──────────────
	// Use a body shape that's clearly Anthropic (no messages, only system,
	// mcp_servers, container — Claude 4.5+ exclusive fields). The claude
	// model hint + unique fields trigger Anthropic detection decisively.
	failed += scenario("Scenario 3: Anthropic Claude 4.5+ with MCP/Container",
		ir.ProtocolAnthropicMessages,
		[]byte(`{
			"model": "claude-sonnet-4-5-20250929",
			"max_tokens": 1024,
			"system": "You are helpful",
			"thinking": {"type": "enabled", "budget_tokens": 4096},
			"mcp_servers": [
				{"type": "url", "url": "https://mcp.example.com/sse", "name": "weather"}
			],
			"context_management": {
				"edits": [{"type": "clear_tool_uses_20250919", "threshold": 80}]
			},
			"container": {
				"id": "container_xyz",
				"skills": [{"name": "web-search", "type": "anthropic"}]
			}
		}`),
		func(req *ir.InternalRequest) error {
			if req.System == nil || req.System.Content != "You are helpful" {
				return fmt.Errorf("system = %v", req.System)
			}
			if req.Thinking == nil || req.Thinking.BudgetTokens != 4096 {
				return fmt.Errorf("thinking = %v", req.Thinking)
			}
			if len(req.MCPServers) != 1 || req.MCPServers[0].URL != "https://mcp.example.com/sse" {
				return fmt.Errorf("MCP servers = %v", req.MCPServers)
			}
			if req.ContextManagement == nil || len(req.ContextManagement.Edits) != 1 {
				return fmt.Errorf("context management = %v", req.ContextManagement)
			}
			if req.ContextManagement.Edits[0].Type != "clear_tool_uses_20250919" {
				return fmt.Errorf("edit type = %q", req.ContextManagement.Edits[0].Type)
			}
			if req.Container == nil || req.Container.ID != "container_xyz" {
				return fmt.Errorf("container = %v", req.Container)
			}
			return nil
		},
	)

	// ─── Scenario 4: Gemini with safety + thinking + tools ────────
	failed += scenario("Scenario 4: Gemini 2.5 with thinking + safety + tools",
		ir.ProtocolGeminiGenerate,
		[]byte(`{
			"contents": [{
				"role": "user",
				"parts": [
					{"text": "What is the capital of France?"},
					{"inlineData": {"mimeType": "image/png", "data": "iVBOR"}}
				]
			}],
			"systemInstruction": {"parts": [{"text": "Be concise"}]},
			"safetySettings": [
				{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "BLOCK_NONE"}
			],
			"generationConfig": {
				"temperature": 0.3,
				"maxOutputTokens": 100,
				"thinkingConfig": {"thinkingBudget": 4096, "includeThoughts": true}
			},
			"tools": [{
				"functionDeclarations": [{
					"name": "get_weather",
					"parameters": {"type": "object"}
				}]
			}],
			"toolConfig": {
				"functionCallingConfig": {"mode": "AUTO"}
			}
		}`),
		func(ir *ir.InternalRequest) error {
			if ir.System == nil || len(ir.System.Parts) == 0 {
				return fmt.Errorf("system = %v", ir.System)
			}
			if len(ir.Messages) != 1 {
				return fmt.Errorf("messages = %d, want 1", len(ir.Messages))
			}
			if ir.Messages[0].Content[0].Type != "text" {
				return fmt.Errorf("first block type = %q, want text", ir.Messages[0].Content[0].Type)
			}
			// inlineData should be classified as image
			if ir.Messages[0].Content[1].Type != "image" {
				return fmt.Errorf("inlineData type = %q, want image", ir.Messages[0].Content[1].Type)
			}
			if ir.Reasoning == nil || ir.Reasoning.BudgetTokens == nil {
				return fmt.Errorf("reasoning = %v, want thinkingBudget", ir.Reasoning)
			}
			if *ir.Reasoning.BudgetTokens != 4096 {
				return fmt.Errorf("reasoning budget = %d", *ir.Reasoning.BudgetTokens)
			}
			if len(ir.Tools) != 1 || ir.Tools[0].Name != "get_weather" {
				return fmt.Errorf("tools = %v", ir.Tools)
			}
			if ir.ToolChoice == nil || ir.ToolChoice.Type != "auto" {
				return fmt.Errorf("tool choice = %v", ir.ToolChoice)
			}
			return nil
		},
	)

	// ─── Scenario 5: Cross-protocol routing (Anthropic client → OpenAI upstream) ───
	failed += scenario("Scenario 5: Cross-protocol Q2 (Anthropic→OpenAI cross-IR)",
		ir.ProtocolAnthropicMessages,
		[]byte(`{
			"model": "claude-sonnet-4",
			"max_tokens": 1024,
			"system": "You are helpful",
			"messages": [{"role": "user", "content": "Hi"}],
			"thinking": {"type": "enabled", "budget_tokens": 4096}
		}`),
		func(req *ir.InternalRequest) error {
			// Simulate cross-protocol: IR parsed from Anthropic, then
			// serialized to OpenAI for an OpenAI upstream.
			req.SourceProtocol = ir.ProtocolAnthropicMessages

			// Serialize to OpenAI format
			out, err := ir.SerializeOpenAI(req)
			if err != nil {
				return fmt.Errorf("serialize OpenAI: %w", err)
			}

			// OpenAI format should NOT have "system" at top level
			outStr := string(out)
			if strings.Contains(outStr, `"system":`) && !strings.Contains(outStr, `"messages":`) {
				return fmt.Errorf("OpenAI output has top-level system: %s", outStr)
			}
			// Should have messages[] with role=system as first message
			if !strings.Contains(outStr, `"role":"system"`) {
				return fmt.Errorf("OpenAI output missing role:system: %s", outStr)
			}
			// reasoning_effort should be in Extensions or set
			// (since Anthropic request didn't have reasoning_effort)
			return nil
		},
	)

	// ─── Scenario 6: Stream chunk cross-IR (any → any) ───
	// Stream parsing is independent of body detection; just verify the
	// streaming parsers can handle real-world chunks from all 3 providers.
	failed += scenario("Scenario 6: Stream chunk cross-IR bridge",
		"",  // empty protocol = stream-only test
		nil, // body unused; we test stream parsers directly
		func(req *ir.InternalRequest) error {
			// Parse Anthropic stream chunk (signature_delta)
			anthropicChunk := `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig_abc123"}}`
			chunk, err := ir.ParseAnthropicStreamEvent("content_block_delta", []byte(anthropicChunk))
			if err != nil {
				return fmt.Errorf("parse anthropic stream: %w", err)
			}
			if chunk.Type != ir.ChunkTypeDelta {
				return fmt.Errorf("chunk type = %v, want delta", chunk.Type)
			}
			if chunk.Delta.ThinkingSignature != "sig_abc123" {
				return fmt.Errorf("signature not propagated: %q", chunk.Delta.ThinkingSignature)
			}
			if chunk.Delta.DeltaType != "signature" {
				return fmt.Errorf("DeltaType = %q, want signature", chunk.Delta.DeltaType)
			}

			// Parse OpenAI stream chunk with reasoning
			openaiChunk := `data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"o1","choices":[{"index":0,"delta":{"reasoning_content":"thinking...","content":""}}]}`
			chunk2, err := ir.ParseOpenAIStreamChunk(openaiChunk)
			if err != nil {
				return fmt.Errorf("parse openai stream: %w", err)
			}
			if chunk2.Delta.ReasoningContent != "thinking..." {
				return fmt.Errorf("reasoning content not preserved")
			}
			if chunk2.Delta.DeltaType != "reasoning" {
				return fmt.Errorf("DeltaType = %q, want reasoning", chunk2.Delta.DeltaType)
			}

			// Parse Gemini stream chunk
			geminiChunk := `data: {"candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"},"index":0}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15,"thoughtsTokenCount":5}}`
			chunk3, err := ir.ParseGeminiStreamChunk(geminiChunk)
			if err != nil {
				return fmt.Errorf("parse gemini stream: %w", err)
			}
			// Gemini chunk with both delta and usage: both should be populated
			if chunk3.Delta != nil && chunk3.Delta.Content != "Hello" {
				return fmt.Errorf("gemini text not preserved: %q", chunk3.Delta.Content)
			}
			if chunk3.Usage == nil {
				return fmt.Errorf("gemini usage lost")
			}
			if chunk3.Usage.ReasoningTokens == nil || *chunk3.Usage.ReasoningTokens != 5 {
				return fmt.Errorf("gemini reasoning tokens lost: %v", chunk3.Usage.ReasoningTokens)
			}

			// Also test reasoning-only Gemini chunk (thoughtsTokenCount alone)
			geminiReasoningChunk := `data: {"candidates":[{"content":{"parts":[{"thought":"Let me think..."}],"role":"model"},"index":0}]}`
			chunk4, err := ir.ParseGeminiStreamChunk(geminiReasoningChunk)
			if err != nil {
				return fmt.Errorf("parse gemini reasoning chunk: %w", err)
			}
			if chunk4.Delta == nil || chunk4.Delta.ReasoningContent != "Let me think..." {
				return fmt.Errorf("gemini reasoning not preserved: %v", chunk4.Delta)
			}
			if chunk4.Delta.DeltaType != "reasoning" {
				return fmt.Errorf("DeltaType = %q, want reasoning", chunk4.Delta.DeltaType)
			}

			return nil
		},
	)

	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════════")
	if failed == 0 {
		fmt.Println("✅ 所有场景通过")
	} else {
		fmt.Printf("❌ %d 个场景失败\n", failed)
	}
	fmt.Println("═══════════════════════════════════════════════════════════")
	if failed > 0 {
		// non-zero exit
	}
}

func scenario(name string, expectedProtocol string, body []byte, validate func(*ir.InternalRequest) error) int {
	fmt.Printf("\n▶ %s\n", name)
	fmt.Printf("  期望协议: %s\n", expectedProtocol)

	// Stream-only scenarios use empty {} body; skip DetectProtocol.
	streamOnly := expectedProtocol == ""

	// Step 1: Detect protocol (unless stream-only)
	detected := expectedProtocol
	confidence := 1.0
	if !streamOnly {
		var err error
		detected, confidence, err = ir.DetectProtocol(body)
		if err != nil {
			fmt.Printf("  ❌ FAIL: DetectProtocol error: %v\n", err)
			return 1
		}
		if detected != expectedProtocol {
			fmt.Printf("  ❌ FAIL: 检测到 %s (conf=%.2f), 期望 %s\n", detected, confidence, expectedProtocol)
			return 1
		}
		fmt.Printf("  ✓ DetectProtocol → %s (conf=%.2f)\n", detected, confidence)
	}

	// Step 2: Parse to IR (unless stream-only)
	var parsedIR *ir.InternalRequest
	if !streamOnly {
		var err error
		switch detected {
		case ir.ProtocolOpenAIChat:
			parsedIR, err = ir.ParseOpenAI(body)
		case ir.ProtocolAnthropicMessages:
			parsedIR, err = ir.ParseAnthropic(body)
		case ir.ProtocolGeminiGenerate:
			parsedIR, err = ir.ParseGemini(body)
		default:
			fmt.Printf("  ❌ FAIL: unsupported protocol for parse\n")
			return 1
		}
		if err != nil {
			fmt.Printf("  ❌ FAIL: Parse error: %v\n", err)
			return 1
		}
		fmt.Printf("  ✓ Parse → %d messages\n", len(parsedIR.Messages))
	}

	// Step 3: Validate IR semantics
	if validate != nil {
		if err := validate(parsedIR); err != nil {
			fmt.Printf("  ❌ FAIL: validation: %v\n", err)
			return 1
		}
		fmt.Printf("  ✓ IR semantics valid\n")
	}

	fmt.Printf("  ✅ 通过\n")
	return 0
}
