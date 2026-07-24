// Package v2 contains the session persistence writer for V2 tables
// (gateway.sessions, gateway.session_turns, gateway.session_bodies,
// gateway.session_turn_logs).
//
// pipeline_hook.go implements the pipeline.Hook interface so SessionWriterV2
// can be wired into the v2 Hook Pipeline as a PhasePostResponse stage,
// enabling production dual-write through the v2 pipeline.
//
// Why in domains/session/v2: depends on SessionWriterV2; pipeline lives in
// domains/pipeline which does not import session/v2 (acyclic).
package v2

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"      //nolint:depguard // historical violation
	"github.com/kaixuan/llm-gateway-go/domains/pipeline" //nolint:depguard // historical violation
	"github.com/kaixuan/llm-gateway-go/settings"
)

// SessionPersistHook is a pipeline.Hook that writes session data to V2 tables
// after the upstream response is received. It runs in PhasePostResponse.
//
// Feature flags (all hot-reload):
//   sessions_v2.enabled          — master switch (default false)
//   sessions_v2.shadow_write    — write to V2 in addition to V1 (default false)
//   sessions_v2.rollout_percent — percentage of sessions routed to V2 (0-100)
//
// Best-effort contract: errors are logged but never propagate up to the
// pipeline. The request is already complete when this hook runs.
type SessionPersistHook struct {
	writer *SessionWriterV2
}

// NewSessionPersistHook creates a SessionPersistHook backed by the given writer.
// Pass nil to disable the hook (it will no-op on every method).
func NewSessionPersistHook(writer *SessionWriterV2) *SessionPersistHook {
	return &SessionPersistHook{writer: writer}
}

// Name implements pipeline.Hook.
func (h *SessionPersistHook) Name() string { return "session.persist" }

// Priority implements pipeline.Hook. Runs after audit (50) and cache_save (60).
func (h *SessionPersistHook) Priority() int { return 70 }

// Enabled implements pipeline.Hook.
func (h *SessionPersistHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	if h.writer == nil || env == nil {
		return false
	}
	if !settings.GetPlatformBool("sessions_v2.enabled", false) {
		return false
	}
	if !settings.GetPlatformBool("sessions_v2.shadow_write", false) {
		return false
	}
	// Rollout: consistent hash on session_id so the same session always
	// hits the same path.
	rollout := settings.GetPlatformInt("sessions_v2.rollout_percent", 0)
	if rollout <= 0 {
		return false
	}
	if rollout >= 100 {
		return true
	}
	// Consistent hash: deterministic, same session always same result.
	return hashString(env.SessionID)%100 < rollout
}

// Execute implements pipeline.Hook. It converts the PipelineRequest to a
// ProcessedRequest and writes it to the V2 tables.
//
// Only a subset of ProcessedRequest fields are populated from PipelineRequest;
// the remainder are best-effort or empty (see struct field comments).
func (h *SessionPersistHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	if h.writer == nil || env == nil {
		return nil
	}

	req := pipelineRequestToProcessed(env)

	// Bound write so a slow DB cannot stall the HTTP response close.
	timeoutMs := settings.GetPlatformInt("sessions_v2.write_timeout_ms", 500)
	writeCtx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	if err := h.writer.Write(writeCtx, req); err != nil {
		slog.Warn("session_persist_hook: V2 write failed (best-effort)",
			"request_id", req.RequestID,
			"session_id", req.SessionID,
			"tenant_id", req.TenantID,
			"error", err)
	}
	return nil
}

// OnError implements pipeline.Hook. The hook already best-efforts in Execute,
// so OnError is a no-op.
func (h *SessionPersistHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	return nil
}

var _ pipeline.Hook = (*SessionPersistHook)(nil)

// pipelineRequestToProcessed converts a domain.PipelineRequest to v2.ProcessedRequest.
//
// This is a best-effort conversion: only fields that are directly available
// on PipelineRequest are populated. Fields that require body parsing (RequestBody,
// ResponseBody) or per-stage metadata (ProcessingStages) are left empty.
//
// This mirrors the design of sessionv2mirror.entryToProcessedRequest which
// bridges V1 telemetry → V2. Unlike that bridge, this hook runs directly
// in the pipeline and has access to the full PipelineRequest envelope.
func pipelineRequestToProcessed(env *domain.PipelineRequest) *ProcessedRequest {
	if env == nil {
		return &ProcessedRequest{}
	}

	req := &ProcessedRequest{
		SessionID:   env.SessionID,
		TenantID:    env.TenantID,
		RequestID:   extractRequestID(env),
		Timestamp:   env.CreatedAt,
		ClientModel: extractClientModel(env),
		ProviderID:  extractProviderID(env),
		CredentialID: extractCredentialID(env),
		Success:     extractSuccess(env),
		StatusCode:  env.StatusCode,
		StartedAt:   env.CreatedAt,
		CompletedAt:  time.Now(),
	}

	// Timestamps: use envelope audit context if available.
	if env.Envelope != nil && env.Envelope.Audit != nil {
		req.StartedAt = time.Unix(env.Envelope.Audit.StartTimeUnix, 0)
	}

	// Latency from CreatedAt to now (best-effort for pipeline path).
	if !req.StartedAt.IsZero() {
		req.CompletedAt = time.Now()
	}

	// Tokens/cost are not available on PipelineRequest in this hook's scope.
	// Attachments and ProcessingStages are also not available at this stage.
	return req
}

// extractRequestID pulls the server-generated request ID from the envelope.
func extractRequestID(env *domain.PipelineRequest) string {
	if env.Envelope != nil {
		return env.Envelope.RequestID
	}
	return ""
}

// extractClientModel pulls the resolved client/outbound model.
func extractClientModel(env *domain.PipelineRequest) string {
	if env.Envelope != nil && env.Envelope.Transport != nil {
		if env.Envelope.Transport.OutboundModel != "" {
			return env.Envelope.Transport.OutboundModel
		}
		return env.Envelope.Transport.ClientModel
	}
	return ""
}

// extractProviderID extracts provider ID from selected provider.
func extractProviderID(env *domain.PipelineRequest) string {
	if env.SelectedProvider != nil {
		return env.SelectedProvider.ID
	}
	if env.Envelope != nil && env.Envelope.Transport != nil {
		return env.Envelope.Transport.UpstreamCatalogCode
	}
	return ""
}

// extractCredentialID extracts credential ID from selected credential.
func extractCredentialID(env *domain.PipelineRequest) string {
	if env.SelectedCredential != nil {
		return env.SelectedCredential.ID
	}
	return ""
}

// extractSuccess determines if the request succeeded based on status code and error.
func extractSuccess(env *domain.PipelineRequest) bool {
	if env.Error != nil {
		return false
	}
	// 0 means "unset/unknown", treat as not definitively successful.
	// 2xx is success, everything else is failure.
	if env.StatusCode == 0 {
		return false
	}
	return env.StatusCode >= 200 && env.StatusCode < 300
}

// hashString is a consistent hash used for rollout percentage decisions.
// Exported so dual_writer.go can use the same algorithm.
func hashString(s string) int {
	hash := 0
	for i := 0; i < len(s); i++ {
		hash = (hash*31 + int(s[i])) & 0x7FFFFFFF
	}
	return hash
}

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
