// Package compressor - cut_marker.go
//
// Compression position tracking: records WHERE in the message array a
// compression cut happened, so the next request for the same session can
// start from the compressed state instead of re-compressing from scratch.
//
// Lifecycle:
//
//  1. First context_length_exceeded 4xx → SmartCompress runs → CutMarker is
//     created with the message index boundary and persisted to SessionCache.
//  2. Next request for the same session → SessionCache.GetOrLoad returns the
//     cached CutMarker → the request handler knows messages [0, CutIndex) have
//     already been summarised, so only messages [CutIndex, newEnd) need to be
//     processed.
//  3. The cached summary is prepended to the new tail, producing
//     [system + summary + messages_from_cut_onwards + new_messages].
//
// This avoids the expensive "summarise the entire conversation every time"
// pattern and makes incremental compression possible.

package compression

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CutMarker records the boundary and metadata of a compression event.
// Stored inside SessionState and serialised to Redis for cross-request
// persistence (30-minute TTL).
type CutMarker struct {
	// Version is the schema version of this marker.
	Version int `json:"v"`

	// CreatedAt is the unix timestamp when this cut was made.
	CreatedAt int64 `json:"ts"`

	// SourceMsgCount is the total message count BEFORE compression.
	SourceMsgCount int `json:"src_mc"`

	// SystemMsgCount is how many leading system messages were retained.
	SystemMsgCount int `json:"sys_mc"`

	// CutIndex is the message index in the non-system portion:
	// messages [SystemMsgCount, SystemMsgCount+CutIndex) were summarised.
	// messages [SystemMsgCount+CutIndex, end) were retained verbatim.
	CutIndex int `json:"ci"`

	// SummaryMarker is the smm_v1 hash of the summary content (matches
	// SessionState.SummaryMarker). Used for dedup / cache invalidation.
	SummaryMarker string `json:"smm"`

	// Strategy is which compression strategy produced this cut:
	// "smart_window", "mechanical_trim", "llm_summary", "memora_l1".
	Strategy string `json:"strat"`

	// BytesBefore / BytesAfter for telemetry.
	BytesBefore int `json:"bb"`
	BytesAfter  int `json:"ba"`

	// PreSanitizeOffsetRange (2026-09-01, audit §五) 记录"压缩覆盖的 message 在
	// sanitize 之前的 index 范围 [Start, End)"。语义：原 messages[Start,End)
	// 在 sanitize 之前已被压缩层覆盖（折叠进摘要或丢弃），sanitize 层在
	// [End, SourceMsgCount) 范围内才生效。把三层 offset 串起来，便于跨请求续接。
	// omitempty：旧数据自动读为零值。
	PreSanitizeOffsetRange [2]int `json:"psor,omitempty"`

	// SummaryText is the actual LLM-generated or mechanical summary text.
	// This is what gets prepended on the next request's incremental build.
	// Only stored in L1 (in-process) to avoid large blobs in Redis.
	SummaryText string `json:"-"`
}

// cutMarkerSchemaVersion is the current CutMarker schema version.
const cutMarkerSchemaVersion = 1

// NewCutMarker creates a CutMarker from a CutPlan and additional context.
func NewCutMarker(plan CutPlan, sourceMsgCount int, strategy string, summaryMarker, summaryText string, bytesBefore, bytesAfter int) CutMarker {
	return NewCutMarkerWithPreSanitize(plan, sourceMsgCount, strategy, summaryMarker, summaryText, bytesBefore, bytesAfter, [2]int{0, 0})
}

// NewCutMarkerWithPreSanitize 在 NewCutMarker 基础上多接受一个 preSanitizeRange
// 参数（[Start, End) 表示压缩覆盖的 message 在 sanitize 之前的 index 范围）。
// 旧调用方继续使用 NewCutMarker；新调用方在知道 sanitize 层 index 时使用本函数。
func NewCutMarkerWithPreSanitize(plan CutPlan, sourceMsgCount int, strategy string, summaryMarker, summaryText string, bytesBefore, bytesAfter int, preSanitizeRange [2]int) CutMarker {
	return CutMarker{
		Version:                cutMarkerSchemaVersion,
		CreatedAt:              time.Now().Unix(),
		SourceMsgCount:         sourceMsgCount,
		SystemMsgCount:         plan.SystemCount,
		CutIndex:               plan.CutIndex,
		SummaryMarker:          summaryMarker,
		Strategy:               strategy,
		BytesBefore:            bytesBefore,
		BytesAfter:             bytesAfter,
		PreSanitizeOffsetRange: preSanitizeRange,
		SummaryText:            summaryText,
	}
}

// IsExpired returns true if the cut marker is older than the given TTL.
// Used to decide whether to reuse a cached compression or re-compress.
func (cm CutMarker) IsExpired(ttl time.Duration) bool {
	if cm.CreatedAt <= 0 || ttl <= 0 {
		return true
	}
	created := time.Unix(cm.CreatedAt, 0)
	// A future marker cannot be trusted: accepting it would let a stale or
	// replayed cache entry bypass the intended TTL window.
	if created.After(time.Now().Add(5 * time.Minute)) {
		return true
	}
	return time.Since(created) > ttl
}

// GlobalCutIndex returns the absolute message index (counting system messages)
// where the retained tail begins. This is the index in the original message
// array that the next request should start reading from.
func (cm CutMarker) GlobalCutIndex() int {
	return cm.SystemMsgCount + cm.CutIndex
}

// MarshalForRedis serialises the CutMarker fields (excluding SummaryText) for
// storage in Redis Hash. SummaryText is kept in-process (L1) only.
func (cm CutMarker) MarshalForRedis() map[string]string {
	out := map[string]string{
		"cm_v":     fmt.Sprintf("%d", cm.Version),
		"cm_ts":    fmt.Sprintf("%d", cm.CreatedAt),
		"cm_src":   fmt.Sprintf("%d", cm.SourceMsgCount),
		"cm_sys":   fmt.Sprintf("%d", cm.SystemMsgCount),
		"cm_ci":    fmt.Sprintf("%d", cm.CutIndex),
		"cm_smm":   cm.SummaryMarker,
		"cm_strat": cm.Strategy,
		"cm_bb":    fmt.Sprintf("%d", cm.BytesBefore),
		"cm_ba":    fmt.Sprintf("%d", cm.BytesAfter),
	}
	// PreSanitizeOffsetRange 仅在 Start 或 End 至少有一个非零时写入（omitempty 语义）。
	if cm.PreSanitizeOffsetRange[0] > 0 || cm.PreSanitizeOffsetRange[1] > 0 {
		out["cm_psor0"] = fmt.Sprintf("%d", cm.PreSanitizeOffsetRange[0])
		out["cm_psor1"] = fmt.Sprintf("%d", cm.PreSanitizeOffsetRange[1])
	}
	return out
}

// UnmarshalFromRedis deserialises CutMarker fields from a Redis Hash.
// Returns nil if no cut marker data is present.
func UnmarshalCutMarkerFromRedis(fields map[string]string) *CutMarker {
	version, err := strconv.Atoi(fields["cm_v"])
	if err != nil || version != cutMarkerSchemaVersion {
		return nil
	}
	created, err1 := strconv.ParseInt(fields["cm_ts"], 10, 64)
	source, err2 := strconv.Atoi(fields["cm_src"])
	system, err3 := strconv.Atoi(fields["cm_sys"])
	cut, err4 := strconv.Atoi(fields["cm_ci"])
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || created <= 0 ||
		source <= 0 || system < 0 || cut <= 0 || system+cut > source {
		return nil
	}
	cm := &CutMarker{
		Version: cutMarkerSchemaVersion, CreatedAt: created, SourceMsgCount: source,
		SystemMsgCount: system, CutIndex: cut, SummaryMarker: fields["cm_smm"],
		Strategy: fields["cm_strat"],
	}
	if !isPersistedCutStrategy(cm.Strategy) {
		return nil
	}
	if value, present := fields["cm_psor0"]; present {
		start, err := strconv.Atoi(value)
		if err != nil {
			return nil
		}
		end, err := strconv.Atoi(fields["cm_psor1"])
		if err != nil || start < 0 || start > end || end > source {
			return nil
		}
		cm.PreSanitizeOffsetRange = [2]int{start, end}
	}
	if value, present := fields["cm_bb"]; present {
		if cm.BytesBefore, err = strconv.Atoi(value); err != nil || cm.BytesBefore < 0 {
			return nil
		}
	}
	if value, present := fields["cm_ba"]; present {
		if cm.BytesAfter, err = strconv.Atoi(value); err != nil || cm.BytesAfter < 0 {
			return nil
		}
	}
	return cm
}

// MarshalJSON serialises CutMarker for embedding in compression_meta JSONB.
func (cm CutMarker) MarshalJSON() ([]byte, error) {
	inner := map[string]any{
		"version":          cm.Version,
		"created_at":       cm.CreatedAt,
		"source_msg_count": cm.SourceMsgCount,
		"system_msg_count": cm.SystemMsgCount,
		"cut_index":        cm.CutIndex,
		"strategy":         cm.Strategy,
		"bytes_before":     cm.BytesBefore,
		"bytes_after":      cm.BytesAfter,
	}
	if cm.SummaryMarker != "" {
		inner["summary_marker"] = cm.SummaryMarker
	}
	// PreSanitizeOffsetRange：omitted when both zero (back-compat with legacy JSONB).
	if cm.PreSanitizeOffsetRange[0] > 0 || cm.PreSanitizeOffsetRange[1] > 0 {
		inner["pre_sanitize_offset_range"] = []int{cm.PreSanitizeOffsetRange[0], cm.PreSanitizeOffsetRange[1]}
	}
	return json.Marshal(map[string]any{"cut_marker": inner})
}

// IncrementalBuildTail rebuilds only the retained tail for a mechanical cut.
// It never invents summary text, so it is safe after L1 eviction. LLM summary
// markers are deliberately rejected when their plaintext is unavailable.
func IncrementalBuildTail(incomingBody []byte, marker CutMarker, protocol string) ([]byte, bool) {
	if marker.CutIndex <= 0 || !isTailRecoveryStrategy(marker.Strategy) {
		return nil, false
	}
	if marker.CreatedAt > 0 && marker.IsExpired(sessionCacheRedisTTL()) {
		return nil, false
	}
	var generic map[string]json.RawMessage
	if json.Unmarshal(incomingBody, &generic) != nil {
		return nil, false
	}
	var req struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if json.Unmarshal(incomingBody, &req) != nil || len(req.Messages) == 0 {
		return nil, false
	}
	if marker.SystemMsgCount < 0 || marker.SystemMsgCount > len(req.Messages) ||
		marker.CutIndex > len(req.Messages)-marker.SystemMsgCount {
		return nil, false
	}
	globalCut := marker.GlobalCutIndex()
	if globalCut < marker.SystemMsgCount || globalCut >= len(req.Messages) {
		return nil, false
	}
	if marker.SourceMsgCount > 0 && (marker.SourceMsgCount < globalCut || marker.SourceMsgCount > len(req.Messages)) {
		return nil, false
	}
	psor := marker.PreSanitizeOffsetRange
	if psor[0] < 0 || psor[1] < psor[0] || psor[1] > len(req.Messages) {
		return nil, false
	}
	out := append([]json.RawMessage(nil), req.Messages[:marker.SystemMsgCount]...)
	out = append(out, req.Messages[globalCut:]...)
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, false
	}
	generic["messages"] = raw
	result, err := json.Marshal(generic)
	return result, err == nil
}

func isTailRecoveryStrategy(strategy string) bool {
	return strategy == "mechanical_trim" || strategy == "smart_window_mechanical" ||
		strings.HasPrefix(strategy, "sliding_window_") && strategy != "sliding_window_llm"
}

// IncrementalBuild reconstructs the outbound body for the next request using
// a cached CutMarker. The result is:
//
//	[system messages] + [summary message] + [messages from cut onwards]
//
// This is called when the session cache has a valid (non-expired) CutMarker
// and the incoming request is for the same session.
//
// Parameters:
//   - incomingBody: the full request body from the client (all messages).
//   - marker: the cached CutMarker from the prior compression.
//   - protocol: "openai" or "anthropic-messages".
//
// Returns (rebuiltBody, true) on success, or (nil, false) if the marker is
// stale (e.g. incoming body has fewer messages than the marker's source).
func IncrementalBuild(incomingBody []byte, marker CutMarker, protocol string) ([]byte, bool) {
	if marker.CutIndex < 0 || marker.SummaryText == "" {
		return nil, false
	}

	// P1-11 fix (2026-08-28): Defense-in-depth expiry check. Callers are
	// expected to check IsExpired before invoking this function (see
	// recovery_coordinator.go), but validating here too protects against
	// call sites that forget the check or reuse a marker across sessions.
	// Only check if CreatedAt is set; tests may create markers without timestamps.
	if marker.CreatedAt > 0 && marker.IsExpired(sessionCacheRedisTTL()) {
		return nil, false
	}

	var generic map[string]json.RawMessage
	if err := json.Unmarshal(incomingBody, &generic); err != nil {
		return nil, false
	}
	var req struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(incomingBody, &req); err != nil {
		return nil, false
	}

	// Validate every marker-derived slice bound before slicing. SourceMsgCount
	// was added after the first marker format, so zero remains a valid legacy
	// value and is treated as "unknown". A non-zero PSOR is provenance for the
	// original message array and must be a monotonic, in-range half-open range.
	messageCount := len(req.Messages)
	if marker.SourceMsgCount < 0 || marker.SystemMsgCount < 0 || marker.SystemMsgCount > messageCount ||
		marker.CutIndex < 0 || marker.CutIndex > messageCount-marker.SystemMsgCount {
		return nil, false
	}
	globalCut := marker.GlobalCutIndex()
	if globalCut < marker.SystemMsgCount || globalCut > messageCount {
		return nil, false
	}
	if marker.SourceMsgCount > 0 {
		if marker.SourceMsgCount < globalCut || marker.SourceMsgCount > messageCount {
			return nil, false
		}
	}
	psorStart, psorEnd := marker.PreSanitizeOffsetRange[0], marker.PreSanitizeOffsetRange[1]
	if psorStart < 0 || psorEnd < 0 || psorStart > psorEnd {
		return nil, false
	}
	if psorEnd > messageCount || (marker.SourceMsgCount > 0 && psorEnd > marker.SourceMsgCount) {
		return nil, false
	}
	if globalCut >= messageCount {
		// Incoming body is shorter than the cached cut point — stale marker.
		return nil, false
	}

	systemMsgs := req.Messages[:marker.SystemMsgCount]
	tailMsgs := req.Messages[globalCut:]

	if protocol == "anthropic-messages" {
		// Anthropic summaries belong in the top-level system field, matching
		// proactive compression and SmartCompress's protocol adapter. Never
		// fabricate a user message carrying the summary on this wire format.
		newSystem, err := rebuildAnthropicSystemField(generic["system"], marker.SummaryText)
		if err != nil {
			return nil, false
		}
		raw, err := json.Marshal(tailMsgs)
		if err != nil {
			return nil, false
		}
		generic["system"] = newSystem
		generic["messages"] = raw
		result, err := json.Marshal(generic)
		if err != nil {
			return nil, false
		}
		return result, true
	}

	summaryContent := smartWindowSummaryPrefix + marker.SummaryText
	summaryMsg, _ := json.Marshal(map[string]string{
		"role":    "user",
		"content": summaryContent,
	})

	out := make([]json.RawMessage, 0, len(systemMsgs)+1+len(tailMsgs))
	out = append(out, systemMsgs...)
	out = append(out, summaryMsg)
	out = append(out, tailMsgs...)

	raw, err := json.Marshal(out)
	if err != nil {
		return nil, false
	}
	generic["messages"] = raw
	result, err := json.Marshal(generic)
	if err != nil {
		return nil, false
	}
	return result, true
}
