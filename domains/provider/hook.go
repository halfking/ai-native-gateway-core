package provider

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"           //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/pipeline" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// ProviderDiscoveryHook provider 发现 Hook
// 在 PreRouting 阶段根据请求模型查找可用 provider
type ProviderDiscoveryHook struct {
	store  *InMemoryStore
	prober *Prober
	// catalog 是**真实目录**（★ 可后绑定：★ 目录在管线构建之后才就位）。
	// nil ⇒ 回落内存 store（★ 老行为，★ demo 目录）。
	catalog DiscoveryCatalog
}

// NewProviderDiscoveryHook 创建发现 Hook
func NewProviderDiscoveryHook(store *InMemoryStore, prober *Prober) *ProviderDiscoveryHook {
	return &ProviderDiscoveryHook{store: store, prober: prober}
}

// SetCatalog 注入真实目录（★ 可后绑定）。
//
// ★ 为什么需要「后绑定」：★ 目录依赖的 DB 客户端与 provider client 在
// 管线构建**之后**才注入（★ 与 PromptInjectionHook.BindDetector 同款问题、
// 同款解法）⇒ ★ 构建时刻只能挂内存 demo 目录、★ 真实目录到位后再换。
func (h *ProviderDiscoveryHook) SetCatalog(c DiscoveryCatalog) {
	if h == nil {
		return
	}
	h.catalog = c
}

// Name 返回 Hook 名称
func (h *ProviderDiscoveryHook) Name() string { return "provider.discover" }

// Priority 返回优先级
func (h *ProviderDiscoveryHook) Priority() int { return 60 }

// Enabled 是否启用
func (h *ProviderDiscoveryHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	return env != nil
}

// DiscoveryCandidate 是发现结果在 env.Metadata 里的序列化形状。
// 抽成包级**导出**类型是为了让严格版与 advisory 版产出**同形**的候选
// （否则下游按类型断言会拿到两种形状）。
//
// ★ 2026-11-23（N3 接线）：导出它还有一个更重要的理由 ——
// ★ **让「目录」直接产出候选，而不是产出 provider 再由 hook 映射**。
//
// 真实目录的粒度是 **provider ⨝ model ⨝ binding(credential)** ⇒ 同一个
// provider 的同一个模型可以有多条 binding（多凭据）⇒ 「候选 ↔ provider」
// **不再是 1:1** ⇒ 若仍让 hook 从 provider 映射，候选的凭据身份就只能靠
// 「provider 的哪一列充当凭据 id」这种约定 ⇒ ★ 在内存 demo 目录里恰好成立
// （provider id 兼任 credential id）、★ 在真实目录里**不成立**
// ⇒ ★ 这正是原设计稿列为「★ 最容易做错的一步」的那一行。
//
// ⇒ 目录直接产出候选 ⇒ **凭据身份由目录给出、hook 不再猜** ⇒ 这一类错误
// 从结构上消失（★ 而不是靠一条纪律去防）。
type DiscoveryCandidate struct {
	CredentialID string
	Provider     string
	Model        string
	Priority     int
}

// DiscoveryCatalog 是 discovery 阶段需要的**只读**目录视图。
//
// ★ 契约（三条，★ 每条都对应一类会「静默给错答案」的坑）：
//
//	① CredentialID 必须是**真实凭据 id**
//	   （★ credential_model_bindings.credential_id），★ 不是 provider id；
//	② 只返回**当前可用**的条目 —— ★ 死活口径由目录自己负责
//	   （★ 见 cmd/gateway 的 routerDiscoveryCatalog：★ 它直接复用路由的
//	   **权威候选查询**、★ 不自己复制门槛 ⇒ ★ 不会与路由漂移）；
//	③ 失败必须返回 error —— ★ advisory 版会把 error 降级成 metadata 里的
//	   `store_error` 状态、★ **绝不中止管线**（★ advisory 的基本契约）。
type DiscoveryCatalog interface {
	FindByModel(ctx context.Context, model, tenantID string) ([]DiscoveryCandidate, error)
}

// buildDiscoveryCandidates 把健康 provider 列表转成候选。
//
// ★ 这是**内存 demo 目录**那条老路径的映射（★ catalog 路径不走这里）。
// ★ demo 目录里 provider id 兼任凭据身份 ⇒ CredentialID 取 p.ID；
// ★ ★ 真实目录**不走这条路径** ⇒ ★ 取错列的那类错误不会发生在这里。
func buildDiscoveryCandidates(healthy []*Provider, model string) []DiscoveryCandidate {
	candidates := make([]DiscoveryCandidate, 0, len(healthy))
	for _, p := range healthy {
		candidates = append(candidates, DiscoveryCandidate{
			CredentialID: p.ID, // provider id 作为 candidate id（★ demo 目录的老行为）
			Provider:     p.ID,
			Model:        model,
		})
	}
	return candidates
}

// candidatesFor 取候选：优先用注入的**真实目录**，否则回落到内存 store。
//
// ★ 注意：catalog 路径**不再过 Prober.FilterHealthy** —— ★ 目录返回的
// 已经是「当前可路由」的条目（★ 死活口径在目录里），★ 再套一层进程内
// 健康判定只会把可用条目误杀（★ Prober 的判据是 Provider.Status()，
// ★ 对目录条目恒为 active）。
func (h *ProviderDiscoveryHook) candidatesFor(ctx context.Context, model, tenantID string) ([]DiscoveryCandidate, error) {
	if h.catalog != nil {
		return h.catalog.FindByModel(ctx, model, tenantID)
	}
	all, err := h.store.FindByModel(model)
	if err != nil {
		return nil, err
	}
	healthy := h.prober.FilterHealthy(all)
	return buildDiscoveryCandidates(healthy, model), nil
}

// Execute 查找支持请求模型的 provider
func (h *ProviderDiscoveryHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	model, _ := env.Metadata["model"].(string)
	if model == "" {
		return nil // 无模型信息跳过
	}
	candidates, err := h.candidatesFor(ctx, model, env.TenantID)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		return errors.New("provider: no healthy provider for model " + model)
	}
	// 序列化为 discovery 候选格式
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
//
//	· 生产日志恒有一条 WARN（v2 pipeline: preflight error）；
//	· 治理链（governance_security / governance_interception）对真实流量不可达；
//	· 路由链（credential_health / routing / …）同样不可达。
//
// ★ 这条 hook 的定位是「**咨询**」而不是「**门禁**」：
//
//	真实路由由 v1 的 chatHandler（DB 凭据仓）负责，管线里的 provider 目录
//	在这条路径上**不是权威来源** ⇒ 它没有资格中止整条管线。
//	（真正的门禁是后面的 governance_interception + dispatch gate。）
//
// ★ 契约：advisory 版**永不返回 error**。它把结论写进 env.Metadata：
//
//		provider_discovery_status = "ok" | "no_healthy_provider" | "store_error"
//		provider_count            = 候选数量
//		provider_discovery_detail = 失败原因（仅失败时）
//
//	  ⇒ 上层能区分「发现到了」与「没发现到」，而管线继续往下走。
//
// discoveryLogInterval 是 discovery 结论日志的**采样间隔**。
//
// ★ 为什么要采样：★ 这一阶段在**每请求**路径上 ⇒ ★ 不采样就是每请求一条
// ★ ⇒ ★ 日志本身变成噪声（★ 而且会拖慢热路径）。
// ★ 60s 一条足以回答「★ 目录接上了吗、★ 命中了吗、★ 命中几个」。
const discoveryLogInterval = 60 * time.Second

// discoveryLogSampler 按间隔采样 discovery 结论日志。
//
// ★ 用原子 CAS 而不是互斥锁：★ 这是**热路径**（★ 每请求一次）⇒ ★ 不能
// ★ 在这里排队。★ CAS 的语义是「★ 第一个把时间戳推进去的那个赢」⇒ ★
// ★ 并发下最多偶尔多出一条（★ 不是恰好一条）★ ⇒ ★ 对采样日志完全够用。
type discoveryLogSampler struct {
	lastUnix int64
}

func (s *discoveryLogSampler) shouldLog() bool {
	now := time.Now().Unix()
	prev := atomic.LoadInt64(&s.lastUnix)
	if now-prev < int64(discoveryLogInterval/time.Second) {
		return false
	}
	return atomic.CompareAndSwapInt64(&s.lastUnix, prev, now)
}

type AdvisoryDiscoveryHook struct {
	hook *ProviderDiscoveryHook
	// logSampler 控制结论日志的采样频率（★ 热路径 ⇒ 必须采样）。
	logSampler discoveryLogSampler
}

// NewAdvisoryDiscoveryHook 创建「不中止管线」的发现 Hook。
func NewAdvisoryDiscoveryHook(store *InMemoryStore, prober *Prober) *AdvisoryDiscoveryHook {
	return &AdvisoryDiscoveryHook{hook: NewProviderDiscoveryHook(store, prober)}
}

// SetCatalog 注入真实目录（★ 可后绑定；★ 与 ProviderDiscoveryHook.SetCatalog 同款）。
func (h *AdvisoryDiscoveryHook) SetCatalog(c DiscoveryCatalog) {
	if h == nil {
		return
	}
	h.hook.SetCatalog(c)
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
	candidates, err := h.hook.candidatesFor(ctx, model, env.TenantID)
	if err != nil {
		setDiscoveryStatus(env, "store_error", err.Error())
		h.logConclusion("store_error", model, 0)
		return nil
	}
	if len(candidates) == 0 {
		setDiscoveryStatus(env, "no_healthy_provider",
			"provider catalog has no healthy entry for model "+model)
		h.logConclusion("no_healthy_provider", model, 0)
		return nil
	}
	env.Metadata["provider_candidates"] = candidates
	env.Metadata["provider_count"] = len(candidates)
	setDiscoveryStatus(env, "ok", "")
	h.logConclusion("ok", model, len(candidates))
	return nil
}

// logConclusion 把 discovery 结论**采样**写进日志。
//
// ★ 为什么必须有它：★ advisory 的契约是「★ 结论只写 metadata、★ 永不中止
// 管线」⇒ ★ 而 metadata **没有人读** ⇒ ★ 结论等于不存在 ⇒ ★ 接上真实
// 目录之后，★ 也**看不出**目录到底命中了什么。
// ★ ⇒ ★ 采样一条 INFO，★ 让「★ 目录接上了吗 / 命中了吗 / 命中几个」
// ★ 这三个问题**在生产上可回答**。
//
// ★ source 字段回答「★ 现在用的是真目录还是内存 demo 目录」⇒ ★ 这是
// ★ 「接线是否生效」的唯一现场读数（★ 比翻代码可靠）。
func (h *AdvisoryDiscoveryHook) logConclusion(status, model string, count int) {
	if !h.logSampler.shouldLog() {
		return
	}
	source := "demo_store"
	if h.hook.catalog != nil {
		source = "catalog"
	}
	slog.Info("provider discovery",
		"status", status,
		"source", source,
		"model", model,
		"candidates", count,
		"sample_interval", discoveryLogInterval.String())
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
