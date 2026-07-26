package streaming

import (
	"context"
	"log/slog"
	"time"
)

// Data-loss anomaly types recorded to response_format_anomalies and surfaced on
// the /format-anomalies page.
//
// These name a specific defect class: request or response content silently
// discarded on a fallback path. A dropped body is indistinguishable from a
// request that never carried one, so without an explicit record the loss is
// invisible — that ambiguity is what let a request_body loss bug survive six
// fix attempts (see docs/VIBECODING_GUIDELINES.md).
const (
	// AnomalyToolsRestoreFailed: the compressor cached the client's tools array
	// and restoring it into the outbound body failed. The upstream provider
	// receives a request with no tools, so a model that cannot call tools is
	// indistinguishable from one that chose not to.
	AnomalyToolsRestoreFailed = "tools_restore_failed"

	// AnomalyRequestBodyTruncated: the request body was truncated by a read
	// timeout or the size limit. The partial body is both persisted AND
	// forwarded upstream, so the row looks like a normal short request.
	AnomalyRequestBodyTruncated = "request_body_truncated"

	// AnomalyBodyDecodeFailed: a stored body could not be decoded for display,
	// so the API emits null — identical to "no body was stored for this turn".
	AnomalyBodyDecodeFailed = "body_decode_failed"

	// AnomalyMetadataDropped: request metadata (routing attempts, auto-route
	// decision, attachments, quality-fix actions) was lost on a marshal or
	// unmarshal failure.
	AnomalyMetadataDropped = "metadata_dropped"
)

// anomalyRecordTimeout bounds the out-of-band write so recording a data loss
// can never stall or fail the request that suffered it.
const anomalyRecordTimeout = 3 * time.Second

// recordDataLoss logs a silent-drop event and records it to
// response_format_anomalies for the /format-anomalies page.
//
// It deliberately takes sizes and reasons rather than the lost content itself:
// these payloads are user prompts and must not reach logs or the anomaly table.
//
// Safe to call with a nil recorder — the log line is still emitted, so a
// deployment without the recorder wired up still surfaces the loss.
func (h *ChatHandler) recordDataLoss(ctx context.Context, anomalyType, severity, requestID, message string, metadata map[string]any) {
	slog.Warn("data loss: "+anomalyType,
		append([]any{"request_id", requestID, "detail", message}, flattenMetadata(metadata)...)...)

	if h == nil || h.anomalyRecorder == nil {
		return
	}
	// context.WithoutCancel: the loss must be recorded even when the request
	// context is already cancelled — client disconnects are precisely when
	// truncation happens.
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), anomalyRecordTimeout)
	defer cancel()
	if err := h.anomalyRecorder.RecordDataAnomaly(recordCtx, anomalyType, severity, requestID, message, metadata); err != nil {
		slog.Warn("failed to record data loss anomaly",
			"request_id", requestID, "anomaly_type", anomalyType, "error", err)
	}
}

func flattenMetadata(metadata map[string]any) []any {
	out := make([]any, 0, len(metadata)*2)
	for k, v := range metadata {
		out = append(out, k, v)
	}
	return out
}
