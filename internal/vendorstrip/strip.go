// Package strip centralizes vendor response sanitization and vendor-specific
// error-envelope detection. Its registry is deliberately response-only so it
// can be used by streaming and non-streaming paths without import cycles.
package strip

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

const (
	VendorMiniMax  = "minimax"
	VendorZhipu    = "zhipu"
	VendorDeepSeek = "deepseek"
	VendorDoubao   = "doubao"
	VendorErnie    = "ernie"
)

// Signal is a vendor error encoded in an otherwise successful HTTP response.
type Signal struct {
	Code    int
	Message string
	Kind    errorsx.ErrorKind
}

// Stripper describes one vendor's response policy. DetectError is separate
// from StripFields so callers can preserve an upstream error envelope before
// removing vendor metadata from a successful response.
type Stripper interface {
	StripFields(body []byte) []byte
	DetectError(body []byte) (Signal, bool)
}

// Registry maps normalized vendor codes to response policies.
type Registry struct {
	strippers map[string]Stripper
}

// NewRegistry returns the built-in response policies. Unknown vendors and
// Ernie deliberately pass through unchanged.
func NewRegistry() *Registry {
	passthrough := NewPassthroughStripper()
	return &Registry{strippers: map[string]Stripper{
		VendorMiniMax:  NewMinimaxStripper(),
		VendorZhipu:    NewZhipuStripper(),
		VendorDeepSeek: NewDeepSeekStripper(),
		VendorDoubao:   NewDoubaoStripper(),
		VendorErnie:    passthrough,
		"baidu":        passthrough,
	}}
}

// NewMinimaxStripper returns the MiniMax field and error policy.
func NewMinimaxStripper() Stripper { return miniMaxStripper{} }

// NewMiniMaxStripper is an acronym-cased alias for NewMinimaxStripper.
// Returns the stripper directly to avoid recursion through the lower-case
// constructor.
func NewMiniMaxStripper() Stripper { return miniMaxStripper{} }

// NewZhipuStripper returns the Zhipu/GLM field policy.
func NewZhipuStripper() Stripper {
	return fieldsStripper{fields: zhipuPrivateFields, logName: "strip_zhipu"}
}

// NewDeepSeekStripper returns the DeepSeek field policy.
func NewDeepSeekStripper() Stripper {
	return fieldsStripper{fields: deepSeekPrivateFields, logName: "strip_deepseek"}
}

// NewDoubaoStripper returns the Doubao field policy.
func NewDoubaoStripper() Stripper {
	return fieldsStripper{fields: doubaoPrivateFields, logName: "strip_doubao"}
}

// NewPassthroughStripper returns a policy that preserves body bytes unchanged.
func NewPassthroughStripper() Stripper { return passthroughStripper{} }

// DefaultRegistry is immutable after construction and safe for concurrent use.
var DefaultRegistry = NewRegistry()

// Get returns the policy for a vendor code. GLM is a Zhipu alias.
func (r *Registry) Get(vendor string) (Stripper, bool) {
	if r == nil {
		return nil, false
	}
	vendor = normalizeVendor(vendor)
	s, ok := r.strippers[vendor]
	return s, ok
}

// Strip removes private fields for an explicitly named vendor. Unknown
// vendors, including unregistered vendor codes, are passed through unchanged.
func (r *Registry) Strip(body []byte, vendor string) []byte {
	if s, ok := r.Get(vendor); ok {
		return s.StripFields(body)
	}
	return body
}

// DetectError detects an error only for an explicitly named vendor. This keeps
// one vendor's envelope from being misclassified as another vendor's error.
func (r *Registry) DetectError(body []byte, vendor string) (Signal, bool) {
	if s, ok := r.Get(vendor); ok {
		return s.DetectError(body)
	}
	return Signal{}, false
}

// Sanitize detects the registered vendor error before stripping fields. On an
// error it returns the original body unchanged, allowing callers to retain the
// exact error envelope for retry classification and diagnostics.
func (r *Registry) Sanitize(body []byte, vendor string) ([]byte, Signal, bool) {
	if signal, ok := r.DetectError(body, vendor); ok {
		return body, signal, true
	}
	return r.Strip(body, vendor), Signal{}, false
}

// Resolve returns an explicit vendor policy, or for an empty vendor code
// infers one solely from top-level response fields. It intentionally does not
// inspect nested content, preventing user text from selecting a vendor policy.
func (r *Registry) Resolve(body []byte, vendor string) (string, Stripper) {
	if vendor = normalizeVendor(vendor); vendor != "" {
		s, _ := r.Get(vendor)
		return vendor, s
	}

	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return "", nil
	}
	switch {
	case fields["base_resp"] != nil || fields["nvext"] != nil || fields["input_sensitive"] != nil:
		return VendorMiniMax, r.strippers[VendorMiniMax]
	case fields["zhipu_request_id"] != nil || fields["web_search_results"] != nil:
		return VendorZhipu, r.strippers[VendorZhipu]
	case fields["deepseek_request_id"] != nil || fields["cache_hit_tokens"] != nil:
		return VendorDeepSeek, r.strippers[VendorDeepSeek]
	case fields["doubao_request_id"] != nil || fields["seeddance_request_id"] != nil:
		return VendorDoubao, r.strippers[VendorDoubao]
	default:
		return "", nil
	}
}

// SanitizeResolved provides the same error-before-strip guarantee as Sanitize,
// while allowing an empty vendor code to use safe top-level-only inference.
func (r *Registry) SanitizeResolved(body []byte, vendor string) ([]byte, string, Signal, bool) {
	vendor, policy := r.Resolve(body, vendor)
	if policy == nil {
		return body, vendor, Signal{}, false
	}
	if signal, ok := policy.DetectError(body); ok {
		return body, vendor, signal, true
	}
	return policy.StripFields(body), vendor, Signal{}, false
}

// IsJSONErrorBody recognizes an upstream JSON error envelope without treating
// a bare top-level message field as an error. It accepts complete bodies and
// single SSE data lines.
func IsJSONErrorBody(body []byte) (bool, string, string) {
	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "data:") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	}
	if trimmed == "" || trimmed[0] != '{' {
		return false, "", ""
	}
	var envelope struct {
		Error *struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
		Type    string `json:"type,omitempty"`
		Code    string `json:"code,omitempty"`
		Message string `json:"message,omitempty"`
	}
	if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil {
		return false, "", ""
	}
	if envelope.Error == nil && envelope.Type == "" && envelope.Code == "" {
		return false, "", ""
	}
	kind, message := "", ""
	if envelope.Error != nil {
		// Preserve the established precedence: the nested error envelope is
		// authoritative; top-level fields are fallback metadata only.
		kind = envelope.Error.Code
		if kind == "" {
			kind = envelope.Error.Type
		}
		message = envelope.Error.Message
	}
	if kind == "" {
		kind = envelope.Code
	}
	if message == "" {
		message = envelope.Message
	}
	if kind == "" && message == "" {
		return false, "", ""
	}
	return true, kind, message
}

func normalizeVendor(vendor string) string {
	vendor = strings.ToLower(strings.TrimSpace(vendor))
	if vendor == "glm" {
		return VendorZhipu
	}
	return vendor
}

type passthroughStripper struct{}

func (passthroughStripper) StripFields(body []byte) []byte    { return body }
func (passthroughStripper) DetectError([]byte) (Signal, bool) { return Signal{}, false }

type fieldsStripper struct {
	fields  []string
	logName string
}

func (s fieldsStripper) StripFields(body []byte) []byte {
	return stripTopLevelFields(body, s.fields, s.logName)
}

func (fieldsStripper) DetectError([]byte) (Signal, bool) { return Signal{}, false }

func stripTopLevelFields(body []byte, fields []string, logName string) []byte {
	if len(body) == 0 {
		return body
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return body
	}
	stripped := false
	for _, field := range fields {
		if _, ok := raw[field]; ok {
			delete(raw, field)
			stripped = true
		}
	}
	if !stripped {
		return body
	}
	out, err := json.Marshal(raw)
	if err != nil {
		slog.Warn(logName+": marshal failed, returning original body", "error", err)
		return body
	}
	return out
}

var zhipuPrivateFields = []string{
	"zhipu_request_id",
	"web_search_results",
	"retrieval_documents",
	"model_version",
	"sensitive_word_check",
}

var deepSeekPrivateFields = []string{
	"deepseek_request_id",
	"model_type",
	"cache_hit_tokens",
}

var doubaoPrivateFields = []string{
	"doubao_request_id",
	"seeddance_request_id",
	"content_safety_score",
	"model_endpoint",
	"ab_test_group",
	"seed_token_usage",
	"request_id",
	"volc_request_id",
	"internal_model_version",
	"sensitive_check",
}
