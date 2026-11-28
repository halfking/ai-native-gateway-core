package provider

import (
	"fmt"
	"sync"
	"time"
)

// protocolNegativeEntry 记录一次「协议不被上游支持」的负向证据。
// 用于闸门 2：首次协议失败后，同 (provider, model, protocol) 组合在 TTL
// 内直接回落基协议，避免重复撞 404。
type protocolNegativeEntry struct {
	expireAt time.Time
}

// protocolNegativeMemory 是协议不支持的负向记忆缓存（闸门 2 配套设施）。
// Key: "provider_id:model_name:protocol" (e.g., "123:gpt-4:anthropic-messages")
// Value: protocolNegativeEntry（带过期时间）
//
// 语义：某 (provider, model, protocol) 组合曾在 TTL 内被上游拒绝为
// "unsupported protocol" → 下次同组合直接用基协议，不再尝试推荐协议。
//
// TTL 默认 24 小时：供应商协议支持变化频率低，24h 内不太可能从不支持变为支持；
// 但设置上限防止永久缓存导致供应商升级协议后仍被误判。
var protocolNegativeMemory struct {
	mu      sync.RWMutex
	entries map[string]protocolNegativeEntry
}

func init() {
	protocolNegativeMemory.entries = make(map[string]protocolNegativeEntry)
	// 每小时清理一次过期条目（防止内存泄漏）
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			cleanExpiredProtocolNegativeEntries()
		}
	}()
}

// RecordProtocolUnsupported 记录一次协议不支持的负向证据（导出版本）。
// providerID: 供应商 ID
// modelName: 模型名（rawModel）
// protocol: 被拒绝的协议（如 "anthropic-messages"）
// ttl: 负向记忆的有效期（默认 24 小时）
func RecordProtocolUnsupported(providerID int, modelName, protocol string, ttl time.Duration) {
	recordProtocolUnsupported(providerID, modelName, protocol, ttl)
}

// recordProtocolUnsupported 记录一次协议不支持的负向证据。
// providerID: 供应商 ID
// modelName: 模型名（rawModel）
// protocol: 被拒绝的协议（如 "anthropic-messages"）
// ttl: 负向记忆的有效期（默认 24 小时）
func recordProtocolUnsupported(providerID int, modelName, protocol string, ttl time.Duration) {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	key := protocolNegativeKey(providerID, modelName, protocol)
	entry := protocolNegativeEntry{
		expireAt: time.Now().Add(ttl),
	}

	protocolNegativeMemory.mu.Lock()
	protocolNegativeMemory.entries[key] = entry
	protocolNegativeMemory.mu.Unlock()
}

// isProtocolUnsupported 检查指定 (provider, model, protocol) 组合是否在
// 负向记忆中（即之前被拒绝且未过期）。
func isProtocolUnsupported(providerID int, modelName, protocol string) bool {
	key := protocolNegativeKey(providerID, modelName, protocol)

	protocolNegativeMemory.mu.RLock()
	entry, exists := protocolNegativeMemory.entries[key]
	protocolNegativeMemory.mu.RUnlock()

	if !exists {
		return false
	}
	// 检查是否过期
	if time.Now().After(entry.expireAt) {
		// 过期，删除并返回 false
		protocolNegativeMemory.mu.Lock()
		delete(protocolNegativeMemory.entries, key)
		protocolNegativeMemory.mu.Unlock()
		return false
	}
	return true
}

// protocolNegativeKey 生成负向记忆缓存的键。
func protocolNegativeKey(providerID int, modelName, protocol string) string {
	return fmt.Sprintf("%d:%s:%s", providerID, modelName, protocol)
}

// cleanExpiredProtocolNegativeEntries 清理所有过期的负向记忆条目。
// 由后台 goroutine 每小时调用一次，防止内存泄漏。
func cleanExpiredProtocolNegativeEntries() {
	now := time.Now()
	protocolNegativeMemory.mu.Lock()
	defer protocolNegativeMemory.mu.Unlock()

	for key, entry := range protocolNegativeMemory.entries {
		if now.After(entry.expireAt) {
			delete(protocolNegativeMemory.entries, key)
		}
	}
}

// resetProtocolNegativeMemoryForTest 清空负向记忆缓存（仅供测试使用）。
func resetProtocolNegativeMemoryForTest() {
	protocolNegativeMemory.mu.Lock()
	protocolNegativeMemory.entries = make(map[string]protocolNegativeEntry)
	protocolNegativeMemory.mu.Unlock()
}
