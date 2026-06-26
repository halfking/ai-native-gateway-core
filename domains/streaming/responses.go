package streaming

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
)

type responsesRequestBody struct {
	Model           string                     `json:"model"`
	Input           json.RawMessage            `json:"input"`
	Instructions    string                     `json:"instructions,omitempty"`
	MaxOutputTokens *int                       `json:"max_output_tokens,omitempty"`
	Stream          bool                       `json:"stream"`
	Temperature     *float64                   `json:"temperature,omitempty"`
	TopP            *float64                   `json:"top_p,omitempty"`
	Extra           map[string]json.RawMessage `json:"-"`
}

func (r *responsesRequestBody) UnmarshalJSON(data []byte) error {
	type alias responsesRequestBody
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	delete(raw, "model")
	delete(raw, "input")
	delete(raw, "instructions")
	delete(raw, "max_output_tokens")
	delete(raw, "stream")
	delete(raw, "temperature")
	delete(raw, "top_p")
	*r = responsesRequestBody(decoded)
	r.Extra = raw
	return nil
}

type ResponsesHandler struct {
	chatHandler *ChatHandler
}

func NewResponsesHandler(ch *ChatHandler) *ResponsesHandler {
	return &ResponsesHandler{chatHandler: ch}
}

func convertResponsesToChatBody(req *responsesRequestBody) map[string]any {
	chatBody := map[string]any{
		"model":  req.Model,
		"stream": req.Stream,
	}

	var messages []any
	if req.Instructions != "" {
		messages = append(messages, map[string]any{"role": "system", "content": req.Instructions})
	}

	var rawInput json.RawMessage = req.Input
	if len(rawInput) > 0 {
		if rawInput[0] == '"' {
			var s string
			if json.Unmarshal(rawInput, &s) == nil {
				messages = append(messages, map[string]any{"role": "user", "content": s})
			}
		} else if rawInput[0] == '[' {
			var items []map[string]any
			if json.Unmarshal(rawInput, &items) == nil {
				for _, item := range items {
					role, _ := item["role"].(string)
					if role == "" {
						role = "user"
					}
					content := item["content"]
					if content == nil {
						content = ""
					}
					messages = append(messages, map[string]any{"role": role, "content": content})
				}
			}
		}
	}
	chatBody["messages"] = messages

	if req.MaxOutputTokens != nil {
		chatBody["max_tokens"] = *req.MaxOutputTokens
	}
	if req.Temperature != nil {
		chatBody["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		chatBody["top_p"] = *req.TopP
	}

	for key, raw := range req.Extra {
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			continue
		}
		if key == "tools" {
			value = normalizeResponsesTools(value)
		}
		chatBody[key] = value
	}

	return chatBody
}

func normalizeResponsesTools(value any) any {
	tools, ok := value.([]any)
	if !ok {
		return value
	}

	normalized := make([]any, 0, len(tools))
	for _, item := range tools {
		tool, ok := item.(map[string]any)
		if !ok {
			normalized = append(normalized, item)
			continue
		}
		if _, exists := tool["function"]; exists {
			normalized = append(normalized, tool)
			continue
		}
		if toolType, _ := tool["type"].(string); toolType != "function" {
			normalized = append(normalized, tool)
			continue
		}

		function := map[string]any{}
		for _, key := range []string{"name", "description", "parameters", "strict"} {
			if val, exists := tool[key]; exists {
				function[key] = val
				delete(tool, key)
			}
		}
		if len(function) == 0 {
			normalized = append(normalized, tool)
			continue
		}
		tool["function"] = function
		normalized = append(normalized, tool)
	}

	return normalized
}

func (h *ResponsesHandler) writeNonStreamResponse(w http.ResponseWriter, body []byte, clientModel, requestID string) []byte {
	if len(body) == 0 {
		writeResponsesError(w, http.StatusInternalServerError, "Failed to read upstream response", "server_error", "upstream_read_error")
		return nil
	}

	respBody := convertChatResponseToResponses(body, clientModel, requestID)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-Id", requestID)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(respBody)
	return respBody
}

func convertChatResponseToResponses(body []byte, clientModel, requestID string) []byte {
	var chatResp map[string]json.RawMessage
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return body
	}

	var choices []map[string]any
	if raw, ok := chatResp["choices"]; ok {
		_ = json.Unmarshal(raw, &choices)
	}

	finishReason := "stop"
	textContent := ""
	reasoningContent := ""
	var toolCalls []map[string]any

	if len(choices) > 0 {
		choice := choices[0]
		if fr, ok := choice["finish_reason"].(string); ok {
			finishReason = fr
		}
		if msg, ok := choice["message"].(map[string]any); ok {
			if c, ok := msg["content"].(string); ok {
				textContent = c
			}
			if rc, ok := msg["reasoning_content"].(string); ok {
				reasoningContent = rc
			}
			if tc, ok := msg["tool_calls"].([]any); ok {
				for _, call := range tc {
					toolCall, ok := call.(map[string]any)
					if ok {
						toolCalls = append(toolCalls, toolCall)
					}
				}
			}
		}
	}

	status := "completed"
	if finishReason == "length" {
		status = "incomplete"
	}

	inputTokens, outputTokens, totalTokens := 0, 0, 0
	if raw, ok := chatResp["usage"]; ok {
		var usage map[string]any
		if json.Unmarshal(raw, &usage) == nil {
			if v, ok := usage["prompt_tokens"].(float64); ok {
				inputTokens = int(v)
			}
			if v, ok := usage["completion_tokens"].(float64); ok {
				outputTokens = int(v)
			}
			if v, ok := usage["total_tokens"].(float64); ok {
				totalTokens = int(v)
			}
		}
	}

	created := int(time.Now().Unix())
	if raw, ok := chatResp["created"]; ok {
		var v float64
		if json.Unmarshal(raw, &v) == nil {
			created = int(v)
		}
	}

	respID := "resp_"
	msgID := "msg_"
	if len(requestID) > 24 {
		respID += requestID[:24]
		msgID += requestID[8:24]
	} else {
		respID += requestID
		msgID += requestID
	}

	output := make([]map[string]any, 0, 2)
	if reasoningContent != "" {
		output = append(output, map[string]any{
			"type": "reasoning",
			"id":   msgID + "_reasoning",
			"summary": []map[string]any{{
				"type": "summary_text",
				"text": reasoningContent,
			}},
		})
	}
	if len(toolCalls) > 0 {
		for index, call := range toolCalls {
			function, _ := call["function"].(map[string]any)
			name, _ := function["name"].(string)
			arguments, _ := function["arguments"].(string)
			callID, _ := call["id"].(string)
			output = append(output, map[string]any{
				"type":      "function_call",
				"id":        fmt.Sprintf("%s_fc_%d", msgID, index),
				"call_id":   callID,
				"name":      name,
				"arguments": arguments,
				"status":    "completed",
			})
		}
	} else {
		output = append(output, map[string]any{
			"type":   "message",
			"id":     msgID,
			"status": status,
			"role":   "assistant",
			"content": []map[string]any{{
				"type":        "output_text",
				"text":        textContent,
				"annotations": []any{},
			}},
		})
	}

	resp := map[string]any{
		"id":         respID,
		"object":     "response",
		"created_at": created,
		"model":      clientModel,
		"status":     status,
		"output":     output,
		"usage": map[string]any{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
			"total_tokens":  totalTokens,
		},
		"x_request_id": requestID,
	}

	result, err := json.Marshal(resp)
	if err != nil {
		return body
	}
	return result
}

func writeResponsesError(w http.ResponseWriter, statusCode int, message, errType, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errType,
			"param":   "model",
			"code":    code,
		},
	})
}

func responsesStreamWrapper(requestID, clientModel, outboundModel string, capture *audit.StreamCapture) executors.StreamWrapperFunc {
	return func(w http.ResponseWriter, resp *http.Response, norm executors.NormalizerFunc, cap *audit.StreamCapture) executors.StreamOutcome {
		c := cap
		if c == nil {
			c = capture
		}
		_ = norm
		return StreamResponsesSSE(w, resp, clientModel, outboundModel, requestID, c)
	}
}
