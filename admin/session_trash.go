package admin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// W1.5 session trash/restore — aligned with magpie gateway_delete / Restore:
//   - trash moves bodies aside + hides session; usage untouched
//   - active within 1 minute → refuse
//   - restore skips expired trash notes
//   - permanent delete keeps the hidden record

const sessionActiveWindow = time.Minute

var errSessionActive = errors.New("session was written to in the last minute; it may still be running")

func (h *Handler) handleSessionTrash(w http.ResponseWriter, r *http.Request, sessionID, action string) {
	if RequireSuperAdminForWrite(w, r) {
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	tenantID := tenantFromQueryOrContext(r)
	ctx := r.Context()
	switch action {
	case "trash":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST required")
			return
		}
		res, err := h.trashSession(ctx, tenantID, sessionID)
		if errors.Is(err, errSessionActive) {
			writeErrorWithCode(w, http.StatusConflict, "session_active", err.Error())
			return
		}
		if err != nil {
			writeInternalErr(w, "trash failed", err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	case "restore":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST required")
			return
		}
		res, err := h.restoreSession(ctx, tenantID, sessionID)
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, res)
	case "purge":
		if r.Method != http.MethodPost && r.Method != http.MethodDelete {
			writeError(w, http.StatusMethodNotAllowed, "POST or DELETE required")
			return
		}
		res, err := h.purgeSessionTrash(ctx, tenantID, sessionID)
		if err != nil {
			writeInternalErr(w, "purge failed", err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	default:
		writeError(w, http.StatusNotFound, "unknown trash action")
	}
}

func (h *Handler) trashSession(ctx context.Context, tenantID, sessionID string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	var lastAt time.Time
	err := h.db.QueryRow(ctx, `
		SELECT COALESCE(updated_at, created_at, NOW())
		FROM public.sessions
		WHERE tenant_id = $1 AND session_id = $2
		ORDER BY partition_date DESC
		LIMIT 1`, tenantID, sessionID).Scan(&lastAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("no such session")
		}
		return nil, err
	}
	if time.Since(lastAt) < sessionActiveWindow {
		return nil, errSessionActive
	}

	tx, err := h.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		INSERT INTO public.session_bodies_trash (
			tenant_id, session_id, turn_no, request_id, ts,
			request_delta, response_delta, outbound_body,
			request_attachments, response_attachments, kind, partition_date,
			trashed_at
		)
		SELECT tenant_id, session_id, turn_no, request_id, ts,
			request_delta, response_delta, outbound_body,
			request_attachments, response_attachments, kind, partition_date,
			NOW()
		FROM public.session_bodies_unified
		WHERE tenant_id = $1 AND session_id = $2
		ON CONFLICT DO NOTHING`, tenantID, sessionID)
	if err != nil {
		return nil, err
	}
	moved := int(tag.RowsAffected())

	_, _ = tx.Exec(ctx, `DELETE FROM public.session_bodies_hot WHERE tenant_id = $1 AND session_id = $2`, tenantID, sessionID)
	_, _ = tx.Exec(ctx, `DELETE FROM public.session_bodies WHERE tenant_id = $1 AND session_id = $2`, tenantID, sessionID)
	_, err = tx.Exec(ctx, `
		UPDATE public.sessions SET status = 'deleted', updated_at = NOW()
		WHERE tenant_id = $1 AND session_id = $2`, tenantID, sessionID)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO public.session_hidden (tenant_id, session_id, hidden_until, last_seen_at, reason)
		VALUES ($1, $2, $3, $4, 'trash')
		ON CONFLICT (tenant_id, session_id) DO UPDATE
		  SET hidden_until = EXCLUDED.hidden_until,
		      last_seen_at = EXCLUDED.last_seen_at,
		      reason = EXCLUDED.reason`,
		tenantID, sessionID, lastAt, lastAt)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return map[string]any{
		"tenant_id":    tenantID,
		"session_id":   sessionID,
		"status":       "trashed",
		"bodies_moved": moved,
		"usage_kept":   true,
		"hidden_until": lastAt.UTC().Format(time.RFC3339),
	}, nil
}

func (h *Handler) restoreSession(ctx context.Context, tenantID, sessionID string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	var trashedAt *time.Time
	err := h.db.QueryRow(ctx, `
		SELECT MIN(trashed_at) FROM public.session_bodies_trash
		WHERE tenant_id = $1 AND session_id = $2`, tenantID, sessionID).Scan(&trashedAt)
	if err != nil || trashedAt == nil || trashedAt.IsZero() {
		return nil, errors.New("no such deleted session (or trash expired)")
	}
	if time.Since(*trashedAt) > 168*time.Hour {
		return nil, errors.New("trash expired; nothing was restored")
	}

	tx, err := h.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		INSERT INTO public.session_bodies_hot (
			session_id, turn_no, tenant_id, request_id, ts,
			request_delta, response_delta, outbound_body,
			request_attachments, response_attachments, kind, partition_date
		)
		SELECT session_id, turn_no, tenant_id, request_id, ts,
			request_delta, response_delta, outbound_body,
			request_attachments, response_attachments, kind, partition_date
		FROM public.session_bodies_trash
		WHERE tenant_id = $1 AND session_id = $2
		ON CONFLICT (tenant_id, request_id, partition_date) DO NOTHING`, tenantID, sessionID)
	if err != nil {
		return nil, err
	}
	restored := int(tag.RowsAffected())
	_, _ = tx.Exec(ctx, `DELETE FROM public.session_bodies_trash WHERE tenant_id = $1 AND session_id = $2`, tenantID, sessionID)
	_, _ = tx.Exec(ctx, `
		UPDATE public.sessions SET status = 'active', updated_at = NOW()
		WHERE tenant_id = $1 AND session_id = $2`, tenantID, sessionID)
	_, _ = tx.Exec(ctx, `DELETE FROM public.session_hidden WHERE tenant_id = $1 AND session_id = $2`, tenantID, sessionID)
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return map[string]any{
		"tenant_id":       tenantID,
		"session_id":      sessionID,
		"status":          "restored",
		"bodies_restored": restored,
	}, nil
}

func (h *Handler) purgeSessionTrash(ctx context.Context, tenantID, sessionID string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	tag, err := h.db.Exec(ctx, `
		DELETE FROM public.session_bodies_trash
		WHERE tenant_id = $1 AND session_id = $2`, tenantID, sessionID)
	if err != nil {
		return nil, err
	}
	_, _ = h.db.Exec(ctx, `
		INSERT INTO public.session_hidden (tenant_id, session_id, hidden_until, last_seen_at, reason)
		VALUES ($1, $2, NOW(), NOW(), 'purge')
		ON CONFLICT (tenant_id, session_id) DO UPDATE
		  SET reason = 'purge', last_seen_at = NOW()`, tenantID, sessionID)

	return map[string]any{
		"tenant_id":     tenantID,
		"session_id":    sessionID,
		"status":        "purged",
		"bodies_purged": int(tag.RowsAffected()),
		"hidden_kept":   true,
	}, nil
}
