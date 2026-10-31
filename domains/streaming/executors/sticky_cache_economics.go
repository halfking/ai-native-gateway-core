package executors

// sticky_cache_economics.go — C1（sticky 缓存经济学，magpie 四域对比刷新轮
// §五 C1 / §三 B13）。
//
// # 契约（研究方案验收量具）
//
//	① 大 cache（CacheRead+CacheWrite ≥ cacheWorthThresh）续期不变；
//	② 小 cache 到期漂移（cacheWorth 未达 且 TTL 已过半 ⇒ 不续期）；
//	③ 无 usage 上报时零变化（fail-open —— Capture nil / usage 缺失 /
//	   TTL 表缺失都走「续期」，与 C1 之前逐字节同行为）。
//
// # magpie 原契约（affinity.go）
//
//	cacheWorth=1024：CacheRead+CacheWrite ≥1024 才续 stick——小于这个量的
//	会话，缓存收益不值得为它牺牲负载均衡；cacheCold=5m：粘了 5m 且无 1h TTL
//	视为冷。本仓对齐前一半（worth 阈值 + TTL 过半的冷判定），载体保持
//	Redis/PG 多实例形态（不引入 affinity.json 文件态——v5 §七不照搬清单）。
//
// # 为什么只拦 L1 多级路径
//
// recordStickySuccess 的三个出口里，只有 SessionID+Model 多级路径是
// 「prompt cache 亲缘」的载体（L1 = session+model）；stickyPreserveBinding
// 的保持分支本来就不重写绑定（保持现状 = 无续期行为可调）；L3 兜底路径
// 没有 session/model 语义，且 Policy.TTL 是管理员显式配置（不属经济学范围）。
//
// # 引用
//
//	domains/streaming/executors/executor.go —— recordStickySuccess 接线点
//	magpie internal/gateway/affinity.go（cacheWorth :51 / cacheCold）—— 原契约

import (
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
)

// cacheWorthThresh 是「值得粘住」的缓存 token 门槛（magpie cacheWorth=1024）。
// CacheRead+CacheWrite ≥ 1024 ⇒ 本会话的 prompt cache 有真实经济价值。
const cacheWorthThresh = 1024

// stickyCacheWorthRenewal 判定这次成功是否值得续期 sticky 绑定。
// 返回 false ⇒ 跳过 RecordSuccessMultiLevel（让绑定按原 TTL 自然过期，
// 下一次请求走正常选路——「小 cache 到期漂移」判据②）。
//
// fail-open 矩阵（判据③）：Capture nil（非流式路径也可能有 Capture 吗——
// 本仓非流式也有 capture，但防御性判 nil）/ SummaryAsMap 无 cache 字段 /
// 字段为 0 / TTL 表无法回答 ⇒ 全部返回 true（续期），行为与 C1 之前一致。
//
// cacheCold 判定取「TTL 过半」而不是 magpie 的固定 5m：本仓 L1 TTL 按
// model 分档（calculateSessionStickyTTL，10min/30min/15min…），
// 固定 5m 会在 30min 档上过早放走仍有价值的会话；「过半」对每档都等价于
// 「粘住的时间已经超过剩余时间」，与 magpie cacheCold 的经济学意图一致。
func stickyCacheWorthRenewal(params *ExecParams, sticky *StickyCache, credentialID int) bool {
	if params == nil || params.Capture == nil {
		return true
	}
	cacheTokens := captureCacheTokens(params.Capture)
	if cacheTokens <= 0 {
		return true // 无 usage 上报 ⇒ fail-open
	}
	if cacheTokens >= cacheWorthThresh {
		return true // 判据①：大 cache 正常续期
	}
	// 判据②：小 cache —— 粘住时长已过半才放走；未过半仍续期
	// （会话还年轻，下几轮可能长大）。
	elapsed, ok := stickyBindingAge(params, sticky, credentialID)
	if !ok {
		return true // 查不到绑定年龄 ⇒ fail-open
	}
	ttl := calculateSessionStickyTTL(params.Model)
	return elapsed*2 < ttl
}

// captureCacheTokens 从 StreamCapture 读 CacheRead+CacheWrite（0 = 无上报）。
// 走 SummaryAsMap（已有的公开读取面，锁安全），不直接摸私有字段。
func captureCacheTokens(capture *audit.StreamCapture) int {
	if capture == nil {
		return 0
	}
	m := capture.SummaryAsMap()
	total := 0
	if v, ok := m["cache_read_tokens"].(int); ok && v > 0 {
		total += v
	}
	if v, ok := m["cache_write_tokens"].(int); ok && v > 0 {
		total += v
	}
	return total
}

// stickyBindingAge 查当前绑定已经粘了多久（elapsed, ok）。
// ok=false = 无法回答（L1 键不存在 / 指向别的凭据 / Redis 侧无年龄信息
// ⇒ 不是可判定的粘住成功 ⇒ fail-open 续期）。
func stickyBindingAge(params *ExecParams, sticky *StickyCache, credentialID int) (time.Duration, bool) {
	if params == nil || params.Model == "" || sticky == nil {
		return 0, false
	}
	l1, _, _ := buildStickyKeys(
		params.TenantID, params.AppID, params.ApiKeyID,
		params.ClientID.Fingerprint.ClientProfile,
		params.SessionID, params.Model,
	)
	if l1 == "" {
		return 0, false
	}
	return sticky.GetL1BindingAge(l1, params.Model, credentialID)
}
