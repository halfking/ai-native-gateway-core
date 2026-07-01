package session

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// KeyVerifier 是 Handler 用来验证 API key 的抽象。
// 真实的实现可能在旧 authentication.KeyVerifier 或新 domains/authentication.Verifier。
// 用 interface 解耦，避免 domains/session 直接 import auth/。
type KeyVerifier interface {
	Enabled() bool
	Verify(ctx context.Context, rawKey string) (KeyInfo, error)
}

// KeyInfo 是验证后的 key 信息。
type KeyInfo struct {
	ID       int
	TenantID string
}

// InvalidKeyError 表示"key 无效/过期"错误。
type InvalidKeyError struct {
	Message string
}

func (e *InvalidKeyError) Error() string { return e.Message }

type Handler struct {
	manager     *Manager
	keyVerifier KeyVerifier
	// pendingStore is the durable cache for client reconnect (Track
	// C). The interface is defined here (not imported from the
	// pending package) to avoid an import cycle: pending/ would
	// otherwise need session.Session for tenant-isolation checks.
	//
	// Methods:
	//   Get(ctx, sessionID, requestID) → (*PendingEntry, bool, error)
	//   GetLatest(ctx, sessionID) → (*PendingEntry, requestID, bool, error)
	//
	// May be nil — the GET endpoint then returns 503 Service Unavailable
	// with a clear error so callers know the cache is not configured.
	pendingStore PendingStore
}

// PendingEntry is the minimal subset of the cached pending entry
// that the session handler needs. Defined here (not imported from
// the pending package) to avoid an import cycle. main.go constructs
// an adapter that converts the pending package's view to this
// struct before handing it to SetPendingStore.
type PendingEntry struct {
	SessionID    string
	TenantID     string
	RequestID    string
	Status       string
	Body         string
	ContentType  string
	ProviderID   int
	CredentialID int
	IsStream     bool
	CompletedAt  int64
	ErrorMessage string
}

// PendingStore is the consumer-side interface. The concrete
// adapter lives in main.go (which can import both session and
// pending) and converts pending.EntryView → PendingEntry.
type PendingStore interface {
	Get(ctx context.Context, sessionID, requestID string) (entry *PendingEntry, found bool, err error)
	GetLatest(ctx context.Context, sessionID string) (entry *PendingEntry, requestID string, found bool, err error)
}

func NewHandler(manager *Manager) *Handler {
	return &Handler{manager: manager}
}

// SetAuth 安装 KeyVerifier 抽象。
// 旧 authentication.KeyVerifier 与新 domains/authentication.Verifier 都通过
// 适配器实现此接口。
func (h *Handler) SetAuth(kv KeyVerifier) {
	h.keyVerifier = kv
}

// SetPendingStore (Track C, 2026-06-18) installs the durable cache
// for client reconnect. nil disables the GET endpoint (returns 503).
func (h *Handler) SetPendingStore(s PendingStore) {
	h.pendingStore = s
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
		if _, ok := err.(*InvalidKeyError); ok {
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
		rest := strings.TrimPrefix(path, "/v1/sessions/")
		if rest == "" {
			writeErrorJSON(w, http.StatusBadRequest, "", "missing session id", "session_error", "MISSING_SESSION_ID")
			return
		}

		if strings.HasSuffix(rest, "/pending-response") {
			sessionID := strings.TrimSuffix(rest, "/pending-response")
			sessionID = strings.TrimSuffix(sessionID, "/")
			if sessionID == "" {
				writeErrorJSON(w, http.StatusBadRequest, "", "missing session id", "session_error", "MISSING_SESSION_ID")
				return
			}
			if r.Method != http.MethodGet {
				writeErrorJSON(w, http.StatusMethodNotAllowed, "", "method not allowed", "session_error", "METHOD_NOT_ALLOWED")
				return
			}
			h.getPendingResponse(w, r, sessionID)
			return
		}

		switch r.Method {
		case http.MethodGet:
			h.GetSessionByID(w, r, rest)
		case http.MethodDelete:
			h.DeleteSessionByID(w, r, rest)
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
	//nolint:errcheck // test parse, non-critical
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
	//nolint:errcheck // HTTP write error non-recoverable
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

	//nolint:errcheck // HTTP write error non-recoverable
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

	//nolint:errcheck // HTTP write error non-recoverable
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

	//nolint:errcheck // HTTP write error non-recoverable
	json.NewEncoder(w).Encode(map[string]any{
		"session_id":  session.SessionID,
		"session_key": session.SessionKey,
		"migrated_to": newDeviceSeed,
		"devices":     session.Devices,
		"expires_at":  session.ExpiresAt.Format(time.RFC3339),
	})
}

func writeErrorJSON(w http.ResponseWriter, status int, requestID, msg, errType, code string) {
	w.Header().Set("Content-Type", "application/json")
	if requestID != "" {
		w.Header().Set("X-Request-Id", requestID)
	}
	w.WriteHeader(status)
	//nolint:errcheck // HTTP write error non-recoverable
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"message":    msg,
			"type":       errType,
			"code":       code,
			"request_id": requestID,
		},
	})
}

// getPendingResponse (Track C, 2026-06-18) is the client reconnect endpoint.
func (h *Handler) getPendingResponse(w http.ResponseWriter, r *http.Request, sessionID string) {
	if h.pendingStore == nil {
		writeErrorJSON(w, http.StatusServiceUnavailable, "",
			"pending response cache not configured", "server_error", "PENDING_STORE_UNAVAILABLE")
		return
	}

	apiKeyID := GetAPIKeyIDFromContext(r.Context())
	tenantID := getTenantIDFromContext(r.Context())
	if h.manager != nil {
		if session, err := h.manager.Get(r.Context(), sessionID); err == nil && session != nil {
			if session.GetAPIKeyID() != apiKeyID {
				writeErrorJSON(w, http.StatusForbidden, "", "session not owned by this API key", "session_error", "SESSION_FORBIDDEN")
				return
			}
		}
	}

	requestID := r.URL.Query().Get("request_id")

	var entry *PendingEntry
	var found bool
	var err error
	if requestID != "" {
		entry, found, err = h.pendingStore.Get(r.Context(), sessionID, requestID)
	} else {
		_, _, found, err = h.pendingStore.GetLatest(r.Context(), sessionID)
	}
	if err != nil {
		writeErrorJSON(w, http.StatusServiceUnavailable, "",
			"pending store error: "+err.Error(), "server_error", "PENDING_STORE_ERROR")
		return
	}
	if !found || entry == nil {
		writeErrorJSON(w, http.StatusNotFound, "",
			"no pending response for this session", "session_error", "PENDING_NOT_FOUND")
		return
	}

	if entry.TenantID != "" && entry.TenantID != tenantID {
		writeErrorJSON(w, http.StatusNotFound, "",
			"no pending response for this session", "session_error", "PENDING_NOT_FOUND")
		return
	}

	switch entry.Status {
	case "in_progress":
		w.Header().Set("Retry-After", "5")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":      "in_progress",
			"session_id":  entry.SessionID,
			"request_id":  entry.RequestID,
			"retry_after": 5,
		})

	case "failed":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":        "failed",
			"session_id":    entry.SessionID,
			"request_id":    entry.RequestID,
			"error_message": entry.ErrorMessage,
			"completed_at":  entry.CompletedAt,
		})

	case "completed":
		if entry.Body == "" {
			writeErrorJSON(w, http.StatusNotFound, "",
				"pending response completed but body is empty", "session_error", "PENDING_EMPTY_BODY")
			return
		}
		w.Header().Set("X-Gw-Pending-Replay", "true")
		w.Header().Set("X-Gw-Pending-Session", entry.SessionID)
		w.Header().Set("X-Gw-Pending-Request", entry.RequestID)
		contentType := entry.ContentType
		if contentType == "" {
			contentType = "application/json"
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(entry.Body))

	default:
		writeErrorJSON(w, http.StatusServiceUnavailable, "",
			"pending entry has unknown status: "+entry.Status, "server_error", "PENDING_BAD_STATUS")
	}
}
