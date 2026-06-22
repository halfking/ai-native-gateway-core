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
	// pendingStore is the durable cache for client reconnect (Track
	// C). The interface is defined here (not imported from the
	// pending package) to avoid an import cycle: pending/ would
	// otherwise need sessions.Session for tenant-isolation checks.
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
// that the sessions handler needs. Defined here (not imported from
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
// adapter lives in main.go (which can import both sessions and
// pending) and converts pending.EntryView → PendingEntry.
type PendingStore interface {
	Get(ctx context.Context, sessionID, requestID string) (entry *PendingEntry, found bool, err error)
	GetLatest(ctx context.Context, sessionID string) (entry *PendingEntry, requestID string, found bool, err error)
}

func NewHandler(manager *Manager) *Handler {
	return &Handler{manager: manager}
}

func (h *Handler) SetAuth(kv *auth.KeyVerifier) {
	h.keyVerifier = kv
}

// SetPendingStore (Track C, 2026-06-18) installs the durable cache
// for client reconnect. nil disables the GET endpoint (returns 503).
// The concrete store is in the pending package; main.go constructs
// the store and wires it here.
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
		rest := strings.TrimPrefix(path, "/v1/sessions/")
		if rest == "" {
			writeErrorJSON(w, http.StatusBadRequest, "", "missing session id", "session_error", "MISSING_SESSION_ID")
			return
		}

		// Track C (2026-06-18): sub-route /v1/sessions/{id}/pending-
		// response. Must be checked BEFORE the plain {id} GET to
		// avoid the catch-all route treating the sub-path as a
		// session id.
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

// getPendingResponse (Track C, 2026-06-18) is the client reconnect
// endpoint. Returns a previously-buffered vendor response (or its
// in_progress / failed status) so a client that disconnected can
// pick up where it left off without re-running the whole request.
//
//   GET /v1/sessions/{sessionID}/pending-response
//       ?request_id=xxx    (optional; defaults to most recent)
//
// Response shape:
//
//   200 — entry is completed; body is the cached vendor response
//         (replayed as SSE if the original request was streaming,
//         otherwise as a plain JSON document matching ContentType).
//         Header X-Gw-Pending-Replay: true tells the client that
//         this body is a re-send (not a fresh request).
//
//   202 — entry is in_progress; client should retry with
//         Retry-After: 5. Body is a small JSON status object.
//
//   404 — no cached entry for the session (or the session never
//         sent a request that produced a pending response).
//
//   403 — session exists but is not owned by the calling API key
//         (caller should not see another tenant's cached bodies).
//
//   503 — pending store not configured (Redis not reachable at
//         startup; graceful degradation — the rest of the gateway
//         still works, this endpoint is the only thing that fails).
//
// Tenant isolation: even on 404 we do NOT reveal whether the
// session id is "valid but not yours" vs "doesn't exist". Both
// return 404 with the same body. The 403 path is reserved for the
// case where the session DOES exist locally and the api_key_id
// does not match — that means an authenticated tenant is asking
// for another tenant's data, which is a real authz failure.
//
// SSE replay: when the original request was a streaming call, the
// cached body is a series of "data: {...}\n\n" lines. We re-emit
// them as-is (the line boundaries are already correct from the
// original capture). Clients that key off "data: [DONE]\n\n" as
// the stream terminator work unchanged.
func (h *Handler) getPendingResponse(w http.ResponseWriter, r *http.Request, sessionID string) {
	if h.pendingStore == nil {
		writeErrorJSON(w, http.StatusServiceUnavailable, "",
			"pending response cache not configured", "server_error", "PENDING_STORE_UNAVAILABLE")
		return
	}

	// Tenant isolation: load the session row first and check
	// ownership. The pending entry itself does not carry api_key_id
	// (only sessionID + tenantID), so we use the session row as the
	// ownership anchor.
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
	// If session lookup itself failed (ErrSessionNotFound /
	// ErrSessionExpired), we fall through and let the pending lookup
	// decide. This is intentional: a pending entry may exist for a
	// session that was later deleted. We do not want to 404 in that
	// case just because the session row is gone — the cached body
	// is still useful to the client.
	//
	// Audit fix #14 (2026-06-18): when the session row is missing
	// (Redis outage or deleted session), we cannot verify ownership
	// via the session row. As a secondary guard, we check the
	// pending entry's TenantID against the request's tenant ID.
	// This prevents a tenant-A user from reading tenant-B's cached
	// response when the session manager is down.

	requestID := r.URL.Query().Get("request_id")

	var entry *PendingEntry
	var found bool
	var err error
	if requestID != "" {
		entry, found, err = h.pendingStore.Get(r.Context(), sessionID, requestID)
	} else {
		entry, requestID, found, err = h.pendingStore.GetLatest(r.Context(), sessionID)
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

	// Audit fix #14 (2026-06-18): secondary tenant isolation
	// check. When the session row was missing (Redis outage or
	// deleted session), the primary ownership check above was
	// skipped. We now verify the pending entry's TenantID
	// against the request's tenant ID. A mismatch means a
	// tenant-A user is trying to read tenant-B's cached body —
	// return 404 (not 403, to avoid leaking existence).
	//
	// Audit fix 5.1: removed the "default" tenant exception.
	// The "default" tenant is a real tenant in multi-tenant
	// deployments; allowing it to bypass the check was a
	// cross-tenant data leakage vector. If the pending entry
	// has a non-empty TenantID that differs from the request's
	// tenant, we reject regardless of which tenant is "default".
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
		// Audit fix 5.2: if Body is empty, return 404 rather
		// than an empty SSE stream that would hang the client.
		if entry.Body == "" {
			writeErrorJSON(w, http.StatusNotFound, "",
				"pending response completed but body is empty", "session_error", "PENDING_EMPTY_BODY")
			return
		}
		// Replay. The cached body is the full vendor response —
		// either a JSON object (non-streaming) or an SSE text
		// (streaming). ContentType drives the wire format.
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
		// Unknown status — treat as 5xx so the client retries later
		// rather than believing a malformed entry is the final answer.
		writeErrorJSON(w, http.StatusServiceUnavailable, "",
			"pending entry has unknown status: "+entry.Status, "server_error", "PENDING_BAD_STATUS")
	}
}