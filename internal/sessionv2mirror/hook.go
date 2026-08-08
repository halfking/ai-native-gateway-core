// Package sessionv2mirror mirrors request_logs into V2 sessions tables
// (gateway.sessions, gateway.session_turns, gateway.session_bodies,
// gateway.session_turn_logs).
//
// Why a separate package: domains/session/v2 cannot import
// domains/hooks/observability/telemetry without creating an import
// cycle (telemetry → dbdegradation → session). Putting the adapter
// in a leaf package that depends on BOTH avoids the cycle.
//
// Best-effort contract: the returned hook logs and continues on any
// error; it never propagates failures up to telemetry. The primary
// request_logs INSERT must always succeed.
package sessionv2mirror

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/session/v2"
	"github.com/kaixuan/llm-gateway-go/metrics"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// PersistHook returns a telemetry "onPersisted" hook that mirrors
// request_logs entries into the V2 sessions tables.
//
// The hook checks sessions_v2.shadow_write and sessions_v2.enabled flags.
// The hook is a no-op when:
//   - writer is nil,
//   - the feature flag disables V2 shadow writes,
//   - the entry has no GwSessionID (no session context).
//
// On real DB errors it logs WARN with the request_id and continues.
//
// The writer parameter accepts the V2Writer interface so unit tests can
// inject a failing writer; production passes a *v2.SessionWriterV2,
// which satisfies V2Writer.
func PersistHook(writer V2Writer) func(entry *telemetry.RequestLogEntry) {
	if writer == nil {
		return func(*telemetry.RequestLogEntry) {}
	}

	return func(entry *telemetry.RequestLogEntry) {
		if entry == nil || entry.GwSessionID == nil || *entry.GwSessionID == "" {
			return
		}

		// request_logs emits an in_progress INSERT before upstream execution.
		// V2 turns are idempotent on request_id and cannot be updated after the
		// first insert, so mirroring that placeholder would permanently record
		// success=false/status_code=500 and drop the later success enrichment.
		// Only terminal entries are valid shadow-write inputs.
		if !entry.Success && !isTerminalFailure(entry) {
			return
		}

		// 2026-08-06: skip gateway-internal auto requests (auto title/summary
		// loopback calls marked via X-Gw-Is-Auto). These are ephemeral and must
		// not be mirrored into the session V2 tables — doing so pollutes the
		// session's turn history with title-generation traffic.
		if entry.IsAutoRequest != nil && *entry.IsAutoRequest {
			return
		}

		// Check feature flags via the shadowWriteEnabled seam (defaults to
		// the settings-backed reader; tests override it because
		// settings.Global is nil in the unit-test binary).
		if !shadowWriteEnabled() {
			return
		}

		// Convert the telemetry entry to a V2 ProcessedRequest
		req := entryToProcessedRequest(entry)
		if req == nil {
			return
		}

		// Bound the shadow write so a slow DB cannot stall telemetry.
		timeoutMs := settings.GetPlatformInt("sessions_v2.write_timeout_ms", 500)
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMs)*time.Millisecond)
		defer cancel()

		if err := writer.Write(ctx, req); err != nil {
			slog.Warn("sessionv2mirror: V2 shadow write failed",
				"request_id", entry.RequestID,
				"session_id", *entry.GwSessionID,
				"error", err)
			// P0-2 (audit §3.6 R-3.3): count this lost-row event so the
			// Grafana rule in deploy/monitoring/grafana-alerts/shadow-write-failures.yaml
			// can fire. V2 sessions tables are migration 430 (shadow write
			// during cutover); losing rows during the cutover window is the
			// exact "data drift" failure mode the audit calls out.
			metrics.Global().RecordShadowWriteFailure("session_v2")
			// Spec §12 GAP 2: also retain the failed entry in the
			// in-process backlog so it is observable (gauge) and drainable
			// rather than dropped on the floor with only a counter. The
			// backlog is bounded (FIFO eviction at cap); it is not
			// persisted — dbdegradation.RingBuffer covers WAL fallback.
			appendBacklog(BacklogItem{
				RequestID: entry.RequestID,
				Req:       req,
				Entry:     entry,
			})
		}
	}
}

// entryToProcessedRequest converts a telemetry.RequestLogEntry to a
// v2.ProcessedRequest. This is the bridge between the V1 telemetry
// pipeline and the V2 writer.
func entryToProcessedRequest(entry *telemetry.RequestLogEntry) *v2.ProcessedRequest {
	if entry == nil || entry.GwSessionID == nil {
		return nil
	}

	eventTime := time.Now()
	if entry.EventAt != nil {
		eventTime = *entry.EventAt
	}
	req := &v2.ProcessedRequest{
		SessionID:   *entry.GwSessionID,
		TenantID:    entry.TenantID,
		RequestID:   entry.RequestID,
		Timestamp:   eventTime,
		ClientModel: strVal(entry.ClientModel),
		ProviderID:  providerID(entry.ProviderID),
		Success:     entry.Success,
		ErrorKind:   strVal(entry.ErrorKind),
		StatusCode:  statusCode(entry),
		StartedAt:   eventTime,
		CompletedAt: eventTime,
	}

	// Usage & cost
	if entry.PromptTokens != nil {
		req.PromptTokens = *entry.PromptTokens
	}
	if entry.CompletionTokens != nil {
		req.CompletionTokens = *entry.CompletionTokens
	}
	if entry.CacheReadTokens != nil {
		req.CacheReadTokens = *entry.CacheReadTokens
	}
	if entry.CacheWriteTokens != nil {
		req.CacheWriteTokens = *entry.CacheWriteTokens
	}
	if entry.CostUSD != nil {
		req.CostUSD = *entry.CostUSD
	}

	// Timestamps
	if entry.EventAt != nil {
		req.StartedAt = *entry.EventAt
		req.CompletedAt = *entry.EventAt
	}
	if entry.LatencyMs != nil && *entry.LatencyMs > 0 && !req.StartedAt.IsZero() {
		req.CompletedAt = req.StartedAt.Add(time.Duration(*entry.LatencyMs) * time.Millisecond)
	}

	// Compression
	if entry.CompressionStrategy != nil {
		req.CompressionStrategy = *entry.CompressionStrategy
		req.CompressionApplied = *entry.CompressionStrategy != ""
	}
	if entry.CompressionReason != nil {
		if req.CompressionMeta == nil {
			req.CompressionMeta = make(map[string]interface{})
		}
		req.CompressionMeta["reason"] = *entry.CompressionReason
	}

	// Credential
	if entry.CredentialID != nil {
		req.CredentialID = intStr(*entry.CredentialID)
	}

	// Parse request body messages
	if entry.RequestBody != nil && *entry.RequestBody != "" {
		req.RequestBody = parseRequestBody(*entry.RequestBody)
	}

	// Parse response body messages
	if entry.ResponseBody != nil && *entry.ResponseBody != "" {
		req.ResponseBody = parseResponseBody(*entry.ResponseBody)
	}

	// Outbound body (from session compressor)
	if len(entry.OutboundBody) > 0 {
		req.OutboundBody = parseMessagesJSON(entry.OutboundBody)
	}

	// Submit-mode header (X-Gw-Submit-Mode). This is the SubmitModeDetector's
	// P0 (authoritative) signal; without it the detector can only infer
	// "inferred_compressed" via LCS rather than emit a true "delta".
	if entry.SubmitModeHeader != nil {
		req.SubmitModeHeader = *entry.SubmitModeHeader
	}
	// LastOutboundBody deliberately remains empty here. SessionWriterV2 loads
	// the previous turn's persisted outbound snapshot when the telemetry entry
	// does not carry one; using the current request body as "previous" corrupts
	// multi-turn delta detection.

	// Attachments
	if len(entry.Attachments) > 0 {
		req.Attachments = parseAttachments(entry.Attachments)
		req.MultimodalTypes = multimodalTypes(req.Attachments)
	}

	return req
}

// ── JSON body parsing ──────────────────────────────────────────────────────

type requestProbe struct {
	Messages []msgProbe `json:"messages"`
}

type responseProbe struct {
	Choices []choiceProbe `json:"choices"`
}

type choiceProbe struct {
	Message msgProbe `json:"message"`
}

type msgProbe struct {
	Role       string                   `json:"role"`
	Content    json.RawMessage          `json:"content"`
	ToolCallID string                   `json:"tool_call_id"`
	Name       string                   `json:"name"`
	ToolCalls  []map[string]interface{} `json:"tool_calls"`
}

func parseRequestBody(body string) []v2.Message {
	return parseProtocolMessages(json.RawMessage(body), false)
}

func parseResponseBody(body string) []v2.Message {
	return parseProtocolMessages(json.RawMessage(body), true)
}

// parseProtocolMessages normalizes the common request/response envelopes before
// handing message content to the shared IR decoder. This keeps legacy string
// messages and native multimodal protocol bodies on one parsing path.
func parseProtocolMessages(raw json.RawMessage, response bool) []v2.Message {
	if len(raw) == 0 {
		return nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil
	}
	var items []json.RawMessage
	if !response {
		if b := root["messages"]; len(b) > 0 {
			_ = json.Unmarshal(b, &items)
		} else if b := root["contents"]; len(b) > 0 {
			_ = json.Unmarshal(b, &items)
		}
	} else if b := root["choices"]; len(b) > 0 {
		var choices []struct {
			Message json.RawMessage `json:"message"`
		}
		if json.Unmarshal(b, &choices) == nil {
			for _, c := range choices {
				if len(c.Message) > 0 {
					items = append(items, c.Message)
				}
			}
		}
	} else if b := root["content"]; len(b) > 0 {
		// Anthropic response content is a block array; wrap it as one message.
		items = []json.RawMessage{json.RawMessage(`{"role":"assistant","content":` + string(b) + `}`)}
	} else if b := root["candidates"]; len(b) > 0 {
		var candidates []struct {
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(b, &candidates) == nil {
			for _, c := range candidates {
				if len(c.Content) > 0 {
					var content map[string]json.RawMessage
					if json.Unmarshal(c.Content, &content) == nil {
						content["role"] = json.RawMessage(`"model"`)
						items = append(items, marshalObject(content))
					}
				}
			}
		}
	} else if b := root["output"]; len(b) > 0 {
		// Responses API output items already carry type/role/content fields.
		_ = json.Unmarshal(b, &items)
	}
	if len(items) == 0 {
		return nil
	}
	canonical, err := json.Marshal(map[string]any{"messages": items})
	if err != nil {
		return nil
	}
	return v2.IRMessagesToV2(v2.IRMessagesFromJSON(canonical))
}

func marshalObject(obj map[string]json.RawMessage) json.RawMessage {
	b, _ := json.Marshal(obj)
	return b
}

// parseMessagesJSON extracts messages from the session compressor's
// OutboundBody. That value is the FULL upstream request object
// ({"model":...,"messages":[...]} — see compression.spliceBodyMessages), so we
// try the object shape first and fall back to a bare [] array for callers /
// tests that pass just the messages slice.
//
// 2026-08-05 (v2 mirror bug): the previous implementation only handled the bare
// array. In production OutboundBody is always the object form, so the unmarshal
// failed silently and req.OutboundBody was always empty — every
// session_bodies.outbound_body persisted NULL, which in turn broke multi-turn
// submit-mode/delta detection (getPreviousBody found no prior outbound).
func parseMessagesJSON(raw json.RawMessage) []v2.Message {
	// Object form: {"messages":[...]} (also tolerates model/tools/etc.).
	var obj requestProbe
	if err := json.Unmarshal(raw, &obj); err == nil && len(obj.Messages) > 0 {
		return toMessages(obj.Messages)
	}
	// Bare array form: [{"role":...,"content":...}, ...].
	var arr []msgProbe
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 {
		return toMessages(arr)
	}
	return nil
}

func toMessages(raw []msgProbe) []v2.Message {
	msgs := make([]v2.Message, 0, len(raw))
	for _, r := range raw {
		msgs = append(msgs, toMessage(r))
	}
	return msgs
}

func toMessage(r msgProbe) v2.Message {
	m := v2.Message{
		Role:       r.Role,
		ToolCallID: r.ToolCallID,
		Name:       r.Name,
	}
	if len(r.Content) > 0 && string(r.Content) != "null" {
		if err := json.Unmarshal(r.Content, &m.Content); err != nil {
			m.ContentRaw = append(json.RawMessage(nil), r.Content...)
		}
	} else if len(r.Content) > 0 {
		m.ContentRaw = append(json.RawMessage(nil), r.Content...)
	}
	if len(r.ToolCalls) > 0 {
		m.ToolCalls = r.ToolCalls
	}
	return m
}

// ── Attachment parsing ─────────────────────────────────────────────────────

type attachProbe struct {
	Name           string    `json:"name"`
	ObjectKey      string    `json:"object_key"`
	LegacyPath     string    `json:"path"`
	MIMEType       string    `json:"mime_type"`
	ContentType    string    `json:"content_type"`
	SizeBytes      int64     `json:"size_bytes"`
	LegacySize     int64     `json:"size"`
	SHA256         string    `json:"sha256"`
	LegacyHash     string    `json:"hash"`
	OriginalURL    string    `json:"original_url"`
	SourceProtocol string    `json:"source_protocol"`
	DeclaredMIME   string    `json:"declared_mime"`
	SniffedMIME    string    `json:"sniffed_mime"`
	ProviderFileID string    `json:"provider_file_id"`
	ExpiresAt      time.Time `json:"expires_at"`
	Replayable     bool      `json:"replayable"`
}

func parseAttachments(raw json.RawMessage) []v2.AttachmentRef {
	var probes []attachProbe
	if err := json.Unmarshal(raw, &probes); err != nil {
		return nil
	}
	refs := make([]v2.AttachmentRef, 0, len(probes))
	for _, p := range probes {
		refs = append(refs, v2.AttachmentRef{
			Name:      p.Name,
			ObjectKey: firstNonEmpty(p.ObjectKey, p.LegacyPath),
			MIMEType:  firstNonEmpty(p.MIMEType, p.ContentType),
			SizeBytes: firstNonZero(p.SizeBytes, p.LegacySize),
			SHA256:    firstNonEmpty(p.SHA256, p.LegacyHash),

			SourceProtocol: p.SourceProtocol,
			DeclaredMIME:   p.DeclaredMIME,
			SniffedMIME:    p.SniffedMIME,
			ProviderFileID: p.ProviderFileID,
			ExpiresAt:      p.ExpiresAt,
			Replayable:     p.Replayable,
		})
	}
	return refs
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func firstNonZero(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func multimodalTypes(attachments []v2.AttachmentRef) []string {
	seen := make(map[string]struct{})
	var types []string
	for _, a := range attachments {
		t := mimeClass(a.MIMEType)
		if t == "" {
			continue
		}
		if _, ok := seen[t]; !ok {
			seen[t] = struct{}{}
			types = append(types, t)
		}
	}
	return types
}

func mimeClass(mime string) string {
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return "image"
	case "audio/mpeg", "audio/wav", "audio/ogg":
		return "audio"
	case "video/mp4", "video/webm":
		return "video"
	case "application/pdf", "text/plain", "text/csv":
		return "document"
	default:
		if mime == "" {
			return ""
		}
		return "document"
	}
}

// ── Small helpers ──────────────────────────────────────────────────────────

func strVal(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func providerID(v *int) string {
	if v == nil || *v == 0 {
		return ""
	}
	return intStr(*v)
}

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

func isTerminalFailure(entry *telemetry.RequestLogEntry) bool {
	if entry == nil || entry.Success {
		return false
	}
	if entry.RequestStatus != nil {
		switch strings.TrimSpace(*entry.RequestStatus) {
		case telemetry.RequestStatusFailure, telemetry.RequestStatusRateLimited:
			return true
		}
	}
	return entry.ErrorKind != nil && strings.TrimSpace(*entry.ErrorKind) != ""
}

func statusCode(entry *telemetry.RequestLogEntry) int {
	if entry.UpstreamStatusCode != nil {
		return *entry.UpstreamStatusCode
	}
	if entry.Success {
		return 200
	}
	return 500
}
