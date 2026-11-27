package mcp

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// EnvVarName 是 MCP 网关读取的环境变量名。
//
// 全部变量都有默认值 ⇒ 不配置任何变量时网关仍可启动（仅控制面不可用）。
// 这样「随主体分发」不会给既有部署带来启动风险。
const (
	EnvEnabled         = "MCP_GATEWAY_ENABLED"
	EnvStoreFile       = "MCP_GATEWAY_STORE_FILE"
	EnvAuditFile       = "MCP_GATEWAY_AUDIT_FILE"
	EnvAdminToken      = "MCP_GATEWAY_ADMIN_TOKEN"
	EnvDefaultTenant   = "MCP_GATEWAY_DEFAULT_TENANT"
	EnvRateLimitPerMin = "MCP_GATEWAY_RATE_LIMIT_PER_MIN"
	EnvReadLimit       = "MCP_GATEWAY_READ_RATE_LIMIT"
	EnvWriteLimit      = "MCP_GATEWAY_WRITE_RATE_LIMIT"
	EnvLLMLimit        = "MCP_GATEWAY_LLM_RATE_LIMIT"
	EnvToolTimeoutMS   = "MCP_GATEWAY_TOOL_TIMEOUT_MS"
	EnvMaxBodyBytes    = "MCP_GATEWAY_MAX_BODY_BYTES"
	EnvAutoRegister    = "MCP_GATEWAY_AUTO_REGISTER"
	EnvMaskPII         = "MCP_GATEWAY_MASK_PII"
	EnvLedgerURL       = "MCP_GATEWAY_LEDGER_URL"
	EnvLedgerToken     = "MCP_GATEWAY_LEDGER_TOKEN"
	EnvLedgerCacheMS   = "MCP_GATEWAY_LEDGER_CACHE_MS"
	EnvCircuitFailures = "MCP_GATEWAY_CIRCUIT_FAILURES"
	EnvCircuitOpenSec  = "MCP_GATEWAY_CIRCUIT_OPEN_SECONDS"

	EnvBootstrapEnabled        = "MCP_GATEWAY_BOOTSTRAP_ENABLED"
	EnvBootstrapAutoApprove    = "MCP_GATEWAY_BOOTSTRAP_AUTO_APPROVE"
	EnvBootstrapAutoApproveDev = "MCP_GATEWAY_BOOTSTRAP_AUTO_APPROVE_DEVICES"
	EnvBootstrapApplyIPLimit   = "MCP_GATEWAY_BOOTSTRAP_APPLY_IP_LIMIT"
	EnvBootstrapApplyDevLimit  = "MCP_GATEWAY_BOOTSTRAP_APPLY_DEVICE_LIMIT"
	EnvBootstrapReadIPLimit    = "MCP_GATEWAY_BOOTSTRAP_READ_IP_LIMIT"

	// EnvControlScope 控制面 scope 鉴权开关（on/off，默认 on）。
	//
	// ★ off ⇒ 控制面**只认管理令牌**（回退到「业务 key 无控制面权限」的既有
	//   口径）；on ⇒ 业务 key 可按 mcp.register / mcp.manage / mcp.analyze /
	//   mcp.topology 访问控制面。
	EnvControlScope = "MCP_GATEWAY_CONTROL_SCOPE"

	// ---- registry 联邦（ADR 2026-11-27 §3 D1 / §4 D2a）----
	// 全部默认关闭/为空 ⇒ 不配置时同步器不存在，行为与引入前完全一致
	// （装配面默认关，对齐 W3 既定口径）。
	// EnvRegistrySyncEnabled 联邦同步总开关（默认 false，fail-closed）。
	EnvRegistrySyncEnabled = "MCP_GATEWAY_REGISTRY_SYNC_ENABLED"
	// EnvRegistryURL memora 基址（如 http://memora:8080）。
	EnvRegistryURL = "MCP_GATEWAY_REGISTRY_URL"
	// EnvRegistryToken 调 memora /api/v2 的 service JWT（长令牌或网关自签均可，
	// 由部署侧保证 memora verifier 认它）。
	EnvRegistryToken = "[REDACTED:secret]"
	// EnvRegistryIntervalSeconds 轮询间隔（默认 60）。
	EnvRegistryIntervalSeconds = "MCP_GATEWAY_REGISTRY_INTERVAL_SECONDS"
	// EnvRegistryJWTSecret 出站 service JWT 签名密钥（= memora JWT_SECRET 同值，
	// ADR §8 R1）。空 ⇒ llm_gateway 模式适配器调用必失败（fail-closed）。
	EnvRegistryJWTSecret = "MCP_GATEWAY_REGISTRY_JWT_SECRET"
	// EnvRegistryJWTIssuer 出站 JWT iss（默认 llm-gateway-mcp；须与 memora
	// V2JWTIssuer 同源配置）。
	EnvRegistryJWTIssuer = "MCP_GATEWAY_REGISTRY_JWT_ISSUER"
	// EnvRegistryJWTAudience 出站 JWT aud（须与 memora V2JWTAudience 同源，
	// memora 默认 memora-api）。
	EnvRegistryJWTAudience = "MCP_GATEWAY_REGISTRY_JWT_AUDIENCE"
)

// EnvConfig 是从环境变量解析出的网关配置。
type EnvConfig struct {
	Enabled         bool
	StoreFile       string
	AuditFile       string
	AdminToken      string
	DefaultTenantID string
	RateLimitPerMin int
	ReadLimit       int
	WriteLimit      int
	LLMLimit        int
	ToolTimeout     time.Duration
	AutoRegister    bool
	MaskPII         bool
	LedgerURL       string
	LedgerToken     string
	LedgerCache     time.Duration
	CircuitFailures int
	CircuitOpen     time.Duration
	Bootstrap       BootstrapConfig
	// MaxBodyBytes 请求体上限（字节）；超限 ⇒ HTTP 413 + -32004。
	MaxBodyBytes int
	// ControlScopeDisabled 控制面 scope 鉴权被关闭（只认管理令牌）。
	ControlScopeDisabled bool
	// RegistrySync 联邦同步配置（ADR 2026-11-27 §3）；Enabled=false 时零值即不装配。
	RegistrySync RegistrySyncConfig
	// RegistryJWT 出站 service JWT 签发配置（ADR §4 D2a）；Secret 为空即不装配签发器。
	RegistryJWT RegistryJWTConfig
	// LocalTools 是网关本地工具注册表（由网关自己执行，不经上游适配器）。
	//
	// ★ 零值 / nil ⇒ 没有任何本地工具 ⇒ 行为与引入本字段前完全一致。
	//   装载方（cmd/gateway）负责填，因为工具的执行依赖网关自己的
	//   装配件（会话库 / 记忆库），mcp 包不该知道它们。
	LocalTools *LocalToolRegistry
}

// LoadEnvConfig 解析环境变量。
func LoadEnvConfig() (EnvConfig, error) {
	cfg := EnvConfig{
		Enabled:         envBool(EnvEnabled, true),
		StoreFile:       strings.TrimSpace(os.Getenv(EnvStoreFile)),
		AuditFile:       strings.TrimSpace(os.Getenv(EnvAuditFile)),
		AdminToken:      strings.TrimSpace(os.Getenv(EnvAdminToken)),
		DefaultTenantID: strings.TrimSpace(os.Getenv(EnvDefaultTenant)),
		RateLimitPerMin: envInt(EnvRateLimitPerMin, 0),
		ReadLimit:       envInt(EnvReadLimit, 120),
		WriteLimit:      envInt(EnvWriteLimit, 30),
		LLMLimit:        envInt(EnvLLMLimit, 10),
		ToolTimeout:     time.Duration(envInt(EnvToolTimeoutMS, 30000)) * time.Millisecond,
		MaxBodyBytes:    envInt(EnvMaxBodyBytes, 1<<20),
		AutoRegister:    envBool(EnvAutoRegister, true),
		MaskPII:         envBool(EnvMaskPII, true),
		LedgerURL:       strings.TrimSpace(os.Getenv(EnvLedgerURL)),
		LedgerToken:     strings.TrimSpace(os.Getenv(EnvLedgerToken)),
		LedgerCache:     time.Duration(envInt(EnvLedgerCacheMS, 10000)) * time.Millisecond,
		CircuitFailures: envInt(EnvCircuitFailures, 5),
		CircuitOpen:     time.Duration(envInt(EnvCircuitOpenSec, 30)) * time.Second,
		Bootstrap: BootstrapConfig{
			Enabled:            envBool(EnvBootstrapEnabled, false),
			ApplyIPLimit:       envInt(EnvBootstrapApplyIPLimit, 10),
			ApplyDeviceLimit:   envInt(EnvBootstrapApplyDevLimit, 3),
			ReadIPLimit:        envInt(EnvBootstrapReadIPLimit, 30),
			IdempotencyTTL:     15 * time.Minute,
			AutoApprove:        envBool(EnvBootstrapAutoApprove, true),
			AutoApproveDevices: splitList(os.Getenv(EnvBootstrapAutoApproveDev)),
			TenantID:           strings.TrimSpace(os.Getenv(EnvDefaultTenant)),
		},
		ControlScopeDisabled: !envBool(EnvControlScope, true),
		RegistrySync: RegistrySyncConfig{
			Enabled:  envBool(EnvRegistrySyncEnabled, false),
			URL:      strings.TrimSpace(os.Getenv(EnvRegistryURL)),
			Token:    os.Getenv(EnvRegistryToken),
			Interval: time.Duration(envInt(EnvRegistryIntervalSeconds, 60)) * time.Second,
		},
		RegistryJWT: RegistryJWTConfig{
			Secret:   os.Getenv(EnvRegistryJWTSecret),
			Issuer:   strings.TrimSpace(os.Getenv(EnvRegistryJWTIssuer)),
			Audience: strings.TrimSpace(os.Getenv(EnvRegistryJWTAudience)),
		},
	}
	if cfg.DefaultTenantID == "" {
		cfg.DefaultTenantID = "default"
	}
	if cfg.ToolTimeout <= 0 {
		cfg.ToolTimeout = 30 * time.Second
	}
	if cfg.LedgerURL != "" && cfg.LedgerToken == "" {
		return EnvConfig{}, fmt.Errorf("mcp: %s is set but %s is empty", EnvLedgerURL, EnvLedgerToken)
	}
	// 联邦装配校验（ADR §3）：开同步必须有 URL 与 Token；配了 JWT secret 则
	// audience 必须显式给（aud 必须与 memora verifier 同源，猜默认=鉴权错位）。
	if cfg.RegistrySync.Enabled {
		if cfg.RegistrySync.URL == "" {
			return EnvConfig{}, fmt.Errorf("mcp: %s is set but %s is empty", EnvRegistrySyncEnabled, EnvRegistryURL)
		}
		if cfg.RegistrySync.Token == "" {
			return EnvConfig{}, fmt.Errorf("mcp: %s is set but %s is empty", EnvRegistrySyncEnabled, EnvRegistryToken)
		}
		if cfg.RegistrySync.Interval <= 0 {
			cfg.RegistrySync.Interval = 60 * time.Second
		}
	}
	if cfg.RegistryJWT.Secret != "" && cfg.RegistryJWT.Audience == "" {
		return EnvConfig{}, fmt.Errorf("mcp: %s is set but %s is empty (audience must match memora %s)", EnvRegistryJWTSecret, EnvRegistryJWTAudience, "V2JWT_AUDIENCE")
	}
	return cfg, nil
}

// Build 按环境配置装配网关。
func (e EnvConfig) Build() (*Gateway, error) {
	store := Store(NewMemoryStore())
	if e.StoreFile != "" {
		fs, err := NewFileStore(e.StoreFile)
		if err != nil {
			return nil, err
		}
		store = fs
	}
	audit, err := NewAuditLog(200, e.AuditFile)
	if err != nil {
		return nil, err
	}
	ledger := Ledger(NewLocalLedger(store))
	if e.LedgerURL != "" {
		ledger = NewRemoteLedger(e.LedgerURL, e.LedgerToken, e.LedgerCache, 2*time.Second)
	}
	cfg := Config{
		Store:                  store,
		Ledger:                 ledger,
		Audit:                  audit,
		AdminToken:             e.AdminToken,
		DefaultTenantID:        e.DefaultTenantID,
		DefaultRateLimitPerMin: e.RateLimitPerMin,
		ClassRateLimits: map[ToolClass]int{
			ClassRead:  e.ReadLimit,
			ClassWrite: e.WriteLimit,
			ClassLLM:   e.LLMLimit,
		},
		ToolTimeout:          e.ToolTimeout,
		MaxBodyBytes:         int64(e.MaxBodyBytes),
		AutoRegisterTools:    e.AutoRegister,
		Redactor:             NewRedactor(e.MaskPII),
		Breaker:              NewCircuitBreaker(e.CircuitFailures, e.CircuitOpen),
		Bootstrap:            e.Bootstrap,
		ControlScopeDisabled: e.ControlScopeDisabled,
		LocalTools:           e.LocalTools,
	}
	g, err := NewGateway(cfg)
	if err != nil {
		return nil, err
	}
	// 联邦装配（ADR 2026-11-27 §4/§3）：出站 JWT 签发器注入 UpstreamClient；
	// 同步器挂 Gateway 供控制面取状态（驱动在 cmd/gateway 装配面 goroutine）。
	if e.RegistryJWT.Secret != "" {
		issuer, err := NewServiceJWTIssuer(e.RegistryJWT.Secret, e.RegistryJWT.Issuer, e.RegistryJWT.Audience, 0)
		if err != nil {
			return nil, err
		}
		g.upstream.SetServiceJWTIssuer(issuer)
	}
	if e.RegistrySync.Enabled {
		g.registrySync = NewRegistrySyncer(e.RegistrySync, store)
	}
	if err := g.LoadCatalog(context.Background(), e.DefaultTenantID); err != nil {
		return nil, err
	}
	return g, nil
}

// splitList 解析逗号分隔的列表。
func splitList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func envBool(key string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}

func envInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
