package streaming

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/identity"
	"github.com/kaixuan/llm-gateway-go/domains/session"
)

type recentGatewaySessionFinder interface {
	FindRecentGatewaySession(ctx context.Context, tenantID, identityHash string, apiKeyID int, since time.Duration) (string, error)
}

type sessionAssignment struct {
	SessionID      string
	SessionInfo    *session.Session
	Resumed        bool
	AutoCreated    bool
	FromRecent     bool
	ShouldPersist  bool
	ClientMessages int
}

func countRequestMessages(body json.RawMessage) int {
	if len(body) == 0 {
		return 0
	}
	var probe struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return 0
	}
	return len(probe.Messages)
}

func shouldCreateFreshSessionForMessageCount(messageCount int) bool {
	return messageCount <= 1
}

func (h *ChatHandler) assignGatewaySession(
	ctx context.Context,
	rawBody []byte,
	r *http.Request,
	keyInfo *authentication.KeyInfo,
	existingSessionID string,
	existingSessionInfo *session.Session,
	clientProfile string,
) (*sessionAssignment, error) {
	var finder recentGatewaySessionFinder
	if h.telemetryClient != nil {
		finder = h.telemetryClient
	}
	return h.assignGatewaySessionWithFinder(ctx, rawBody, r, keyInfo, existingSessionID, existingSessionInfo, clientProfile, finder)
}

// ensureSessionID returns a non-empty gw_session_id for early-failure
// paths (body read error / body too large / json parse error) where
// bodyBytes is unavailable or invalid and the full assignGatewaySession
// cannot run. It is intentionally simpler than assignGatewaySession:
//   - never queries the DB for recent sessions
//   - always generates a fresh gw_<uuid> when the client did not provide one
//   - falls back to a system-generated gw_<uuid> when keyInfo is nil,
//     sessionGetter is nil, or CreateV2 fails
func (h *ChatHandler) ensureSessionID(
	ctx context.Context,
	r *http.Request,
	keyInfo *authentication.KeyInfo,
) string {
	if r != nil {
		if id := sanitizeGwSessionHeader(r.Header.Get("X-Gw-Session-Id")); id != "" {
			return id
		}
		if legacy := strings.TrimSpace(r.Header.Get("X-Session-Id")); strings.HasPrefix(legacy, "gw_") {
			return sanitizeGwSessionHeader(legacy)
		}
	}
	if keyInfo != nil && h.sessionGetter != nil {
		deviceSeed := r.Header.Get("X-Device-Seed")
		if deviceSeed == "" {
			deviceSeed = r.Header.Get("X-Machine-Id")
		}
		if deviceSeed == "" {
			deviceSeed = "default"
		}
		taskID := r.Header.Get("X-Gw-Task-Id")
		newSession, err := h.sessionGetter.CreateV2(ctx, keyInfo.ID, keyInfo.TenantID, deviceSeed, taskID)
		if err == nil && newSession != nil {
			return newSession.SessionID
		}
	}
	return generateSystemSessionID()
}

func (h *ChatHandler) assignGatewaySessionWithFinder(
	ctx context.Context,
	rawBody []byte,
	r *http.Request,
	keyInfo *authentication.KeyInfo,
	existingSessionID string,
	existingSessionInfo *session.Session,
	clientProfile string,
	finder recentGatewaySessionFinder,
) (*sessionAssignment, error) {
	assignment := &sessionAssignment{
		SessionID:      existingSessionID,
		SessionInfo:    existingSessionInfo,
		ClientMessages: countRequestMessages(rawBody),
	}
	if assignment.SessionID != "" || keyInfo == nil {
		return assignment, nil
	}

	if h.sessionGetter == nil {
		assignment.SessionID = generateSystemSessionID()
		return assignment, nil
	}

	deviceSeed := r.Header.Get("X-Device-Seed")
	if deviceSeed == "" {
		deviceSeed = r.Header.Get("X-Machine-Id")
	}
	if deviceSeed == "" {
		deviceSeed = "default"
	}
	taskID := r.Header.Get("X-Gw-Task-Id")

	createSession := func() (*sessionAssignment, error) {
		newSession, err := h.sessionGetter.CreateV2(ctx, keyInfo.ID, keyInfo.TenantID, deviceSeed, taskID)
		if err != nil {
			return nil, err
		}
		assignment.SessionID = newSession.SessionID
		assignment.SessionInfo = newSession
		assignment.AutoCreated = true
		assignment.ShouldPersist = true
		return assignment, nil
	}

	if shouldCreateFreshSessionForMessageCount(assignment.ClientMessages) {
		return createSession()
	}

	if finder != nil {
		clientID := identity.BuildIdentityFromRequest(r, keyInfo.TenantID, appID(keyInfo), &keyInfo.ID, clientProfile)
		recentSessionID, err := finder.FindRecentGatewaySession(ctx, keyInfo.TenantID, clientID.IdentityHash, keyInfo.ID, h.sessionReuseWindowOrDefault())
		if err != nil {
			return nil, err
		}
		if recentSessionID != "" {
			si, getErr := h.sessionGetter.Get(ctx, recentSessionID)
			if getErr == nil && si != nil {
				assignment.SessionID = recentSessionID
				assignment.SessionInfo = si
				assignment.Resumed = true
				assignment.FromRecent = true
				assignment.ShouldPersist = true
				return assignment, nil
			}
		}
	}

	return createSession()
}

// parseSessionReuseWindow reads the LLM_GATEWAY_SESSION_REUSE_WINDOW env
// var (e.g. "5m", "10m", "1h") and returns the parsed duration. Falls
// back to session.LastSystemSessionTTL (5 minutes) for empty, invalid,
// zero, or negative values. Production wiring calls this once at boot
// in cmd/gateway/main.go.
func parseSessionReuseWindow() time.Duration {
	raw := strings.TrimSpace(os.Getenv("LLM_GATEWAY_SESSION_REUSE_WINDOW"))
	if raw == "" {
		return session.LastSystemSessionTTL
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return session.LastSystemSessionTTL
	}
	return d
}

// ParseSessionReuseWindow is the exported alias used by cmd/gateway/main.go.
func ParseSessionReuseWindow() time.Duration { return parseSessionReuseWindow() }
