package main

// 安全事件出网 sink 的装配。
//
// # 开关语义
//
// LLM_GATEWAY_SECURITY_EVENT_SINK=1 才装。默认**关**：安全事件的出网是
// 纯副作用（写不进去不影响任何请求结果），在没配 memora 的环境里挂它只会
// 产生无意义的连接尝试与告警。
//
// # 为什么不复用 session-analysis 那条推送器
//
// 那条链走的是 memora 的 v2 会话分析契约面（端点、鉴权、幂等键都不同），
// 且契约由 memora 仓的集成文档冻结。安全事件是**另一种**数据（事件 vs
// 分析产出），复用它会把两套语义混在一个端点上。

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/memora"
	"github.com/kaixuan/llm-gateway-go/internal/securityevent"
)

// installSecurityEventSink 装上安全事件的出网 sink。
//
// 返回 (sink, stop)。sink 为 nil 表示没装（开关关或前提不满足）；stop 为
// nil 表示没有需要取消的后台任务。
//
// 前提：
//   - LLM_GATEWAY_MEMORA_BASE_URL 必须配（落地面）；
//   - LLM_GATEWAY_MEMORA_PROJECT_ID 必须配 —— memora 的项目作用域是
//     fail-closed 的（缺 X-Project-ID 直接 400 "project_id is required"），
//     没配就**不装**：装上只会让每条事件都写失败，日志全是噪声。
//
// 通道选择：
//   - 配了 LLM_GATEWAY_MEMORA_JWT_SECRET ⇒ 走 v2 typed ingest（逐条幂等 +
//     scope_chain）；
//   - 否则 ⇒ 走 legacy 记忆入口（ingest API key，X-API-Key 头）。
//     网关现役配的就是这个凭据（.env.example 的 MEMORA_INGEST_API_KEY）。
func installSecurityEventSink(baseURL string) (*securityevent.Sink, context.CancelFunc) {
	if os.Getenv("LLM_GATEWAY_SECURITY_EVENT_SINK") != "1" {
		slog.Info("security event sink disabled (set LLM_GATEWAY_SECURITY_EVENT_SINK=1 to enable)")
		return nil, nil
	}
	if strings.TrimSpace(baseURL) == "" {
		slog.Warn("security event sink not wired: LLM_GATEWAY_MEMORA_BASE_URL is not set")
		return nil, nil
	}
	projectID := strings.TrimSpace(os.Getenv("LLM_GATEWAY_MEMORA_PROJECT_ID"))
	if projectID == "" {
		slog.Warn("security event sink not wired: LLM_GATEWAY_MEMORA_PROJECT_ID is not set — memora's project scope is fail-closed, writes would be rejected")
		return nil, nil
	}

	writer, channel := securityEventWriter(baseURL, projectID)
	if writer == nil {
		return nil, nil
	}

	sink := securityevent.NewSink(securityevent.SinkConfig{
		Writer: writer,
		Logger: slog.Default(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	sink.Start(ctx)
	slog.Info("security event sink enabled",
		"base_url", baseURL,
		"project_id", projectID,
		"channel", channel)
	return sink, cancel
}

// securityEventWriter 造落地面：优先 v2 typed ingest，回退 legacy 入口。
func securityEventWriter(baseURL, projectID string) (securityevent.Writer, string) {
	if secret := strings.TrimSpace(os.Getenv("LLM_GATEWAY_MEMORA_JWT_SECRET")); secret != "" {
		client, err := memora.New(memora.Config{
			BaseURL:   baseURL,
			JWTSecret: secret,
			Issuer:    envOrDefault("LLM_GATEWAY_MEMORA_JWT_ISSUER", memora.DefaultIssuer),
			Audience:  envOrDefault("LLM_GATEWAY_MEMORA_JWT_AUDIENCE", memora.DefaultAudience),
			TenantID:  envOrDefault("LLM_GATEWAY_MEMORA_TENANT_ID", memora.DefaultTenantID),
			ActorID:   envOrDefault("LLM_GATEWAY_MEMORA_ACTOR_ID", "local-ops"),
			ProjectID: projectID,
		})
		if err != nil {
			slog.Warn("security event sink: memora v2 client unusable, falling back to the legacy channel", "error", err)
		} else {
			return securityevent.NewMemoraV2Writer(client), "v2-typed-ingest"
		}
	}

	client := newMemoraClientFromEnv(baseURL)
	return securityevent.NewMemoraLegacyWriter(client, os.Getenv("LLM_GATEWAY_MEMORA_SECURITY_USER_ID")), "legacy-add"
}
