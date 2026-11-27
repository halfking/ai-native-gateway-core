package streaming

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/middleware"
)

type requestReceiptWAL interface {
	CreateInitial(context.Context, *telemetry.InitialRequest) error
}

type modalityWAL interface {
	requestReceiptWAL
	Update(*telemetry.LogUpdate)
}

type modalityIngressKey struct{}

type modalityIngress struct {
	wal             modalityWAL
	attrs           func(*telemetry.ContextAttrsEntry)
	requestID       string
	clientRequestID string
	received        time.Time
	status          string
	reason          string
	verified        *authentication.KeyInfo
}

// ObserveModalityIngress wraps the routed request outside its concurrency gate.
// Independent modalities have no canonical session to attach to this row.
func ObserveModalityIngress(next http.Handler, wal modalityWAL, attrs func(*telemetry.ContextAttrsEntry)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := middleware.ServerRequestID(r.Context())
		if requestID == "" {
			requestID = generateRequestID()
		}
		r.Header.Set("X-Request-Id", requestID)
		w.Header().Set("X-Request-Id", requestID)
		entry := &modalityIngress{
			wal: wal, attrs: attrs, requestID: requestID,
			clientRequestID: sanitizeRequestCorrelationID(w.Header().Get("X-Client-Request-Id")),
			received:        middleware.ServerRequestReceivedAt(r.Context()),
		}
		r = r.WithContext(context.WithValue(r.Context(), modalityIngressKey{}, entry))
		entry.createInitial(r.Context(), "default")

		capture := &modalityResponseWriter{ResponseWriter: w}
		var response http.ResponseWriter = capture
		if flusher, ok := w.(http.Flusher); ok {
			response = &modalityFlushWriter{modalityResponseWriter: capture, flusher: flusher}
		}
		returned := false
		defer func() { entry.finish(r.Context(), capture, returned) }()
		next.ServeHTTP(response, r)
		returned = true
	})
}

func (entry *modalityIngress) createInitial(ctx context.Context, tenant string) {
	if err := entry.wal.CreateInitial(context.WithoutCancel(ctx), &telemetry.InitialRequest{
		RequestID: entry.requestID, TenantID: tenant, Provisional: true, ReceivedAt: entry.received,
	}); err != nil {
		slog.Warn("modality request WAL initial write failed", "request_id", entry.requestID, "error", err)
	}
}

func observeModalityIdentity(r *http.Request, key *authentication.KeyInfo) {
	entry, _ := r.Context().Value(modalityIngressKey{}).(*modalityIngress)
	if entry == nil || key == nil || key.ID <= 0 || key.TenantID == "" {
		return
	}
	entry.verified = key
	if key.TenantID != "default" {
		entry.createInitial(r.Context(), key.TenantID)
	}
	if entry.attrs != nil {
		source := "reconciliation_modality"
		attrs := &telemetry.ContextAttrsEntry{
			RequestID: entry.requestID, TenantID: key.TenantID,
			SourceChannel: &source,
		}
		if entry.clientRequestID != "" {
			attrs.ClientRequestID = &entry.clientRequestID
		}
		if stage, _, ok := trustedPlatformOrigin(r.Context(), key); ok && reconciliationPlatformClass(stage) != "" {
			attrs.OriginStage = &stage
			attrs.IsProbe = isProbeOriginStage(stage)
		}
		fillFromKeyInfo(attrs, key)
		entry.attrs(attrs)
	}
}

func markModalityOutcome(r *http.Request, status, reason string) {
	if entry, ok := r.Context().Value(modalityIngressKey{}).(*modalityIngress); ok {
		entry.status, entry.reason = status, reason
	}
}

func markModalityRateLimited(r *http.Request, reason string) {
	markModalityOutcome(r, telemetry.StatusRateLimited, reason)
}

func markModalityProtocolFailure(r *http.Request, reason string) {
	markModalityOutcome(r, telemetry.StatusFailure, reason)
}

func (entry *modalityIngress) finish(ctx context.Context, capture *modalityResponseWriter, returned bool) {
	status, reason := entry.status, entry.reason
	switch {
	case !returned:
		status, reason = telemetry.StatusFailure, "handler_interrupted"
	case capture.writeFailed || (ctx.Err() == context.Canceled && status != telemetry.StatusRateLimited):
		status, reason = telemetry.StatusCancelled, "client_disconnect: context canceled"
	case status != "":
	case capture.status >= 300:
		status, reason = telemetry.StatusFailure, "http_"+strconv.Itoa(capture.status)
	case capture.status >= 200:
		status = telemetry.StatusSuccess
	default:
		status, reason = telemetry.StatusUnknown, "response_unobserved"
	}
	stage := telemetry.StageResponseFail
	if status == telemetry.StatusSuccess {
		stage = telemetry.StageCompleted
	}
	entry.wal.Update(&telemetry.LogUpdate{
		RequestID: entry.requestID, Status: status, Stage: stage, Error: reason, CompletedAt: time.Now().UTC(),
		RequireInitial: true,
	})
}

type modalityResponseWriter struct {
	http.ResponseWriter
	status      int
	writeFailed bool
}

func (w *modalityResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *modalityResponseWriter) WriteHeader(status int) {
	if w.status == 0 && status >= 200 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *modalityResponseWriter) Write(p []byte) (int, error) {
	w.implicitSuccess()
	n, err := w.ResponseWriter.Write(p)
	w.writeFailed = w.writeFailed || err != nil || n < len(p)
	return n, err
}

func (w *modalityResponseWriter) implicitSuccess() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
}

type modalityFlushWriter struct {
	*modalityResponseWriter
	flusher http.Flusher
}

func (w *modalityFlushWriter) Flush() {
	w.implicitSuccess()
	w.flusher.Flush()
}
