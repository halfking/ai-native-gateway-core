package ir

import (
	"encoding/json"
	"fmt"
)

// ParseGemini parses a Gemini generateContent request body into IR.
//
// Gemini generateContent request shape:
//
//	{
//	  "contents": [{"role":"user","parts":[{"text":"..."}]}],
//	  "systemInstruction": {"parts":[{"text":"..."}]},
//	  "tools": [{"functionDeclarations":[...]}],
//	  "toolConfig": {"functionCallingConfig":{"mode":"AUTO","allowedFunctionNames":[...]}},
//	  "safetySettings": [{"category":"...","threshold":"..."}],
//	  "generationConfig": {"temperature":0.7,"topP":0.9,"maxOutputTokens":1024,
//	                       "responseMimeType":"application/json","responseSchema":{...},
//	                       "thinkingConfig":{"thinkingBudget":8192}},
//	  "cachedContent": "cachedContents/xxx"
//	}
//
// audit-gemini-adapter (2026-07-13): Adds native Gemini → IR conversion as the
// third inbound protocol alongside OpenAI and Anthropic.
func ParseGemini(body []byte) (*InternalRequest, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("empty gemini body")
	}

	// Phase 1: extract unknown fields to Extensions
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(body, &rawMap); err != nil {
		return nil, fmt.Errorf("unmarshal gemini body to map: %w", err)
	}

	// Phase 2: parse known fields
	var src struct {
		Contents          json.RawMessage `json:"contents"`
		SystemInstruction json.RawMessage `json:"systemInstruction,omitempty"`
		Tools             json.RawMessage `json:"tools,omitempty"`
		ToolConfig        json.RawMessage `json:"toolConfig,omitempty"`
		SafetySettings    json.RawMessage `json:"safetySettings,omitempty"`
		GenerationConfig  json.RawMessage `json:"generationConfig,omitempty"`
		CachedContent     string          `json:"cachedContent,omitempty"`
	}

	if err := json.Unmarshal(body, &src); err != nil {
		return nil, fmt.Errorf("unmarshal gemini body: %w", err)
	}

	// Known fields whitelist (other top-level fields flow to Extensions)
	knownFields := map[string]bool{
		"contents": true, "systemInstruction": true, "tools": true, "toolConfig": true,
		"safetySettings": true, "generationConfig": true, "cachedContent": true,
	}
	extensions := make(map[string]json.RawMessage)
	for key, val := range rawMap {
		if !knownFields[key] && len(val) > 0 && string(val) != "null" {
			extensions[key] = val
			// Step 4.10 (2026-07-28): parse-time unknown-field anomaly.
			ReportUnknownField("unknown", ProtocolGeminiGenerate, key, nil)
		}
	}

	ir := &InternalRequest{
		SourceProtocol: ProtocolGeminiGenerate,
		Extensions:     extensions,
	}

	// Parse contents → IR Messages
	if src.Contents != nil && string(src.Contents) != "null" {
		msgs, err := parseGeminiContents(src.Contents)
		if err != nil {
			return nil, fmt.Errorf("parse contents: %w", err)
		}
		ir.Messages = msgs
	}

	// Parse systemInstruction → IR System (Gemini uses "parts" only)
	if src.SystemInstruction != nil && string(src.SystemInstruction) != "null" {
		sys, err := parseGeminiSystemInstruction(src.SystemInstruction)
		if err != nil {
			return nil, fmt.Errorf("parse systemInstruction: %w", err)
		}
		ir.System = sys
	}

	// Parse tools → IR Tools
	if src.Tools != nil && string(src.Tools) != "null" {
		tools, err := parseGeminiTools(src.Tools)
		if err != nil {
			return nil, fmt.Errorf("parse tools: %w", err)
		}
		ir.Tools = tools
	}

	// Parse toolConfig → IR ToolChoice (Gemini: AUTO / ANY / NONE)
	if src.ToolConfig != nil && string(src.ToolConfig) != "null" {
		tc, err := parseGeminiToolConfig(src.ToolConfig)
		if err != nil {
			return nil, fmt.Errorf("parse toolConfig: %w", err)
		}
		ir.ToolChoice = tc
	}

	// Parse generationConfig → IR sampling params
	if src.GenerationConfig != nil && string(src.GenerationConfig) != "null" {
		if err := parseGeminiGenerationConfig(src.GenerationConfig, ir); err != nil {
			return nil, fmt.Errorf("parse generationConfig: %w", err)
		}
	}

	return ir, nil
}

// parseGeminiContents converts Gemini `contents[]` to IR Messages.
// Gemini uses role="model" for assistant turns (mapped to "assistant") and
// role="function" for tool responses (mapped to "tool").
func parseGeminiContents(raw json.RawMessage) ([]Message, error) {
	var contents []struct {
		Role  string `json:"role"`
		Parts []struct {
			Text             string          `json:"text"`
			InlineData       json.RawMessage `json:"inlineData"`
			FileData         json.RawMessage `json:"fileData"`
			FunctionCall     json.RawMessage `json:"functionCall"`
			FunctionResponse json.RawMessage `json:"functionResponse"`
			Thought          string          `json:"thought"`
		} `json:"parts"`
	}
	if err := json.Unmarshal(raw, &contents); err != nil {
		return nil, fmt.Errorf("unmarshal contents: %w", err)
	}

	out := make([]Message, 0, len(contents))
	for _, c := range contents {
		role := c.Role
		switch role {
		case "model":
			role = "assistant"
		case "function":
			role = "tool"
		}

		msg := Message{Role: role}
		for _, p := range c.Parts {
			// Gemini thinking part
			if p.Thought != "" {
				msg.Content = append(msg.Content, ContentBlock{
					Type:     "thinking",
					Thinking: &ThinkingBlock{Thinking: p.Thought},
				})
				continue
			}

			// Inline media (base64)
			if len(p.InlineData) > 0 && string(p.InlineData) != "null" {
				var id struct {
					MimeType string `json:"mimeType"`
					Data     string `json:"data"`
				}
				if err := json.Unmarshal(p.InlineData, &id); err == nil && id.Data != "" {
					msg.Content = append(msg.Content, geminiMediaBlock(id.MimeType, id.Data))
				}
				continue
			}

			// File URI reference (Gemini Files API)
			if len(p.FileData) > 0 && string(p.FileData) != "null" {
				var fd struct {
					MimeType string `json:"mimeType"`
					FileURI  string `json:"fileUri"`
				}
				if err := json.Unmarshal(p.FileData, &fd); err == nil && fd.FileURI != "" {
					msg.Content = append(msg.Content, geminiFileURIMediaBlock(fd.MimeType, fd.FileURI))
				}
				continue
			}

			// Function call (assistant tool call)
			if len(p.FunctionCall) > 0 && string(p.FunctionCall) != "null" {
				var fc struct {
					Name string          `json:"name"`
					Args json.RawMessage `json:"args"`
				}
				if err := json.Unmarshal(p.FunctionCall, &fc); err == nil && fc.Name != "" {
					argsStr := "{}"
					if len(fc.Args) > 0 {
						argsStr = string(fc.Args)
					}
					msg.Content = append(msg.Content, ContentBlock{
						Type: "tool_use",
						ToolUse: &ToolUse{
							ID:    "gemini_call_" + fc.Name,
							Name:  fc.Name,
							Input: fc.Args,
						},
					})
					_ = argsStr
				}
				continue
			}

			// Function response (tool role)
			if len(p.FunctionResponse) > 0 && string(p.FunctionResponse) != "null" {
				var fr struct {
					Name     string          `json:"name"`
					Response json.RawMessage `json:"response"`
				}
				if err := json.Unmarshal(p.FunctionResponse, &fr); err == nil && fr.Name != "" {
					msg.Content = append(msg.Content, ContentBlock{
						Type: "tool_result",
						ToolResult: &ToolResult{
							ToolUseID: "gemini_call_" + fr.Name,
							Content: []ContentBlock{
								{Type: "text", Text: string(fr.Response)},
							},
						},
					})
				}
				continue
			}

			// Plain text part
			if p.Text != "" {
				msg.Content = append(msg.Content, ContentBlock{
					Type: "text",
					Text: p.Text,
				})
			}
		}
		out = append(out, msg)
	}

	return out, nil
}

// geminiMediaBlock dispatches an inline_data part into the correct IR block
// (image/audio/video) based on MIME type.
func geminiMediaBlock(mimeType, base64Data string) ContentBlock {
	switch {
	case len(mimeType) >= 5 && mimeType[:5] == "image":
		return ContentBlock{
			Type: "image",
			Image: &ImageSource{
				Type:      "base64",
				MediaType: mimeType,
				Data:      base64Data,
			},
		}
	case len(mimeType) >= 5 && mimeType[:5] == "audio":
		return ContentBlock{
			Type: "audio",
			Audio: &MediaSource{
				Type:      "base64",
				MediaType: mimeType,
				Data:      base64Data,
				Format:    mimeType[6:], // strip "audio/" prefix
			},
		}
	case len(mimeType) >= 5 && mimeType[:5] == "video":
		return ContentBlock{
			Type: "video",
			Video: &MediaSource{
				Type:      "base64",
				MediaType: mimeType,
				Data:      base64Data,
				Format:    mimeType[6:],
			},
		}
	default:
		// Unknown MIME → store as document
		return ContentBlock{
			Type: "document",
			Document: &DocumentBlock{
				MIMEType: mimeType,
				Source: &DocumentSource{
					Type:      "base64",
					MediaType: mimeType,
					Data:      base64Data,
				},
			},
		}
	}
}

// geminiFileURIMediaBlock converts a fileData part into the appropriate IR
// block using file_uri as the source carrier.
func geminiFileURIMediaBlock(mimeType, fileURI string) ContentBlock {
	switch {
	case len(mimeType) >= 5 && mimeType[:5] == "image":
		return ContentBlock{
			Type: "image",
			Image: &ImageSource{
				Type:      "file_uri",
				MediaType: mimeType,
				URL:       fileURI,
			},
		}
	case len(mimeType) >= 5 && mimeType[:5] == "audio":
		return ContentBlock{
			Type: "audio",
			Audio: &MediaSource{
				Type:      "file_uri",
				MediaType: mimeType,
				FileURI:   fileURI,
			},
		}
	case len(mimeType) >= 5 && mimeType[:5] == "video":
		return ContentBlock{
			Type: "video",
			Video: &MediaSource{
				Type:      "file_uri",
				MediaType: mimeType,
				FileURI:   fileURI,
			},
		}
	default:
		return ContentBlock{
			Type: "document",
			Document: &DocumentBlock{
				MIMEType: mimeType,
				Source: &DocumentSource{
					Type:      "url",
					Data:      fileURI,
					MediaType: mimeType,
				},
			},
		}
	}
}

// parseGeminiSystemInstruction converts Gemini's systemInstruction.parts to IR System.
func parseGeminiSystemInstruction(raw json.RawMessage) (*SystemPrompt, error) {
	var si struct {
		Parts []struct {
			Text string `json:"text"`
		} `json:"parts"`
	}
	if err := json.Unmarshal(raw, &si); err != nil {
		return nil, fmt.Errorf("unmarshal systemInstruction: %w", err)
	}
	if len(si.Parts) == 0 {
		return nil, nil
	}
	sys := &SystemPrompt{}
	for _, p := range si.Parts {
		if p.Text != "" {
			sys.Parts = append(sys.Parts, ContentBlock{Type: "text", Text: p.Text})
		}
	}
	return sys, nil
}

// parseGeminiTools converts Gemini `tools[].functionDeclarations[]` to IR Tools.
// Gemini has no `tools[].type:"function"` wrapper — declarations sit directly under tools[].
func parseGeminiTools(raw json.RawMessage) ([]ToolDefinition, error) {
	var toolsArr []struct {
		FunctionDeclarations []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"functionDeclarations"`
	}
	if err := json.Unmarshal(raw, &toolsArr); err != nil {
		return nil, fmt.Errorf("unmarshal tools: %w", err)
	}

	out := make([]ToolDefinition, 0)
	for _, t := range toolsArr {
		for _, fd := range t.FunctionDeclarations {
			out = append(out, ToolDefinition{
				Name:        fd.Name,
				Description: fd.Description,
				Parameters:  fd.Parameters,
			})
		}
	}
	return out, nil
}

// parseGeminiToolConfig converts Gemini `toolConfig.functionCallingConfig`
// to IR ToolChoice. Mode mapping:
//   - "AUTO"  → "auto"
//   - "ANY"   → "any" (Anthropic) / "required" (OpenAI)
//   - "NONE"  → "none"
func parseGeminiToolConfig(raw json.RawMessage) (*ToolChoice, error) {
	var tc struct {
		FunctionCallingConfig struct {
			Mode                 string   `json:"mode"`
			AllowedFunctionNames []string `json:"allowedFunctionNames"`
		} `json:"functionCallingConfig"`
	}
	if err := json.Unmarshal(raw, &tc); err != nil {
		return nil, fmt.Errorf("unmarshal toolConfig: %w", err)
	}

	out := &ToolChoice{}
	switch tc.FunctionCallingConfig.Mode {
	case "AUTO":
		out.Type = "auto"
	case "ANY":
		out.Type = "any"
	case "NONE":
		out.Type = "none"
	default:
		out.Type = "auto"
	}

	// If allowedFunctionNames has exactly one entry, force that specific tool.
	if len(tc.FunctionCallingConfig.AllowedFunctionNames) == 1 {
		out.Type = "tool"
		out.Name = tc.FunctionCallingConfig.AllowedFunctionNames[0]
	}

	return out, nil
}

// parseGeminiGenerationConfig populates IR sampling params from Gemini's
// generationConfig block.
func parseGeminiGenerationConfig(raw json.RawMessage, ir *InternalRequest) error {
	var gc struct {
		Temperature      *float64              `json:"temperature,omitempty"`
		TopP             *float64              `json:"topP,omitempty"`
		TopK             *int                  `json:"topK,omitempty"`
		MaxOutputTokens  *int                  `json:"maxOutputTokens,omitempty"`
		StopSequences    []string              `json:"stopSequences,omitempty"`
		ResponseMimeType string                `json:"responseMimeType,omitempty"`
		ResponseSchema   json.RawMessage       `json:"responseSchema,omitempty"`
		CandidateCount   *int                  `json:"candidateCount,omitempty"`
		Seed             *int64                `json:"seed,omitempty"`
		ThinkingConfig   *GeminiThinkingConfig `json:"thinkingConfig,omitempty"`
	}
	if err := json.Unmarshal(raw, &gc); err != nil {
		return err
	}

	ir.Temperature = gc.Temperature
	ir.TopP = gc.TopP
	ir.TopK = gc.TopK
	if gc.MaxOutputTokens != nil {
		ir.MaxTokens = *gc.MaxOutputTokens
	}
	if len(gc.StopSequences) > 0 {
		ir.Stop = gc.StopSequences
	}
	if gc.Seed != nil {
		ir.Seed = gc.Seed
	}
	if gc.CandidateCount != nil {
		ir.N = *gc.CandidateCount
	}

	// Map Gemini responseMimeType/Schema to IR ResponseFormat
	if gc.ResponseMimeType == "application/json" {
		rf := &ResponseFormat{Type: "json_object"}
		if gc.ResponseSchema != nil {
			rf.Schema = gc.ResponseSchema
			rf.Type = "json_schema"
		}
		ir.ResponseFormat = rf
	}

	// Map Gemini thinkingConfig to IR ReasoningConfig
	if gc.ThinkingConfig != nil && gc.ThinkingConfig.ThinkingBudget != nil {
		ir.Reasoning = &ReasoningConfig{
			Type:         "enabled",
			BudgetTokens: gc.ThinkingConfig.ThinkingBudget,
		}
	}

	return nil
}
