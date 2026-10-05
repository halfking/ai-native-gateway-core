// Package apihub implements the unified asset registry for the AI & Agent
// Gateway. It consolidates LLM endpoints (model_offers / api_keys), MCP
// servers (registry/), and Agents (Q1 2027) into a single "asset" table so
// that downstream features — topology, discovery, billing, health — have one
// consistent interface.
//
// Domain boundary (see docs/产品方案/2026-06-23-llmgw-domain-architecture-refactor.md):
//   - apihub OWNS: asset identity (kind, ref_id), topology (relationships),
//     health_state aggregation, in-process cache.
//   - apihub does NOT own: MCP protocol translation (mcp/ package),
//     Agent execution (agent_registry/ package), request relaying (relay/).
//
// Multi-tenancy: every Asset carries tenant_id and the DB tables enforce RLS
// (see db/migrations/047_apihub_assets.sql). The Service never reads across
// tenants — queries always scope by the tenant_id from context.
package apihub

import (
	"errors"
	"time"
)

// Kind is the category of an asset. It identifies which source table the
// ref_id points into (model_offers / tool_registry.tools / future agents).
type Kind string

const (
	KindLLMEndpoint Kind = "llm_endpoint"
	KindMCPServer   Kind = "mcp_server"
	KindAgent       Kind = "agent"
)

// ValidKinds is the set of supported Kinds. Use IsValid to check.
var ValidKinds = map[Kind]bool{
	KindLLMEndpoint: true,
	KindMCPServer:   true,
	KindAgent:       true,
}

// IsValid reports whether k is a recognized asset Kind.
func (k Kind) IsValid() bool { return ValidKinds[k] }

// HealthState is the rollup health of an asset, aggregated from probes.
type HealthState string

const (
	HealthHealthy  HealthState = "healthy"
	HealthDegraded HealthState = "degraded"
	HealthDown     HealthState = "down"
	HealthUnknown  HealthState = "unknown"
	// HealthStorage 表示「资产本身没坏，但它依赖的存储读路径不可用」
	// （数据库不可达、连接池为空、读事务开不起来）。与 HealthDown 的区别很
	// 重要：down 意味着探针确认资产不响应，storage_degraded 意味着探针还能
	// 跑但读不到持久化状态 —— 对应的 HTTP 语义是 503 + storage_status，
	// 而不是 500。
	//
	// 供 admin 会话读端点在存储降级时上报（Subtask 4，handoff §6）。
	//
	// R73 审计登记：当前**零消费点**——503 路径（admin/storage_degraded.go）
	// 只写 HTTP 响应体的 storage_status 与 metrics_storage 打点，从未把
	// session_bodies 等资产的 HealthState 映射为 storage_degraded。保留为
	// 预留枚举：接入前任何读者都应把它当作「尚未接线」而非「已上报」。
	HealthStorage HealthState = "storage_degraded"
)

// Asset is the unified resource record. The primary key is the composite
// (Kind, RefID) so that different source tables can share the namespace
// without collision.
type Asset struct {
	Kind         Kind              `json:"kind"`
	RefID        int64             `json:"ref_id"`
	TenantID     string            `json:"tenant_id"`
	Name         string            `json:"name"`
	Owner        string            `json:"owner,omitempty"`
	Team         string            `json:"team,omitempty"`
	CostCenter   string            `json:"cost_center,omitempty"`
	Tags         map[string]string `json:"tags,omitempty"`
	HealthState  HealthState       `json:"health_state"`
	Version      string            `json:"version,omitempty"`
	RegisteredAt time.Time         `json:"registered_at"`
	LastSeenAt   time.Time         `json:"last_seen_at,omitempty"`
	Metadata     map[string]any    `json:"metadata,omitempty"`
}

// RelationType describes the edge semantics in the topology graph.
type RelationType string

const (
	RelDependsOn RelationType = "depends_on" // A needs B to function
	RelCalls     RelationType = "calls"      // A invokes B at runtime
	RelSimilarTo RelationType = "similar_to" // A and B are substitutes
)

// Relationship is a directed edge between two assets (A -> B).
type Relationship struct {
	SrcKind RelationEndpoint `json:"src_kind"`
	DstKind RelationEndpoint `json:"dst_kind"`
	Type    RelationType     `json:"type"`
	Weight  float64          `json:"weight,omitempty"`
}

// RelationEndpoint identifies one side of a relationship.
type RelationEndpoint struct {
	Kind  Kind  `json:"kind"`
	RefID int64 `json:"ref_id"`
}

// Filter narrows a List query.
type Filter struct {
	TenantID string      `json:"tenant_id"`
	Kind     Kind        `json:"kind,omitempty"`   // optional
	Tag      string      `json:"tag,omitempty"`    // optional: "key=value" match
	Health   HealthState `json:"health,omitempty"` // optional
	Limit    int         `json:"limit,omitempty"`  // default 100, max 500
	// Offset 是分页游标，配合 Limit 使用。
	//
	// 2026-10-05（runbook §10.27）：这个字段之前**不存在**，而 Limit 被硬
	// 截在 500 —— 于是「取第 501~1000 行」这件事**根本无法表达**。
	// AssetHealthProbe 的 Step 2 写着 `// Fetch all assets with pagination`，
	// 却因为写不出 OFFSET 而只能拿到前 500 行：2141 行里有 1521 行
	// 从来没被检查过（实测 835 行不在 liveLookup，只有 396 行在前 500 内）。
	// 排序键 (kind, ref_id) 是复合主键，租户内全序 ⇒ OFFSET 分页稳定。
	Offset int `json:"offset,omitempty"`
}

// ErrNotFound is returned by Get when no asset matches (kind, ref_id).
var ErrNotFound = errors.New("apihub: asset not found")

// ErrInvalidKind is returned when a Kind fails IsValid().
var ErrInvalidKind = errors.New("apihub: invalid asset kind")
