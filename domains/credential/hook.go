package credential

import (
	"context"
	"errors"
	"fmt"

	"github.com/kaixuan/llm-gateway-go/domain"           //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/pipeline" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// HealthCheckHook 健康检查 Hook
// 在 PreRouting 阶段过滤出健康凭据并写入 metadata
type HealthCheckHook struct {
	store   *InMemoryStore
	checker *HealthChecker
}

// NewHealthCheckHook 创建健康检查 Hook
func NewHealthCheckHook(store *InMemoryStore, checker *HealthChecker) *HealthCheckHook {
	return &HealthCheckHook{store: store, checker: checker}
}

// Name 返回 Hook 名称
func (h *HealthCheckHook) Name() string { return "credential.health" }

// Priority 返回优先级
func (h *HealthCheckHook) Priority() int { return 50 }

// Enabled 是否启用
func (h *HealthCheckHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	return env != nil
}

// Execute 过滤健康凭据
func (h *HealthCheckHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	creds, err := h.store.List(env.TenantID)
	if err != nil {
		return err
	}
	healthy := h.checker.FilterHealthy(creds)
	// 序列化为 candidate 列表（与 streaming.Candidate 兼容）
	type cand struct {
		CredentialID string
		Provider     string
		Model        string
		Priority     int
	}
	candidates := make([]cand, 0, len(healthy))
	for _, c := range healthy {
		candidates = append(candidates, cand{
			CredentialID: c.ID,
			Provider:     c.ProviderID,
			Model:        c.Model,
			Priority:     c.Priority,
		})
	}
	env.Metadata["credential_candidates"] = candidates
	env.Metadata["credential_count"] = len(candidates)
	return nil
}

// OnError 错误处理
func (h *HealthCheckHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	return err // 凭据加载失败必须上报
}

var _ pipeline.Hook = (*HealthCheckHook)(nil)

// LimiterHook 并发限制 Hook
// 占用选中凭据的并发槽位
//
// 2026-06-26 migration: hooks into the migrated 4-layer Limiter (was a
// simple in-memory map before). The hook uses the per-credential layer
// of the new Limiter via Limiter.Credential(providerID, credentialID),
// which returns a *Semaphore keyed by int IDs. We derive the int
// credentialID by FNV-hashing the string ID, and stash the semaphore
// in env.Metadata so OnError can release the same slot. ProviderID
// is 0 (pipeline view doesn't carry a numeric provider ID yet).
type LimiterHook struct {
	limiter *Limiter
}

// NewLimiterHook 创建限流 Hook
func NewLimiterHook(limiter *Limiter) *LimiterHook {
	return &LimiterHook{limiter: limiter}
}

// Name 返回 Hook 名称
func (h *LimiterHook) Name() string { return "credential.limit" }

// Priority 返回优先级
func (h *LimiterHook) Priority() int { return 150 }

// Enabled 是否启用
func (h *LimiterHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	return env != nil && env.SelectedCredential != nil
}

// Execute 占用并发槽位
func (h *LimiterHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	if env.SelectedCredential == nil {
		return errors.New("credential: no selected credential")
	}
	credID := fnvHash(env.SelectedCredential.ID)
	sem := h.limiter.Credential(0, credID)
	if !sem.TryAcquire() {
		return fmt.Errorf("credential: rate limit exceeded for %s", env.SelectedCredential.ID)
	}
	env.Metadata["credential_locked"] = true
	env.Metadata["credential_semaphore"] = sem
	env.Metadata["credential_cred_id"] = credID
	return nil
}

// OnError 释放槽位
func (h *LimiterHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	if env == nil {
		return err
	}
	if sem, ok := env.Metadata["credential_semaphore"].(*Semaphore); ok && sem != nil {
		sem.Release()
	}
	return err
}

var _ pipeline.Hook = (*LimiterHook)(nil)

// fnvHash maps a string credential ID to the int credential slot used by
// the migrated 4-layer Limiter. FNV-1a 32-bit is collision-resistant enough
// for per-credential rate limiting (we just need a stable mapping).
func fnvHash(s string) int {
	const (
		offset32 = 2166136261
		prime32  = 16777619
	)
	h := uint32(offset32)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= prime32
	}
	return int(h)
}
