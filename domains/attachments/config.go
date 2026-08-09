package attachments

import (
	"os"
	"strings"
)

// Config 附件访问配置。
type Config struct {
	// AuthMode 认证模式（none/apikey/signed/admin）
	AuthMode AuthMode

	// PublicURL 公开访问 URL 前缀（用于生成下载链接）
	// 留空则使用 /api/attachments/（默认）
	// 配置示例：https://cdn.example.com/attachments
	PublicURL string

	// CORSOrigins 允许的跨域来源（逗号分隔）
	// * 表示允许所有来源
	CORSOrigins []string
}

// LoadConfigFromEnv 从环境变量加载配置。
func LoadConfigFromEnv() *Config {
	cfg := &Config{
		AuthMode:  AuthModeNone, // 默认无认证（向后兼容）
		PublicURL: "/api/attachments/",
	}

	// LLM_GATEWAY_ATTACHMENT_AUTH_MODE
	if mode := os.Getenv("LLM_GATEWAY_ATTACHMENT_AUTH_MODE"); mode != "" {
		cfg.AuthMode = AuthMode(strings.ToLower(mode))
	}

	// LLM_GATEWAY_ATTACHMENT_PUBLIC_URL
	if publicURL := os.Getenv("LLM_GATEWAY_ATTACHMENT_PUBLIC_URL"); publicURL != "" {
		cfg.PublicURL = strings.TrimSuffix(publicURL, "/") + "/"
	}

	// LLM_GATEWAY_ATTACHMENT_CORS_ORIGINS
	if origins := os.Getenv("LLM_GATEWAY_ATTACHMENT_CORS_ORIGINS"); origins != "" {
		cfg.CORSOrigins = strings.Split(origins, ",")
		for i := range cfg.CORSOrigins {
			cfg.CORSOrigins[i] = strings.TrimSpace(cfg.CORSOrigins[i])
		}
	}

	return cfg
}
