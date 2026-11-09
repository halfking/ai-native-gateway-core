// Package contextbudget enforces per-attempt input budgets on the final JSON
// sent to an LLM. Its default counter is deliberately conservative, not the
// character/token heuristic used to decide when compression is worth trying.
package contextbudget

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

const (
	MaxInputTokens      = 500000
	DefaultMaxJSONBytes = 8 << 20 // Gateway resource policy, not a vendor limit.
	TokensExceeded      = "context_budget_exceeded"
	BytesExceeded       = "upstream_body_too_large"
	UnknownContext      = "context_budget_unknown"
	Uncountable         = "context_budget_uncountable"
	InvalidRequest      = "context_budget_invalid_request"
	InvalidSpec         = "context_budget_invalid_spec"
	requestOverhead     = 64
	nodeOverhead        = 16
)

// Spec must come from trusted bucket capabilities, never client claims.
// ExternalInputTokens covers ALL media/remote state not represented by text in
// this body. A known external bound must be positive when such input is used.
type Spec struct {
	ContextWindow       int
	MaxInputTokens      int
	MaxJSONBytes        int
	OutputReserve       int
	SafetyMargin        int
	ExternalInputTokens int
	ExternalInputKnown  bool
}

type Budget struct {
	InputTokens int
	JSONBytes   int
}

// Error is safe to log: Detail contains fixed diagnostics, never source text.
type Error struct {
	Kind   string
	Limit  int
	Actual int
	Detail string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s (actual=%d limit=%d)", e.Kind, e.Detail, e.Actual, e.Limit)
}

// Resolve derives the per-attempt budget from trusted bucket capabilities.
// A missing/invalid context window is never treated as unlimited: it resolves
// to the package-global conservative caps (MaxInputTokens / DefaultMaxJSONBytes)
// instead of failing. 2026-11-10: the previous behavior failed closed with
// UnknownContext for any candidate whose window was unknown, and production
// data carries ~90% NULL models_canonical.context_window — every candidate of
// such a model was rejected pre-dispatch (claude-opus-5-5 422 outage, ZCode
// empty_model_response round 3). Unknown now means "cannot verify the exact
// window", not "over budget": enforcement falls back to the global caps, and
// buckets with KNOWN windows keep the exact arithmetic below unchanged.
func Resolve(spec Spec) (Budget, error) {
	if spec.ContextWindow <= 0 {
		return Budget{InputTokens: MaxInputTokens, JSONBytes: DefaultMaxJSONBytes}, nil
	}
	if spec.OutputReserve < 0 || spec.SafetyMargin < 0 || spec.MaxInputTokens < 0 || spec.MaxJSONBytes < 0 || spec.ExternalInputTokens < 0 {
		return Budget{}, &Error{Kind: InvalidSpec, Detail: "negative bucket capacity"}
	}
	if spec.OutputReserve >= spec.ContextWindow || spec.SafetyMargin >= spec.ContextWindow-spec.OutputReserve {
		return Budget{}, &Error{Kind: TokensExceeded, Detail: "output and safety reserves exhaust context", Limit: spec.ContextWindow}
	}
	limit := min(MaxInputTokens, spec.ContextWindow/2, spec.ContextWindow-spec.OutputReserve-spec.SafetyMargin)
	if spec.MaxInputTokens > 0 {
		limit = min(limit, spec.MaxInputTokens)
	}
	if limit <= 0 {
		return Budget{}, &Error{Kind: TokensExceeded, Detail: "no input capacity remains"}
	}
	byteLimit := spec.MaxJSONBytes
	if byteLimit == 0 {
		byteLimit = DefaultMaxJSONBytes
	}
	return Budget{InputTokens: limit, JSONBytes: byteLimit}, nil
}

// SafeBodyBytes is a worst-case byte target for the fallback counter: each
// syntax node consumes at least one wire byte. Realistic requests can use much
// larger bodies by calling Check and fitting iteratively. Output parameters in
// the body or known external input may tighten this target further.
func SafeBodyBytes(spec Spec) int {
	budget, err := Resolve(spec)
	if err != nil {
		return 0
	}
	return max(0, min(budget.JSONBytes, (budget.InputTokens-requestOverhead-spec.ExternalInputTokens)/(nodeOverhead+1)))
}

type Receipt struct {
	Tokens     int
	JSONBytes  int
	Budget     Budget
	BodySHA256 string
	Counter    string
}

// Counter counts the complete model-visible input, including media and remote
// state. An implementation must be appropriate for the actual bucket/model.
type Counter interface {
	Count(body []byte) (int, error)
}

type CounterFunc func([]byte) (int, error)

func (f CounterFunc) Count(body []byte) (int, error) { return f(body) }

func Check(body []byte, spec Spec) (Receipt, error) {
	return CheckWithCounter(body, spec, nil)
}

// CheckWithCounter validates the final body. A nil counter uses the UTF-8-byte
// upper bound plus framing overhead; it will reject some requests that a
// model-specific exact tokenizer could safely admit.
func CheckWithCounter(body []byte, spec Spec, counter Counter) (Receipt, error) {
	budget, err := Resolve(spec)
	if err != nil {
		return Receipt{}, err
	}
	if len(body) > budget.JSONBytes {
		return Receipt{}, &Error{Kind: BytesExceeded, Limit: budget.JSONBytes, Actual: len(body), Detail: "final JSON exceeds byte limit"}
	}
	if counter == nil && len(body) > budget.InputTokens {
		return Receipt{}, &Error{Kind: TokensExceeded, Limit: budget.InputTokens, Actual: len(body), Detail: "final JSON exceeds conservative input bound"}
	}
	info, err := inspect(body)
	if err != nil {
		return Receipt{}, err
	}
	spec.OutputReserve = max(spec.OutputReserve, info.outputReserve)
	budget, err = Resolve(spec)
	if err != nil {
		return Receipt{}, err
	}
	count, method := 0, "utf8_bytes_with_framing_upper_bound"
	if counter != nil {
		count, err = counter.Count(body)
		method = "bucket_counter"
	} else if info.external && (!spec.ExternalInputKnown || spec.ExternalInputTokens <= 0) {
		err = &Error{Kind: Uncountable, Detail: "media or remote context requires a trusted token bound"}
	} else {
		count = len(body) + requestOverhead + nodeOverhead*info.nodes + spec.ExternalInputTokens
	}
	if err != nil {
		return Receipt{}, err
	}
	if count < 0 {
		return Receipt{}, &Error{Kind: Uncountable, Detail: "counter returned a negative count"}
	}
	if count > budget.InputTokens {
		return Receipt{}, &Error{Kind: TokensExceeded, Limit: budget.InputTokens, Actual: count, Detail: "final model input exceeds bucket budget"}
	}
	digest := sha256.Sum256(body)
	return Receipt{Tokens: count, JSONBytes: len(body), Budget: budget, BodySHA256: hex.EncodeToString(digest[:]), Counter: method}, nil
}

// UpperBound supports text JSON only. Use Check with a trusted external bound
// or a bucket-specific Counter for media and provider-managed state.
func UpperBound(body []byte) (int, error) {
	info, err := inspect(body)
	if err != nil {
		return 0, err
	}
	if info.external {
		return 0, &Error{Kind: Uncountable, Detail: "input contains media or remote state"}
	}
	return len(body) + requestOverhead + nodeOverhead*info.nodes, nil
}
