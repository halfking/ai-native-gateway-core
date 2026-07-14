package ir

import (
	"encoding/json"
	"fmt"
)

// SerializeGemini serializes an InternalRequest into a Gemini generateContent request body.
//
// Shape:
//
//	{
//	  "contents": [{"role":"user","parts":[{"text":"..."}]}],
//	  "systemInstruction": {"parts":[{"text":"..."}]},
//	  "tools": [{"functionDeclarations":[...]}],
//	  "toolConfig": {"functionCallingConfig":{"mode":"AUTO"}},
//	  "generationConfig": {...},
//	  "safetySettings": [...],
//	  "cachedContent": "..."
//	}
//
// audit-gemini-adapter (2026-07-13): Third outbound protocol serializer, completes
// the O(N) protocol matrix (OpenAI, Anthropic, Gemini).
func SerializeGemini(req *InternalRequest) ([]byte, error) {
	if req == nil {
		return nil, fmt.Errorf("request is nil")
	}

	out := map[string]any{}

	// System instruction
	if req.System != nil {
		si := buildGeminiSystemInstruction(req.System)
		if si != nil {
			out["systemInstruction"] = si
		}
	}

	// Contents (messages)
	contents := buildGeminiContents(req.Messages)
	if len(contents) > 0 {
		out["contents"] = contents
	}

	// Tools (function declarations)
	if len(req.Tools) > 0 {
		out["tools"] = buildGeminiTools(req.Tools)
	}

	// Tool config
	if req.ToolChoice != nil {
		if tc := buildGeminiToolConfig(req.ToolChoice); tc != nil {
			out["toolConfig"] = tc
		}
	}

	// Generation config (combines IR sampling params + Gemini-specific fields)
	if gc := buildGeminiGenerationConfig(req); gc != nil {
		out["generationConfig"] = gc
	}

	return json.Marshal(out)
}

// buildGeminiSystemInstruction converts IR System → Gemini systemInstruction.
func buildGeminiSystemInstruction(sys *SystemPrompt) map[string]any {
	if sys == nil {
		return nil
	}
	parts := make([]map[string]any, 0)

	if len(sys.Parts) > 0 {
		for _, p := range sys.Parts {
			if p.Text != "" {
				parts = append(parts, map[string]any{"text": p.Text})
			}
		}
	} else if sys.Content != "" {
		parts = append(parts, map[string]any{"text": sys.Content})
	}

	if len(parts) == 0 {
		return nil
	}
	return map[string]any{"parts": parts}
}

// buildGeminiContents converts IR Messages → Gemini contents[].
// Maps role: "assistant" → "model", "tool" → "function".
func buildGeminiContents(messages []Message) []map[string]any {
	out := make([]map[string]any, 0, len(messages))

	for _, msg := range messages {
		role := msg.Role
		switch role {
		case "assistant":
			role = "model"
		case "tool":
			role = "function"
		case "system":
			// System messages are conveyed via systemInstruction, skip here
			continue
		}

		parts := make([]map[string]any, 0)

		// OpenAI Chat/Responses input may normalize tool calls into the
		// message-level ToolCalls field. Preserve those calls when the same IR
		// is sent to native Gemini instead of relying only on Anthropic blocks.
		for _, call := range msg.ToolCalls {
			name := call.Function.Name
			if name == "" {
				continue
			}
			args := map[string]any{}
			if call.Function.Arguments != "" {
				if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
					continue
				}
			}
			parts = append(parts, map[string]any{
				"functionCall": map[string]any{
					"name": name,
					"args": args,
				},
			})
		}

		// Chat Completions tool messages carry their result outside content.
		// Gemini requires the corresponding functionResponse part.
		hasToolResultBlock := false
		for _, block := range msg.Content {
			if block.Type == "tool_result" && block.ToolResult != nil {
				hasToolResultBlock = true
				break
			}
		}
		if !hasToolResultBlock && (msg.Role == "function" || (msg.Role == "tool" && msg.ToolCallID != "")) {
			name := msg.Name
			if name == "" {
				name = toolUseNameFromID(msg.ToolCallID)
			}
			if name != "" {
				parts = append(parts, map[string]any{
					"functionResponse": map[string]any{
						"name":     name,
						"response": map[string]any{"result": extractTextFromContent(msg.Content)},
					},
				})
			}
		}

		// Convert each ContentBlock
		for _, block := range msg.Content {
			switch block.Type {
			case "text":
				if block.Text != "" {
					parts = append(parts, map[string]any{"text": block.Text})
				}
			case "image":
				if block.Image != nil {
					parts = append(parts, irImageToGeminiPart(block.Image))
				}
			case "audio":
				if block.Audio != nil {
					parts = append(parts, irAudioToGeminiPart(block.Audio))
				}
			case "video":
				if block.Video != nil {
					parts = append(parts, irVideoToGeminiPart(block.Video))
				}
			case "document":
				if block.Document != nil && block.Document.Source != nil {
					parts = append(parts, irDocumentToGeminiPart(block.Document))
				}
			case "tool_use":
				if block.ToolUse != nil {
					parts = append(parts, map[string]any{
						"functionCall": map[string]any{
							"name": block.ToolUse.Name,
							"args": block.ToolUse.Input,
						},
					})
				}
			case "tool_result":
				if block.ToolResult != nil {
					respText := extractTextFromContent(block.ToolResult.Content)
					parts = append(parts, map[string]any{
						"functionResponse": map[string]any{
							"name":     toolUseNameFromID(block.ToolResult.ToolUseID),
							"response": map[string]any{"result": respText},
						},
					})
				}
			case "thinking":
				// Gemini 2.5+ thinking part (with includeThoughts=true)
				if block.Thinking != nil {
					parts = append(parts, map[string]any{
						"thought": block.Thinking.Thinking,
					})
				}
			}
		}

		if len(parts) == 0 {
			continue
		}
		out = append(out, map[string]any{
			"role":  role,
			"parts": parts,
		})
	}
	return out
}

func irImageToGeminiPart(img *ImageSource) map[string]any {
	if img.Type == "base64" || img.Data != "" {
		return map[string]any{
			"inlineData": map[string]any{
				"mimeType": imageMIME(img),
				"data":     img.Data,
			},
		}
	}
	if img.Type == "file_uri" || img.URL != "" {
		uri := img.URL
		if img.FileURI != "" {
			uri = img.FileURI
		}
		return map[string]any{
			"fileData": map[string]any{
				"mimeType": imageMIME(img),
				"fileUri":  uri,
			},
		}
	}
	// Plain URL
	return map[string]any{
		"fileData": map[string]any{
			"mimeType": imageMIME(img),
			"fileUri":  img.URL,
		},
	}
}

func irAudioToGeminiPart(audio *MediaSource) map[string]any {
	mt := audio.MediaType
	if mt == "" {
		mt = "audio/" + audio.Format
	}
	if audio.Type == "base64" || audio.Data != "" {
		return map[string]any{
			"inlineData": map[string]any{
				"mimeType": mt,
				"data":     audio.Data,
			},
		}
	}
	uri := audio.FileURI
	if uri == "" {
		uri = audio.URL
	}
	return map[string]any{
		"fileData": map[string]any{
			"mimeType": mt,
			"fileUri":  uri,
		},
	}
}

func irVideoToGeminiPart(video *MediaSource) map[string]any {
	mt := video.MediaType
	if mt == "" {
		mt = "video/" + video.Format
	}
	if video.Type == "base64" || video.Data != "" {
		return map[string]any{
			"inlineData": map[string]any{
				"mimeType": mt,
				"data":     video.Data,
			},
		}
	}
	uri := video.FileURI
	if uri == "" {
		uri = video.URL
	}
	return map[string]any{
		"fileData": map[string]any{
			"mimeType": mt,
			"fileUri":  uri,
		},
	}
}

func irDocumentToGeminiPart(doc *DocumentBlock) map[string]any {
	mt := doc.MIMEType
	if doc.Source != nil && doc.Source.MediaType != "" {
		mt = doc.Source.MediaType
	}
	src := doc.Source
	switch src.Type {
	case "base64":
		return map[string]any{
			"inlineData": map[string]any{
				"mimeType": mt,
				"data":     src.Data,
			},
		}
	default:
		uri := src.Data
		if src.URL != "" {
			uri = src.URL
		}
		return map[string]any{
			"fileData": map[string]any{
				"mimeType": mt,
				"fileUri":  uri,
			},
		}
	}
}

func imageMIME(img *ImageSource) string {
	if img.MediaType != "" {
		return img.MediaType
	}
	return "image/png"
}

// extractTextFromContent concatenates text blocks into a single string.
func extractTextFromContent(blocks []ContentBlock) string {
	out := ""
	for _, b := range blocks {
		if b.Type == "text" {
			if out != "" {
				out += "\n"
			}
			out += b.Text
		}
	}
	return out
}

// toolUseNameFromID extracts the function name from a synthetic tool_use_id
// of the form "gemini_call_<name>". Falls back to the full ID.
func toolUseNameFromID(id string) string {
	const prefix = "gemini_call_"
	if len(id) > len(prefix) && id[:len(prefix)] == prefix {
		return id[len(prefix):]
	}
	return id
}

// buildGeminiTools converts IR Tools → Gemini functionDeclarations wrapper.
func buildGeminiTools(tools []ToolDefinition) []map[string]any {
	decls := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		decl := map[string]any{
			"name":        t.Name,
			"description": t.Description,
		}
		if t.Parameters != nil {
			decl["parameters"] = t.Parameters
		}
		decls = append(decls, decl)
	}
	return []map[string]any{
		{"functionDeclarations": decls},
	}
}

// buildGeminiToolConfig converts IR ToolChoice → Gemini functionCallingConfig.
func buildGeminiToolChoice(tc *ToolChoice) map[string]any {
	if tc == nil {
		return nil
	}
	cfg := map[string]any{}
	switch tc.Type {
	case "auto":
		cfg["mode"] = "AUTO"
	case "any", "required":
		cfg["mode"] = "ANY"
	case "none":
		cfg["mode"] = "NONE"
	case "tool":
		cfg["mode"] = "ANY"
		if tc.Name != "" {
			cfg["allowedFunctionNames"] = []string{tc.Name}
		}
	default:
		cfg["mode"] = "AUTO"
	}
	return cfg
}

// buildGeminiToolConfig wraps ToolChoice in the full toolConfig structure.
func buildGeminiToolConfig(tc *ToolChoice) map[string]any {
	inner := buildGeminiToolChoice(tc)
	if inner == nil {
		return nil
	}
	return map[string]any{
		"functionCallingConfig": inner,
	}
}

// buildGeminiGenerationConfig merges IR sampling params into Gemini's
// generationConfig block.
func buildGeminiGenerationConfig(req *InternalRequest) map[string]any {
	gc := map[string]any{}
	hasAny := false

	if req.Temperature != nil {
		gc["temperature"] = *req.Temperature
		hasAny = true
	}
	if req.TopP != nil {
		gc["topP"] = *req.TopP
		hasAny = true
	}
	if req.TopK != nil {
		gc["topK"] = *req.TopK
		hasAny = true
	}
	if req.MaxTokens > 0 {
		gc["maxOutputTokens"] = req.MaxTokens
		hasAny = true
	}
	if len(req.Stop) > 0 {
		gc["stopSequences"] = req.Stop
		hasAny = true
	}
	if req.Seed != nil {
		gc["seed"] = *req.Seed
		hasAny = true
	}
	if req.N > 0 {
		gc["candidateCount"] = req.N
		hasAny = true
	}

	// Map response_format → responseMimeType/Schema
	if req.ResponseFormat != nil {
		switch req.ResponseFormat.Type {
		case "json_object":
			gc["responseMimeType"] = "application/json"
			hasAny = true
		case "json_schema":
			gc["responseMimeType"] = "application/json"
			if req.ResponseFormat.Schema != nil {
				gc["responseSchema"] = req.ResponseFormat.Schema
			}
			hasAny = true
		}
	}

	// Map ReasoningConfig → Gemini thinkingConfig
	if req.Reasoning != nil && req.Reasoning.BudgetTokens != nil {
		gc["thinkingConfig"] = map[string]any{
			"thinkingBudget":  *req.Reasoning.BudgetTokens,
			"includeThoughts": true,
		}
		hasAny = true
	}

	if !hasAny {
		return nil
	}
	return gc
}
