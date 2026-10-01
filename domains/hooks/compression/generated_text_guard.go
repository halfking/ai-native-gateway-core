package compression

import (
	"bytes"
	"context"
	"encoding/json"
)

// GeneratedTextGuard checks summarizer output before it is spliced into a
// request, fingerprinted or memoized. Failures select the existing mechanical
// fallback; the original client body at this point is already sanitized.
type GeneratedTextGuard func(context.Context, string) (string, error)
type generatedTextGuardKey struct{}

func WithGeneratedTextGuard(ctx context.Context, guard GeneratedTextGuard) context.Context {
	return context.WithValue(ctx, generatedTextGuardKey{}, guard)
}

func guardGeneratedText(ctx context.Context, text string) (string, error) {
	if guard, ok := ctx.Value(generatedTextGuardKey{}).(GeneratedTextGuard); ok && guard != nil {
		return guard(ctx, text)
	}
	return text, nil
}

// cachedBodyPassesGuard rechecks cached model-visible payloads using the
// current policy. A generation proves dictionary identity, not that a summary
// was checked by this binary/policy. Reject rather than reuse altered content.
func cachedBodyPassesGuard(ctx context.Context, body []byte) bool {
	guard, active := ctx.Value(generatedTextGuardKey{}).(GeneratedTextGuard)
	if !active || guard == nil || len(body) == 0 {
		return true
	}
	var envelope map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if !json.Valid(body) || decoder.Decode(&envelope) != nil || envelope == nil {
		return false
	}
	var check func(any, int) bool
	check = func(value any, depth int) bool {
		if depth > 256 {
			return false
		}
		switch value := value.(type) {
		case string:
			checked, err := guard(ctx, value)
			return err == nil && checked == value
		case []any:
			for _, child := range value {
				if !check(child, depth+1) {
					return false
				}
			}
		case map[string]any:
			for key, child := range value {
				// Preserve the same opaque attachment boundary as input handling.
				if _, scalar := child.(string); scalar && (key == "data" || key == "base64" || key == "file_data" || key == "audio" || key == "image") {
					continue
				}
				if key == "input" || key == "arguments" || key == "tool_calls" || key == "function_call" {
					if _, scalar := child.(string); !scalar {
						raw, err := json.Marshal(child)
						if err != nil {
							return false
						}
						checked, err := guard(ctx, string(raw))
						if err != nil || checked != string(raw) {
							return false
						}
					}
				}
				if !check(child, depth+1) {
					return false
				}
			}
		}
		return true
	}
	for _, key := range []string{"messages", "system", "input", "prompt"} {
		if !check(envelope[key], 0) {
			return false
		}
	}
	return true
}
