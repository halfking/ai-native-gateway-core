package mcp

import (
	"encoding/json"
	"time"
)

// Visibility 工具可见性（ContextForge 的 L1 可见性层）。
type Visibility string

const (
	// VisibilityPrivate 仅绑定主体（key 所属 agent/组织）可见。
	VisibilityPrivate Visibility = "private"
	// VisibilityTeam 指定团队可见。
	VisibilityTeam Visibility = "team"
	// VisibilityPublic 所有已认证主体可见（仍受 RBAC 与限流约束）。
	VisibilityPublic Visibility = "public"
)

// ValidVisibility 报告 v 是否为受支持的可见性取值。
func (v Visibility) ValidVisibility() bool {
	switch v {
	case VisibilityPrivate, VisibilityTeam, VisibilityPublic:
		return true
	default:
		return false
	}
}

// Sensitivity 工具敏感度分级（对应 PRD 的 只读 / 写入 / 高敏）。
type Sensitivity string

const (
	// SensitivityRead 只读工具：可自助开通，限流额度高。
	SensitivityRead Sensitivity = "read"
	// SensitivityWrite 写入工具：需人工审批，默认不豁免。
	SensitivityWrite Sensitivity = "write"
	// SensitivityHigh 高敏工具：需人工审批 + 更严限流 + 出参脱敏。
	SensitivityHigh Sensitivity = "high"
)

// ValidSensitivity 报告 s 是否为受支持的敏感度取值。
func (s Sensitivity) ValidSensitivity() bool {
	switch s {
	case SensitivityRead, SensitivityWrite, SensitivityHigh:
		return true
	default:
		return false
	}
}

// ToolClass 工具调用类别，决定默认限流档位。
type ToolClass string

const (
	// ClassRead 读类工具。
	ClassRead ToolClass = "read"
	// ClassWrite 写类工具。
	ClassWrite ToolClass = "write"
	// ClassLLM 工具内部会调用大模型（成本高、延迟大）。
	ClassLLM ToolClass = "llm"
)

// ValidToolClass 报告 c 是否为受支持的类别。
func (c ToolClass) ValidToolClass() bool {
	switch c {
	case ClassRead, ClassWrite, ClassLLM:
		return true
	default:
		return false
	}
}

// Adapter 是一个被网关纳管的 MCP 上游服务（原 tools/mcp-gateway 的「路径分流
// 目标」的泛化：路径前缀 → 具名适配器）。
type Adapter struct {
	// Name 唯一名（注册面标识），同时作为工具命名空间前缀。
	Name string `json:"name"`
	// TenantID 所属租户，空表示默认租户。
	TenantID string `json:"tenant_id,omitempty"`
	// DisplayName 展示名。
	DisplayName string `json:"display_name,omitempty"`
	// Description 说明。
	Description string `json:"description,omitempty"`
	// Endpoint 上游 MCP 服务基址（http/https）。
	Endpoint string `json:"endpoint"`
	// ProtocolVersion 上游 MCP 协议版本，默认 "2026-07-28"。
	ProtocolVersion string `json:"protocol_version,omitempty"`
	// Headers 转发到上游的静态请求头（如租户标识）。
	Headers map[string]string `json:"headers,omitempty"`
	// AuthMode 上游鉴权模式：none / bearer / apikey。
	AuthMode string `json:"auth_mode,omitempty"`
	// AuthSecret 上游凭据。对外输出时一律脱敏。
	AuthSecret string `json:"-"`
	// Capability 该适配器所属能力域（memory / code / agent ...），用于能力路由。
	Capability string `json:"capability,omitempty"`
	// Sensitivity 该适配器整体敏感度（工具可单独覆盖）。
	Sensitivity Sensitivity `json:"sensitivity,omitempty"`
	// Enabled 是否启用。
	Enabled bool `json:"enabled"`
	// TimeoutMS 上游调用超时（毫秒）。
	TimeoutMS int `json:"timeout_ms,omitempty"`
	// MaxConcurrent 并发上限，0 表示不限。
	MaxConcurrent int `json:"max_concurrent,omitempty"`
	// RateLimitPerMin 每分钟请求上限（对所有 key 合计），0 表示不限。
	RateLimitPerMin int `json:"rate_limit_per_min,omitempty"`
	// CreatedAt / UpdatedAt。
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Tool 是一个被网关暴露的工具定义。
type Tool struct {
	// Name 对外暴露的规范化名称（<adapter>__<tool> 或显式注册名）。
	Name string `json:"name"`
	// RawName 上游原始工具名。
	RawName string `json:"raw_name,omitempty"`
	// Adapter 归属适配器名。
	Adapter string `json:"adapter"`
	// TenantID 所属租户。
	TenantID string `json:"tenant_id,omitempty"`
	// Description 描述（进入客户端上下文）。
	Description string `json:"description,omitempty"`
	// InputSchema JSON Schema（入参白名单与校验依据）。
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	// Visibility 可见性。
	Visibility Visibility `json:"visibility,omitempty"`
	// Teams 可见团队（Visibility=team 时生效）。
	Teams []string `json:"teams,omitempty"`
	// Sensitivity 敏感度分级。
	Sensitivity Sensitivity `json:"sensitivity,omitempty"`
	// Class 调用类别，决定限流档位。
	Class ToolClass `json:"class,omitempty"`
	// AllowedParams 参数白名单：非空时只允许这些参数出现在调用参数里。
	AllowedParams []string `json:"allowed_params,omitempty"`
	// RequireApproval 是否必须携带已批准的审批凭据才能调用。
	RequireApproval bool `json:"require_approval,omitempty"`
	// AgentID 私有工具归属的智能体（visibility=private 时按此收口）。
	AgentID string `json:"agent_id,omitempty"`
	// Enabled 是否启用。
	Enabled bool `json:"enabled"`
	// Group 工具分组名（可按组开独立端点以抑制上下文膨胀）。
	Group string `json:"group,omitempty"`
	// RateLimitPerMin 单 key 单工具每分钟上限，0 表示沿用类别默认值。
	RateLimitPerMin int `json:"rate_limit_per_min,omitempty"`
	// CreatedAt / UpdatedAt。
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Agent 是智能体定义（模型 + 允许工具清单 + 绑定的凭证）。
type Agent struct {
	ID           string `json:"id"`
	TenantID     string `json:"tenant_id,omitempty"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	SystemPrompt string `json:"system_prompt,omitempty"`
	Model        string `json:"model,omitempty"`
	// AllowedTools 允许的工具名/模式（支持 `*` 通配），空表示不额外收口。
	AllowedTools []string `json:"allowed_tools,omitempty"`
	// KeyIDs 绑定的凭证 ID。
	KeyIDs    []string  `json:"key_ids,omitempty"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Skill 是技能定义（manifest + 绑定工具 + 入口）。
type Skill struct {
	ID          string `json:"id"`
	TenantID    string `json:"tenant_id,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Manifest 技能清单（SKILL.md 语义的结构化形式）。
	Manifest json.RawMessage `json:"manifest,omitempty"`
	// Tools 技能可调用的工具名/模式。
	Tools []string `json:"tools,omitempty"`
	// Entry 技能入口（MCP 端点或内部命令名）。
	Entry     string    `json:"entry,omitempty"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Key 是一条被网关签发的访问凭证（入站身份）。
type Key struct {
	ID       string `json:"id"`
	TenantID string `json:"tenant_id,omitempty"`
	// OrgID 组织/部门标识（离职回收与按组织统计的落点）。
	OrgID string `json:"org_id,omitempty"`
	// UserID 主体标识。
	UserID string `json:"user_id,omitempty"`
	Name   string `json:"name,omitempty"`
	// Prefix 明文前缀（便于人工识别，不参与校验）。
	Prefix string `json:"prefix,omitempty"`
	// Hash 明文 SHA-256 十六进制；明文只在签发/轮换时返回一次。
	Hash string `json:"-"`
	// Status active / inactive / revoked。
	Status string `json:"status"`
	// Scopes 角色位：mcp.read / mcp.execute / mcp.admin。
	Scopes []string `json:"scopes,omitempty"`
	// AllowedTools 允许的工具名/模式（空表示不额外收口，交由工具可见性裁决）。
	AllowedTools []string `json:"allowed_tools,omitempty"`
	// DeniedTools 拒绝的工具名/模式（优先级高于 AllowedTools）。
	DeniedTools []string `json:"denied_tools,omitempty"`
	// AllowedAdapters 允许访问的适配器（空表示全部）。
	AllowedAdapters []string `json:"allowed_adapters,omitempty"`
	// Bindings 一 key 多后端单边禁用：map[backend]bool，false 表示禁用。
	Bindings map[string]bool `json:"bindings,omitempty"`
	// AgentID 绑定的智能体。
	AgentID string `json:"agent_id,omitempty"`
	// ToolAllowEnforce 是否强制执行 key×工具允许表（外部账本语义）。
	ToolAllowEnforce bool `json:"tool_allow_enforce,omitempty"`
	// ExpiresAt 过期时间。
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// CreatedAt / LastUsedAt。
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	// RevokedAt 吊销时间。
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

// Active 报告凭证是否可用（状态有效且未过期）。
func (k *Key) Active(now time.Time) bool {
	if k == nil {
		return false
	}
	if k.Status != StatusActive {
		return false
	}
	if k.ExpiresAt != nil && now.After(*k.ExpiresAt) {
		return false
	}
	return true
}

// HasScope 报告是否具备指定 scope。
//
// ★ `mcp.admin` 是超集：持有它的 key 具备全部 scope（既有语义，逐字不变）。
func (k *Key) HasScope(scope string) bool {
	if k == nil {
		return false
	}
	for _, s := range k.Scopes {
		if s == scope || s == ScopeAdmin {
			return true
		}
	}
	return false
}

// 凭证状态常量。
const (
	StatusActive   = "active"
	StatusInactive = "inactive"
	StatusRevoked  = "revoked"
)

// Scope 角色位常量。
//
// ★ 契约纪律：**既有三位逐字不动**（mcp.read / mcp.execute / mcp.admin）——
//
//	既有客户端/外部账本按 scope 字面值判分支，改名即破坏契约。
//
// ★ 新增的四位是**控制面**能力位（业务 key 按细粒度权限访问控制面）：
//
//	mcp.register  注册面：创建/查看适配器、工具、智能体、技能
//	mcp.manage    管理面：变更/删除注册项、签发/吊销/轮换凭证、审批裁决
//	mcp.analyze   统计分析面：审计查询/导出、路由清单、能力目录、指标快照
//	mcp.topology  内部拓扑面：/health 的上游端点与熔断态（**加严子位**）
//
// ★ 派生规则（HasScope 天然实现）：
//   - `mcp.admin` 是**超集**：隐含以上全部能力位（既有逻辑不变）。
//   - 其余各位**互相不派生**（正交）：analyze 不含 topology；register 不含
//     manage。少一项就是少一项权限 ⇒ 运营侧可以精确授权。
const (
	// ScopeRead 只读元数据（tools/list 等）。
	ScopeRead = "mcp.read"
	// ScopeExecute 执行工具。
	ScopeExecute = "mcp.execute"
	// ScopeAdmin 控制面管理（超集：隐含全部其它 scope）。
	ScopeAdmin = "mcp.admin"

	// ScopeRegister 注册面能力位。
	ScopeRegister = "mcp.register"
	// ScopeManage 管理面能力位。
	ScopeManage = "mcp.manage"
	// ScopeAnalyze 统计分析能力位。
	ScopeAnalyze = "mcp.analyze"
	// ScopeTopology 内部拓扑能力位（/health 这类暴露拓扑的面）。
	ScopeTopology = "mcp.topology"
)

// ControlScopes 返回网关支持的全部 scope（RFC 9728 元数据与文档口径用）。
func ControlScopes() []string {
	return []string{
		ScopeRead, ScopeExecute, ScopeAdmin,
		ScopeRegister, ScopeManage, ScopeAnalyze, ScopeTopology,
	}
}

// adminScopes 返回管理令牌对应的 scope 集（超集）。
func adminScopes() []string {
	return ControlScopes()
}

// Approval 是敏感工具的审批凭据（对应 PRD 的分级审批）。
type Approval struct {
	ID       string `json:"id"`
	TenantID string `json:"tenant_id,omitempty"`
	Tool     string `json:"tool"`
	// Requester 申请人（key 的主体）。
	Requester string `json:"requester,omitempty"`
	// Reason 申请理由。
	Reason string `json:"reason,omitempty"`
	Status string `json:"status"`
	// ExpiresAt 审批凭据过期时间。
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	DecidedAt *time.Time `json:"decided_at,omitempty"`
	DecidedBy string     `json:"decided_by,omitempty"`
}

// 审批状态常量。
const (
	ApprovalPending  = "pending"
	ApprovalApproved = "approved"
	ApprovalRejected = "rejected"
)

// AuditEvent 是一条审计事件（可归因：谁、在何时、调了什么、结果如何）。
type AuditEvent struct {
	At time.Time `json:"at"`
	// Kind 事件类别：call / authz / ratelimit / key / admin / upstream。
	Kind string `json:"kind"`
	// Outcome 结果：allowed / denied / error。
	Outcome string `json:"outcome"`
	// TenantID / OrgID / UserID / KeyID 归因维度。
	TenantID string `json:"tenant_id,omitempty"`
	OrgID    string `json:"org_id,omitempty"`
	UserID   string `json:"user_id,omitempty"`
	KeyID    string `json:"key_id,omitempty"`
	AgentID  string `json:"agent_id,omitempty"`
	// Tool 工具名（规范化后）。
	Tool string `json:"tool,omitempty"`
	// Adapter 上游适配器。
	Adapter string `json:"adapter,omitempty"`
	// Method JSON-RPC 方法名。
	Method string `json:"method,omitempty"`
	// RequestID 请求标识（链路对账用）。
	RequestID string `json:"request_id,omitempty"`
	// Reason 拒绝/失败原因码。
	Reason string `json:"reason,omitempty"`
	// LatencyMS 调用耗时。
	LatencyMS int64 `json:"latency_ms,omitempty"`
	// Status HTTP 状态码。
	Status int `json:"status,omitempty"`
	// Detail 附加详情（已脱敏）。
	Detail map[string]any `json:"detail,omitempty"`
}

// 审计事件类别与结果常量。
const (
	AuditKindCall     = "call"
	AuditKindAuthz    = "authz"
	AuditKindRate     = "ratelimit"
	AuditKindKey      = "key"
	AuditKindAdmin    = "admin"
	AuditKindUpstream = "upstream"

	OutcomeAllowed = "allowed"
	OutcomeDenied  = "denied"
	OutcomeError   = "error"
)

// ToolView 是 tools/list 对客户端暴露的工具视图。
type ToolView struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	// Annotations MCP 注解（readOnly 等）。
	Annotations map[string]any `json:"annotations,omitempty"`
	// Meta 网关附加元数据（敏感度、能力域、归属适配器）。
	Meta map[string]any `json:"_meta,omitempty"`
}
