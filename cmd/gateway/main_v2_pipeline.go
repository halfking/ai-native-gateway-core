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

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domain"                                           //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	agentecosystem "github.com/kaixuan/llm-gateway-go/domains/agent-ecosystem"           //nolint:depguard
	"github.com/kaixuan/llm-gateway-go/domains/credential"                               //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"                              //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/cache"                              //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"                        //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability"                      //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/security"                           //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	sessioninspector "github.com/kaixuan/llm-gateway-go/domains/hooks/session-inspector" //nolint:depguard
	"github.com/kaixuan/llm-gateway-go/domains/hooks/tools"                              //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"                                 //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/provider"                                 //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/routing"                                  //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	v2 "github.com/kaixuan/llm-gateway-go/domains/session/v2"                            //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/streaming"                                //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/eventbus"
	"github.com/kaixuan/llm-gateway-go/settings"
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
	Config             v2PipelineConfig
	Pipeline           *pipeline.RequestPipeline
	EventBus           *eventbus.MemoryBus
	CacheStore         cache.Store
	AuditSink          audit.Sink
	AuditWriter        *audit.BatchWriter
	Metrics            *observability.Registry
	Tracer             observability.Tracer
	AgentReg           *agentecosystem.Registry
	CredentialStore    *credential.InMemoryStore
	CredentialHealth   *credential.HealthChecker
	CredentialLimit    *credential.Limiter
	ProviderStore      *provider.InMemoryStore
	ProviderProber     *provider.Prober
	SessionPersistHook *v2.SessionPersistHook // nil when pool is nil (in-memory stub mode)
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
				security.NewSecurityHook(settings.Global),
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
			func() pipeline.Hook {
				inspectorHook := sessioninspector.NewInspectorHookWithConfig(nil)
				// 注入 EventBus 以启用告警事件发布（2026-07-09 audit fix）
				if deps.EventBus != nil {
					inspectorHook.SetEventBus(deps.EventBus)
				}
				return inspectorHook
			}(),
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

	// PhasePostResponse: session persistence — writes to V2 tables (sessions, session_turns,
	// session_bodies, session_turn_logs). Runs after metrics so session snapshot is the last
	// hook. Feature-flagged via sessions_v2.{enabled,shadow_write,rollout_percent}.
	// Best-effort: errors are logged and never propagate to the HTTP response.
	if deps.SessionPersistHook != nil {
		p.AddStage(&pipeline.PipelineStage{
			Name: "session_persist", Phase: pipeline.PhasePostResponse, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{deps.SessionPersistHook},
		})
	}

	return p
}

// newV2PipelineDeps creates in-memory dependencies for the v2 route group.
// IMPORTANT: when pool is non-nil, the session persist hook is wired with real
// DB writers so v2 pipeline traffic is dual-written to V2 tables. When pool is
// nil (in-memory stub mode), the session hook is disabled and no DB write occurs.
func newV2PipelineDeps(cfg v2PipelineConfig, pool *pgxpool.Pool) *v2PipelineDeps {
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

	var sessionHook *v2.SessionPersistHook
	if pool != nil {
		turnWriter := v2.NewTurnWriter(pool)
		bodiesWriter := v2.NewSessionBodiesWriter(pool)
		aggregator := v2.NewSessionAggregator(pool)
		turnLogsWriter := v2.NewTurnLogsWriter(pool)
		writer := v2.NewSessionWriterV2(turnWriter, bodiesWriter, aggregator, turnLogsWriter)
		sessionHook = v2.NewSessionPersistHook(writer)
		slog.Info("v2 pipeline: session persist hook wired (dual-write ready)",
			"pool_healthy", pool != nil)
	} else {
		slog.Info("v2 pipeline: no DB pool, session persist hook disabled (in-memory stub)")
	}

	return &v2PipelineDeps{
		Config:             cfg,
		CacheStore:         cacheStore,
		AuditSink:          auditSink,
		AuditWriter:        auditWriter,
		Metrics:            metrics,
		Tracer:             tracer,
		AgentReg:           agentReg,
		CredentialStore:    credStore,
		CredentialHealth:   credHealth,
		CredentialLimit:    credLimiter,
		ProviderStore:      provStore,
		ProviderProber:     provProber,
		EventBus:           eventbus.NewMemoryBus(100),
		SessionPersistHook: sessionHook,
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
		// Only populate user_content when the URL param is actually present (non-empty).
		// Storing "" for "absent" makes it impossible for downstream to distinguish
		// "client sent no q param" from "client explicitly sent q="". See Bug-3.
		env.Metadata = map[string]any{
			"model":   r.URL.Query().Get("model"),
			"api_key": r.Header.Get("X-API-Key"),
		}
		if q := r.URL.Query().Get("q"); q != "" {
			env.Metadata["user_content"] = q
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
// pool may be nil for pure in-memory stub mode.
func v2PipelineSubMux(pool *pgxpool.Pool) (http.Handler, *v2PipelineDeps, bool) {
	cfg := loadV2PipelineConfig()
	if !cfg.Enabled {
		return nil, nil, false
	}
	deps := newV2PipelineDeps(cfg, pool)
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
func registerV2PipelineRoutes(parent *http.ServeMux, pool *pgxpool.Pool) {
	if parent == nil {
		slog.Warn("v2 pipeline: nil parent mux, skipping registration")
		return
	}

	if !v2PipelineEnabled() {
		slog.Info("v2 pipeline: LLM_GATEWAY_V2_ENABLED is not set; /v2/* routes not registered (production default)")
		return
	}

	cfg := loadV2PipelineConfig()
	deps := newV2PipelineDeps(cfg, pool)
	deps.Pipeline = buildV2Pipeline(deps)

	// 2026-07-09: 飞书机器人模块 late-binding。
	// 与 main.go 同一函数 InitFeishubotPlugin；v2 pipeline 无 LarkChannel 注入，
	// 因此仅在 gLarkCh 非空时生效。失败仅记日志（best-effort）。
	if _, ferr := InitFeishubotPlugin(deps.EventBus, gLarkCh, gApprovalMgr, parent, nil); ferr != nil {
		slog.Warn("v2 pipeline: feishubot init failed (best-effort)", "error", ferr)
	}

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

// sessionV2HookConfig is the minimal flag subset that controls whether the
// SessionPersistHook (V2 shadow write sidecar) gets wired into the
// production pipeline. The struct is intentionally separate from
// v2DispatchConfig and v2PipelineConfig so callers from main.go and from
// the /v2/* demo can each pass the bits they actually own.
//
//	Enabled     — sessions_v2.enabled (master switch).
//	ShadowWrite — sessions_v2.shadow_write (dual-write sidecar).
//
// The rollout percentage (sessions_v2.rollout_percent) is read live by
// SessionPersistHook.Enabled() — see domains/session/v2/pipeline_hook.go.
// We deliberately do NOT plumb it through this struct: keeping the
// percentage as a hot-reload knob (settings.Global) is the design.
type sessionV2HookConfig struct {
	Enabled     bool
	ShadowWrite bool
}

// buildV2PipelineHooks returns the V2 pipeline hooks that should be
// appended to the production pipeline when sessions_v2 is enabled.
//
// Today the only such hook is SessionPersistHook (the V2 shadow write
// sidecar). The function returns an empty slice when the master switch
// or shadow_write flag is off, and nil when cfg is nil.
//
// Nil pool safety: when pool is nil, the helper still returns the hook
// so registration works; DB writes happen at Execute() time and the
// hook itself is no-op-safe via writer==nil guards.
//
// Exposed as a package-level helper (rather than private to main.go)
// so TestSessionV2HookRegistered in main_v2_pipeline_test.go can
// assert the wiring without spinning up a full pipeline.
func buildV2PipelineHooks(cfg *sessionV2HookConfig, pool *pgxpool.Pool) []pipeline.Hook {
	if cfg == nil || !cfg.Enabled || !cfg.ShadowWrite {
		return nil
	}
	// initSessionV2Writer logs WARN and returns nil when pool is nil
	// or both flags are off at startup. We still register the hook
	// either way: the hook's Enabled() reads the live flag values on
	// every call, so a startup-time nil writer is safe (DB writes
	// happen at Execute time and writer==nil is a guarded no-op).
	writer := initSessionV2Writer(pool)
	return []pipeline.Hook{v2.NewSessionPersistHook(writer)}
}

// loadSessionV2HookConfigFromSettings reads the two production flags
// from the platform settings store (hot-reload) and packages them
// into a sessionV2HookConfig. main.go calls this once at startup
// (after settings.Global is initialized) and the resulting struct is
// passed into buildV2PipelineHooks.
//
// Hot-reload caveat: the SessionPersistHook re-reads these flags via
// settings.GetPlatformBool on every Enabled() call, so a startup-time
// snapshot is only used to decide WHETHER to register the hook at
// all (the cheapest correct answer when both flags are off).
func loadSessionV2HookConfigFromSettings() *sessionV2HookConfig {
	return &sessionV2HookConfig{
		Enabled:     settings.GetPlatformBool("sessions_v2.enabled", false),
		ShadowWrite: settings.GetPlatformBool("sessions_v2.shadow_write", false),
	}
}
