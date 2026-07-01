package provider

import (
	"context"
	"errors"

	"github.com/kaixuan/llm-gateway-go/domain"           //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/pipeline" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// ProviderDiscoveryHook provider 发现 Hook
// 在 PreRouting 阶段根据请求模型查找可用 provider
type ProviderDiscoveryHook struct {
	store  *InMemoryStore
	prober *Prober
}

// NewProviderDiscoveryHook 创建发现 Hook
func NewProviderDiscoveryHook(store *InMemoryStore, prober *Prober) *ProviderDiscoveryHook {
	return &ProviderDiscoveryHook{store: store, prober: prober}
}

// Name 返回 Hook 名称
func (h *ProviderDiscoveryHook) Name() string { return "provider.discover" }

// Priority 返回优先级
func (h *ProviderDiscoveryHook) Priority() int { return 60 }

// Enabled 是否启用
func (h *ProviderDiscoveryHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	return env != nil
}

// Execute 查找支持请求模型的 provider
func (h *ProviderDiscoveryHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	model, _ := env.Metadata["model"].(string)
	if model == "" {
		return nil // 无模型信息跳过
	}
	all, err := h.store.FindByModel(model)
	if err != nil {
		return err
	}
	healthy := h.prober.FilterHealthy(all)
	if len(healthy) == 0 {
		return errors.New("provider: no healthy provider for model " + model)
	}
	// 序列化为 streaming.Candidate 格式
	type cand struct {
		CredentialID string
		Provider     string
		Model        string
		Priority     int
	}
	candidates := make([]cand, 0, len(healthy))
	for _, p := range healthy {
		candidates = append(candidates, cand{
			CredentialID: p.ID, // provider id 作为 candidate id
			Provider:     p.ID,
			Model:        model,
		})
	}
	env.Metadata["provider_candidates"] = candidates
	env.Metadata["provider_count"] = len(candidates)
	return nil
}

// OnError 错误处理
func (h *ProviderDiscoveryHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	return err
}

var _ pipeline.Hook = (*ProviderDiscoveryHook)(nil)
