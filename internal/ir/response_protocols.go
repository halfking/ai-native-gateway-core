package ir

import (
	"encoding/json"
	"fmt"
)

// ParseGeminiResponse parses a Gemini generateContent response into IR.
func ParseGeminiResponse(body []byte) (*InternalResponse, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("empty gemini response body")
	}

	var src struct {
		Candidates []struct {
			Content struct {
				Role  string `json:"role"`
				Parts []struct {
					Text         string          `json:"text"`
					Thought      string          `json:"thought"`
					FunctionCall json.RawMessage `json:"functionCall"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
			TotalTokenCount      int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
		ModelVersion string `json:"modelVersion"`
	}
	if err := json.Unmarshal(body, &src); err != nil {
		return nil, fmt.Errorf("unmarshal gemini response: %w", err)
	}

	resp := &InternalResponse{
		Model:          src.ModelVersion,
		SourceProtocol: ProtocolGeminiGenerate,
		Usage: ResponseUsage{
			PromptTokens:     src.UsageMetadata.PromptTokenCount,
			CompletionTokens: src.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      src.UsageMetadata.TotalTokenCount,
		},
	}
	if len(src.Candidates) == 0 {
		return resp, nil
	}

	candidate := src.Candidates[0]
	resp.Role = candidate.Content.Role
	if resp.Role == "model" {
		resp.Role = "assistant"
	}
	resp.FinishReason = mapGeminiFinishReason(candidate.FinishReason)
	for index, part := range candidate.Content.Parts {
		switch {
		case part.Text != "":
			resp.Content = append(resp.Content, ResponseContentBlock{Type: "text", Text: part.Text})
		case part.Thought != "":
			resp.Content = append(resp.Content, ResponseContentBlock{Type: "thinking", Thinking: part.Thought})
			resp.ReasoningContent += part.Thought
		case len(part.FunctionCall) > 0 && string(part.FunctionCall) != "null":
			var call struct {
				Name string          `json:"name"`
				Args json.RawMessage `json:"args"`
			}
			if err := json.Unmarshal(part.FunctionCall, &call); err != nil {
				return nil, fmt.Errorf("parse gemini candidate functionCall[%d]: %w", index, err)
			}
			if call.Name == "" {
				return nil, fmt.Errorf("parse gemini candidate functionCall[%d]: missing name", index)
			}
			id := fmt.Sprintf("gemini_call_%d_%s", index, call.Name)
			resp.Content = append(resp.Content, ResponseContentBlock{Type: "tool_use", ID: id, Name: call.Name, Input: call.Args})
			resp.ToolCalls = append(resp.ToolCalls, ResponseToolCall{ID: id, Name: call.Name, Arguments: string(call.Args), InputRaw: append(json.RawMessage(nil), call.Args...)})
		}
	}
	return resp, nil
}

// ParseResponsesResponse parses an OpenAI Responses API response into IR.
func ParseResponsesResponse(body []byte) (*InternalResponse, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("empty responses response body")
	}

	var src struct {
		ID                string `json:"id"`
		CreatedAt         int64  `json:"created_at"`
		Model             string `json:"model"`
		Status            string `json:"status"`
		IncompleteDetails struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Output []struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`
			Role      string          `json:"role"`
			Name      string          `json:"name"`
			CallID    string          `json:"call_id"`
			Arguments string          `json:"arguments"`
			Content   json.RawMessage `json:"content"`
			Summary   json.RawMessage `json:"summary"`
		} `json:"output"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &src); err != nil {
		return nil, fmt.Errorf("unmarshal responses response: %w", err)
	}
	if src.Status == "failed" {
		return nil, fmt.Errorf("responses response failed")
	}

	resp := &InternalResponse{
		ID:             src.ID,
		Model:          src.Model,
		Created:        src.CreatedAt,
		SourceProtocol: ProtocolOpenAIResponses,
		FinishReason:   mapResponsesStatus(src.Status, src.IncompleteDetails.Reason),
		Usage: ResponseUsage{
			PromptTokens:     src.Usage.InputTokens,
			CompletionTokens: src.Usage.OutputTokens,
			TotalTokens:      src.Usage.TotalTokens,
		},
	}
	for index, item := range src.Output {
		switch item.Type {
		case "message":
			resp.Role = item.Role
			if err := appendResponsesMessageContent(resp, item.Content); err != nil {
				return nil, fmt.Errorf("parse responses output[%d] message: %w", index, err)
			}
		case "function_call":
			if item.Name == "" || item.CallID == "" {
				return nil, fmt.Errorf("parse responses output[%d] function_call: missing name or call_id", index)
			}
			arguments := json.RawMessage(item.Arguments)
			resp.Content = append(resp.Content, ResponseContentBlock{Type: "tool_use", ID: item.CallID, Name: item.Name, Input: arguments})
			resp.ToolCalls = append(resp.ToolCalls, ResponseToolCall{ID: item.CallID, Name: item.Name, Arguments: item.Arguments, InputRaw: arguments})
		case "reasoning":
			text, err := responsesSummaryText(item.Summary)
			if err != nil {
				return nil, fmt.Errorf("parse responses output[%d] reasoning: %w", index, err)
			}
			if text != "" {
				resp.Content = append(resp.Content, ResponseContentBlock{Type: "thinking", Thinking: text})
				resp.ReasoningContent += text
			}
		}
	}
	return resp, nil
}

func appendResponsesMessageContent(resp *InternalResponse, raw json.RawMessage) error {
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return err
	}
	for _, block := range blocks {
		if block.Type == "output_text" || block.Type == "text" {
			resp.Content = append(resp.Content, ResponseContentBlock{Type: "text", Text: block.Text})
		}
	}
	return nil
}

func responsesSummaryText(raw json.RawMessage) (string, error) {
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", err
	}
	text := ""
	for _, block := range blocks {
		if block.Type == "summary_text" {
			text += block.Text
		}
	}
	return text, nil
}

func mapResponsesStatus(status, incompleteReason string) string {
	if status == "incomplete" {
		if incompleteReason == "content_filter" {
			return "content_filter"
		}
		return "length"
	}
	return "stop"
}
