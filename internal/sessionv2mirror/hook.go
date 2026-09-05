// Package sessionv2mirror mirrors request_logs into V2 sessions tables
// (public.sessions, public.session_turns, public.session_bodies,
// public.session_turn_logs).
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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
//
// Optional dims writers (e.g. *SessionDimWriter) run on the same gating and
// maintain the V1 session_dim dimension table (task/project/owner/client) —
// also best-effort, never blocking the primary INSERT.
func PersistHook(writer V2Writer, dims ...DimWriter) func(entry *telemetry.RequestLogEntry) {
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

		// Gateway-internal title/summary loopbacks are not user turns. Business
		// auto-route requests also set IsAutoRequest, but carry TaskType and must
		// be mirrored so the task dimension is queryable from session_turns.
		if isInternalAutoEntry(entry) {
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

		// session_dim 维度维护（任务/项目/属主/客户端）：与 V2 写入同一
		// 超时预算，独立 best-effort —— V2 写失败也不影响维度更新。
		for _, dim := range dims {
			if dim == nil {
				continue
			}
			if err := dim.UpsertSessionDim(ctx, entry); err != nil {
				slog.Warn("sessionv2mirror: session_dim upsert failed",
					"request_id", entry.RequestID,
					"session_id", *entry.GwSessionID,
					"error", err)
			}
		}

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
		SessionID:       *entry.GwSessionID,
		TenantID:        entry.TenantID,
		RequestID:       entry.RequestID,
		Timestamp:       eventTime,
		ProjectID:       strVal(entry.ProjectID),
		Namespace:       strVal(entry.Namespace),
		ParentRequestID: strVal(entry.ParentRequestID),
		TaskType:        strVal(entry.TaskType),
		ClientModel:     strVal(entry.ClientModel),
		ProviderID:      providerID(entry.ProviderID),
		Success:         entry.Success,
		ErrorKind:       strVal(entry.ErrorKind),
		StatusCode:      statusCode(entry),
		StartedAt:       eventTime,
		CompletedAt:     eventTime,
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

	// V3.2 dual-write (migration 513): copy the 10 dispatch queue timestamps
	// from RequestLogEntry → ProcessedRequest so SessionWriterV2 can persist
	// them into public.session_turns alongside the existing turn columns.
	// nil-safe: pre-491 RequestLogEntries (older telemetry rows) carry nil
	// for all T0..T9, so the columns persist as NULL — no breakage.
	req.T0ArrivedAt = entry.T0ArrivedAt
	req.T1TotalEnqueuedAt = entry.T1TotalEnqueuedAt
	req.T2TotalDequeuedAt = entry.T2TotalDequeuedAt
	req.T3ModelEnqueuedAt = entry.T3ModelEnqueuedAt
	req.T4ModelDequeuedAt = entry.T4ModelDequeuedAt
	req.T5CredEnqueuedAt = entry.T5CredEnqueuedAt
	req.T6CredDequeuedAt = entry.T6CredDequeuedAt
	req.T7ForwardStartAt = entry.T7ForwardStartAt
	req.T8ResponseStartAt = entry.T8ResponseStartAt
	req.T9ResponseEndAt = entry.T9ResponseEndAt

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
	// Copy only the audited, body-free compression metadata. Raw request or
	// response payloads are intentionally excluded from the V2 mirror.
	for key, value := range safeCompressionMeta(entry.CompressionMeta, entry.TenantID, *entry.GwSessionID) {
		if req.CompressionMeta == nil {
			req.CompressionMeta = make(map[string]interface{})
		}
		req.CompressionMeta[key] = value
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

// safeCompressionMeta returns the body-free compression metadata that is safe to
// mirror into V2. Request-log compression_meta is an extensible JSON object, so
// copying it wholesale would accidentally persist raw payloads or future PII
// fields. Nested cut_marker fields are filtered independently.
const (
	maxCompressionMetaBytes = 256 << 10
	maxMetadataRecords      = 4096
	maxMetadataString       = 256
)

func safeCompressionMeta(raw json.RawMessage, tenantID, sessionID string) map[string]interface{} {
	if len(raw) == 0 || len(raw) > maxCompressionMetaBytes || string(raw) == "null" {
		return nil
	}
	var input map[string]interface{}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil
	}
	out := make(map[string]interface{})
	for key, value := range input {
		switch key {
		case "cut_marker":
			if filtered := safeCutMarkerMeta(value); len(filtered) > 0 {
				out[key] = filtered
				if psor, ok := filtered["pre_sanitize_offset_range"]; ok {
					out["pre_sanitize_offset_range"] = psor
				}
			}
		case "alignment_map":
			if filtered := safeAlignmentRecords(value); len(filtered) > 0 {
				out[key] = filtered
			}
		case "sanitize_message_refs":
			if filtered := safeSanitizeRefs(value); len(filtered) > 0 {
				out[key] = filtered
			}
		case "sanitize_map_ref":
			ref, ok := value.(string)
			expected := sanitizeMapRef(tenantID, sessionID)
			if ok && expected != "" && ref == expected {
				out[key] = ref
			}
		case "summary_marker", "compressed_prefix_hash":
			if text, ok := boundedHashLike(value); ok {
				out[key] = text
			}
		case "strategy", "compression_strategy", "reason", "compression_reason", "window_triggered", "lossiness":
			if text, ok := boundedLabel(value); ok && knownCompressionLabel(key, text) {
				out[key] = text
			}
		case "tokens_before", "tokens_after", "bytes_before", "bytes_after", "context_window_used", "msg_count", "token_est", "raw_token_est", "compressed_tokens", "compressed_msgs":
			if number, ok := boundedNumber(value); ok {
				out[key] = number
			}
		case "pre_sanitize_offset_range":
			if pair, ok := boundedPair(value); ok {
				out[key] = pair
			}
		}
	}
	return out
}

func safeAlignmentRecords(value interface{}) []map[string]interface{} {
	items, ok := value.([]interface{})
	if !ok || len(items) == 0 || len(items) > maxMetadataRecords {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		record, ok := item.(map[string]interface{})
		if !ok {
			return nil
		}
		original, ok1 := nonNegativeIndex(record["original_index"])
		compressed, ok2 := signedIndex(record["compressed_index"])
		into, ok3 := signedIndex(record["compressed_into"])
		isCompressed, ok4 := record["is_compressed"].(bool)
		hash, ok5 := boundedHex(record["hash"], 32, 64)
		if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 {
			return nil
		}
		out = append(out, map[string]interface{}{
			"original_index": original, "compressed_index": compressed,
			"is_compressed": isCompressed, "compressed_into": into, "hash": hash,
		})
	}
	return out
}

func safeSanitizeRefs(value interface{}) []map[string]interface{} {
	items, ok := value.([]interface{})
	if !ok || len(items) == 0 || len(items) > maxMetadataRecords {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		record, ok := item.(map[string]interface{})
		if !ok {
			return nil
		}
		rawIndex, ok1 := nonNegativeIndex(record["raw_index"])
		sanitizedIndex, ok2 := nonNegativeIndex(record["sanitized_index"])
		rawHash, ok3 := boundedHex(record["raw_hash"], 32, 64)
		sanitizedHash, ok4 := boundedHex(record["sanitized_hash"], 32, 64)
		changed, ok5 := record["changed"].(bool)
		if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 {
			return nil
		}
		filtered := map[string]interface{}{
			"raw_index": rawIndex, "sanitized_index": sanitizedIndex,
			"raw_hash": rawHash, "sanitized_hash": sanitizedHash, "changed": changed,
		}
		if count, ok := boundedNumber(record["placeholder_count"]); ok {
			filtered["placeholder_count"] = count
		}
		out = append(out, filtered)
	}
	return out
}

func safeCutMarkerMeta(value interface{}) map[string]interface{} {
	input, ok := value.(map[string]interface{})
	if !ok {
		return nil
	}
	version, ok1 := boundedNumber(input["version"])
	createdAt, ok2 := boundedNumber(input["created_at"])
	source, ok3 := boundedNumber(input["source_msg_count"])
	system, ok4 := boundedNumber(input["system_msg_count"])
	cut, ok5 := boundedNumber(input["cut_index"])
	strategy, ok6 := boundedLabel(input["strategy"])
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 || !knownCompressionLabel("strategy", strategy) || cut <= 0 || system+cut > source {
		return nil
	}
	out := map[string]interface{}{
		"version": version, "created_at": createdAt, "source_msg_count": source,
		"system_msg_count": system, "cut_index": cut, "strategy": strategy,
	}
	if number, ok := boundedNumber(input["bytes_before"]); ok {
		out["bytes_before"] = number
	}
	if number, ok := boundedNumber(input["bytes_after"]); ok {
		out["bytes_after"] = number
	}
	if marker, ok := boundedHashLike(input["summary_marker"]); ok {
		out["summary_marker"] = marker
	}
	if pair, ok := boundedPair(input["pre_sanitize_offset_range"]); ok && pair[1] <= source {
		out["pre_sanitize_offset_range"] = pair
	}
	return out
}

func boundedNumber(value interface{}) (float64, bool) {
	n, ok := value.(float64)
	return n, ok && n >= 0 && n <= 1<<53 && n == float64(int64(n))
}

func nonNegativeIndex(value interface{}) (float64, bool) {
	n, ok := value.(float64)
	return n, ok && n >= 0 && n <= maxMetadataRecords && n == float64(int64(n))
}

func signedIndex(value interface{}) (float64, bool) {
	n, ok := value.(float64)
	return n, ok && n >= -1 && n <= maxMetadataRecords && n == float64(int64(n))
}

func boundedPair(value interface{}) ([]float64, bool) {
	items, ok := value.([]interface{})
	if !ok || len(items) != 2 {
		return nil, false
	}
	start, ok1 := boundedNumber(items[0])
	end, ok2 := boundedNumber(items[1])
	return []float64{start, end}, ok1 && ok2 && start <= end
}

func boundedLabel(value interface{}) (string, bool) {
	text, ok := value.(string)
	if !ok || len(text) == 0 || len(text) > maxMetadataString {
		return "", false
	}
	for _, r := range text {
		if !(r == '_' || r == '-' || r == '.' || r == ':' || r == '/' || r == ' ' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return "", false
		}
	}
	return text, true
}

func knownCompressionLabel(key, text string) bool {
	known := map[string]bool{
		"none": true, "noop": true, "delta_append": true, "mechanical_trim": true,
		"smart_window": true, "smart_window_llm": true, "smart_window_mechanical": true,
		"sliding_window_token": true, "sliding_window_token_absolute": true,
		"sliding_window_msg_count": true, "sliding_window_idle": true,
		"incremental_cache": true, "incremental_cache_tail": true, "incremental_v2_metadata": true,
		"strategy_runner": true, "memora_l1": true, "memora_l1_inject": true,
		"llm_summary": true, "auto_threshold": true, "provider_window": true,
		"aggressive": true, "mode_1_auto_threshold": true, "mode_2_on_4xx": true,
		"mode_warmup_skipped": true, "token_threshold_forced_absolute": true,
		"token_threshold_preliminary": true, "tail": true, "whole": true,
		"below": true, "preliminary": true, "forced": true,
		"size": true, "count": true, "idle": true, "token": true,
	}
	if known[text] {
		return true
	}
	if key == "window_triggered" && strings.HasPrefix(text, "sliding_window_") {
		suffix := strings.TrimPrefix(text, "sliding_window_")
		return suffix == "token" || suffix == "token_absolute" || suffix == "msg_count" || suffix == "idle"
	}
	return false
}

func boundedHashLike(value interface{}) (string, bool) {
	text, ok := value.(string)
	if !ok || len(text) == 0 || len(text) > maxMetadataString {
		return "", false
	}
	if strings.HasPrefix(text, "[smm_v1:") && strings.HasSuffix(text, "]") {
		inner := strings.TrimSuffix(strings.TrimPrefix(text, "[smm_v1:"), "]")
		if validated, ok := boundedHex(inner, 16, 16); ok {
			return "[smm_v1:" + validated + "]", true
		}
		return "", false
	}
	return boundedHex(value, 16, 128)
}

func boundedHex(value interface{}, minLen, maxLen int) (string, bool) {
	text, ok := value.(string)
	if !ok || len(text) < minLen || len(text) > maxLen {
		return "", false
	}
	for _, r := range text {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return "", false
		}
	}
	return text, true
}

func sanitizeMapRef(tenantID, sessionID string) string {
	if tenantID == "" || sessionID == "" {
		return ""
	}
	hash := sha256.Sum256([]byte(tenantID))
	return fmt.Sprintf("session:%s:%s:sanitize", hex.EncodeToString(hash[:8]), sessionID)
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

func isInternalAutoEntry(entry *telemetry.RequestLogEntry) bool {
	if entry == nil || entry.IsAutoRequest == nil || !*entry.IsAutoRequest {
		return false
	}
	if entry.RequestType != nil {
		switch strings.TrimSpace(*entry.RequestType) {
		case "title_gen", "summary":
			return true
		}
	}
	if entry.OriginActor != nil {
		switch strings.TrimSpace(*entry.OriginActor) {
		case "auto-title-generator", "auto-summary-generator", "session-summary":
			return true
		}
	}
	return entry.TaskType == nil || strings.TrimSpace(*entry.TaskType) == ""
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
