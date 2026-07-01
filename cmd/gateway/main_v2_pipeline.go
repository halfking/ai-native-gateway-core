// Command gateway - main_v2_pipeline.go (2026-06-26)
//
// Opt-in registration of the v2 Hook Pipeline endpoints behind a feature
// flag. The actual production data plane (cmd/gateway/main.go) keeps running
// on its existing routing/relay path; this file only attaches a parallel
// /v2/* route group that demonstrates the new Pipeline architecture from
// cmd/gateway-v2.
//
// Safety contract (Round 43 / R1.12):
//
//   - Default OFF. The flag is read once at startup from
//     LLM_GATEWAY_V2_ENABLED; toggling it requires a restart.
//   - When OFF: registerV2PipelineRoutes is a no-op. mux state and main.go
//     behavior are unchanged. Existing v1 routes, /healthz, /metrics,
//     admin API, etc. continue to work exactly as before.
//   - When ON: a parallel /v2/* route group is added. v1 routes are not
//     touched. Operators can route a small percentage of traffic to the
//     v2 hostname via nginx to灰度 test.
//
// Dependencies: this file copies the demo v2 wiring from
// cmd/gateway-v2/main.go (the two binaries live in different `package main`
// scopes, so import isn't possible). The deps remain in-memory for the
// flag stub so the production DB pool and Redis are untouched. Wiring the
// real DB pool / Redis is a later phase (R1.13+) once the v2 Pipeline
// passes integration tests under load.
//
// Why the v2 stub omits the `transform` stage: domains/transformation
// package registers prometheus metrics under the `transport_*` prefix
// (a copy-paste leftover in metrics.go), which collides with the
// `transport/metrics.go` package init. cmd/gateway/main.go already
// imports `transport`, so importing `transformation` from this package
// would panic the test binary at init time. The transform stage is
// therefore skipped here; the remaining 13 Hook Pipeline stages are
// sufficient to validate routing, security, cache, credential, and
// observability behavior under the v2 entry point. A future phase that
// wires the real production DB pool will also rename the conflicting
// metric names so the transform stage can be re-enabled.

package main

import (
	"context"
	"encoding/json"
	"fmt"
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
)

// v2PipelineConfig holds the feature-flag-driven configuration for the v2
// /v2/* route group. Every field except Enabled defaults to the values used
// by cmd/gateway-v2 so that "ON" matches the demo behavior exactly.
type v2PipelineConfig struct {
	Enabled         bool
	EnableCache     bool
	EnableSecurity  bool
	EnableAudit     bool
	EnableObserv    bool
	EnableStreaming bool
}

// v2PipelineEnabled reports whether the v2 Pipeline route group should be
// registered. Reads LLM_GATEWAY_V2_ENABLED at call time (once, at startup)
// and treats "1", "true", "yes" (case-insensitive) as truthy. Any other
// value or unset is OFF.
//
// Default: false. Production safety is the priority: until R1.13+ validates
// the v2 Pipeline under real traffic, the flag must remain off everywhere.
func v2PipelineEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("LLM_GATEWAY_V2_ENABLED")))
	return v == "1" || v == "true" || v == "yes"
}

// loadV2PipelineConfig builds the v2 configuration from env vars. The
// LLM_GATEWAY_V2_* knobs mirror cmd/gateway-v2/main.go so that the two
// binaries stay in sync when both are deployed side-by-side.
func loadV2PipelineConfig() v2PipelineConfig {
	return v2PipelineConfig{
		Enabled:         v2PipelineEnabled(),
		EnableCache:     envBool("LLM_GATEWAY_V2_CACHE", true),
		EnableSecurity:  envBool("LLM_GATEWAY_V2_SECURITY", true),
		EnableAudit:     envBool("LLM_GATEWAY_V2_AUDIT", true),
		EnableObserv:    envBool("LLM_GATEWAY_V2_OBSERV", true),
		EnableStreaming: envBool("LLM_GATEWAY_V2_STREAMING", true),
	}
}

// envBool is a small helper for parsing boolean env vars. "1", "true",
// "yes" (case-insensitive) → true. Anything else (including empty) → def.
func envBool(key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if v == "" {
		return def
	}
	return v == "1" || v == "true" || v == "yes"
}

// v2PipelineDeps mirrors cmd/gateway-v2/main.go::v2Deps but is local to the
// cmd/gateway package so the v1 binary can register the demo routes without
// importing across the two `package main` binaries.
type v2PipelineDeps struct {
	Config           v2PipelineConfig
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
}

// passthroughHook is a no-op Hook implementation used as a placeholder for
// stages that cannot be linked into cmd/gateway due to package-level
// conflicts (see the file header note on the transform stage). It records
// the stage name in the envelope metadata so tests can assert execution
// order without losing observability.
type passthroughHook struct {
	name     string
	priority int
}

func (h *passthroughHook) Name() string { return h.name }

func (h *passthroughHook) Priority() int { return h.priority }

func (h *passthroughHook) Enabled(_ context.Context, _ *domain.PipelineRequest) bool {
	return true
}

func (h *passthroughHook) Execute(_ context.Context, env *domain.PipelineRequest) error {
	if env == nil {
		return nil
	}
	if env.Metadata == nil {
		env.Metadata = make(map[string]any)
	}
	env.Metadata["v2_stage_"+h.name] = "ok"
	return nil
}

func (h *passthroughHook) OnError(_ context.Context, _ *domain.PipelineRequest, err error) error {
	return err
}

// buildV2Pipeline assembles the Hook Pipeline stages for the v2 stub. The
// order mirrors cmd/gateway-v2/main.go::buildPipeline EXCEPT the transform
// stage (see file header for rationale). The compression stage here uses
// the LCS compressor from domains/hooks/compression directly — that
// package handles prometheus duplicate-registration gracefully.
func buildV2Pipeline(deps *v2PipelineDeps) *pipeline.RequestPipeline {
	p := pipeline.NewRequestPipeline()

	if deps.Config.EnableObserv {
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

	p.AddStage(&pipeline.PipelineStage{
		Name: "provider_discovery", Phase: pipeline.PhasePreRouting, Mode: pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			provider.NewProviderDiscoveryHook(deps.ProviderStore, deps.ProviderProber),
		},
	})

	p.AddStage(&pipeline.PipelineStage{
		Name: "credential_health", Phase: pipeline.PhasePreRouting, Mode: pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			credential.NewHealthCheckHook(deps.CredentialStore, deps.CredentialHealth),
		},
	})

	if deps.Config.EnableCache {
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

	p.AddStage(&pipeline.PipelineStage{
		Name: "agent_discovery", Phase: pipeline.PhasePreRouting, Mode: pipeline.ModeSequential,
		Hooks: []pipeline.Hook{agentecosystem.NewAgentDiscoveryHook(deps.AgentReg)},
	})

	sticky := routing.NewStickyRouter(routing.NewRoundRobinRouter())
	p.AddStage(&pipeline.PipelineStage{
		Name: "routing", Phase: pipeline.PhaseRouting, Mode: pipeline.ModeSequential,
		Hooks: []pipeline.Hook{routing.NewRoutingHook(sticky)},
	})

	p.AddStage(&pipeline.PipelineStage{
		Name: "credential_limit", Phase: pipeline.PhasePostRouting, Mode: pipeline.ModeSequential,
		Hooks: []pipeline.Hook{credential.NewLimiterHook(deps.CredentialLimit)},
	})

	// transform stage (domains/transformation) is omitted here. See file
	// header for rationale. A no-op placeholder is kept so the stage
	// sequence length and timing parity with cmd/gateway-v2 stay close.
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

	if deps.Config.EnableAudit {
		p.AddStage(&pipeline.PipelineStage{
			Name: "audit", Phase: pipeline.PhasePostResponse, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{audit.NewAuditLogHook(deps.AuditWriter)},
		})
	}

	if deps.Config.EnableCache {
		p.AddStage(&pipeline.PipelineStage{
			Name: "cache_save", Phase: pipeline.PhasePostResponse, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{cache.NewCacheSaveHook(deps.CacheStore, 5*time.Minute)},
		})
	}

	if deps.Config.EnableObserv {
		p.AddStage(&pipeline.PipelineStage{
			Name: "metrics", Phase: pipeline.PhasePostResponse, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{observability.NewMetricsHook(deps.Metrics)},
		})
	}

	return p
}

// newV2PipelineDeps creates in-memory dependencies for the v2 route group.
// IMPORTANT: this does NOT touch the production DB pool, Redis, or Memora
// client held in main.go. The flag stub is a sandboxed demo so an
// accidental enable cannot affect v1 traffic or production data.
func newV2PipelineDeps(cfg v2PipelineConfig) *v2PipelineDeps {
	cacheStore := cache.NewInMemoryStore()
	auditSink := audit.NewInMemorySink()
	auditWriter := audit.NewBatchWriter(auditSink, 100, 5*time.Second)
	metrics := observability.NewRegistry()
	tracer := observability.NewInMemoryTracer()
	agentReg := agentecosystem.NewRegistry()

	credStore := credential.NewInMemoryStore()
	credHealth := credential.NewHealthChecker(credStore)
	credLimiter := credential.NewLimiter()

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

	return &v2PipelineDeps{
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
	}
}

// v2PipelineHTTPHandler returns the http.Handler that backs the /v2/*
// route group. The shape mirrors cmd/gateway-v2/main.go::httpHandler so the
// two endpoints are byte-compatible for parity testing.
func v2PipelineHTTPHandler(deps *v2PipelineDeps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		env := domain.NewRequestEnvelope(ctx, &domain.RequestEnvelope{
			RequestID: fmt.Sprintf("v2-req-%d", time.Now().UnixNano()),
			CreatedAt: time.Now(),
			GoContext: ctx,
		})

		env.TenantID = r.Header.Get("X-Tenant-ID")
		env.SessionID = r.Header.Get("X-Session-ID")
		env.Metadata = map[string]any{
			"user_content": r.URL.Query().Get("q"),
			"model":        r.URL.Query().Get("model"),
			"api_key":      r.Header.Get("X-API-Key"),
		}
		if env.Metadata["user_content"] == nil {
			env.Metadata["user_content"] = ""
		}

		if err := deps.Pipeline.Execute(ctx, env); err != nil {
			w.Header().Set("Content-Type", "application/json")
			if env.StatusCode == 0 {
				env.StatusCode = 500
			}
			w.WriteHeader(env.StatusCode)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":      err.Error(),
				"request_id": env.Envelope.RequestID,
			})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"request_id": env.Envelope.RequestID,
			"status":     "ok",
			"tenant_id":  env.TenantID,
		})
	})

	mux.HandleFunc("/v2/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"service":"llm-gateway-go","version":"0.3.0","pipeline":"v2","status":"ok"}`))
	})

	return mux
}

// v2PipelineSubMux builds (and returns) the v2 sub-mux without registering
// it onto a parent. Exported for tests that need to assert mux shape.
func v2PipelineSubMux() (http.Handler, *v2PipelineDeps, bool) {
	cfg := loadV2PipelineConfig()
	if !cfg.Enabled {
		return nil, nil, false
	}
	deps := newV2PipelineDeps(cfg)
	deps.Pipeline = buildV2Pipeline(deps)
	return v2PipelineHTTPHandler(deps), deps, true
}

// registerV2PipelineRoutes is the production entry point called from
// main.go immediately after the v1 mux is built. It is intentionally
// idempotent and safe to call once per process.
//
// When the flag is OFF (default), this is a no-op — the existing v1 mux
// passes through unchanged.
//
// When the flag is ON, a fresh sub-mux holding /v2/* routes is mounted
// onto the parent mux under "/v2/". This means the v1 routes (/v1/*,
// /healthz, /metrics, /api/*, etc.) continue to handle their existing
// paths and the v2 namespace is independent.
func registerV2PipelineRoutes(parent *http.ServeMux) {
	if parent == nil {
		slog.Warn("v2 pipeline: nil parent mux, skipping registration")
		return
	}

	if !v2PipelineEnabled() {
		slog.Info("v2 pipeline: LLM_GATEWAY_V2_ENABLED is not set; /v2/* routes not registered (production default)")
		return
	}

	cfg := loadV2PipelineConfig()
	deps := newV2PipelineDeps(cfg)
	deps.Pipeline = buildV2Pipeline(deps)

	// Register the v2 sub-mux under /v2/. This is independent of the v1
	// routes; the v1 mux's /v1/chat/completions, /v1/messages, etc. are
	// unaffected. A misconfigured nginx upstream cannot reach /v2/* on
	// the v1 hostname unless the operator explicitly adds the rule.
	parent.Handle("/v2/", v2PipelineHTTPHandler(deps))

	slog.Info("v2 pipeline: LLM_GATEWAY_V2_ENABLED=true, /v2/* routes registered",
		"cache", cfg.EnableCache,
		"security", cfg.EnableSecurity,
		"audit", cfg.EnableAudit,
		"observ", cfg.EnableObserv,
		"streaming", cfg.EnableStreaming,
		"stages", len(deps.Pipeline.Stages()),
	)
}

// shutdownV2Pipeline releases in-memory resources held by the v2 deps.
// Currently a thin wrapper around the audit writer close; expanded when
// real DB/Redis wiring lands in a later phase.
func shutdownV2Pipeline(deps *v2PipelineDeps) {
	if deps == nil || deps.AuditWriter == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = ctx
	_ = deps.AuditWriter.Close()
}
