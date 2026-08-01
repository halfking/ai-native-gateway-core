package ir

import (
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
)

var ErrInvalidJSON = errors.New("invalid tool arguments JSON")

type ToolArgumentsAssembler struct {
	mu  sync.Mutex
	buf strings.Builder
}

func NewToolArgumentsAssembler() *ToolArgumentsAssembler {
	return &ToolArgumentsAssembler{}
}

func (a *ToolArgumentsAssembler) Append(chunk string) error {
	if a == nil {
		return errors.New("nil tool arguments assembler")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	_, err := a.buf.WriteString(chunk)
	return err
}

func (a *ToolArgumentsAssembler) Finalize() (string, string, error) {
	if a == nil {
		return "", "invalid_json", ErrInvalidJSON
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	raw := a.buf.String()
	if json.Valid([]byte(raw)) {
		return raw, "", nil
	}

	patched, reason, safe := patchTruncatedJSON(raw)
	if safe && json.Valid([]byte(patched)) {
		return patched, reason, nil
	}

	reason = classifyInvalidJSON(raw)
	slog.Warn("invalid streaming tool arguments JSON",
		"request_id", "unknown",
		"reason", reason,
		"length", len(raw))
	return "", reason, ErrInvalidJSON
}

func (a *ToolArgumentsAssembler) Reset() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.buf.Reset()
	a.mu.Unlock()
}

func patchTruncatedJSON(raw string) (string, string, bool) {
	stack := make([]byte, 0, 8)
	inString := false
	escaped := false

	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch ch {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}

		switch ch {
		case '"':
			inString = true
		case '{', '[':
			stack = append(stack, ch)
		case '}', ']':
			if len(stack) == 0 || !matchingDelimiter(stack[len(stack)-1], ch) {
				return raw, "invalid_json", false
			}
			stack = stack[:len(stack)-1]
		}
	}

	patched := raw
	reason := "no_brace_close"
	if inString {
		if escaped {
			return raw, "unescaped_string", false
		}
		patched += `"`
		reason = "unescaped_string"
	}
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] == '{' {
			patched += "}"
		} else {
			patched += "]"
		}
	}
	return patched, reason, inString || len(stack) > 0
}

func matchingDelimiter(open, close byte) bool {
	return open == '{' && close == '}' || open == '[' && close == ']'
}

func classifyInvalidJSON(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if strings.HasSuffix(trimmed, ",}") || strings.HasSuffix(trimmed, ",]") {
		return "trailing_comma"
	}
	return "invalid_json"
}
