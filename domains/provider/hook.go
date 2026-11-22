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

// discoveryCandidate 是发现结果在 env.Metadata 里的序列化形状。
// 抽成包级类型是为了让严格版与 advisory 版产出**同形**的候选
// （否则下游按类型断言会拿到两种形状）。
type discoveryCandidate struct {
	CredentialID string
	Provider     string
	Model        string
	Priority     int
}

// buildDiscoveryCandidates 把健康 provider 列表转成候选。
func buildDiscoveryCandidates(healthy []*Provider, model string) []discoveryCandidate {
	candidates := make([]discoveryCandidate, 0, len(healthy))
	for _, p := range healthy {
		candidates = append(candidates, discoveryCandidate{
			CredentialID: p.ID, // provider id 作为 candidate id
			Provider:     p.ID,
			Model:        model,
		})
	}
	return candidates
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
	candidates := buildDiscoveryCandidates(healthy, model)
	env.Metadata["provider_candidates"] = candidates
	env.Metadata["provider_count"] = len(candidates)
	return nil
}

// OnError 错误处理
func (h *ProviderDiscoveryHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	return err
}

var _ pipeline.Hook = (*ProviderDiscoveryHook)(nil)

// ─────────────────────────────────────────────────────────────────────────
// AdvisoryDiscoveryHook：同一条发现逻辑，但**不中止管线**。
//
// 2026-11-23（211 配置优化遗留 L3）：严格版的 ProviderDiscoveryHook 在
// 「找不到健康 provider」时返回 error ⇒ 管线在此**中止** ⇒ 排在它之后的
// 所有阶段对真实流量不可达。
//
// 而 cmd/gateway 的 v1 包装路径（LLM_GATEWAY_USE_V2_PIPELINE=true，211 生产
// 走的就是它）挂的 provider 目录是**内存 demo 目录**（newV2DispatchDepsFromMain
// 种的 gpt-4 假凭据）⇒ 任何真实模型都命中「no healthy provider」
// ⇒ **每个请求**都在这一阶段中止 ⇒
//   · 生产日志恒有一条 WARN（v2 pipeline: preflight error）；
//   · 治理链（governance_security / governance_interception）对真实流量不可达；
//   · 路由链（credential_health / routing / …）同样不可达。
//
// ★ 这条 hook 的定位是「**咨询**」而不是「**门禁**」：
//   真实路由由 v1 的 chatHandler（DB 凭据仓）负责，管线里的 provider 目录
//   在这条路径上**不是权威来源** ⇒ 它没有资格中止整条管线。
//   （真正的门禁是后面的 governance_interception + dispatch gate。）
//
// ★ 契约：advisory 版**永不返回 error**。它把结论写进 env.Metadata：
//
//	provider_discovery_status = "ok" | "no_healthy_provider" | "store_error"
//	provider_count            = 候选数量
//	provider_discovery_detail = 失败原因（仅失败时）
//
//   ⇒ 上层能区分「发现到了」与「没发现到」，而管线继续往下走。
type AdvisoryDiscoveryHook struct {
	hook *ProviderDiscoveryHook
}

// NewAdvisoryDiscoveryHook 创建「不中止管线」的发现 Hook。
func NewAdvisoryDiscoveryHook(store *InMemoryStore, prober *Prober) *AdvisoryDiscoveryHook {
	return &AdvisoryDiscoveryHook{hook: NewProviderDiscoveryHook(store, prober)}
}

// Name 返回 Hook 名称（与严格版一致，便于按名字定位阶段）。
func (h *AdvisoryDiscoveryHook) Name() string { return h.hook.Name() }

// Priority 返回优先级
func (h *AdvisoryDiscoveryHook) Priority() int { return h.hook.Priority() }

// Enabled 是否启用
func (h *AdvisoryDiscoveryHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	return h.hook.Enabled(ctx, env)
}

// Execute 查找支持请求模型的 provider；**不中止管线**。
func (h *AdvisoryDiscoveryHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	model, _ := env.Metadata["model"].(string)
	if model == "" {
		return nil // 无模型信息跳过（与严格版一致）
	}
	all, err := h.hook.store.FindByModel(model)
	if err != nil {
		setDiscoveryStatus(env, "store_error", err.Error())
		return nil
	}
	healthy := h.hook.prober.FilterHealthy(all)
	if len(healthy) == 0 {
		setDiscoveryStatus(env, "no_healthy_provider",
			"provider catalog has no healthy entry for model "+model)
		return nil
	}
	candidates := buildDiscoveryCandidates(healthy, model)
	env.Metadata["provider_candidates"] = candidates
	env.Metadata["provider_count"] = len(candidates)
	setDiscoveryStatus(env, "ok", "")
	return nil
}

// OnError 错误处理：advisory 版不把错误往上抛。
func (h *AdvisoryDiscoveryHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	return nil
}

func setDiscoveryStatus(env *domain.PipelineRequest, status, detail string) {
	if env == nil {
		return
	}
	if env.Metadata == nil {
		env.Metadata = map[string]any{}
	}
	env.Metadata["provider_discovery_status"] = status
	if detail != "" {
		env.Metadata["provider_discovery_detail"] = detail
	}
}

var _ pipeline.Hook = (*AdvisoryDiscoveryHook)(nil)
