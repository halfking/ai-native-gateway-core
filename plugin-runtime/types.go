// Package pluginruntime 是 Gateway 侧的插件运行时：发现、校验、启动、握手、
// 健康检查、菜单注册与 HTTP 反向代理。P0 只实现内存态骨架。
package pluginruntime

import "time"

const (
	SupportedAPIContract   = "gateway-plugin-v1"
	SupportedAPIContractV2 = "gateway-plugin-v2"
)

// License declares the optional activation licensing policy in manifest v2.
type License struct {
	Mode     string   `json:"mode,omitempty"`
	Required bool     `json:"required,omitempty"`
	Features []string `json:"features,omitempty"`
}

// ConfigSchema is intentionally opaque at the wire level; manifest validation
// checks the bounded JSON-Schema subset before it is exposed to a UI.
type ConfigSchema map[string]any

// BindingPhase identifies the lifecycle seam where a plugin binding runs.
type BindingPhase string

const (
	PhaseRequest      BindingPhase = "request"
	PhaseGovernance   BindingPhase = "governance"
	PhaseTransform    BindingPhase = "transform"
	PhaseTool         BindingPhase = "tool"
	PhaseResponse     BindingPhase = "response"
	PhaseStream       BindingPhase = "stream"
	PhaseAnalysis     BindingPhase = "analysis"
	PhaseSessionClose BindingPhase = "session_close"
)

// ExecutionMode controls whether bindings at one seam are invoked serially or concurrently.
type ExecutionMode string

const (
	ExecutionSequential ExecutionMode = "sequential"
	ExecutionParallel   ExecutionMode = "parallel"
)

// FailurePolicy is explicit per binding; hook defaults must not be inferred implicitly.
type FailurePolicy string

const (
	FailureOpen     FailurePolicy = "fail_open"
	FailureClosed   FailurePolicy = "fail_closed"
	FailureSuspend  FailurePolicy = "suspend"
	FailureRetryDLQ FailurePolicy = "retry_dlq"
)

// DTOProfile controls the maximum request data exposed to a plugin.
type DTOProfile string

const (
	DTOProfileNone     DTOProfile = "none"
	DTOProfileSummary  DTOProfile = "summary"
	DTOProfileRedacted DTOProfile = "redacted"
)

// Capabilities are intentionally allowlisted. Unknown values fail closed.
const (
	CapabilityRequestObserve  = "request.observe"
	CapabilityRequestMutate   = "request.mutate"
	CapabilityRequestBlock    = "request.block"
	CapabilitySessionObserve  = "session.observe"
	CapabilitySessionClose    = "session.close"
	CapabilityResponseObserve = "response.observe"
	CapabilityResponseMutate  = "response.mutate"
	CapabilityResponseBlock   = "response.block"
	CapabilityResponseStream  = "response.stream"
	CapabilityToolObserve     = "tool.observe"
	CapabilityToolMutate      = "tool.mutate"
	CapabilityToolBlock       = "tool.block"
	CapabilityToolExecute     = "tool.execute"
	CapabilityAuditEmit       = "audit.emit"
	CapabilityDurableConsume  = "durable.consume"
)

// PluginBinding declares a lifecycle attachment. It is separate from the menu/status registry.
type PluginBinding struct {
	PluginID         string        `json:"plugin_id,omitempty"`
	BindingID        string        `json:"binding_id"`
	Phase            BindingPhase  `json:"phase"`
	Priority         int           `json:"priority"`
	ExecutionMode    ExecutionMode `json:"execution_mode"`
	Capabilities     []string      `json:"capabilities"`
	TenantScope      []string      `json:"tenant_scope,omitempty"`
	ModelScope       []string      `json:"model_scope,omitempty"`
	TimeoutMillis    int           `json:"timeout_ms"`
	ConcurrencyLimit int           `json:"concurrency_limit"`
	FailurePolicy    FailurePolicy `json:"failure_policy"`
	DTOProfile       DTOProfile    `json:"dto_profile"`
	Enabled          bool          `json:"enabled"`
}

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
	Permissions          []string             `json:"permissions,omitempty"`
	Hooks                []string             `json:"hooks,omitempty"`
	ConfigSchema         ConfigSchema         `json:"config_schema,omitempty"`
	Bindings             []PluginBinding      `json:"bindings,omitempty"`
	Pages                []Page               `json:"pages"`
	Web                  Web                  `json:"web"`
	Activation           Activation           `json:"activation"`
	// ManifestPath 是 manifest 文件的绝对路径，由 LoadManifest 填充；不参与 JSON 序列化。
	ManifestPath string `json:"-"`
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
	ModuleKey       string  `json:"module_key"`
	LicenseRequired bool    `json:"license_required"`
	License         License `json:"license,omitempty"`
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
	Status        string // discovered | starting | ready | degraded | failed | stopped
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
