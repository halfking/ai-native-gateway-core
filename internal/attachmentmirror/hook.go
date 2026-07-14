// Package attachmentmirror mirrors request_logs.attachments JSONB
// (migration 325) into the relational public.request_attachments
// table (migration 401).
//
// Why a separate package: domains/attachments cannot import
// domains/hooks/observability/telemetry without creating an import
// cycle (telemetry reaches attachments through domains/session).
// Putting the hook in a leaf package that depends on BOTH avoids
// the cycle while keeping the wire-up close to where the data lives.
//
// Best-effort contract: the returned hook logs and continues on any
// error; it never propagates failures up to telemetry. The primary
// request_logs INSERT must always succeed.
package attachmentmirror

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/attachments"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// PersistHook returns a telemetry "onPersisted" hook that mirrors the
// request_logs.attachments JSONB column into public.request_attachments.
//
// The hook is a no-op when:
//   - repo is nil (caller forgot to wire it),
//   - entry.Attachments is empty / missing,
//   - the JSON payload fails to parse (logically inconsistent JSONB).
//
// On real DB errors it logs WARN with the request_id and continues.
func PersistHook(repo *attachments.Repository) func(entry *telemetry.RequestLogEntry) {
	if repo == nil {
		return func(*telemetry.RequestLogEntry) {}
	}
	return func(entry *telemetry.RequestLogEntry) {
		if entry == nil || len(entry.Attachments) == 0 {
			return
		}
		var meta []attachments.AttachmentMetadata
		if err := json.Unmarshal(entry.Attachments, &meta); err != nil || len(meta) == 0 {
			return
		}
		rows := make([]attachments.RequestAttachmentRow, 0, len(meta))
		for _, m := range meta {
			rows = append(rows, attachments.ToRow(entry.RequestID, m))
		}

		// Bound the secondary write so a slow DB cannot stall telemetry.
		// 500ms is generous for a tiny INSERT batch and well below the
		// 30s queue flush the RequestLogger tolerates.
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		if _, err := repo.InsertBatch(ctx, rows); err != nil {
			slog.Warn("attachmentmirror: persist into request_attachments failed",
				"request_id", entry.RequestID,
				"count", len(rows),
				"error", err)
		}
	}
}
