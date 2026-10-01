package ir

import (
	"fmt"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// ParseError wraps an error with additional classification metadata for
// vendor-specific error signaling (e.g. GLM finish_reason error channel).
type ParseError struct {
	Kind    errorsx.ErrorKind
	Message string
	Err     error
}

func (e *ParseError) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

func (e *ParseError) Unwrap() error {
	return e.Err
}

// jsonTypeName renders the dynamic type a decoded-JSON `any` holds using the
// JSON vocabulary the client wrote, so client-bug error messages name the
// actual offending shape ("number" / "boolean" / "object") instead of Go
// internals ("float64").
func jsonTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	default:
		return fmt.Sprintf("%T", v)
	}
}
