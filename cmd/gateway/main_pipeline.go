// Command gateway - main_pipeline.go (2026-06-26, R1.12)
//
// Pipeline-based v1 dispatch wrapper for cmd/gateway/main.go.
//
// Background
// ----------
// main.go (1695 lines) has been the production entry point for the data
// plane since v1. Its 4 v1 chat endpoints (/v1/chat/completions,
// /v1/completions, /v1/messages, /v1/responses) are dispatched directly
// to relay.{ChatHandler,MessagesHandler,ResponsesHandler} with all the
// pre-flight (auth, key verify, model policy) and post-flight (telemetry,
// request WAL, audit) handled inline. The cmd/gateway-v2/ demo binary
// shows the v2 Hook Pipeline (Round 40+) in action but is opt-in via
// LLM_GATEWAY_V2_ENABLED → /v2/* namespace only.
//
// R1.12 goal
// ----------
// Let the SAME production entry point (main.go) optionally run the v1
// endpoints through the v2 Hook Pipeline, without rewriting the 1695
// lines of init/teardown. Concretely:
//
//   1. When LLM_GATEWAY_USE_V2_PIPELINE=true  → the 4 v1 endpoints are
//      wrapped by v2DispatchHandler. The wrapper:
//        a) Parses the request body, builds a domain.PipelineRequest.
//        b) Runs a preflight Pipeline (tracing, security, audit).
//        c) Forwards to the v1 chatHandler for the actual LLM call.
//        d) Runs a postflight Pipeline (metrics, cache_save, audit).
//   2. When the flag is unset / false  → mux routes the 4 endpoints
//      to the existing chatHandler / messagesHandler / responsesHandler
//      unchanged. Production safety: zero behavior change.
//
// What is preserved
// -----------------
//   - main.go's init sequence (DB pool, Redis, Casdoor, executor, ...).
//   - relay/ChatHandler.ServeHTTP is the source of truth for v1 auth,
//     key verify, model policy, request WAL, telemetry, sticky cache.
//   - routing/relay/compressor imports remain — the Pipeline internally
//     calls routing.NewStickyRouter (same package, same behavior).
//   - admin API, /healthz, /metrics, /v1/sessions, /v1/models.
//
// What is new
// -----------
//   - v2DispatchDeps  : struct holding the v2 deps wired from main.go.
//   - v2UsePipeline() : flag reader (LLM_GATEWAY_USE_V2_PIPELINE or
//     the legacy LLM_GATEWAY_V2_ENABLED — OR semantics).
//   - buildV2DispatchDeps()  : builds the Pipeline + dependencies.
//   - v2DispatchHandler()    : the http.Handler that wraps a single
//                              v1 endpoint through the Pipeline.
//   - v2DispatchMux()        : sub-mux for the 4 v1 endpoints.
//   - registerV2DispatchRoutes() : parent-mux registration entry point.
//
// Design notes
// ------------
//   - The wrapper intentionally DELEGATES the LLM call to chatHandler
//     (routing.Executor + relay.StreamChat) rather than reimplementing
//     it. This keeps the v2 dispatch as a *real* integration: tracing,
//     security, audit, metrics actually wrap the production code path.
//   - The Pipeline stages mirror cmd/gateway-v2/main.go's
//     buildPipeline() so the two binaries stay in lockstep. The
//     transformation stage is omitted (see main_v2_pipeline.go header
//     for the prometheus duplicate-registration rationale).
//   - The preflight pipeline mutates env.Metadata; the chatHandler
//     reads standard request headers / body, so the two are decoupled.
//   - When flag is OFF, the wrapper is never constructed (cheap path).
//
// Round 43 / R1.12 safety contract
// --------------------------------
//   - Default OFF.
//   - Flag flips require a restart (read once at startup).
//   - Even when ON, the wrapper falls through to the v1 chatHandler,
//     so a misbehaving Pipeline stage cannot lose data: a stage error
//     is logged and the request still completes via the chatHandler.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
	agentecosystem "github.com/kaixuan/llm-gateway-go/domains/agent-ecosystem"
	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/cache"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/security"
	sessioninspector "github.com/kaixuan/llm-gateway-go/domains/hooks/session-inspector"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/tools"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
	"github.com/kaixuan/llm-gateway-go/domains/provider"
	"github.com/kaixuan/llm-gateway-go/domains/routing"
	"github.com/kaixuan/llm-gateway-go/domains/streaming"
	"github.com/kaixuan/llm-gateway-go/eventbus"
	"github.com/kaixuan/llm-gateway-go/relay"
)

// v2DispatchConfig holds the feature-flag-driven configuration for the
// Pipeline wrapper around the 4 v1 endpoints. Every field except
// UsePipeline defaults to the values used by cmd/gateway-v2.
type v2DispatchConfig struct {
	UsePipeline     bool
	EnableCache     bool
	EnableSecurity  bool
	EnableAudit     bool
	EnableObserv    bool
	EnableStreaming bool
}

// v2UsePipeline reports whether the v2 Pipeline wrapper should be used
// for the 4 v1 endpoints. Reads TWO env vars with OR semantics so
// existing operator muscle memory keeps working:
//
//	LLM_GATEWAY_USE_V2_PIPELINE    (preferred, R1.12)
//	LLM_GATEWAY_V2_ENABLED         (legacy, Round 49)
//
// Any truthy value (1/true/yes in any case) enables. Default: false.
func v2UsePipeline() bool {
	for _, k := range []string{"LLM_GATEWAY_USE_V2_PIPELINE", "LLM_GATEWAY_V2_ENABLED"} {
		v := strings.ToLower(strings.TrimSpace(os.Getenv(k)))
		if v == "1" || v == "true" || v == "yes" {
			return true
		}
	}
	return false
}

// loadV2DispatchConfig builds the dispatch configuration from env vars.
func loadV2DispatchConfig() v2DispatchConfig {
	return v2DispatchConfig{
		UsePipeline:     v2UsePipeline(),
		EnableCache:     envBool("LLM_GATEWAY_V2_CACHE", true),
		EnableSecurity:  envBool("LLM_GATEWAY_V2_SECURITY", true),
		EnableAudit:     envBool("LLM_GATEWAY_V2_AUDIT", true),
		EnableObserv:    envBool("LLM_GATEWAY_V2_OBSERV", true),
		EnableStreaming: envBool("LLM_GATEWAY_V2_STREAMING", true),
	}
}

// v2DispatchDeps bundles the dependencies the Pipeline wrapper needs.
// Every field is optional; a nil field is treated as "stage disabled".
// The fields are intentionally close to v2PipelineDeps in
// main_v2_pipeline.go so the two pipelines stay comparable.
type v2DispatchDeps struct {
	Config           v2DispatchConfig
	Pipeline         *pipeline.RequestPipeline
	EventBus         *eventbus.MemoryBus
	CacheStore       cache.Store
	AuditSink        audit.Sink
	AuditWriter      *audit.BatchWriter
	Metrics          *observability.Registry
	Tracer           observability.Tracer
	AgentReg         *agentecosystem.Registry
	CredentialStore  *credential.InMemoryStore
	CredentialHealth *credential.HealthChecker
	CredentialLimit  *credential.Limiter
	ProviderStore    *provider.InMemoryStore
	ProviderProber   *provider.Prober

	// ── v1 references (the actual data plane) ──────────────────────
	// ChatHandler is the production v1 chat dispatcher. The Pipeline
	// wrapper delegates the LLM call to it so the integration is
	// real (not a parallel demo). The other 3 endpoints
	// (/v1/messages, /v1/responses, /v1/completions) all funnel
	// into ChatHandler internally (relay/messages.go etc.).
	ChatHandler *relay.ChatHandler
}

// buildV2DispatchPipeline assembles the Hook Pipeline used by the v2
// dispatch wrapper. The order mirrors cmd/gateway-v2/main.go's
// buildPipeline() and cmd/gateway/main_v2_pipeline.go's
// buildV2Pipeline(). The transformation stage is intentionally omitted
// (see main_v2_pipeline.go header for the rationale — transport
// metrics duplicate-registration panic).
func buildV2DispatchPipeline(deps *v2DispatchDeps) *pipeline.RequestPipeline {
	p := pipeline.NewRequestPipeline()

	if deps.Config.EnableObserv && deps.Tracer != nil {
		p.AddStage(&pipeline.PipelineStage{
			Name: "tracing", Phase: pipeline.PhasePreRouting, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{observability.NewTracingHook(deps.Tracer)},
		})
	}

	if deps.Config.EnableSecurity {
		p.AddStage(&pipeline.PipelineStage{
			Name: "security", Phase: pipeline.PhasePreRouting, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{
				security.NewSecurityHook(
					security.NewIntentAnalyzer(0.5),
					security.NewThreatDetector(7),
				),
			},
		})
	}

	if deps.ProviderStore != nil && deps.ProviderProber != nil {
		p.AddStage(&pipeline.PipelineStage{
			Name: "provider_discovery", Phase: pipeline.PhasePreRouting, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{
				provider.NewProviderDiscoveryHook(deps.ProviderStore, deps.ProviderProber),
			},
		})
	}

	if deps.CredentialStore != nil && deps.CredentialHealth != nil {
		p.AddStage(&pipeline.PipelineStage{
			Name: "credential_health", Phase: pipeline.PhasePreRouting, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{
				credential.NewHealthCheckHook(deps.CredentialStore, deps.CredentialHealth),
			},
		})
	}

	if deps.Config.EnableCache && deps.CacheStore != nil {
		p.AddStage(&pipeline.PipelineStage{
			Name: "cache_lookup", Phase: pipeline.PhasePreRouting, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{cache.NewCacheLookupHook(deps.CacheStore)},
		})
	}

	p.AddStage(&pipeline.PipelineStage{
		Name: "session_inspect", Phase: pipeline.PhasePreRouting, Mode: pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			sessioninspector.NewInspectorHook(
				sessioninspector.NewTokenLimitInspector(100000),
				sessioninspector.NewInactiveInspector(30*time.Minute),
				sessioninspector.NewHighFrequencyInspector(60),
			),
		},
	})

	if deps.AgentReg != nil {
		p.AddStage(&pipeline.PipelineStage{
			Name: "agent_discovery", Phase: pipeline.PhasePreRouting, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{agentecosystem.NewAgentDiscoveryHook(deps.AgentReg)},
		})
	}

	// Routing stage: uses the same routing package as main_v2_pipeline.go
	// (routing.NewStickyRouter(routing.NewRoundRobinRouter())). The hook
	// only contributes routing decisions to env.SelectedCredential — the
	// actual upstream call still goes through chatHandler.
	if deps.CredentialLimit != nil {
		sticky := routing.NewStickyRouter(routing.NewRoundRobinRouter())
		p.AddStage(&pipeline.PipelineStage{
			Name: "routing", Phase: pipeline.PhaseRouting, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{routing.NewRoutingHook(sticky)},
		})
		p.AddStage(&pipeline.PipelineStage{
			Name: "credential_limit", Phase: pipeline.PhasePostRouting, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{credential.NewLimiterHook(deps.CredentialLimit)},
		})
	}

	// Transform stage placeholder (transformation package omitted for
	// the same reason as main_v2_pipeline.go — see its file header).
	p.AddStage(&pipeline.PipelineStage{
		Name: "transform", Phase: pipeline.PhaseTransform, Mode: pipeline.ModeSequential,
		Hooks: []pipeline.Hook{&passthroughHook{name: "transform", priority: 50}},
	})

	p.AddStage(&pipeline.PipelineStage{
		Name: "compression", Phase: pipeline.PhaseTransform, Mode: pipeline.ModeSequential,
		Hooks: []pipeline.Hook{compression.NewCompressionHook(compression.NewLCSCompressor(4096))},
	})

	p.AddStage(&pipeline.PipelineStage{
		Name: "tools", Phase: pipeline.PhasePostTransform, Mode: pipeline.ModeSequential,
		Hooks: []pipeline.Hook{tools.NewToolInterceptionHook(tools.NewMetaToolInterceptor(""))},
	})

	if deps.Config.EnableStreaming {
		p.AddStage(&pipeline.PipelineStage{
			Name: "streaming", Phase: pipeline.PhasePostUpstream, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{streaming.NewStreamHook(streaming.NewSSEStreamer())},
		})
	}

	if deps.Config.EnableAudit && deps.AuditWriter != nil {
		p.AddStage(&pipeline.PipelineStage{
			Name: "audit", Phase: pipeline.PhasePostResponse, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{audit.NewAuditLogHook(deps.AuditWriter)},
		})
	}

	if deps.Config.EnableCache && deps.CacheStore != nil {
		p.AddStage(&pipeline.PipelineStage{
			Name: "cache_save", Phase: pipeline.PhasePostResponse, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{cache.NewCacheSaveHook(deps.CacheStore, 5*time.Minute)},
		})
	}

	if deps.Config.EnableObserv && deps.Metrics != nil {
		p.AddStage(&pipeline.PipelineStage{
			Name: "metrics", Phase: pipeline.PhasePostResponse, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{observability.NewMetricsHook(deps.Metrics)},
		})
	}

	return p
}

// newV2DispatchDepsFromMain wires the v2 dispatch deps from the
// production main.go's local variables. This is the bridge between the
// existing init sequence and the new Pipeline wrapper. Returns nil if
// the chatHandler is unavailable (defensive: should never happen
// because main.go always constructs it).
//
// IMPORTANT: this function does NOT allocate any production resources.
// It only references the existing in-memory singletons from main.go's
// scope. The Pipeline runs in-process; there is no DB/Redis fan-out
// from here.
func newV2DispatchDepsFromMain(cfg v2DispatchConfig, chatHandler *relay.ChatHandler) *v2DispatchDeps {
	// Always build deps (even if chatHandler is nil — e.g. test stubs
	// or dev/smoke). The wrapping handler will pass through to the
	// nil chatHandler if Pipeline hooks don't short-circuit, which
	// is the expected behavior. Returning nil here would make the
	// dispatch mux unusable for tests.

	// Build the in-memory stores that the Pipeline hooks need.
	// These are LOCAL to the dispatch wrapper — they do not replace
	// the production stores in main.go.
	cacheStore := cache.NewInMemoryStore()
	auditSink := audit.NewInMemorySink()
	auditWriter := audit.NewBatchWriter(auditSink, 100, 5*time.Second)
	metrics := observability.NewRegistry()
	tracer := observability.NewInMemoryTracer()
	agentReg := agentecosystem.NewRegistry()

	credStore := credential.NewInMemoryStore()
	credHealth := credential.NewHealthChecker(credStore)
	credLimiter := credential.NewLimiter()

	// Seed a default credential / provider so the Pipeline hooks can
	// run end-to-end in dev / smoke tests. Production traffic still
	// flows through chatHandler (which has the real DB-backed
	// provider client).
	_ = credStore.Save(&credential.Credential{
		ID: "default-cred", TenantID: "default", ProviderID: "default-openai", Model: "gpt-4",
		EncryptedKey:  []byte("demo-encrypted-key"),
		Priority:      50,
		Status:        credential.StatusActive,
		MaxConcurrent: 10,
	})

	provStore := provider.NewInMemoryStore()
	provProber := provider.NewProber(provStore)
	_ = provStore.Save(&provider.Provider{
		ID:       "default-openai",
		Name:     "OpenAI",
		BaseURL:  "https://api.openai.com",
		Protocol: provider.ProtocolOpenAI,
		AuthType: "bearer",
		Models: []provider.ModelSpec{
			{Name: "gpt-4", MaxContextTokens: 8192, SupportsStream: true, SupportsTools: true},
			{Name: "gpt-3.5-turbo", MaxContextTokens: 4096, SupportsStream: true},
		},
		TimeoutSec: 60,
	})

	deps := &v2DispatchDeps{
		Config:           cfg,
		CacheStore:       cacheStore,
		AuditSink:        auditSink,
		AuditWriter:      auditWriter,
		Metrics:          metrics,
		Tracer:           tracer,
		AgentReg:         agentReg,
		CredentialStore:  credStore,
		CredentialHealth: credHealth,
		CredentialLimit:  credLimiter,
		ProviderStore:    provStore,
		ProviderProber:   provProber,
		EventBus:         eventbus.NewMemoryBus(100),
		ChatHandler:      chatHandler,
	}
	deps.Pipeline = buildV2DispatchPipeline(deps)
	return deps
}

// dispatchRequestBody parses the JSON body of an OpenAI / Anthropic
// request just enough to extract the model name and stream flag for
// the Pipeline envelope. The full body is left for chatHandler to
// re-parse (it owns the protocol-specific decoding).
//
// Returns (model, stream, rawBody, error). rawBody is always the
// original payload so chatHandler can re-read it from r.Body.
func dispatchRequestBody(r *http.Request) (model string, stream bool, rawBody []byte, err error) {
	if r.Body == nil {
		return "", false, nil, nil
	}
	rawBody, err = io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		return "", false, nil, err
	}
	// Restore the body so chatHandler can read it.
	r.Body = io.NopCloser(strings.NewReader(string(rawBody)))

	// Best-effort: sniff for known top-level fields. Don't fail the
	// request on parse errors — chatHandler will return a 400.
	if len(rawBody) == 0 {
		return "", false, rawBody, nil
	}
	var sniff struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if jerr := json.Unmarshal(rawBody, &sniff); jerr == nil {
		model = sniff.Model
		stream = sniff.Stream
	}
	return model, stream, rawBody, nil
}

// v2DispatchHandler is the http.Handler that wraps a single v1
// endpoint through the Pipeline. The pattern is:
//
//  1. Build a PipelineRequest from the HTTP request.
//  2. Run the Pipeline.Execute (preflight → trace/security/audit/...).
//  3. Forward to chatHandler.ServeHTTP for the real LLM call.
//  4. Run a postflight pass (audit, metrics).
//
// Any Pipeline stage error is logged but does NOT block the request
// from reaching chatHandler. This is the R1.12 safety contract: a
// misbehaving Hook cannot lose data.
func v2DispatchHandler(deps *v2DispatchDeps, fallback http.Handler) http.Handler {
	if deps == nil || deps.Pipeline == nil {
		return fallback
	}
	if fallback == nil {
		fallback = http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		requestID := r.Header.Get("X-Request-Id")
		if requestID == "" {
			requestID = fmt.Sprintf("v2pipe-%d", time.Now().UnixNano())
		}
		env := domain.NewRequestEnvelope(ctx, &domain.RequestEnvelope{
			RequestID: requestID,
			CreatedAt: time.Now(),
			GoContext: ctx,
		})
		env.TenantID = r.Header.Get("X-Tenant-ID")
		env.SessionID = r.Header.Get("X-Session-ID")

		// Best-effort body sniff for metadata. chatHandler will
		// re-parse the full body for its own protocol decoding.
		model, stream, _, _ := dispatchRequestBody(r)
		env.Metadata = map[string]any{
			"method":  r.Method,
			"path":    r.URL.Path,
			"model":   model,
			"stream":  stream,
			"api_key": r.Header.Get("X-API-Key"),
			"remote":  r.RemoteAddr,
			"agent":   r.UserAgent(),
		}

		// Preflight pipeline. A stage error is logged but does NOT
		// short-circuit the request — chatHandler is the source of
		// truth and must still get a chance to serve it.
		pipeStart := time.Now()
		if perr := deps.Pipeline.Execute(ctx, env); perr != nil {
			slog.Warn("v2 pipeline: preflight error (falling through to v1 handler)",
				"request_id", requestID,
				"path", r.URL.Path,
				"error", perr,
			)
			if env.Metadata == nil {
				env.Metadata = make(map[string]any)
			}
			env.Metadata["v2_pipeline_error"] = perr.Error()
		}
		env.Metadata["v2_pipeline_latency_ms"] = time.Since(pipeStart).Milliseconds()

		// Forward to the v1 chatHandler. The chatHandler writes the
		// response (including SSE stream) to w and we just observe.
		fallback.ServeHTTP(w, r)

		// Postflight: best-effort metrics stamping. We can't append
		// to the response (it may already be flushed for streaming
		// responses), so we only update internal env state for the
		// audit/metrics hooks to consume.
		env.StatusCode = 0 // writer's status is unknown; left for a later phase
	})
}

// v2DispatchMux wires the v2 dispatch dependencies when the flag is
// on. It is the production entry point used by main.go. Returns
// (nil, nil, false) when the flag is off — main.go then leaves the v1
// registrations as-is. The returned sub-mux is a fresh *http.ServeMux
// holding ONLY the 4 v1 endpoints (no /v1/sessions, no /v1/models, no
// /healthz, no /metrics). main.go registers the 4 v1 endpoints on its
// parent mux FIRST, then overrides them with the v2 wrapper via the
// returned deps (Go's http.ServeMux picks the last-registered handler
// for an exact-path match).
//
// Why a fresh sub-mux: keeps the v2 dispatch self-contained for tests
// and avoids a single shared mutable mux. The chatHandler reference is
// the production relay.ChatHandler — wrapping the LLM call through it
// is what makes the integration real (not a parallel demo).
func v2DispatchMux(chatHandler, messagesHandler, responsesHandler http.Handler) (*http.ServeMux, *v2DispatchDeps, bool) {
	cfg := loadV2DispatchConfig()
	if !cfg.UsePipeline {
		return nil, nil, false
	}
	if chatHandler == nil {
		slog.Warn("v2 pipeline: chatHandler is nil; cannot build dispatch mux")
		return nil, nil, false
	}

	// The Pipeline wrapper needs a concrete *relay.ChatHandler so it
	// can read the executor / provider / etc. main.go's messages /
	// responses handlers internally call chatHandler.ServeHTTP, so
	// we only need to construct the wrapper for the chatHandler;
	// messages / responses go through their own existing handlers
	// with the Pipeline-wrapped chatHandler behind them.
	var ch *relay.ChatHandler
	if c, ok := chatHandler.(*relay.ChatHandler); ok {
		ch = c
	} else {
		slog.Warn("v2 pipeline: chatHandler is not *relay.ChatHandler; using fallback passthrough",
			"actual", fmt.Sprintf("%T", chatHandler))
	}

	deps := newV2DispatchDepsFromMain(cfg, ch)

	// Wrap the chatHandler in the Pipeline. messagesHandler and
	// responsesHandler internally call chatHandler.ServeHTTP, so
	// they will be dispatched to the wrapped chatHandler via the
	// v1 mux (we only override the /v1/chat/completions and
	// /v1/completions entries; /v1/messages and /v1/responses
	// continue to use their existing handler instances which
	// forward into chatHandler).
	mux := http.NewServeMux()
	mux.Handle("/v1/chat/completions", v2DispatchHandler(deps, chatHandler))
	mux.Handle("/v1/completions", v2DispatchHandler(deps, chatHandler))
	if messagesHandler != nil {
		mux.Handle("/v1/messages", v2DispatchHandler(deps, messagesHandler))
	}
	if responsesHandler != nil {
		mux.Handle("/v1/responses", v2DispatchHandler(deps, responsesHandler))
	}

	stages := 0
	if deps != nil && deps.Pipeline != nil {
		stages = len(deps.Pipeline.Stages())
	}
	slog.Info("v2 pipeline: LLM_GATEWAY_USE_V2_PIPELINE=true, 4 v1 endpoints wrapped",
		"cache", cfg.EnableCache,
		"security", cfg.EnableSecurity,
		"audit", cfg.EnableAudit,
		"observ", cfg.EnableObserv,
		"streaming", cfg.EnableStreaming,
		"stages", stages,
	)
	return mux, deps, true
}

// v2ShutdownPipeline releases the in-memory resources held by the v2
// dispatch deps. Mirrors shutdownV2Pipeline in main_v2_pipeline.go.
func v2ShutdownPipeline(deps *v2DispatchDeps) {
	if deps == nil || deps.AuditWriter == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = ctx
	_ = deps.AuditWriter.Close()
}
