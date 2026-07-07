package settings

import "time"

// StickyTTLConfig 配置 Sticky 缓存的 TTL 策略
type StickyTTLConfig struct {
	// L1: Session + Model
	EmbeddingTTL  time.Duration
	ChatTTL       time.Duration
	CompletionTTL time.Duration
	DefaultL1TTL  time.Duration

	// L2: Client + Model
	ClientModelTTL time.Duration

	// L3: Client Baseline
	ClientBaselineTTL time.Duration
}

// DefaultStickyTTLConfig 返回默认的 TTL 配置
func DefaultStickyTTLConfig() StickyTTLConfig {
	return StickyTTLConfig{
		// L1 TTL（基于模型类型）
		EmbeddingTTL:  30 * time.Second,
		ChatTTL:       10 * time.Minute,
		CompletionTTL: 30 * time.Minute,
		DefaultL1TTL:  15 * time.Minute,

		// L2 TTL（客户端+模型）
		ClientModelTTL: 2 * time.Hour, // 从 24h 缩短到 2h

		// L3 TTL（客户端基线）
		ClientBaselineTTL: 24 * time.Hour, // 从 7 天缩短到 1 天
	}
}

// ProductionStickyTTLConfig 返回生产环境优化的 TTL 配置
func ProductionStickyTTLConfig() StickyTTLConfig {
	return StickyTTLConfig{
		EmbeddingTTL:      30 * time.Second,
		ChatTTL:           10 * time.Minute,
		CompletionTTL:     30 * time.Minute,
		DefaultL1TTL:      15 * time.Minute,
		ClientModelTTL:    2 * time.Hour,
		ClientBaselineTTL: 24 * time.Hour,
	}
}

// TestStickyTTLConfig 返回测试环境的 TTL 配置（较短的 TTL）
func TestStickyTTLConfig() StickyTTLConfig {
	return StickyTTLConfig{
		EmbeddingTTL:      5 * time.Second,
		ChatTTL:           1 * time.Minute,
		CompletionTTL:     5 * time.Minute,
		DefaultL1TTL:      2 * time.Minute,
		ClientModelTTL:    10 * time.Minute,
		ClientBaselineTTL: 30 * time.Minute,
	}
}
