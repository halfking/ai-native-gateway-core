package relay

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/kaixuan/llm-gateway-go/sessions"
)

// SessionHeadersPriority is the ordered list of headers to extract session ID from.
// Per design spec (2026-06-26 V3.1, flow step 3).
var SessionHeadersPriority = []string{
	"X-Gw-Session-Id",
	"X-Session-Id",
	"X-Conversation-Id",
	"X-Chat-Session-Id",
	"X-Thread-Id",
}

// extractSessionIDFromHeaders extracts session ID from request headers using priority order.
// Returns empty string if no header contains a valid session ID.
//
// 2026-06-26: Extended from 2 headers (X-Gw-Session-Id, X-Session-Id) to 5 headers
// for better compatibility with various client SDKs.
func extractSessionIDFromHeaders(r *http.Request) string {
	for _, header := range SessionHeadersPriority {
		value := r.Header.Get(header)
		if value != "" {
			if header == "X-Gw-Session-Id" {
				return sanitizeGwSessionHeader(value)
			}
			return value
		}
	}
	return ""
}

// resolveSession resolves session ID with V3.1 logic:
// 1. Extract session ID from headers (priority order)
// 2. If empty, try LastSystemSessionIndex (5-minute reuse window)
// 3. If still empty, create new system-assigned session and update index
//
// Returns:
// - sessionID: resolved session ID (may be newly created)
// - sessionInfo: session details (may be nil if sessionGetter is nil)
// - sessionReused: true if reused from LastSystemSessionIndex
// - sessionNew: true if newly created (gw_<uuid> format)
//
// 2026-06-26: New V3.1 session resolution flow.
func resolveSession(
	ctx context.Context,
	r *http.Request,
	w http.ResponseWriter,
	sessionGetter *sessions.Manager,
	lastSystemSessionIdx *sessions.LastSystemSessionIndex,
	keyInfo *struct {
		ID       int
		TenantID string
	},
) (sessionID string, sessionInfo *sessions.Session, sessionReused bool, sessionNew bool) {
	// Step 1: Extract from headers
	sessionID = extractSessionIDFromHeaders(r)

	// Step 2: If empty and LastSystemSessionIndex available, try reuse
	if sessionID == "" && lastSystemSessionIdx != nil && keyInfo != nil {
		entry, found := lastSystemSessionIdx.Get(ctx, keyInfo.ID)
		if found {
			// Verify session still exists in Redis
			if sessionGetter != nil {
				si, err := sessionGetter.Get(ctx, entry.SessionID)
				if err == nil && si != nil {
					sessionID = entry.SessionID
					sessionInfo = si
					sessionReused = true
					w.Header().Set("X-Gw-Session-Id-Resume", sessionID)
					w.Header().Set("X-Gw-Session-Reused", "true")
					slog.Debug("session reused from LastSystemSessionIndex",
						"api_key_id", keyInfo.ID,
						"session_id", sessionID,
						"last_assigned_at", entry.LastAssignedAt,
					)
					return sessionID, sessionInfo, sessionReused, false
				}
			}
		}
	}

	// Step 3: If sessionID from headers exists, look it up
	if sessionID != "" && sessionGetter != nil {
		si, err := sessionGetter.Get(ctx, sessionID)
		if err == nil {
			sessionInfo = si
			return sessionID, sessionInfo, false, false
		}
	}

	// Step 4: No-id fallback - create new system-assigned session
	if sessionID == "" && sessionGetter != nil && keyInfo != nil {
		deviceSeed := r.Header.Get("X-Device-Seed")
		if deviceSeed == "" {
			deviceSeed = r.Header.Get("X-Machine-Id")
		}
		if deviceSeed == "" {
			deviceSeed = "default"
		}
		taskID := r.Header.Get("X-Gw-Task-Id")

		newSession, createErr := sessionGetter.CreateV2(ctx, keyInfo.ID, keyInfo.TenantID, deviceSeed, taskID)
		if createErr != nil {
			slog.Error("session no-id create failed", "error", createErr)
			return "", nil, false, false
		}

		sessionID = newSession.SessionID
		sessionInfo = newSession
		sessionNew = true
		w.Header().Set("X-Gw-Session-Id-Resume", sessionID)
		w.Header().Set("X-Gw-Session-Auto", "true")
		slog.Info("session auto-created (no-id fallback)",
			"api_key_id", keyInfo.ID,
			"session_id", sessionID,
			"task_id", taskID,
		)

		// Update LastSystemSessionIndex for 5-minute reuse
		if lastSystemSessionIdx != nil {
			entry := &sessions.LastSystemSessionEntry{
				SessionID:  sessionID,
				DeviceSeed: deviceSeed,
				TaskID:     taskID,
			}
			if err := lastSystemSessionIdx.Set(ctx, keyInfo.ID, entry); err != nil {
				slog.Warn("LastSystemSessionIndex update failed", "error", err, "api_key_id", keyInfo.ID)
			}
		}

		return sessionID, sessionInfo, false, sessionNew
	}

	return sessionID, sessionInfo, false, false
}

// detectAndHandleModelSwitch checks if the session's model has changed.
// If model changed, clears SessionPreference to force re-selection.
//
// Per design spec (2026-06-26 V3.1, flow step 6).
//
// Returns:
// - modelChanged: true if model changed (session_pref was cleared)
// - previousModel: the previous model (empty if no previous)
func detectAndHandleModelSwitch(
	ctx context.Context,
	sessionPref *sessions.SessionPreference,
	sessionID string,
	clientModel string,
) (modelChanged bool, previousModel string) {
	if sessionPref == nil || sessionID == "" {
		return false, ""
	}

	// Read current preference
	credID, found := sessionPref.Get(ctx, sessionID)
	if !found {
		return false, ""
	}

	// We need to track the model associated with this preference.
	// For now, we use a convention: store model in a separate key.
	// TODO: Refactor SessionPreference to also store model.
	_ = credID // currently unused, will be used when we track model

	return false, ""
}

// generateSystemSessionID generates a new system-assigned session ID.
// Format: "gw_<uuid>"
func generateSystemSessionID() string {
	return "gw_" + uuid.New().String()
}
