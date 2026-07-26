package ursm

import (
	"fmt"
	"time"
)

// ProviderState Provider层状态
type ProviderState struct {
	ProviderID     int       `json:"provider_id"`
	Enabled        bool      `json:"enabled"`
	ManualDisabled bool      `json:"manual_disabled"`
	DisplayName    string    `json:"display_name"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// IsAvailable 检查Provider是否可用
func (s *ProviderState) IsAvailable() bool {
	return s.Enabled && !s.ManualDisabled
}

// UnavailableReason 返回不可用原因
func (s *ProviderState) UnavailableReason() string {
	if !s.Enabled {
		return "provider_disabled"
	}
	if s.ManualDisabled {
		return "provider_manual_disabled"
	}
	return ""
}

// CredentialState Credential层状态
type CredentialState struct {
	CredentialID        int        `json:"credential_id"`
	ProviderID          int        `json:"provider_id"`
	Status              string     `json:"status"`             // active/inactive
	LifecycleStatus     string     `json:"lifecycle_status"`   // active/retired
	AvailabilityState   string     `json:"availability_state"` // ready/degraded/auth_failed/rate_limited/unreachable/suspended
	HealthStatus        string     `json:"health_status"`      // healthy/warning/degraded/error/unreachable/auth_failed
	QuotaState          string     `json:"quota_state"`        // ok/periodic_exhausted/balance_exhausted/permanently_exhausted
	ManualDisabled      bool       `json:"manual_disabled"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	RecoverAt           *time.Time `json:"recover_at,omitempty"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// IsAvailable 检查Credential是否可用
func (s *CredentialState) IsAvailable() bool {
	if s.Status != "active" {
		return false
	}
	if s.LifecycleStatus != "active" {
		return false
	}
	if s.ManualDisabled {
		return false
	}
	if s.AvailabilityState == "auth_failed" || s.AvailabilityState == "suspended" {
		return false
	}
	if s.QuotaState == "permanently_exhausted" || s.QuotaState == "balance_exhausted" {
		return false
	}
	return true
}

// UnavailableReason 返回不可用原因
func (s *CredentialState) UnavailableReason() string {
	if s.Status != "active" {
		return fmt.Sprintf("credential_status_%s", s.Status)
	}
	if s.LifecycleStatus != "active" {
		return fmt.Sprintf("credential_lifecycle_%s", s.LifecycleStatus)
	}
	if s.ManualDisabled {
		return "credential_manual_disabled"
	}
	if s.AvailabilityState == "auth_failed" {
		return "credential_auth_failed"
	}
	if s.AvailabilityState == "suspended" {
		return "credential_suspended"
	}
	if s.QuotaState == "permanently_exhausted" {
		return "credential_quota_permanently_exhausted"
	}
	if s.QuotaState == "balance_exhausted" {
		return "credential_quota_balance_exhausted"
	}
	return ""
}

// ModelState Model层状态
type ModelState struct {
	CredentialID     int       `json:"credential_id"`
	RawModel         string    `json:"raw_model"`
	OfferAvailable   bool      `json:"offer_available"`
	OfferReason      string    `json:"offer_reason,omitempty"`
	BindingAvailable bool      `json:"binding_available"`
	BindingReason    string    `json:"binding_reason,omitempty"`
	ProbeState       string    `json:"probe_state"` // unknown/recovering/healthy_confirmed/broken_confirmed
	UpdatedAt        time.Time `json:"updated_at"`
}

// IsAvailable 检查Model是否可用
func (s *ModelState) IsAvailable() bool {
	return s.OfferAvailable &&
		s.BindingAvailable &&
		s.ProbeState != "broken_confirmed"
}

// UnavailableReason 返回不可用原因
func (s *ModelState) UnavailableReason() string {
	if !s.OfferAvailable {
		if s.OfferReason != "" {
			return fmt.Sprintf("model_offer_unavailable: %s", s.OfferReason)
		}
		return "model_offer_unavailable"
	}
	if !s.BindingAvailable {
		if s.BindingReason != "" {
			return fmt.Sprintf("model_binding_unavailable: %s", s.BindingReason)
		}
		return "model_binding_unavailable"
	}
	if s.ProbeState == "broken_confirmed" {
		return "model_broken_confirmed"
	}
	return ""
}

// NodeState Node层状态
type NodeState struct {
	CredentialID        int        `json:"credential_id"`
	RawModel            string     `json:"raw_model"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	SuccessCount        int64      `json:"success_count"`
	FailureCount        int64      `json:"failure_count"`
	LastSuccessAt       *time.Time `json:"last_success_at,omitempty"`
	LastFailureAt       *time.Time `json:"last_failure_at,omitempty"`
	Disabled            bool       `json:"disabled"`
	DisabledUntil       *time.Time `json:"disabled_until,omitempty"`
	RecoverAt           *time.Time `json:"recover_at,omitempty"`
	LastError           string     `json:"last_error,omitempty"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// IsAvailable 检查Node是否可用
func (s *NodeState) IsAvailable() bool {
	// 连续失败超过阈值，节点不可用
	if s.ConsecutiveFailures >= 3 {
		return false
	}

	// 节点被禁用，且禁用时间未到
	if s.Disabled && s.DisabledUntil != nil && time.Now().Before(*s.DisabledUntil) {
		return false
	}

	return true
}

// UnavailableReason 返回不可用原因
func (s *NodeState) UnavailableReason() string {
	if s.ConsecutiveFailures >= 3 {
		return fmt.Sprintf("node_consecutive_failures:%d", s.ConsecutiveFailures)
	}
	if s.Disabled {
		if s.DisabledUntil != nil && time.Now().Before(*s.DisabledUntil) {
			return fmt.Sprintf("node_disabled_until:%s", s.DisabledUntil.Format(time.RFC3339))
		}
		return "node_disabled"
	}
	return ""
}

// RouteNode 路由节点（用于路由决策）
type RouteNode struct {
	// 标识
	CredentialID int    `json:"credential_id"`
	RawModel     string `json:"raw_model"`
	ProviderID   int    `json:"provider_id"`
	ProviderName string `json:"provider_name"`

	// 状态（查询时填充）
	Available         bool   `json:"available"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
	HealthStatus      string `json:"health_status"`

	// 资源（路由时分配）
	FpSlotIndex     int  `json:"fp_slot_index"`    // 已获取的指纹槽索引（-1表示未获取）
	ConcurrencyHeld bool `json:"concurrency_held"` // 是否已获取并发槽

	// 成本因素（用于排序）
	PriceInPer1M   float64 `json:"price_in_per_1m"`
	PriceOutPer1M  float64 `json:"price_out_per_1m"`
	Currency       string  `json:"currency"`
	SuccessRate    float64 `json:"success_rate"` // 0.0-1.0
	P95LatencyMs   int     `json:"p95_latency_ms"`
	CompositeScore float64 `json:"composite_score"` // 综合评分（用于排序）

	// 限额配置
	FpSlotLimit      int `json:"fp_slot_limit"`
	ConcurrencyLimit int `json:"concurrency_limit"`
}

// StateUpdate 状态更新（用于批量写入）
type StateUpdate struct {
	Layer     Layer     `json:"layer"`
	Timestamp time.Time `json:"timestamp"`
	Actor     string    `json:"actor"`  // 操作者
	Reason    string    `json:"reason"` // 变更原因
	Source    string    `json:"source"` // 来源：request/probe/manual

	// Provider层字段
	ProviderID     int   `json:"provider_id,omitempty"`
	Enabled        *bool `json:"enabled,omitempty"`
	ManualDisabled *bool `json:"manual_disabled,omitempty"`

	// Credential层字段
	CredentialID      int     `json:"credential_id,omitempty"`
	AvailabilityState *string `json:"availability_state,omitempty"`
	HealthStatus      *string `json:"health_status,omitempty"`
	QuotaState        *string `json:"quota_state,omitempty"`

	// Model层字段
	Model            string  `json:"model,omitempty"`
	ProbeState       *string `json:"probe_state,omitempty"`
	OfferAvailable   *bool   `json:"offer_available,omitempty"`
	BindingAvailable *bool   `json:"binding_available,omitempty"`

	// Node层字段
	Success             bool   `json:"success,omitempty"`
	ErrorKind           string `json:"error_kind,omitempty"`
	LatencyMs           int    `json:"latency_ms,omitempty"`
	ConsecutiveFailures *int   `json:"consecutive_failures,omitempty"`
}

// Layer 层级枚举
type Layer int

const (
	LayerProvider Layer = iota
	LayerCredential
	LayerModel
	LayerNode
)

// String 实现Stringer接口
func (l Layer) String() string {
	switch l {
	case LayerProvider:
		return "provider"
	case LayerCredential:
		return "credential"
	case LayerModel:
		return "model"
	case LayerNode:
		return "node"
	default:
		return "unknown"
	}
}
