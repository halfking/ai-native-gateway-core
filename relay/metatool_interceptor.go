package relay

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kaixuan/llm-gateway-go/metatools"
)

// MetaToolInterceptor handles meta-tool calls (list_categories, load_tools)
// without forwarding to upstream LLM providers.
type MetaToolInterceptor struct {
	handler *metatools.Handler
}

// NewMetaToolInterceptor creates a new meta-tool interceptor.
func NewMetaToolInterceptor(handler *metatools.Handler) *MetaToolInterceptor {
	return &MetaToolInterceptor{handler: handler}
}

// InterceptRequest checks if the request contains meta-tool responses that
// should be handled locally. Returns (modified body, intercepted, error).
//
// If intercepted=true, the request was handled locally and should not be
// forwarded to upstream LLM.
func (i *MetaToolInterceptor) InterceptRequest(ctx context.Context, body []byte) ([]byte, bool, error) {
	if i.handler == nil {
		return body, false, nil
	}

	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return body, false, nil
	}

	messages, ok := req["messages"].([]interface{})
	if !ok || len(messages) == 0 {
		return body, false, nil
	}

	// Check last message for meta-tool calls
	lastMsg, ok := messages[len(messages)-1].(map[string]interface{})
	if !ok {
		return body, false, nil
	}

	role, _ := lastMsg["role"].(string)
	if role != "assistant" {
		return body, false, nil
	}

	// Check for tool_calls
	toolCalls, ok := lastMsg["tool_calls"].([]interface{})
	if !ok || len(toolCalls) == 0 {
		return body, false, nil
	}

	// Process each tool call
	intercepted := false
	for _, tc := range toolCalls {
		toolCall, ok := tc.(map[string]interface{})
		if !ok {
			continue
		}

		fn, ok := toolCall["function"].(map[string]interface{})
		if !ok {
			continue
		}

		name, _ := fn["name"].(string)
		argsStr, _ := fn["arguments"].(string)

		var result string
		var err error

		switch name {
		case "list_categories":
			result, err = i.handleListCategories(ctx)
			intercepted = true

		case "load_tools":
			result, err = i.handleLoadTools(ctx, argsStr)
			intercepted = true

		default:
			continue
		}

		if err != nil {
			return body, false, fmt.Errorf("meta-tool %s failed: %w", name, err)
		}

		// Replace the assistant message with tool response
		toolCallID, _ := toolCall["id"].(string)
		toolResponse := map[string]interface{}{
			"role":         "tool",
			"tool_call_id": toolCallID,
			"content":      result,
		}

		// Replace last message with tool response
		messages[len(messages)-1] = toolResponse
	}

	if !intercepted {
		return body, false, nil
	}

	// Rebuild request body
	req["messages"] = messages
	modified, err := json.Marshal(req)
	if err != nil {
		return body, false, fmt.Errorf("rebuild request: %w", err)
	}

	return modified, true, nil
}

func (i *MetaToolInterceptor) handleListCategories(ctx context.Context) (string, error) {
	categories, err := i.handler.ListCategories(ctx)
	if err != nil {
		return "", err
	}

	result := map[string]interface{}{
		"categories": categories,
	}

	data, err := json.Marshal(result)
	if err != nil {
		return "", err
	}

	return string(data), nil
}

func (i *MetaToolInterceptor) handleLoadTools(ctx context.Context, argsJSON string) (string, error) {
	var args metatools.LoadToolsArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse arguments: %w", err)
	}

	result, err := i.handler.LoadTools(ctx, args.Categories)
	if err != nil {
		return "", err
	}

	data, err := json.Marshal(result)
	if err != nil {
		return "", err
	}

	return string(data), nil
}
