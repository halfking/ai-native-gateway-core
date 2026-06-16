package sessions

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/auth"
)

type Handler struct {
	manager     *Manager
	keyVerifier *auth.KeyVerifier
}

func NewHandler(manager *Manager) *Handler {
	return &Handler{manager: manager}
}

func (h *Handler) SetAuth(kv *auth.KeyVerifier) {
	h.keyVerifier = kv
}

func extractBearerToken(r *http.Request) string {
	if authHdr := r.Header.Get("Authorization"); authHdr != "" {
		if strings.HasPrefix(authHdr, "Bearer ") {
			return strings.TrimPrefix(authHdr, "Bearer ")
		}
		if strings.HasPrefix(authHdr, "bearer ") {
			return strings.TrimPrefix(authHdr, "bearer ")
		}
	}
	if key := r.Header.Get("x-api-key"); key != "" {
		return key
	}
	return ""
}

// authenticate verifies sk-* API keys and injects api_key_id + tenant_id into context.
func (h *Handler) authenticate(w http.ResponseWriter, r *http.Request) (context.Context, bool) {
	if h.keyVerifier == nil || !h.keyVerifier.Enabled() {
		return r.Context(), true
	}
	rawKey := extractBearerToken(r)
	if rawKey == "" {
		writeErrorJSON(w, http.StatusUnauthorized, "", "Missing API key", "authentication_error", "MISSING_KEY")
		return nil, false
	}
	ki, err := h.keyVerifier.Verify(r.Context(), rawKey)
	if err != nil {
		if _, ok := err.(*auth.InvalidKeyError); ok {
			writeErrorJSON(w, http.StatusUnauthorized, "", "Invalid or expired API key", "authentication_error", "INVALID_KEY")
		} else {
			writeErrorJSON(w, http.StatusServiceUnavailable, "", "Authentication service temporarily unavailable", "server_error", "AUTH_UNAVAILABLE")
		}
		return nil, false
	}
	ctx := SetAPIKeyID(r.Context(), ki.ID)
	ctx = SetTenantID(ctx, ki.TenantID)
	return ctx, true
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	r = r.WithContext(ctx)

	path := r.URL.Path

	if path == "/v1/sessions" && r.Method == http.MethodPost {
		h.CreateSession(w, r)
		return
	}

	if path == "/v1/sessions/migrate" && r.Method == http.MethodPost {
		h.MigrateSession(w, r)
		return
	}

	if strings.HasPrefix(path, "/v1/sessions/") {
		sessionID := strings.TrimPrefix(path, "/v1/sessions/")
		if sessionID == "" {
			writeErrorJSON(w, http.StatusBadRequest, "", "missing session id", "session_error", "MISSING_SESSION_ID")
			return
		}

		switch r.Method {
		case http.MethodGet:
			h.GetSessionByID(w, r, sessionID)
		case http.MethodDelete:
			h.DeleteSessionByID(w, r, sessionID)
		default:
			writeErrorJSON(w, http.StatusMethodNotAllowed, "", "method not allowed", "session_error", "METHOD_NOT_ALLOWED")
		}
		return
	}

	writeErrorJSON(w, http.StatusNotFound, "", "not found", "session_error", "NOT_FOUND")
}

type createSessionRequest struct {
	TaskID   string            `json:"task_id,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

func (h *Handler) CreateSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErrorJSON(w, http.StatusMethodNotAllowed, "", "method not allowed", "session_error", "METHOD_NOT_ALLOWED")
		return
	}

	apiKeyID := GetAPIKeyIDFromContext(r.Context())
	tenantID := getTenantIDFromContext(r.Context())
	deviceSeed := r.Header.Get("X-Device-Seed")

	if deviceSeed == "" {
		deviceSeed = r.Header.Get("X-Machine-Id")
	}
	if deviceSeed == "" {
		deviceSeed = "default"
	}

	var body createSessionRequest
	json.NewDecoder(r.Body).Decode(&body)

	taskID := body.TaskID
	if taskID == "" {
		taskID = r.Header.Get("X-Gw-Task-Id")
	}

	session, err := h.manager.CreateV2(r.Context(), apiKeyID, tenantID, deviceSeed, taskID)
	if err != nil {
		writeErrorJSON(w, http.StatusInternalServerError, "", "failed to create session", "session_error", "SESSION_CREATE_FAILED")
		return
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"session_id":  session.SessionID,
		"session_key": session.SessionKey,
		"expires_at":  session.ExpiresAt.Format(time.RFC3339),
		"created_at":  session.CreatedAt.Format(time.RFC3339),
	})
}

func (h *Handler) GetSessionByID(w http.ResponseWriter, r *http.Request, sessionID string) {
	session, err := h.manager.Get(r.Context(), sessionID)
	if err != nil {
		if err == ErrSessionNotFound {
			writeErrorJSON(w, http.StatusNotFound, "", "session not found", "session_error", "SESSION_NOT_FOUND")
		} else if err == ErrSessionExpired {
			writeErrorJSON(w, http.StatusGone, "", "session expired", "session_error", "SESSION_EXPIRED")
		} else {
			writeErrorJSON(w, http.StatusInternalServerError, "", "failed to get session", "session_error", "SESSION_GET_FAILED")
		}
		return
	}

	apiKeyID := GetAPIKeyIDFromContext(r.Context())
	if session.GetAPIKeyID() != apiKeyID {
		writeErrorJSON(w, http.StatusForbidden, "", "session not owned by this API key", "session_error", "SESSION_FORBIDDEN")
		return
	}

	json.NewEncoder(w).Encode(session)
}

func (h *Handler) DeleteSessionByID(w http.ResponseWriter, r *http.Request, sessionID string) {
	apiKeyID := GetAPIKeyIDFromContext(r.Context())

	session, err := h.manager.Get(r.Context(), sessionID)
	if err != nil {
		if err == ErrSessionNotFound {
			writeErrorJSON(w, http.StatusNotFound, "", "session not found", "session_error", "SESSION_NOT_FOUND")
		} else {
			writeErrorJSON(w, http.StatusInternalServerError, "", "failed to get session", "session_error", "SESSION_GET_FAILED")
		}
		return
	}

	if session.GetAPIKeyID() != apiKeyID {
		writeErrorJSON(w, http.StatusForbidden, "", "session not owned by this API key", "session_error", "SESSION_FORBIDDEN")
		return
	}

	if err := h.manager.Delete(r.Context(), sessionID); err != nil {
		writeErrorJSON(w, http.StatusInternalServerError, "", "failed to delete session", "session_error", "SESSION_DELETE_FAILED")
		return
	}

	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (h *Handler) MigrateSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErrorJSON(w, http.StatusMethodNotAllowed, "", "method not allowed", "session_error", "METHOD_NOT_ALLOWED")
		return
	}

	var body struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, "", "invalid request body", "session_error", "INVALID_REQUEST")
		return
	}

	if body.SessionID == "" {
		writeErrorJSON(w, http.StatusBadRequest, "", "missing session_id", "session_error", "MISSING_SESSION_ID")
		return
	}

	newDeviceSeed := r.Header.Get("X-Device-Seed")
	if newDeviceSeed == "" {
		newDeviceSeed = r.Header.Get("X-Machine-Id")
	}
	if newDeviceSeed == "" {
		writeErrorJSON(w, http.StatusBadRequest, "", "missing device seed", "session_error", "MISSING_DEVICE_SEED")
		return
	}

	apiKeyID := GetAPIKeyIDFromContext(r.Context())

	existingSession, err := h.manager.Get(r.Context(), body.SessionID)
	if err != nil {
		if err == ErrSessionNotFound {
			writeErrorJSON(w, http.StatusNotFound, "", "session not found", "session_error", "SESSION_NOT_FOUND")
		} else {
			writeErrorJSON(w, http.StatusInternalServerError, "", "failed to get session", "session_error", "SESSION_GET_FAILED")
		}
		return
	}

	if existingSession.GetAPIKeyID() != apiKeyID {
		writeErrorJSON(w, http.StatusForbidden, "", "session not owned by this API key", "session_error", "SESSION_FORBIDDEN")
		return
	}

	session, err := h.manager.Migrate(r.Context(), body.SessionID, newDeviceSeed)
	if err != nil {
		writeErrorJSON(w, http.StatusInternalServerError, "", "failed to migrate session", "session_error", "SESSION_MIGRATE_FAILED")
		return
	}

	json.NewEncoder(w).Encode(map[string]any{
		"session_id":   session.SessionID,
		"session_key":  session.SessionKey,
		"migrated_to":  newDeviceSeed,
		"devices":      session.Devices,
		"expires_at":   session.ExpiresAt.Format(time.RFC3339),
	})
}

func writeErrorJSON(w http.ResponseWriter, status int, requestID, msg, errType, code string) {
	w.Header().Set("Content-Type", "application/json")
	if requestID != "" {
		w.Header().Set("X-Request-Id", requestID)
	}
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"message":    msg,
			"type":       errType,
			"code":       code,
			"request_id": requestID,
		},
	})
}