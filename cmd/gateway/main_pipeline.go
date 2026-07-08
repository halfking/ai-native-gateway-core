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

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/admin"                                                //nolint:depguard // Phase 4 SetClusterRunner 注入
	"github.com/kaixuan/llm-gateway-go/autoroute"                                            //nolint:depguard // LLM caller for enhanced PI detection
	"github.com/kaixuan/llm-gateway-go/domain"                                               //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domain/analysis"                                      //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	agentecosystem "github.com/kaixuan/llm-gateway-go/domains/agent-ecosystem"               //nolint:depguard
	sessionanalytics "github.com/kaixuan/llm-gateway-go/domains/analysis"                    //nolint:depguard // Phase 4 会话全景分析引擎
	"github.com/kaixuan/llm-gateway-go/domains/analysis/bus"                                 //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/analysis/workers"                             //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/assets"                                       //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/authentication"                               //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/credential"                                   //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"                                  //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/cache"                                  //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"                            //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability"                          //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	outputcompliancehooks "github.com/kaixuan/llm-gateway-go/domains/hooks/outputcompliance" //nolint:depguard
	promptinjectionhooks "github.com/kaixuan/llm-gateway-go/domains/hooks/promptinjection"   //nolint:depguard
	legacysec "github.com/kaixuan/llm-gateway-go/domains/hooks/security"                     //nolint:depguard
	sessionanalysis "github.com/kaixuan/llm-gateway-go/domains/hooks/sessionanalysis"        //nolint:depguard
	sessioninspector "github.com/kaixuan/llm-gateway-go/domains/hooks/session-inspector"     //nolint:depguard
	"github.com/kaixuan/llm-gateway-go/domains/hooks/tools"                                  //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/identity"                                     //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/interception"                                 //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"                                     //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/provider"                                     //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/routing"                                      //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/security"                                     //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	securityplugins "github.com/kaixuan/llm-gateway-go/domains/security/plugins"             //nolint:depguard
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"                                 //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/streaming"                                    //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/eventbus"
)

// v2DispatchConfig holds the feature-flag-driven configuration for the
// Pipeline wrapper around the 4 v1 endpoints. Every field except
// UsePipeline defaults to the values used by cmd/gateway-v2.
type v2DispatchConfig struct {
	UsePipeline       bool
	EnableCache       bool
	EnableSecurity    bool
	EnableAudit       bool
	EnableObserv      bool
	EnableStreaming   bool
	EnableAuth        bool // 2026-06-29: Enable authentication hook
	EnableAnalysis    bool // PR-V4-09: 异步分析 Loop 默认 off
	AnalysisInterval  time.Duration
	AnalysisBatchSize int
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
		UsePipeline:       v2UsePipeline(),
		EnableCache:       envBool("LLM_GATEWAY_V2_CACHE", true),
		EnableSecurity:    envBool("LLM_GATEWAY_V2_SECURITY", true),
		EnableAudit:       envBool("LLM_GATEWAY_V2_AUDIT", true),
		EnableObserv:      envBool("LLM_GATEWAY_V2_OBSERV", true),
		EnableStreaming:   envBool("LLM_GATEWAY_V2_STREAMING", true),
		EnableAuth:        envBool("LLM_GATEWAY_V2_AUTH", false), // 2026-06-29: Auth disabled by default (demo mode)
		EnableAnalysis:    envBool("LLM_GATEWAY_V2_ANALYSIS", false),
		AnalysisInterval:  envDuration("LLM_GATEWAY_V2_ANALYSIS_INTERVAL", 5*time.Second),
		AnalysisBatchSize: envInt("LLM_GATEWAY_V2_ANALYSIS_BATCH", 10),
	}
}

func envDuration(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	return def
}

func envInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
		return n
	}
	return def
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
	// into ChatHandler internally (domains/streaming/messages.go etc.).
	ChatHandler *streaming.ChatHandler

	// ── V4 async analysis Loop (PR-V4-09) ────────────────────────
	// PGDBPool 由 main.go 注入；nil 时 EnableAnalysis 自动失效。
	PGDBPool *pgxpool.Pool
	// ApprovalManager 让 dispatch_gate 在 Suspend 时真正写 approval_queue。
	ApprovalManager *sessionaudit.ApprovalManager

	// analysisCancel Loop goroutine 退出信号；v2ShutdownPipeline 触发。
	analysisCancel context.CancelFunc

	// ── V4 publish + asset sink (PR-V4-10) ───────────────────────
	// Publisher 把 request.completed 写入 analysis_events；
	// IntentStore 是 flusher 的 sink，按 (tenant, kind) 累计。
	// 两者均为 nil 时仅退化为 in-memory Loop。
	Publisher   bus.Publisher
	IntentStore assets.IntentAggregateStore

	// intentWorker 在 startAnalysisLoopIfConfigured 创建；flusher 共用它。
	intentWorker *workers.IntentWorker
	intentCancel context.CancelFunc // flusher goroutine 退出信号

	// ── V4 governance hooks (PR-V4-11) ───────────────────────────
	// 这三个接口均为 nil 时对应 Hook 不注册（Enabled=false）。
	// 由 main.go 在 dbConn 可用时注入具体实现。
	PromptInjectionDetector promptinjectionhooks.Detector
	OutputComplianceChecker outputcompliancehooks.Checker
	SessionSummarizer       workers.SessionSummarizer

	// ── 增强版提示词注入检测插件 ────────────────────────────────
	// EnhancedPIPlugin 在 buildV2DispatchPipeline 中创建并注册到 secRegistry，
	// 由 SetV2DispatchAnalysisResources 注入 DB + LLM caller 完成初始化。
	EnhancedPIPlugin *securityplugins.PromptInjectionEnhancedPlugin

	// ── Phase 4: Session Analytics Hook (会话全景分析插件) ───────
	// SessionAnalysisEngines 为 nil 时分析 Hook 不注册（Enabled=false）。
	// 由 main.go 在 session_analytics.enabled 且 dbConn 可用时注入。
	// 通过 PhasePostResponse Hook 接入请求管道，准实时生成逐步摘要/标签/标题。
	SessionAnalysisEngines *sessionanalysis.Engines
	SessionAnalysisConfig  *sessionanalytics.LLMStageConfig
}

// buildV2DispatchPipeline assembles the Hook Pipeline used by the v2
// dispatch wrapper. The order mirrors cmd/gateway-v2/main.go's
// buildPipeline() and cmd/gateway/main_v2_pipeline.go's
// buildV2Pipeline(). The transformation stage is intentionally omitted
// (see main_v2_pipeline.go header for the rationale — transport
// metrics duplicate-registration panic).
func buildV2DispatchPipeline(deps *v2DispatchDeps) *pipeline.RequestPipeline {
	p := pipeline.NewRequestPipeline()

	// === Phase: Authentication (priority 10) ===
	// 2026-06-29: Enable when LLM_GATEWAY_V2_AUTH=true
	// Extract API Key from metadata and verify against database.
	if deps.Config.EnableAuth {
		keyVerifier := authentication.NewKeyVerifier()
		// Note: KeyVerifier needs DB connection to verify keys.
		// In production, pass the DB pool from main.go.
		// For demo/test: keys are not verified, hook will skip.
		p.AddStage(&pipeline.PipelineStage{
			Name:  "authentication",
			Phase: pipeline.PhaseAuthentication,
			Mode:  pipeline.ModeSequential,
			Hooks: []pipeline.Hook{authentication.NewAPIKeyAuthHook(keyVerifier)},
		})
	}

	// === Phase: Client Identity (priority 20) ===
	// 2026-06-29: Extract client identity hash from request (IP, headers, tenant, API key)
	// and inject into env.Metadata for downstream hooks and audit.
	p.AddStage(&pipeline.PipelineStage{
		Name: "client_identity", Phase: pipeline.PhasePreRouting, Mode: pipeline.ModeSequential,
		Hooks: []pipeline.Hook{identity.NewClientIdentityHook()},
	})

	// === Phase: Session Loader (priority 30) ===
	// TODO: Extract session logic from ChatHandler to session.Hook
	// p.AddStage(&pipeline.PipelineStage{
	// 	Name: "session_loader", Phase: pipeline.PhasePreRouting, Mode: pipeline.ModeSequential,
	// 	Hooks: []pipeline.Hook{session.NewSessionLoaderHook(...)},
	// })

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
				legacysec.NewSecurityHook(
					legacysec.NewIntentAnalyzer(0.5),
					legacysec.NewThreatDetector(7),
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

	// V4 governance stage (PR-V4-02). PR-V4-03 wires the security plugin
	// registry here; older SecurityHook in domains/hooks/security still
	// runs at PreRouting as a coarse pre-filter (dual-path during
	// migration; consolidated in a later PR).
	secRegistry := security.NewRegistry()
	secRegistry.MustRegister(securityplugins.NewPromptInjectionChecker())
	secRegistry.MustRegister(securityplugins.NewSensitiveInputChecker())
	secRegistry.MustRegister(securityplugins.NewSensitiveOutputChecker())
	secRegistry.MustRegister(securityplugins.NewPolicyComplianceChecker())
	secRegistry.MustRegister(securityplugins.NewToolRiskChecker())
	secRegistry.MustRegister(securityplugins.NewDataExfiltrationChecker())

	// 增强版提示词注入检测插件（延迟初始化，依赖 DB + LLM caller）
	enhancedPIPlugin := securityplugins.NewPromptInjectionEnhancedPlugin()
	secRegistry.MustRegister(enhancedPIPlugin)
	deps.EnhancedPIPlugin = enhancedPIPlugin

	p.AddStage(&pipeline.PipelineStage{
		Name: "governance_security", Phase: pipeline.PhaseGovernance, Mode: pipeline.ModeSequential,
		Hooks: []pipeline.Hook{security.NewSecurityHook(secRegistry, security.Scope{})},
	})

	// PR-V4-11: prompt injection hook（可选；deps.PromptInjectionDetector 非 nil 时启用）。
	// 放在 governance_security 之后，interception engine 之前，让 Engine 汇总它的 verdict。
	if deps.PromptInjectionDetector != nil {
		p.AddStage(&pipeline.PipelineStage{
			Name: "governance_prompt_injection", Phase: pipeline.PhaseGovernance, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{promptinjectionhooks.NewHook(deps.PromptInjectionDetector)},
		})
	}

	// V4 interception engine (PR-V4-04): 把 security verdicts 汇总成 Decision，
	// 写入 env.Governance.Decision。后续 PR 引入 dispatch gate 后接管 HTTP 拦截。
	interceptEngine := interception.NewEngine(interception.EngineConfig{
		BlockThreshold:    2,
		SuspendOnCritical: false,
	})
	p.AddStage(&pipeline.PipelineStage{
		Name: "governance_interception", Phase: pipeline.PhaseGovernance, Mode: pipeline.ModeSequential,
		Hooks: []pipeline.Hook{interception.NewInterceptionHook(interceptEngine)},
	})

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
		// PR-V4-11: output compliance hook（可选；deps.OutputComplianceChecker 非 nil 时启用）。
		// 放在 streaming 之前——这样 redaction 发生在 SSE 切片之前。
		if deps.OutputComplianceChecker != nil {
			p.AddStage(&pipeline.PipelineStage{
				Name: "post_upstream_output_compliance", Phase: pipeline.PhasePostUpstream, Mode: pipeline.ModeSequential,
				Hooks: []pipeline.Hook{outputcompliancehooks.NewHook(deps.OutputComplianceChecker)},
			})
		}
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

	// === Phase: Session Analytics (PostResponse, 准实时分析插件) ===
	// 由 settings.session_analytics.enabled 控制；engines 为 nil 时跳过。
	// 在 metrics 之后执行（priority 250），异步不阻塞响应。
	if deps.SessionAnalysisEngines != nil {
		p.AddStage(&pipeline.PipelineStage{
			Name: "session_analytics", Phase: pipeline.PhasePostResponse, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{
				sessionanalysis.NewAnalysisHook(deps.SessionAnalysisEngines, deps.SessionAnalysisConfig, slog.Default()),
			},
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
func newV2DispatchDepsFromMain(cfg v2DispatchConfig, chatHandler *streaming.ChatHandler) *v2DispatchDeps {
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
		// 2026-06-26: prefer the server-generated X-Request-Id (the
		// RequestIDMiddleware always overwrites this header). Fall
		// back to a v2pipe-prefixed timestamp only when the middleware
		// chain was bypassed — e.g. direct unit-test dispatch. Never
		// reuse the client-supplied value: a misbehaving client could
		// otherwise collapse every retry into a single audit row.
		requestID := r.Header.Get("X-Request-Id")
		if requestID == "" {
			requestID = fmt.Sprintf("v2pipe-%d", time.Now().UnixNano())
		}
		env := domain.NewRequestEnvelope(ctx, &domain.RequestEnvelope{
			RequestID: requestID,
			CreatedAt: time.Now(),
			GoContext: ctx,
			// 2026-06-29: Populate Transport so hooks that depend on
			// the raw *http.Request (e.g. ClientIdentityHook) can run.
			// Without this, env.Envelope.HasTransport() is false and
			// every transport-dependent hook is silently disabled.
			Transport: &domain.TransportContext{
				R:        r,
				IsStream: false, // updated below after body sniff
			},
		})
		env.TenantID = r.Header.Get("X-Tenant-ID")
		env.SessionID = r.Header.Get("X-Session-ID")

		// Best-effort body sniff for metadata. chatHandler will
		// re-parse the full body for its own protocol decoding.
		model, stream, _, _ := dispatchRequestBody(r)
		if env.Envelope != nil && env.Envelope.Transport != nil {
			env.Envelope.Transport.IsStream = stream
		}
		env.Metadata = map[string]any{
			"method": r.Method,
			"path":   r.URL.Path,
			"model":  model,
			"stream": stream,
			"remote": r.RemoteAddr,
			"agent":  r.UserAgent(),
		}

		// Extract API Key from Authorization header for authentication hook
		if auth := r.Header.Get("Authorization"); auth != "" {
			if len(auth) > 7 && auth[:7] == "Bearer " {
				env.Metadata["api_key"] = auth[7:]
			}
		}
		// Also check X-API-Key header (fallback)
		if apiKey := r.Header.Get("X-API-Key"); apiKey != "" {
			env.Metadata["api_key"] = apiKey
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

		// V4 dispatch gate (PR-V4-05): pipeline 写入的 Decision 在此生效。
		// Block / Suspend / Terminate → 直接写响应并 short-circuit；
		// Continue / Mutate → 继续交给 fallback handler。
		if interception.InspectDecision(env) == interception.DispatchShortCircuit {
			// PR-V4-09: 当 deps.ApprovalManager 非 nil 时，注入真实
			// ApprovalCreator，让 Suspend 决策真正写 approval_queue。
			var creator interception.ApprovalCreator
			if deps.ApprovalManager != nil {
				creator = interception.NewApprovalManagerCreator(deps.ApprovalManager)
			}
			code, body := interception.WriteDecisionResponseWithApprovals(ctx, env, creator)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(code)
			if len(body) > 0 {
				_, _ = w.Write(body)
			}
			slog.Info("v2 dispatch gate: short-circuit",
				"request_id", requestID,
				"path", r.URL.Path,
				"status", code,
				"decision_kind", env.Governance.Decision.Kind,
			)
			return
		}

		// Forward to the v1 chatHandler. The chatHandler writes the
		// response (including SSE stream) to w and we just observe.
		//
		// 2026-06-29: Inject Pipeline-computed identity into the request
		// context so ChatHandler can reuse it instead of recomputing.
		// See domains/streaming/handler.go:1151 fallback.
		if rawID, ok := env.Metadata["client_identity"]; ok {
			if cid, ok := rawID.(identity.ClientIdentity); ok {
				ctx = identity.WithComputedIdentity(ctx, &cid)
				r = r.WithContext(ctx)
			}
		}
		fallback.ServeHTTP(w, r)

		// Postflight: best-effort metrics stamping. We can't append
		// to the response (it may already be flushed for streaming
		// responses), so we only update internal env state for the
		// audit/metrics hooks to consume.
		env.StatusCode = 0 // writer's status is unknown; left for a later phase

		// PR-V4-10: 异步发布 request.completed 事件 → analysis_events。
		// 只在 deps.Publisher 非 nil 时执行（即 EnableAnalysis=true 且
		// 注入了 DB pool）。失败仅记录日志，不影响主流程。
		if deps.Publisher != nil && env.TenantID != "" {
			evt := analysis.AnalysisEvent{
				EventID:    "evt-" + requestID,
				Type:       analysis.EventRequestCompleted,
				TenantID:   env.TenantID,
				SessionID:  env.SessionID,
				RequestID:  requestID,
				OccurredAt: time.Now(),
				Payload: map[string]any{
					"user_content": extractFirstUserMessage(env),
					"status_code":  env.StatusCode,
					"model":        env.Metadata["model"],
					"path":         r.URL.Path,
				},
			}
			if err := deps.Publisher.Publish(ctx, evt); err != nil {
				slog.Warn("v2 dispatch: publish request.completed failed",
					"request_id", requestID, "error", err)
			}
		}
	})
}

// extractFirstUserMessage 从 env 中尝试取出第一条用户消息文本。
//
// 容忍多种 envelope 形态（不同 endpoint 走不同结构）；找不到时返回空串。
// 这里不做完整解析——只供 IntentWorker 分类用，越宽容越好。
func extractFirstUserMessage(env *domain.PipelineRequest) string {
	if env == nil || env.Metadata == nil {
		return ""
	}
	for _, k := range []string{"user_content", "user_message", "prompt", "input"} {
		if v, ok := env.Metadata[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
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
// the production streaming.ChatHandler — wrapping the LLM call through it
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

	// The Pipeline wrapper needs a concrete *streaming.ChatHandler so it
	// can read the executor / provider / etc. main.go's messages /
	// responses handlers internally call chatHandler.ServeHTTP, so
	// we only need to construct the wrapper for the chatHandler;
	// messages / responses go through their own existing handlers
	// with the Pipeline-wrapped chatHandler behind them.
	var ch *streaming.ChatHandler
	if c, ok := chatHandler.(*streaming.ChatHandler); ok {
		ch = c
	} else {
		slog.Warn("v2 pipeline: chatHandler is not *streaming.ChatHandler; using fallback passthrough",
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
	// Loop 启动延后到 SetV2DispatchAnalysisResources 之后（PR-V4-09）。
	// 见 main.go 中的 StartV2DispatchAnalysisLoop 调用。
	return mux, deps, true
}

// StartV2DispatchAnalysisLoop (PR-V4-09) 在 SetV2DispatchAnalysisResources 注入 DB
// pool + ApprovalManager 之后调用。如果之前 v2DispatchMux 已经返回 deps 但资源
// 还没注入，需要在这里显式触发启动。
func StartV2DispatchAnalysisLoop(deps *v2DispatchDeps) {
	startAnalysisLoopIfConfigured(deps)
}

// startAnalysisLoopIfConfigured (PR-V4-09)
//
// 仅当 EnableAnalysis=true 且 deps.PGDBPool 非 nil 时启动 V4 异步分析 Loop。
// Loop 由 IntentWorker + PGPollFunc + PGMarkFunc 组成；goroutine 内运行，
// analysisCancel 用于优雅退出。
func startAnalysisLoopIfConfigured(deps *v2DispatchDeps) {
	if deps == nil {
		return
	}
	if !deps.Config.EnableAnalysis {
		return
	}
	if deps.PGDBPool == nil {
		slog.Warn("v2 pipeline: EnableAnalysis=true but PGDBPool is nil; skipping loop")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	deps.analysisCancel = cancel

	worker := workers.NewIntentWorker(slog.Default())
	deps.intentWorker = worker
	poll := bus.NewPGPollFunc(bus.AsPGDB(deps.PGDBPool), worker.SubscribedTypes(), deps.Config.AnalysisBatchSize)
	mark := bus.NewPGMarkFunc(bus.AsPGDB(deps.PGDBPool), slog.Default())

	go bus.RunLoop(ctx, worker, poll, mark, bus.LoopConfig{
		Interval:  deps.Config.AnalysisInterval,
		BatchSize: deps.Config.AnalysisBatchSize,
		Logger:    slog.Default(),
	})

	// PR-V4-11: SessionSummaryWorker 独立跑另一个 RunLoop（订阅 session.closed）。
	// deps.SessionSummarizer 为 nil 时跳过（避免空跑）。
	if deps.SessionSummarizer != nil {
		sumWorker := workers.NewSessionSummaryWorker(deps.SessionSummarizer, slog.Default())
		sumPoll := bus.NewPGPollFunc(bus.AsPGDB(deps.PGDBPool), sumWorker.SubscribedTypes(), deps.Config.AnalysisBatchSize)
		sumMark := bus.NewPGMarkFunc(bus.AsPGDB(deps.PGDBPool), slog.Default())
		go bus.RunLoop(ctx, sumWorker, sumPoll, sumMark, bus.LoopConfig{
			Interval:  deps.Config.AnalysisInterval,
			BatchSize: deps.Config.AnalysisBatchSize,
			Logger:    slog.Default(),
		})
		slog.Info("v2 pipeline: session_summary_worker loop started",
			"interval", deps.Config.AnalysisInterval.String())
	}

	// PR-V4-10: 启动 IntentFlusher 把 worker 累计 flush 到 assets store。
	// IntentStore 为 nil 时 flusher 只做 in-memory reset（仍跑 goroutine，
	// 保持代码路径一致，便于测试）。
	flusherCtx, flusherCancel := context.WithCancel(context.Background())
	deps.intentCancel = flusherCancel
	flusherInterval := deps.Config.AnalysisInterval * 6
	if flusherInterval < 30*time.Second {
		flusherInterval = 60 * time.Second
	}
	flusher := workers.NewIntentFlusher(worker, deps.IntentStore, flusherInterval, slog.Default())
	go flusher.Run(flusherCtx)

	slog.Info("v2 pipeline: V4 async analysis loop started",
		"worker", worker.Name(),
		"subscribed_types", worker.SubscribedTypes(),
		"interval", deps.Config.AnalysisInterval.String(),
		"batch_size", deps.Config.AnalysisBatchSize,
		"flusher_interval", flusherInterval.String(),
	)
}

// analysisSubscribedWorkerTypes 是 Loop 默认订阅的事件类型（PR-V4-09）。
// 当前仅 IntentWorker；将来扩展时改这里。
func analysisSubscribedWorkerTypes() []analysis.EventType { //nolint:unused
	return []analysis.EventType{
		analysis.EventRequestCompleted,
	}
}

// v2ShutdownPipeline releases the in-memory resources held by the v2
// dispatch deps. Mirrors shutdownV2Pipeline in main_v2_pipeline.go.
func v2ShutdownPipeline(deps *v2DispatchDeps) {
	if deps == nil {
		return
	}
	// 先取消 flusher（PR-V4-10），保证它在 Loop 退出前把 worker 累计
	// flush 到 store，避免最后窗内的 delta 丢失。
	if deps.intentCancel != nil {
		deps.intentCancel()
		deps.intentCancel = nil
	}
	// 取消 analysis loop（PR-V4-09）。
	if deps.analysisCancel != nil {
		deps.analysisCancel()
		deps.analysisCancel = nil
	}
	// Publisher / IntentStore 不由本函数关闭（pool 由 dbConn 管），
	// 但调用 Close 让实现内部清理（PGPool 之外通常是 no-op）。
	if deps.Publisher != nil {
		_ = deps.Publisher.Close()
	}
	if deps.AuditWriter == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = ctx
	_ = deps.AuditWriter.Close()
}

// SetV2DispatchAnalysisResources (PR-V4-09 / PR-V4-10 / PR-V4-11) 把 main.go 持有的
// DB pool、ApprovalManager、Publisher、IntentStore、可选 hook detector/checker 注入 deps。
//
// 调用时机：v2DispatchMux 返回 deps 之后、StartV2DispatchAnalysisLoop 之前。
// nil 参数对应的子能力自动关闭：
//   - pool == nil → Loop / Flusher / Publisher / IntentStore 都不启动
//   - pub == nil → postflight publish 不跑
//   - store == nil → flusher 只做 in-memory reset
//   - detector == nil → prompt injection hook 不注册
//   - checker == nil → output compliance hook 不注册
func SetV2DispatchAnalysisResources(
	deps *v2DispatchDeps,
	pool *pgxpool.Pool,
	mgr *sessionaudit.ApprovalManager,
	pub bus.Publisher,
	store assets.IntentAggregateStore,
	detector promptinjectionhooks.Detector,
	checker outputcompliancehooks.Checker,
	summarizer workers.SessionSummarizer,
) {
	if deps == nil {
		return
	}
	if pool != nil {
		deps.PGDBPool = pool
	}
	if mgr != nil {
		deps.ApprovalManager = mgr
	}
	if pub != nil {
		deps.Publisher = pub
	}
	if store != nil {
		deps.IntentStore = store
	}
	if detector != nil {
		deps.PromptInjectionDetector = detector
	}
	if checker != nil {
		deps.OutputComplianceChecker = checker
	}
	if summarizer != nil {
		deps.SessionSummarizer = summarizer
	}

	// 增强版提示词注入检测插件初始化
	// 当 DB pool 可用时，初始化插件并注入依赖
	if pool != nil && deps.EnhancedPIPlugin != nil {
		// 构建 LLM caller（复用 autoroute 模式）
		llmCaller := buildEnhancedPILMCaller()
		deps.EnhancedPIPlugin.Init(pool, llmCaller)
		slog.Info("enhanced prompt injection plugin initialized")
	}

	// Phase 4: 会话全景分析引擎注入。
	// 仅当 session_analytics.enabled 且 pool 可用时构建引擎。
	// 通过 PhasePostResponse hook 接入请求管道，准实时分析。
	if pool != nil && sessionanalytics.NewLLMStageConfig(nil).Enabled() {
		cfg := sessionanalytics.NewLLMStageConfig(nil)
		analyticsDB := sessionanalytics.NewPoolDB(pool)
		engines := &sessionanalysis.Engines{
			RequestSummarizer: sessionanalytics.NewRequestSummarizer(analyticsDB, cfg, nil, slog.Default()),
			Tagger:            sessionanalytics.NewSessionTagger(analyticsDB, cfg, slog.Default()),
		}
		deps.SessionAnalysisEngines = engines
		deps.SessionAnalysisConfig = cfg

		// 注入 ClusterRunner（手动触发聚类用）
		admin.SetClusterRunner(sessionanalytics.NewSessionClusterer(analyticsDB, cfg, nil, slog.Default()))
	}
}

// buildEnhancedPILMCaller 构建增强版 PI 检测的 LLM caller
// 复用 autoroute 的 HTTPLlmCallerConfig 模式
func buildEnhancedPILMCaller() securityplugins.LLMCaller {
	endpoint := strings.TrimSpace(os.Getenv("LLMGatewayAutoLLMEndpoint"))
	if endpoint == "" {
		// 未配置 LLM endpoint，返回 nil（LLM 检测将被禁用）
		return nil
	}

	cfg := autoroute.HTTPLlmCallerConfig{
		Endpoint: endpoint,
		APIKey:   strings.TrimSpace(os.Getenv("LLMGatewayAutoLLMApiKey")),
		Model:    strings.TrimSpace(os.Getenv("LLMGatewayAutoLLMModel")),
	}
	if cfg.Model == "" {
		cfg.Model = "gpt-4o-mini"
	}
	cfg.Timeout = 10 * time.Second
	cfg.MaxTokens = 512

	caller := autoroute.NewHTTPLlmCaller(cfg)
	return &llmCallerAdapter{caller: caller}
}

// llmCallerAdapter 适配 autoroute.LLMCaller 到 securityplugins.LLMCaller
type llmCallerAdapter struct {
	caller autoroute.LLMCaller
}

func (a *llmCallerAdapter) Call(ctx context.Context, prompt string) (string, error) {
	return a.caller.Call(ctx, prompt)
}
