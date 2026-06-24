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
}

// ErrNotFound is returned by Get when no asset matches (kind, ref_id).
var ErrNotFound = errors.New("apihub: asset not found")

// ErrInvalidKind is returned when a Kind fails IsValid().
var ErrInvalidKind = errors.New("apihub: invalid asset kind")
