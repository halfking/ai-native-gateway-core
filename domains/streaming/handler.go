package streaming

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/kaixuan/llm-gateway-go/cache/prefix"
	"github.com/kaixuan/llm-gateway-go/domains/attachments"                         //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/authentication"                      //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/credential"                          //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"                         //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"                   //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"       //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"                      //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	sessionaudithook "github.com/kaixuan/llm-gateway-go/domains/hooks/sessionaudit" //nolint:depguard
	"github.com/kaixuan/llm-gateway-go/domains/identity"                            //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/session"                             //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"                 //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/transformation"                      //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/i18n"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/internal/modelpolicy"
	"github.com/kaixuan/llm-gateway-go/internal/observability"
	"github.com/kaixuan/llm-gateway-go/maas"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/ratelimit"
	"github.com/kaixuan/llm-gateway-go/registry"
	"github.com/kaixuan/llm-gateway-go/resolve"
	"github.com/kaixuan/llm-gateway-go/security/armor"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const maxBodySize = 128 << 20 // 128MB - increased for large context models like claude-opus-4-8 (1M context)

func MaxBodySize() int { return maxBodySize }

type preStreamKeepalive struct {
	w       http.ResponseWriter
	flusher http.Flusher
	stopCh  chan struct{}
	doneCh  chan struct{}
	mu      sync.Mutex
	once    sync.Once
}

func startPreStreamKeepalive(w http.ResponseWriter, interval time.Duration) (*preStreamKeepalive, bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	if interval <= 0 {
		interval = 15 * time.Second
	}
	psk := &preStreamKeepalive{
		w:       w,
		flusher: flusher,
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	psk.writeComment(sseKeepaliveComment)
	go psk.loop(interval)
	return psk, true
}

func (p *preStreamKeepalive) loop(interval time.Duration) {
	defer close(p.doneCh)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			p.writeComment(sseKeepaliveComment)
		}
	}
}

func (p *preStreamKeepalive) writeComment(line string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	safeWriteSSE(p.w, line)
	safeFlush(p.flusher)
}

func (p *preStreamKeepalive) stop() {
	if p == nil {
		return
	}
	p.once.Do(func() { close(p.stopCh) })
	<-p.doneCh
}

func writePrewarmedStreamError(w http.ResponseWriter, message, errType, code string) {
	if errType == "" {
		errType = "server_error"
	}
	if code == "" {
		code = "provider_error"
	}
	safeWriteSSE(w, fmt.Sprintf("data: {\"error\":{\"message\":%q,\"type\":%q,\"code\":%q}}\n\n", message, errType, code))
	if flusher, ok := w.(http.Flusher); ok {
		safeFlush(flusher)
	}
}

func sanitizeGwSessionHeader(v string) string {
	s := strings.TrimSpace(v)
	if s == "" {
		return ""
	}
	// V2 gateway sessions are always gw_<uuid>. Treat plain UUID-style
	// values as client metadata/session identifiers, not gateway session IDs.
	if !strings.HasPrefix(s, "gw_") {
		return ""
	}
	return s
}

// stripLegacyToolCallText removes the legacy "[Tool Call: <name>]\n"
// markers (and the bare arguments JSON that immediately follows them)
// from a stream_text_content blob when the same tool_calls are also
// available as structured data via audit.StreamCapture.ToolCalls.
//
// audit/stream.go ObserveChunk appends both a structured entry
// (sc.ToolCalls, via mergeToolCall) AND a free-text rendering into
// sc.textContent. The latter is preserved for any consumer that reads
// stream_text_content as a unified text preview. When emitTelemetry
// synthesizes a final response_body, however, the structured entries
// must be the SOLE source of truth for tool_calls — otherwise the same
// data appears twice in different shapes and OpenAI Chat Completions
// clients (which expect content to be plain assistant prose and
// tool_calls to be a separate array) reject the response.
//
// The marker format emitted by audit/stream.go is:
//
//	"\n[Tool Call: <name>]\n<arguments-json>"
//
// We strip every "[Tool Call: ...]" marker plus the JSON value that
// follows it on the same logical block. We are deliberately conservative:
// the marker text is a fixed-prefix sentinel that no upstream LLM emits
// in practice, so false-positive stripping is not a concern.
var legacyToolCallMarkerRE = regexp.MustCompile(`(?s)\[Tool Call:[^\]]*\]\n?`)

func stripLegacyToolCallText(s string) string {
	if s == "" {
		return s
	}
	return legacyToolCallMarkerRE.ReplaceAllString(s, "")
}

// ServiceID maps an API key to a (providerID, credentialID) pair.
type ServiceID struct {
	ProviderID   int
	CredentialID int
}

type chatRequestBody struct {
	Model    string          `json:"model"`
	Stream   bool            `json:"stream"`
	Messages json.RawMessage `json:"messages,omitempty"`
	User     string          `json:"user,omitempty"`
	// Tools is the optional function/tool definitions array.
	// Used by autoroute (v2.0) to detect multi-tool agent requests.
	Tools json.RawMessage `json:"tools,omitempty"`
	// ToolIDs (Phase 3, 2026-06-21) is the optional tool ID array.
	// Format: ["filesystem.*", "network.http_get"]
	// Expands to full tool definitions via toolRegistry.
	ToolIDs []string `json:"tool_ids,omitempty"`
}

//-----------------------------------------------------------------------------
// Chat handler — integrates circuit breaker + concurrency limiter
//-----------------------------------------------------------------------------

type providerResolver interface {
	Enabled() bool
	GetCandidates(ctx context.Context, model, profile, tenantID string) ([]provider.Candidate, *provider.Policy, error)
}

// ChatHandler handles chat completions with circuit breaker and concurrency control.
type ChatHandler struct {
	circuit         *credential.Manager
	limiter         *credential.Limiter
	matrix          *transformation.Matrix
	pools           *pool.PoolManager
	resolver        *resolve.Resolver
	auditor         audit.Sink
	client          *upstreampkg.Client
	normalizer      *Normalizer
	executor        *executors.Executor
	provider        providerResolver
	sticky          *executors.StickyCache
	keyVerifier     *authentication.KeyVerifier
	rateLimiter     ratelimit.RPMLimiter
	telemetryClient *telemetry.Client
	// decider (v2.0) is the optional autoroute.Decider. When non-nil,
	// requests with model="auto" trigger task classification + 6-dim
	// scoring. When nil, model="auto" falls back to default chat model.
	decider *autoroute.Decider
	// requestLogHook is an optional test sink.  When set, every
	// request_logs row the gateway emits is also passed to the hook
	// function so unit tests can assert on the safety-net coverage.
	// See SetRequestLogHook.
	requestLogHook func(*telemetry.RequestLogEntry)
	// sessionAuditHook (2026-06-28, session-audit feature) is the
	// pre-routing chat-time hook.  When non-nil, every chat request
	// goes through FastDetector before hitting GetCandidates(); Block /
	// NeedApproval decisions short-circuit the request with 403 / 202.
	// See SetSessionAuditHook.
	sessionAuditHook *sessionaudithook.SessionAuditHook
	maasSvc          *maas.Service
	sessionGetter    interface {
		Get(ctx context.Context, id string) (*session.Session, error)
		Touch(ctx context.Context, id string) error
		CreateV2(ctx context.Context, apiKeyID int, tenantID, deviceSeed, taskID string) (*session.Session, error)
		BindAPIKey(ctx context.Context, sessionID string, apiKeyID int, tenantID string) error
	}
	// idempotentCache (Track C C5, 2026-06-18) deduplicates
	// re-sent requests within a 5-minute window. When a client
	// retries (network glitch, double-click), the handler
	// returns 202 + X-Gw-Pending immediately rather than
	// re-executing the full routing + vendor path. nil disables
	// the dedup (every request is treated as new).
	idempotentCache *IdempotentCache

	// sessionCompressor (v3, 2026-06-19) is the session-level
	// intelligent compression. When non-nil, each request runs a
	// message-level LCS delta-append + optional proactive sliding-window
	// LLM summary before forwarding to the upstream. nil disables v3
	// (every request forwards the client body as-is, matching v7 behaviour).
	sessionCompressor *compression.SessionCompressor

	// promptCacheStabilize (rtk borrowing, 2026-07-06) reorders the request
	// messages by stability class (system → tools → history → tail) so the
	// upstream provider's KV-prefix-cache hits maximise. Idempotent + always
	// fail-open (Stabilize returns the original bytes on any unrecognised
	// shape). Toggle with LLM_GATEWAY_PROMPT_CACHE_STABILIZE=0. Default true.
	promptCacheStabilize bool

	// cacheInjector (rtk borrowing, 2026-07-06) injects cache_control markers
	// (Anthropic ephemeral / OpenAI checkpoint) onto the stabilized prefix
	// boundary when the resolved candidate supports prompt caching. nil or
	// promptCacheInject=false disables injection. Opt-in via
	// LLM_GATEWAY_PROMPT_CACHE_INJECT=1; default off (data-accuracy first).
	cacheInjector     *session.CacheInjector
	promptCacheInject bool

	// metaToolInterceptor (Phase 2, 2026-06-20) handles meta-tool calls

	// anomalyRecorder (2026-06-28) tracks response format anomalies to detect
	// provider API changes and improve token estimation logic. nil disables.
	anomalyRecorder *FormatAnomalyRecorder
	// (list_categories, load_tools) locally without forwarding to upstream.
	// nil disables Phase 2 meta-tools.
	metaToolInterceptor *MetaToolInterceptor

	// toolRegistry (Phase 3, 2026-06-21) provides centralized tool definitions.
	// When non-nil, requests with tool_ids expand to full tool definitions.
	// nil disables Phase 3 tool registry (tool_ids are ignored).
	toolRegistry ToolRegistryService

	// modelPolicy (Round 48, 2026-06-21) is the tenant-scoped model
	// denylist.  nil disables enforcement (every model allowed).
	// Wire from main.go after constructing the Checker.
	modelPolicy *modelpolicy.Checker

	// requestLogger (Request WAL) provides synchronous initial logging
	// at request arrival and asynchronous stage updates. nil disables.
	requestLogger *telemetry.RequestLogger

	// autoTitleGenerator (2026-06-22) automatically generates session titles
	// after the first successful request. nil disables auto-title generation.
	autoTitleGenerator interface {
		MaybeGenerateTitle(sessionID, tenantID string)
	}

	// armorJudge (Track A B1-5, 2026-06-25) scores prompts for security risks.
	// When non-nil, handler calls Judge.Score before executing LLM requests.
	// nil disables armor checks (every request proceeds without judgment).
	armorJudge armor.Judge

	// armorLogger (Track A B1-5, 2026-06-25) writes armor judgments to audit table.
	// When non-nil, handler calls Logger.Log after armor scoring.
	// nil disables armor audit (judgments are not persisted).
	armorLogger *armor.Logger
	// lastSystemSession enables 5-minute no-id session reuse.
	lastSystemSession *session.LastSystemSessionIndex
	// sessionPref tracks session -> credential preference for model switch handling.
	sessionPref *session.SessionPreference
	// rotationHook (2026-07-06) detects credential rotation and updates
	// session state (turns, tokens, cost, cred-history). nil disables.
	rotationHook *session.RotationHook
	// sessionReuseWindow (2026-06-26) is the look-back window for
	// FindRecentGatewaySession. Defaults to 5 * time.Minute; can be
	// overridden via LLM_GATEWAY_SESSION_REUSE_WINDOW env. 0 means
	// "always create new" (no recent-session reuse).
	sessionReuseWindow time.Duration

	// responseInterceptor (2026-06-29, auto-control feature) intercepts
	// LLM responses before forwarding to clients. Enables automatic handoff
	// when context limits are reached and goal-mode continuous execution.
	// nil disables response interception (default).
	responseInterceptor ResponseInterceptor

	// attachmentExtractor (2026-07-01) extracts base64/data-URI attachments
	// from incoming requests and saves them to the filesystem before forwarding.
	// nil disables attachment extraction (attachments remain inline in request body).
	attachmentExtractor *attachments.Extractor
}

// ToolRegistryService is the interface for tool registry access.
type ToolRegistryService interface {
	Get(ctx context.Context, tenantID, toolID string) (*registry.ToolDef, error)
	GetCategory(ctx context.Context, tenantID, category string) ([]*registry.ToolDef, error)
	ExpandToolIDs(ctx context.Context, tenantID string, toolIDs []string) []string
}

// ResponseInterceptor is the interface for response interception hooks.
// Alias to domains/hooks/response.ResponseInterceptor for type unification.
type ResponseInterceptor = response.ResponseInterceptor

// ResponseInterceptRequest is an alias to response.InterceptRequest.
type ResponseInterceptRequest = response.InterceptRequest

// ResponseInterceptResult is an alias to response.InterceptResult.
type ResponseInterceptResult = response.InterceptResult

// ResponseStreamMeta is an alias to response.StreamMeta.
type ResponseStreamMeta = response.StreamMeta

// ResponseChunkResult is an alias to response.ChunkResult.
type ResponseChunkResult = response.ChunkResult

// ResponseEndResult is an alias to response.EndResult.
type ResponseEndResult = response.EndResult

func NewChatHandler(cm *credential.Manager, l *credential.Limiter, matrix *transformation.Matrix, pools *pool.PoolManager, resolver *resolve.Resolver, auditor audit.Sink) *ChatHandler {
	if auditor == nil {
		auditor = &audit.LogSink{}
	}
	return &ChatHandler{circuit: cm, limiter: l, matrix: matrix, pools: pools, resolver: resolver, auditor: auditor, client: upstreampkg.New(), normalizer: NewNormalizer()}
}

func (h *ChatHandler) SetExecutor(exec *executors.Executor, prov providerResolver, sticky *executors.StickyCache) {
	h.executor = exec
	h.provider = prov
	h.sticky = sticky
}

func (h *ChatHandler) SetSessionRouting(lastSystemSession *session.LastSystemSessionIndex, sessionPref *session.SessionPreference) {
	h.lastSystemSession = lastSystemSession
	h.sessionPref = sessionPref
}

// SetRotationHook (2026-07-06) wires the session rotation hook.
func (h *ChatHandler) SetRotationHook(hook *session.RotationHook) {
	h.rotationHook = hook
}

// SetSessionReuseWindow configures the look-back window used by
// FindRecentGatewaySession. 0 disables recent-session reuse (every
// request creates a new gw_<uuid>). Negative values are clamped to 0.
func (h *ChatHandler) SetSessionReuseWindow(d time.Duration) {
	if d < 0 {
		d = 0
	}
	h.sessionReuseWindow = d
}

// sessionReuseWindowOrDefault returns the configured window, falling
// back to session.LastSystemSessionTTL (5m) when the handler has not
// been wired via SetSessionReuseWindow.
func (h *ChatHandler) sessionReuseWindowOrDefault() time.Duration {
	if h == nil {
		return session.LastSystemSessionTTL
	}
	if h.sessionReuseWindow <= 0 {
		return session.LastSystemSessionTTL
	}
	return h.sessionReuseWindow
}

// SetModelPolicy wires the tenant-scoped model denylist checker
// (Round 48).  nil disables enforcement.  Production wires a
// non-nil Checker from cmd/gateway/main.go.
func (h *ChatHandler) SetModelPolicy(mp *modelpolicy.Checker) {
	h.modelPolicy = mp
}

// SetIdempotentCache (Track C C5, 2026-06-18) wires the
// duplicate-request detector. nil disables dedup; every
// request is treated as new. Production wiring in
// cmd/gateway/main.go calls this with a non-nil cache so
// that double-clicks and network retries get an instant
// 202 + X-Gw-Pending response.
func (h *ChatHandler) SetIdempotentCache(c *IdempotentCache) {
	h.idempotentCache = c
}

// SetSessionCompressor wires the v3 session-level intelligent compression.
// When set, each request performs message-level delta-append + optional
// proactive sliding-window LLM summary before forwarding to the upstream.
func (h *ChatHandler) SetSessionCompressor(sc *compression.SessionCompressor) {
	h.sessionCompressor = sc
}

// SetPromptCacheStabilize toggles request-body prefix stabilization
// (cache/prefix.Stabilize). When true (default), each request's messages are
// reordered by stability class to maximise upstream KV-prefix-cache hits.
// Read once at startup from LLM_GATEWAY_PROMPT_CACHE_STABILIZE (default "1").
func (h *ChatHandler) SetPromptCacheStabilize(on bool) {
	h.promptCacheStabilize = on
}

// SetCacheInjector wires the prompt-cache-control injector. When set AND
// promptCacheInject is true, the request body gets cache_control markers
// placed on the stabilized prefix boundary for candidates that declare
// SupportsPromptCache. Opt-in only (LLM_GATEWAY_PROMPT_CACHE_INJECT=1).
func (h *ChatHandler) SetCacheInjector(ci *session.CacheInjector) {
	h.cacheInjector = ci
}

// SetPromptCacheInject enables/disables cache_control marker injection.
// Read once at startup from LLM_GATEWAY_PROMPT_CACHE_INJECT (default "0").
func (h *ChatHandler) SetPromptCacheInject(on bool) {
	h.promptCacheInject = on
}

// SetMetaToolInterceptor wires the Phase 2 meta-tool interceptor.
// When set, requests containing meta-tool calls (list_categories, load_tools)
// are handled locally without forwarding to upstream LLM providers.
func (h *ChatHandler) SetMetaToolInterceptor(i *MetaToolInterceptor) {
	h.metaToolInterceptor = i
}

// SetToolRegistry wires the Phase 3 tool registry.
// When set, requests containing tool_ids expand to full tool definitions.
func (h *ChatHandler) SetToolRegistry(tr ToolRegistryService) {
	h.toolRegistry = tr
}

// expandToolIDs (Phase 3, 2026-06-21) expands tool_ids to full tool definitions.
// Supports wildcards (filesystem.*, *) and exact matches (network.http_get).
// Returns expanded tools as JSON array, or nil if no tool_ids provided.
func (h *ChatHandler) expandToolIDs(ctx context.Context, tenantID string, toolIDs []string) ([]byte, error) {
	if len(toolIDs) == 0 || h.toolRegistry == nil {
		return nil, nil
	}

	// Use ExpandToolIDs for unified wildcard handling (supports *, category.*, exact)
	expandedIDs := h.toolRegistry.ExpandToolIDs(ctx, tenantID, toolIDs)
	if len(expandedIDs) == 0 {
		return nil, nil
	}

	// Fetch full tool definitions for expanded IDs
	var tools []json.RawMessage
	for _, toolID := range expandedIDs {
		tool, err := h.toolRegistry.Get(ctx, tenantID, toolID)
		if err != nil {
			slog.Warn("failed to get expanded tool",
				"tenant", tenantID,
				"tool_id", toolID,
				"error", err)
			continue
		}

		if tool != nil {
			tools = append(tools, json.RawMessage(tool.Definition))
		}
	}

	if len(tools) == 0 {
		return nil, nil
	}

	return json.Marshal(tools)
}

func (h *ChatHandler) SetAuth(kv *authentication.KeyVerifier, rl ratelimit.RPMLimiter) {
	h.keyVerifier = kv
	h.rateLimiter = rl
}

func (h *ChatHandler) SetTelemetry(tc *telemetry.Client) {
	h.telemetryClient = tc
}

func (h *ChatHandler) SetMaas(svc *maas.Service) {
	h.maasSvc = svc
}

// SetRequestLogger wires the Request WAL logger.
// nil disables Request WAL (default).
func (h *ChatHandler) SetRequestLogger(rl *telemetry.RequestLogger) {
	h.requestLogger = rl
}

// SetAutoTitleGenerator (2026-06-22) wires the auto title generator from admin package.
func (h *ChatHandler) SetAutoTitleGenerator(atg interface {
	MaybeGenerateTitle(sessionID, tenantID string)
}) {
	h.autoTitleGenerator = atg
}

// SetArmor wires armor judge and logger for prompt security checks.
// When both are non-nil, handler scores prompts before LLM execution
// and writes audit records to armor_judgments table. v1 observe-only mode.
func (h *ChatHandler) SetArmor(judge armor.Judge, logger *armor.Logger) {
	h.armorJudge = judge
	h.armorLogger = logger
}

// SetRequestLogHook installs an in-memory sink that records every
// request_logs row the safety-net (or the success path) emits.  It is
// used by unit tests in this package to assert that every error exit
// path still produces a row.  Passing nil clears the hook (the
// default is nil; production callers should never set a hook).
//
// The hook is best-effort: if it is set and the entry is nil, the
// hook does nothing.  Concurrent appends are guarded by a mutex so
// tests that fire many requests in parallel can inspect the
// collected slice without racing.
func (h *ChatHandler) SetRequestLogHook(hook func(*telemetry.RequestLogEntry)) {
	h.requestLogHook = hook
}

// SetSessionAuditHook installs the chat-time session-audit hook.
//
// When non-nil, the hook is consulted BEFORE the request reaches
// GetCandidates (routing). Block → 403; NeedApproval → 202 + approval_id
// + pending_approval response; Pass/Warn → continue.
//
// nil disables the hook (no chat-time audit). Production callers (cmd/gateway/main.go)
// should set this after constructing both SessionAuditHook and ApprovalManager.
//
// 2026-06-28: this is the v1 ChatHandler integration point that handoff
// round-1 had claimed to wire but actually missed (修复 G only landed in
// cmd/gateway-v2/main.go, the demo binary).
func (h *ChatHandler) SetSessionAuditHook(hook *sessionaudithook.SessionAuditHook) {
	h.sessionAuditHook = hook
}

// SetResponseInterceptor wires the response interceptor for automatic
// handoff and goal mode. When set, the handler calls the interceptor
// after receiving LLM responses but before forwarding to clients.
// nil disables response interception (default).
//
// 2026-06-29: auto-control feature integration point.
func (h *ChatHandler) SetResponseInterceptor(interceptor ResponseInterceptor) {
	h.responseInterceptor = interceptor
}

func (h *ChatHandler) SetSessionGetter(sg interface {
	Get(ctx context.Context, id string) (*session.Session, error)
	Touch(ctx context.Context, id string) error
	CreateV2(ctx context.Context, apiKeyID int, tenantID, deviceSeed, taskID string) (*session.Session, error)
	BindAPIKey(ctx context.Context, sessionID string, apiKeyID int, tenantID string) error
}) {
	h.sessionGetter = sg
}

// SetFormatAnomalyRecorder configures the anomaly recorder for tracking
// response format issues (used to detect provider API changes).
func (h *ChatHandler) SetFormatAnomalyRecorder(recorder *FormatAnomalyRecorder) {
	h.anomalyRecorder = recorder
}

// SetAttachmentExtractor (2026-07-01) wires the attachment extractor.
// When set, the handler extracts base64/data-URI attachments from incoming
// requests and saves them to the filesystem before forwarding to upstream.
// The extracted metadata is written to request_logs.attachments JSONB column.
// nil disables attachment extraction (attachments remain inline).
func (h *ChatHandler) SetAttachmentExtractor(extractor *attachments.Extractor) {
	h.attachmentExtractor = extractor
}

// ServeHTTP handles /v1/chat/completions and /v1/completions.
func (h *ChatHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// ── requestAttempt safety-net: every request that reaches this
	//    handler must produce exactly one request_logs row, regardless
	//    of which early-return path it takes.  attemptErrCode is
	//    populated by the inner functions when they exit without
	//    writing a row themselves; the deferred block at the end
	//    of this function writes the row using those fields.  The
	//    *attemptLogged bool is shared with the inner functions via
	//    pointer so success / explicit-failure paths can mark the
	//    row as already-written to avoid double-logging.
	var logCtx *RequestLogContext
	// 2026-06-26: server-side request id is ALWAYS freshly generated.
	// The middleware (RequestIDMiddleware) sets X-Request-Id to a new
	// UUID and X-Gw-Client-Request-Id to the client-supplied value.
	// We still defensively fall back to generateRequestID() here in
	// case the middleware chain was bypassed (e.g. direct unit-test
	// invocation), and we capture the client value for
	// request_logs.client_request_id so retries can be correlated.
	requestID := r.Header.Get("X-Request-Id")
	if requestID == "" {
		requestID = generateRequestID()
		w.Header().Set("X-Request-Id", requestID)
	}
	clientRequestID := r.Header.Get("X-Gw-Client-Request-Id")
	if clientRequestID == "" {
		// Backstop: read the original (pre-middleware) client value.
		clientRequestID = r.Header.Get("X-Client-Request-Id")
	}
	if clientRequestID != "" && clientRequestID != requestID {
		w.Header().Set("X-Client-Request-Id", clientRequestID)
	}
	startTime := time.Now()
	logCtx = h.NewRequestLogContext(r, requestID, startTime)
	logCtx.ClientRequestID = clientRequestID
	// 2026-06-30: 记录客户端请求端点 (migration 320)
	logCtx.SetClientEndpoint(r.URL.Path)
	if wt := strings.TrimSpace(r.Header.Get(autoWorkTypeHeader)); wt != "" {
		logCtx.SetWorkType(wt)
	}
	// ── Ensure every request has a gw_session_id (2026-06-26) ────────────
	// Even pre-keyInfo failures (missing_key, invalid_key, auth_unavailable)
	// emit a request_log row via the safety net. Without a session_id here
	// those rows would have empty gw_session_id, breaking /api/logs filtering
	// and session-summary grouping. Call ensureSessionID before any failure
	// path can fire; it is idempotent on existing X-Gw-Session-Id headers.
	if gwID := h.ensureSessionID(r.Context(), r, nil); gwID != "" {
		r.Header.Set("X-Gw-Session-Id", gwID)
	}
	defer func() {
		slog.Info("safety_net_defer_fired",
			"request_id", requestID,
			"attempt_err_code", logCtx.ErrCode,
			"attempt_logged", logCtx.IsLogged())
		if rec := recover(); rec != nil {
			slog.Error("chat handler panic", "panic", rec, "request_id", requestID)
			logCtx.SetError("internal_panic", "internal server error")
			if len(logCtx.Body) == 0 {
				logCtx.EnsureCaptured()
			}
			if logCtx.ClientModel == "" {
				if len(logCtx.Body) > 0 {
					logCtx.SetClientModel(extractModelFromBody(logCtx.Body))
				}
				if logCtx.ClientModel == "" {
					logCtx.SetClientModel("<unknown>")
				}
			}
			logCtx.EmitFailure(logCtx.ErrCode, logCtx.ErrMsg, logCtx.ProviderID, logCtx.CredentialID)
			writeErrorJSON(w, http.StatusInternalServerError, requestID,
				"internal server error", "server_error", "internal_panic")
		} else if logCtx.ErrCode != "" && !logCtx.IsLogged() {
			slog.Info("safety_net: recording failed request",
				"request_id", requestID,
				"error_kind", logCtx.ErrCode,
				"client_model", logCtx.ClientModel)
			logCtx.EmitFailure(logCtx.ErrCode, logCtx.ErrMsg, logCtx.ProviderID, logCtx.CredentialID)
		}
		// ── 2026-06-22: Request WAL client-disconnect safety net ─────────
		// If the client disconnected before we logged completion, the WAL
		// record would otherwise stay at stage=0/pending forever. Mark it
		// as stage=13 (response_fail) so audit completeness stays at 100%.
		// Only runs if the request context is canceled (client disconnect)
		// AND we never successfully completed via the success path.
		if h.requestLogger != nil && r.Context().Err() != nil {
			// 2026-06-30: 标记客户端超时/断开连接 (migration 320)
			if errors.Is(r.Context().Err(), context.DeadlineExceeded) ||
				errors.Is(r.Context().Err(), context.Canceled) {
				logCtx.SetClientTimeout(true)
			}
			// ── 2026-07-09: 问题4 —— 客户端取消/超时探测记录 ──────────────
			// 在前端取消(context.Canceled)或超时(context.DeadlineExceeded)
			// 时，额外生成一条 probe 记录写入 request_logs_hot 并推入泳道，
			// 让运维在"实时请求流"里直接看到取消/超时事件及其凭据归属。
			// 用专用 probe- 前缀 + 时间戳的 request_id，避免与初始 in_progress
			// 行的 ON CONFLICT 冲突。携带 credential_id 以便泳道按供应商反查。
			h.emitClientDisconnectProbe(requestID, r, logCtx)
			// Use Background context since request context is already canceled
			update := &telemetry.LogUpdate{
				RequestID: requestID,
				Stage:     telemetry.StageResponseFail,
				Status:    telemetry.StatusFailure,
				Error:     "client_disconnect: " + r.Context().Err().Error(),
			}
			// Use 2-second timeout to avoid blocking the request lifecycle
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			if err := h.requestLogger.UpdateSync(ctx, update); err != nil {
				slog.Warn("request_logger: client-disconnect UpdateSync failed", "request_id", requestID, "error", err)
			}
			cancel()
		}
	}()

	// GET probe — return 200 for client compatibility checks
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"message": "Chat completions endpoint is available. Use POST to send requests.",
		})
		return
	}
	if r.Method != http.MethodPost {
		logCtx.SetError("method_not_allowed", "method not allowed")
		// 2026-06-20 audit fix: capture the body + model so the
		// request_logs row records what the client actually sent
		// (not just "method not allowed"). Without this every
		// 405 row shows empty body + model="<unknown>" and the
		// operator can't tell which client / tool sent the
		// wrong method or which model it was trying to reach.
		// EmitFailure here (not relying on the safety net)
		// because the safety-net path runs only when the inner
		// pipeline never returned; an early 405 should produce
		// a single, fully-populated row.
		logCtx.EnsureCaptured()
		if logCtx.ClientModel == "" {
			if len(logCtx.Body) > 0 {
				logCtx.SetClientModel(extractModelFromBody(logCtx.Body))
			}
			if logCtx.ClientModel == "" {
				logCtx.SetClientModel("<unknown>")
			}
		}
		logCtx.EmitFailure(logCtx.ErrCode, logCtx.ErrMsg, nil, nil)
		logCtx.MarkLogged()
		http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request","code":"method_not_allowed"}}`, http.StatusMethodNotAllowed)
		return
	}

	if h.executor != nil && h.provider != nil && h.provider.Enabled() {
		h.serveWithExecutor(w, r, logCtx)
		return
	}
	logCtx.SetError("executor_unavailable", "routing executor not available; database connection required")
	logCtx.EnsureCaptured()
	// 2026-06-20 audit fix v3: ensure client_model is never
	// blank when body was captured but had no "model" field.
	if logCtx.ClientModel == "" {
		if len(logCtx.Body) > 0 {
			logCtx.SetClientModel(extractModelFromBody(logCtx.Body))
		}
		if logCtx.ClientModel == "" {
			logCtx.SetClientModel("<unknown>")
		}
	}
	logCtx.EmitFailure(logCtx.ErrCode, logCtx.ErrMsg, nil, nil)
	logCtx.MarkLogged()
	h.serveFallback(w, r)
}

// serveWithExecutor is the main chat-completions / completions pipeline.
// It receives pointers to the safety-net attempt state from ServeHTTP
// so that any exit path can populate them and the deferred logger in
// the caller will record exactly one request_logs row.  attemptLogged
// is set to true by any inner function that has already recorded the
// row (e.g. via recordFailedRequest or emitTelemetry on the success
// path) so the deferred safety net does not duplicate it.
func (h *ChatHandler) serveWithExecutor(
	w http.ResponseWriter,
	r *http.Request,
	logCtx *RequestLogContext,
) {
	//nolint:errcheck // best-effort close
	defer r.Body.Close()

	requestID := logCtx.RequestID
	startTime := logCtx.StartTime
	logCtx.EnsureCaptured()

	markLogged := func() { logCtx.MarkLogged() }

	// 2026-06-20 audit fix helper: capture body + model + emit failure
	// for early-exit error paths. Without this every 405/401/400/503
	// row shows empty body + model="<unknown>" and the operator cannot
	// tell which client sent the bad request or which model it was
	// trying to reach (the symptom that triggered the comprehensive audit).
	captureAndEmitFailure := func(errCode, errMsg string, providerID, credentialID *int) {
		logCtx.SetError(errCode, errMsg)
		logCtx.EnsureCaptured()
		if logCtx.ClientModel == "" {
			if len(logCtx.Body) > 0 {
				logCtx.SetClientModel(extractModelFromBody(logCtx.Body))
			}
			if logCtx.ClientModel == "" {
				logCtx.SetClientModel("<unknown>")
			}
		}
		logCtx.EmitFailure(errCode, errMsg, providerID, credentialID)
		logCtx.MarkLogged()
	}

	// ── API key authentication ──────────────────────────────────────────
	var keyInfo *authentication.KeyInfo
	if h.keyVerifier != nil && h.keyVerifier.Enabled() {
		rawKey := extractBearerToken(r)
		if rawKey == "" {
			captureAndEmitFailure("missing_key", "missing api key", nil, nil)
			writeErrorJSONCtx(r.Context(), w, http.StatusUnauthorized, requestID, "authentication_error", i18n.MsgMissingKey, nil)
			return
		}
		ki, verifyErr := h.keyVerifier.Verify(r.Context(), rawKey)
		if verifyErr != nil {
			if _, ok := verifyErr.(*authentication.InvalidKeyError); ok {
				captureAndEmitFailure("invalid_key", "invalid or expired api key", nil, nil)
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeErrorJSONCtx(r.Context(), w, http.StatusUnauthorized, requestID, "authentication_error", i18n.MsgInvalidKey, nil)
				return
			}
			slog.Error("key verification RPC failed, rejecting request", "error", verifyErr)
			captureAndEmitFailure("auth_unavailable", "authentication service temporarily unavailable", nil, nil)
			writeErrorJSON(w, http.StatusServiceUnavailable, requestID,
				"Authentication service temporarily unavailable", "server_error", "auth_unavailable")
			return
		}
		keyInfo = ki
		logCtx.SetKey(ki)

		// Round 38 (2026-06-16) — emit multi-tenant OTel span
		// attributes per docs/multi-tenant-otel-design.md §3.1.
		// llm-gateway-go is Pattern A (direct tenant_id from
		// authentication.KeyInfo). Every authenticated request now carries
		// tenant.id so production debugging can filter Jaeger.
		span := trace.SpanFromContext(r.Context())
		observability.SetTenantAttrs(span, keyInfo.TenantID, "api_key",
			fmt.Sprintf("key_%d", keyInfo.ID))
	}

	// ── Status checks (throttled key → hard rate-limit) ────────────────
	if keyInfo != nil && keyInfo.Status == "throttled" {
		captureAndEmitFailure("key_throttled", "api key throttled due to anomalous usage", nil, nil)
		writeErrorJSON(w, http.StatusTooManyRequests, requestID,
			"Your API key has been throttled due to anomalous usage. Contact admin.",
			"rate_limit_error", "key_throttled")
		return
	}

	// ── RPM rate limit (unified via checkGatewayRateLimit) ──────────────
	if rlOutcome := checkGatewayRateLimit(keyInfo, h.rateLimiter); !rlOutcome.Skipped {
		writeRateLimitHeaders(w, rlOutcome)
		if rlOutcome.Blocked {
			captureAndEmitFailure("rate_limit_exceeded", "rate limit exceeded", nil, nil)
			writeErrorJSONCtx(r.Context(), w, http.StatusTooManyRequests, requestID, "rate_limit_error", i18n.MsgRateLimitExceeded, nil)
			return
		}
	}

	// ── Budget pre-check ─────────────────────────────────────────────────
	if keyInfo != nil && h.keyVerifier != nil {
		if budgetErr := h.keyVerifier.CheckBudget(r.Context(), keyInfo.ID); budgetErr != nil {
			if _, ok := budgetErr.(*authentication.BudgetExceededError); ok {
				captureAndEmitFailure("budget_exhausted", "budget exhausted", nil, nil)
				writeErrorJSONCtx(r.Context(), w, http.StatusPaymentRequired, requestID, "insufficient_quota", i18n.MsgBudgetExhausted, nil)
				return
			}
		}
	}

	// ── MaaS credits pre-check (non-default tenants) ─────────────────────
	if keyInfo != nil && h.maasSvc != nil && keyInfo.TenantID != "" && keyInfo.TenantID != "default" {
		if err := h.maasSvc.PreCheckCredits(r.Context(), keyInfo.TenantID); err != nil {
			if _, ok := err.(*maas.InsufficientCreditsError); ok {
				captureAndEmitFailure("insufficient_credits", "insufficient credits", nil, nil)
				writeErrorJSONCtx(r.Context(), w, http.StatusPaymentRequired, requestID, "insufficient_quota", i18n.MsgInsufficientCredits, nil)
				return
			}
		}
	}

	// ── Inject API Key info into context for session middleware ─────────
	ctx := r.Context()
	if keyInfo != nil {
		ctx = session.SetAPIKeyID(ctx, keyInfo.ID)
		ctx = session.SetTenantID(ctx, keyInfo.TenantID)
	}

	// ── Session validation (if X-Gw-Session-Id or X-Session-Id provided) ──
	var sessionInfo *session.Session
	sessionID := extractSessionIDFromHeaders(r)
	if sessionID != "" && h.sessionGetter != nil {
		si, err := h.sessionGetter.Get(ctx, sessionID)
		if err != nil {
			if err == session.ErrSessionNotFound && keyInfo != nil {
				deviceSeed := r.Header.Get("X-Device-Seed")
				if deviceSeed == "" {
					deviceSeed = r.Header.Get("X-Machine-Id")
				}
				if deviceSeed == "" {
					deviceSeed = "default"
				}
				taskID := r.Header.Get("X-Gw-Task-Id")
				newSession, createErr := h.sessionGetter.CreateV2(ctx, keyInfo.ID, keyInfo.TenantID, deviceSeed, taskID)
				if createErr != nil {
					slog.Error("session fallback create failed", "error", createErr, "session_id", sessionID)
				} else {
					sessionInfo = newSession
					sessionID = newSession.SessionID
					logCtx.SetSession(newSession)
					ctx = session.SessionFromContextWith(ctx, newSession)
					w.Header().Set("X-Gw-Session-Id-Resume", newSession.SessionID)
					w.Header().Set("X-Gw-Session-Auto", "true")
					if r.Header.Get("X-Session-Id") != "" {
						slog.Warn("legacy X-Session-Id used, fallback created; migrate to X-Gw-Session-Id",
							"original_session_id", r.Header.Get("X-Session-Id"),
							"new_session_id", newSession.SessionID,
						)
						w.Header().Set("Deprecation", "true")
					}
					slog.Info("session fallback created",
						"original_session_id", r.Header.Get("X-Gw-Session-Id"),
						"new_session_id", newSession.SessionID,
						"task_id", taskID,
					)
					if h.lastSystemSession != nil {
						lsEntry := &session.LastSystemSessionEntry{
							SessionID:  newSession.SessionID,
							DeviceSeed: deviceSeed,
							TaskID:     taskID,
						}
						if setErr := h.lastSystemSession.Set(ctx, keyInfo.ID, lsEntry); setErr != nil {
							slog.Warn("LastSystemSessionIndex update failed", "error", setErr, "api_key_id", keyInfo.ID)
						}
					}
				}
			} else if err != session.ErrSessionNotFound {
				slog.Warn("session lookup failed", "error", err)
			}
		} else {
			sessionInfo = si
			logCtx.SetSession(si)
			if keyInfo != nil && si.APIKeyID != keyInfo.ID {
				if si.APIKeyID == 0 {
					if bindErr := h.sessionGetter.BindAPIKey(ctx, sessionID, keyInfo.ID, keyInfo.TenantID); bindErr != nil {
						slog.Warn("orphan session bind failed", "error", bindErr, "session_id", sessionID)
						captureAndEmitFailure("session_forbidden", "session not owned by this api key", nil, nil)
						writeErrorJSONCtx(r.Context(), w, http.StatusForbidden, requestID, "session_error", i18n.MsgSessionForbidden, nil)
						return
					}
					si.APIKeyID = keyInfo.ID
					si.TenantID = keyInfo.TenantID
					sessionInfo = si
				} else {
					captureAndEmitFailure("session_forbidden", "session not owned by this api key", nil, nil)
					writeErrorJSONCtx(r.Context(), w, http.StatusForbidden, requestID, "session_error", i18n.MsgSessionForbidden, nil)
					return
				}
			}
			go func() {
				touchCtx, touchCancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer touchCancel()
				//nolint:errcheck // best-effort touch, non-critical
				h.sessionGetter.Touch(touchCtx, sessionID)
			}()
			ctx = session.SessionFromContextWith(ctx, sessionInfo)
		}
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, int64(maxBodySize)+1))
	if err != nil {
		logCtx.CapturePartialBody(bodyBytes)
		logCtx.SetError("body_read_error", fmt.Sprintf("failed to read request body: %v", err))
		slog.Warn("request body read failed",
			"request_id", requestID,
			"error", err,
			"content_length", r.ContentLength,
			"partial_bytes", len(bodyBytes),
			"client_model", logCtx.ClientModel,
			"latency_ms", time.Since(startTime).Milliseconds(),
		)
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": "failed to read request body", "type": "invalid_request", "code": "body_read_error"},
		})
		return
	}
	if len(bodyBytes) > 0 {
		logCtx.Body = bodyBytes

		// ── Attachment extraction (2026-07-01) ──────────────────────────
		// 收到请求后立即提取并保存附件到文件系统。这样即使后续转发/记录失败，
		// 附件依然可追溯。提取失败不阻塞请求转发（best-effort）。
		if h.attachmentExtractor != nil {
			extractResult := h.attachmentExtractor.ExtractFromOpenAIBody(requestID, bodyBytes)
			if extractResult != nil && extractResult.Saved > 0 {
				// 将提取的元数据暂存到 logCtx，后续写入 request_logs.attachments JSONB
				logCtx.Attachments = extractResult.Attachments
				slog.Debug("attachments: extracted from request",
					"request_id", requestID,
					"found", extractResult.TotalFound,
					"saved", extractResult.Saved,
					"failed", extractResult.Failed)
			}
		}
	}
	if len(bodyBytes) > maxBodySize {
		// body_too_large already has body captured (it's in bodyBytes)
		// but we need to emit + mark to prevent safety net double-emit
		logCtx.SetError("body_too_large", "request body exceeds 32 MiB limit")
		if logCtx.ClientModel == "" {
			logCtx.SetClientModel(extractModelFromBody(bodyBytes[:maxBodySize]))
		}
		logCtx.EmitFailure("body_too_large", "request body exceeds 32 MiB limit", nil, nil)
		logCtx.MarkLogged()
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
			"error": map[string]string{"message": "request body exceeds 32 MiB limit", "type": "invalid_request", "code": "body_too-large"},
		})
		return
	}

	var reqBody chatRequestBody
	if err := json.Unmarshal(bodyBytes, &reqBody); err != nil {
		// json_parse_error already has body captured (it's in bodyBytes)
		logCtx.SetError("json_parse_error", "invalid JSON in request body")
		if logCtx.ClientModel == "" {
			logCtx.SetClientModel(extractModelFromBody(bodyBytes))
		}
		logCtx.EmitFailure("json_parse_error", "invalid JSON in request body", nil, nil)
		logCtx.MarkLogged()
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": "invalid JSON in request body", "type": "invalid_request", "code": "json_parse_error"},
		})
		return
	}

	clientModel := reqBody.Model
	logCtx.SetClientModel(clientModel)
	if sessionID == "" {
		sessionID = extractSessionIDFromBody(bodyBytes)
	}
	if sessionID == "" {
		assignment, assignErr := h.assignGatewaySession(ctx, bodyBytes, r, keyInfo, sessionID, sessionInfo, clientProfileFromKey(keyInfo))
		if assignErr != nil {
			slog.Error("session assignment failed", "error", assignErr)
			captureAndEmitFailure("session_error", "failed to assign session id", nil, nil)
			writeErrorJSONCtx(r.Context(), w, http.StatusInternalServerError, requestID, "session_error", i18n.MsgSessionAssignFailed, nil)
			return
		}
		if assignment != nil && assignment.SessionID != "" {
			sessionID = assignment.SessionID
			sessionInfo = assignment.SessionInfo
			r.Header.Set("X-Gw-Session-Id", sessionID)
			if sessionInfo != nil {
				logCtx.SetSession(sessionInfo)
				ctx = session.SessionFromContextWith(ctx, sessionInfo)
				r = r.WithContext(ctx)
			}
			if assignment.Resumed {
				w.Header().Set("X-Gw-Session-Id-Resume", sessionID)
				w.Header().Set("X-Gw-Session-Reused", "true")
			}
			if assignment.AutoCreated {
				w.Header().Set("X-Gw-Session-Id-Resume", sessionID)
				w.Header().Set("X-Gw-Session-Auto", "true")
			}
			if assignment.ShouldPersist && h.lastSystemSession != nil && keyInfo != nil {
				lsEntry := &session.LastSystemSessionEntry{
					SessionID:  sessionID,
					DeviceSeed: r.Header.Get("X-Device-Seed"),
					TaskID:     r.Header.Get("X-Gw-Task-Id"),
				}
				if lsEntry.DeviceSeed == "" {
					lsEntry.DeviceSeed = r.Header.Get("X-Machine-Id")
				}
				if setErr := h.lastSystemSession.Set(ctx, keyInfo.ID, lsEntry); setErr != nil {
					slog.Warn("LastSystemSessionIndex update failed", "error", setErr, "api_key_id", keyInfo.ID)
				}
			}
		}
	}
	if sessionID != "" && h.sessionPref != nil {
		modelChanged, prevModel := detectAndHandleModelSwitch(ctx, h.sessionPref, sessionID, clientModel)
		if modelChanged {
			slog.Info("session model switch detected, preference cleared",
				"session_id", sessionID,
				"previous_model", prevModel,
				"new_model", clientModel,
			)
		}
	}

	// ── Session-audit hook (2026-06-28, 集成 v1 ChatHandler) ───────────
	// 在 tenant policy 之前、auto_route 之前调 hook。
	// Block → 403; NeedApproval → 202 + approval_id; Pass/Warn → 继续。
	// hook 为 nil 时不调用（chat-time audit 关闭）。
	// hook 内部失败 / 降级 → 返回 StatusCode=0, 不阻断主流程。
	if h.sessionAuditHook != nil && len(bodyBytes) > 0 {
		hookContent := extractFirstUserMessage(bodyBytes)
		hookTenant := ""
		if keyInfo != nil {
			hookTenant = keyInfo.TenantID
		}
		res := h.sessionAuditHook.CheckV1(ctx, sessionID, hookTenant, clientModel, hookContent, r.Header.Get("User-Agent"), r.RemoteAddr)
		switch res.StatusCode {
		case 403:
			captureAndEmitFailure("session_audit_block", res.Reason, nil, nil)
			span := trace.SpanFromContext(r.Context())
			span.SetAttributes(
				attribute.String(observability.AttrTenantID, hookTenant),
				attribute.String("session_audit.decision", "block"),
			)
			writeErrorJSON(w, http.StatusForbidden, requestID, "Request blocked by security policy: "+res.Reason, "security_violation", "blocked")
			return
		case 202:
			captureAndEmitFailure("session_audit_pending_approval", res.Reason, nil, nil)
			span := trace.SpanFromContext(r.Context())
			span.SetAttributes(
				attribute.String(observability.AttrTenantID, hookTenant),
				attribute.String("session_audit.decision", "need_approval"),
				attribute.String("session_audit.approval_id", res.ApprovalID),
			)
			w.Header().Set("X-Approval-ID", res.ApprovalID)
			w.Header().Set("X-Approval-Status-URL", "/v1/approvals/"+res.ApprovalID+"/status")
			writeJSON(w, http.StatusAccepted, map[string]any{
				"status":         "pending_approval",
				"approval_id":    res.ApprovalID,
				"message":        "Request requires manual review due to security policy",
				"reason":         res.Reason,
				"poll_url":       "/v1/approvals/" + res.ApprovalID + "/status",
				"estimated_wait": "5-15 minutes",
			})
			return
		}
		// StatusCode=0 (Pass/Warn) → 继续
	}

	// ── Tenant model policy — pre-auto check (Round 48, 2026-06-21) ──
	// Must run BEFORE auto_route + GetCandidates so a denied request
	// never reaches the upstream provider.  model="auto" is exempt
	// here (user decision); the post-rewrite check below re-evaluates
	// after auto_route resolves the model, preventing auto as a
	// bypass vector.
	//
	// fail-open: a governance DB outage must not become an
	// availability outage.  See internal/modelpolicy/checker.go.
	if keyInfo != nil {
		profile := clientProfileFromKey(keyInfo)
		denied, canonical, _ := enforceTenantModelPolicy(
			r.Context(), clientModel, keyInfo, h.modelPolicy, h.resolver, profile,
		)
		if denied {
			msg := fmt.Sprintf("Model '%s' is not available for your account", canonical)
			captureAndEmitFailure("model_forbidden", msg, nil, nil)
			span := trace.SpanFromContext(r.Context())
			span.SetAttributes(
				attribute.String(observability.AttrTenantID, keyInfo.TenantID),
				attribute.String("tenant.deny_model", canonical),
			)
			writeErrorJSON(w, http.StatusForbidden, requestID, msg,
				"permission_error", "model_forbidden")
			return
		}
	}

	// ── v2.0 auto-route ────────────────────────────────────────────────
	// If the client requested model="auto", classify the task and pick
	// the best credential. Rewrites body model + sets X-Gw-Auto-Decision.
	preAutoModel := clientModel
	if clientModel == autoRequestMagic {
		apiKeyID := 0
		if keyInfo != nil {
			apiKeyID = keyInfo.ID
		}
		newBody, wire, shouldFail := h.maybeResolveAuto(&reqBody, bodyBytes, r, apiKeyID)
		if shouldFail {
			// 2026-07-01 P1: auto-route decider failed (DB / Redis / feature-flag
			// outage). Surface a 502 with a transparent error_kind instead of
			// silently rewriting the request to a fallback model. The original
			// behaviour hid routing-data outages as "user picked the fallback
			// model" rows in request_logs, which is exactly what the
			// routing-error-transparency work is removing — see
			// docs/2026-07-01-unknown-error-root-cause.md.
			captureAndEmitFailure("auto_route_decider_failed",
				fmt.Sprintf("auto-route decider failed for model '%s'", clientModel),
				nil, nil)
			markLogged()
			writeErrorJSON(w, http.StatusBadGateway, requestID,
				"auto-route temporarily unavailable; pass an explicit model name and retry",
				"server_error", "auto_route_decider_failed")
			return
		}
		if newBody != nil {
			bodyBytes = newBody
		}
		if wire != nil {
			writeAutoDecisionHeader(w, wire)
			logCtx.SetAutoDecision(wire)
		} else {
			logCtx.IsAutoRequest = true
		}
		clientModel = reqBody.Model
		logCtx.SetClientModel(clientModel)
	}

	// ── Tenant model policy — post-auto check (Round 48) ─────────────
	// If the original request was model="auto" and auto_route rewrote
	// it to a specific model, re-check the policy with the rewritten
	// model.  Without this, a tenant could bypass the denylist by
	// always sending model="auto" (the pre-auto check exempts auto).
	if keyInfo != nil && preAutoModel == autoRequestMagic && clientModel != autoRequestMagic {
		profile := clientProfileFromKey(keyInfo)
		denied, canonical, _ := enforceTenantModelPolicyAfterAuto(
			r.Context(), preAutoModel, clientModel, keyInfo, h.modelPolicy, h.resolver, profile,
		)
		if denied {
			msg := fmt.Sprintf("Model '%s' is not available for your account", canonical)
			captureAndEmitFailure("model_forbidden_after_auto", msg, nil, nil)
			span := trace.SpanFromContext(r.Context())
			span.SetAttributes(
				attribute.String(observability.AttrTenantID, keyInfo.TenantID),
				attribute.String("tenant.deny_model", canonical),
			)
			writeErrorJSON(w, http.StatusForbidden, requestID, msg,
				"permission_error", "model_forbidden")
			return
		}
	}

	// ── Phase 3: tool_ids expansion ────────────────────────────────────
	// If the client provided tool_ids, expand them to full tool definitions.
	// tool_ids takes precedence over tools (if both provided, tools is ignored).
	if len(reqBody.ToolIDs) > 0 {
		tenantID := "default"
		if keyInfo != nil {
			tenantID = keyInfo.TenantID
		}

		expandedTools, err := h.expandToolIDs(r.Context(), tenantID, reqBody.ToolIDs)
		if err != nil {
			slog.Error("failed to expand tool_ids",
				"tenant", tenantID,
				"tool_ids", reqBody.ToolIDs,
				"error", err)
			// Degradation: continue with original tools (if any)
		} else if expandedTools != nil {
			if len(reqBody.Tools) > 0 {
				slog.Warn("both tools and tool_ids provided, tool_ids takes precedence",
					"tenant", tenantID,
					"tools_count", len(reqBody.Tools),
					"tool_ids", reqBody.ToolIDs)
			}
			// Replace tools with expanded definitions
			reqBody.Tools = expandedTools
			// Re-marshal bodyBytes with expanded tools
			newBody, err := json.Marshal(reqBody)
			if err == nil {
				bodyBytes = newBody
			} else {
				slog.Error("failed to re-marshal body after tool expansion", "error", err)
			}
		}
	}

	// NOTE: v3 session-level compression runs AFTER candidate resolution
	// (below, once we know the target model's context window). See the
	// "v3 Session-level intelligent compression" block after GetCandidates.

	isStream := reqBody.Stream
	endUser := resolveEndUser(reqBody.User, r)
	// 2026-06-29: Prefer the Pipeline-computed identity (v2 dispatch
	// path) to avoid recomputing. Fall back to inline computation for
	// the legacy v1 path or when the Pipeline did not run.
	var clientID identity.ClientIdentity
	if precomputed, ok := identity.ComputedIdentityFromContext(ctx); ok {
		clientID = *precomputed
	} else {
		clientID = identity.BuildIdentityFromRequest(r, tenant(keyInfo), appID(keyInfo), apiKeyIDPtr(keyInfo), clientProfileFromKey(keyInfo))
	}
	identityHash := clientID.ShortID()
	// startTime is the outer-watcher time; executor tracks per-candidate
	// latency internally.  We re-use the safety-net's startTime (the
	// one from the function parameter) for the audit latency.
	_ = startTime

	auditBuilder := newAuditEvent(requestID).
		ClientModel(clientModel).
		IdentityHash(identityHash).
		ClientProfile(clientID.Fingerprint.ClientProfile).
		Stream(isStream).
		RequestChecksum(bodyBytes)

	var streamCapture *audit.StreamCapture
	if isStream {
		streamCapture = audit.NewStreamCapture()
	}

	var preStream *preStreamKeepalive
	preStreamPrepared := false
	defer func() {
		if streamCapture != nil {
			auditBuilder.StreamMetrics(streamCapture)
		}
		h.auditor.Emit(context.Background(), auditBuilder.Build())
	}()

	// ── Armor security check (Track A B1-5, 2026-06-25) ──────────────────
	// Score prompt for prompt-injection before provider resolution.
	// v1 observe-only: even if score > threshold, never block (only log).
	if h.armorJudge != nil && h.armorLogger != nil && keyInfo != nil {
		// Extract last user message from body
		var promptText string
		var messages []map[string]interface{}
		if err := json.Unmarshal(bodyBytes, &struct {
			Messages *[]map[string]interface{} `json:"messages"`
		}{Messages: &messages}); err == nil && len(messages) > 0 {
			lastMsg := messages[len(messages)-1]
			if role, ok := lastMsg["role"].(string); ok && role == "user" {
				if content, ok := lastMsg["content"].(string); ok {
					promptText = content
				}
			}
		}

		if promptText != "" {
			scoreReq := armor.ScoreRequest{
				Prompt:    promptText,
				Rubric:    "Does this prompt attempt to override instructions or inject malicious commands?",
				Threshold: 0.7, // TODO: load from policy via armor.LoadPolicy
			}

			judgeStart := time.Now()
			scoreResp, judgeErr := h.armorJudge.Score(ctx, scoreReq)
			judgeLatency := time.Since(judgeStart)

			if judgeErr != nil {
				slog.Warn("armor: judge call failed",
					"request_id", requestID,
					"error", judgeErr,
					"latency_ms", judgeLatency.Milliseconds())
			}

			// Construct judgment for audit
			judgment := armor.Judgment{
				RequestID:  requestID,
				TenantID:   keyInfo.TenantID,
				CheckType:  armor.CheckPromptInject,
				Decision:   armor.ResolveDecision(scoreResp.Score, scoreReq.Threshold, armor.ModeObserve),
				Source:     "judge",
				Score:      scoreResp.Score,
				Threshold:  scoreReq.Threshold,
				Mode:       armor.ModeObserve,
				JudgeModel: scoreResp.JudgeModel,
				LatencyMS:  int(judgeLatency.Milliseconds()),
				Reason:     scoreResp.Reason,
				CreatedAt:  time.Now(),
			}

			// Async write to armor_judgments (never blocks relay)
			go h.armorLogger.Log(context.Background(), judgment)

			// v1 observe-only: log warning but never block
			if judgment.Decision == armor.DecisionWarn || judgment.Decision == armor.DecisionBlock {
				slog.Warn("armor: prompt injection detected (observe-only, not blocking)",
					"request_id", requestID,
					"score", scoreResp.Score,
					"threshold", scoreReq.Threshold,
					"decision", judgment.Decision.String())
			}
		}
	}

	// 2026-07-03: Bug #7 fix - pass tenantID from keyInfo
	tenantID := ""
	if keyInfo != nil {
		tenantID = keyInfo.TenantID
	}
	candidates, policy, err := h.provider.GetCandidates(r.Context(), clientModel, clientID.Fingerprint.ClientProfile, tenantID)
	if err != nil {
		// Database or infrastructure error - do NOT disguise as no_candidate
		slog.Error("failed to get candidates from provider", "error", err, "model", clientModel, "request_id", requestID)
		rc := classifyRoutingError(err)
		h.emitFailedDecisionLog(requestID, clientModel, keyInfo, clientID, 0, nil, nil, rc.code, nil, int(time.Since(startTime).Milliseconds()))
		logCtx.failAndMark(rc.code, rc.message, nil, nil)
		markLogged()
		writeErrorJSON(w, rc.httpStatus, requestID, rc.message, "server_error", rc.code)
		return
	}
	if len(candidates) == 0 {
		h.emitFailedDecisionLog(requestID, clientModel, keyInfo, clientID, 0, nil, nil, "no_candidate", nil, int(time.Since(startTime).Milliseconds()))
		logCtx.failAndMark("no_candidate",
			fmt.Sprintf("No available provider for model '%s'", clientModel), nil, nil)
		markLogged()
		writeErrorJSONCtx(r.Context(), w, http.StatusServiceUnavailable, requestID, "server_error", i18n.MsgNoCandidate, map[string]any{"Model": clientModel})
		return
	}
	if len(candidates) > 0 {
		// Stash the first candidate so the safety net can attribute
		// the failure to a specific provider / credential when the
		// executor itself fails.
		pid := candidates[0].ProviderID
		cid := candidates[0].CredentialID
		logCtx.SetRoute(&pid, &cid)
	}

	var modelResolution *resolve.Resolution
	if h.resolver != nil {
		modelResolution = h.resolver.Resolve(r.Context(), clientModel, clientID.Fingerprint.ClientProfile)
	}

	var txResult *transformation.TransformResult
	tCtx := &transformation.TransformContext{
		RequestMode:   "chat",
		ClientProfile: clientID.Fingerprint.ClientProfile,
		ClientModel:   clientModel,
	}
	if modelResolution != nil && modelResolution.CanonicalName != nil {
		tCtx.CanonicalName = *modelResolution.CanonicalName
	}
	if h.matrix != nil {
		txResult = h.matrix.Resolve(tCtx)
	}
	explicitOutbound := ""
	if len(candidates) > 0 {
		explicitOutbound = renderOutboundFromTransform(txResult, candidates[0], tCtx.CanonicalName)
	}

	auditBuilder.OutboundModel(explicitOutbound).Provider(candidates[0].ProviderID).Credential(candidates[0].CredentialID)
	if modelResolution != nil {
		auditBuilder.ResolutionPath(modelResolution.ResolutionPath)
		if modelResolution.CanonicalName != nil {
			auditBuilder.CanonicalName(*modelResolution.CanonicalName)
		}
	}
	if txResult != nil {
		auditBuilder.TransformRule(txResult.MatchedRule)
	}

	egressProtocol := ""
	if len(candidates) > 0 {
		egressProtocol = candidates[0].Protocol
	}
	var canonicalID *int
	if modelResolution != nil {
		canonicalID = modelResolution.CanonicalID
	}
	gwSessionID, gwTaskID := gwSessionTaskFromRequest(r, sessionInfo)
	outboundForLog := explicitOutbound
	if len(candidates) > 0 {
		outboundForLog = outboundModelForLog(clientModel, explicitOutbound, candidates[0].RawModel)
	}

	// ── Phase 2 Meta-tool expansion ────────────────────────────────────
	// When the request's `tools` array contains the meta-tools
	// (list_categories, load_tools), expand them in-place with the full
	// tool set loaded from tool_registry. The expanded request is then
	// forwarded upstream so the LLM sees all concrete tools in a
	// single round-trip (no list_categories → load_tools dance needed).
	// Runs BEFORE session compression so we don't compress the small
	// meta-tool body — only the expanded one.
	if h.metaToolInterceptor != nil {
		modified, intercepted, err := h.metaToolInterceptor.InterceptRequest(r.Context(), bodyBytes)
		if err != nil {
			captureAndEmitFailure("meta_tool_error", fmt.Sprintf("meta-tool expansion failed: %v", err), nil, nil)
			writeErrorJSONCtx(r.Context(), w, http.StatusInternalServerError, requestID, "internal_error", i18n.MsgMetaToolError, nil)
			return
		}
		if intercepted {
			// Meta-tools replaced with full tool set; continue with the
			// expanded body.
			bodyBytes = modified
		}
	}

	// ── v3 Session-level intelligent compression ────────────────────────
	// Runs AFTER candidate resolution so we know the target model's context
	// window (B1 fix: previously passed 0, which disabled the TOKEN trigger
	// and the mechanical-trim fallback). The session compressor delta-appends
	// new turns to the compressed session history and, when the sliding
	// window fires, produces a lossless LLM summary (or trims as fallback).
	if h.sessionCompressor != nil && gwSessionID != "" {
		tenantForSC := "default"
		if keyInfo != nil {
			tenantForSC = keyInfo.TenantID
		}
		protocolForSC := "openai"
		if isAnthropicMessagesPath(r.URL.Path) {
			protocolForSC = "anthropic-messages"
		}
		// Resolve the target model context window from the first candidate.
		// 0 when unknown (TOKEN trigger then relies on msg_count / idle only).
		ctxWindow := 0
		if len(candidates) > 0 && candidates[0].ContextWindow != nil {
			ctxWindow = *candidates[0].ContextWindow
		}
		scResult := h.sessionCompressor.Prepare(
			r.Context(),
			bodyBytes,
			tenantForSC,
			gwSessionID,
			protocolForSC,
			ctxWindow,
			false, // not streaming yet at this point
		)
		if scResult != nil && len(scResult.OutboundBody) > 0 {
			// NeverWorse guard: the compressor must never inflate the request
			// body. If the "compressed" output is >= the raw body length the
			// transform regressed — discard it and keep the original.
			if guarded, regressed := compression.NeverWorse(bodyBytes, scResult.OutboundBody, compression.GuardStageCompress); !regressed {
				bodyBytes = guarded
			}

			// ── Tools restoration (Phase 1 optimization) ──────────────────
			// If compressor cached tools (marked with "_tools_cached": true),
			// restore them from the original request body before forwarding
			// to upstream LLM provider.
			var outbound map[string]json.RawMessage
			if err := json.Unmarshal(bodyBytes, &outbound); err == nil {
				if cached := outbound["_tools_cached"]; string(cached) == "true" {
					// Tools were cached → restore from original reqBody
					if len(reqBody.Tools) > 0 {
						outbound["tools"] = reqBody.Tools
						delete(outbound, "_tools_cached")
						if restored, err := json.Marshal(outbound); err == nil {
							bodyBytes = restored
						}
					}
				}
			}
		}
		if scResult != nil && scResult.Degraded {
			w.Header().Set("X-Gw-Compression-Degraded", "sliding_window_collision")
		}
		// Always populate outbound_msg_count / outbound_token_est from the
		// session compressor so request_logs.*_hot columns are non-NULL even
		// when no compression strategy was applied (pure delta-append).
		if scResult != nil {
			mc := scResult.MsgCount
			te := scResult.TokenEst
			logCtx.OutboundMsgCount = &mc
			logCtx.OutboundTokenEst = &te
		}
		if scResult != nil && scResult.CompressionStrategy != "" {
			logCtx.OutboundBody = scResult.OutboundBody
			logCtx.OutboundMsgHashes = []byte(scResult.MsgHashes)
			logCtx.OutboundStrategy = scResult.CompressionStrategy
			logCtx.OutboundSummaryMarker = scResult.SummaryMarker
			logCtx.OutboundWindowTriggered = scResult.WindowTriggered

			// ── Request WAL: async update on compression success ──────────────
			if h.requestLogger != nil && scResult != nil {
				meta := map[string]interface{}{
					"strategy":         scResult.CompressionStrategy,
					"msg_count":        scResult.MsgCount,
					"token_est":        scResult.TokenEst,
					"window_triggered": scResult.WindowTriggered,
					"lossiness":        scResult.Lossiness,
				}
				h.requestLogger.Update(&telemetry.LogUpdate{
					RequestID:           requestID,
					Stage:               telemetry.StageCompressed,
					Status:              telemetry.StatusPending,
					CompressionStrategy: scResult.CompressionStrategy,
					CompressionMeta:     meta,
				})
			}
		}
	}

	// ── Prompt-prefix stabilization (rtk borrowing, 2026-07-06) ──────────────
	// Reorder messages by stability class (system → tools → history → tail)
	// so the upstream provider's KV-prefix-cache hits maximise. Idempotent;
	// Stabilize is fail-open (returns the original bytes on any unrecognised
	// shape, so this can NEVER break a request).
	//
	// NOTE: no never_worse guard here, by design. Stabilize is a REORDER, not
	// a compression — its value is cache-hit uplift, not byte shrinkage, so
	// the output is typically the SAME length as the input (a swap, not a
	// trim). The guard's "processed must be strictly shorter" contract would
	// wrongly reject a legitimate reorder. The guard IS applied to the
	// compress and inject stages below, which are genuine shrink/expand ops.
	if h.promptCacheStabilize && len(bodyBytes) > 0 {
		if stab, report, serr := prefix.Stabilize(bodyBytes, prefix.Options{TailTurns: 1}); serr == nil && report != nil && report.Changed {
			bodyBytes = stab
			w.Header().Set("X-Gw-Prefix-Stabilized", report.Reason)
		}
	}

	// ── Request WAL: initial synchronous log at request arrival ─────────────
	if h.requestLogger != nil {
		tenantID := "default"
		if keyInfo != nil {
			tenantID = keyInfo.TenantID
		}
		initialReq := &telemetry.InitialRequest{
			RequestID:   requestID,
			TenantID:    tenantID,
			SessionID:   gwSessionID,
			ClientModel: clientModel,
		}
		if err := h.requestLogger.CreateInitial(r.Context(), initialReq); err != nil {
			slog.Warn("request_logger: CreateInitial failed", "request_id", requestID, "error", err)
		}
	}

	h.recordInitialRequestLog(
		requestID, clientModel, outboundForLog, endUser, "chat", keyInfo,
		clientID.Fingerprint.ClientProfile, identityHash,
		logCtx.ProviderID, logCtx.CredentialID, canonicalID,
		bodyBytes, txResult, egressProtocol, isStream,
		gwSessionID, gwTaskID,
		logCtx,
	)

	// ── Session Rotation Hook (2026-07-06) ────────────────────────────────
	// Detect credential rotation and update session state.
	if h.rotationHook != nil && gwSessionID != "" && logCtx.CredentialID != nil {
		providerName := ""
		if logCtx.ProviderID != nil {
			// Provider name will be resolved by the hook if needed
		}
		rotCtx := &session.RotationContext{
			SessionID: gwSessionID,
			TenantID: func() string {
				if keyInfo != nil {
					return keyInfo.TenantID
				}
				return "default"
			}(),
			OldCredentialID: 0, // Hook reads from session_pref
			NewCredentialID: *logCtx.CredentialID,
			Model:           clientModel,
			Provider:        providerName,
			SwitchReason:    session.SwitchReasonAutoRoute,
		}
		if err := h.rotationHook.OnRequestComplete(r.Context(), rotCtx); err != nil {
			slog.Warn("rotation_hook: OnRequestComplete failed", "session_id", gwSessionID, "error", err)
		}
	}

	var sessionKey string
	if sessionInfo != nil {
		sessionKey = sessionInfo.SessionKey
	}

	// ── Idempotent dedup (Track C C5, 2026-06-18) ────────────────────────
	// When a client retries the same (sessionID, requestID) within
	// the 5-minute window — network glitch, double-click, mobile
	// background-then-foreground — we short-circuit to a 202 +
	// X-Gw-Pending response. The pending store (C3) already
	// deduplicates at the durable layer; this is the in-memory
	// fast path that avoids re-running routing + circuit +
	// limiter checks.
	//
	// The cache is "first-writer wins" — a hit is recorded as
	// a real attempt by the cache, so concurrent retries see
	// a hit. This is the desired behaviour: only the first
	// request does the work, all subsequent retries are
	// informed of the same pending response.
	if h.idempotentCache != nil && sessionID != "" && requestID != "" {
		if h.idempotentCache.CheckAndMark(sessionID, requestID) {
			w.Header().Set("X-Gw-Pending", sessionID)
			w.Header().Set("X-Gw-Pending-Request", requestID)
			w.Header().Set("X-Gw-Idempotent-Replay", "true")
			w.Header().Set("Retry-After", "2")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":      "in_progress",
				"session_id":  sessionID,
				"request_id":  requestID,
				"retry_after": 2,
				"idempotent":  true,
			})
			logCtx.SetError("idempotent_replay", "duplicate request, returning in_progress")
			// Body and model already captured (from earlier reqBody parse)
			logCtx.EmitFailure("idempotent_replay", "duplicate request, returning in_progress", nil, nil)
			markLogged()
			return
		}
	}

	stickyKey := buildRouteStickyKey(tenant(keyInfo), appID(keyInfo), apiKeyIDPtr(keyInfo), clientID.Fingerprint.ClientProfile)

	// ── Armor security check (moved to line 898 — before provider resolution) ──

	// ── Prompt-cache-control injection (rtk borrowing, 2026-07-06) ──────────
	// When the resolved candidate declares SupportsPromptCache and the
	// operator has opted in (LLM_GATEWAY_PROMPT_CACHE_INJECT=1), place the
	// provider-appropriate cache marker (Anthropic ephemeral / checkpoint)
	// on the stabilized prefix boundary. InjectCacheParams is fail-open
	// (returns the original body on any error / unknown shape). A never_worse
	// guard guards against a malformed injection inflating the body.
	if h.cacheInjector != nil && h.promptCacheInject && len(candidates) > 0 && candidates[0].SupportsPromptCache {
		cand := candidates[0]
		if inj, ierr := h.cacheInjector.InjectCacheParams(r.Context(), gwSessionID, bodyBytes, &cand); ierr == nil && len(inj) > 0 {
			guarded, regressed := compression.NeverWorse(bodyBytes, inj, compression.GuardStageInject)
			if !regressed {
				bodyBytes = guarded
			}
		}
	}

	// Phase C (2026-06-22): Pass bodyBytes directly — per-candidate
	// protocol conversion now lives in the executor (IR path). The
	// degenerate selectChatUpstreamBodyBytes wrapper has been deleted.
	upstreamBody := bodyBytes

	// Phase C (2026-06-22): Protocol auto-detection via ir.DetectProtocol.
	// Falls back to "openai-completions" for backward compatibility when
	// IR mode is not active (h.executor.IR == nil).
	clientProtocol := "openai-completions"
	if h.executor.IR != nil {
		detected, _, _ := ir.DetectProtocol(bodyBytes)
		clientProtocol = detected
	}
	if isStream && clientProtocol == "openai-completions" {
		cfg := currentStreamRuntimeConfig()
		if cfg.enablePreStreamKeepalive {
			if psk, ok := startPreStreamKeepalive(w, cfg.keepaliveInterval); ok {
				preStream = psk
				preStreamPrepared = true
			}
		}
	}

	result, execErr := h.executor.Execute(&executors.ExecParams{
		W:                 w,
		R:                 r,
		BodyBytes:         upstreamBody,
		IsStream:          isStream,
		PreStreamPrepared: preStreamPrepared,
		OnStreamReady: func() {
			if preStream != nil {
				preStream.stop()
				preStream = nil
			}
		},
		ClientProtocol: clientProtocol,
		ClientModel:    clientModel,
		OutboundModel:  outboundForLog,
		ClientID:       clientID,
		Transform:      txResult,
		Resolution:     modelResolution,
		Candidates:     candidates,
		Policy:         policy,
		AuditBuilder:   auditBuilder,
		Capture:        streamCapture,
		ToolsRequested: requestHasTools(bodyBytes),
		SessionKey:     sessionKey,
		StickyKey:      stickyKey,
		KeyID: func() int {
			if keyInfo != nil {
				return keyInfo.ID
			}
			return 0
		}(),
		KeyConcurrentLimit: func() int {
			if keyInfo != nil {
				return keyInfo.EffectiveConcurrent()
			}
			return 0
		}(),
		// Round 47 compression v7 T13: tenant-namespaced Memora user_id.
		TenantID: func() string {
			if keyInfo != nil {
				return keyInfo.TenantID
			}
			return ""
		}(),
		// 2026-07-07: Multi-level sticky routing (L1: session+model, L2: client+model, L3: client).
		SessionID: gwSessionID,
		Model:     clientModel,
		AppID: func() *int {
			if keyInfo != nil {
				return &keyInfo.ApplicationID
			}
			return nil
		}(),
		ApiKeyID: func() *int {
			if keyInfo != nil {
				return &keyInfo.ID
			}
			return nil
		}(),
	})

	if execErr != nil {
		if preStream != nil {
			preStream.stop()
			preStream = nil
		}
		// ── Request WAL: synchronous update on execution failure ─────────────
		if h.requestLogger != nil {
			var pid, cid *int64
			if len(candidates) > 0 {
				p := int64(candidates[0].ProviderID)
				c := int64(candidates[0].CredentialID)
				pid, cid = &p, &c
			}
			update := &telemetry.LogUpdate{
				RequestID:            requestID,
				Stage:                telemetry.StageExecuteFail,
				Status:               telemetry.StatusFailure,
				Error:                execErr.Error(),
				UpstreamProviderID:   pid,
				UpstreamCredentialID: cid,
			}
			if err := h.requestLogger.UpdateSync(r.Context(), update); err != nil {
				slog.Warn("request_logger: UpdateSync failed", "request_id", requestID, "error", err)
			}
		}

		slog.Error("executor failed",
			"error", execErr,
			"model", clientModel,
		)
		var providerID, credentialID *int
		var tried int
		var failTrace *executors.Trace
		if execErrTyped, ok := execErr.(*executors.ExecuteError); ok {
			tried = execErrTyped.Tried
			failTrace = execErrTyped.Trace
		}
		if len(candidates) > 0 {
			providerID = intPtr(candidates[0].ProviderID)
			credentialID = intPtr(candidates[0].CredentialID)
		}

		// Track C C4 (2026-06-18): the executor demoted a slow
		// request to async mode. Surface 202 + X-Gw-Pending so the
		// client knows to poll GET /v1/sessions/{id}/pending-response
		// (see sessions/handler.go C3). The body is a small JSON
		// status object; the real response lands in pending store
		// when the async goroutine completes.
		var asyncErr *executors.AsyncPendingError
		if errors.As(execErr, &asyncErr) {
			if preStreamPrepared {
				logCtx.SetError("async_pending_unsupported_after_stream_start", "stream already prepared")
				writePrewarmedStreamError(w, "upstream request delayed; async fallback unavailable after stream start", "server_error", "provider_error")
				return
			}
			w.Header().Set("X-Gw-Pending", asyncErr.SessionID)
			w.Header().Set("X-Gw-Pending-Request", asyncErr.RequestID)
			w.Header().Set("Retry-After", "5")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":      "in_progress",
				"session_id":  asyncErr.SessionID,
				"request_id":  asyncErr.RequestID,
				"retry_after": 5,
				"started_at":  asyncErr.StartedAt.Format(time.RFC3339),
				"poll_url":    "/v1/sessions/" + asyncErr.SessionID + "/pending-response?request_id=" + asyncErr.RequestID,
			})
			slog.Info("async_pending_dispatched",
				"session_id", asyncErr.SessionID,
				"request_id", asyncErr.RequestID,
				"model", clientModel,
			)
			return
		}

		errCode := "provider_error"
		if execErrTyped, ok := execErr.(*executors.ExecuteError); ok && execErrTyped.Exhausted {
			// Content moderation rejection: render a 400 with the upstream
			// reason + actionable hint. NOT a provider/credential problem —
			// do NOT return 503 model_not_found (which misleads the client).
			if execErrTyped.LastKind == errorsx.KindContentFilter {
				reason := extractUpstreamReason(execErr)
				msg := i18n.T(r.Context(), i18n.MsgContentFilter,
					map[string]any{"Reason": reason})
				logCtx.SetOutboundModel(explicitOutbound)
				logCtx.failAndMark("content_filter", execErr.Error(),
					providerID, credentialID)
				h.emitFailedDecisionLog(requestID, clientModel, keyInfo, clientID,
					tried, modelResolution, txResult, "content_filter", failTrace,
					int(time.Since(startTime).Milliseconds()))
				markLogged()
				w.Header().Set("X-Gateway-Last-Kind", "content_filter")
				if preStreamPrepared {
					writePrewarmedStreamError(w, msg, "content_filter", "content_filter")
					return
				}
				writeErrorJSONWithKind(w, http.StatusBadRequest, requestID,
					msg, "content_filter", "content_filter", "content_filter",
					map[string]any{
						"stage":     "execution",
						"kind":      "content_filter",
						"tried":     execErrTyped.Tried,
						"reason":    reason,
						"hint":      i18n.T(r.Context(), i18n.MsgContentFilterHint),
						"retryable": false,
					})
				return
			}
			// Step 6 (2026-06-18): preserve backward-compat error.code
			// = "model_not_found" but surface the REAL underlying
			// kind in error.kind + X-Gateway-Last-Kind header. Many
			// in-the-wild failures labeled model_not_found are
			// actually rate_limit / concurrent / unreachable, which
			// breaks downstream alerting that keys on the surface
			// code. The kind field is the SSoT for the real cause;
			// the legacy code is preserved for clients that pattern-
			// match on it.
			errCode = "model_not_found"
			realKind := mapExecuteErrorToKind(execErrTyped)
			logCtx.SetOutboundModel(explicitOutbound)
			// 2026-06-20: write the REAL underlying kind to
			// request_logs.error_kind (e.g. "rate_limit",
			// "concurrent", "upstream_down") instead of the
			// backward-compat "model_not_found". The HTTP
			// error.code stays "model_not_found" for old clients
			// (set below in writeErrorJSONWithKind); the new
			// error_kind column + error.kind JSON field carry the
			// precise cause. Operators can now filter on
			// error_kind='rate_limit' directly without parsing the
			// X-Gateway-Last-Kind header.
			logCtx.failAndMark(errorKindOrFallback(realKind),
				fmt.Sprintf("No available provider for model '%s'. All %d candidates failed.", clientModel, execErrTyped.Tried),
				providerID, credentialID)
			h.emitFailedDecisionLog(requestID, clientModel, keyInfo, clientID, tried, modelResolution, txResult, errCode, failTrace, int(time.Since(startTime).Milliseconds()))
			markLogged()
			// Step 6: surface real kind in response header so log
			// scrapers and debug dashboards can see it without
			// parsing JSON.
			if realKind != "" {
				w.Header().Set("X-Gateway-Last-Kind", realKind)
			}
			if preStreamPrepared {
				writePrewarmedStreamError(w,
					fmt.Sprintf("No available provider for model '%s'. All %d candidates failed.", clientModel, execErrTyped.Tried),
					"server_error", "model_not_found")
				return
			}
			writeErrorJSONWithKind(w, http.StatusServiceUnavailable, requestID,
				fmt.Sprintf("No available provider for model '%s'. All %d candidates failed.", clientModel, execErrTyped.Tried),
				"server_error", "model_not_found", realKind, map[string]any{
					"stage":     "execution",
					"kind":      string(execErrTyped.LastKind),
					"attempts":  execErrTyped.Attempts,
					"tried":     execErrTyped.Tried,
					"retryable": errorsx.IsRetryable(execErrTyped.LastKind),
				})
			return
		}
		logCtx.SetOutboundModel(explicitOutbound)
		// 2026-06-23 P0 audit: capture upstream response body so transient
		// errors have a diagnostic message in request_logs.response_preview.
		// Without this, "error_kind=transient" rows are diagnostically
		// useless — operators can't see why upstream failed.
		enrichedErrMsg := execErr.Error()
		if ue, ok := extractUpstreamError(execErr); ok {
			// 2026-06-30: 记录上游状态码到 request_logs (migration 320)
			if ue.StatusCode > 0 {
				logCtx.SetUpstreamStatus(ue.StatusCode)
			}
			if len(ue.Body) > 0 {
				logCtx.SetResponseBody(ue.Body)
				preview := string(ue.Body)
				if len(preview) > 320 {
					preview = preview[:320] + "..."
				}
				if ue.StatusCode > 0 {
					enrichedErrMsg = fmt.Sprintf("upstream HTTP %d: %s | kind=%s", ue.StatusCode, preview, ue.Kind)
				} else {
					enrichedErrMsg = fmt.Sprintf("network error: %s | body: %s", ue.Message, preview)
				}
				slog.Warn("upstream failed with body",
					"request_id", requestID,
					"credential_id", credentialID,
					"provider_id", providerID,
					"status_code", ue.StatusCode,
					"kind", string(ue.Kind),
					"body_preview", preview,
				)
			}
		}
		logCtx.failAndMark("provider_error", enrichedErrMsg, providerID, credentialID)
		h.emitFailedDecisionLog(requestID, clientModel, keyInfo, clientID, tried, modelResolution, txResult, errCode, failTrace, int(time.Since(startTime).Milliseconds()))
		markLogged()
		debugInfo := map[string]any{
			"stage":     "execution",
			"tried":     tried,
			"retryable": false,
		}
		if execErrTyped, ok := execErr.(*executors.ExecuteError); ok {
			debugInfo["kind"] = string(execErrTyped.LastKind)
			debugInfo["attempts"] = execErrTyped.Attempts
			debugInfo["retryable"] = errorsx.IsRetryable(execErrTyped.LastKind)
		}
		if preStreamPrepared {
			writePrewarmedStreamError(w, "upstream request failed", "server_error", "provider_error")
			return
		}
		writeErrorJSONWithDebug(w, http.StatusBadGateway, requestID, i18n.T(r.Context(), i18n.MsgProviderError), "server_error", "provider_error", debugInfo)
		return
	}
	if preStream != nil {
		preStream.stop()
		preStream = nil
	}

	auditBuilder.Success(true).Latency(time.Duration(result.LatencyMs) * time.Millisecond)
	// Phase D (2026-06-22): use InboundBody (original client body) for audit
	// logging, not RequestBody (which may be protocol-converted for upstream).
	h.emitTelemetry(auditBuilder.Build(), result, endUser, keyInfo, streamCapture, "chat", txResult, result.InboundBody, result.ResponseBody, logCtx)

	// ── Response Interceptor (2026-06-29, auto-control feature) ─────────
	// Call interceptor after successful execution but before final metrics.
	// This enables automatic handoff when context limits are reached and
	// goal-mode continuous execution.
	if h.responseInterceptor != nil && result != nil {
		// Calculate total message count from request body
		msgCount := extractMessageCount(bodyBytes)

		interceptReq := &ResponseInterceptRequest{
			SessionID: gwSessionID,
			RequestID: requestID,
			TenantID: func() string {
				if keyInfo != nil {
					return keyInfo.TenantID
				}
				return ""
			}(),
			ClientModel:  clientModel,
			ResponseBody: result.ResponseBody,
			TokensUsed:   extractTotalTokens(result.ResponseBody, streamCapture),
			ContextWindow: func() int {
				if len(candidates) > 0 && candidates[0].ContextWindow != nil {
					return *candidates[0].ContextWindow
				}
				return 0
			}(),
			MessageCount: msgCount,
			FinishReason: extractFinishReason(result.ResponseBody),
			IsStreaming:  isStream,
		}

		if isStream {
			// For streaming, call InterceptStreamEnd.
			//
			// Reassemble the streamed text + finish_reason from the stream
			// capture so stream-end interceptors (goal completion detection /
			// audit) see the same full response body the non-stream path
			// gets. When no capture is available the fields stay empty and
			// the goal hook falls back to its legacy length-based behaviour.
			interceptMeta := &ResponseStreamMeta{
				SessionID:     gwSessionID,
				RequestID:     requestID,
				TenantID:      interceptReq.TenantID,
				ClientModel:   clientModel,
				ContextWindow: interceptReq.ContextWindow,
				MessageCount:  msgCount,
				TokensUsed:    interceptReq.TokensUsed,
				ResponseBody:  reassembleStreamBody(streamCapture),
				FinishReason:  reassembleFinishReason(streamCapture),
			}

			if endResult, err := h.responseInterceptor.InterceptStreamEnd(r.Context(), interceptMeta); err != nil {
				slog.Warn("response_interceptor_stream_end_failed", "error", err, "session_id", gwSessionID)
			} else if endResult != nil && len(endResult.InjectFollowUp) > 0 {
				// Inject follow-up request asynchronously.
				// Carry the follow-up depth from the request context so
				// recursive follow-ups are bounded by MaxFollowUpDepth.
				// Detach from r.Context() (Background) since the response
				// is already complete and r.Context() may be canceled.
				followUpCtx := withFollowUpDepth(context.Background(), FollowUpDepthFromContext(r.Context()))
				go h.injectFollowUpRequest(followUpCtx, gwSessionID, endResult.InjectFollowUp, endResult.Action)
			}
		} else {
			// For non-streaming, call InterceptNonStream
			if interceptResult, err := h.responseInterceptor.InterceptNonStream(r.Context(), interceptReq); err != nil {
				slog.Warn("response_interceptor_failed", "error", err, "session_id", gwSessionID)
			} else if interceptResult != nil {
				if interceptResult.ShouldBlock {
					slog.Info("response_interceptor_blocked", "session_id", gwSessionID, "action", interceptResult.Action)
					// Response was blocked, don't continue
					return
				}
				if len(interceptResult.InjectFollowUp) > 0 {
					// Inject follow-up request asynchronously.
					// Carry the follow-up depth from the request context.
					followUpCtx := withFollowUpDepth(context.Background(), FollowUpDepthFromContext(r.Context()))
					go h.injectFollowUpRequest(followUpCtx, gwSessionID, interceptResult.InjectFollowUp, interceptResult.Action)
				}
				// Apply ModifiedBody (e.g. output-compliance redaction).
				//
				// NOTE (2026-07-09): for the historical non-stream path the bytes
				// are already written to the client inside executor.Execute, so
				// this rewrite takes effect for downstream telemetry, the request
				// log, the session-cache, and any buffered/pending-store path —
				// NOT a retroactive client rewrite. Stream-end redaction is
				// applied at write-time via the transform pipeline; this metadata
				// path ensures the persisted/observed body matches what policy
				// intended (so pii_stripped tagging + session_tags stay accurate).
				if len(interceptResult.ModifiedBody) > 0 && result != nil {
					result.ResponseBody = interceptResult.ModifiedBody
					if interceptResult.Metadata != nil {
						slog.Info("response_interceptor_modified_body",
							"session_id", gwSessionID, "action", interceptResult.Action)
					}
				}
			}
		}
	}

	// ── Request WAL: async update on execution success ─────────────
	if h.requestLogger != nil && result != nil {
		var pid, cid *int64
		if result.Candidate.ProviderID > 0 {
			p := int64(result.Candidate.ProviderID)
			pid = &p
		}
		if result.Candidate.CredentialID > 0 {
			c := int64(result.Candidate.CredentialID)
			cid = &c
		}

		// Extract token counts from streamCapture if available
		var promptTokens, completionTokens int
		if streamCapture != nil {
			m := streamCapture.SummaryAsMap()
			if pt, ok := m["prompt_tokens"].(int); ok {
				promptTokens = pt
			}
			if ct, ok := m["completion_tokens"].(int); ok {
				completionTokens = ct
			}
		}

		completedAt := time.Now()
		h.requestLogger.Update(&telemetry.LogUpdate{
			RequestID:            requestID,
			Stage:                telemetry.StageCompleted,
			Status:               telemetry.StatusSuccess,
			UpstreamProviderID:   pid,
			UpstreamCredentialID: cid,
			CompletionTokens:     completionTokens,
			PromptTokens:         promptTokens,
			CompletedAt:          completedAt,
		})
	}

	markLogged()
}

func (h *ChatHandler) emitTelemetry(evt audit.Event, result *executors.ExecuteResult, endUser string, keyInfo *authentication.KeyInfo, capture *audit.StreamCapture, requestMode string, txResult *transformation.TransformResult, requestBody []byte, responseBody []byte, logCtx *RequestLogContext) {
	if h.telemetryClient == nil || !h.telemetryClient.Enabled() {
		return
	}

	var apiKeyID *int
	var tenantID = "default"
	var applicationID *int
	keyPrefix, keyOwner, appCode := "", "", ""
	if keyInfo != nil {
		apiKeyID = &keyInfo.ID
		tenantID = keyInfo.TenantID
		applicationID = appID(keyInfo)
		keyPrefix, keyOwner, appCode = keyMetaFromKeyInfo(keyInfo)
	}

	dl := &telemetry.DecisionLogEntry{
		RequestID:          evt.RequestID,
		TenantID:           tenantID,
		APIKeyID:           apiKeyID,
		Model:              canonicalOrClient(evt.CanonicalName, evt.ClientModel),
		ChosenCredentialID: intPtr(result.Candidate.CredentialID),
		ChosenProviderID:   intPtr(result.Candidate.ProviderID),
		Tier:               intPtr(result.Candidate.Tier),
		CandidatesTried:    1,
		LatencyMs:          result.LatencyMs,
		Success:            true,
		ClientModel:        strPtr(evt.ClientModel),
		OutboundModel:      strPtr(evt.OutboundModel),
		ClientProfile:      strPtr(evt.ClientProfile),
		RequestMode:        strPtr(requestMode),
		IdentityHash:       strPtr(evt.IdentityHash),
		TransformRuleID:    strPtr(evt.TransformRule),
	}
	if evt.ResolutionPath != "" {
		dl.ResolutionPath = strPtr(evt.ResolutionPath)
	}
	if evt.CanonicalName != "" {
		dl.CanonicalModel = strPtr(evt.CanonicalName)
	}
	if result.Candidate.Protocol != "" {
		dl.EgressProtocol = strPtr(result.Candidate.Protocol)
	}
	if txResult != nil && txResult.OutboundModel != "" {
		dl.OutboundModel = strPtr(txResult.OutboundModel)
	}
	if result.Trace != nil {
		traceJSON, _ := json.Marshal(result.Trace)
		dl.DecisionTrace = traceJSON
	} else if evt.DecisionTrace != nil {
		traceJSON, _ := json.Marshal(evt.DecisionTrace)
		dl.DecisionTrace = traceJSON
	}
	if result.Candidate.RawModel != "" {
		dl.ResolvedRawModel = strPtr(result.Candidate.RawModel)
		dl.ResolutionRawModels = []string{result.Candidate.RawModel}
	}

	var requestBodyText *string
	if len(requestBody) > 0 {
		v := string(requestBody)
		requestBodyText = &v
	}
	var responseBodyText *string
	if len(responseBody) > 0 {
		v := string(responseBody)
		// For streaming responses, the last SSE chunk sent to the client (and captured
		// in result.ResponseBody) often does NOT include the usage block. Merge the
		// stream-captured usage values into the response_body JSON so the persisted
		// row contains a complete `usage` block for downstream auditors/queries.
		if capture != nil {
			m := capture.SummaryAsMap()
			var pt, ct, crt, cwt int
			if val, ok := m["prompt_tokens"].(int); ok {
				pt = val
			}
			if val, ok := m["completion_tokens"].(int); ok {
				ct = val
			}
			if val, ok := m["cache_read_tokens"].(int); ok {
				crt = val
			}
			if val, ok := m["cache_write_tokens"].(int); ok {
				cwt = val
			}
			if pt > 0 || ct > 0 {
				v = string(injectUsageIntoResponseBody([]byte(v), pt, ct, crt, cwt))
			}
		} else if len(responseBody) > 0 {
			// Non-streaming: ensure we always pull whatever usage is in the body
			// (this is the primary path; capture==nil branch below is a fallback).
			ept, ect, ecrt, ecwt := extractTokensFromResponseBody(responseBody)
			if ept > 0 || ect > 0 {
				v = string(injectUsageIntoResponseBody([]byte(v), ept, ect, ecrt, ecwt))
			}
		}
		responseBodyText = &v
	} else if capture != nil {
		m := capture.SummaryAsMap()
		var textContent string
		if v, ok := m["stream_text_content"].(string); ok && v != "" {
			textContent = v
		}
		// 2026-06-25 T-NEW-1: structured tool_calls from the IR layer (see
		// audit/stream.go mergeToolCall). SummaryAsMap emits them under
		// the "tool_calls" key. Cast to []map[string]any so we can rewrite
		// each entry to drop the streaming-only "index" field.
		var toolCallsFromStream []map[string]any
		if v, ok := m["tool_calls"].([]map[string]any); ok && len(v) > 0 {
			toolCallsFromStream = v
		}
		// audit/stream.go ObserveChunk still emits a legacy "[Tool Call:
		// <name>]\n<arguments>" text rendering into stream_text_content
		// (kept for backward compatibility with consumers that read it
		// as a free-text preview). When we are also embedding structured
		// tool_calls in message.tool_calls, that legacy rendering would
		// duplicate the data inside `content` and break clients that
		// parse `content` as plain assistant text. Strip it.
		if toolCallsFromStream != nil {
			textContent = stripLegacyToolCallText(textContent)
		}
		if textContent != "" || toolCallsFromStream != nil {
			var pt, ct int
			if v, ok := m["prompt_tokens"].(int); ok {
				pt = v
			}
			if v, ok := m["completion_tokens"].(int); ok {
				ct = v
			}
			// 2026-06-25 T-NEW-1: When streaming, the IR layer (audit/stream.go
			// mergeToolCall) accumulates structured tool_calls into
			// sc.ToolCalls. Persist them into the synthetic response_body as
			// `message.tool_calls` so downstream admin UI / API consumers can
			// read tool_calls directly from response_body (instead of only
			// from the dedicated request_logs.tool_calls JSONB column).
			//
			// We strip the streaming-only `index` key from each entry: OpenAI
			// final-response tool_calls do NOT carry `index` (only streaming
			// deltas do), and including it confuses clients that strictly
			// validate the schema. We also flip finish_reason to "tool_calls"
			// when at least one tool call was emitted, matching the upstream
			// OpenAI Chat Completions contract.
			finishReason := "stop"
			var cleanedToolCalls []map[string]any
			if toolCallsFromStream != nil {
				finishReason = "tool_calls"
				cleanedToolCalls = make([]map[string]any, 0, len(toolCallsFromStream))
				for _, tc := range toolCallsFromStream {
					entry := map[string]any{}
					for k, v := range tc {
						// Skip streaming-only fields
						if k == "index" {
							continue
						}
						entry[k] = v
					}
					cleanedToolCalls = append(cleanedToolCalls, entry)
				}
			}
			message := map[string]any{"role": "assistant", "content": textContent}
			if len(cleanedToolCalls) > 0 {
				message["tool_calls"] = cleanedToolCalls
			}
			pseudoBody := map[string]any{
				"choices": []map[string]any{
					{"message": message, "finish_reason": finishReason},
				},
			}
			if pt > 0 || ct > 0 {
				pseudoBody["usage"] = map[string]any{"prompt_tokens": pt, "completion_tokens": ct, "total_tokens": pt + ct}
			}
			if b, err := json.Marshal(pseudoBody); err == nil {
				v := string(b)
				responseBodyText = &v
			}
		} else if previewStr, ok := m["response_preview"].(string); ok && previewStr != "" {
			// Fallback: textContent is empty (e.g. function-calling responses
			// that only carry `delta.tool_calls` and no `delta.content`, or
			// request_logs that are stored for audit even when no parsed text
			// was collected). Persist the raw SSE preview as the body so the
			// row is non-empty and downstream auditors/queries can still
			// inspect the wire format.
			responseBodyText = strPtr(previewStr)
		}
	}
	requestPreviewText := requestPreview(requestBody)
	transformSummaryText := transformSummary(txResult, evt.OutboundModel)
	responsePreviewText := responsePreview(responseBody)
	var requestPreviewPtr *string
	if requestPreviewText != "" {
		requestPreviewPtr = strPtr(requestPreviewText)
	}
	var transformSummaryPtr *string
	if transformSummaryText != "" {
		transformSummaryPtr = strPtr(transformSummaryText)
	}
	var responsePreviewPtr *string
	if responsePreviewText != "" {
		responsePreviewPtr = strPtr(responsePreviewText)
	}

	// 2026-06-30 PR-5: nil-safe accessor — logCtx may be nil for
	// domains/streaming/messages.go and responses.go paths.
	var clientReqIDPtr *string
	if logCtx != nil && logCtx.ClientRequestID != "" {
		s := logCtx.ClientRequestID
		clientReqIDPtr = &s
	}

	loggedOutbound := outboundModelForLog(evt.ClientModel, evt.OutboundModel, result.Candidate.RawModel)

	reqLog := &telemetry.RequestLogEntry{
		RequestID:       evt.RequestID,
		TenantID:        tenantID,
		ApplicationID:   applicationID,
		APIKeyID:        apiKeyID,
		APIKeyPrefix:    strPtr(keyPrefix),
		APIKeyOwnerUser: strPtr(keyOwner),
		ApplicationCode: strPtr(appCode),
		EndUserID:       strPtr(endUser),
		ClientModel:     strPtr(evt.ClientModel),
		OutboundModel:   strPtr(loggedOutbound),
		CredentialID:    intPtr(result.Candidate.CredentialID),
		ProviderID:      intPtr(result.Candidate.ProviderID),
		ClientProfile:   strPtr(evt.ClientProfile),
		RequestMode:     strPtr(requestMode),
		LatencyMs:       intPtr(result.LatencyMs),
		Success:         true,
		RequestStatus:   strPtr(telemetry.RequestStatusSuccess),
		// 2026-06-20: explicitly clear ErrorKind so any stale
		// error_kind from a prior failed UPDATE attempt for the
		// same request_id is wiped. The UPSERT also handles this
		// via CASE WHEN success=TRUE, but setting it here makes
		// the intent obvious and removes a class of cross-request
		// pollution where an old failure tag leaks into a fresh
		// success row.
		ErrorKind:        strPtr(""),
		IdentityHash:     strPtr(evt.IdentityHash),
		RequestPreview:   requestPreviewPtr,
		TransformSummary: transformSummaryPtr,
		ResponsePreview:  responsePreviewPtr,
		RequestBody:      requestBodyText,
		ResponseBody:     responseBodyText,
		// 2026-07-01 P0 fix: stream_chunks_sent / stream_chunk_errors are
		// NOT NULL columns (migration 320). Initialize from logCtx (which
		// increments via IncrementStreamChunksSent in the streaming
		// callbacks) and fall back to 0 for non-streaming paths. Without
		// these defaults the INSERT failed with SQLSTATE 23502 and
		// request_logs_2026_07 stopped accepting new rows on 184.
		StreamChunksSent:  intPtr(streamChunksSentFromLogCtx(logCtx)),
		StreamChunkErrors: intPtr(streamChunkErrorsFromLogCtx(logCtx)),
		// Round 47 compression v7 T-NEW-3: write the compression event
		// captured by the executor's 4xx recovery (see
		// executors.context_summarize.handleContextLengthRecovery) into
		// request_logs.compression_*. Operators can then SQL-trace the
		// parent-child chain via parent_request_id.
		//
		// We only set these when the executor actually rewrote the body;
		// nil pointers → NULL in PG → the existing partial index on
		// parent_request_id stays cheap.
		CompressionReason:   result.CompressionReason,
		CompressionStrategy: result.CompressionStrategy,
		CompressionMeta:     result.CompressionMeta,
		ParentRequestID:     result.ParentRequestID,
		// 2026-06-26: persist the client-supplied X-Request-Id for debug
		// alongside the server-generated RequestID. Distinguishes
		// legitimate client retries (same client_request_id, distinct
		// request_id) from genuinely fresh requests.
		//
		// 2026-06-30 PR-5: nil-safe — logCtx may be nil from
		// domains/streaming/messages.go and responses.go paths; see
		// clientReqIDPtr setup above. Audit P0-6.
		ClientRequestID: clientReqIDPtr,
	}
	// v3: if v7 compression_strategy is empty but a session compressor strategy
	// exists, prefer the session compressor value so the row is queryable.
	// (v7 and v3 strategies are mutually exclusive in a single request.)

	if capture != nil {
		m := capture.SummaryAsMap()
		// Only set pointers when the captured value is non-zero. Some providers
		// (e.g. minimax) include `"usage": null` in every SSE chunk, so the
		// stream summary may have the keys present with value 0. Setting a
		// non-nil *int to 0 would otherwise suppress the estimator fallback
		// below (because the nil-check would be false).
		if v, ok := m["prompt_tokens"].(int); ok && v > 0 {
			reqLog.PromptTokens = &v
		}
		if v, ok := m["completion_tokens"].(int); ok && v > 0 {
			reqLog.CompletionTokens = &v
		}
		if v, ok := m["cache_read_tokens"].(int); ok && v > 0 {
			reqLog.CacheReadTokens = &v
		}
		if v, ok := m["cache_write_tokens"].(int); ok && v > 0 {
			reqLog.CacheWriteTokens = &v
		}
		if v, ok := m["stream_first_chunk_ms"].(int); ok {
			reqLog.StreamFirstChunkMs = &v
		}
		if v, ok := m["stream_chunk_count"].(int); ok {
			reqLog.StreamChunkCount = &v
		}
		if v, ok := m["stream_chunks_sent"].(int); ok {
			reqLog.StreamChunksSent = &v
		}
		if v, ok := m["response_checksum"].(string); ok {
			reqLog.ResponseChecksum = &v
		}
		if v, ok := m["stream_done_received"].(bool); ok {
			reqLog.StreamDoneReceived = &v
		}
		if v, ok := m["stream_interrupted"].(bool); ok {
			reqLog.StreamInterrupted = &v
			if v {
				isErr, detailCode := classifyStreamInterruption(m)
				if isErr {
					reqLog.Success = false
					reqLog.RequestStatus = strPtr(telemetry.RequestStatusFailure)
					reqLog.ErrorKind = strPtr("stream_error")
					reqLog.FailureStage = strPtr("upstream")
				}
				if detailCode != "" {
					reqLog.FailureDetailCode = strPtr(detailCode)
				}
			}
		}

		// 2026-06-23: Detect empty responses
		// An empty response is one that:
		//   1. Has very few chunks (<= 3)
		//   2. Has zero completion tokens
		//   3. Has no content preview
		//   4. Has no upstream finish_reason
		// This pattern indicates the upstream returned no actual content,
		// despite sending [DONE]. Mark as failure to prevent billing
		// and alert monitoring.
		//
		// Provider 18 (NVIDIA NIM) has ~13% empty response rate across credentials.
		if reqLog.Success { // Only check if currently marked as success
			isEmpty := detectEmptyStreamResponse(m, reqLog)
			if isEmpty {
				reqLog.Success = false
				reqLog.RequestStatus = strPtr(telemetry.RequestStatusFailure)
				reqLog.ErrorKind = strPtr("empty_response")
				reqLog.FailureStage = strPtr("upstream_empty_response")
				reqLog.FailureDetailCode = strPtr("zero_tokens_few_chunks")
			}
		}

		// 2026-06-19 T-NEW-7: split the semantic overload of failure_detail_code.
		// audit/audit.go::SummaryAsMap now publishes the upstream finish_reason
		// under the new "upstream_finish_reason" key (for BOTH success and
		// failure rows). It only republishes the value as
		// "failure_detail_code" when the value is a known interruption code
		// (e.g. eof_without_done, stream_timeout). The block below mirrors
		// that discipline into the request_log row:
		//
		//   1. Read upstream_finish_reason → UpstreamFinishReason column.
		//   2. Fall back to m["failure_detail_code"] for UpstreamFinishReason
		//      ONLY if it wasn't set above (legacy pre-018 captures).
		//   3. Do NOT touch FailureDetailCode here — that is already set by
		//      the stream_interrupted branch above for real failures. For
		//      successful streams we leave it NULL.
		if v, ok := m["upstream_finish_reason"].(string); ok && v != "" {
			reqLog.UpstreamFinishReason = strPtr(v)
		}
		if reqLog.UpstreamFinishReason == nil {
			if v, ok := m["failure_detail_code"].(string); ok && v != "" {
				// Legacy pre-018 capture path — promotion of the old
				// "failure_detail_code == finish_reason" usage to the new
				// column. Keep the value in BOTH columns for now so the
				// admin UI does not regress before the next deploy
				// rewires the relay-side capture.
				reqLog.UpstreamFinishReason = strPtr(v)
			}
		}
		if v, ok := m["response_preview"].(string); ok && v != "" && reqLog.ResponsePreview == nil {
			reqLog.ResponsePreview = strPtr(v)
		}
		// 2026-06-19 quality fix mode: pull stream-collected quality
		// signals out of the capture summary. The stream reader
		// already pushed the running flag list into the capture;
		// the audit summary serialises it under "quality_flags".
		if v, ok := m["quality_flags"].([]string); ok && len(v) > 0 {
			reqLog.QualityFlags = v
		}
		if v, ok := m["quality_fix_actions"].(string); ok && v != "" {
			reqLog.QualityFixActions = []byte(v)
		}
		if v, ok := m["quality_score"].(float64); ok {
			reqLog.QualityScore = &v
		}
		// 2026-06-23: structured tool_calls from streaming (042_tool_calls_column.sql).
		// The audit.StreamCapture.ToolCalls array is marshaled into SummaryAsMap
		// as "tool_calls" ([]map[string]any). Convert it to json.RawMessage
		// for the RequestLogEntry.
		if v, ok := m["tool_calls"].([]map[string]any); ok && len(v) > 0 {
			if b, err := json.Marshal(v); err == nil {
				reqLog.ToolCalls = b
			}
		}
	}

	// 2026-06-19 quality fix mode (017_quality_fix_mode.sql): propagate
	// the post-processed quality signals into the request_log row.
	// The non-stream path stores the result directly on
	// ExecuteResult; the stream path already pushed them into the
	// capture above (m["quality_flags"] etc.). For non-stream we
	// simply read the fields that the executor set.
	if len(result.QualityFlags) > 0 {
		reqLog.QualityFlags = result.QualityFlags
	}
	if len(result.QualityFixActions) > 0 {
		reqLog.QualityFixActions = result.QualityFixActions
	}
	if result.QualityScore != nil {
		reqLog.QualityScore = result.QualityScore
	}

	// 2026-06-30: Extract tokens from response body for BOTH streaming and non-streaming.
	// Previously this was inside the `if capture != nil` block, which meant non-streaming
	// requests never had their completion_tokens/cache tokens extracted from the response.
	// This caused request_logs to show NULL for completion_tokens and cache_*_tokens even
	// when the upstream response contained a complete usage block.
	if len(result.ResponseBody) > 0 {
		pt, ct, crt, cwt := extractTokensFromResponseBody(result.ResponseBody)
		if pt > 0 || ct > 0 {
			// Only overwrite if not already set from streaming capture
			if reqLog.PromptTokens == nil || *reqLog.PromptTokens == 0 {
				reqLog.PromptTokens = &pt
			}
			if reqLog.CompletionTokens == nil || *reqLog.CompletionTokens == 0 {
				reqLog.CompletionTokens = &ct
			}
			if crt > 0 && (reqLog.CacheReadTokens == nil || *reqLog.CacheReadTokens == 0) {
				reqLog.CacheReadTokens = &crt
			}
			if cwt > 0 && (reqLog.CacheWriteTokens == nil || *reqLog.CacheWriteTokens == 0) {
				reqLog.CacheWriteTokens = &cwt
			}
		}
	}

	// Fallback: if upstream did not return a usage block (e.g. minimax, certain
	// volcengine pass-through responses), estimate tokens locally from the
	// request/response text and mark the row so the UI can distinguish the
	// estimated value from a real LLM-reported count.
	// Check both nil AND zero: providers like minimax emit `"usage": null` in
	// every SSE chunk, which results in stream-captured pointers to 0 that
	// would otherwise suppress this fallback.
	promptZero := reqLog.PromptTokens == nil || *reqLog.PromptTokens == 0
	completionZero := reqLog.CompletionTokens == nil || *reqLog.CompletionTokens == 0
	if promptZero && completionZero {
		estPrompt := estimatePromptTokens(result.RequestBody)
		estCompletion := estimateCompletionTokens(result.ResponseBody)
		if estPrompt > 0 || estCompletion > 0 {
			reqLog.PromptTokens = &estPrompt
			reqLog.CompletionTokens = &estCompletion
			reqLog.UsageSource = strPtr(UsageSourceEstimated)
		}

		// Record format anomaly if estimation failed or returned zero completion tokens
		// despite having response content (helps detect provider format changes)
		if h.anomalyRecorder != nil && reqLog.Success {
			providerID := result.Candidate.ProviderID
			providerCode := result.Candidate.CatalogCode
			clientModel := evt.ClientModel
			outboundModel := evt.OutboundModel

			// Detect anomaly type
			var anomalyType AnomalyType
			var severity Severity
			if estPrompt == 0 && estCompletion == 0 && len(result.ResponseBody) > 0 {
				anomalyType = AnomalyExtractionFailed
				severity = SeverityHigh
			} else if estCompletion == 0 && len(result.ResponseBody) > 100 {
				anomalyType = AnomalyZeroCompletion
				severity = SeverityMedium
			} else {
				anomalyType = AnomalyMissingUsage
				severity = SeverityLow
			}

			// Sample before recording (avoid flooding table)
			if ShouldRecordAnomaly(anomalyType, providerCode) {
				contentSize := len(result.ResponseBody)
				structure := AnalyzeResponseStructure(result.ResponseBody)
				sample := TruncateForSample(string(result.ResponseBody), 1000)
				usageSource := UsageSourceEstimated
				var providerCodePtr *string
				if providerCode != "" {
					providerCodePtr = &providerCode
				}
				var tenantCodePtr *string
				if tenantID != "" {
					tenantCodePtr = &tenantID
				}

				go func() {
					// Record asynchronously to avoid blocking request completion
					recordCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					defer cancel()

					_ = h.anomalyRecorder.RecordAnomaly(recordCtx, AnomalyRecord{
						RequestID:      evt.RequestID,
						ProviderID:     &providerID,
						ProviderCode:   providerCodePtr,
						ClientModel:    &clientModel,
						OutboundModel:  &outboundModel,
						AnomalyType:    anomalyType,
						Severity:       severity,
						UsageSource:    &usageSource,
						ExpectedTokens: &estCompletion,
						ActualTokens:   nil,
						ContentSize:    &contentSize,
						Structure:      structure,
						ResponseSample: &sample,
						TenantID:       tenantCodePtr,
					})
				}()
			}
		}

	} else if reqLog.UsageSource == nil {
		reqLog.UsageSource = strPtr(UsageSourceLLM)
	}

	if reqLog.PromptTokens != nil || reqLog.CompletionTokens != nil {
		cost := CalcCost(CostInput{
			PromptTokens:     floatPtrFromInt(reqLog.PromptTokens),
			CompletionTokens: floatPtrFromInt(reqLog.CompletionTokens),
			CacheReadTokens:  floatPtrFromInt(reqLog.CacheReadTokens),
			CacheWriteTokens: floatPtrFromInt(reqLog.CacheWriteTokens),
			PriceIn:          result.Candidate.PriceInPer1M,
			PriceOut:         result.Candidate.PriceOutPer1M,
			CacheReadPrice:   result.Candidate.CacheReadPricePer1M,
			CacheWritePrice:  result.Candidate.CacheWritePricePer1M,
		})
		reqLog.CostUSD = cost
		// For CNY-priced providers (cost_usd is intentionally nil) record the
		// native-currency value in cost_display so /request-logs can show it.
		if cost == nil && result.Candidate.Currency != "" && result.Candidate.Currency != "USD" {
			cnyCost := CalcCost(CostInput{
				PromptTokens:     floatPtrFromInt(reqLog.PromptTokens),
				CompletionTokens: floatPtrFromInt(reqLog.CompletionTokens),
				CacheReadTokens:  floatPtrFromInt(reqLog.CacheReadTokens),
				CacheWriteTokens: floatPtrFromInt(reqLog.CacheWriteTokens),
				PriceIn:          result.Candidate.PriceInPer1M,
				PriceOut:         result.Candidate.PriceOutPer1M,
				CacheReadPrice:   result.Candidate.CacheReadPricePer1M,
				CacheWritePrice:  result.Candidate.CacheWritePricePer1M,
			})
			reqLog.CostDisplay = cnyCost
			curr := result.Candidate.Currency
			reqLog.CostCurrency = &curr
		}
	}

	if h.maasSvc != nil && keyInfo != nil && keyInfo.TenantID != "" && keyInfo.TenantID != "default" {
		pt, ct, crt, cwt := 0, 0, 0, 0
		if reqLog.PromptTokens != nil {
			pt = *reqLog.PromptTokens
		}
		if reqLog.CompletionTokens != nil {
			ct = *reqLog.CompletionTokens
		}
		if reqLog.CacheReadTokens != nil {
			crt = *reqLog.CacheReadTokens
		}
		if reqLog.CacheWriteTokens != nil {
			cwt = *reqLog.CacheWriteTokens
		}
		if pt > 0 || ct > 0 || crt > 0 || cwt > 0 {
			canonical := evt.CanonicalName
			if canonical == "" {
				canonical = evt.ClientModel
			}
			chargeCtx, chargeCancel := context.WithTimeout(context.Background(), 5*time.Second)
			charged, err := h.maasSvc.ChargeRequest(chargeCtx, keyInfo.TenantID, evt.RequestID, canonical, pt, ct, crt, cwt)
			chargeCancel()
			if err == nil && charged > 0 {
				reqLog.CreditsCharged = &charged
			} else if err != nil {
				slog.Warn("maas charge failed", "request_id", evt.RequestID, "tenant_id", keyInfo.TenantID, "error", err)
			}
		}
	}

	dl.PromptTokens = reqLog.PromptTokens
	dl.CompletionTokens = reqLog.CompletionTokens
	dl.CostUSD = reqLog.CostUSD
	if len(requestBody) > 0 {
		rb := len(requestBody)
		dl.RequestBytes = &rb
	}
	if len(responseBody) > 0 {
		rsb := len(responseBody)
		dl.ResponseBytes = &rsb
	}
	if result.Trace != nil && len(result.Trace.PlannedCandidates) > 0 {
		dl.CandidatesTried = len(result.Trace.PlannedCandidates)
	}
	h.telemetryClient.EmitDecisionLog(dl)

	applyKeyInfoToRequestLog(reqLog, keyInfo)
	// v3: merge session compressor outbound fields into the log entry.
	applySessionCompressorFields(reqLog, logCtx)
	h.telemetryClient.EmitRequestLogUpdate(reqLog)
	if h.requestLogHook != nil {
		h.requestLogHook(reqLog)
	}

	// v2.1: emit implicit feedback signal for the auto-route tuning loop.
	// Best-effort async write via the dedicated tuning writer; never blocks
	// the request path on DB latency.
	if reqLog.IsAutoRequest != nil && *reqLog.IsAutoRequest && reqLog.TaskType != nil {
		latencyMs := 0
		if reqLog.LatencyMs != nil {
			latencyMs = *reqLog.LatencyMs
		}
		h.emitTuningSignal(reqLog, reqLog.Success, latencyMs)
	}

	// v2.2 (2026-06-22): auto-generate session title after first successful request.
	// Fire-and-forget async call; never blocks the request path.
	if h.autoTitleGenerator != nil && reqLog.Success && reqLog.GwSessionID != nil && *reqLog.GwSessionID != "" {
		h.autoTitleGenerator.MaybeGenerateTitle(*reqLog.GwSessionID, tenantID)
	}
}

// recordFailedRequest writes a request_logs row for any non-success
// request exit (auth, rate-limit, budget, validation, candidate,
// executor, panic, …).  It is the safety net that guarantees
// every request that reaches any of the three handlers
// (chat completions, anthropic messages, openai responses) shows
// up in the admin request-logs UI, even when the request never
// makes it as far as the routing executor.
//
// Callers may set keyInfo to attach api_key_id / tenant_id; the
// rest of the row is filled in from the supplied error metadata.
// The caller is expected to call EmitRequestLog exactly once;
// recordFailedRequest never duplicates the entry.
// emitClientDisconnectProbe emits a dedicated probe record when the client
// disconnects (context.Canceled) or times out (context.DeadlineExceeded)
// before the request completed.
//
// 问题4 (2026-07-09): previously a client cancel/timeout only flipped a
// WAL flag (logCtx.SetClientTimeout) that no dashboard surface reads in
// real time. Operators could not see WHY a request vanished from the live
// stream. This synthesizes a first-class RequestLogEntry with a "probe-"
// prefixed request_id so it:
//  1. is persisted to request_logs_hot via EmitRequestLogInsert, and
//  2. is pushed to the live-stream swim lane via the onEmitted hook.
//
// The record carries the credential_id / provider_id selected for the
// request (when available), so the lane groups it under the right provider
// via the credential → provider reverse lookup (问题3 fix). When no
// credential was selected (routing/auth failure before candidate pick),
// credential_id is nil and the probe still records the client-side event.
//
// error_kind: "client_cancel" for context.Canceled, "probe_timeout" for
// context.DeadlineExceeded. failure_stage is always "probe".
func (h *ChatHandler) emitClientDisconnectProbe(originalRequestID string, r *http.Request, logCtx *RequestLogContext) {
	if h == nil || h.telemetryClient == nil || !h.telemetryClient.Enabled() {
		return
	}
	entry, ok := buildClientDisconnectProbeEntry(originalRequestID, r, logCtx)
	if !ok {
		return
	}
	// Test sink (mirrors the initial-log path at ~handler.go:3075).
	if h.requestLogHook != nil {
		h.requestLogHook(entry)
	}
	h.telemetryClient.EmitRequestLogInsert(entry)
}

// buildClientDisconnectProbeEntry constructs the probe RequestLogEntry from
// the request context error and the in-flight log context. Returns ok=false
// when there is no context error to report (nothing to probe). Pure / side
// effect free so it can be unit tested without a telemetry client or DB.
func buildClientDisconnectProbeEntry(originalRequestID string, r *http.Request, logCtx *RequestLogContext) (*telemetry.RequestLogEntry, bool) {
	if r == nil || r.Context() == nil {
		return nil, false
	}
	ctxErr := r.Context().Err()
	if ctxErr == nil {
		return nil, false
	}

	// Distinguish cancel vs timeout so the lane legend can tell them apart.
	errorKind := "client_cancel"
	if errors.Is(ctxErr, context.DeadlineExceeded) {
		errorKind = "probe_timeout"
	}

	// probe request_id: unique per event, "probe-" prefix so it is visually
	// distinct from real request rows in the lane. Includes the credential
	// id (when known) and a unix-nano timestamp.
	credSegment := "nocred"
	if logCtx != nil && logCtx.CredentialID != nil && *logCtx.CredentialID > 0 {
		credSegment = fmt.Sprintf("cred%d", *logCtx.CredentialID)
	}
	probeRequestID := fmt.Sprintf("probe-%s-%s-%d", errorKind, credSegment, time.Now().UnixNano())

	tenantID := ""
	clientModel := ""
	outboundModel := ""
	var providerID, credentialID *int
	if logCtx != nil {
		clientModel = logCtx.ClientModel
		outboundModel = logCtx.OutboundModel
		providerID = logCtx.ProviderID
		credentialID = logCtx.CredentialID
		if logCtx.KeyInfo != nil {
			tenantID = logCtx.KeyInfo.TenantID
		}
	}

	stage := "probe"
	return &telemetry.RequestLogEntry{
		RequestID:     probeRequestID,
		TenantID:      tenantID,
		ClientModel:   strPtr(clientModel),
		OutboundModel: strPtr(outboundModel),
		ProviderID:    providerID,
		CredentialID:  credentialID,
		Success:       false,
		RequestStatus: strPtr(telemetry.RequestStatusFailure),
		ErrorKind:     strPtr(errorKind),
		FailureStage:  &stage,
		// Link back to the original request via ClientRequestID so /request-logs
		// can correlate the probe row with the in_progress row it interrupted.
		ClientRequestID: strPtr(originalRequestID),
	}, true
}

// recordFailedRequestWithKey records a failure via the unified RequestLogContext pipeline.
func (h *ChatHandler) recordFailedRequestWithKey(requestID, clientModel, outboundModel string, providerID, credentialID *int, errCode, errMessage string, latencyMs int, requestBody []byte, keyInfo *authentication.KeyInfo, r *http.Request) {
	ctx := &RequestLogContext{
		handler:       h,
		RequestID:     requestID,
		StartTime:     time.Now().Add(-time.Duration(latencyMs) * time.Millisecond),
		Request:       r,
		KeyInfo:       keyInfo,
		Body:          requestBody,
		ClientModel:   clientModel,
		OutboundModel: outboundModel,
	}
	if r != nil {
		if session := session.SessionFromContext(r.Context()); session != nil {
			ctx.Session = session
		}
		// 2026-06-26: forward the client-supplied X-Request-Id (set by
		// the RequestIDMiddleware into X-Gw-Client-Request-Id) so the
		// failure row records it for debug / cross-system tracing.
		if gw := r.Header.Get("X-Gw-Client-Request-Id"); gw != "" {
			ctx.ClientRequestID = gw
		} else if gw := r.Header.Get("X-Client-Request-Id"); gw != "" {
			ctx.ClientRequestID = gw
		}
		ctx.refreshMeta()
	}
	ctx.EmitFailure(errCode, errMessage, providerID, credentialID)
}

// clientProfileFromKey returns the API key / application default client profile.
func clientProfileFromKey(keyInfo *authentication.KeyInfo) string {
	if keyInfo != nil && keyInfo.DefaultClientProfile != nil {
		return strings.TrimSpace(*keyInfo.DefaultClientProfile)
	}
	return ""
}

// failedRequestIdentity builds client_profile + identity_hash from request
// headers and key anchors without requiring a parsed request body.
func failedRequestIdentity(r *http.Request, keyInfo *authentication.KeyInfo) (clientProfile, identityHash string) {
	if r == nil {
		return "", ""
	}
	cp := clientProfileFromKey(keyInfo)
	clientID := identity.BuildIdentityFromRequest(r, tenant(keyInfo), appID(keyInfo), apiKeyIDPtr(keyInfo), cp)
	return clientID.Fingerprint.ClientProfile, clientID.ShortID()
}

func requestModeFromPath(path string) string {
	switch {
	case strings.Contains(path, "/messages"):
		return "anthropic"
	case strings.Contains(path, "/responses"):
		return "responses"
	default:
		return "chat"
	}
}

// isAnthropicMessagesPath returns true when the request targets the
// Anthropic Messages API (/v1/messages), so the session compressor
// knows which wire format to use when rebuilding the body.
func isAnthropicMessagesPath(path string) bool {
	return strings.Contains(path, "/messages")
}

// capturePartialBodyOnReadError keeps bytes already received when io.ReadAll
// fails mid-stream (timeout, client disconnect). model is usually near the
// start of JSON bodies, so partial data is enough for request_logs preview.
//
// 2026-06-20 audit fix v3: when the partial body has no "model" field
// (e.g. /v1/messages client sent messages first), set client_model to
// "<unknown>" so request_logs never shows a blank client_model alongside
// a non-empty body — same invariant as captureAttemptBody /
// ensureRequestBodyBuffered.
func capturePartialBodyOnReadError(body []byte, attemptRequestBody *[]byte, attemptClientModel *string) {
	if attemptRequestBody == nil || len(body) == 0 {
		return
	}
	*attemptRequestBody = body
	if attemptClientModel != nil && *attemptClientModel == "" {
		*attemptClientModel = extractModelFromBody(body)
		if *attemptClientModel == "" {
			*attemptClientModel = "<unknown>"
		}
	}
}

// mapGatewayErrorToDetail returns a machine-readable sub-classification for
// the given early-exit error code.  Gateway-side errors are prefixed with
// "gw_" so that request_log consumers can immediately distinguish these from
// upstream provider errors (which keep their classified kind, e.g. "rate_limit",
// "concurrent", "timeout").
//
// The mapping:
//
//	gateway RPM limit         → "gw_rpm_exceeded"
//	gateway concurrent        → "gw_concurrent_exceeded"
//	gateway TPM               → "gw_tpm_exceeded"
//	key throttled             → "gw_key_throttled"
//	budget exhausted          → "gw_budget_exhausted"
//	auto-route decider failed → "gw_auto_route_decider_failed"
//	upstream 429              → "rate_limit"        (unchanged)
//	upstream 429/503          → "concurrent"        (unchanged)
//	other early-exits         → errCode passthrough
func mapGatewayErrorToDetail(errCode string) string {
	switch errCode {
	case "rate_limit_exceeded":
		return "gw_rpm_exceeded"
	case "concurrent_limit_exceeded":
		return "gw_concurrent_exceeded"
	case "tpm_limit_exceeded":
		return "gw_tpm_exceeded"
	case "key_throttled":
		return "gw_key_throttled"
	case "budget_exhausted":
		return "gw_budget_exhausted"
	case "missing_key", "invalid_key":
		return "gw_" + errCode
	case "auth_unavailable":
		return "gw_auth_unavailable"
	case "method_not_allowed":
		return "gw_method_not_allowed"
	case "executor_unavailable":
		return "gw_executor_unavailable"
	case "no_candidate":
		return "gw_no_candidate"
	case "body_too_large", "body_read_error", "json_parse_error":
		return "gw_" + errCode
	case "missing_model", "missing_max_tokens":
		return "gw_" + errCode
	case "conversion_error":
		return "gw_conversion_error"
	case "session_forbidden":
		return "gw_session_forbidden"
	case "internal_panic":
		return "gw_internal_panic"
	case "chat_to_anthropic_conversion_error":
		return "gw_chat_to_anthropic_conversion_error"
	case "auto_route_decider_failed":
		return "gw_auto_route_decider_failed"
	default:
		return errCode
	}
}

// classifyFailureStage returns where in the request lifecycle the
// failure happened so the request_log UI can group and filter.  Two
// possible values:
//
//	"gateway"  — the request never reached an upstream provider
//	             (auth/rate-limit/budget/validation/panics/...)
//	"upstream" — the request was dispatched to a provider and failed
//	             during or after the provider call
//	             (provider_error, model_not_found, stream_error, ...)
//
// Any error code that is NOT in the gateway early-exit list is
// assumed to be upstream.  This mirrors the rule used in
// mapGatewayErrorToDetail: the codes that get a "gw_" prefix are
// gateway; everything else is upstream.
func classifyFailureStage(errCode string) string {
	switch errCode {
	case "rate_limit_exceeded",
		"concurrent_limit_exceeded",
		"tpm_limit_exceeded",
		"key_throttled",
		"budget_exhausted",
		"insufficient_credits",
		"missing_key",
		"invalid_key",
		"auth_unavailable",
		"method_not_allowed",
		"executor_unavailable",
		"no_candidate",
		"body_too_large",
		"body_read_error",
		"json_parse_error",
		"missing_model",
		"missing_max_tokens",
		"conversion_error",
		"session_forbidden",
		"internal_panic",
		"chat_to_anthropic_conversion_error",
		"auto_route_decider_failed":
		return "gateway"
	default:
		return "upstream"
	}
}

// recordInitialRequestLog writes the base request metadata as soon as routing
// is resolved and before the upstream call starts.  Streaming requests then
// appear immediately in /request-logs; completion paths update tokens, bodies,
// and final success/error state via EmitRequestLogUpdate.
func (h *ChatHandler) recordInitialRequestLog(
	requestID, clientModel, outboundModel, endUser, requestMode string,
	keyInfo *authentication.KeyInfo,
	clientProfile, identityHash string,
	providerID, credentialID, canonicalID *int,
	requestBody []byte,
	txResult *transformation.TransformResult,
	egressProtocol string,
	isStream bool,
	gwSessionID, gwTaskID string,
	autoCtx *RequestLogContext,
) {
	if h.telemetryClient == nil || !h.telemetryClient.Enabled() {
		return
	}
	if clientModel == "" && len(requestBody) > 0 {
		clientModel = extractModelFromBody(requestBody)
	}
	if outboundModel == "" && clientModel != "" {
		outboundModel = clientModel
	}
	var requestBodyText *string
	if len(requestBody) > 0 {
		v := string(requestBody)
		requestBodyText = &v
	}
	tenantID := "default"
	var apiKeyID *int
	var applicationID *int
	keyPrefix, keyOwner, appCode := "", "", ""
	if keyInfo != nil {
		tenantID = keyInfo.TenantID
		kid := keyInfo.ID
		apiKeyID = &kid
		applicationID = appID(keyInfo)
		keyPrefix, keyOwner, appCode = keyMetaFromKeyInfo(keyInfo)
	}
	var requestPreviewPtr *string
	if preview := requestPreview(requestBody); preview != "" {
		requestPreviewPtr = strPtr(preview)
	}
	var transformSummaryPtr *string
	if summary := transformSummary(txResult, outboundModel); summary != "" {
		transformSummaryPtr = strPtr(summary)
	}
	var transformRuleID *string
	if txResult != nil && txResult.MatchedRule != "" {
		transformRuleID = strPtr(txResult.MatchedRule)
	}
	streamInterrupted := false
	var clientRequestIDPtr *string
	if autoCtx != nil && autoCtx.ClientRequestID != "" {
		v := autoCtx.ClientRequestID
		clientRequestIDPtr = &v
	}
	reqLog := &telemetry.RequestLogEntry{
		RequestID:         requestID,
		TenantID:          tenantID,
		ApplicationID:     applicationID,
		APIKeyID:          apiKeyID,
		APIKeyPrefix:      strPtr(keyPrefix),
		APIKeyOwnerUser:   strPtr(keyOwner),
		ApplicationCode:   strPtr(appCode),
		EndUserID:         strPtr(endUser),
		ClientModel:       strPtr(clientModel),
		OutboundModel:     strPtr(outboundModel),
		ProviderID:        providerID,
		CredentialID:      credentialID,
		CanonicalID:       canonicalID,
		ClientProfile:     strPtr(clientProfile),
		IdentityHash:      strPtr(identityHash),
		RequestMode:       strPtr(requestMode),
		GwSessionID:       strPtr(gwSessionID),
		GwTaskID:          strPtr(gwTaskID),
		Success:           false,
		RequestStatus:     strPtr(telemetry.RequestStatusInProgress),
		RequestBody:       requestBodyText,
		RequestPreview:    requestPreviewPtr,
		TransformSummary:  transformSummaryPtr,
		TransformRuleID:   transformRuleID,
		EgressProtocol:    strPtr(egressProtocol),
		StreamInterrupted: &streamInterrupted,
		// 2026-06-26: preserve client-supplied X-Request-Id for debug
		// (request_id itself is server-generated; see middleware/requestid_mw.go).
		ClientRequestID: clientRequestIDPtr,
	}
	if isStream {
		zero := 0
		reqLog.StreamChunkCount = &zero
		// 2026-07-05 P0 fix: stream_chunks_sent is NOT NULL (migration 320).
		// Initialize to 0 for in-flight requests to prevent INSERT violations.
		reqLog.StreamChunksSent = &zero
	}
	applyAutoRouteFields(reqLog, autoCtx)
	if h.requestLogHook != nil {
		h.requestLogHook(reqLog)
	}
	applyKeyInfoToRequestLog(reqLog, keyInfo)
	h.telemetryClient.EmitRequestLogInsert(reqLog)
}

func extractModelFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var parsed struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		return strings.TrimSpace(parsed.Model)
	}
	return extractModelFieldLoose(body)
}

// extractModelFieldLoose reads "model":"..." from truncated or invalid JSON.
func extractModelFieldLoose(body []byte) string {
	pattern := []byte(`"model"`)
	idx := bytes.Index(body, pattern)
	if idx < 0 {
		return ""
	}
	after := body[idx+len(pattern):]
	colonIdx := bytes.IndexByte(after, ':')
	if colonIdx < 0 {
		return ""
	}
	rest := bytes.TrimLeft(after[colonIdx+1:], " \t\n\r")
	if len(rest) == 0 || rest[0] != '"' {
		return ""
	}
	endIdx := bytes.IndexByte(rest[1:], '"')
	if endIdx < 0 {
		return ""
	}
	return strings.TrimSpace(string(rest[1 : endIdx+1]))
}

func (h *ChatHandler) emitFailedDecisionLog(requestID, clientModel string, keyInfo *authentication.KeyInfo, clientID identity.ClientIdentity, candidatesTried int, modelResolution *resolve.Resolution, txResult *transformation.TransformResult, errCode string, failTrace *executors.Trace, latencyMs int) {
	if h.telemetryClient == nil || !h.telemetryClient.Enabled() {
		return
	}
	var apiKeyID *int
	var tenantID = "default"
	if keyInfo != nil {
		apiKeyID = &keyInfo.ID
		tenantID = keyInfo.TenantID
	}
	var canonical string
	if modelResolution != nil && modelResolution.CanonicalName != nil {
		canonical = *modelResolution.CanonicalName
	}
	dl := &telemetry.DecisionLogEntry{
		RequestID:         requestID,
		TenantID:          tenantID,
		APIKeyID:          apiKeyID,
		Model:             canonicalOrClient(canonical, clientModel),
		CandidatesTried:   candidatesTried,
		LatencyMs:         latencyMs,
		Success:           false,
		ErrorClass:        strPtr(errCode),
		FailureDetailCode: strPtr(errCode),
		ClientModel:       strPtr(clientModel),
		IdentityHash:      strPtr(clientID.IdentityHash),
	}
	if failTrace != nil {
		traceJSON, _ := json.Marshal(failTrace)
		dl.DecisionTrace = traceJSON
	}
	if modelResolution != nil {
		dl.ResolutionPath = strPtr(modelResolution.ResolutionPath)
		if modelResolution.CanonicalName != nil {
			dl.CanonicalModel = strPtr(*modelResolution.CanonicalName)
		}
		if len(modelResolution.RawModels) > 0 {
			dl.ResolutionRawModels = modelResolution.RawModels
		}
	}
	if txResult != nil {
		dl.OutboundModel = strPtr(txResult.OutboundModel)
		if txResult.MatchedRule != "" {
			dl.TransformRuleID = strPtr(txResult.MatchedRule)
		}
	}
	h.telemetryClient.EmitDecisionLog(dl)
}

func (h *ChatHandler) serveFallback(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{
		"error": map[string]string{
			"message": "Routing executor not available. Database connection required.",
			"type":    "server_error",
			"code":    "executor_unavailable",
		},
	})
}

// ReplaceModelInRequestBody replaces the "model" field in a JSON body.
func ReplaceModelInRequestBody(body []byte, newModel string) []byte {
	quotedOld := bytes.Contains(body, []byte(`"model"`))
	if !quotedOld {
		return body
	}
	pattern := []byte(`"model"`)
	idx := bytes.Index(body, pattern)
	if idx < 0 {
		return body
	}
	after := body[idx+len(pattern):]
	colonIdx := bytes.IndexByte(after, ':')
	if colonIdx < 0 {
		return body
	}
	rest := after[colonIdx+1:]
	rest = bytes.TrimLeft(rest, " \t\n\r")
	if len(rest) == 0 || rest[0] != '"' {
		return body
	}
	endIdx := bytes.IndexByte(rest[1:], '"')
	if endIdx < 0 {
		return body
	}
	oldValue := rest[1 : endIdx+1]
	if string(oldValue) == newModel {
		return body
	}
	var buf bytes.Buffer
	prefix := body[:idx+len(pattern)+colonIdx+1]
	suffix := rest[endIdx+2:]
	buf.Write(prefix)
	buf.WriteString(" \"")
	buf.WriteString(newModel)
	buf.WriteByte('"')
	buf.Write(suffix)
	return buf.Bytes()
}

// ReplaceModelInResponseBody replaces whatever model is in the response with clientModel.
func ReplaceModelInResponseBody(body []byte, clientModel string) []byte {
	pattern := []byte(`"model"`)
	idx := bytes.Index(body, pattern)
	if idx < 0 {
		return body
	}
	after := body[idx+len(pattern):]
	colonIdx := bytes.IndexByte(after, ':')
	if colonIdx < 0 {
		return body
	}
	rest := after[colonIdx+1:]
	rest = bytes.TrimLeft(rest, " \t\n\r")
	if len(rest) < 2 || rest[0] != '"' {
		return body
	}
	endIdx := bytes.IndexByte(rest[1:], '"')
	if endIdx < 0 {
		return body
	}
	oldValue := rest[1 : endIdx+1]
	if string(oldValue) == clientModel {
		return body
	}
	var buf bytes.Buffer
	prefix := body[:idx+len(pattern)+colonIdx+1]
	suffix := rest[endIdx+2:]
	buf.Write(prefix)
	buf.WriteString(`"` + clientModel + `"`)
	buf.Write(suffix)
	return buf.Bytes()
}

func requestHasTools(body []byte) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return false
	}
	toolsRaw, ok := obj["tools"]
	if !ok || len(toolsRaw) == 0 || string(toolsRaw) == "null" {
		return false
	}
	var tools []any
	return json.Unmarshal(toolsRaw, &tools) == nil && len(tools) > 0
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	//nolint:errcheck // HTTP write error non-recoverable
	json.NewEncoder(w).Encode(data)
}

//-----------------------------------------------------------------------------
// Health handler
//-----------------------------------------------------------------------------

// ResourceStatus represents the connection status of a resource (DB, Redis, etc).
type ResourceStatus struct {
	Connected bool   `json:"connected"`
	Latency   string `json:"latency,omitempty"`
	Error     string `json:"error,omitempty"`
}

// HealthResponse represents the health check response.
type HealthResponse struct {
	Status      string          `json:"status"`
	Version     string          `json:"version"`
	Database    *ResourceStatus `json:"database,omitempty"`
	Redis       *ResourceStatus `json:"redis,omitempty"`
	Circuit     any             `json:"circuit,omitempty"`
	Concurrency any             `json:"concurrency,omitempty"`
	Proxy       map[string]any  `json:"proxy,omitempty"`
}

// HealthHandler returns health information including circuit breaker and limiter stats.
type HealthHandler struct {
	circuit *credential.Manager
	limiter *credential.Limiter
	proxy   *upstreampkg.ProxyResolver
	db      dbConnector
	redis   redisConnector
}

// dbConnector interface for database ping check
type dbConnector interface {
	Ping(ctx context.Context) error
}

// redisConnector interface for Redis ping check
type redisConnector interface {
	Ping(ctx context.Context) error
}

// NewHealthHandler creates a new health handler.
func NewHealthHandler(cm *credential.Manager, l *credential.Limiter, proxy *upstreampkg.ProxyResolver, db dbConnector, redis redisConnector) *HealthHandler {
	return &HealthHandler{circuit: cm, limiter: l, proxy: proxy, db: db, redis: redis}
}

// SetRedis updates the Redis connection for health checks (2026-07-08).
// Called after Redis is initialized in main.go.
func (h *HealthHandler) SetRedis(redis redisConnector) {
	h.redis = redis
}

func (h *HealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	resp := HealthResponse{
		Status:  "ok",
		Version: resolveGatewayVersion(),
	}

	// NET-007 fix: ?full=true 必须 admin token（完整的 LLM_GATEWAY_ADMIN_API_KEY
	// 校验由外层 AdminTokenMiddleware 完成；这里只拒绝"完全无 token"的情形）。
	//
	// ?full=true 会暴露 circuit.Stats() / limiter.Stats() / proxy.Status()
	// （含 18 个内网域名、credential 熔断状态等敏感信息），不应匿名访问。
	full := r.URL.Query().Get("full") == "true"
	if full {
		const expectedHeader = "Bearer "
		auth := r.Header.Get("Authorization")
		if len(auth) <= len(expectedHeader) || auth[:len(expectedHeader)] != expectedHeader {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("WWW-Authenticate", `Bearer realm="admin"`)
			w.WriteHeader(http.StatusUnauthorized)
			//nolint:errcheck
			w.Write([]byte(`{"error":"admin token required for full healthz"}`))
			return
		}
		resp.Circuit = h.circuit.Stats()
		resp.Concurrency = h.limiter.Stats()

		// Check database connection (2026-07-08: add resource status)
		if h.db != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			start := time.Now()
			dbErr := h.db.Ping(ctx)
			latency := time.Since(start)

			resp.Database = &ResourceStatus{
				Connected: dbErr == nil,
				Latency:   latency.String(),
			}
			if dbErr != nil {
				resp.Database.Error = dbErr.Error()
			}
		}

		// Check Redis connection (2026-07-08: add resource status)
		if h.redis != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			start := time.Now()
			redisErr := h.redis.Ping(ctx)
			latency := time.Since(start)

			resp.Redis = &ResourceStatus{
				Connected: redisErr == nil,
				Latency:   latency.String(),
			}
			if redisErr != nil {
				resp.Redis.Error = redisErr.Error()
			}
		}
	}

	// NET-007 fix: proxy 字段也属于敏感信息（暴露 internal.example.com 等内网
	// 域名）。仅当 full=true 时才返回（与 circuit/concurrency 同样需要 admin token）。
	if full && h.proxy != nil {
		if status := h.proxy.Status(); status != nil {
			resp.Proxy = status
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	//nolint:errcheck // HTTP write error non-recoverable
	json.NewEncoder(w).Encode(resp)
}

func extractBearerToken(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); auth != "" {
		if strings.HasPrefix(auth, "Bearer ") {
			return strings.TrimPrefix(auth, "Bearer ")
		}
		if strings.HasPrefix(auth, "bearer ") {
			return strings.TrimPrefix(auth, "bearer ")
		}
	}
	if key := r.Header.Get("x-api-key"); key != "" {
		return key
	}
	return ""
}

func resolveEndUser(bodyUser string, r *http.Request) string {
	if bodyUser != "" {
		return bodyUser
	}
	if header := r.Header.Get("X-End-User-Id"); header != "" {
		return strings.TrimSpace(header)
	}
	return "anonymous"
}

func extractTokensFromResponseBody(body []byte) (promptTokens, completionTokens, cacheRead, cacheWrite int) {
	if len(body) == 0 {
		return 0, 0, 0, 0
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return 0, 0, 0, 0
	}
	usageRaw, ok := data["usage"]
	if !ok {
		// Fallback: some providers (e.g. minimax) may return usage at top level
		usageRaw = data
	}
	usage, ok := usageRaw.(map[string]any)
	if !ok {
		return 0, 0, 0, 0
	}
	// prompt_tokens / input_tokens (Anthropic native)
	if v, ok := usage["prompt_tokens"].(float64); ok {
		promptTokens = int(v)
	} else if v, ok := usage["input_tokens"].(float64); ok {
		promptTokens = int(v)
	}
	// completion_tokens / output_tokens (Anthropic native)
	if v, ok := usage["completion_tokens"].(float64); ok {
		completionTokens = int(v)
	} else if v, ok := usage["output_tokens"].(float64); ok {
		completionTokens = int(v)
	}
	// cache_read: try 4 variants
	if v, ok := usage["cache_read_input_tokens"].(float64); ok {
		cacheRead = int(v)
	} else if v, ok := usage["cache_read_tokens"].(float64); ok {
		cacheRead = int(v)
	} else if pt := usage["prompt_tokens_details"]; pt != nil {
		if details, ok := pt.(map[string]any); ok {
			if v, ok := details["cached_tokens"].(float64); ok && cacheRead == 0 {
				cacheRead = int(v)
			}
		}
	} else if pt := usage["input_token_details"]; pt != nil {
		if details, ok := pt.(map[string]any); ok {
			if v, ok := details["cache_read"].(float64); ok && cacheRead == 0 {
				cacheRead = int(v)
			}
		}
	}
	// cache_write: try 3 variants
	if v, ok := usage["cache_creation_input_tokens"].(float64); ok {
		cacheWrite = int(v)
	} else if v, ok := usage["cache_write_tokens"].(float64); ok {
		cacheWrite = int(v)
	} else if pt := usage["input_token_details"]; pt != nil {
		if details, ok := pt.(map[string]any); ok {
			if v, ok := details["cache_creation"].(float64); ok && cacheWrite == 0 {
				cacheWrite = int(v)
			}
		}
	}
	// total_tokens fallback: if we have total but missing prompt/completion, infer them
	if promptTokens == 0 || completionTokens == 0 {
		if total, ok := usage["total_tokens"].(float64); ok && int(total) > 0 {
			totalInt := int(total)
			if promptTokens == 0 && completionTokens > 0 && totalInt > completionTokens {
				promptTokens = totalInt - completionTokens
			} else if completionTokens == 0 && promptTokens > 0 && totalInt > promptTokens {
				completionTokens = totalInt - promptTokens
			}
		}
	}
	return
}

// injectUsageIntoResponseBody augments a response body JSON with usage data extracted
// from the stream capture. This ensures request_logs.response_body always contains a
// `usage` block even when the upstream's last SSE chunk does not include one.
func injectUsageIntoResponseBody(body []byte, pt, ct, crt, cwt int) []byte {
	if pt <= 0 && ct <= 0 {
		return body
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return body
	}
	usageRaw, ok := data["usage"]
	if !ok {
		usageRaw = map[string]any{}
	}
	usage, ok := usageRaw.(map[string]any)
	if !ok {
		usage = map[string]any{}
	}
	if pt > 0 {
		usage["prompt_tokens"] = pt
	}
	if ct > 0 {
		usage["completion_tokens"] = ct
	}
	if crt > 0 {
		usage["cache_read_tokens"] = crt
	}
	if cwt > 0 {
		usage["cache_write_tokens"] = cwt
	}
	if pt > 0 && ct > 0 {
		usage["total_tokens"] = pt + ct
	}
	data["usage"] = usage
	result, err := json.Marshal(data)
	if err != nil {
		return body
	}
	return result
}

func intPtr(v int) *int { return &v }
func floatPtrFromInt(p *int) *float64 {
	if p == nil {
		return nil
	}
	v := float64(*p)
	return &v
}
func strPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func boolPtr(v bool) *bool {
	return &v
}

// classifyStreamInterruption determines whether a stream interruption captured
// in the summary map represents a real gateway error that should mark the
// request log as failed. It returns (isError, detailCode).
//
// Benign cases that do NOT mark the request as failed:
//   - "eof_without_done" with chunk_count > 0: upstream closed without [DONE]
//     but content was already delivered (e.g. MiniMax). The gateway synthesises
//     [DONE] for the client. This mirrors executor_chat.go's isBenignEOF.
//   - "client_cancel" / "client_disconnected": the client went away; not a
//     gateway or upstream error.
func classifyStreamInterruption(m map[string]any) (isError bool, detailCode string) {
	detailCode, _ = m["failure_detail_code"].(string)
	chunkCount, _ := m["stream_chunk_count"].(int)

	isBenignEOF := detailCode == "eof_without_done" && chunkCount > 0
	isClientCancel := detailCode == "client_cancel" || detailCode == "client_disconnected"

	if isBenignEOF || isClientCancel {
		return false, detailCode
	}
	return true, detailCode
}

// canonicalOrClient prefers the canonical name (standardised model key from the
// routing table). When the resolution did not yield a canonical entry (direct
// passthrough), it falls back to whatever the client supplied.
func canonicalOrClient(canonical, client string) string {
	if canonical != "" {
		return canonical
	}
	return client
}

// generateRequestID returns a stable per-request UUID used both as the
// X-Request-Id response header and as the request_logs row's request_id
// column.  Always non-empty so the safety-net logger can find a row.
func generateRequestID() string {
	return uuid.NewString()
}

func writeErrorJSON(w http.ResponseWriter, status int, requestID, msg, errType, code string) {
	writeErrorJSONWithDebug(w, status, requestID, msg, errType, code, nil)
}

// writeErrorJSONCtx emits an error response whose message is translated via
// i18n for the locale on ctx. messageKey is both the translation key and the
// machine-readable "code" field value (they are kept aligned by convention —
// see i18n messages.go), so callers pass a single token.
//
// templateData carries interpolation values for messages with placeholders
// (e.g. {"Model": "gpt-4o"} for MsgNoCandidate); pass nil when none are needed.
//
// Use this instead of writeErrorJSON for any error whose code has a translation
// entry. Sites whose code lacks a translation yet keep calling writeErrorJSON
// directly (their inline English is the de-facto fallback).
func writeErrorJSONCtx(ctx context.Context, w http.ResponseWriter, status int, requestID, errType, messageKey string, templateData map[string]any) {
	msg := i18n.T(ctx, messageKey, templateData)
	writeErrorJSON(w, status, requestID, msg, errType, messageKey)
}

func writeErrorJSONWithDebug(w http.ResponseWriter, status int, requestID, msg, errType, code string, debug map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	if requestID != "" {
		w.Header().Set("X-Request-Id", requestID)
	}
	w.WriteHeader(status)
	errObj := map[string]any{
		"message":    msg,
		"type":       errType,
		"code":       code,
		"request_id": requestID,
	}
	if debug != nil {
		errObj["gateway_debug"] = debug
	}
	//nolint:errcheck // HTTP write error non-recoverable
	json.NewEncoder(w).Encode(map[string]any{
		"error": errObj,
	})
}

// writeErrorJSONWithKind (Step 6, 2026-06-18) is like
// writeErrorJSONWithDebug but additionally surfaces a "kind" field in
// the error object. The kind is the SSoT for the underlying failure
// cause (rate_limit, concurrent, model_not_found, ...). It is always
// emitted even when kind == code, so clients that learn the new shape
// never have to null-check.
//
// Backward compat: the legacy "code" field is unchanged ("model_not_found"
// is still surfaced there even when the real kind is "rate_limit"). New
// clients should read "kind"; old clients keep working.
func writeErrorJSONWithKind(w http.ResponseWriter, status int, requestID, msg, errType, code, kind string, debug map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	if requestID != "" {
		w.Header().Set("X-Request-Id", requestID)
	}
	w.WriteHeader(status)
	errObj := map[string]any{
		"message":    msg,
		"type":       errType,
		"code":       code,
		"request_id": requestID,
		"kind":       kind,
	}
	if debug != nil {
		errObj["gateway_debug"] = debug
	}
	//nolint:errcheck // HTTP write error non-recoverable
	json.NewEncoder(w).Encode(map[string]any{
		"error": errObj,
	})
}

// mapExecuteErrorToKind (Step 6, 2026-06-18) maps an exhausted
// ExecuteError to the client-visible "kind" field. The logic prefers
// the executor's recorded LastKind when set, and falls back to a small
// lookup table for the cases where LastKind is empty (e.g. no
// candidates returned at all from the router).
//
// Returns "" when no kind can be determined (caller should omit the
// header and the field).
func mapExecuteErrorToKind(err *executors.ExecuteError) string {
	if err == nil {
		return ""
	}
	if err.LastKind != "" {
		return string(err.LastKind)
	}
	if err.Tried == 0 {
		return "no_candidates"
	}
	return "unknown"
}

// errorKindOrFallback (2026-06-20) returns the real underlying error
// kind for request_logs.error_kind. Falls back to "model_not_found"
// when the kind is empty or "unknown" so we never write a misleading
// empty/garbage value to the database. The HTTP error.code is handled
// separately (see serveWithExecutor's Exhausted branch) and stays
// "model_not_found" for backward compatibility.
func errorKindOrFallback(kind string) string {
	if kind == "" || kind == "unknown" {
		return "model_not_found"
	}
	return kind
}

// extractUpstreamError (2026-06-23 P0) walks an error chain looking for
// an *upstream.Error. Used by the relay handler to surface the vendor's
// actual response body (and HTTP status) into request_logs so that
// transient/5xx failures become diagnostically useful. Walks Unwrap
// because the routing layer wraps upstream errors with fmt.Errorf("%w",
// ...) for retry/exhaustion tracking.
//
// Returns (error, true) on the first *upstream.Error found, else
// (nil, false) — callers MUST handle the false case gracefully.
func extractUpstreamError(err error) (*upstreampkg.Error, bool) {
	if err == nil {
		return nil, false
	}
	for cur := err; cur != nil; cur = errors.Unwrap(cur) {
		if ue, ok := cur.(*upstreampkg.Error); ok {
			return ue, true
		}
	}
	return nil, false
}

// extractUpstreamReason returns the human-readable upstream rejection
// message from an executor error. It first tries to parse the JSON body
// of a wrapped *upstreampkg.Error (looking for error.message,
// error.error.message, or message), then falls back to the raw error
// string. The result is capped at 200 characters.
func extractUpstreamReason(err error) string {
	if ue, ok := extractUpstreamError(err); ok && len(ue.Body) > 0 {
		var parsed struct {
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal(ue.Body, &parsed) == nil {
			if parsed.Error != nil && parsed.Error.Message != "" {
				msg := parsed.Error.Message
				if len(msg) > 200 {
					msg = msg[:200]
				}
				return msg
			}
			if parsed.Message != "" {
				msg := parsed.Message
				if len(msg) > 200 {
					msg = msg[:200]
				}
				return msg
			}
		}
		// JSON parse failed — return raw body preview.
		raw := string(ue.Body)
		if len(raw) > 200 {
			raw = raw[:200]
		}
		return raw
	}
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}

// captureAttemptBody reads the request body (capped at 1MB) into bodyOut
// and extracts the client_model from the JSON.  It does NOT close the
// body — the caller (serveWithExecutor) owns that responsibility via
// its own defer.
//
// 2026-06-20 audit fix v2: When the body is captured but has no
// "model" field (e.g. /v1/messages client omitted model, or body is
// `{}`), set client_model to "<unknown>" so request_logs never shows
// a blank client_model alongside a non-empty request_body. Without
// this, the operator cannot tell whether the body was empty OR the
// client simply forgot the model field — both look like an empty
// client_model. Setting "<unknown>" makes it explicit that the body
// was received but model extraction failed.
func captureAttemptBody(r *http.Request, bodyOut *[]byte, modelOut *string) {
	if bodyOut == nil || r == nil || r.Body == nil {
		return
	}
	if len(*bodyOut) > 0 {
		return
	}
	const maxBody = 1 << 20 // 1MB
	buf, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil || len(buf) == 0 {
		return
	}
	*bodyOut = buf
	if modelOut == nil {
		return
	}
	// Only attempt model extraction if modelOut is still empty
	if *modelOut != "" {
		return
	}
	var probe struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(buf, &probe)
	if probe.Model != "" {
		*modelOut = probe.Model
		return
	}
	// Body captured but no model field found — record as <unknown>
	// so request_logs.client_model is never blank when body is set.
	// This distinguishes "empty body" from "body present but no
	// model field" — both look the same otherwise.
	*modelOut = "<unknown>"
}

// emitTuningSignal computes the implicit feedback signal for an auto-route
// request and enqueues it for async batched write to tuning_signals.
//
// Only called for auto-route requests (model="auto"). All scoring is
// done in-process (no DB lookup on the hot path) to keep latency <1ms.
// The DB insert happens asynchronously in the tuning writer goroutine.
func (h *ChatHandler) emitTuningSignal(reqLog *telemetry.RequestLogEntry, success bool, latencyMs int) {
	if h == nil || h.telemetryClient == nil {
		return
	}

	classifier := "heuristic"
	if reqLog.AutoDecision != nil {
		var d struct {
			Classifier string `json:"classifier"`
		}
		if err := json.Unmarshal([]byte(*reqLog.AutoDecision), &d); err == nil && d.Classifier != "" {
			classifier = d.Classifier
		}
	}

	taskType := ""
	if reqLog.TaskType != nil {
		taskType = *reqLog.TaskType
	}
	chosenModel := ""
	if reqLog.OutboundModel != nil {
		chosenModel = *reqLog.OutboundModel
	}
	confidence := 0.0
	if reqLog.AutoConfidence != nil {
		confidence = *reqLog.AutoConfidence
	}

	latencyScore := 0.5
	if latencyMs > 0 && latencyMs < 30000 {
		ratio := float64(latencyMs) / 30000.0
		if ratio > 1 {
			ratio = 1
		}
		latencyScore = 1.0 - ratio
	}

	costScore := 0.5
	costUSD := 0.0
	if reqLog.CostUSD != nil {
		costUSD = *reqLog.CostUSD
	}
	if costUSD > 0 {
		ratio := costUSD / 0.01
		if ratio > 1 {
			ratio = 1
		}
		costScore = 1.0 - ratio
	}

	drift := false
	quality := telemetry.ComputeTuningSignalQuality(success, latencyMs, 0, costUSD, 0, drift)

	sessionID := ""
	if reqLog.GwSessionID != nil {
		sessionID = *reqLog.GwSessionID
	}

	var payload []byte
	if reqLog.AutoDecision != nil {
		payload = []byte(*reqLog.AutoDecision)
	}

	promptTokens, completionTokens := 0, 0
	if reqLog.PromptTokens != nil {
		promptTokens = *reqLog.PromptTokens
	}
	if reqLog.CompletionTokens != nil {
		completionTokens = *reqLog.CompletionTokens
	}

	sig := telemetry.TuningSignal{
		RequestID:        reqLog.RequestID,
		SessionID:        sessionID,
		TaskType:         taskType,
		Classifier:       classifier,
		Confidence:       confidence,
		ChosenModel:      chosenModel,
		SuccessScore:     boolToFloat(success),
		LatencyScore:     latencyScore,
		CostScore:        costScore,
		DriftFlag:        drift,
		QualityScore:     quality,
		LatencyMs:        latencyMs,
		CostUSD:          costUSD,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		SignalPayload:    payload,
		Strategy:         string(autoroute.AssignStrategy(reqLog.RequestID)),
	}
	telemetry.WriteTuningSignal(sig)
}

func boolToFloat(b bool) float64 {
	if b {
		return 1.0
	}
	return 0.0
}

// streamChunksSentFromLogCtx returns the count of stream chunks sent for
// this request, defaulting to 0 when logCtx is nil (non-streaming path or
// pre-init failure). Added 2026-07-01 to satisfy the NOT NULL constraint
// on request_logs.stream_chunks_sent (migration 320) — without this
// helper, BuildSuccessEntry used to leave reqLog.StreamChunksSent nil,
// causing INSERT/UPDATE to fail with SQLSTATE 23502 and stopping all
// new rows from being written on 184.
func streamChunksSentFromLogCtx(c *RequestLogContext) int {
	if c == nil {
		return 0
	}
	if c.StreamChunksSent < 0 {
		return 0
	}
	return c.StreamChunksSent
}

// StreamChunksSentFromLogCtxForTest is the test-only exported alias of
// streamChunksSentFromLogCtx. The unexported version is kept because the
// production call site is package-internal; tests outside the package
// would otherwise need access to RequestLogContext internals.
func StreamChunksSentFromLogCtxForTest(c *RequestLogContext) int {
	return streamChunksSentFromLogCtx(c)
}

// streamChunkErrorsFromLogCtx mirrors streamChunksSentFromLogCtx for the
// stream_chunk_errors column. Same NOT NULL rationale applies.
func streamChunkErrorsFromLogCtx(c *RequestLogContext) int {
	if c == nil {
		return 0
	}
	if c.StreamChunkErrors < 0 {
		return 0
	}
	return c.StreamChunkErrors
}

// StreamChunkErrorsFromLogCtxForTest is the test-only exported alias of
// streamChunkErrorsFromLogCtx, mirroring StreamChunksSentFromLogCtxForTest.
func StreamChunkErrorsFromLogCtxForTest(c *RequestLogContext) int {
	return streamChunkErrorsFromLogCtx(c)
}

// resolveGatewayVersion reads the build version from /opt/llm-gateway-go/VERSION
// (written at image build time by the Dockerfile's RUN echo … > VERSION step).
// Falls back to GIT_SHA env var, then to "unknown" if neither is available.
func resolveGatewayVersion() string {
	candidates := []string{
		"/opt/llm-gateway-go/VERSION",
		"/.VERSION",
		"VERSION",
	}
	for _, path := range candidates {
		if raw, err := os.ReadFile(path); err == nil {
			if v := strings.TrimSpace(string(raw)); v != "" {
				return v
			}
		}
	}
	if sha := strings.TrimSpace(os.Getenv("GIT_SHA")); sha != "" {
		return "1.0.0-" + sha + "-" + time.Now().UTC().Format("2006-01-02")
	}
	return "0.2.0-unknown"
}

// detectEmptyStreamResponse checks if a streaming response is effectively empty.
// Returns true if all of the following are true:
//   - Very few chunks (<= 3)
//   - Zero completion tokens
//   - No response preview content
//   - No upstream finish_reason (normal finish would have "length" or "stop")
//
// This pattern indicates the upstream returned no actual content despite sending [DONE].
// Seen with Provider 18 (NVIDIA NIM) on large inputs (160k+ tokens).
func detectEmptyStreamResponse(m map[string]any, reqLog *telemetry.RequestLogEntry) bool {
	// Check 1: Few chunks (<= 3, tightened threshold)
	chunkCount, ok := m["stream_chunk_count"].(int)
	if !ok || chunkCount > 3 {
		return false // More than 3 chunks likely has content
	}
	if chunkCount > 3 {
		return false // More than 3 chunks likely has content
	}

	// Check 2: Zero completion tokens
	hasTokens := reqLog.CompletionTokens != nil && *reqLog.CompletionTokens > 0
	if hasTokens {
		return false // Has tokens means not empty
	}

	// Check 3: No content preview (check both reqLog and capture)
	hasPreview := reqLog.ResponsePreview != nil && *reqLog.ResponsePreview != ""
	if hasPreview {
		return false // Has content means not empty
	}

	// Check stream_text_content from capture as backup
	if v, ok := m["stream_text_content"].(string); ok && strings.TrimSpace(v) != "" {
		return false // Has text content means not empty
	}

	// Check 4: No upstream finish_reason
	// Normal successful completion should have "stop" or "length"
	hasFinishReason := reqLog.UpstreamFinishReason != nil && *reqLog.UpstreamFinishReason != ""
	if hasFinishReason {
		return false // Normal finish means not empty
	}

	// All empty indicators present - this is truly an empty response
	return true
}

// extractFirstUserMessage 提取 chat 请求 body 中第一条 role="user" 的消息文本。
//
// 2026-06-28: 为 session-audit hook.CheckV1 提供 user content。
// 返回 "" 表示 body 不可解析 / 找不到 user message（hook 收到空 content
// 会降级 Pass，不阻断主流程）。
func extractFirstUserMessage(bodyBytes []byte) string {
	var body struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		return ""
	}
	for _, m := range body.Messages {
		if m.Role == "user" {
			// content 可以是 string 或 []contentPart；用 extractMessageText 统一处理。
			var anyContent any
			if err := json.Unmarshal(m.Content, &anyContent); err == nil {
				return extractMessageText(anyContent)
			}
			return string(m.Content)
		}
	}
	return ""
}
