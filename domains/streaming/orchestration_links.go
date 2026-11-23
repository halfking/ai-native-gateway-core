package streaming

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/middleware"
)

const orchestrationLinkWriteBudget = 300 * time.Millisecond

type orchestrationLinkStore interface {
	Register(context.Context, string, int, string, string) (string, error)
	Claim(context.Context, string, string, int, string, string) (bool, error)
}

func registerOrchestrationChild(req *http.Request, body []byte) {
	ctx := req.Context()
	admission, _ := ctx.Value(gatewayAdmissionKey{}).(*gatewayAdmission)
	parent, _ := ctx.Value(modalityIngressKey{}).(*modalityIngress)
	if admission == nil || admission.links == nil || parent == nil || parent.requestID != admission.requestID ||
		parent.verified == nil || req.Header.Get("Authorization") == "" {
		return
	}
	writeCtx, cancel := context.WithTimeout(ctx, orchestrationLinkWriteBudget)
	defer cancel()
	digest := sha256.Sum256(body)
	token, err := admission.links.Register(writeCtx, parent.verified.TenantID, parent.verified.ID, parent.requestID, hex.EncodeToString(digest[:]))
	if err != nil {
		slog.Warn("orchestration dispatch evidence unavailable", "parent_request_id", parent.requestID, "error", err)
		return
	}
	req.Header.Set(middleware.OrchestrationLinkHeader, token)
}

func claimOrchestrationChild(r *http.Request, key *authentication.KeyInfo, bodySHA string) {
	if key == nil || r.URL.Path != "/v1/chat/completions" {
		return
	}
	ctx := r.Context()
	admission, _ := ctx.Value(gatewayAdmissionKey{}).(*gatewayAdmission)
	token := middleware.OrchestrationLink(ctx)
	if admission == nil || admission.links == nil || token == "" {
		return
	}
	writeCtx, cancel := context.WithTimeout(ctx, orchestrationLinkWriteBudget)
	defer cancel()
	if _, err := admission.links.Claim(writeCtx, token, key.TenantID, key.ID, admission.requestID, bodySHA); err != nil {
		slog.Warn("orchestration receipt link unavailable", "request_id", admission.requestID, "error", err)
	}
}
