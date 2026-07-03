package ursm

import (
	"os"
	"strconv"
	"time"
)

// Config URSM配置
type Config struct {
	// 缓存TTL
	MemCacheTTL   time.Duration
	RedisCacheTTL time.Duration
	StaleTTL      time.Duration

	// 指纹槽配置
	FpSlotConfig FpSlotConfig

	// 成本评分权重
	ScoringWeights ScoringWeights

	// 故障阈值
	ConsecutiveFailThreshold int
	ProbeThreshold           int
}

// FpSlotConfig 指纹槽配置
type FpSlotConfig struct {
	SlotTTL      time.Duration // 槽TTL（30分钟）
	PinTTL       time.Duration // Pin TTL（24小时）
	ActiveGate   time.Duration // 活跃阈值（5分钟）
	DefaultLimit int           // 默认槽数限制
}

// ScoringWeights 成本评分权重
type ScoringWeights struct {
	PriceWeight     float64 // 价格权重（30%）
	SpeedWeight     float64 // 速度权重（40%）
	StabilityWeight float64 // 稳定性权重（30%）
}

// DefaultConfig 返回默认配置
func DefaultConfig() *Config {
	return &Config{
		MemCacheTTL:   10 * time.Second,
		RedisCacheTTL: 5 * time.Minute,
		StaleTTL:      2 * time.Minute,

		FpSlotConfig: FpSlotConfig{
			SlotTTL:      30 * time.Minute,
			PinTTL:       24 * time.Hour,
			ActiveGate:   5 * time.Minute,
			DefaultLimit: 5,
		},

		ScoringWeights: ScoringWeights{
			PriceWeight:     0.3,
			SpeedWeight:     0.4,
			StabilityWeight: 0.3,
		},

		ConsecutiveFailThreshold: 3,
		ProbeThreshold:           3,
	}
}

// LoadFromEnv 从环境变量加载配置
func LoadFromEnv() *Config {
	cfg := DefaultConfig()

	if val := os.Getenv("URSM_MEM_CACHE_TTL"); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			cfg.MemCacheTTL = d
		}
	}

	if val := os.Getenv("URSM_REDIS_CACHE_TTL"); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			cfg.RedisCacheTTL = d
		}
	}

	if val := os.Getenv("URSM_FP_SLOT_TTL"); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			cfg.FpSlotConfig.SlotTTL = d
		}
	}

	if val := os.Getenv("URSM_FP_PIN_TTL"); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			cfg.FpSlotConfig.PinTTL = d
		}
	}

	if val := os.Getenv("URSM_CONSECUTIVE_FAIL_THRESHOLD"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.ConsecutiveFailThreshold = n
		}
	}

	if val := os.Getenv("URSM_PRICE_WEIGHT"); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			cfg.ScoringWeights.PriceWeight = f
		}
	}

	if val := os.Getenv("URSM_SPEED_WEIGHT"); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			cfg.ScoringWeights.SpeedWeight = f
		}
	}

	if val := os.Getenv("URSM_STABILITY_WEIGHT"); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			cfg.ScoringWeights.StabilityWeight = f
		}
	}

	return cfg
}
