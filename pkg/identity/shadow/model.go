// Package shadow 提供跨项目 user 影子表的 DAO 与对账能力。
//
// 影子表设计哲学：
//   - 每项目保持独立 user 表（自闭环）
//   - 跨项目身份由 shadow_users.canonical_user_id 统一
//   - shadow_user_providers 记录 (provider, subject, tenant_id) 三元组 → shadow_user_id 的映射
//   - owner = kxuser，与 memora audit.* 同模式；不开 RLS；FORCE RLS = false
package shadow

import (
	"time"
)

// ShadowUser 跨项目统一身份主表。
type ShadowUser struct {
	ShadowUserID    string    `json:"shadow_user_id"`
	CanonicalUserID string    `json:"canonical_user_id"`
	Status          string    `json:"status"` // active | disabled
	DisplayName     string    `json:"display_name"`
	PrimaryEmail    string    `json:"primary_email"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// ShadowProvider 每条记录是"某个项目内的某个 user"到 shadow_user 的映射。
type ShadowProvider struct {
	Provider     string    `json:"provider"`  // "redclaw" | "memora" | "llm-gateway" | "pocket" | "acc"
	Subject      string    `json:"subject"`   // 项目内 user_id / Casdoor sub
	TenantID     string    `json:"tenant_id"` // 项目内 tenant_id
	ShadowUserID string    `json:"shadow_user_id"`
	ExternalID   string    `json:"external_id"` // casdoor_id / oidc_sub（保留外部 IdP 引用）
	Metadata     string    `json:"metadata"`    // JSON 字符串
	LinkedAt     time.Time `json:"linked_at"`
	LastSeenAt   time.Time `json:"last_seen_at"`
}

// ShadowAudit 跨项目身份变更审计。
type ShadowAudit struct {
	ID             int64     `json:"id"`
	ActorProject   string    `json:"actor_project"`
	Action         string    `json:"action"` // 'link' | 'unlink' | 'reconcile_orphan' | 'auto_create'
	TargetProvider string    `json:"target_provider"`
	TargetSubject  string    `json:"target_subject"`
	TargetShadowID string    `json:"target_shadow_id"`
	Metadata       string    `json:"metadata"`
	CreatedAt      time.Time `json:"created_at"`
}
