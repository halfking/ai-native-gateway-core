// Package pluginruntime 是 Gateway 侧的插件运行时：发现、校验、启动、握手、
// 健康检查、菜单注册与 HTTP 反向代理。P0 只实现内存态骨架。
package pluginruntime

import "time"

const SupportedAPIContract = "gateway-plugin-v1"

type Manifest struct {
	SchemaVersion        int                  `json:"schema_version"`
	PluginID             string               `json:"plugin_id"`
	DisplayName          string               `json:"display_name"`
	PluginVersion        string               `json:"plugin_version"`
	BuildSeq             int                  `json:"build_seq"`
	Channel              string               `json:"channel"`
	GatewayCompatibility GatewayCompatibility `json:"gateway_compatibility"`
	Runtime              Runtime              `json:"runtime"`
	Capabilities         []string             `json:"capabilities"`
	Pages                []Page               `json:"pages"`
	Web                  Web                  `json:"web"`
	Activation           Activation           `json:"activation"`
}

type GatewayCompatibility struct {
	MinVersion  string `json:"min_version"`
	MaxVersion  string `json:"max_version"`
	APIContract string `json:"api_contract"`
}

type Runtime struct {
	Entrypoint        string `json:"entrypoint"`
	Protocol          string `json:"protocol"`
	HealthPath        string `json:"health_path"`
	HandshakePath     string `json:"handshake_path"`
	ShutdownGraceSecs int    `json:"shutdown_grace_seconds"`
}

type Nav struct {
	Group       string `json:"group"`
	LabelKey    string `json:"label_key"`
	Super       bool   `json:"super"`
	PlatformOps bool   `json:"platform_ops"`
	TenantOnly  bool   `json:"tenant_only"`
	Order       int    `json:"order"`
}

type Page struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Nav  *Nav   `json:"nav"`
}

type Web struct {
	Mount    string `json:"mount"`
	BasePath string `json:"base_path"`
	Entry    string `json:"entry"`
}

type Activation struct {
	ModuleKey       string `json:"module_key"`
	LicenseRequired bool   `json:"license_required"`
}

type HandshakeResponse struct {
	PluginID      string   `json:"plugin_id"`
	PluginVersion string   `json:"plugin_version"`
	APIContract   string   `json:"api_contract"`
	Status        string   `json:"status"`
	Capabilities  []string `json:"capabilities"`
	TenantMode    string   `json:"tenant_mode"`
	EventMode     string   `json:"event_mode"`
}

type PluginState struct {
	PluginID      string
	PluginVersion string
	Status        string // discovered | starting | ready | degraded | stopped
	StartedAt     time.Time
	LastHealth    time.Time
	SocketPath    string
	Pid           int // 进程 PID，0 表示未启动
}

// NavEntry 是 plugin_nav 表的一行，返回给 web。
type NavEntry struct {
	PluginID      string `json:"plugin_id"`
	PluginVersion string `json:"plugin_version"`
	PagePath      string `json:"page_path"`
	PageType      string `json:"page_type"`
	NavGroup      string `json:"nav_group"`
	LabelKey      string `json:"label_key"`
	Icon          string `json:"icon,omitempty"`
	Super         bool   `json:"super"`
	PlatformOps   bool   `json:"platform_ops"`
	TenantOnly    bool   `json:"tenant_only"`
	Order         int    `json:"order"`
	RouteURL      string `json:"route_url"`
}
