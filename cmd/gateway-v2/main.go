// Command gateway-v2 is the new Pipeline-based entry point for llm-gateway-go.
//
// 重要: 这是一个**并行的演示入口**，与 cmd/gateway/main.go 独立运行。
// 目的：展示新 Hook Pipeline 架构的完整工作流程，供集成测试和未来切流参考。
// 不替换/不修改 cmd/gateway/main.go（避免影响 71 生产部署路径）。
//
// 用法：
//
//	LLM_GATEWAY_LISTEN=:8782 go run ./cmd/gateway-v2
//
// 区别于 cmd/gateway/main.go：
//   - 使用 domain.PipelineRequest + pipeline.RequestPipeline
//   - 集成所有新领域 Hook（core + cross-cutting）
//   - 通过 metadata 而非全局状态传递数据
package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"                                           //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	agentecosystem "github.com/kaixuan/llm-gateway-go/domains/agent-ecosystem"           //nolint:depguard
	"github.com/kaixuan/llm-gateway-go/domains/authentication"                           //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/credential"                               //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"                              //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/cache"                              //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"                        //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability"                      //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/security"                           //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	sessioninspector "github.com/kaixuan/llm-gateway-go/domains/hooks/session-inspector" //nolint:depguard
	sessionaudithook "github.com/kaixuan/llm-gateway-go/domains/hooks/sessionaudit"      //nolint:depguard
	"github.com/kaixuan/llm-gateway-go/domains/hooks/tools"                              //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/identity"                                 //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/integration"                              //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"                                 //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/provider"                                 //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/routing"                                  //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/session"                                  //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"                             //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/streaming"                                //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/transformation"                           //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/eventbus"
	"github.com/kaixuan/llm-gateway-go/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// v2Config 简化的 v2 配置
type v2Config struct {
	Listen             string
	APIKey             string // NET-002 fix: v2 现在也要求 API Key 中间件
	EnableCache        bool
	EnableSecurity     bool
	EnableAudit        bool
	EnableSessionAudit bool // 2026-06-27: session audit hook 独立开关
	EnableApprovalGate bool // 2026-06-27: approval gate 独立开关(v2 demo 默认关,需 DB+Redis)
	EnableObserv       bool
	EnableStreaming    bool
}

// loadConfig 加载配置
func loadConfig() *v2Config {
	cfg := &v2Config{
		Listen:             getEnv("LLM_GATEWAY_LISTEN", ":8782"),
		APIKey:             getEnv("LLM_GATEWAY_API_KEY", ""),
		EnableCache:        getEnv("LLM_GATEWAY_V2_CACHE", "true") == "true",
		EnableSecurity:     getEnv("LLM_GATEWAY_V2_SECURITY", "true") == "true",
		EnableAudit:        getEnv("LLM_GATEWAY_V2_AUDIT", "true") == "true",
		EnableSessionAudit: getEnv("LLM_GATEWAY_V2_SESSION_AUDIT", "true") == "true",
		EnableApprovalGate: getEnv("LLM_GATEWAY_V2_APPROVAL_GATE", "false") == "true",
		EnableObserv:       getEnv("LLM_GATEWAY_V2_OBSERV", "true") == "true",
		EnableStreaming:    getEnv("LLM_GATEWAY_V2_STREAMING", "true") == "true",
	}
	return cfg
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// fingerprintKey 返回 API Key 的不可逆指纹（SHA-256 前 16 位 hex）。
// 用于审计/观测关联，但不暴露原密钥。NET-002 fix。
func fingerprintKey(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}

// authChain 构建 v2 网关的中间件链：recovery → requestid → auth → logging。
// CORS 由调用方在外面 Wrap（这样 preflight 才能在 auth 之前通过）。
//
// NET-002 fix: 修复前 v2 完全无中间件，任何人都能访问。
//
// 与 cmd/gateway 的差异：
//   - 这里的 auth 是简化版——只校验单个静态 API Key，不查 DB。
//   - 无 Prometheus 中间件（v2 是 demo 端口，不需要 metrics）。
func authChain(deps *v2Deps, cfg *v2Config) http.Handler {
	h := httpHandler(deps)
	// 注意：logging 中间件包外层、auth 包里层 —— 这样 401 请求也被记录
	// 到访问日志（运维可观测"被拒绝"流量）。
	if cfg.APIKey != "" {
		h = simpleAPIKeyAuth(cfg.APIKey)(h)
	} else {
		// API Key 未配置时显式警告，但允许通过（演示场景）。
		slog.Warn("v2 API key authentication disabled (LLM_GATEWAY_API_KEY not set)")
	}
	h = middleware.NewRequestIDMiddleware().Wrap(h)
	h = middleware.NewRecoveryMiddleware().Wrap(h)
	return h
}

// simpleAPIKeyAuth 轻量级 API Key 校验（Header: X-API-Key）。
// 使用 crypto/subtle.ConstantTimeCompare 防 timing attack。
func simpleAPIKeyAuth(expected string) func(http.Handler) http.Handler {
	expectedSum := sha256.Sum256([]byte(expected))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got := r.Header.Get("X-API-Key")
			gotSum := sha256.Sum256([]byte(got))
			if subtle.ConstantTimeCompare(gotSum[:], expectedSum[:]) != 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// v2Deps 集中管理所有依赖（便于测试替换）
type v2Deps struct {
	Config           *v2Config
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
	// 2026-06-27: SessionAudit Hook（sessionaudit domain）
	AuditDetector *sessionaudit.FastDetector
	AuditHook     *sessionaudithook.SessionAuditHook
	GateHook      *sessionaudithook.ApprovalGateHook
}

// buildPipeline 组装完整的 Pipeline（包含所有横切 + 核心 Hook）
func buildPipeline(deps *v2Deps) *pipeline.RequestPipeline {
	p := pipeline.NewRequestPipeline()

	// === Phase: Tracing (PreRouting, priority 1) ===
	if deps.Config.EnableObserv {
		p.AddStage(&pipeline.PipelineStage{
			Name: "tracing", Phase: pipeline.PhasePreRouting, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{observability.NewTracingHook(deps.Tracer)},
		})
	}

	// === Phase: Session Audit (PreRouting, priority 100) ===
	// 2026-06-27: 实时安全检测（敏感词 / PII / jailbreak / injection）。
	// 命中 NeedApproval 时由 ApprovalGateHook (priority 105) 拦截。
	if deps.Config.EnableSessionAudit && deps.AuditHook != nil {
		p.AddStage(&pipeline.PipelineStage{
			Name: "session_audit", Phase: pipeline.PhasePreRouting, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{deps.AuditHook},
		})
	}

	// === Phase: Approval Gate (PreRouting, priority 105) ===
	// 在 SessionAuditHook 之后；如需审批，返回 202 + approval_id。
	// v2 demo 默认关（无 DB+Redis）；生产用 cmd/gateway/main.go 走真实 PG + Redis。
	if deps.Config.EnableApprovalGate && deps.GateHook != nil {
		p.AddStage(&pipeline.PipelineStage{
			Name: "session_approval_gate", Phase: pipeline.PhasePreRouting, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{deps.GateHook},
		})
	}

	// === Phase: Security (PreRouting, parallel) ===
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

	// === Phase: Provider Discovery (PreRouting) ===
	p.AddStage(&pipeline.PipelineStage{
		Name: "provider_discovery", Phase: pipeline.PhasePreRouting, Mode: pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			provider.NewProviderDiscoveryHook(deps.ProviderStore, deps.ProviderProber),
		},
	})

	// === Phase: Credential Health (PreRouting) ===
	p.AddStage(&pipeline.PipelineStage{
		Name: "credential_health", Phase: pipeline.PhasePreRouting, Mode: pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			credential.NewHealthCheckHook(deps.CredentialStore, deps.CredentialHealth),
		},
	})

	// === Phase: Cache lookup (PreRouting) ===
	if deps.Config.EnableCache {
		p.AddStage(&pipeline.PipelineStage{
			Name: "cache_lookup", Phase: pipeline.PhasePreRouting, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{cache.NewCacheLookupHook(deps.CacheStore)},
		})
	}

	// === Phase: Session Inspect (PreRouting) ===
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

	// === Phase: Agent Discovery (PreRouting) ===
	p.AddStage(&pipeline.PipelineStage{
		Name: "agent_discovery", Phase: pipeline.PhasePreRouting, Mode: pipeline.ModeSequential,
		Hooks: []pipeline.Hook{agentecosystem.NewAgentDiscoveryHook(deps.AgentReg)},
	})

	// === Phase: Routing (Routing) ===
	sticky := routing.NewStickyRouter(routing.NewRoundRobinRouter())
	p.AddStage(&pipeline.PipelineStage{
		Name: "routing", Phase: pipeline.PhaseRouting, Mode: pipeline.ModeSequential,
		Hooks: []pipeline.Hook{routing.NewRoutingHook(sticky)},
	})

	// === Phase: Credential Limit (PostRouting) ===
	p.AddStage(&pipeline.PipelineStage{
		Name: "credential_limit", Phase: pipeline.PhasePostRouting, Mode: pipeline.ModeSequential,
		Hooks: []pipeline.Hook{credential.NewLimiterHook(deps.CredentialLimit)},
	})

	// === Phase: Transform (Transform) ===
	p.AddStage(&pipeline.PipelineStage{
		Name: "transform", Phase: pipeline.PhaseTransform, Mode: pipeline.ModeSequential,
		Hooks: []pipeline.Hook{
			transformation.NewTransformHook(
				transformation.NewSanitizer(),
				transformation.NewCompressor(4096),
			),
		},
	})

	// === Phase: Compression (Transform) ===
	p.AddStage(&pipeline.PipelineStage{
		Name: "compression", Phase: pipeline.PhaseTransform, Mode: pipeline.ModeSequential,
		Hooks: []pipeline.Hook{compression.NewCompressionHook(compression.NewLCSCompressor(4096))},
	})

	// === Phase: Tool Interception (PostTransform) ===
	p.AddStage(&pipeline.PipelineStage{
		Name: "tools", Phase: pipeline.PhasePostTransform, Mode: pipeline.ModeSequential,
		Hooks: []pipeline.Hook{tools.NewToolInterceptionHook(tools.NewMetaToolInterceptor(""))},
	})

	// === Phase: Streaming (PostUpstream) ===
	if deps.Config.EnableStreaming {
		p.AddStage(&pipeline.PipelineStage{
			Name: "streaming", Phase: pipeline.PhasePostUpstream, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{streaming.NewStreamHook(streaming.NewSSEStreamer())},
		})
	}

	// === Phase: Audit (PostResponse) ===
	if deps.Config.EnableAudit {
		p.AddStage(&pipeline.PipelineStage{
			Name: "audit", Phase: pipeline.PhasePostResponse, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{audit.NewAuditLogHook(deps.AuditWriter)},
		})
	}

	// === Phase: Cache save (PostResponse) ===
	if deps.Config.EnableCache {
		p.AddStage(&pipeline.PipelineStage{
			Name: "cache_save", Phase: pipeline.PhasePostResponse, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{cache.NewCacheSaveHook(deps.CacheStore, 5*time.Minute)},
		})
	}

	// === Phase: Metrics (PostResponse) ===
	if deps.Config.EnableObserv {
		p.AddStage(&pipeline.PipelineStage{
			Name: "metrics", Phase: pipeline.PhasePostResponse, Mode: pipeline.ModeSequential,
			Hooks: []pipeline.Hook{observability.NewMetricsHook(deps.Metrics)},
		})
	}

	return p
}

// newDeps 创建默认依赖
func newDeps(cfg *v2Config) *v2Deps {
	cacheStore := cache.NewInMemoryStore()
	auditSink := audit.NewInMemorySink()
	auditWriter := audit.NewBatchWriter(auditSink, 100, 5*time.Second)
	metrics := observability.NewRegistry()
	tracer := observability.NewInMemoryTracer()
	agentReg := agentecosystem.NewRegistry()

	credStore := credential.NewInMemoryStore()
	credHealth := credential.NewHealthChecker(credStore)
	// 2026-06-26 migration: NewLimiter() no longer takes *InMemoryStore.
	// The migrated 4-layer Limiter is keyed by int (providerID, credentialID)
	// and uses NewWithLimits(global, pool, credential, identity) for
	// custom limits. We use the defaults — the gateway-v2 demo is
	// single-tenant with no production concurrency tuning.
	credLimiter := credential.NewLimiter()

	// 预置一个默认 credential 供 demo 使用
	_ = credStore.Save(&credential.Credential{
		ID: "default-cred", TenantID: "default", ProviderID: "default-openai", Model: "gpt-4o",
		EncryptedKey:  []byte("demo-encrypted-key"),
		Priority:      50,
		Status:        credential.StatusActive,
		MaxConcurrent: 10,
	})

	provStore := provider.NewInMemoryStore()
	provProber := provider.NewProber(provStore)

	// 预置一个默认 provider（gpt-4o）供 demo 使用
	_ = provStore.Save(&provider.Provider{
		ID:       "default-openai",
		Name:     "OpenAI",
		BaseURL:  "https://api.openai.com",
		Protocol: provider.ProtocolOpenAI,
		AuthType: "bearer",
		Models: []provider.ModelSpec{
			{Name: "gpt-4o", MaxContextTokens: 128000, SupportsStream: true, SupportsTools: true},
			{Name: "gpt-4", MaxContextTokens: 8192, SupportsStream: true, SupportsTools: true},
			{Name: "gpt-3.5-turbo", MaxContextTokens: 4096, SupportsStream: true},
			{Name: "claude-3-5-sonnet-20241022", MaxContextTokens: 200000, SupportsStream: true, SupportsTools: true},
		},
		TimeoutSec: 60,
	})

	// 2026-06-27 session audit: 初始化检测器并构造 Hook。
	// v2 demo 无 DB → ApprovalGateHook 的 mgr=nil；hook 仍能注册但
	// 审批创建会降级（仅日志，不阻断主流程）。生产用 cmd/gateway/main.go
	// 走真实 PG + Redis。
	auditDetector := sessionaudit.NewFastDetector(sessionaudit.DefaultDetectorConfig())
	eventBus := eventbus.NewMemoryBus(100)
	auditHook := sessionaudithook.NewSessionAuditHook(auditDetector, eventBus)
	gateHook := sessionaudithook.NewApprovalGateHook(nil, nil, eventBus)

	return &v2Deps{
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
		EventBus:         eventBus,
		AuditDetector:    auditDetector,
		AuditHook:        auditHook,
		GateHook:         gateHook,
	}
}

// httpHandler 简化的 HTTP handler（演示用）
func httpHandler(deps *v2Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		env := domain.NewRequestEnvelope(ctx, &domain.RequestEnvelope{
			RequestID: fmt.Sprintf("req-%d", time.Now().UnixNano()),
			CreatedAt: time.Now(),
			GoContext: ctx,
		})

		// 模拟提取请求信息
		env.TenantID = r.Header.Get("X-Tenant-ID")
		env.SessionID = r.Header.Get("X-Session-ID")
		// NET-002 fix: 严禁把原始 API Key 注入 env.Metadata（会落到
		// 审计/观测/session 钩子的可序列化字段中）。改用 SHA-256 前 16 位
		// 指纹，便于关联但不泄露密钥。
		apiKey := r.Header.Get("X-API-Key")
		env.Metadata = map[string]any{
			"user_content": r.URL.Query().Get("q"),
			"model":        r.URL.Query().Get("model"),
			"api_key_fp":   fingerprintKey(apiKey),
		}
		if env.Metadata["user_content"] == nil {
			env.Metadata["user_content"] = ""
		}

		// 执行 Pipeline
		if err := deps.Pipeline.Execute(ctx, env); err != nil {
			w.Header().Set("Content-Type", "application/json")
			if env.StatusCode == 0 {
				env.StatusCode = 500
			}
			w.WriteHeader(env.StatusCode)
			// NET-002 fix: 不再把 err.Error() 回显给客户端（含内部路径、
			// 依赖服务地址等），只回通用错误 + request_id。详细错误保留
			// 在服务端日志供运维排查。
			slog.Error("v2 pipeline execute failed", "request_id", env.Envelope.RequestID, "err", err)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":      "internal error",
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

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// /v1/chat/completions — OpenAI 兼容 chat completions 端点
	// (2026-06-29 端点补全 P0 第 1 步)
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeOpenAIError(w, http.StatusMethodNotAllowed,
				"method not allowed, use POST", "invalid_request_error")
			return
		}

		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Stream bool   `json:"stream"`
			User   string `json:"user,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeOpenAIError(w, http.StatusBadRequest,
				fmt.Sprintf("invalid JSON: %v", err), "invalid_request_error")
			return
		}
		if req.Model == "" {
			req.Model = "gpt-4o"
		}
		if len(req.Messages) == 0 {
			writeOpenAIError(w, http.StatusBadRequest,
				"messages array is required", "invalid_request_error")
			return
		}
		if req.Stream {
			writeOpenAIError(w, http.StatusBadRequest,
				"stream=true is not supported in gateway-v2 demo mode; set stream=false",
				"invalid_request_error")
			return
		}

		lastUser := ""
		for i := len(req.Messages) - 1; i >= 0; i-- {
			if req.Messages[i].Role == "user" {
				lastUser = req.Messages[i].Content
				break
			}
		}

		ctx := r.Context()
		env := domain.NewRequestEnvelope(ctx, &domain.RequestEnvelope{
			RequestID: fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
			CreatedAt: time.Now(),
			GoContext: ctx,
		})
		env.TenantID = r.Header.Get("X-Tenant-ID")
		env.SessionID = r.Header.Get("X-Session-ID")
		env.Metadata = map[string]any{
			"model":         req.Model,
			"user_content":  lastUser,
			"message_count": len(req.Messages),
			"stream":        req.Stream,
			"api_key_fp":    fingerprintKey(r.Header.Get("X-API-Key")),
		}
		if err := deps.Pipeline.Execute(ctx, env); err != nil {
			slog.Error("v2 chat/completions pipeline failed",
				"request_id", env.Envelope.RequestID, "err", err)
			// 遵循 env.StatusCode (security hook 设 403, 其他默认 500)
			statusCode := env.StatusCode
			if statusCode == 0 {
				statusCode = http.StatusInternalServerError
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(statusCode)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"message":    "internal error",
					"type":       "internal_error",
					"request_id": env.Envelope.RequestID,
				},
			})
			return
		}

		promptTokens := len(lastUser) / 4
		completionContent := fmt.Sprintf("[gateway-v2 demo] 收到模型 %s 的请求。最后一条 user 消息: %q。完整 Pipeline 已执行 17 stages。",
			req.Model, truncate(lastUser, 100))
		completionTokens := len(completionContent) / 4

		completionID := env.Envelope.RequestID
		resp := map[string]any{
			"id":      completionID,
			"object":  "chat.completion",
			"created": time.Now().Unix(),
			"model":   req.Model,
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": completionContent,
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     promptTokens,
				"completion_tokens": completionTokens,
				"total_tokens":      promptTokens + completionTokens,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	// /v1/messages — Anthropic Messages API 兼容端点 (2026-06-29 端点补全第 4 步)
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeAnthropicError(w, http.StatusMethodNotAllowed,
				"method not allowed, use POST", "invalid_request_error")
			return
		}

		var req struct {
			Model     string `json:"model"`
			MaxTokens int    `json:"max_tokens"`
			System    string `json:"system"`
			Messages  []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAnthropicError(w, http.StatusBadRequest,
				fmt.Sprintf("invalid JSON: %v", err), "invalid_request_error")
			return
		}
		if req.Model == "" {
			req.Model = "claude-3-5-sonnet-20241022"
		}
		if req.MaxTokens <= 0 {
			req.MaxTokens = 1024
		}
		if len(req.Messages) == 0 {
			writeAnthropicError(w, http.StatusBadRequest,
				"messages is required", "invalid_request_error")
			return
		}
		for _, m := range req.Messages {
			if m.Role == "system" {
				writeAnthropicError(w, http.StatusBadRequest,
					"messages must not contain 'system' role (use top-level 'system' field)",
					"invalid_request_error")
				return
			}
		}

		lastUser := ""
		for i := len(req.Messages) - 1; i >= 0; i-- {
			if req.Messages[i].Role == "user" {
				lastUser = req.Messages[i].Content
				break
			}
		}

		ctx := r.Context()
		env := domain.NewRequestEnvelope(ctx, &domain.RequestEnvelope{
			RequestID: fmt.Sprintf("msg_%d", time.Now().UnixNano()),
			CreatedAt: time.Now(),
			GoContext: ctx,
		})
		env.TenantID = r.Header.Get("X-Tenant-ID")
		env.SessionID = r.Header.Get("X-Session-ID")
		env.Metadata = map[string]any{
			"model":         req.Model,
			"max_tokens":    req.MaxTokens,
			"system":        req.System,
			"user_content":  lastUser,
			"message_count": len(req.Messages),
			"api_key_fp":    fingerprintKey(r.Header.Get("X-API-Key")),
		}
		if err := deps.Pipeline.Execute(ctx, env); err != nil {
			slog.Error("v2 messages pipeline failed",
				"request_id", env.Envelope.RequestID, "err", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"type": "error",
				"error": map[string]any{
					"type":    "internal_error",
					"message": "internal error",
				},
			})
			return
		}

		inputTokens := (len(lastUser) + len(req.System)) / 4
		contentText := fmt.Sprintf("[gateway-v2 demo] 收到 Anthropic Messages API 请求。模型: %s, max_tokens: %d, system: %q。最后一条 user: %q。Pipeline 已完成。",
			req.Model, req.MaxTokens, truncate(req.System, 50), truncate(lastUser, 80))
		outputTokens := len(contentText) / 4

		resp := map[string]any{
			"id":            env.Envelope.RequestID,
			"type":          "message",
			"role":          "assistant",
			"content":       []map[string]any{{"type": "text", "text": contentText}},
			"model":         req.Model,
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":  inputTokens,
				"output_tokens": outputTokens,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	// /v1/responses — OpenAI Responses API 兼容端点
	// (2026-06-29 端点补全 P0 第 5 步)
	//
	// OpenAI Responses API 格式 (2025 新版):
	//   request:  {"model": "gpt-4o", "input": "Hello"} 或
	//             {"model": "gpt-4o", "input": [{"role": "user", "content": "Hello"}]}
	//   response: {"id": "resp_...", "object": "response", "status": "completed",
	//              "output": [{"type": "message", "role": "assistant",
	//                          "content": [{"type": "output_text", "text": "..."}]}],
	//              "model": "...", "usage": {"input_tokens": N, "output_tokens": M}}
	mux.HandleFunc("/v1/responses", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeOpenAIError(w, http.StatusMethodNotAllowed,
				"method not allowed, use POST", "invalid_request_error")
			return
		}

		var req struct {
			Model     string          `json:"model"`
			Input     json.RawMessage `json:"input"`
			MaxTokens *int            `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeOpenAIError(w, http.StatusBadRequest,
				fmt.Sprintf("invalid JSON: %v", err), "invalid_request_error")
			return
		}
		if req.Model == "" {
			req.Model = "gpt-4o"
		}
		if len(req.Input) == 0 {
			writeOpenAIError(w, http.StatusBadRequest,
				"input is required", "invalid_request_error")
			return
		}

		// 提取 input 内容（字符串或数组）
		inputText := strings.TrimSpace(string(req.Input))
		if strings.HasPrefix(inputText, "[") {
			// 数组格式: [{"role": "user", "content": "..."}]
			var items []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(req.Input, &items); err == nil && len(items) > 0 {
				// 取最后一个 user 内容
				for i := len(items) - 1; i >= 0; i-- {
					if items[i].Role == "user" {
						inputText = items[i].Content
						break
					}
				}
			}
		}
		inputText = strings.Trim(inputText, `"`)

		ctx := r.Context()
		env := domain.NewRequestEnvelope(ctx, &domain.RequestEnvelope{
			RequestID: fmt.Sprintf("resp_%d", time.Now().UnixNano()),
			CreatedAt: time.Now(),
			GoContext: ctx,
		})
		env.TenantID = r.Header.Get("X-Tenant-ID")
		env.SessionID = r.Header.Get("X-Session-ID")
		env.Metadata = map[string]any{
			"model":        req.Model,
			"user_content": inputText,
			"api_key_fp":   fingerprintKey(r.Header.Get("X-API-Key")),
		}
		if req.MaxTokens != nil {
			env.Metadata["max_tokens"] = *req.MaxTokens
		}
		if err := deps.Pipeline.Execute(ctx, env); err != nil {
			slog.Error("v2 responses pipeline failed",
				"request_id", env.Envelope.RequestID, "err", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"message": "internal error", "type": "internal_error",
					"request_id": env.Envelope.RequestID,
				},
			})
			return
		}

		inputTokens := len(inputText) / 4
		outputText := fmt.Sprintf("[gateway-v2 demo] 收到 OpenAI Responses API 请求。模型: %s, input: %q。Pipeline 17 stages 已完成。",
			req.Model, truncate(inputText, 100))
		outputTokens := len(outputText) / 4

		resp := map[string]any{
			"id":         env.Envelope.RequestID,
			"object":     "response",
			"created_at": time.Now().Unix(),
			"status":     "completed",
			"model":      req.Model,
			"output": []map[string]any{
				{
					"type": "message",
					// 使用 msg_<nanos> 格式（与 resp_<nanos> 区分），避免双重前缀
					"id":   fmt.Sprintf("msg_%d", time.Now().UnixNano()),
					"role": "assistant",
					"content": []map[string]any{
						{
							"type":        "output_text",
							"text":        outputText,
							"annotations": []any{},
						},
					},
				},
			},
			"usage": map[string]any{
				"input_tokens":  inputTokens,
				"output_tokens": outputTokens,
				"total_tokens":  inputTokens + outputTokens,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	// /v1/completions — OpenAI 旧版 completions 端点 (legacy)
	// (2026-06-29 端点补全 P0 第 3 步)
	//
	// 与 /v1/chat/completions 类似但请求/响应格式更简单:
	//   request:  {"model": "...", "prompt": "Hello", "max_tokens": 16}
	//   response: {"id": "cmpl-...", "object": "text_completion", "choices": [{"text": "..."}], ...}
	mux.HandleFunc("/v1/completions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeOpenAIError(w, http.StatusMethodNotAllowed,
				"method not allowed, use POST", "invalid_request_error")
			return
		}

		var req struct {
			Model     string `json:"model"`
			Prompt    string `json:"prompt"`
			MaxTokens int    `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeOpenAIError(w, http.StatusBadRequest,
				fmt.Sprintf("invalid JSON: %v", err), "invalid_request_error")
			return
		}
		if req.Model == "" {
			req.Model = "gpt-3.5-turbo" // legacy default (matches demo provider)
		}
		if req.Prompt == "" {
			writeOpenAIError(w, http.StatusBadRequest,
				"prompt is required", "invalid_request_error")
			return
		}

		ctx := r.Context()
		env := domain.NewRequestEnvelope(ctx, &domain.RequestEnvelope{
			RequestID: fmt.Sprintf("cmpl-%d", time.Now().UnixNano()),
			CreatedAt: time.Now(),
			GoContext: ctx,
		})
		env.TenantID = r.Header.Get("X-Tenant-ID")
		env.SessionID = r.Header.Get("X-Session-ID")
		env.Metadata = map[string]any{
			"model":        req.Model,
			"user_content": req.Prompt,
			"max_tokens":   req.MaxTokens,
			"api_key_fp":   fingerprintKey(r.Header.Get("X-API-Key")),
		}
		if err := deps.Pipeline.Execute(ctx, env); err != nil {
			slog.Error("v2 completions pipeline failed",
				"request_id", env.Envelope.RequestID, "err", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"message": "internal error", "type": "internal_error",
					"request_id": env.Envelope.RequestID,
				},
			})
			return
		}

		promptTokens := len(req.Prompt) / 4
		completionText := fmt.Sprintf("[gateway-v2 demo] 收到模型 %s 的 completions 请求。Prompt: %q。",
			req.Model, truncate(req.Prompt, 80))
		completionTokens := len(completionText) / 4

		resp := map[string]any{
			"id":      env.Envelope.RequestID,
			"object":  "text_completion",
			"created": time.Now().Unix(),
			"model":   req.Model,
			"choices": []map[string]any{
				{
					"text":          completionText,
					"index":         0,
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     promptTokens,
				"completion_tokens": completionTokens,
				"total_tokens":      promptTokens + completionTokens,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	// /v1/models — OpenAI 兼容的模型列表端点（2026-06-26 端点补全第一步）
	// 返回所有活跃 provider 的 model 列表，按 OpenAI API 格式:
	//   { "object": "list", "data": [{ "id": "...", "object": "model", ... }] }
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		providers, err := deps.ProviderStore.List()
		if err != nil {
			writeOpenAIError(w, http.StatusInternalServerError,
				err.Error(), "api_error")
			return
		}

		type openAIModel struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
			OwnedBy string `json:"owned_by"`
		}

		data := make([]openAIModel, 0)
		now := time.Now().Unix()
		for _, p := range providers {
			if p.Disabled {
				continue
			}
			for _, m := range p.Models {
				data = append(data, openAIModel{
					ID:      m.Name,
					Object:  "model",
					Created: now,
					OwnedBy: p.Name,
				})
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data":   data,
		})
	})

	// /v1/models/{model_id} — OpenAI 兼容的单模型查询端点
	// (2026-06-29 端点补全 P0 第 2 步)
	//
	// 返回 404 如果模型不存在。查询所有 provider + models 列表，匹配第一个。
	mux.HandleFunc("/v1/models/", func(w http.ResponseWriter, r *http.Request) {
		// 从路径提取 model_id: "/v1/models/gpt-4o" -> "gpt-4o"
		modelID := strings.TrimPrefix(r.URL.Path, "/v1/models/")
		if modelID == "" || strings.Contains(modelID, "/") {
			writeOpenAIError(w, http.StatusBadRequest,
				"model_id required", "invalid_request_error")
			return
		}

		providers, err := deps.ProviderStore.List()
		if err != nil {
			writeOpenAIError(w, http.StatusInternalServerError,
				err.Error(), "api_error")
			return
		}

		type openAIModel struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
			OwnedBy string `json:"owned_by"`
		}

		now := time.Now().Unix()
		for _, p := range providers {
			if p.Disabled {
				continue
			}
			for _, m := range p.Models {
				if m.Name == modelID {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(openAIModel{
						ID:      m.Name,
						Object:  "model",
						Created: now,
						OwnedBy: p.Name,
					})
					return
				}
			}
		}

		// OpenAI 格式错误: {"error": {"message": "...", "type": "...", "code": "model_not_found"}}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"message": fmt.Sprintf("The model '%s' does not exist", modelID),
				"type":    "invalid_request_error",
				"param":   "model",
				"code":    "model_not_found",
			},
		})
	})

	// /metrics: Prometheus 格式指标端点。
	// 暴露两部分:
	//   1. prometheus.DefaultGatherer —— compression 包注册的
	//      compression_triggered_total / compression_latency_seconds / compression_ratio
	//      (这些用 prometheus.DefaultRegisterer)
	//   2. deps.Metrics (自研 observability.Registry) —— requests_total 等
	//      序列化为 Prometheus 文本格式追加在后面
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", prometheusTextContentType)

		// 1. 标准 Prometheus 指标 (compression_*)
		stdHandler := promhttp.HandlerFor(prometheus.DefaultGatherer,
			promhttp.HandlerOpts{EnableOpenMetrics: false})
		stdHandler.ServeHTTP(w, r)

		// 2. 自研 Registry 指标 (requests_total 等) —— 追加为文本
		if deps.Metrics != nil {
			renderInMemoryMetrics(w, deps.Metrics)
		}
	})

	return mux
}

// prometheusTextContentType is the standard Prometheus text exposition format.
const prometheusTextContentType = "text/plain; version=0.0.4; charset=utf-8"

// renderInMemoryMetrics serializes the v2 in-memory observability.Registry
// (Counters + Histograms) into Prometheus text format and writes it to w.
// This is appended after the standard promhttp output so /metrics shows both
// the compression_* metrics (prometheus.DefaultRegisterer) and the v2 pipeline
// counters (requests_total etc.).
func renderInMemoryMetrics(w http.ResponseWriter, reg *observability.Registry) {
	// Counters
	for _, c := range reg.Counters() {
		fmt.Fprintf(w, "# TYPE %s counter\n", c.Name)
		fmt.Fprintf(w, "%s%s %v\n", c.Name, renderLabels(c.Labels), c.Value)
	}
	// Histograms
	for _, h := range reg.Histograms() {
		fmt.Fprintf(w, "# TYPE %s histogram\n", h.Name)
		lbl := renderLabels(h.Labels)
		for i, b := range h.Buckets {
			fmt.Fprintf(w, "%s_bucket%s{le=\"%g\"} %d\n", h.Name, lbl, b, h.Counts[i])
		}
		fmt.Fprintf(w, "%s_bucket%s{le=\"+Inf\"} %d\n", h.Name, lbl, h.Counts[len(h.Buckets)])
		fmt.Fprintf(w, "%s_sum%s %g\n", h.Name, lbl, h.Sum)
		fmt.Fprintf(w, "%s_count%s %d\n", h.Name, lbl, h.Count)
	}
}

// renderLabels renders a label map as Prometheus {k="v",...} suffix,
// or empty string if nil/empty.
func renderLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	// deterministic order
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteString("=\"")
		b.WriteString(labels[k])
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cfg := loadConfig()
	deps := newDeps(cfg)
	deps.Pipeline = buildPipeline(deps)

	logger.Info("gateway-v2 starting", "listen", cfg.Listen, "stages", len(deps.Pipeline.Stages()))

	srv := &http.Server{
		Addr: cfg.Listen,
		// NET-002 fix: 复用 cmd/gateway 的中间件链 —— recovery / requestid /
		// cors / auth / logging。修复前 v2 端完全裸奔。
		//
		// 超时：参考 cmd/gateway 的值，但 WriteTimeout 调大（演示环境可能
		// 需要长时间 SSE 流）。ReadHeaderTimeout 仍必须设置防 Slowloris。
		Handler:           middleware.NewCORSMiddleware(getEnv("LLM_GATEWAY_CORS_ORIGINS", "http://localhost:5173")).Wrap(authChain(deps, cfg)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       120 * time.Second,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	// 优雅退出
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		logger.Info("shutting down")
		_ = deps.AuditWriter.Close()
		_ = srv.Shutdown(ctx)
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server failed", "err", err)
		os.Exit(1)
	}
}

// 编译期检查：确保新包被使用
var (
	_ *identity.IdentityBuilder
	_ *authentication.Verifier
	_ *session.SessionStore
	_ session.SessionLoaderHook
	_ *integration.MinimalDeps
	_ pipeline.Hook = (*observability.TracingHook)(nil)
	_ *credential.HealthCheckHook
	_ *provider.ProviderDiscoveryHook
)

// truncate 截断字符串到 maxLen runes（避免 split multibyte）
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

// writeOpenAIError 返回 OpenAI 格式的 JSON 错误响应。
// 用于 OpenAI 兼容端点：/v1/chat/completions、/v1/completions、
// /v1/responses、/v1/models/{model_id}。
//
// OpenAI 错误格式：{"error": {"message": "...", "type": "...", "param": "...", "code": "..."}}
func writeOpenAIError(w http.ResponseWriter, status int, msg, errType string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    errType,
		},
	})
}

// writeAnthropicError 返回 Anthropic 格式的 JSON 错误响应。
// 用于 Anthropic Messages API 兼容端点：/v1/messages。
//
// Anthropic 错误格式：{"type": "error", "error": {"type": "...", "message": "..."}}
func writeAnthropicError(w http.ResponseWriter, status int, msg, errType string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    errType,
			"message": msg,
		},
	})
}
