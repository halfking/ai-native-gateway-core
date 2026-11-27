// adaptive_protocol.go — 「协议自适应」在候选加载出口的消解。
//
// 为什么插入点在这里（而不是在 admin 写入侧或执行器里）：
//
//	provider.Candidate.Protocol 是全链路唯一的线格式维度——分发路由
//	(executor_dispatch.classifyDispatchRoute)、请求体转换
//	(executor_anthropic.prepareAnthropicRequestBody / executor_gemini 的
//	ir.ParseOpenAI→SerializeGemini)、出站 URL（upstreamurl.*URL）、响应解析与
//	用量证据（reconciliation.ParseUsageEvidence）都以它为准。在候选加载出口
//	把它消解成具体协议之后，下游 20+ 处读取点全部自动继承，一处都不用改。
//
//	反过来，若在 admin 写入侧就消解，就失去了「按模型」这个维度；若在执行器里
//	消解，则每个执行器、每个探针都得各自实现一遍，迟早分叉。
//
// 与 catalog.ResolveAdaptiveProtocol 的分工：本文件只负责「把数据搬到判定体
// 面前」（读 provider_settings、遍历候选），判定语义全部在 catalog 层，纯函数、
// 可表驱动测试。
package provider

import (
	"context"
	"encoding/json"
	"log/slog"

	providercatalog "github.com/kaixuan/llm-gateway-go/provider/catalog"
)

// ProviderSettingKeyAdaptiveAllowed 是「上游真实可用的协议」声明键。
//
// 值形态与语义的唯一真相在 provider/catalog/adaptive_protocol.go
// （catalog.AdaptiveAllowedSettingKey）：那里同时被 admin 写入侧引用，
// 两侧共用同一个常量避免 SQL 字面量漂移。
const ProviderSettingKeyAdaptiveAllowed = providercatalog.AdaptiveAllowedSettingKey

// resolveAdaptiveProtocols 就地把携带自适应选择器的候选改写成具体协议，返回
// 被改写的条数。allowedByProvider 缺失的 provider（无声明）按
// catalog.ResolveAdaptiveProtocol 的惰性语义退化到 openai-completions。
//
// 只读 cand.Protocol 与 cand.RawModel：不碰 RawModel 以外的任何字段，因此对
// 非自适应候选是完全的空操作。
func resolveAdaptiveProtocols(cands []Candidate, allowedByProvider map[int][]string) int {
	changed := 0
	for i := range cands {
		if !providercatalog.IsAdaptiveProtocol(cands[i].Protocol) {
			continue
		}
		// 以 RawModel 判定家族：这是发给上游的真实模型名。StandardizedName /
		// CanonicalID 是客户端侧归一产物，按它判定会把「客户端怎么写」混进
		// 「上游认什么」，与「按原厂格式对接」的语义相反。
		resolved := providercatalog.ResolveAdaptiveProtocol(cands[i].RawModel, allowedByProvider[cands[i].ProviderID])
		if resolved == cands[i].Protocol {
			continue
		}
		cands[i].Protocol = resolved
		changed++
	}
	return changed
}

// adaptiveProviderIDs 返回候选里携带自适应选择器的 provider id 去重集合。
// 返回空表示本批候选与自适应无关，调用方据此跳过数据库往返。
func adaptiveProviderIDs(cands []Candidate) []int {
	var ids []int
	seen := make(map[int]struct{}, 2)
	for i := range cands {
		if !providercatalog.IsAdaptiveProtocol(cands[i].Protocol) {
			continue
		}
		if _, dup := seen[cands[i].ProviderID]; dup {
			continue
		}
		seen[cands[i].ProviderID] = struct{}{}
		ids = append(ids, cands[i].ProviderID)
	}
	return ids
}

// adaptiveAllowedByProvider 批量读取若干 provider 的可用协议声明。
//
// 失败时返回 nil（= 全部无声明 ⇒ 自适应惰性）：这是有意的 fail-safe。声明读
// 不到时若按「推荐值照用」处理，等于把运营的安全闸门拆掉；退化成基协议则最
// 坏只是「自适应没生效」，与启用前行为一致。
func (c *Client) adaptiveAllowedByProvider(ctx context.Context, providerIDs []int) map[int][]string {
	if c.dbPool == nil || len(providerIDs) == 0 {
		return nil
	}
	rows, err := c.dbPool.Query(ctx, `
		SELECT provider_id, setting_value
		FROM provider_settings
		WHERE setting_key = $1
		  AND enabled = TRUE
		  AND provider_id = ANY($2)
	`, ProviderSettingKeyAdaptiveAllowed, providerIDs)
	if err != nil {
		slog.Warn("adaptive protocol: allowed-list query failed; adaptive stays inert",
			"error", err, "provider_ids", providerIDs)
		return nil
	}
	defer rows.Close()

	out := make(map[int][]string, len(providerIDs))
	for rows.Next() {
		var providerID int
		var raw json.RawMessage
		if err := rows.Scan(&providerID, &raw); err != nil {
			slog.Warn("adaptive protocol: allowed-list row scan failed; skipping row",
				"error", err, "provider_id", providerID)
			continue
		}
		var declared []string
		if err := json.Unmarshal(raw, &declared); err != nil {
			slog.Warn("adaptive protocol: allowed-list value is not a string array; treating as undeclared",
				"error", err, "provider_id", providerID)
			continue
		}
		normalized := providercatalog.NormalizeAdaptiveAllowed(declared)
		if len(normalized) == 0 {
			slog.Warn("adaptive protocol: allowed-list declares no usable protocol; adaptive stays inert",
				"provider_id", providerID, "declared", declared)
			continue
		}
		out[providerID] = normalized
	}
	if err := rows.Err(); err != nil {
		slog.Warn("adaptive protocol: allowed-list iteration failed; adaptive stays inert",
			"error", err, "provider_ids", providerIDs)
		return nil
	}
	return out
}

// resolveAdaptiveCandidates 是候选出口的接线：仅当本批候选真的携带选择器时
// 才查一次库。没有自适应候选的请求路径（今天的全部流量）零额外往返。
func (c *Client) resolveAdaptiveCandidates(ctx context.Context, cands []Candidate) int {
	providerIDs := adaptiveProviderIDs(cands)
	if len(providerIDs) == 0 {
		return 0
	}
	allowed := c.adaptiveAllowedByProvider(ctx, providerIDs)
	changed := resolveAdaptiveProtocols(cands, allowed)
	// Debug 而非 Info：这是请求路径上的逐批日志，自适应供应商的每一条请求都会
	// 走到这里。Info 会把线上日志量按请求数放大。
	slog.Debug("adaptive protocol: resolved candidate protocols",
		"providers", providerIDs, "changed", changed, "declared", allowed != nil)
	return changed
}
