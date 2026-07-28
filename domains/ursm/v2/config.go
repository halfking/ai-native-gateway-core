package v2

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

// ScoringWeights are the additive weights used by FilterAndScore when
// computing per-candidate scores. All weights are dimensionless — the
// resulting Score is used only to order candidates, never to bound
// behaviour. Tweak these in DefaultScoringWeights() rather than at
// call sites.
type ScoringWeights struct {
	Price     float64
	Latency   float64
	Stability float64
}

func DefaultScoringWeights() ScoringWeights {
	return ScoringWeights{
		Price:     0.4,
		Latency:   0.4,
		Stability: 0.2,
	}
}

// Config is the static, mode-toggled configuration for the v2 facade.
// Always construct via DefaultConfig(); the zero value is not safe
// (RedisKeyPrefix is empty and Mode is "" rather than api.ModeOff).
type Config struct {
	Mode                api.RolloutMode
	CanaryPercent       int
	CanaryTenants       []string
	CanaryModels        []string
	ShadowSampleRate    float64
	RecordTimeoutMs     int
	RecoveryBlockOnMiss bool
	PersistIntervalSec  int
	RedisKeyPrefix      string
	Window1mTTL         time.Duration
	Window5mTTL         time.Duration
	Window30mTTL        time.Duration
	NodeTTL             time.Duration
	ScoringWeights      ScoringWeights
	// LRUMirrorSize is the capacity of the process-local NodeMirror LRU
	// (M2, spec Decision 2). Default 100000. 0 disables the mirror (every
	// FilterAndScore reads Redis). The mirror is a read accelerator only;
	// Redis remains authoritative and the LRU is never written to before a
	// successful Redis Lua write.
	LRUMirrorSize int
	// LRUMirrorSoftTTL is how long a mirror entry is trusted before the next
	// access re-reads Redis. Default 30s (spec Decision 2). Short enough that
	// a disabled/cooled node is observed within this window even if the LRU
	// still has an older Available=true entry.
	LRUMirrorSoftTTL time.Duration
	// CoolSeconds is the cooling duration (in seconds) when a node is disabled
	// due to consecutive failures. Defaults to 120 (2 minutes) if not set.
	// This should be aligned with the circuit breaker's cooling policies to
	// ensure consistent behavior between the in-memory breaker and URSM v2.
	CoolSeconds int
	// ShadowDoubleWrite opts shadow mode into writing sidecar records to
	// the v2 store. Default false. P0-3 (audit §7.1) flips this true during
	// the 7-day cutover comparison window. Routing stays on legacy
	// credentialstate (selectStateBackendWithReady only trusts v2 in
	// ModeAuthoritative). See rollout.Config.ShadowDoubleWrite for the
	// matching rollout-layer knob — both must agree for the sidecar to
	// fire. Loaded from URSM_V2_SHADOW_DOUBLE_WRITE (truthy 1/true/yes).
	ShadowDoubleWrite bool
}

func DefaultConfig() Config {
	return Config{
		Mode:                api.ModeOff,
		CanaryPercent:       0,
		ShadowSampleRate:    0.01,
		RecordTimeoutMs:     20,
		RecoveryBlockOnMiss: true,
		PersistIntervalSec:  60,
		RedisKeyPrefix:      "ursm:v2:",
		Window1mTTL:         90 * time.Second,
		Window5mTTL:         6 * time.Minute,
		Window30mTTL:        35 * time.Minute,
		NodeTTL:             60 * time.Minute,
		ScoringWeights:      DefaultScoringWeights(),
		// 2026-07-27 (M2): process LRU mirror defaults (spec Decision 2).
		LRUMirrorSize:    100000,
		LRUMirrorSoftTTL: 30 * time.Second,
		// 2026-07-24: 降低冷却时间到2分钟，与circuit breaker的RateLimit/Concurrent冷却时间对齐
		// 减少网关请求中断时长，提升多轮对话质量
		CoolSeconds: 120,
		// P0-3: shadow double-write is opt-in. Operators must explicitly
		// flip URSM_V2_SHADOW_DOUBLE_WRITE=1 for the cutover comparison
		// window. Default off keeps v2 Redis namespace clean.
		ShadowDoubleWrite: false,
	}
}

func LoadFromEnv() Config {
	c := DefaultConfig()
	if v := os.Getenv("URSM_V2_MODE"); v != "" {
		c.Mode = api.RolloutMode(v)
	}
	if v := os.Getenv("URSM_V2_CANARY_PERCENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.CanaryPercent = n
		}
	}
	// 2026-07-24: 支持通过环境变量配置 URSM v2 冷却时间
	if v := os.Getenv("URSM_V2_COOL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.CoolSeconds = n
		}
	}
	// 2026-07-27 (M2): LRU mirror tuning (spec Decision 6). URSM_V2_LRU_SIZE
	// accepts 1000..500000; 0 explicitly disables the mirror.
	if v := os.Getenv("URSM_V2_LRU_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			c.LRUMirrorSize = n
		}
	}
	if v := os.Getenv("URSM_V2_LRU_SOFT_TTL_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.LRUMirrorSoftTTL = time.Duration(n) * time.Millisecond
		}
	}
	// P0-3: shadow double-write env. Truthy (1/true/yes, case-insensitive)
	// turns on sidecar writes in ModeShadow. Operators MUST also leave
	// URSM_V2_MODE=shadow (NOT authoritative) so routing stays on legacy.
	// Invalid values are silently ignored — default-off is the safe choice.
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("URSM_V2_SHADOW_DOUBLE_WRITE"))); v != "" {
		if v == "1" || v == "true" || v == "yes" {
			c.ShadowDoubleWrite = true
		}
	}
	return c
}
