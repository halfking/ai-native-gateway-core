package credential

import (
	"context"
	"errors"
	"fmt"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
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
	// 序列化为 candidate 列表（与 routing.Candidate 兼容）
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
	ok, err := h.limiter.Acquire(env.SelectedCredential.ID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("credential: rate limit exceeded for %s", env.SelectedCredential.ID)
	}
	env.Metadata["credential_locked"] = true
	return nil
}

// OnError 释放槽位
func (h *LimiterHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	if env != nil && env.SelectedCredential != nil {
		h.limiter.Release(env.SelectedCredential.ID)
	}
	return err
}

var _ pipeline.Hook = (*LimiterHook)(nil)
