// Package v2 contains the session persistence writer for V2 tables
// (gateway.sessions, gateway.session_turns, gateway.session_bodies,
// gateway.session_turn_logs).
//
// pipeline_hook.go is the shell kept for backwards compatibility only:
// telemetry onPersisted is the sole production V2 write owner, and the
// v2 pipeline no longer loads SessionPersistHook (cmd/gateway/main_v2_pipeline.go).
// The hook is kept as a tiny no-op so external code that holds a reference
// to the symbol still compiles.
package v2

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"           //nolint:depguard // historical violation
	"github.com/kaixuan/llm-gateway-go/domains/pipeline" //nolint:depguard // historical violation
	"github.com/kaixuan/llm-gateway-go/settings"
)

// SessionPersistHook was the PhasePostResponse event-source hook. It is
// preserved so existing imports keep compiling, but Execute is a no-op when
// constructed with a nil publisher. The hook is intentionally NOT loaded by
// the production pipeline anymore — telemetry onPersisted is the sole V2
// write owner.
type SessionPersistHook struct {
	// publisher is retained as a nil-safe hook for callers that pass a real
	// publisher (e.g. unit tests, ad-hoc wiring). The production pipeline
	// always passes nil.
	publisher any
}

// NewSessionPersistHook creates a SessionPersistHook. The publisher argument
// is ignored; it remains in the signature for source compatibility with
// callers that historically passed an EventPublisher. The hook always
// no-ops at Execute time.
func NewSessionPersistHook(_ any) *SessionPersistHook {
	return &SessionPersistHook{}
}

// Name implements pipeline.Hook.
func (h *SessionPersistHook) Name() string { return "session.persist" }

// Priority implements pipeline.Hook.
func (h *SessionPersistHook) Priority() int { return 70 }

// Enabled implements pipeline.Hook. Returns false unconditionally — the
// production pipeline does not load this hook, and ad-hoc callers must
// opt in via the underlying writer.
func (h *SessionPersistHook) Enabled(_ context.Context, _ *domain.PipelineRequest) bool {
	if h == nil {
		return false
	}
	if !settings.GetPlatformBool("sessions_v2.enabled", false) {
		return false
	}
	if !settings.GetPlatformBool("sessions_v2.shadow_write", false) {
		return false
	}
	return false
}

// Execute implements pipeline.Hook. Returns nil unconditionally; the hook
// is a no-op shell. When the publisher is non-nil, it is accepted but
// never invoked — the production owner is telemetry onPersisted.
func (h *SessionPersistHook) Execute(_ context.Context, _ *domain.PipelineRequest) error {
	if h == nil {
		return nil
	}
	return nil
}

// OnError implements pipeline.Hook. No-op.
func (h *SessionPersistHook) OnError(_ context.Context, _ *domain.PipelineRequest, err error) error {
	return nil
}

var _ pipeline.Hook = (*SessionPersistHook)(nil)

// parseRequestBodyMessages parses a raw JSON request body into []Message.
// This is used when the pipeline carries the raw body in Metadata.
func parseRequestBodyMessages(body []byte) []Message {
	if len(body) == 0 {
		return nil
	}
	var p struct {
		Messages []msgProbe `json:"messages"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil
	}
	msgs := make([]Message, 0, len(p.Messages))
	for _, r := range p.Messages {
		m := Message{
			Role:    r.Role,
			Content: r.Content,
		}
		if len(r.ToolCalls) > 0 {
			m.ToolCalls = r.ToolCalls
		}
		msgs = append(msgs, m)
	}
	return msgs
}

type msgProbe struct {
	Role       string                   `json:"role"`
	Content    string                   `json:"content"`
	ToolCallID string                   `json:"tool_call_id"`
	Name       string                   `json:"name"`
	ToolCalls  []map[string]interface{} `json:"tool_calls"`
}

// safeMessageParse parses a JSON body and returns the messages array.
// Returns nil on any error (caller handles gracefully).
func safeMessageParse(body []byte) []Message {
	if len(body) == 0 {
		return nil
	}
	return parseRequestBodyMessages(body)
}

// strPtr is a helper to create a string pointer.
func strPtr(s string) *string { return &s } //nolint:revive // internal helper

// intStr converts an int to a string.
func intStr(v int) string {
	if v == 0 {
		return ""
	}
	buf := make([]byte, 0, 10)
	for v > 0 {
		buf = append(buf, byte('0'+v%10))
		v /= 10
	}
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}

// ParseJSONInt parses an int from a JSON number field. Returns 0 on error.
func ParseJSONInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case string:
		i, _ := strconv.Atoi(n)
		return i
	default:
		return 0
	}
}

// hashString is kept for callers that still depend on the consistent
// hash (was used for the rollout percentage in the legacy hook).
func hashString(s string) int {
	hash := 0
	for i := 0; i < len(s); i++ {
		hash = (hash*31 + int(s[i])) & 0x7FFFFFFF
	}
	return hash
}

// pipelineRequestToProcessed is no longer used by the production pipeline
// but is retained for callers that might still import it. The result is
// populated from the envelope's request id, session id, and tenant id.
func pipelineRequestToProcessed(env *domain.PipelineRequest) *ProcessedRequest {
	if env == nil {
		return &ProcessedRequest{}
	}
	req := &ProcessedRequest{
		SessionID:    env.SessionID,
		TenantID:     env.TenantID,
		RequestID:    extractRequestID(env),
		Timestamp:    env.CreatedAt,
		ClientModel:  extractClientModel(env),
		ProviderID:   extractProviderID(env),
		CredentialID: extractCredentialID(env),
		Success:      extractSuccess(env),
		StatusCode:   env.StatusCode,
		StartedAt:    env.CreatedAt,
		CompletedAt:  time.Now(),
	}
	if env.Envelope != nil && env.Envelope.Audit != nil {
		req.StartedAt = time.Unix(env.Envelope.Audit.StartTimeUnix, 0)
	}
	if !req.StartedAt.IsZero() {
		req.CompletedAt = time.Now()
	}
	return req
}

func extractRequestID(env *domain.PipelineRequest) string {
	if env.Envelope != nil {
		return env.Envelope.RequestID
	}
	return ""
}

func extractClientModel(env *domain.PipelineRequest) string {
	if env.Envelope != nil && env.Envelope.Transport != nil {
		if env.Envelope.Transport.OutboundModel != "" {
			return env.Envelope.Transport.OutboundModel
		}
		return env.Envelope.Transport.ClientModel
	}
	return ""
}

func extractProviderID(env *domain.PipelineRequest) string {
	if env.SelectedProvider != nil {
		return env.SelectedProvider.ID
	}
	if env.Envelope != nil && env.Envelope.Transport != nil {
		return env.Envelope.Transport.UpstreamCatalogCode
	}
	return ""
}

func extractCredentialID(env *domain.PipelineRequest) string {
	if env.SelectedCredential != nil {
		return env.SelectedCredential.ID
	}
	return ""
}

func extractSuccess(env *domain.PipelineRequest) bool {
	if env.Error != nil {
		return false
	}
	if env.StatusCode == 0 {
		return false
	}
	return env.StatusCode >= 200 && env.StatusCode < 300
}
