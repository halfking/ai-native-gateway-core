// adaptive_probe_protocol.go — 探针侧的「协议自适应」消解。
//
// 为什么探针要单独做一次：数据面的消解点在候选加载出口
// (provider/adaptive_protocol.go)，探针走的是另一条读路径——它直接读
// providers.protocol（bg 下十余处 `COALESCE(p.protocol,'openai-completions')`），
// 不经过候选加载。所以「数据面已按模型消解」并不能让探针自动继承。
//
// 不分叉会怎样：探针按 chat 腿探、真实流量按推荐腿发，健康判定与线上表现
// 相反。这正是 2026-09-25 vapeur / gpt-5.3-codex 那次事故的形状（chat 腿
// 400 code 4006、responses 腿 200，节点被判健康而真实请求必然 503）。
//
// 口径与数据面逐字节一致：判定体是同一个纯函数
// providercatalog.ResolveAdaptiveProtocol，这里只负责把「声明的可用协议」
// 搬到判定体面前。
//
// 只改描述符不写库：providers.protocol 的存储值保持 'adaptive'，探针拿到的是
// 这一次探测该用的具体协议。写回存储会让「按模型」失效。
package bg

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	providercatalog "github.com/kaixuan/llm-gateway-go/provider/catalog"
)

// adaptiveProbeSettingKey 的单一真相在 catalog（admin 写入侧与数据面读取侧
// 共用同一个常量，避免 SQL 字面量三处漂移）。
const adaptiveProbeSettingKey = providercatalog.AdaptiveAllowedSettingKey

// adaptiveProbeCacheTTL 是「声明的可用协议」在探针进程内的缓存时长。
//
// 为什么需要缓存：探针是逐 (credential, model) 发起的，一次全量探测会命中同
// 一个 provider 成百上千次（供应商 36 有 474 个模型 × 3 个凭据）。逐次查库会
// 把一条只读两行的表打成热点。
//
// 为什么是 60s 而不是永久：运营改声明后，探针口径要在可接受延迟内跟上。
// 60s 足够覆盖一轮探测，又不会让错误声明长期驻留。
const adaptiveProbeCacheTTL = 60 * time.Second

// adaptiveProbeQueryTimeout 单次声明查询的上限。探针本身已有超时，这里再兜
// 一层：声明读不到最多退化为「未声明」（⇒ 基协议 = openai-completions），
// 不能让一个慢查询把整轮探测拖住。
const adaptiveProbeQueryTimeout = 3 * time.Second

type adaptiveProbeEntry struct {
	allowed  []string
	loadedAt time.Time
}

var (
	adaptiveProbeMu    sync.Mutex
	adaptiveProbeCache = map[int]adaptiveProbeEntry{}
)

// resetAdaptiveProbeCacheForTest 清空进程内缓存。仅供测试；生产代码不调用。
func resetAdaptiveProbeCacheForTest() {
	adaptiveProbeMu.Lock()
	adaptiveProbeCache = map[int]adaptiveProbeEntry{}
	adaptiveProbeMu.Unlock()
}

// resolveAdaptiveProbeProtocol 是纯函数：把存储里的 protocol 值变成这次探测
// 该用的具体协议。
//
// 非选择器输入原样返回（绝大多数字段是五个 canonical 协议之一，这条提前返回
// 让非自适应供应商零行为变化、零数据库入口）。
func resolveAdaptiveProbeProtocol(protocol, rawModel string, allowed []string) string {
	if !providercatalog.IsAdaptiveProtocol(protocol) {
		return protocol
	}
	return providercatalog.ResolveAdaptiveProtocol(rawModel, allowed)
}

// adaptiveProbeAllowed 读取并缓存某 provider 声明的可用协议。
//
// 失败一律返回 nil（= 未声明 ⇒ 自适应退化为 openai-completions，与启用前
// 逐字节等价）：声明读不到时若按「推荐值照用」处理，等于把运营的安全闸门
// 拆掉。负结果同样进缓存，避免一个未声明声明的 provider 被反复打库。
func adaptiveProbeAllowed(ctx context.Context, db *pgxpool.Pool, providerID int) []string {
	if db == nil || providerID <= 0 {
		return nil
	}
	now := time.Now()
	adaptiveProbeMu.Lock()
	if entry, ok := adaptiveProbeCache[providerID]; ok && now.Sub(entry.loadedAt) < adaptiveProbeCacheTTL {
		allowed := entry.allowed
		adaptiveProbeMu.Unlock()
		return allowed
	}
	adaptiveProbeMu.Unlock()

	queryCtx, cancel := context.WithTimeout(ctx, adaptiveProbeQueryTimeout)
	defer cancel()

	var raw json.RawMessage
	err := db.QueryRow(queryCtx, `
		SELECT setting_value
		FROM provider_settings
		WHERE provider_id = $1
		  AND setting_key = $2
		  AND enabled = TRUE
	`, providerID, adaptiveProbeSettingKey).Scan(&raw)

	var declared []string
	switch {
	case err != nil:
		// 含 pgx.ErrNoRows（未声明）——这是常态，不告警。
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("adaptive probe: allowed-list query failed; falling back to base protocol",
				"provider_id", providerID, "error", err)
		}
	case json.Unmarshal(raw, &declared) != nil:
		slog.Warn("adaptive probe: allowed-list value is not a string array; treating as undeclared",
			"provider_id", providerID)
		declared = nil
	default:
		declared = providercatalog.NormalizeAdaptiveAllowed(declared)
		if len(declared) == 0 {
			slog.Warn("adaptive probe: allowed-list declares no usable protocol; falling back to base protocol",
				"provider_id", providerID)
			declared = nil
		}
	}

	adaptiveProbeMu.Lock()
	adaptiveProbeCache[providerID] = adaptiveProbeEntry{allowed: declared, loadedAt: now}
	adaptiveProbeMu.Unlock()
	return declared
}

// resolveProbeProtocol 是探针路线的接线入口：读声明 + 消解。
func resolveProbeProtocol(ctx context.Context, db *pgxpool.Pool, providerID int, protocol, rawModel string) string {
	if !providercatalog.IsAdaptiveProtocol(protocol) {
		return protocol
	}
	resolved := resolveAdaptiveProbeProtocol(protocol, rawModel, adaptiveProbeAllowed(ctx, db, providerID))
	if resolved == protocol {
		// 理论上不可达（ResolveAdaptiveProtocol 恒返回 canonical 成员），
		// 留一条留痕：真的发生时说明选择器漏到了描述符层。
		slog.Warn("adaptive probe: resolver returned the selector itself; using chat default",
			"provider_id", providerID, "raw_model", rawModel)
		return providercatalog.ProtocolOpenAICompletions
	}
	return resolved
}

// alignAdaptiveDirectTarget 把自适应供应商的直连探针目标消解到具体协议。
// 非自适应输入原样返回（零数据库入口、零行为变化）。
func (w *NodeProbeWorker) alignAdaptiveDirectTarget(ctx context.Context, providerID int, protocol, rawModel, baseURL string) (string, string) {
	if !providercatalog.IsAdaptiveProtocol(protocol) {
		return protocol, baseURL
	}
	resolved := resolveProbeProtocol(ctx, w.db, providerID, protocol, rawModel)
	if urls, err := w.listMatchedEndpointBaseURLs(ctx, providerID, resolved); err == nil && len(urls) > 0 {
		baseURL = urls[0]
	}
	return resolved, baseURL
}
