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

	// SummaryText is the actual LLM-generated or mechanical summary text.
	// This is what gets prepended on the next request's incremental build.
	// Only stored in L1 (in-process) to avoid large blobs in Redis.
	SummaryText string `json:"-"`
}

// cutMarkerSchemaVersion is the current CutMarker schema version.
const cutMarkerSchemaVersion = 1

// NewCutMarker creates a CutMarker from a CutPlan and additional context.
func NewCutMarker(plan CutPlan, sourceMsgCount int, strategy string, summaryMarker, summaryText string, bytesBefore, bytesAfter int) CutMarker {
	return CutMarker{
		Version:        cutMarkerSchemaVersion,
		CreatedAt:      time.Now().Unix(),
		SourceMsgCount: sourceMsgCount,
		SystemMsgCount: plan.SystemCount,
		CutIndex:       plan.CutIndex,
		SummaryMarker:  summaryMarker,
		Strategy:       strategy,
		BytesBefore:    bytesBefore,
		BytesAfter:     bytesAfter,
		SummaryText:    summaryText,
	}
}

// IsExpired returns true if the cut marker is older than the given TTL.
// Used to decide whether to reuse a cached compression or re-compress.
func (cm CutMarker) IsExpired(ttl time.Duration) bool {
	if cm.CreatedAt == 0 {
		return true
	}
	return time.Since(time.Unix(cm.CreatedAt, 0)) > ttl
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
	return map[string]string{
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
}

// UnmarshalFromRedis deserialises CutMarker fields from a Redis Hash.
// Returns nil if no cut marker data is present.
func UnmarshalCutMarkerFromRedis(fields map[string]string) *CutMarker {
	if _, ok := fields["cm_v"]; !ok {
		return nil
	}
	cm := &CutMarker{}
	parseInt64(fields["cm_ts"], &cm.CreatedAt)
	parseInt(fields["cm_src"], &cm.SourceMsgCount)
	parseInt(fields["cm_sys"], &cm.SystemMsgCount)
	parseInt(fields["cm_ci"], &cm.CutIndex)
	cm.SummaryMarker = fields["cm_smm"]
	cm.Strategy = fields["cm_strat"]
	parseInt(fields["cm_bb"], &cm.BytesBefore)
	parseInt(fields["cm_ba"], &cm.BytesAfter)
	cm.Version = cutMarkerSchemaVersion
	return cm
}

// MarshalJSON serialises CutMarker for embedding in compression_meta JSONB.
func (cm CutMarker) MarshalJSON() ([]byte, error) {
	m := map[string]any{
		"cut_marker": map[string]any{
			"version":          cm.Version,
			"created_at":       cm.CreatedAt,
			"source_msg_count": cm.SourceMsgCount,
			"system_msg_count": cm.SystemMsgCount,
			"cut_index":        cm.CutIndex,
			"strategy":         cm.Strategy,
			"bytes_before":     cm.BytesBefore,
			"bytes_after":      cm.BytesAfter,
		},
	}
	if cm.SummaryMarker != "" {
		m["cut_marker"].(map[string]any)["summary_marker"] = cm.SummaryMarker
	}
	return json.Marshal(m)
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

	globalCut := marker.GlobalCutIndex()
	if globalCut >= len(req.Messages) {
		// Incoming body is shorter than the cached cut point — stale marker.
		return nil, false
	}

	systemMsgs := req.Messages[:marker.SystemMsgCount]
	tailMsgs := req.Messages[globalCut:]

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

func parseInt64(s string, dst *int64) {
	var v int64
	fmt.Sscanf(s, "%d", &v)
	*dst = v
}

func parseInt(s string, dst *int) {
	var v int
	fmt.Sscanf(s, "%d", &v)
	*dst = v
}
