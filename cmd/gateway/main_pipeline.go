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
//   - main.go's init sequence (DB pool, Redis, executor, ...). Auth uses the
//     gateway's local HS256 JWT + HttpOnly cookie (admin/auth.go); Casdoor
//     SSO is a planned optional integration, not yet wired in.
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
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"                                                         //nolint:depguard // pgxpool→sql.DB 桥接
	"github.com/kaixuan/llm-gateway-go/admin"                                                //nolint:depguard // Phase 4 SetClusterRunner 注入
	"github.com/kaixuan/llm-gateway-go/domain"                                               //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domain/analysis"                                      //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	agentecosystem "github.com/kaixuan/llm-gateway-go/domains/agent-ecosystem"               //nolint:depguard
	sessionanalytics "github.com/kaixuan/llm-gateway-go/domains/analysis"                    //nolint:depguard // Phase 4 会话全景分析引擎
	"github.com/kaixuan/llm-gateway-go/domains/analysis/bus"                                 //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/analysis/projectattr"                         //nolint:depguard // 2026-08-20 项目归属 resolver 装配点
	"github.com/kaixuan/llm-gateway-go/domains/analysis/sessionmeta"                         //nolint:depguard // session analysis metadata final UPSERT
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
	sessioninspector "github.com/kaixuan/llm-gateway-go/domains/hooks/session-inspector"     //nolint:depguard
	sessionanalysis "github.com/kaixuan/llm-gateway-go/domains/hooks/sessionanalysis"        //nolint:depguard
	"github.com/kaixuan/llm-gateway-go/domains/hooks/tools"                                  //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/identity"                                     //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/interception"                                 //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"                                     //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/provider"                                     //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/routing"                                      //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/security"                                     //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	securityplugins "github.com/kaixuan/llm-gateway-go/domains/security/plugins"             //nolint:depguard
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"                                 //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/sessionsummary"                               //nolint:depguard // session summary worker wiring
	"github.com/kaixuan/llm-gateway-go/domains/streaming"
	"github.com/kaixuan/llm-gateway-go/eventbus"
	"github.com/kaixuan/llm-gateway-go/extpoints"          //nolint:depguard // 2026-11-19: 16-Phase Plugin 架构桥接
	"github.com/kaixuan/llm-gateway-go/internal/sessionid" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/internal/taskbus"   //nolint:depguard // 03: Hook 双模 taskbus 内核
	"github.com/kaixuan/llm-gateway-go/security/sensitive" //nolint:depguard // AC敏感词引擎
	"github.com/kaixuan/llm-gateway-go/settings"
	"github.com/redis/go-redis/v9"
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
	EnableAuth      bool // 2026-06-29: Enable authentication hook
	// EnableRequestPathStages 注册「请求路径」阶段（transform/response/audit/
	// metrics/session_analytics/session_v2_persist …）。
	//
	// 2026-11-23（211 配置优化遗留 L3）：默认 **false**。
	// 这些阶段要看到上游响应才有意义，而本管线是**前置**执行；
	// 且 audit / session_v2_persist / session_analytics 在前置位置会与
	// handler 自己的 postflight 重复写。详见 buildV2DispatchPipeline 里的注释。
	EnableRequestPathStages bool
	EnableAnalysis          bool // PR-V4-09: 异步分析 Loop 默认 off
	AnalysisInterval        time.Duration
	AnalysisBatchSize       int
	// AdminAPIKey gates the X-LLMGW-Preferred-Credential routing override.
	// 2026-08-23.
	AdminAPIKey string
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
		EnableAuth:      envBool("LLM_GATEWAY_V2_AUTH", false), // 2026-06-29: Auth disabled by default (demo mode)
		// 2026-11-23（L3）：请求路径阶段默认关 —— v2 主链路迁移前不接。
		EnableRequestPathStages: envBool("LLM_GATEWAY_V2_REQUEST_PATH", false),
		EnableAnalysis:          envBool("LLM_GATEWAY_V2_ANALYSIS", false),
		AnalysisInterval:        envDuration("LLM_GATEWAY_V2_ANALYSIS_INTERVAL", 5*time.Second),
		AnalysisBatchSize:       envInt("LLM_GATEWAY_V2_ANALYSIS_BATCH", 10),
		AdminAPIKey:             os.Getenv("LLM_GATEWAY_ADMIN_API_KEY"),
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

	// AdminAPIKey is the static admin token (cfg.AdminAPIKey, env
	// LLM_GATEWAY_ADMIN_API_KEY). It is used to gate the
	// X-LLMGW-Preferred-Credential routing override so that only
	// authenticated admins can force a credential on a request.
	// 2026-08-23.
	AdminAPIKey string

	// ── v1 references (the actual data plane) ──────────────────────
	// ChatHandler is the production v1 chat dispatcher. The Pipeline
	// wrapper delegates the LLM call to it so the integration is
	// real (not a parallel demo). The other 3 endpoints
	// (/v1/messages, /v1/responses, /v1/completions) all funnel
	// into ChatHandler internally (domains/streaming/messages.go etc.).
	ChatHandler *streaming.ChatHandler
	KeyVerifier *authentication.KeyVerifier

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
	// F-41（2026-11-13）：pipeline 在 v2DispatchMux 内构建，而
	// SetV2DispatchAnalysisResources 在构建之后才注入 detector——构建时刻
	// 的 nil 使 stage 永不注册（双重空转第二重）。现改为 stage 无条件注册 +
	// PromptInjectionHook 延迟绑定（见 BindDetector），注入经本字段到达 hook。
	PromptInjectionDetector promptinjectionhooks.Detector
	PromptInjectionHook     *promptinjectionhooks.Hook

	// InterceptEngine 是治理拦截引擎（与 pipeline 里的
	// governance_interception stage 共用同一个实例）。
	//
	// ★ 2026-11-18 审计轮 P0：拦截决策原本**只**由那个 stage 产出，
	//	而 stage 注册序在 provider_discovery 之后 ⇒ 对真实流量
	//	（provider_discovery 对真实模型恒失败 ⇒ 管线在此中止）
	//	**永远到不了决策闸** ⇒ 已写下的 deny verdict 被丢弃。
	//	⇒ 把它暴露出来，让 dispatch 层在管线中止后**补做一次决策**
	//	（见 v2DispatchHandler 的「收口决策」）。
	InterceptEngine         *interception.Engine
	OutputComplianceChecker outputcompliancehooks.Checker
	SessionSummarizer       workers.SessionSummarizer

	// ── 2026-08-20: 项目归属 resolver ────────────────────────────
	// 在 settings flag 打开、pg pool 可用且 resolver 非 nil 时由
	// startAnalysisLoopIfConfigured 装配 CloseHook。resolver=nil 表示
	// "运维还没启用项目同步链路"，hook 静默跳过，避免误触发。
	ProjectAttrResolver *projectattr.ProjectResolver

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
	AnalysisSQLDBs         []*sql.DB

	// SensitiveWordEngine (2026-07-18) 敏感词 AC 自动机引擎。
	// 由 main.go 创建并同时交给 admin handler 与本 Pipeline 的敏感词插件，
	// 使 POST /api/admin/sensitive-words/reload 真正作用到拦流量的那个实例。
	// 在此之前这里是一个死接线：main.go 在 v2DispatchMux 返回之后才赋值，
	// 而插件用的是本函数内的局部引擎，reload 返回 200 但词表纹丝不动。
	SensitiveWordEngine *sensitive.SensitiveWordEngine

	// SecurityRegistry (2026-10-18) 治理插件注册中心的回传口。
	// secRegistry 在 buildV2DispatchPipeline 内创建并装配到治理 stage，
	// 这里回传给 main.go，让 /api/admin/topology/plugins 读到的是
	// **真正拦流量的那个注册中心**（与 SensitiveWordEngine 同款共享接线，
	// 避免再造一份静态清单漂移）。
	SecurityRegistry *security.Registry

	// ── ExtensionPoint Registry (2026-11-19) ──────────────────────
	// ExtensionPointRegistry 是 16-Phase Plugin 架构的注册中心。
	// 在 buildV2DispatchPipeline 内创建，通过 ExtensionPointHook 桥接到
	// domains/pipeline 架构，使插件能在生产请求流中执行。
	// 回传给 main.go 供 demo 插件或外部插件注册使用。
	ExtensionPointRegistry extpoints.ExtensionPointRegistry
}

// buildV2DispatchPipeline assembles the Hook Pipeline used by the v2
// dispatch wrapper. The order mirrors cmd/gateway-v2/main.go's
// buildPipeline() and cmd/gateway/main_v2_pipeline.go's
// buildV2Pipeline(). The transformation stage is intentionally omitted
// (see main_v2_pipeline.go header for the rationale — transport
// metrics duplicate-registration panic).
func buildV2DispatchPipeline(deps *v2DispatchDeps) *pipeline.RequestPipeline {
	p := pipeline.NewRequestPipeline()

	// ── ExtensionPoint Registry (2026-11-19) ──────────────────────────────
	// 创建 16-Phase Plugin 注册表，通过 ExtensionPointHook 桥接到现有 Pipeline。
	// 插件可以在各个 Phase 注册，ExecutionEngine 按优先级顺序执行它们。
	//
	// 集成策略：
	//   - 为每个主要 Phase 创建一个 ExtensionPointHook
	//   - Hook 的 Priority = 500（中等），在标准 Hook 之后执行
	//   - fail-open 策略：插件失败不阻塞请求
	//   - registry 回传到 deps，供外部插件注册
	extRegistry := extpoints.NewExtensionPointRegistry()
	if deps != nil {
		deps.ExtensionPointRegistry = extRegistry
	}

	// 03 taskbus: 注册期 V2 对拍 + cache_save 示范迁（memora 生产零挂载，本波不迁）。
	tbus := taskbus.NewDemoBus()
	if deps != nil && deps.CacheStore != nil {
		cache.RegisterCacheSaveHandler(tbus.Registry, deps.CacheStore)
	}
	p.SetHandlerLookup(tbus)
	go func() { _ = tbus.Dispatcher.Start(context.Background()) }()

	// === Phase: PreAuth (priority 5) ===
	// 2026-11-19: ExtensionPoint - 认证前预处理阶段
	// 允许插件在认证之前执行（例如：请求预处理、速率限制预检、请求标记）
	p.AddStage(&pipeline.PipelineStage{
		Name:  "pre_auth",
		Phase: pipeline.PhasePreAuthentication,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			extpoints.NewExtensionPointHook(extRegistry, "pre_auth"),
		},
	})

	// === Phase: Authentication (priority 10) ===
	// 2026-06-29: Enable when LLM_GATEWAY_V2_AUTH=true
	// Extract API Key from metadata and verify against database.
	if deps.Config.EnableAuth {
		keyVerifier := deps.KeyVerifier
		if keyVerifier == nil {
			keyVerifier = authentication.NewKeyVerifier()
		}
		// Note: KeyVerifier needs DB connection to verify keys.
		// In production, pass the DB pool from main.go.
		// For demo/test: keys are not verified, hook will skip.
		authHooks := []pipeline.Hook{authentication.NewAPIKeyAuthHook(keyVerifier)}

		// ExtensionPoint: 添加 PhaseAuth 插件支持
		authHooks = append(authHooks, extpoints.NewExtensionPointHook(extRegistry, "authentication"))

		p.AddStage(&pipeline.PipelineStage{
			Name:  "authentication",
			Phase: pipeline.PhaseAuthentication,
			Mode:  pipeline.ModeSequential,
			Hooks: authHooks,
		})
	}

	// === Phase: PostAuth (priority 15) ===
	// 2026-11-19: ExtensionPoint - 认证后处理阶段
	// 允许插件在认证完成后执行（例如：用户上下文增强、权限检查、会话初始化）
	p.AddStage(&pipeline.PipelineStage{
		Name:  "post_auth",
		Phase: pipeline.PhasePostAuthentication,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			extpoints.NewExtensionPointHook(extRegistry, "post_auth"),
		},
	})

	// === Phase: Client Identity (priority 20) ===
	// 2026-06-29: Extract client identity hash from request (IP, headers, tenant, API key)
	// and inject into env.Metadata for downstream hooks and audit.
	p.AddStage(&pipeline.PipelineStage{
		Name: "client_identity", Phase: pipeline.PhasePreRouting, Mode: pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			identity.NewClientIdentityHook(),
			// ExtensionPoint: PhasePreRoute 插件
			extpoints.NewExtensionPointHook(extRegistry, "pre_routing"),
		},
	})

	// === Phase: Session Loader (priority 30) ===
	// Session loading is handled inline by ChatHandler (no separate hook).

	if deps.Config.EnableObserv && deps.Tracer != nil {
		p.AddStage(&pipeline.PipelineStage{
			Name: "tracing", Phase: pipeline.PhasePreRouting, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{observability.NewTracingHook(deps.Tracer)},
		})
	}

	// PR-V4-11: prompt injection hook。
	// ★ 2026-11-18 根修（245 生产实证）：阶段从 governance 区块（原
	// governance_security 之后、interception 之前）**前移到 security 之前**。
	// 管线按**注册序**执行（Phase 只是标签），而本管线在 v1 包装路径上的
	// 两个现实决定了位置：
	//   ① provider_discovery 挂在**内存 demo 凭据仓**（newV2DispatchDepsFromMain
	//      种的 gpt-4 假凭据）上，对任何真实模型都报「no healthy provider」
	//      ⇒ 阶段失败 ⇒ wrapper 回落 v1 ⇒ 排在它之后的阶段对真实流量不可达；
	//   ② security.check（独立 ThreatDetector）对注入载荷返回**阶段错误**
	//      （critical threat）⇒ 串行模式中断 ⇒ 排在 security 之后的阶段
	//      对注入载荷也不可达（245 实测：两类请求都到不了 PI hook，
	//      prompt_injection_detections 恒空）。
	// PI 检测是**内容驱动**的，不依赖供应商发现；observe 模式下它**不产生
	// 阶段错误**（warn 继续）⇒ 放在最前：真实流量先过 PI hook（留痕 +
	// 检测失败入队 Area①），后续阶段照旧（security 拦截/provider_discovery
	// 失败回落 v1 完成真实路由）。
	// F-41（2026-11-13）：stage **无条件注册**——构建时刻 deps.PromptInjectionDetector
	// 恒为 nil（真实现由 SetV2DispatchAnalysisResources 在构建后注入），条件注册使
	// governance_prompt_injection 永不出现（双重空转第二重）。hook 经
	// deps.PromptInjectionHook 延迟绑定；detector 未到场时 Enabled() 恒 false，
	// 与「未注册」逐请求等价（pipeline.go executeStage 先过 Enabled 过滤）。
	deps.PromptInjectionHook = promptinjectionhooks.NewHook(deps.PromptInjectionDetector)
	p.AddStage(&pipeline.PipelineStage{
		Name: "governance_prompt_injection", Phase: pipeline.PhaseGovernance, Mode: pipeline.ModeSequential,
		Hooks: []pipeline.Hook{deps.PromptInjectionHook},
	})

	if deps.Config.EnableSecurity {
		p.AddStage(&pipeline.PipelineStage{
			Name: "security", Phase: pipeline.PhasePreRouting, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{
				legacysec.NewSecurityHook(settings.Global),
			},
		})
	}

	if deps.ProviderStore != nil && deps.ProviderProber != nil {
		// ★ 2026-11-23（211 配置优化遗留 L3）：这一阶段改用 **advisory** 版本。
		//
		//	旧行为（严格版）：找不到健康 provider ⇒ 返回 error ⇒
		//	管线在此**中止** ⇒ 排在它之后的阶段对真实流量**全部不可达**。
		//
		//	为什么它对每个真实请求都失败：这条路径（v1 包装，211 生产走的就是它）
		//	挂的 provider 目录是 newV2DispatchDepsFromMain 种下的**内存 demo 目录**
		//	（gpt-4 假凭据 + default-openai 假 provider）⇒ 任何真实模型
		//	（glm-5.3-flash / minimax-m3.1-flash / claude-sonnet-5-5 …）
		//	都命中「no healthy provider」。
		//	⇒ 后果：① 每个请求一条 WARN（v2 pipeline: preflight error）；
		//	         ② 治理链（governance_security / governance_interception）
		//	            对真实流量不可达（2026-11-18 审计轮为此专门加了
		//	            「中止后补做一次决策」的兜底）；
		//	         ③ 路由链（credential_health / routing / …）同样不可达。
		//
		//	★ 为什么它**没有资格**中止管线：在这条路径上，真实路由由 v1 的
		//	  chatHandler（DB 凭据仓）负责，管线里的 provider 目录**不是权威来源**
		//	  ⇒ 它只是「咨询」。真正的门禁是后面的 governance_interception +
		//	  dispatch gate。
		//	⇒ advisory 版把结论写进 env.Metadata（provider_discovery_status /
		//	  provider_count），管线继续往下走。
		//
		//	★ 遗留（已登记）：provider 域要接**真实目录**
		//	  （providers ⨝ provider_models ⨝ credential_model_bindings），
		//	  之后这一阶段才能给出有意义的候选 —— 那时再切回严格版也不迟。
		p.AddStage(&pipeline.PipelineStage{
			Name: "provider_discovery", Phase: pipeline.PhasePreRouting, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{
				provider.NewAdvisoryDiscoveryHook(deps.ProviderStore, deps.ProviderProber),
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
			Hooks: []pipeline.Hook{
				routing.NewRoutingHook(sticky),
				// ExtensionPoint: PhaseRoute 插件
				extpoints.NewExtensionPointHook(extRegistry, "routing"),
			},
		})
		p.AddStage(&pipeline.PipelineStage{
			Name: "credential_limit", Phase: pipeline.PhasePostRouting, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{
				credential.NewLimiterHook(deps.CredentialLimit),
				// ExtensionPoint: PhasePostRoute 插件
				extpoints.NewExtensionPointHook(extRegistry, "post_routing"),
			},
		})
	}

	// V4 governance stage (PR-V4-02). PR-V4-03 wires the security plugin
	// registry here; older SecurityHook in domains/hooks/security still
	// runs at PreRouting as a coarse pre-filter (dual-path during
	// migration; consolidated in a later PR).
	secRegistry := security.NewRegistry()

	// ── AC 自动机敏感词引擎（VibeCoding 多模式匹配，O(n) 线性扫描） ──
	// 使用 main.go 传入的实例，这样 admin 的热加载与真正拦流量的插件共享
	// 同一份词表。deps 为 nil（仅测试桩）时退回自建实例，保持可运行。
	swEngine := deps.SensitiveWordEngine
	if swEngine == nil {
		swEngine = sensitive.NewSensitiveWordEngine()
		swCfgPath := "configs/sensitive_words.json"
		if err := swEngine.BuildFromFile(swCfgPath); err != nil {
			slog.Warn("sensitive word engine init skipped", "path", swCfgPath, "error", err)
		} else {
			slog.Info("sensitive word engine ready", "words", swEngine.LoadedWordCount())
		}
	} else {
		slog.Info("sensitive word engine shared with admin handler",
			"words", swEngine.LoadedWordCount())
	}
	// 注册输入/输出侧敏感词检测插件，替代占位实现
	secRegistry.MustRegister(sensitive.NewSensitiveWordInputPlugin(swEngine))
	secRegistry.MustRegister(sensitive.NewSensitiveWordOutputPlugin(swEngine))

	secRegistry.MustRegister(securityplugins.NewPromptInjectionChecker())
	secRegistry.MustRegister(securityplugins.NewPolicyComplianceChecker())
	secRegistry.MustRegister(securityplugins.NewToolRiskChecker())
	secRegistry.MustRegister(securityplugins.NewDataExfiltrationChecker())

	// 增强版提示词注入检测插件（延迟初始化，依赖 DB + LLM caller）
	enhancedPIPlugin := securityplugins.NewPromptInjectionEnhancedPlugin()
	secRegistry.MustRegister(enhancedPIPlugin)
	deps.EnhancedPIPlugin = enhancedPIPlugin
	deps.SecurityRegistry = secRegistry

	p.AddStage(&pipeline.PipelineStage{
		Name: "governance_security", Phase: pipeline.PhaseGovernance, Mode: pipeline.ModeSequential,
		Hooks: []pipeline.Hook{security.NewSecurityHook(secRegistry, security.Scope{})},
	})

	// （governance_prompt_injection 已于 2026-11-18 前移到 security /
	// provider_discovery **之前**——见上方注册处的根修注释。
	//
	// ★ 2026-11-18 审计轮订正：本行此前写的是「前移到 security **之后**」，
	//   而实际注册已在 security **之前**（6761fba1b 二次前移时只改了注册处
	//   的注释，没回改这行标记）⇒ 注释与代码矛盾 ⇒ 按代码订正。）

	// V4 interception engine (PR-V4-04): 把 security verdicts 汇总成 Decision，
	// 写入 env.Governance.Decision。后续 PR 引入 dispatch gate 后接管 HTTP 拦截。
	interceptEngine := interception.NewEngine(interception.EngineConfig{
		BlockThreshold:    2,
		SuspendOnCritical: false,
	})
	// ★ 2026-11-18 审计轮 P0：把引擎暴露给 dispatch 层，让它在管线
	//	中止（例如 provider_discovery 对真实模型失败）后**补做一次决策**
	//	—— 否则中止前已写下的 deny verdict 会被丢弃（决策闸不可达）。
	deps.InterceptEngine = interceptEngine
	p.AddStage(&pipeline.PipelineStage{
		Name: "governance_interception", Phase: pipeline.PhaseGovernance, Mode: pipeline.ModeSequential,
		Hooks: []pipeline.Hook{interception.NewInterceptionHook(interceptEngine)},
	})

	// ★ 2026-11-23（211 配置优化遗留 L3）：请求路径阶段默认**不注册**。
	//
	//	这些阶段（transform / compression / tools / streaming / response /
	//	audit / metrics / session_analytics / session_v2_persist …）属于
	//	**请求路径**：它们要看到上游响应（env.UpstreamResponse）才能正确工作。
	//	而本管线是**前置**执行（请求还没发出去就执行完了）⇒ 它们在 v1 包装
	//	路径上既没有输入也没有意义。
	//
	//	★ 为什么这不是「把功能关掉」：这些阶段此前**同样不可达** ——
	//	  2026-11-23 之前，管线在 provider_discovery 阶段就中止了（demo 目录
	//	  ⇒ 每个真实请求都命中「no healthy provider」）⇒ 排在它之后的阶段
	//	  对真实流量**从来没有执行过**。
	//	★ 为什么不能「顺手打开」：audit / session_v2_persist / session_analytics
	//	  在前置位置执行会**与 handler 自己的 postflight 重复写**
	//	  （request_logs / session 行 / 分析任务各写两遍）。
	//	⇒ 打开它需要一次专门的迁移（v2 主链路：让管线成为请求路径本身），
	//	  由 LLM_GATEWAY_V2_REQUEST_PATH 显式开启。
	if deps.Config.EnableRequestPathStages {
		registerV2RequestPathStages(p, deps, extRegistry, tbus)
	}

	// NOTE: deps.SensitiveWordEngine is supplied by main.go through
	// v2DispatchMux, i.e. BEFORE this function runs. Do not re-create the
	// engine here — that is what silently detached admin reloads from the
	// engine the plugins actually scan with.

	return p
}

// registerV2RequestPathStages 注册「请求路径」阶段的管线段。
//
// 2026-11-23（211 配置优化遗留 L3）：这些阶段只在 v2 主链路迁移开关
// （LLM_GATEWAY_V2_REQUEST_PATH）打开时注册 —— 理由见调用处注释。
func registerV2RequestPathStages(p *pipeline.RequestPipeline, deps *v2DispatchDeps,
	extRegistry extpoints.ExtensionPointRegistry, tbus *taskbus.Bus) {
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

	// ExtensionPoint: PhasePreUpstream 插件 (2026-11-19)
	// 在 upstream LLM 调用之前执行，允许插件修改请求
	p.AddStage(&pipeline.PipelineStage{
		Name:  "pre_upstream",
		Phase: pipeline.PhasePreUpstream,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			extpoints.NewExtensionPointHook(extRegistry, "pre_upstream"),
		},
	})

	if deps.Config.EnableStreaming {
		// PR-V4-11: output compliance hook（可选；deps.OutputComplianceChecker 非 nil 时启用）。
		// 放在 streaming 之前——这样 redaction 发生在 SSE 切片之前。
		if deps.OutputComplianceChecker != nil {
			p.AddStage(&pipeline.PipelineStage{
				Name: "post_upstream_output_compliance", Phase: pipeline.PhasePostUpstream, Mode: pipeline.ModeSequential,
				Hooks: []pipeline.Hook{
					outputcompliancehooks.NewHook(deps.OutputComplianceChecker),
					// ExtensionPoint: PhasePostUpstream 插件
					extpoints.NewExtensionPointHook(extRegistry, "post_upstream"),
				},
			})
		}
		p.AddStage(&pipeline.PipelineStage{
			Name: "streaming", Phase: pipeline.PhasePostUpstream, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{streaming.NewStreamHook(streaming.NewSSEStreamer())},
		})
	}

	// ExtensionPoint: PhasePreResponse 插件 (2026-11-19)
	// 在响应处理之前执行，允许插件进行响应预处理
	p.AddStage(&pipeline.PipelineStage{
		Name:  "pre_response",
		Phase: pipeline.PhasePreResponse,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			extpoints.NewExtensionPointHook(extRegistry, "pre_response"),
		},
	})

	// ExtensionPoint: PhaseResponse 插件 (2026-11-19)
	// 在响应返回之前执行，允许插件处理最终响应
	p.AddStage(&pipeline.PipelineStage{
		Name:  "response",
		Phase: pipeline.PhaseResponse,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			extpoints.NewExtensionPointHook(extRegistry, "response"),
		},
	})

	// ExtensionPoint: PhasePostResponse 插件 (2026-11-19)
	// 在审计和缓存之前执行，允许插件进行响应后处理
	p.AddStage(&pipeline.PipelineStage{
		Name:  "post_response",
		Phase: pipeline.PhasePostResponse,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			extpoints.NewExtensionPointHook(extRegistry, "post_response"),
		},
	})

	if deps.Config.EnableAudit && deps.AuditWriter != nil {
		p.AddStage(&pipeline.PipelineStage{
			Name: "audit", Phase: pipeline.PhasePostResponse, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{audit.NewAuditLogHook(deps.AuditWriter)},
		})
	}

	if deps.Config.EnableCache && deps.CacheStore != nil {
		// 示范迁：cache_save → TaskBus（Execute 只入队；业务在 cache.save.v1 handler）。
		p.AddStage(&pipeline.PipelineStage{
			Name: "cache_save", Phase: pipeline.PhasePostResponse, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{cache.NewCacheSaveTaskHook(tbus, 5*time.Minute)},
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

	// === V2-P2.3: Session V2 Shadow Write (PostResponse) ===
	// Gated by buildV2PipelineHooks via sessions_v2.enabled &&
	// sessions_v2.shadow_write. When both flags are on, the
	// SessionPersistHook dual-writes request logs to V2 tables
	// (public.sessions / session_turns / session_bodies /
	// session_turn_logs) in addition to the V1 telemetry path.
	// Best-effort: errors are logged inside the hook and never
	// propagated up.
	//
	// PGDBPool is the production DB pool injected by main.go via
	// SetV2DispatchAnalysisResources. Nil pool means shadow write
	// stays disabled (initSessionV2Writer logs WARN and the hook
	// is still registered as a no-op for test/dev parity).
	//
	// The hook's Enabled() also reads sessions_v2.rollout_percent
	// hot-reload at call time, so a startup-time snapshot only
	// controls whether the hook is REGISTERED at all (cheap path
	// when the feature is off).
	for _, h := range buildV2PipelineHooks(loadSessionV2HookConfigFromSettings(), deps.PGDBPool) {
		if h == nil {
			continue
		}
		p.AddStage(&pipeline.PipelineStage{
			Name: "session_v2_persist", Phase: pipeline.PhasePostResponse, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{h},
		})
	}

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
func newV2DispatchDepsFromMain(cfg v2DispatchConfig, chatHandler *streaming.ChatHandler, keyVerifier *authentication.KeyVerifier, swEngine *sensitive.SensitiveWordEngine) *v2DispatchDeps {
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
		KeyVerifier:      keyVerifier,
		AdminAPIKey:      cfg.AdminAPIKey,
	}
	deps.SensitiveWordEngine = swEngine
	deps.Pipeline = buildV2DispatchPipeline(deps)
	return deps
}

// MaxDispatchBodyBytes bounds the dispatch path's request body sniff
// at 100 MB. The outer ingress archives RAW before this bounded sniff.
// Previously this cap was enforced as a silent io.LimitReader
// (which truncated without erroring), an oversized body would reach
// chatHandler as a corrupted 32 MiB slice. Now http.MaxBytesReader
// returns *http.MaxBytesError so we can surface 413 to the client.
const MaxDispatchBodyBytes = 100_000_000

// dispatchRequestBody parses the JSON body of an OpenAI / Anthropic
// request just enough to extract the model name and stream flag for
// the Pipeline envelope. The full body is left for chatHandler to
// re-parse (it owns the protocol-specific decoding).
//
// Returns (model, stream, rawBody, error). rawBody is always the
// original payload so chatHandler can re-read it from r.Body.
//
// 2026-08-26 (P1-20 fix): the previous implementation used
// io.LimitReader(r.Body, 32<<20) which silently truncates oversize
// bodies — a 33 MiB payload would be processed as a corrupted 32 MiB
// one. We now wrap r.Body in http.MaxBytesReader so an oversize
// request returns a *http.MaxBytesError that the caller surfaces as
// HTTP 413 Payload Too Large.
func dispatchRequestBody(r *http.Request) (model string, stream bool, rawBody []byte, err error) {
	if r.Body == nil {
		return "", false, nil, nil
	}

	// Preserve the dispatch maximum even when ingress was bypassed.
	if r.ContentLength > MaxDispatchBodyBytes {
		return "", false, nil, &http.MaxBytesError{Limit: MaxDispatchBodyBytes}
	}
	// Non-archived large requests must use the ingress reduction path.
	if _, archived := streaming.LargeRequestArchive(r.Context()); !archived && r.ContentLength > 32<<20 {
		return "", false, nil, &http.MaxBytesError{Limit: 32 << 20}
	}
	// D-A2 (2026-11-14 audit relay): both static checks above are blind
	// to chunked bodies — Transfer-Encoding: chunked leaves
	// r.ContentLength at -1, so a non-archived 50 MiB body used to slip
	// past the 32 MiB ingress gate and get fully buffered for
	// chatHandler (a Content-Length twin would have been 413'd).
	// Restore the same effective limits on the read itself: the
	// MaxBytesReader now enforces 32 MiB when the ingress reduction
	// path was skipped, 100 MiB for archived bodies. Known-length
	// paths are unaffected: their bodies already passed the pre-checks,
	// so the tighter wrap only bites where the old wrap was too loose.
	limit := int64(MaxDispatchBodyBytes)
	if _, archived := streaming.LargeRequestArchive(r.Context()); !archived {
		limit = 32 << 20
	}
	r.Body = http.MaxBytesReader(nil, r.Body, limit)
	rawBody, err = io.ReadAll(r.Body)
	if err != nil {
		var mbErr *http.MaxBytesError
		if errors.As(err, &mbErr) {
			// Caller surfaces this as 413. We keep the original error
			// wrapped so log lines make it obvious which limit hit.
			return "", false, nil, fmt.Errorf("dispatch body exceeds %d bytes: %w", limit, err)
		}
		return "", false, nil, err
	}
	// Restore the body so chatHandler can read it.
	r.Body = io.NopCloser(strings.NewReader(string(rawBody)))

	// Best-effort: sniff for known top-level fields. Don't fail the
	// request on parse errors — chatHandler will return a 400.
	if len(rawBody) == 0 {
		return "", false, nil, nil
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
			// 2026-07-27 (G-ID-2): when the middleware chain is bypassed
			// (e.g. direct unit-test dispatch), synthesize a request id AND
			// write it back onto the request header. Previously the fallback
			// value lived only in env.RequestID while the downstream
			// chatHandler re-read the (still-empty) header and generated a
			// different id — splitting one request across two audit trails.
			requestID = fmt.Sprintf("v2pipe-%d", time.Now().UnixNano())
			r.Header.Set("X-Request-Id", requestID)
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
		// 2026-07-27 (G-ID-1): read the gateway-canonical session header.
		// The prior code read "X-Session-ID" only, which (because Header.Get
		// is case-insensitive) happened to match the legacy "X-Session-Id"
		// but never the canonical "X-Gw-Session-Id". As a result env.SessionID
		// — and the analysis_events row published below — lost the gateway
		// session id whenever the v2 pipeline was enabled. Mirror the priority
		// used by internal/sessionid.FromHeaders (single source).
		// Single source: internal/sessionid (header table + case fold).
		if sid, _ := sessionid.FromHeaders(r.Header); sid != "" {
			env.SessionID = sessionid.DeriveGatewayID(sid)
		}

		// 2026-08-26 (P1-20 fix): the dispatch sniff used to silently
		// truncate oversize bodies. We now propagate the error from
		// dispatchRequestBody and surface HTTP 413 — the request never
		// reaches chatHandler, so a 33 MiB payload can't corrupt a
		// downstream protocol decoder. Best-effort: parse errors still
		// fall through (chatHandler owns the real protocol decoding
		// and will return its own 400).
		model, stream, rawBody, derr := dispatchRequestBody(r)
		if derr != nil {
			var mbErr *http.MaxBytesError
			if errors.As(derr, &mbErr) {
				slog.Warn("dispatch: body exceeds limit",
					"path", r.URL.Path,
					"limit_bytes", mbErr.Limit)
				writePayloadTooLarge(w, int(mbErr.Limit))
				return
			}
			// Non-size parse error — continue with empty metadata;
			// chatHandler will surface the real problem.
			slog.Debug("dispatch: body sniff error (continuing)", "error", derr.Error())
		}
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
		// F-41（2026-11-13）user_content 写面：v1 包装器此前无人写入，
		// governance_prompt_injection hook 与 security plugins 的
		// user_content 消费面全部空转（唯一写入点在 /v2/* 命名空间）。
		// 取最后一条 user 消息（注入检测面向「当前这条用户输入」）。
		// 泄漏面审查（§全面审计v3 02 F-41 修复前提）：① 事件面契约
		// （event_fields.go）明确剥离 user_content；② 全仓无 env.Metadata
		// 全量 dump 进日志的写点（本轮逐点排查留痕）⇒ 写入仅限进程内
		// hook 消费；③ 大小上限 64KiB（detector 侧另有 policy.MaxInputLength
		// 截断），避免多 MB 请求把 Metadata 撑爆。
		if userContentEnabled() {
			if uc := userContentFromBody(rawBody); uc != "" {
				env.Metadata["user_content"] = uc
			}
		}
		if setForceCompressionMetadata(env.Metadata, r.Header.Get("X-Gw-Force-Compression")) {
			if messages := compressionMessagesFromBody(rawBody); len(messages) > 0 {
				env.Metadata[compression.MetaKeyMessages] = messages
			}
		}

		rawKey := pipelineAPIKey(r)
		if rawKey != "" {
			env.Metadata["api_key"] = rawKey
		}

		// 2026-08-23: read the X-LLMGW-Preferred-Credential routing
		// override. The header is gated on the admin token (carried in a
		// separate X-LLMGW-Admin-Token header) so only authenticated
		// operators can pin a credential on a request. The chat request
		// itself still authenticates with a normal client key; the admin
		// token only unlocks the pin. The body metadata field (OpenAI
		// metadata / Anthropic messages.metadata / Responses Extra) is
		// extracted from rawBody and merged into the same key. Either
		// source populates env.Metadata["preferred_credential"], which the
		// v2 routing StickyRouter already consumes.
		if pref := streaming.ExtractPreferredCredential(
			r.Header.Get(streaming.PreferredCredentialHeader),
			rawBody,
			r.Header.Get(streaming.PreferredCredentialAdminTokenHeader),
			deps.AdminAPIKey,
		); pref != "" {
			env.Metadata["preferred_credential"] = pref
			env.Metadata["preferred_credential_source"] = "admin"
		}
		if deps.KeyVerifier != nil && deps.KeyVerifier.Enabled() {
			if rawKey == "" {
				if strings.TrimSpace(r.Header.Get("X-Tenant-ID")) != "" {
					http.Error(w, "authentication required", http.StatusUnauthorized)
					return
				}
			} else {
				keyInfo, verifyErr := deps.KeyVerifier.Verify(ctx, rawKey)
				if verifyErr != nil {
					http.Error(w, "authentication failed", http.StatusUnauthorized)
					return
				}
				tenantID, tenantErr := validatePipelineTenantHeader(r.Header.Get("X-Tenant-ID"), keyInfo)
				if tenantErr != nil {
					status := http.StatusUnauthorized
					if _, ok := tenantErr.(*tenantHeaderMismatchError); ok {
						status = http.StatusForbidden
					}
					http.Error(w, tenantErr.Error(), status)
					return
				}
				env.TenantID = tenantID
				env.APIKey = &domain.PipelineAPIKey{
					ID:       fmt.Sprintf("%d", keyInfo.ID),
					Key:      rawKey,
					TenantID: tenantID,
					Enabled:  true,
				}
				env.Authenticated = true
			}
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

		// ★ 2026-11-18 审计轮 P0：治理决策**收口**。
		//
		//	拦截决策原本只由 governance_interception stage 产出，而该
		//	stage 注册序在 provider_discovery 之后 ⇒ 对真实流量
		//	（provider_discovery 挂在内存 demo 凭据仓上，对任何真实模型
		//	都报「no healthy provider」⇒ 管线在此中止）**永远到不了
		//	决策闸** ⇒ 中止前已经写下的 deny verdict（PI 检测、legacy
		//	security 的 critical threat）被丢弃 ⇒ 拦截形同虚设。
		//
		//	⇒ 这里在管线中止后补做一次决策：若 Decision 尚未产出，就用
		//	  已收集到的 verdicts 产出一次（与 stage 同一个引擎实例、
		//	  同一套阈值/规则）。
		if env.Governance == nil || env.Governance.Decision == nil {
			if deps.InterceptEngine != nil {
				if _, derr := deps.InterceptEngine.Decide(ctx, env); derr != nil {
					slog.Warn("v2 dispatch: governance decision failed (falling through)",
						"request_id", requestID, "error", derr)
				}
			}
		}

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
			// 2026-11-23：补归因字段（decision_reason / 来源插件 / verdicts
			// 摘要）——此前只记 decision_kind，线上 403 无法归因「谁挡的」，
			// 且容器重启后日志滚走事后补不回来（拦截不落审计表）。
			// 归因只取形态字段（plugin/allow/severity/code/reason），
			// **不含 evidence**（命中片段等用户内容不进日志）。
			gateLogFields := []any{
				"request_id", requestID,
				"path", r.URL.Path,
				"status", code,
				"decision_kind", env.Governance.Decision.Kind,
			}
			gateLogFields = append(gateLogFields,
				interception.AttributionLogFields(interception.AttributeDecision(env))...)
			slog.Info("v2 dispatch gate: short-circuit", gateLogFields...)
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

		// ★ 2026-11-18 审计轮 P0：PI 的拒绝判定必须真正生效。
		//
		//	此前 PI hook 的 OnError 只设 env.StatusCode（403/410/202），
		//	而本文件**从不读它**（全仓只有 cmd/gateway-v2/main.go 读）
		//	⇒ PI 判定为 reject/block 的请求照样落到下面的 fallback
		//	  handler 被正常服务 ⇒ 拦截形同虚设。
		//	⇒ 这里消费 PI 留下的显式判定标记，在落到 fallback **之前**
		//	  写响应并终止。
		//
		//	顺序说明：本闸在**治理决策闸之后**——
		//	· 治理决策（Decision=Block/Suspend）优先，它覆盖所有来源的
		//	  deny verdict（含 PI 的），语义更完整（挂起/审批/封禁）；
		//	· 治理决策为 Continue 时（例如 PI 判 medium 风险 ⇒ severity=1
		//	  低于 BlockThreshold=2），本闸兜住 PI 自己的拒绝结论。
		if code, blocked := promptInjectionGate(env); blocked {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":      "request rejected by prompt injection detection",
				"request_id": requestID,
				"verdict":    env.Metadata["prompt_injection_verdict"],
			})
			slog.Info("v2 dispatch: prompt injection gate short-circuit",
				"request_id", requestID,
				"path", r.URL.Path,
				"status", code,
				"verdict", env.Metadata["prompt_injection_verdict"],
			)
			return
		}

		fallback.ServeHTTP(w, r)

		// Postflight: best-effort metrics stamping. We can't append
		// to the response (it may already be flushed for streaming
		// responses), so we only update internal env state for the
		// audit/metrics hooks to consume.
		env.StatusCode = 0 // writer's status is unknown; left for a later phase

		// Phase 2 Step 3: 发布 request.completed.v1 事件（契约完整版）
		// 使用 extractRequestCompletedPayload() 补全 10 个缺失字段，
		// 移除 user_content 违规字段。
		//
		// 只在 deps.Publisher 非 nil 时执行（即 EnableAnalysis=true 且
		// 注入了 DB pool）。失败仅记录日志，不影响主流程。
		//
		// Refs: docs/omni-ref2/03-GATEWAY-EVENT-FIELD-MAPPING.md
		//       docs/修订0811/08-Phase2-进度报告.md Step 3
		if deps.Publisher != nil && env.TenantID != "" && r.Header.Get("X-Gateway-Internal-Purpose") != "session-analysis" {
			// Extract contract-compliant payload (11 fields, no user_content)
			payload := extractRequestCompletedPayload(env, requestID, env.CreatedAt)

			evt := analysis.AnalysisEvent{
				EventID:    "evt-" + requestID,
				Type:       analysis.EventRequestCompleted,
				TenantID:   env.TenantID,
				SessionID:  env.SessionID,
				RequestID:  requestID,
				OccurredAt: time.Now(),
				Payload:    payload,
			}
			if err := deps.Publisher.Publish(ctx, evt); err != nil {
				slog.Warn("v2 dispatch: publish request.completed failed",
					"request_id", requestID, "error", err)
			}
		}
	})
}

// promptInjectionGate 读取 PI 检测 hook 留下的拒绝判定。
//
// ★ 2026-11-18 审计轮 P0：这是 PI 判定从「hook 内部状态」变成
//
//	「客户端可见的 HTTP 状态」的**唯一**出口。
//
//	返回 (statusCode, blocked)：
//	  - blocked=false ⇒ 没有 PI 拒绝判定 ⇒ 调用方继续正常处理；
//	  - blocked=true  ⇒ 本请求被 PI 判定为拒绝/待审批/会话终止，
//	    statusCode 是应当回给客户端的状态码（403/410/202）。
//
//	判据用**显式标记**（prompt_injection_blocked /
//	prompt_injection_verdict）而不是 env.StatusCode：
//	env.StatusCode 是通用字段（其它阶段也可能写），用它做判据等于
//	依赖「只有 PI 会写它」这个隐含约定。
func promptInjectionGate(env *domain.PipelineRequest) (int, bool) {
	if env == nil || env.Metadata == nil {
		return 0, false
	}
	blocked, _ := env.Metadata["prompt_injection_blocked"].(bool)
	if !blocked {
		return 0, false
	}
	code := env.StatusCode
	if code == 0 {
		// 兜底：PI 标记了拒绝但没给状态码 ⇒ 按拒绝处理。
		code = http.StatusForbidden
	}
	return code, true
}

// setForceCompressionMetadata maps the client opt-in header to the existing
// compression hook contract. Only an explicit true value activates it.
func setForceCompressionMetadata(metadata map[string]any, header string) bool {
	if metadata == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(header), "true") {
		return false
	}
	metadata[compression.MetaKeyNeedsCompression] = true
	return true
}

func compressionMessagesFromBody(body []byte) []compression.Message {
	var request struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return nil
	}
	messages := make([]compression.Message, 0, len(request.Messages))
	for _, message := range request.Messages {
		if message.Role != "" && message.Content != "" {
			messages = append(messages, compression.Message{Role: message.Role, Content: message.Content})
		}
	}
	return messages
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

type tenantHeaderMismatchError struct{}

func (e *tenantHeaderMismatchError) Error() string {
	return "X-Tenant-ID does not match authenticated tenant"
}

func validatePipelineTenantHeader(header string, keyInfo *authentication.KeyInfo) (string, error) {
	if keyInfo == nil || strings.TrimSpace(keyInfo.TenantID) == "" {
		return "", fmt.Errorf("authenticated tenant is missing")
	}
	tenantID := strings.TrimSpace(keyInfo.TenantID)
	requestedTenant := strings.TrimSpace(header)
	if requestedTenant != "" && requestedTenant != tenantID {
		return "", &tenantHeaderMismatchError{}
	}
	return tenantID, nil
}

func pipelineAPIKey(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(auth, "bearer "))
	}
	return strings.TrimSpace(r.Header.Get("X-API-Key"))
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
func v2DispatchMux(chatHandler, messagesHandler, responsesHandler http.Handler, swEngine *sensitive.SensitiveWordEngine) (*http.ServeMux, *v2DispatchDeps, bool) {
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

	keyVerifier := (*authentication.KeyVerifier)(nil)
	if ch != nil {
		keyVerifier = ch.AuthKeyVerifier()
	}
	deps := newV2DispatchDepsFromMain(cfg, ch, keyVerifier, swEngine)

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

		// 2026-08-20: 项目归属（LLM 推断）hook 装配。
		//
		// 三个前置条件同时满足才挂载：
		//   1. settings.GetPlatformBool("project_attribution.enabled")=true（默认 false）
		//   2. PG pool 可用（推断需要查 session_dim / project_dim / _request_logs）
		//   3. resolver 非 nil（依赖 deps.ProjectAttrResolver；由 main.go 装配）
		//
		// 任一条件不满足都静默跳过——本轮目标是默认关闭、显式开启，而不是
		// "悄悄挂载"。这避免 "settings flag 打开但 resolver 未配置" 这种
		// 隐性失败。
		if settings.GetPlatformBool("project_attribution.enabled", false) &&
			deps.PGDBPool != nil &&
			deps.ProjectAttrResolver != nil {
			store := projectattr.NewPGStoreFromPool(deps.PGDBPool)
			hook := projectattr.NewCloseHook(store, nil, slog.Default())
			// 注入一个 BuildAttributor 工厂：每次 hook 触发时按 tenant
			// 现取快照。Attributor 在 BuildAttributor 内组装（带 inherit，
			// 不带 LLM——计划要求本轮不启用模型兜底）。
			hook.AttributorFor = func(ctx context.Context, tenantID, gwSessionID string) (*projectattr.Attributor, error) {
				return deps.ProjectAttrResolver.BuildAttributor(ctx, tenantID)
			}
			sumWorker.AddCloseHook(hook)
			slog.Info("v2 pipeline: project attribution hook enabled")
		} else {
			slog.Info("v2 pipeline: project attribution hook disabled",
				"flag_on", settings.GetPlatformBool("project_attribution.enabled", false),
				"pg_ready", deps.PGDBPool != nil,
				"resolver_ready", deps.ProjectAttrResolver != nil)
		}

		// Session analysis metadata final UPSERT (migration 567 §7).
		// 2026-08-27: d2cbaf88b restored this block from origin/main but
		// sessionsummary.NewRequestLogsMessageSource was removed when v2
		// session_bodies became the only source. Fold the flag — v2 is the
		// sole supported path; the flag is still honored by the compression
		// layer below for read-source selection.
		if deps.PGDBPool != nil {
			msgSource := sessionsummary.NewV2SessionBodiesSource(deps.PGDBPool)
			metaHook := workers.NewSessionMetadataCloseHook(
				sessionmeta.NewMetadataStore(deps.PGDBPool),
				msgSource,
				slog.Default(),
			)
			sumWorker.AddCloseHook(metaHook)
			slog.Info("v2 pipeline: session metadata final close hook enabled")
		}

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
	// ★ 2026-11-18 根修（245 生产实证）：**不再调用 deps.Publisher.Close()**。
	// 旧代码在这里调 Publisher.Close——但 V2 块构造的 PGPublisher 挂在
	// **共享主 pool** 上（main.go V2 块 `bus.NewPGPublisher(pool, ...)`），
	// 其 Close 的唯一副作用就是把主 pool 整个关掉：
	//   · boot 期出错走 defer 清理（如 panic 展开）时，pgxpool.Close
	//     （puddle）等待在途连接归还 ⇒ 卡死 ⇒ panic 冻结在 defer 里 ⇒
	//     进程僵尸化（监听从未绑定、systemd 不拉起）——245 事故实证；
	//   · 正常停机时也会把主 pool 提前关掉，与本函数旧注释「pool 由
	//     dbConn 管」的所有权矛盾。
	// PGPublisher 自身没有 pool 之外的资源需要释放（bus/publisher.go:98）。
	// 若未来 Publisher 获得真正的自有资源，应让 V2 块传入**独占 pool** 的
	// 实例，再恢复这里的 Close（并配僵尸化回归钉测）。
	for _, sqlDB := range deps.AnalysisSQLDBs {
		if sqlDB != nil {
			closeAnalysisSQLDBBounded(sqlDB, analysisSQLDBCloseTimeout)
		}
	}
	deps.AnalysisSQLDBs = nil
	if deps.AuditWriter == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = ctx
	_ = deps.AuditWriter.Close()
}

// analysisSQLDBCloseTimeout 是关闭 analysis *sql.DB 的有界等待上限。
//
// ★ 2026-11-18 根修（245 生产实证）：boot 期一旦主流程出错走 defer 清理
// （例如 panic 展开），后台任务（PI 检测器规则加载 / 采集器）仍持有连接 ⇒
// 无界等待把「崩溃 + systemd 自动拉起」变成「僵尸进程」：pool 已关、
// 监听从未绑定、进程不退出。有界关闭让清理路径必然返回。
//
// ★ 2026-11-18 审计轮订正（假根因）：本注释此前断言「*sql.DB.Close 会等
//
//	所有在途连接归还（底层 puddle Pool.Close 卡 WaitGroup.Wait）」——
//	**这是错的**，且本仓自己的钉测文件已写明相反事实
//	（cmd/gateway/v2_dispatch_registration_guard_test.go:「stdlib *sql.DB.Close
//	不等待在途连接归还；这里的有界包装是**纵深防御**」）。
//	实测：Go 1.27.4 stdlib database/sql.(*DB).Close 只关空闲连接、置 closed、
//	stop()，**不等在途查询**；本仓走 stdlib.OpenDB → pgx.ConnectConfig
//	（裸 pgx，**不经 pgxpool/puddle**）。
//	⇒ 「僵尸进程」的真实机制在 PGPublisher.Close() → pool.Close()
//	（domains/analysis/bus/publisher.go）——那一半是对的；
//	★ 而这里的 *sql.DB 有界包装是**纵深防御**（未来换驱动实现时兜底），
//	不是本次根因的必要条件。保留该包装，但不再把它说成根因。
const analysisSQLDBCloseTimeout = 5 * time.Second

// closeAnalysisSQLDBBounded 有界关闭一个 analysis *sql.DB：超时放弃并留痕
// （连接随进程退出由 OS 回收；比卡死整个 shutdown 路径代价小得多）。
func closeAnalysisSQLDBBounded(db *sql.DB, timeout time.Duration) {
	if db == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = db.Close()
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		slog.Warn("v2 pipeline: analysis sqlDB close timed out (in-flight connection held); giving up",
			"timeout", timeout.String())
	}
}

var lookupAnalysisIP = net.LookupIP

func validateAnalysisEndpoint(rawURL string, allowInsecureLocal bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Hostname() == "" {
		return "", fmt.Errorf("invalid analysis endpoint")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("analysis endpoint must not contain credentials, query, or fragment")
	}
	host := strings.ToLower(parsed.Hostname())
	if allowInsecureLocal && parsed.Scheme == "http" && (host == "localhost" || host == "127.0.0.1" || host == "::1") {
		return strings.TrimRight(parsed.String(), "/"), nil
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("analysis endpoint must use https")
	}
	addresses, err := lookupAnalysisIP(host)
	if err != nil || len(addresses) == 0 {
		return "", fmt.Errorf("analysis endpoint hostname cannot be resolved")
	}
	for _, address := range addresses {
		if address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsUnspecified() {
			return "", fmt.Errorf("analysis endpoint resolves to a non-public address")
		}
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

type sessionSummaryLLMAdapter struct {
	client *sessionanalytics.OpenAIClient
}

func (a sessionSummaryLLMAdapter) Complete(ctx context.Context, prompt string, opts ...sessionsummary.CompletionOption) (string, error) {
	config := sessionsummary.CompletionConfig{Model: "summary-fast"}
	for _, option := range opts {
		option(&config)
	}
	return a.client.CompleteWithConfig(ctx, prompt, sessionanalytics.CompletionRequest{
		Model:        config.Model,
		MaxTokens:    config.MaxTokens,
		Temperature:  config.Temperature,
		SystemPrompt: config.SystemPrompt,
	})
}

type sessionSummaryWorkerAdapter struct {
	summarizer *sessionsummary.Summarizer
}

func (a sessionSummaryWorkerAdapter) GenerateSummary(ctx context.Context, tenantID, sessionKey string) (any, error) {
	return a.summarizer.GenerateRollingSummary(ctx, tenantID, sessionKey)
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
	redisClient *redis.Client,
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
		// F-41（2026-11-13）：stage 已无条件注册，detector 到场后经
		// PromptInjectionHook 延迟绑定补上（仅更新本 deps 的 hook；
		// buildV2DispatchPipeline 构建时刻捕获的 nil 无法感知此次注入）。
		if deps.PromptInjectionHook != nil {
			deps.PromptInjectionHook.BindDetector(detector)
		}
	}
	if checker != nil {
		deps.OutputComplianceChecker = checker
	}
	if summarizer != nil {
		deps.SessionSummarizer = summarizer
	}

	// 增强版提示词注入检测插件初始化
	// Plugin 是薄适配层，内部复用核心 promptinjection.Detector。
	// 这里把 pgxpool 桥接为 *sql.DB，交给核心 Detector。
	if pool != nil && deps.EnhancedPIPlugin != nil {
		sqlDB := stdlib.OpenDB(*pool.Config().ConnConfig)
		deps.AnalysisSQLDBs = append(deps.AnalysisSQLDBs, sqlDB)
		deps.EnhancedPIPlugin.Init(sqlDB)
		slog.Info("enhanced prompt injection plugin initialized (wraps core Detector)")
	}

	// Phase 4: 会话全景分析引擎注入。
	// 仅当 session_analytics.enabled 且 pool 可用时构建引擎。
	// 通过 PhasePostResponse hook 接入请求管道，准实时分析。
	// 2026-11-20 闭环审计轮：LLM 客户端/stage 配置构造收敛到 newAnalysisLLMClient
	// （与独立闭链共用）；resolver 换 sessionAnalyticsAutoResolver——此前的
	// DefaultModelResolver 返回 "summary-fast"/"intent-fast" 等内置别名，
	// 而全仓不存在这些别名的解析表 ⇒ 别名被原样发往分析端点必失败。
	if pool != nil && sessionanalytics.NewLLMStageConfig(nil).Enabled() {
		analysisClient, cfg := newAnalysisLLMClient()
		if analysisClient != nil && deps.SessionSummarizer == nil {
			// 2026-08-06: pass *pgxpool.Pool directly to NewSummarizer
			// instead of bridging to *sql.DB. The summarizer now
			// uses summarystore.Upsert for the write path and
			// pgxpool for reads — no more sql.DB involvement in
			// the v2 dispatch path.
			summaryService := sessionsummary.NewSummarizer(pool, redisClient, sessionSummaryLLMAdapter{client: analysisClient})
			summaryService.SetModel(normalizeAnalysisModelAlias(cfg.ModelFor(sessionanalytics.StageSummary)))
			// docs/omni-ref3 A1 (decision: compress+summary cut over
			// together, default-on, same kill-switch as compression read):
			// read session messages from public.session_bodies instead of
			// request_logs. Same platform flag as shouldUseV2, default true,
			// hot-reloadable to false to revert to V1 reads.
			if settings.GetPlatformBool("sessions_v2_compression_read", true) {
				// 24h 审计第二轮 B#4：默认仍走 V2 源；per-turn digest
				// 层（sessions_summary_per_turn_digest，默认关）开启时
				// 摘要输入改为逐轮 user+assistant 人读摘要，关闭时该源
				// 逐字节委托 V2 源，行为不变。
				summaryService.SetMessageSource(sessionsummary.NewPerTurnDigestSource(pool))
			}
			deps.SessionSummarizer = sessionSummaryWorkerAdapter{summarizer: summaryService}
		}
		analyticsDB := sessionanalytics.NewPoolDB(pool)
		engines := &sessionanalysis.Engines{
			RequestSummarizer: sessionanalytics.NewRequestSummarizer(analyticsDB, cfg, analysisClient, slog.Default()),
			Tagger:            sessionanalytics.NewSessionTagger(analyticsDB, cfg, slog.Default()),
		}
		deps.SessionAnalysisEngines = engines
		deps.SessionAnalysisConfig = cfg

		// 注入 ClusterRunner（手动触发聚类用）
		admin.SetClusterRunner(sessionanalytics.NewSessionClusterer(analyticsDB, cfg, analysisClient, slog.Default()))

		// 2026-11-20 闭环审计轮（P1）：ProjectAttrResolver 生产装配。
		// 此前该字段全仓没有任何赋值点 ⇒ project_attribution.enabled 打开后
		// 挂载条件（flag && pool && resolver）第三条恒 false，三级项目归属
		// 推断（rule→inherit→llm）生产不可达。
		if deps.ProjectAttrResolver == nil {
			deps.ProjectAttrResolver = projectattr.NewProjectResolver(projectattr.NewPGStoreFromPool(pool))
		}
	}
}

// writePayloadTooLarge returns a 413 with a stable error code so the
// frontend can distinguish dispatch-cap rejections from upstream 4xx.
// 2026-08-26 (P1-20 fix): before this helper existed the dispatch path
// silently truncated at MaxDispatchBodyBytes via io.LimitReader.
func writePayloadTooLarge(w http.ResponseWriter, limit int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusRequestEntityTooLarge)
	_, _ = w.Write([]byte(fmt.Sprintf(
		`{"error":"request body exceeds %d bytes","code":"dispatch.body_too_large"}`,
		limit)))
}
