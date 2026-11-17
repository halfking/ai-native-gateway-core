package main

// 跨实例威胁模式（M2-3）的装配。
//
// # 开关语义
//
// LLM_GATEWAY_SECURITY_PATTERN_SYNC=1 才装。默认**关**：读面是纯副作用
// （共享域读不到不影响任何请求结果），在没配 memora 的环境里挂它只会
// 产生无意义的连接尝试与告警。
//
// # 前提：读面必须与写面同一身份通道
//
// memora 的 v2 检索以 **actor** 作为 user_id 谓词（memora 仓
// internal/v2/retrieval.go），而 v2 ingest 也以 actor 作为落库 user_id
// （internal/v2/ingest.go）⇒ 同一个 service JWT ⇒ 同一个命名空间，
// 读回来的就是别的实例写进去的东西。
//
// 而 legacy 通道（ingest API key）写的事件落在 **user_id = 租户** 的
// 命名空间里 ⇒ 用 actor 身份去读**读不到**。⇒ 没配 JWT 密钥时**不装**
// 读面：装上只会每分钟拉一次空结果，看起来像"没有别的实例"。
//
// # 三个环境变量
//
//	LLM_GATEWAY_SECURITY_PATTERN_SYNC=1     开关
//	LLM_GATEWAY_SECURITY_PATTERN_INTERVAL    同步间隔（默认 60s）
//	LLM_GATEWAY_SECURITY_PATTERN_MAX_AGE     快照最大可用年龄（默认 15m）
//	LLM_GATEWAY_SECURITY_PATTERN_TOP_K       每次取回条数（默认 50，上限 100）
//	LLM_GATEWAY_SECURITY_PATTERN_QUERY       检索词（默认见 securitypattern）

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/memora"
	"github.com/kaixuan/llm-gateway-go/internal/securitypattern"
)

// installSecurityPatternSync 装上跨实例模式同步。
//
// 返回 (cache, syncer, stop)。cache 为 nil 表示没装（开关关或前提不满足）；
// syncer 为 nil 时 stop 也为 nil。
//
// cache 会传给请求路径（publishSecurityEvent）：本机检测到的事件挂上
// "全网视角"。**它不改变任何处置结论**（见 securitypattern 包的说明）。
func installSecurityPatternSync(baseURL string) (*securitypattern.Cache, *securitypattern.Syncer, context.CancelFunc) {
	if os.Getenv("LLM_GATEWAY_SECURITY_PATTERN_SYNC") != "1" {
		slog.Info("security pattern sync disabled (set LLM_GATEWAY_SECURITY_PATTERN_SYNC=1 to enable)")
		return nil, nil, nil
	}
	if strings.TrimSpace(baseURL) == "" {
		slog.Warn("security pattern sync not wired: LLM_GATEWAY_MEMORA_BASE_URL is not set")
		return nil, nil, nil
	}
	projectID := strings.TrimSpace(os.Getenv("LLM_GATEWAY_MEMORA_PROJECT_ID"))
	if projectID == "" {
		slog.Warn("security pattern sync not wired: LLM_GATEWAY_MEMORA_PROJECT_ID is not set — memora's project scope is fail-closed")
		return nil, nil, nil
	}
	if strings.TrimSpace(os.Getenv("LLM_GATEWAY_MEMORA_JWT_SECRET")) == "" {
		// ★ 这是最容易"配了却不工作"的一处：legacy 通道写的事件在
		//   user_id = 租户 的命名空间里，而读面用 actor 身份检索 ⇒ 读不到。
		//   明确说出来，比装一个永远空转的循环有用。
		slog.Warn("security pattern sync not wired: LLM_GATEWAY_MEMORA_JWT_SECRET is not set — " +
			"the read face searches the service actor's namespace, while the legacy channel writes into the tenant's namespace; " +
			"without the v2 service JWT the shared patterns would always come back empty")
		return nil, nil, nil
	}

	client, err := newMemoraV2Client(baseURL, projectID)
	if err != nil {
		slog.Warn("security pattern sync not wired: memora v2 client unusable", "error", err)
		return nil, nil, nil
	}

	reader := securitypattern.NewMemoraReader(
		client,
		strings.TrimSpace(os.Getenv("LLM_GATEWAY_SECURITY_PATTERN_QUERY")),
		securityPatternTopK(),
	)
	cache := securitypattern.NewCache(securityPatternMaxAge())
	syncer := securitypattern.NewSyncer(securitypattern.SyncerConfig{
		Reader:   reader,
		Cache:    cache,
		Interval: securityPatternInterval(),
		Logger:   slog.Default(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	go syncer.Run(ctx)
	slog.Info("security pattern sync enabled",
		"base_url", baseURL,
		"project_id", projectID,
		"tenant_id", envOrDefault("LLM_GATEWAY_MEMORA_TENANT_ID", memora.DefaultTenantID),
		"actor_id", envOrDefault("LLM_GATEWAY_MEMORA_ACTOR_ID", "local-ops"),
		"interval", securityPatternInterval(),
		"max_age", securityPatternMaxAge(),
		"top_k", securityPatternTopK(),
		// ★ 跨实例共享的前提：所有网关实例配**同一套**身份
		//   （同一 JWT 密钥 / tenant / actor / project）。memora 的 memory
		//   读端点硬过滤 user_id = actor ⇒ actor 不同则互相读不到，
		//   模式功能会退化成"只统计自己"，而且看不出来。
		"cross_instance_requires_shared_identity", true)
	return cache, syncer, cancel
}

// securityPatternInterval 读同步间隔。
func securityPatternInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv("LLM_GATEWAY_SECURITY_PATTERN_INTERVAL"))
	if raw == "" {
		return securitypattern.DefaultInterval
	}
	if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
		return parsed
	}
	slog.Warn("security pattern sync: LLM_GATEWAY_SECURITY_PATTERN_INTERVAL is not a valid duration, using the default",
		"value", raw, "default", securitypattern.DefaultInterval)
	return securitypattern.DefaultInterval
}

// securityPatternMaxAge 读快照最大可用年龄。
func securityPatternMaxAge() time.Duration {
	raw := strings.TrimSpace(os.Getenv("LLM_GATEWAY_SECURITY_PATTERN_MAX_AGE"))
	if raw == "" {
		return securitypattern.DefaultMaxAge
	}
	if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
		return parsed
	}
	slog.Warn("security pattern sync: LLM_GATEWAY_SECURITY_PATTERN_MAX_AGE is not a valid duration, using the default",
		"value", raw, "default", securitypattern.DefaultMaxAge)
	return securitypattern.DefaultMaxAge
}

// securityPatternTopK 读每次取回条数。
func securityPatternTopK() int {
	raw := strings.TrimSpace(os.Getenv("LLM_GATEWAY_SECURITY_PATTERN_TOP_K"))
	if raw == "" {
		return securitypattern.DefaultTopK
	}
	if value, err := strconv.Atoi(raw); err == nil && value > 0 {
		return value
	}
	slog.Warn("security pattern sync: LLM_GATEWAY_SECURITY_PATTERN_TOP_K is not a valid count, using the default",
		"value", raw, "default", securitypattern.DefaultTopK)
	return securitypattern.DefaultTopK
}
