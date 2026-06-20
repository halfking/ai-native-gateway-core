package relay

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kaixuan/llm-gateway-go/metatools"
)

// MetaToolInterceptor transparently expands the `tools` array when a
// client declares only the two meta-tools (list_categories, load_tools).
// The original implementation tried to detect meta-tool invocations on
// the assistant side of the conversation, which is unreachable: clients
// never send assistant messages with tool_calls. The correct interception
// point is the *request's tools array* — when the client opts into the
// meta-tool protocol we load the full tool registry and replace the
// meta-tools with the concrete ones, then forward the expanded request
// to the upstream LLM in a single round-trip.
//
// Workflow:
//
//  1. Client sends initial request with only `tools: [list_categories,
//     load_tools]` (~2KB).
//  2. MetaToolInterceptor detects the meta-tools in `tools`.
//  3. It loads all enabled tools from the DB-backed tool_registry.
//  4. It replaces the meta-tools with the full set (keeping any other
//     non-meta-tool declarations the client may have made).
//  5. The expanded request is forwarded to upstream; the LLM sees all
//     concrete tools in a single pass.
//
// This achieves the original design goal (96% smaller initial requests)
// without the unreachable "intercept assistant tool_calls" logic.
type MetaToolInterceptor struct {
	handler *metatools.Handler
}

// NewMetaToolInterceptor creates a new meta-tool interceptor.
func NewMetaToolInterceptor(handler *metatools.Handler) *MetaToolInterceptor {
	return &MetaToolInterceptor{handler: handler}
}

// MetaToolNames lists the canonical names of the meta-tools. Exported
// so tests (and other callers) can probe the set without duplicating
// string literals.
var MetaToolNames = map[string]bool{
	"list_categories": true,
	"load_tools":      true,
}

// isMetaTool reports whether the given tool name is a meta-tool.
func isMetaTool(name string) bool {
	return MetaToolNames[name]
}

// InterceptRequest detects meta-tools in the request's `tools` field and,
// if present, expands them with the full tool set loaded from the
// tool_registry. Returns:
//
//   - (modified, true, nil)   — meta-tools replaced with the full set
//   - (original, false, nil)  — no meta-tools, body unchanged
//   - (original, false, err)  — DB error; caller should treat as a
//                               passthrough so a metatool outage cannot
//                               break real requests
func (i *MetaToolInterceptor) InterceptRequest(ctx context.Context, body []byte) ([]byte, bool, error) {
	if i.handler == nil {
		return body, false, nil
	}

	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return body, false, nil
	}

	toolsRaw, ok := req["tools"].([]interface{})
	if !ok || len(toolsRaw) == 0 {
		return body, false, nil
	}

	hasMetaTool := false
	for _, t := range toolsRaw {
		tMap, ok := t.(map[string]interface{})
		if !ok {
			continue
		}
		fn, ok := tMap["function"].(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := fn["name"].(string)
		if isMetaTool(name) {
			hasMetaTool = true
			break
		}
	}
	if !hasMetaTool {
		return body, false, nil
	}

	// Load the full tool set from tool_registry. Empty categories =
	// "everything enabled" per the metatools package contract.
	result, err := i.handler.LoadTools(ctx, []string{})
	if err != nil {
		// Don't block real requests on a metatool outage: fall through.
		return body, false, fmt.Errorf("load tools: %w", err)
	}
	if result == nil || result.TotalCount == 0 {
		return body, false, nil
	}

	expanded := make([]interface{}, 0, len(toolsRaw)+len(result.Tools))
	for _, t := range toolsRaw {
		tMap, ok := t.(map[string]interface{})
		if !ok {
			expanded = append(expanded, t)
			continue
		}
		fn, ok := tMap["function"].(map[string]interface{})
		if !ok {
			expanded = append(expanded, t)
			continue
		}
		name, _ := fn["name"].(string)
		if !isMetaTool(name) {
			expanded = append(expanded, t)
		}
	}
	for _, def := range result.Tools {
		var tool interface{}
		if err := json.Unmarshal(def, &tool); err != nil {
			continue
		}
		expanded = append(expanded, tool)
	}
	req["tools"] = expanded

	modified, err := json.Marshal(req)
	if err != nil {
		return body, false, fmt.Errorf("rebuild request: %w", err)
	}
	return modified, true, nil
}
