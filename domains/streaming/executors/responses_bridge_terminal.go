package executors

import (
	"encoding/json"
	"errors"
)

// fail emits the existing Chat in-band error shape and records a read failure.
// Consumers must never receive a successful finish_reason or [DONE] for it.
func (t *responsesToChatTranslator) fail(payload []byte, cause error) []byte {
	t.done = true
	if cause == nil {
		cause = errors.New("upstream Responses stream failed")
	}
	t.err = cause
	var event struct {
		Type, Code, Message string
		Error               json.RawMessage
		Response            struct{ Error json.RawMessage }
	}
	_ = json.Unmarshal(payload, &event)
	raw := event.Error
	if len(raw) == 0 || string(raw) == "null" {
		raw = event.Response.Error
	}
	var detail struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &detail)
	if detail.Code == "" {
		detail.Code = event.Code
	}
	if detail.Message == "" {
		detail.Message = event.Message
	}
	if detail.Type == "" || detail.Type == "error" {
		detail.Type = "upstream_error"
	}
	if detail.Message == "" {
		detail.Message = cause.Error()
	}
	out, _ := json.Marshal(map[string]any{"error": detail})
	return append(append([]byte("data: "), out...), '\n', '\n')
}

func (t *responsesToChatTranslator) terminal(eventType string, payload []byte) []byte {
	var event struct {
		Response *struct {
			Status     string          `json:"status"`
			Error      json.RawMessage `json:"error"`
			Incomplete struct {
				Reason string `json:"reason"`
			} `json:"incomplete_details"`
			Tokens struct {
				Input  int `json:"input_tokens"`
				Output int `json:"output_tokens"`
				Total  int `json:"total_tokens"`
			} `json:"usage"`
		} `json:"response"`
	}
	if json.Unmarshal(payload, &event) != nil || event.Response == nil {
		return t.fail(payload, errors.New("invalid Responses terminal"))
	}
	response := event.Response
	if eventType == "response.failed" || response.Status == "failed" || (len(response.Error) > 0 && string(response.Error) != "null") {
		return t.fail(payload, nil)
	}
	reason := "stop"
	if t.toolCallSeen {
		reason = "tool_calls"
	}
	if eventType == "response.incomplete" || response.Status == "incomplete" {
		switch response.Incomplete.Reason {
		case "max_output_tokens":
			reason = "length"
		case "content_filter":
			reason = "content_filter"
		default:
			return t.fail(payload, errors.New("unrecognized Responses incomplete reason"))
		}
	} else if response.Status != "" && response.Status != "completed" {
		return t.fail(payload, errors.New("invalid Responses terminal status"))
	}
	usage := &chatUsage{PromptTokens: response.Tokens.Input, CompletionTokens: response.Tokens.Output, TotalTokens: response.Tokens.Total}
	t.done = true
	return append(t.frameUsage(chatDelta{}, strPtr(reason), usage), []byte("data: [DONE]\n\n")...)
}
