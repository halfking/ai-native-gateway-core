package streaming

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/reconciliation"
	"github.com/kaixuan/llm-gateway-go/middleware"
)

type gatewayAdmissionObserver struct {
	wal   requestReceiptWAL
	links orchestrationLinkStore
}

type gatewayAdmissionKey struct{}

type gatewayAdmission struct {
	wal       requestReceiptWAL
	links     orchestrationLinkStore
	requestID string
	received  time.Time
	once      sync.Once
}

// NewGatewayAdmissionObserver records terminal gateway rejections under the request ID assigned at ingress.
func NewGatewayAdmissionObserver(wal *telemetry.RequestLogger, enabled bool, links *reconciliation.RequestLinks) middleware.Middleware {
	observer := gatewayAdmissionObserver{}
	if enabled && wal != nil {
		observer.wal = wal
		if links != nil {
			observer.links = links
		}
	}
	return observer
}

func (gatewayAdmissionObserver) Name() string { return "gateway_admission" }

func (m gatewayAdmissionObserver) Wrap(next http.Handler) http.Handler {
	if m.wal == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := middleware.ServerRequestID(r.Context())
		if id == "" || !gatewayAdmissionPath(r) {
			next.ServeHTTP(w, r)
			return
		}
		entry := &gatewayAdmission{wal: m.wal, links: m.links, requestID: id, received: middleware.ServerRequestReceivedAt(r.Context())}
		ctx := context.WithValue(r.Context(), gatewayAdmissionKey{}, entry)
		ctx = middleware.ObserveAuthRejection(ctx, func(reason string) {
			entry.record(ctx, http.StatusUnauthorized, reason, nil)
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func gatewayAdmissionPath(r *http.Request) bool {
	if generationPath(r) {
		return true
	}
	if r.Method != http.MethodPost {
		return false
	}
	switch r.URL.Path {
	case "/v1/embeddings", "/v1/audio/transcriptions", "/v1/audio/speech", "/v1/mcp",
		"/v1/images/generations", "/v1/videos/generations", "/v1/audio/refine", "/v1/audio/analyze",
		"/v1/summarize", "/v1/summarize/title", "/v1/classify":
		return true
	}
	return false
}

// RecordGatewayAdmissionRejection is called by gates that return before the protocol handler.
// reason must be a fixed code, free of request data.
func RecordGatewayAdmissionRejection(r *http.Request, httpStatus int, reason string, key *authentication.KeyInfo) {
	if entry, ok := r.Context().Value(gatewayAdmissionKey{}).(*gatewayAdmission); ok {
		entry.record(r.Context(), httpStatus, reason, key)
	}
}

func (entry *gatewayAdmission) record(ctx context.Context, httpStatus int, reason string, key *authentication.KeyInfo) {
	entry.once.Do(func() {
		tenantID, scope := "default", "unassigned"
		if httpStatus != http.StatusUnauthorized && key != nil && key.ID > 0 && key.TenantID != "" {
			tenantID, scope = key.TenantID, "tenant"
			if key.IsSystem {
				scope = "unassigned"
				if platformOriginForKey(ctx, key) != "" {
					scope = "platform"
				}
			}
		}
		status := telemetry.StatusFailure
		switch {
		case httpStatus == http.StatusTooManyRequests:
			status = telemetry.StatusRateLimited
		case reason == "request_canceled" || ctx.Err() == context.Canceled:
			status = telemetry.StatusCancelled
		}
		if err := entry.wal.CreateInitial(context.WithoutCancel(ctx), &telemetry.InitialRequest{
			RequestID: entry.requestID, TenantID: tenantID, Provisional: true, ReceivedAt: entry.received,
			Rejection: &telemetry.AdmissionRejection{
				Status: status, CompletedAt: time.Now().UTC(),
				Reason: "gateway_admission:" + scope + ":" + reason,
			},
		}); err != nil {
			slog.Warn("gateway admission WAL write failed", "request_id", entry.requestID, "error", err)
		}
	})
}
