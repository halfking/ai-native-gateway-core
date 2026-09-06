// Package promptoptimization 实现提示词优化 Hook。
//
// 在请求发送到 LLM 之前，把 system/user prompt 发给 prompt-optimizer-service
// 优化，再把优化后的内容写回请求体。设计要点：
//   - 默认关闭（PROMPT_OPTIMIZATION_ENABLED=1 显式开启）
//   - 基于 SHA256 的结果缓存，避免重复优化相同 prompt
//   - 优化失败时回退到原始 prompt（优化是增强，不是依赖）
//   - 优化前后对比写入 env.Metadata["prompt_optimization"] 供审计
//
// 参考 compression hook 的实现模式：实现 pipeline.Hook 接口，
// 在 PhaseTransform 阶段、compression 之前执行（priority 90 < 100）。
package promptoptimization

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// 环境变量名（配置契约，与 docs/hooks/prompt-optimization.md 保持一致）。
const (
	EnvEnabled         = "PROMPT_OPTIMIZATION_ENABLED"          // 默认 false
	EnvOptimizerURL    = "PROMPT_OPTIMIZER_URL"                 // 默认 http://prompt-optimizer:8090
	EnvCacheTTL        = "OPTIMIZATION_CACHE_TTL"               // 默认 24h
	EnvTimeout         = "OPTIMIZATION_TIMEOUT"                 // 默认 5s
	EnvMode            = "OPTIMIZATION_MODE"                    // system|user|both，默认 both
	EnvModelWhitelist  = "OPTIMIZATION_MODEL_WHITELIST"         // 逗号分隔，空=全部允许
	EnvModelBlacklist  = "OPTIMIZATION_MODEL_BLACKLIST"         // 逗号分隔，优先于白名单
	EnvTenantAllowlist = "OPTIMIZATION_TENANTS"                 // 逗号分隔，空=所有租户
)

// Mode 控制优化哪些角色的 prompt。
type Mode string

const (
	// ModeSystem 只优化 system prompt。
	ModeSystem Mode = "system"
	// ModeUser 只优化 user prompt。
	ModeUser Mode = "user"
	// ModeBoth 优化 system 和 user prompt（默认）。
	ModeBoth Mode = "both"
)

// Covers 报告该模式是否覆盖指定角色。
func (m Mode) Covers(role string) bool {
	switch m {
	case ModeSystem:
		return role == "system"
	case ModeUser:
		return role == "user"
	case ModeBoth:
		return role == "system" || role == "user"
	default:
		return false
	}
}

// IsValid 报告模式是否合法。
func (m Mode) IsValid() bool {
	switch m {
	case ModeSystem, ModeUser, ModeBoth:
		return true
	default:
		return false
	}
}

// Config Hook 配置。
type Config struct {
	// Enabled 总开关（默认 false，需要显式开启）。
	Enabled bool
	// OptimizerURL prompt-optimizer-service 基地址。
	OptimizerURL string
	// CacheTTL 优化结果缓存 TTL。
	CacheTTL time.Duration
	// Timeout 单次优化调用超时。
	Timeout time.Duration
	// Mode 优化 system/user/both。
	Mode Mode
	// ModelWhitelist 模型白名单（空=全部允许）。
	ModelWhitelist []string
	// ModelBlacklist 模型黑名单（优先于白名单）。
	ModelBlacklist []string
	// TenantAllowlist 租户白名单（空=所有租户；租户级开关）。
	TenantAllowlist []string
}

// DefaultConfig 返回默认配置（对应 handoff 中的配置契约）。
func DefaultConfig() *Config {
	return &Config{
		Enabled:      false,
		OptimizerURL: "http://prompt-optimizer:8090",
		CacheTTL:     24 * time.Hour,
		Timeout:      5 * time.Second,
		Mode:         ModeBoth,
	}
}

// FromEnv 从环境变量构建配置，非法值回退默认（fail-safe）。
func FromEnv() *Config {
	cfg := DefaultConfig()

	if v := strings.TrimSpace(os.Getenv(EnvEnabled)); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Enabled = b
		}
	}
	if v := strings.TrimSpace(os.Getenv(EnvOptimizerURL)); v != "" {
		cfg.OptimizerURL = strings.TrimRight(v, "/")
	}
	if v := strings.TrimSpace(os.Getenv(EnvCacheTTL)); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.CacheTTL = d
		}
	}
	if v := strings.TrimSpace(os.Getenv(EnvTimeout)); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.Timeout = d
		}
	}
	if v := Mode(strings.TrimSpace(os.Getenv(EnvMode))); v != "" {
		if v.IsValid() {
			cfg.Mode = v
		}
	}
	cfg.ModelWhitelist = splitCSV(os.Getenv(EnvModelWhitelist))
	cfg.ModelBlacklist = splitCSV(os.Getenv(EnvModelBlacklist))
	cfg.TenantAllowlist = splitCSV(os.Getenv(EnvTenantAllowlist))
	return cfg
}

// modelAllowed 判断模型是否允许优化。黑名单优先于白名单；
// 两个名单都为空表示全部允许。
func (c *Config) modelAllowed(model string) bool {
	if containsFold(c.ModelBlacklist, model) {
		return false
	}
	if len(c.ModelWhitelist) == 0 {
		return true
	}
	return containsFold(c.ModelWhitelist, model)
}

// tenantAllowed 判断租户是否允许优化（租户级开关，空名单=全部允许）。
func (c *Config) tenantAllowed(tenantID string) bool {
	if len(c.TenantAllowlist) == 0 {
		return true
	}
	return containsFold(c.TenantAllowlist, tenantID)
}

// splitCSV 解析逗号分隔的字符串为去空白、去空的切片。
func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// containsFold 大小写不敏感的包含判断。
func containsFold(list []string, v string) bool {
	for _, item := range list {
		if strings.EqualFold(item, v) {
			return true
		}
	}
	return false
}
