package admin

import (
	"context"
	"log/slog"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// Wave 1 A1（2026-09-22 设计差距审计）：
// /api/routing/resolve 的 plan_order 旧实现恒为 []，运营看到的"测试可用"
// 与生产选路不同源——正是"resolve 正常但请求无可用节点"事故的温床。
// 现在启动时经 SetLiveRoutingSource 注入生产 Router + 候选解析器，
// resolve 用与真实请求相同的 PlanCandidatesPinned 产出真实 plan_order；
// 与列表排序的差异写进响应 order_debug。nil 注入（no-DB / 老装配形态）
// 退化为 plan_order 空 + source=unavailable，行为不劣于旧版。
type LiveRoutingSource struct {
	Router   *executors.Router
	Resolver LiveCandidateResolver
}

// LiveCandidateResolver 是生产候选解析的最小切面（*provider.Client 满足）。
// profile 传空 → 解析器按无客户端画像的基础候选集解析。
type LiveCandidateResolver interface {
	GetCandidates(ctx context.Context, model, profile, tenantID string) ([]provider.Candidate, *provider.Policy, error)
}

// SetLiveRoutingSource 注入生产路由源；须在 Router 构建完成后调用。
func (h *Handler) SetLiveRoutingSource(src *LiveRoutingSource) {
	h.liveRouting = src
}

// livePlanOrder 用真实选路链产出 plan_order。返回 (entries, source)：
// source ∈ {"live-router","no-candidates","unavailable"}。
func (h *Handler) livePlanOrder(ctx context.Context, canonicalModel, tenantID string) ([]map[string]any, string) {
	entries := []map[string]any{}
	src := h.liveRouting
	if src == nil || src.Router == nil || src.Resolver == nil {
		return entries, "unavailable"
	}
	cands, policy, err := src.Resolver.GetCandidates(ctx, canonicalModel, "", tenantID)
	if err != nil {
		slog.Debug("routing resolve: live plan_order resolver failed",
			"model", canonicalModel, "error", err.Error())
		return entries, "unavailable"
	}
	if len(cands) == 0 {
		return entries, "no-candidates"
	}
	// 与真实请求路径同参：sticky=nil（resolve 无会话语义）、probePin=nil
	//（probe pin 仅限 OriginMiddleware 放行的自检探针）、egress=nil。
	// requestID 用固定合成值：routing_source_map 有 FIFO 上限，不会泄漏。
	planned := src.Router.PlanCandidatesPinned(ctx, cands, nil, nil, policy, nil, tenantID, canonicalModel, "admin-resolve")
	for i, c := range planned {
		entries = append(entries, map[string]any{
			"rank":          i + 1,
			"credential_id": c.CredentialID,
			"provider_id":   c.ProviderID,
			"raw_model":     c.BindingRawModel(),
			"tier":          c.Tier,
			"weight":        c.Weight,
		})
	}
	return entries, "live-router"
}

// buildOrderDebug 汇总 plan_order 与 resolve 列表首位的差异。列表首位是
// 持久化手工排序（sortResolveCandidatesStable），plan_order 是运行时选路；
// 二者首位不一致时运营应优先以 plan_order 为准。
func buildOrderDebug(planOrder []map[string]any, planSource string, candidates []resolveCandidate) map[string]any {
	debug := map[string]any{"plan_order_source": planSource}
	if planSource != "live-router" || len(planOrder) == 0 || len(candidates) == 0 {
		return debug
	}
	topPlanned, _ := planOrder[0]["credential_id"].(int)
	topListed := candidates[0].CredentialID
	debug["first_match"] = topPlanned == topListed
	if topPlanned != topListed {
		debug["note"] = "list order (persisted manual order) and live router order disagree on the top candidate; plan_order is what production routing picks"
	}
	return debug
}
