
package transport

import (
	"context"
	"hash/crc32"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/kaixuan/llm-gateway-go/domain"
)

// TransportFactory 根据配置选择 IRTransport 或 LegacyTransport。
//
// 灰度策略（优先级从高到低）：
//  1. 全局开关 TRANSPORT_LAYER_IR_ENABLED
//  2. 租户白名单 TRANSPORT_IR_TENANT_WHITELIST
//  3. 模型白名单 TRANSPORT_IR_MODEL_WHITELIST
//  4. 百分比灰度 TRANSPORT_IR_ROLLOUT_PERCENT（基于 tenant_id+model 哈希稳定分配）
//  5. 默认 Legacy
type TransportFactory struct {
	mu              sync.RWMutex
	irTransport     *IRTransport
	legacyTransport *LegacyTransport
	enabled         bool
	tenantWhitelist map[string]struct{}
	modelWhitelist  map[string]struct{}
	rolloutPercent  int
}

// NewTransportFactory 构造一个工厂。
func NewTransportFactory() *TransportFactory {
	return &TransportFactory{
		irTransport:     NewIRTransport(),
		legacyTransport: NewLegacyTransport(),
		enabled:         false,
		tenantWhitelist: make(map[string]struct{}),
		modelWhitelist:  make(map[string]struct{}),
		rolloutPercent:  0,
	}
}

// Reload 从环境变量重新加载灰度配置。
func (f *TransportFactory) Reload() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.enabled = os.Getenv("TRANSPORT_LAYER_IR_ENABLED") == "true"
	f.tenantWhitelist = parseList(os.Getenv("TRANSPORT_IR_TENANT_WHITELIST"))
	f.modelWhitelist = parseList(os.Getenv("TRANSPORT_IR_MODEL_WHITELIST"))
	f.rolloutPercent = parseInt(os.Getenv("TRANSPORT_IR_ROLLOUT_PERCENT"), 0, 0, 100)

	// 同步 metric
	SetActiveImplementation("ir", f.enabled)
	SetActiveImplementation("legacy", true)
}

// Pick 根据灰度策略选择 Transport 实现。
func (f *TransportFactory) Pick(ctx context.Context, envelope *domain.RequestEnvelope) TransportLayer {
	if f.shouldUseIR(envelope) {
		return f.irTransport
	}
	return f.legacyTransport
}

// shouldUseIR 报告给定 envelope 是否应该走 IR。
func (f *TransportFactory) shouldUseIR(envelope *domain.RequestEnvelope) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()

	// 1. 全局开关
	if !f.enabled {
		return false
	}

	// 2. 流式降级熔断（仅对流式请求生效）
	// 如果 IR 流式路径熔断，强制走 Legacy 以避免对生产造成持续影响
	if envelope != nil && envelope.Transport != nil && envelope.Transport.IsStream {
		if f.irTransport != nil && f.irTransport.cb != nil && f.irTransport.cb.ShouldFallback() {
			return false
		}
	}

	// 3. 租户白名单
	if len(f.tenantWhitelist) > 0 && envelope != nil && envelope.Tenant != nil {
		if _, ok := f.tenantWhitelist[envelope.Tenant.ID]; ok {
			return true
		}
	}

	// 4. 模型白名单
	if len(f.modelWhitelist) > 0 && envelope != nil && envelope.Transport != nil {
		if _, ok := f.modelWhitelist[envelope.Transport.ClientModel]; ok {
			return true
		}
	}

	// 5. 按百分比灰度
	if f.rolloutPercent == 0 {
		return false
	}
	if f.rolloutPercent == 100 {
		return true
	}

	// 基于 tenant_id + model 哈希分流（稳定分配）
	if envelope == nil || envelope.Tenant == nil || envelope.Transport == nil {
		return false
	}
	key := envelope.Tenant.ID + ":" + envelope.Transport.ClientModel
	hash := crc32.ChecksumIEEE([]byte(key))
	return (hash % 100) < uint32(f.rolloutPercent)
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
