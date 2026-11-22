package main

import (
	"context"
	"errors"
	"strconv"

	domainprovider "github.com/kaixuan/llm-gateway-go/domains/provider" //nolint:depguard // 同 main_pipeline.go：domains/provider 仍是历史依赖
	routerprovider "github.com/kaixuan/llm-gateway-go/provider"
)

// routerDiscoveryCatalog 把**路由的权威候选**适配成 provider.DiscoveryCatalog。
//
// ★ 2026-11-23（N3 接线）：★ 「真实 provider 目录」这件事，★ 仓里**早就有
// 一份权威实现** —— ★ `provider.Client.GetCandidates`（★ provider/client.go
// 的 `candidateQuerySQLBaseTemplate`）：
//
//	· 数据源＝`model_offers`（★ credential_model_bindings 的视图）
//	  ⨝ credentials ⨝ providers ⨝ provider_models ⨝ models_canonical；
//	· 模型名匹配＝**5 路矩阵**（★ canonical_raw_name / standardized_name /
//	  model_name_mapping / model_aliases / canonical_id）；
//	· 死活门槛＝**一整套**（★ cmb.available / pm.available / 凭据的
//	  status·lifecycle·availability_state·quota_state·health_status·
//	  trust_level·manual_disabled / provider 的 enabled·manual_disabled …）。
//
// ★ ⇒ ★ 所以本文件**不写任何新的查询、也不复制任何门槛** ⇒ ★ 目录的结论
// ★ 与「路由会怎么选」**同源** ⇒ ★ 不会漂移。
//
// ★ ★ 为什么不自己写一条 SQL（★ 原设计稿 §9.19·附8 的方案 ① 是「换 store 的
// 数据来源」）：★ 仓内**已有证据**表明复制门槛必然漂移 —— ★ autoroute 的门禁
// 注释写着（★ autoroute/availability_filter_sql_test.go）：
//
//	「re-implementing the candidate side's gates always drifts
//	  (211 evidence: 189 of 1010 pooled models were "alive on the decision
//	  side, dead on the candidate side" — 19%). The only stable contract is
//	  sharing the view itself.」
//
// ★ ⇒ ★ 而「自己写一条最小查询」的口径差异有多大，★ 本轮**实测过**：
// ★ 按设计稿的 3 条约束（★ providers.enabled + 三表 join）＝ **3,132** 行；
// ★ 按权威死活口径（＝ 726 行）⇒ ★ **差 4.3 倍** ⇒ ★ 照设计稿接线会把
// ★ **2,406 条不可路由的绑定**当成可用候选报出去（★ 静默给错答案）。
//
// ★ 缓存：★ `provider.Client` 自带候选缓存（★ TTL 30s + 30s 陈旧宽限）
// ⇒ ★ discovery 在**每请求**路径上调用它也只打一次 SQL/30s。
type routerDiscoveryCatalog struct {
	client candidateSource
}

// candidateSource 是 routerDiscoveryCatalog 依赖的**最小面**。
//
// ★ 抽成接口（★ 而不是直接持有 *provider.Client）有两个理由：
//
//	① ★ 行为门可以注入假源（★ 目录失败时的降级、★ 凭据 id 是否被顶替，
//	   ★ 这些都不需要真 DB）；
//	② ★ 真库门可以注入**真** client（★ 与生产同一条查询路径）。
type candidateSource interface {
	Enabled() bool
	GetCandidates(ctx context.Context, model, profile, tenantID string) ([]routerprovider.Candidate, *routerprovider.Policy, error)
}

// FindByModel 返回该模型当前**可路由**的候选。
//
// ★ 契约（★ 对齐 domains/provider.DiscoveryCatalog）：
//   - CredentialID ＝ **真实凭据 id**（★ provider.Candidate.CredentialID
//     本来就是 credential_model_bindings.credential_id ⇒ ★ 不存在
//     「取错列」的可能）；
//   - 只返回可路由条目（★ 死活口径在权威查询里）；
//   - 失败返回 error（★ advisory 版把它降级成 metadata 的 store_error）。
func (c *routerDiscoveryCatalog) FindByModel(ctx context.Context, model, tenantID string) ([]domainprovider.DiscoveryCandidate, error) {
	if c == nil || c.client == nil {
		return nil, errors.New("provider discovery: catalog not wired")
	}
	if !c.client.Enabled() {
		// ★ 没有 DB ⇒ 权威目录不可用 ⇒ 诚实报错（★ 而不是静默给空候选，
		// ★ 空候选会被当成「这个模型没有可用 provider」——★ 那是另一种错答案）。
		return nil, errors.New("provider discovery: provider client not configured (no DB)")
	}
	if tenantID == "" {
		tenantID = "default"
	}
	cands, _, err := c.client.GetCandidates(ctx, model, "", tenantID)
	if err != nil {
		return nil, err
	}
	out := make([]domainprovider.DiscoveryCandidate, 0, len(cands))
	for i := range cands {
		cc := &cands[i]
		if cc.CredentialID <= 0 {
			// ★ 没有凭据身份 ⇒ 不是候选（★ 宁缺毋滥：★ 一个空/错凭据 id
			// 流到下游，比没有候选更糟）。
			continue
		}
		if !cc.Routable {
			// ★ **只报路由真会用的候选**（★ Routable 是路由侧自己的判定，
			// ★ 含 DB 门槛与密钥可用性）⇒ ★ 目录的结论与路由一致。
			// ★ 全都不可路由 ⇒ 目录给 0 个 ⇒ advisory 报
			// ★ 「no_healthy_provider」⇒ ★ 那是**诚实**的结论
			// （★ 而不是一个含死条目的名单）。
			continue
		}
		modelName := cc.OfferRawModel
		if modelName == "" {
			modelName = cc.RawModel
		}
		out = append(out, domainprovider.DiscoveryCandidate{
			CredentialID: strconv.Itoa(cc.CredentialID),
			Provider:     strconv.Itoa(cc.ProviderID),
			Model:        modelName,
			// ★ Priority 留 0：★ 权威候选里的顺序信息是 tier/weight，
			// ★ 而 discovery 的 Priority 语义在 demo 目录里恒为 0
			// ⇒ ★ 这里不引入一个「看起来有含义、实际没人读」的值。
		})
	}
	return out, nil
}

var _ domainprovider.DiscoveryCatalog = (*routerDiscoveryCatalog)(nil)
