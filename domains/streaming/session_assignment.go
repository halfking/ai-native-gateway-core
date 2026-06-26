package streaming

import (
	"context"
	"encoding/json"
	"net/http"
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
		recentSessionID, err := finder.FindRecentGatewaySession(ctx, keyInfo.TenantID, clientID.IdentityHash, keyInfo.ID, session.LastSystemSessionTTL)
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
