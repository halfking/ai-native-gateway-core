package streaming

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/internal/contextbudget"
	"github.com/kaixuan/llm-gateway-go/internal/largebody"
	"github.com/kaixuan/llm-gateway-go/internal/sessioncontext"
	"github.com/kaixuan/llm-gateway-go/internal/sessionid"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
)

const largeMemoryThreshold = 256 << 10

type LargeIngressConfig struct {
	MaxConcurrent int
	ContextBytes  int
	MinFreeBytes  uint64
}
type LargeIngress struct {
	handler *ChatHandler
	store   *largebody.Store
	config  LargeIngressConfig
	slots   chan struct{}
}
type largeArchiveKey struct{}
type largeArchive struct {
	Ref    largebody.Ref
	Report sessioncontext.Report
}

func LargeRequestArchive(ctx context.Context) (largebody.Ref, bool) {
	v, ok := ctx.Value(largeArchiveKey{}).(largeArchive)
	return v.Ref, ok
}
func NewLargeIngress(h *ChatHandler, store *largebody.Store, cfg LargeIngressConfig) *LargeIngress {
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 4
	}
	if cfg.ContextBytes <= 0 {
		cfg.ContextBytes = 1 << 20
	}
	if cfg.MinFreeBytes == 0 {
		cfg.MinFreeBytes = 512 << 20
	}
	return &LargeIngress{handler: h, store: store, config: cfg, slots: make(chan struct{}, cfg.MaxConcurrent)}
}
func generationPath(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	switch strings.TrimSuffix(r.URL.Path, "/") {
	case "/v1/chat/completions", "/chat/completions", "/v1/completions", "/completions", "/v1/messages", "/messages", "/v1/responses", "/responses":
		return true
	}
	_, action, ok := extractGeminiPath(r.URL.Path)
	return ok && (action == "generateContent" || action == "streamGenerateContent")
}

// Wrap must be outside V2 dispatch and streamretry: both snapshot request bodies.
func (g *LargeIngress) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !generationPath(r) {
			next.ServeHTTP(w, r)
			return
		}
		if _, ok := LargeRequestArchive(r.Context()); ok {
			next.ServeHTTP(w, r)
			return
		}
		r, authorized := g.handler.prepareSanitizeRequest(r)
		if !authorized {
			g.reject(w, r, 401, "authentication_required", "authentication failed")
			return
		}
		tenantID := "default"
		if cached, ok := r.Context().Value(sanitizeVerifiedKeyContextKey{}).(sanitizeVerifiedKey); ok && cached.info != nil {
			tenantID = tenant(cached.info)
		}
		if requested := strings.TrimSpace(r.Header.Get("X-Tenant-ID")); requested != "" && requested != tenantID {
			g.reject(w, r, 403, "tenant_mismatch", "tenant does not match authenticated key")
			return
		}
		if r.ContentLength > largebody.LimitBytes {
			g.reject(w, r, 413, "prompt_too_large", "request exceeds 100000000 bytes")
			return
		}
		if encoding := r.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
			g.reject(w, r, 415, "unsupported_content_encoding", "send uncompressed JSON")
			return
		}
		select {
		case g.slots <- struct{}{}:
		default:
			w.Header().Set("Retry-After", "1")
			g.reject(w, r, 429, "ingress_capacity_exceeded", "request processing capacity is full")
			return
		}
		released := false
		release := func() {
			if !released {
				<-g.slots
				released = true
			}
		}
		defer release()
		if g.store == nil {
			g.reject(w, r, 503, "session_archive_unavailable", "session archive is unavailable")
			return
		}
		free, err := archiveFreeBytes(g.store.RootPath())
		if err != nil || free < uint64(largebody.LimitBytes)+g.config.MinFreeBytes {
			g.reject(w, r, 503, "session_archive_capacity", "session archive has insufficient capacity")
			return
		}
		original := r.Body
		if original == nil {
			g.reject(w, r, 400, "invalid_request", "request body is required")
			return
		}
		defer original.Close()
		r = g.handler.prepareHistoryGuard(r)
		prepCtx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
		defer cancel()
		controller := http.NewResponseController(w)
		_ = controller.SetReadDeadline(time.Now().Add(120 * time.Second))
		stop := context.AfterFunc(prepCtx, func() {
			if controller.SetReadDeadline(time.Now()) != nil {
				_ = original.Close()
			}
		})
		defer stop()
		prefix, err := io.ReadAll(io.LimitReader(original, largeMemoryThreshold+1))
		if err != nil {
			g.reject(w, r, 400, "request_read_failed", "request body could not be read")
			return
		}
		// Client-selected identities are untrusted until the parsed body's
		// precedence and the existing session ownership gate have both run.
		// Rejected uploads remain recoverable in a server-generated namespace.
		sid := "gw_staging_" + uuid.NewString()
		ref, err := g.store.Capture(prepCtx, largebody.Identity{TenantID: tenantID, SessionID: sid}, io.MultiReader(bytes.NewReader(prefix), original))
		if err != nil {
			status, code := 503, "session_archive_unavailable"
			if errors.Is(err, largebody.ErrTooLarge) {
				status, code = 413, "prompt_too_large"
			}
			g.reject(w, r, status, code, "request could not be archived within the input limit")
			return
		}
		body := prefix
		report := sessioncontext.Report{SourceBytes: ref.Size, OutputBytes: len(body), SourceHash: ref.SHA256}
		// Rejections after RAW commit must retain an auditable recovery reference.
		r = r.WithContext(context.WithValue(r.Context(), largeArchiveKey{}, largeArchive{ref, report}))
		w.Header().Set("X-Gw-Archive-Id", ref.RequestID)
		if ref.Size > largeMemoryThreshold {
			body, report, err = sessioncontext.Reduce(prepCtx, g.store, ref, g.config.ContextBytes)
			if err != nil {
				status, code := 422, "context_budget_exceeded"
				message := "request cannot produce a valid bounded context; split the protected input"
				if errors.Is(err, context.DeadlineExceeded) {
					status, code = 503, "request_processing_timeout"
					message = "request preprocessing exceeded its time limit"
				} else if errors.Is(err, context.Canceled) {
					status, code = 408, "request_canceled"
					message = "request preprocessing was canceled"
				} else if errors.Is(err, sanitize.ErrRuleBlocked) {
					status, code = 403, "sensitive_policy_blocked"
					message = "request blocked by sensitive information policy"
				} else if errors.Is(err, largebody.ErrInvalidJSON) || errors.Is(err, largebody.ErrStructureLimit) {
					status, code = 400, "invalid_request"
				}
				g.reject(w, r, status, code, message)
				return
			}
		}
		resolved, updated, err := g.resolveArchiveSession(w, r.WithContext(prepCtx), body)
		if err != nil {
			if errors.Is(err, errClientSessionForbidden) {
				g.reject(w, r, 403, "session_forbidden", "session not owned by this api key")
			} else if errors.Is(err, largebody.ErrInvalidJSON) {
				g.reject(w, r, 400, "invalid_request", "request body must be a JSON object")
			} else {
				g.reject(w, r, 503, "session_unavailable", "session store unavailable")
			}
			return
		}
		body = updated
		if resolved != "" && resolved != ref.SessionID {
			ref, err = g.store.Rebind(prepCtx, ref, resolved)
			if err != nil {
				g.reject(w, r, 503, "session_archive_unavailable", "session archive could not bind the resolved session")
				return
			}
			sid = resolved
		}
		stop()
		cancel()
		_ = controller.SetReadDeadline(time.Time{})
		ctx := context.WithValue(r.Context(), largeArchiveKey{}, largeArchive{ref, report})
		ctx = contextbudget.WithSpec(ctx, contextbudget.Spec{}) // Buckets supply their own window later.
		r = r.Clone(ctx)
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		r.TransferEncoding = nil
		r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
		r.Header.Del("Content-Length")
		r.Header.Set("X-Gw-Session-Id", sid)
		w.Header().Set("X-Gw-Archive-Id", ref.RequestID)
		if report.Reduced {
			w.Header().Set("X-Gw-Context-Reduced", "extractive_history")
		}
		release() // Preprocessing quota must not be held for the duration of an SSE response.
		g.serveArchivedResponse(next, w, r, ref)
	})
}

func (g *LargeIngress) resolveArchiveSession(w http.ResponseWriter, r *http.Request, body []byte) (string, []byte, error) {
	verified, _ := r.Context().Value(sanitizeVerifiedKeyContextKey{}).(sanitizeVerifiedKey)
	keyInfo := verified.info
	resolved, _ := sessionid.Resolve(r.Header, body)
	hasBodyID := sessionid.FromBody(body) != ""
	if keyInfo == nil || g.handler.sessionGetter == nil {
		// A missing session authority is a supported local/degraded mode,
		// not permission to place caller data in another session's archive.
		resolved = ""
	} else if resolved == "" {
		// Preserve the existing recent-session/auto-create policy; a staging
		// id must never masquerade as an explicit client session header.
		assignment, err := g.handler.assignGatewaySession(r.Context(), body, r, keyInfo, "", nil, clientProfileFromKey(keyInfo))
		if err != nil {
			return "", nil, errClientSessionUnavailable
		}
		if assignment != nil {
			resolved = assignment.SessionID
			if assignment.Resumed {
				w.Header().Set("X-Gw-Session-Id-Resume", resolved)
				w.Header().Set("X-Gw-Session-Reused", "true")
			}
			if assignment.AutoCreated {
				w.Header().Set("X-Gw-Session-Id-Resume", resolved)
				w.Header().Set("X-Gw-Session-Auto", "true")
			}
		}
	}
	sid := resolved
	if resolved == "" {
		sid = generateSystemSessionID()
	} else {
		verifiedID, info, err := normalizeAndRegisterClientSession(r, resolved, g.handler.sessionGetter, keyInfo)
		if err != nil {
			return "", nil, err
		}
		sid = verifiedID
		if sid != resolved && info == nil {
			// Legacy stores without atomic claim receive a real fresh session,
			// so the inner handler cannot replace the helper's fallback again.
			deviceSeed := r.Header.Get("X-Device-Seed")
			if deviceSeed == "" {
				deviceSeed = r.Header.Get("X-Machine-Id")
			}
			if deviceSeed == "" {
				deviceSeed = "default"
			}
			created, err := g.handler.sessionGetter.CreateV2(r.Context(), keyInfo.ID, keyInfo.TenantID, deviceSeed, sanitizeRequestCorrelationID(r.Header.Get("X-Gw-Task-Id")))
			if err != nil || created == nil || created.SessionID == "" {
				return "", nil, errClientSessionUnavailable
			}
			sid = created.SessionID
		}
	}
	if hasBodyID {
		var envelope map[string]json.RawMessage
		if json.Unmarshal(body, &envelope) != nil || envelope == nil {
			return "", nil, largebody.ErrInvalidJSON
		}
		encoded, _ := json.Marshal(sid)
		// Canonical root aliases outrank nested metadata. Replace alternative
		// spellings of the highest-priority alias as well, avoiding map-order
		// ambiguity when the client supplied both gwSessionId and session_id.
		normalize := strings.NewReplacer("-", "", "_", "", ".", "", " ", "")
		for name := range envelope {
			if strings.ToLower(normalize.Replace(strings.TrimSpace(name))) == "gwsessionid" {
				envelope[name] = encoded
			}
		}
		envelope["gw_session_id"], envelope["session_id"] = encoded, encoded
		updated, err := json.Marshal(envelope)
		if err != nil {
			return "", nil, err
		}
		body = updated
	}
	return sid, body, nil
}

func (g *LargeIngress) reject(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	// The normal handler is bypassed on ingress rejection. Record metadata only;
	// a failed auth/upload must never trigger another read of the network body.
	rid := r.Header.Get("X-Request-Id")
	if rid == "" {
		rid = uuid.NewString()
	}
	safe := r.Clone(r.Context())
	safe.Body = http.NoBody
	safe.ContentLength = 0
	safe.GetBody = nil
	logCtx := g.handler.NewRequestLogContext(safe, rid, time.Now())
	if cached, ok := r.Context().Value(sanitizeVerifiedKeyContextKey{}).(sanitizeVerifiedKey); ok && cached.info != nil {
		logCtx.SetKey(cached.info)
	}
	logCtx.SetClientModel("<unknown>")
	logCtx.SetClientStatus(status)
	if client := g.handler.telemetryClient; client != nil && client.Enabled() {
		state := telemetry.RequestStatusFailure
		if status == http.StatusTooManyRequests {
			state = telemetry.RequestStatusRateLimited
		}
		client.EmitRequestLogInsert(logCtx.buildEntry(code, message, nil, nil, state))
	}
	if status == http.StatusTooManyRequests {
		logCtx.EmitRateLimited(code, message, nil, nil)
	} else {
		logCtx.failAndMark(code, message, nil, nil, status)
	}

	write := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := extractGeminiPath(r.URL.Path); ok {
			writeGeminiError(w, status, message)
			return
		}
		writeErrorJSONWithKindProto(protocolOfRequest(r), w, status, r.Header.Get("X-Request-Id"), message, "invalid_request_error", code, code, map[string]any{"stage": "ingress", "retryable": status == 429 || status == 503})
	})
	if ref, ok := LargeRequestArchive(r.Context()); ok {
		g.serveArchivedResponse(write, w, r, ref)
	} else {
		write.ServeHTTP(w, r)
	}

}

// Archive identity is merged into existing metadata. Neither raw bytes nor local
// absolute paths enter the database, Redis, logs or durable execution snapshots.
func applyLargeArchiveMetadata(entry *telemetry.RequestLogEntry, ctx context.Context) {
	raw := largeArchiveJSON(ctx)
	if len(raw) == 0 || entry == nil {
		return
	}
	meta := map[string]json.RawMessage{}
	if len(entry.CompressionMeta) > 0 {
		if json.Unmarshal(entry.CompressionMeta, &meta) != nil {
			return
		}
	}
	meta["large_request"] = raw
	if b, err := json.Marshal(meta); err == nil {
		entry.CompressionMeta = b
	}
}

// Keep the context metadata encoding a small, independently versioned record.
func largeArchiveJSON(ctx context.Context) json.RawMessage {
	if ctx == nil {
		return nil
	}
	v, ok := ctx.Value(largeArchiveKey{}).(largeArchive)
	if !ok {
		return nil
	}
	b, _ := json.Marshal(map[string]any{"archive": v.Ref, "reduction": v.Report, "client_response_request_id": v.Ref.RequestID + "-response", "version": 1})
	return b
}
