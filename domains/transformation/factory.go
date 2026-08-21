package transformation

import (
	"context"
	"hash/crc32"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/kaixuan/llm-gateway-go/domain" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// TransportFactory 根据配置选择 IRTransport 或 LegacyTransport。
//
// 灰度策略（优先级从高到低）：
//  1. 全局开关 TRANSPORT_LAYER_IR_ENABLED
//  2. 租户白名单 TRANSPORT_IR_TENANT_WHITELIST
//  3. 模型白名单 TRANSPORT_IR_MODEL_WHITELIST
//  4. 百分比灰度 TRANSPORT_IR_ROLLOUT_PERCENT（基于 tenant_id+model 哈希稳定分配）
//  5. 默认 IR；显式 false 或流式熔断时回退 Legacy
type TransportFactory struct {
	mu              sync.RWMutex
	irTransport     *IRTransport
	legacyTransport *LegacyTransport
	enabled         bool
	tenantWhitelist map[string]struct{}
	modelWhitelist  map[string]struct{}
	rolloutPercent  int

	// conversion_path 记录机制（spec §10.4.1）：
	// 每次 Pick 后记录最后一次选择的路径与原因，供运维审计"禁止无记录切换"。
	lastPath   string // 最后一次 Pick 选择的路径："ir" | "legacy"（受 mu 保护）
	lastReason string // 最后一次 Pick 的原因（受 mu 保护）

	// 原子计数器：累计 Pick 选择 ir / legacy 的次数，供监控切换比例。
	irCount     atomic.Uint64
	legacyCount atomic.Uint64
}

// NewTransportFactory 构造一个工厂。
func NewTransportFactory() *TransportFactory {
	return &TransportFactory{
		irTransport:     NewIRTransport(),
		legacyTransport: NewLegacyTransport(),
		enabled:         true,
		tenantWhitelist: make(map[string]struct{}),
		modelWhitelist:  make(map[string]struct{}),
		rolloutPercent:  100,
	}
}

// Reload 从环境变量重新加载灰度配置。
func (f *TransportFactory) Reload() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.enabled = os.Getenv("TRANSPORT_LAYER_IR_ENABLED") != "false"
	f.tenantWhitelist = parseList(os.Getenv("TRANSPORT_IR_TENANT_WHITELIST"))
	f.modelWhitelist = parseList(os.Getenv("TRANSPORT_IR_MODEL_WHITELIST"))
	f.rolloutPercent = parseInt(os.Getenv("TRANSPORT_IR_ROLLOUT_PERCENT"), 100, 0, 100)

	// 同步 metric
	SetActiveImplementation("ir", f.enabled)
	SetActiveImplementation("legacy", true)
}

// Pick 根据灰度策略选择 Transport 实现。
//
// 每次 Pick 都会记录 conversion_path 与选择原因（spec §10.4.1）：
//   - 选中 Legacy 时以 slog.Info 记录（"禁止无记录切换"）
//   - 选中 IR 时以 slog.Debug 记录（正常路径，不需要 Info 级别）
//
// 同时更新 GetConversionPathStats 返回的原子计数。
func (f *TransportFactory) Pick(ctx context.Context, envelope *domain.RequestEnvelope) TransportLayer {
	path, reason := f.pickDecision(envelope)

	// 更新最后一次选择（受 mu 保护）
	f.mu.Lock()
	f.lastPath = path
	f.lastReason = reason
	f.mu.Unlock()

	// 累计原子计数
	if path == "ir" {
		f.irCount.Add(1)
		slog.DebugContext(ctx, "transport.pick: conversion_path",
			"conversion_path", "ir",
			"reason", reason,
		)
		return f.irTransport
	}

	f.legacyCount.Add(1)
	// spec §10.4.1: 禁止无记录切换 → Legacy 路径必须 Info 级别记录原因
	slog.InfoContext(ctx, "transport.pick: conversion_path",
		"conversion_path", "legacy",
		"reason", reason,
	)
	return f.legacyTransport
}

// ConversionPath 返回最后一次 Pick 选择的路径："ir" 或 "legacy"。
//
// 用于运维/审计快速确认当前请求实际走的是哪条转换链路。
// 在尚未调用 Pick 之前返回空字符串。
func (f *TransportFactory) ConversionPath() string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.lastPath
}

// ConversionReason 返回最后一次 Pick 选择的原因（与 ConversionPath 配对）。
func (f *TransportFactory) ConversionReason() string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.lastReason
}

// GetConversionPathStats 返回累计的 (irCount, legacyCount) 切换计数，供运维监控切换比例。
// 计数通过原子操作更新，可在并发 Pick 下安全读取。
func (f *TransportFactory) GetConversionPathStats() (irCount, legacyCount uint64) {
	return f.irCount.Load(), f.legacyCount.Load()
}

// pickDecision 根据灰度策略决定走 IR 还是 Legacy，并返回原因字符串。
//
// 原因取值（用于日志 conversion_path + reason）：
//   - "whitelist"         IR：命中租户或模型白名单
//   - "rollout_percent"   IR：百分比灰度命中（含 100% 兜底与 tenant+model 哈希命中）
//   - "disabled"          Legacy：全局开关未开启
//   - "circuit_breaker"   Legacy：IR 流式路径熔断，强制降级
//   - "rollout_percent_0" Legacy：开关开但 rollout_percent=0，或哈希未命中百分比区间
func (f *TransportFactory) pickDecision(envelope *domain.RequestEnvelope) (path, reason string) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	// 1. 全局开关
	if !f.enabled {
		return "legacy", "disabled"
	}

	// 2. 流式降级熔断（仅对流式请求生效）
	// 如果 IR 流式路径熔断，强制走 Legacy 以避免对生产造成持续影响
	if envelope != nil && envelope.Transport != nil && envelope.Transport.IsStream {
		if f.irTransport != nil && f.irTransport.cb != nil && f.irTransport.cb.ShouldFallback() {
			return "legacy", "circuit_breaker"
		}
	}

	// 3. 租户白名单
	if len(f.tenantWhitelist) > 0 && envelope != nil && envelope.Tenant != nil {
		if _, ok := f.tenantWhitelist[envelope.Tenant.ID]; ok {
			return "ir", "whitelist"
		}
	}

	// 4. 模型白名单
	if len(f.modelWhitelist) > 0 && envelope != nil && envelope.Transport != nil {
		if _, ok := f.modelWhitelist[envelope.Transport.ClientModel]; ok {
			return "ir", "whitelist"
		}
	}

	// 5. 按百分比灰度
	if f.rolloutPercent == 0 {
		return "legacy", "rollout_percent_0"
	}
	if f.rolloutPercent == 100 {
		return "ir", "rollout_percent"
	}

	// 基于 tenant_id + model 哈希分流（稳定分配）
	if envelope == nil || envelope.Tenant == nil || envelope.Transport == nil {
		return "legacy", "rollout_percent_0"
	}
	key := envelope.Tenant.ID + ":" + envelope.Transport.ClientModel
	hash := crc32.ChecksumIEEE([]byte(key))
	if (hash % 100) < uint32(f.rolloutPercent) {
		return "ir", "rollout_percent"
	}
	return "legacy", "rollout_percent_0"
}

// IR 返回 IR 实现（用于测试和直接访问）。
func (f *TransportFactory) IR() *IRTransport { return f.irTransport }

// Legacy 返回 Legacy 实现。
func (f *TransportFactory) Legacy() *LegacyTransport { return f.legacyTransport }

// Enabled 报告 IR 全局开关是否启用。
func (f *TransportFactory) Enabled() bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.enabled
}

func parseList(s string) map[string]struct{} {
	out := make(map[string]struct{})
	if s == "" {
		return out
	}
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out[t] = struct{}{}
		}
	}
	return out
}

func parseInt(s string, def, min, max int) int {
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
