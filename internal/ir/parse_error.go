package ir

import "github.com/kaixuan/llm-gateway-go/errorsx"

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
