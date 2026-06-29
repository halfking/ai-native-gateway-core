package identity

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
)

// ClientIdentityHook extracts client identity from the request.
//
// This hook builds a stable client identity hash from the request context,
// mirroring the logic in domains/streaming/handler.go:1151.
type ClientIdentityHook struct{}

// NewClientIdentityHook creates a new client identity hook.
func NewClientIdentityHook() *ClientIdentityHook {
	return &ClientIdentityHook{}
}

// Name returns the hook name.
func (h *ClientIdentityHook) Name() string { return "identity.client_identity" }

// Priority returns the hook priority (should run after authentication).
func (h *ClientIdentityHook) Priority() int { return 20 }

// Enabled checks if the hook should run.
func (h *ClientIdentityHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	if env == nil || env.Envelope == nil || !env.Envelope.HasTransport() {
		return false
	}
	if env.Metadata["identity_hash"] != nil {
		return false
	}
	return env.Envelope.Transport.R != nil
}

// Execute extracts and builds client identity.
func (h *ClientIdentityHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	if env == nil || env.Envelope == nil || !env.Envelope.HasTransport() {
		return nil
	}
	httpReq := env.Envelope.Transport.R
	if httpReq == nil {
		return nil
	}

	var tenantID string
	var appID, apiKeyID *int
	
	if env.APIKey != nil {
		tenantID = env.APIKey.TenantID
		if env.APIKey.ID != "" {
			if id, err := strconv.Atoi(env.APIKey.ID); err == nil {
				apiKeyID = &id
			}
		}
	}

	clientProfile := extractClientProfile(httpReq)
	clientID := BuildIdentityFromRequest(httpReq, tenantID, appID, apiKeyID, clientProfile)

	if env.Metadata == nil {
		env.Metadata = make(map[string]any)
	}
	env.Metadata["identity_hash"] = clientID.IdentityHash
	env.Metadata["identity_short_id"] = clientID.ShortID()
	env.Metadata["client_identity"] = clientID
	env.Metadata["client_profile"] = clientID.Fingerprint.ClientProfile

	slog.Debug("identity hook: computed client identity",
		"identity_hash", clientID.IdentityHash[:16],
		"client_profile", clientProfile,
		"tenant_id", tenantID,
	)
	return nil
}

// OnError handles errors during identity extraction.
func (h *ClientIdentityHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	slog.Warn("identity hook: error during execution", "error", err)
	return nil
}

func extractClientProfile(r *http.Request) string {
	if profile := r.Header.Get("X-Client-Profile"); profile != "" {
		return profile
	}
	return "default"
}

var _ pipeline.Hook = (*ClientIdentityHook)(nil)
