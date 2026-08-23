package workers

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/analysis/sessionmeta"
	"github.com/kaixuan/llm-gateway-go/domains/sessionsummary"
)

// SessionMessageLoader loads bounded session messages for final metadata extraction.
type SessionMessageLoader interface {
	GetSessionMessages(ctx context.Context, tenantID, sessionKey string) ([]sessionsummary.SessionMessage, error)
}

// SessionMetadataCloseHook UPSERTs status=final into session_analysis_metadata
// after the session summary worker succeeds. Best-effort: failures are logged
// and never fail the close pipeline.
type SessionMetadataCloseHook struct {
	store  *sessionmeta.MetadataStore
	loader SessionMessageLoader
	logger *slog.Logger

	written atomic.Int64
	skipped atomic.Int64
	failed  atomic.Int64
}

func NewSessionMetadataCloseHook(store *sessionmeta.MetadataStore, loader SessionMessageLoader, logger *slog.Logger) *SessionMetadataCloseHook {
	if logger == nil {
		logger = slog.Default()
	}
	return &SessionMetadataCloseHook{store: store, loader: loader, logger: logger}
}

func (h *SessionMetadataCloseHook) OnSessionClosed(ctx context.Context, tenantID, gwSessionID string) error {
	if h == nil || h.store == nil || h.loader == nil {
		return nil
	}
	if tenantID == "" || gwSessionID == "" {
		return nil
	}
	startedAt := time.Now()

	msgs, err := h.loader.GetSessionMessages(ctx, tenantID, gwSessionID)
	if err != nil {
		h.failed.Add(1)
		h.logger.Warn("sessionmeta close hook: load messages failed",
			"tenant_id", tenantID, "session_id", gwSessionID, "error", err)
		return nil
	}
	in := sessionmeta.Input{Messages: toSessionmetaMessages(msgs)}
	result := sessionmeta.Extract(in)
	if err := h.store.UpsertFinal(ctx, tenantID, gwSessionID, "", result, startedAt); err != nil {
		h.failed.Add(1)
		h.logger.Warn("sessionmeta close hook: upsert final failed",
			"tenant_id", tenantID, "session_id", gwSessionID, "error", err)
		return nil
	}
	h.written.Add(1)
	h.logger.Debug("sessionmeta close hook: final metadata upserted",
		"tenant_id", tenantID, "session_id", gwSessionID, "input_hash", result.InputHash)
	return nil
}

func (h *SessionMetadataCloseHook) Stats() map[string]int64 {
	if h == nil {
		return nil
	}
	return map[string]int64{
		"written": h.written.Load(),
		"skipped": h.skipped.Load(),
		"failed":  h.failed.Load(),
	}
}

func toSessionmetaMessages(msgs []sessionsummary.SessionMessage) []sessionmeta.Message {
	out := make([]sessionmeta.Message, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, sessionmeta.Message{Role: m.Role, Content: m.Content})
	}
	return out
}
