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
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/agent-ecosystem"
	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/cache"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/security"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/session-inspector"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/tools"
	"github.com/kaixuan/llm-gateway-go/domains/identity"
	"github.com/kaixuan/llm-gateway-go/domains/integration"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
	"github.com/kaixuan/llm-gateway-go/domains/provider"
	"github.com/kaixuan/llm-gateway-go/domains/routing"
	"github.com/kaixuan/llm-gateway-go/domains/session"
	"github.com/kaixuan/llm-gateway-go/domains/streaming"
	"github.com/kaixuan/llm-gateway-go/domains/transformation"
	"github.com/kaixuan/llm-gateway-go/eventbus"
)

// v2Config 简化的 v2 配置
type v2Config struct {
	Listen          string
	EnableCache     bool
	EnableSecurity  bool
	EnableAudit     bool
	EnableObserv    bool
	EnableStreaming bool
}

// loadConfig 加载配置
func loadConfig() *v2Config {
	cfg := &v2Config{
		Listen:          getEnv("LLM_GATEWAY_LISTEN", ":8782"),
		EnableCache:     getEnv("LLM_GATEWAY_V2_CACHE", "true") == "true",
		EnableSecurity:  getEnv("LLM_GATEWAY_V2_SECURITY", "true") == "true",
		EnableAudit:     getEnv("LLM_GATEWAY_V2_AUDIT", "true") == "true",
		EnableObserv:    getEnv("LLM_GATEWAY_V2_OBSERV", "true") == "true",
		EnableStreaming: getEnv("LLM_GATEWAY_V2_STREAMING", "true") == "true",
	}
	return cfg
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
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
		},
		TimeoutSec: 60,
	})

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
		EventBus:         eventbus.NewMemoryBus(100),
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
		env.Metadata = map[string]any{
			"user_content": r.URL.Query().Get("q"),
			"model":        r.URL.Query().Get("model"),
			"api_key":      r.Header.Get("X-API-Key"),
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

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// /v1/models — OpenAI 兼容的模型列表端点（2026-06-26 端点补全第一步）
	// 返回所有活跃 provider 的 model 列表，按 OpenAI API 格式:
	//   { "object": "list", "data": [{ "id": "...", "object": "model", ... }] }
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		providers, err := deps.ProviderStore.List()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
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

	return mux
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cfg := loadConfig()
	deps := newDeps(cfg)
	deps.Pipeline = buildPipeline(deps)

	logger.Info("gateway-v2 starting", "listen", cfg.Listen, "stages", len(deps.Pipeline.Stages()))

	srv := &http.Server{
		Addr:    cfg.Listen,
		Handler: httpHandler(deps),
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
