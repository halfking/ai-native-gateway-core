// Package tooloffer carries the caller's executable tool names to response repair.
package tooloffer

import (
	"context"
	"encoding/json"
	"strings"
)

// Names is an immutable request-scoped set. Its zero value grants no names.
type Names struct{ names map[string]struct{} }

func (n Names) Has(name string) bool { _, ok := n.names[name]; return ok }
func (n Names) Len() int             { return len(n.names) }

// FromRequest accepts flat/nested functions and schema-bearing Anthropic custom tools.
// Built-in tool types and namespaces are not executable function definitions.
func FromRequest(body []byte) Names {
	var request struct {
		Tools []struct {
			InputSchema json.RawMessage `json:"input_schema"`
			Type        string          `json:"type"`
			Name        string          `json:"name"`
			Function    *struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if json.Unmarshal(body, &request) != nil {
		return Names{}
	}
	names := map[string]struct{}{}
	for _, tool := range request.Tools {
		switch tool.Type {
		case "", "function":
		case "custom_tool":
			var schema map[string]json.RawMessage
			if json.Unmarshal(tool.InputSchema, &schema) != nil || schema == nil {
				continue
			}
		default:
			continue
		}
		name := tool.Name
		if tool.Function != nil && tool.Type != "custom_tool" {
			name = tool.Function.Name
		}
		if name != "" && strings.TrimSpace(name) == name {
			names[name] = struct{}{}
		}
	}
	return Names{names: names}
}

type contextKey struct{}

func WithNames(ctx context.Context, names Names) context.Context {
	return context.WithValue(ctx, contextKey{}, names)
}

func FromContext(ctx context.Context) Names {
	if ctx == nil {
		return Names{}
	}
	names, _ := ctx.Value(contextKey{}).(Names)
	return names
}
