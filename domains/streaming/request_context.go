package streaming

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication" //nolint:depguard // trusted tenant identity
	"github.com/kaixuan/llm-gateway-go/domains/requestjourney" //nolint:depguard // request lifecycle observation
	"github.com/kaixuan/llm-gateway-go/domains/session"        //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

type explicitStreamSessionKey struct{}
type requestJourneyContextKey struct{}

type explicitStreamSessionState struct {
	explicit bool
}

type requestJourneyStatusWriter struct {
	http.ResponseWriter
	status int
}

type requestJourneyFlushStatusWriter struct {
	*requestJourneyStatusWriter
	flusher http.Flusher
}

func (w *requestJourneyStatusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *requestJourneyStatusWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *requestJourneyStatusWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(body)
}

func (w *requestJourneyFlushStatusWriter) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.flusher.Flush()
}

func markExplicitStreamSession(r *http.Request) *http.Request {
	if r == nil {
		return r
	}
	if _, marked := r.Context().Value(explicitStreamSessionKey{}).(explicitStreamSessionState); marked {
		return r
	}
	explicit := sanitizeGwSessionHeader(r.Header.Get("X-Gw-Session-Id")) != "" ||
		strings.TrimSpace(r.Header.Get("X-Session-Id")) != ""
	state := explicitStreamSessionState{explicit: explicit}
	return r.WithContext(context.WithValue(r.Context(), explicitStreamSessionKey{}, state))
}

func explicitStreamSession(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	state, _ := ctx.Value(explicitStreamSessionKey{}).(explicitStreamSessionState)
	return state.explicit
}

func beginRequestJourney(w http.ResponseWriter, r *http.Request, handler *ChatHandler) (http.ResponseWriter, *http.Request, *requestJourneyStatusWriter) {
	if r == nil {
		return w, r, nil
	}
	if lifecycle := requestJourneyFromRequest(r); lifecycle != nil {
		return w, r, nil
	}
	identity := initializeRequestIdentity(r)
	r, lifecycle := ensureRequestJourney(r, handler, identity.RequestID)
	if lifecycle == nil {
		return w, r, nil
	}
	writer := &requestJourneyStatusWriter{ResponseWriter: w}
	if flusher, ok := w.(http.Flusher); ok {
		return &requestJourneyFlushStatusWriter{requestJourneyStatusWriter: writer, flusher: flusher}, r, writer
	}
	return writer, r, writer
}

func finishRequestJourney(r *http.Request, writer *requestJourneyStatusWriter) {
	if writer == nil {
		return
	}
	lifecycle := requestJourneyFromRequest(r)
	if lifecycle == nil {
		return
	}
	if recovered := recover(); recovered != nil {
		lifecycle.MarkFailure("internal_panic")
		lifecycle.Finish(r.Context(), requestjourney.OutcomeFailure, "internal_panic", http.StatusInternalServerError)
		panic(recovered)
	}
	if err := r.Context().Err(); err != nil {
		lifecycle.Finish(r.Context(), requestjourney.OutcomeCanceled, "client_canceled", 0)
		return
	}
	status := http.StatusOK
	if writer != nil && writer.status != 0 {
		status = writer.status
	}
	errorKind := lifecycle.FailureKind()
	if status >= http.StatusBadRequest || errorKind != "" {
		if errorKind == "" {
			errorKind = strings.ToLower(strings.ReplaceAll(http.StatusText(status), " ", "_"))
		}
		lifecycle.Finish(r.Context(), requestjourney.OutcomeFailure, errorKind, status)
		return
	}
	lifecycle.Finish(r.Context(), requestjourney.OutcomeSuccess, "", status)
}

func ensureRequestJourney(r *http.Request, handler *ChatHandler, requestID string) (*http.Request, *requestjourney.Lifecycle) {
	if r == nil {
		return r, nil
	}
	if lifecycle, _ := r.Context().Value(requestJourneyContextKey{}).(*requestjourney.Lifecycle); lifecycle != nil {
		return r, lifecycle
	}
	if handler == nil || handler.journeyRecorder == nil || handler.journeyGatewayInstanceID == "" || requestID == "" {
		return r, nil
	}
	protocol, pathClass := requestJourneyIngressClass(r.URL.Path)
	lifecycle := requestjourney.NewIngressLifecycle(
		handler.journeyRecorder, handler.journeyGatewayInstanceID, requestID,
		protocol, pathClass, time.Now(),
	)
	r = r.WithContext(context.WithValue(r.Context(), requestJourneyContextKey{}, lifecycle))
	if handler.keyVerifier == nil || !handler.keyVerifier.Enabled() {
		lifecycle.BindTenant(r.Context(), "default", "")
	}
	return r, lifecycle
}

func requestJourneyIngressClass(path string) (requestjourney.IngressProtocol, requestjourney.IngressPathClass) {
	switch {
	case strings.HasPrefix(path, "/v1/messages"):
		return requestjourney.IngressProtocolMessages, requestjourney.IngressPathMessages
	case strings.HasPrefix(path, "/v1/responses"):
		return requestjourney.IngressProtocolResponses, requestjourney.IngressPathResponses
	case strings.HasPrefix(path, "/v1beta/models/") || strings.HasPrefix(path, "/v1/models/"):
		return requestjourney.IngressProtocolGemini, requestjourney.IngressPathGeminiModels
	default:
		return requestjourney.IngressProtocolChat, requestjourney.IngressPathChatCompletions
	}
}

func requestJourneyFromRequest(r *http.Request) *requestjourney.Lifecycle {
	if r == nil {
		return nil
	}
	lifecycle, _ := r.Context().Value(requestJourneyContextKey{}).(*requestjourney.Lifecycle)
	return lifecycle
}

func bindRequestJourney(r *http.Request, keyInfoTenant, model string) {
	if lifecycle := requestJourneyFromRequest(r); lifecycle != nil {
		lifecycle.BindTenant(r.Context(), keyInfoTenant, model)
	}
}

func resolveRequestJourney(r *http.Request, tenantID, requestedModel, resolvedModel string) {
	if lifecycle := requestJourneyFromRequest(r); lifecycle != nil {
		lifecycle.RouteResolved(r.Context(), tenantID, requestedModel, resolvedModel)
	}
}

func markRequestJourneyFailure(r *http.Request, keyInfo *authentication.KeyInfo, errorKind string) {
	lifecycle := requestJourneyFromRequest(r)
	if lifecycle == nil {
		return
	}
	if keyInfo != nil {
		lifecycle.BindTenant(r.Context(), keyInfo.TenantID, "")
	}
	lifecycle.MarkFailure(errorKind)
}

func requestJourneyExecState(r *http.Request) (string, *atomic.Int64, *atomic.Bool) {
	lifecycle := requestJourneyFromRequest(r)
	if lifecycle == nil {
		return "", nil, nil
	}
	return lifecycle.GatewayInstanceID(), lifecycle.Sequence(), lifecycle.TerminalState()
}

// gwSessionTaskFromRequest resolves gateway session and task identifiers for
// request_logs correlation. Priority:
//   - session: X-Gw-Session-Id > X-Session-Id > loaded session.SessionID
//   - task:    X-Gw-Task-Id > loaded session.TaskID
func gwSessionTaskFromRequest(r *http.Request, session *session.Session) (sessionID, taskID string) {
	if r != nil {
		sessionID = sanitizeGwSessionHeader(r.Header.Get("X-Gw-Session-Id"))
		if sessionID == "" {
			legacy := sanitizeRequestCorrelationID(r.Header.Get("X-Session-Id"))
			if strings.HasPrefix(legacy, "gw_") {
				sessionID = legacy
			}
		}
		taskID = sanitizeRequestCorrelationID(r.Header.Get("X-Gw-Task-Id"))
	}
	if session != nil {
		if sessionID == "" {
			sessionID = sanitizeRequestCorrelationID(session.SessionID)
		}
		if taskID == "" {
			taskID = sanitizeRequestCorrelationID(session.TaskID)
		}
	}
	return sessionID, taskID
}

const maxRequestCorrelationIDLen = 128

func sanitizeRequestCorrelationID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxRequestCorrelationIDLen {
		return ""
	}
	for _, ch := range value {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' || ch == ':' {
			continue
		}
		return ""
	}
	return value
}
