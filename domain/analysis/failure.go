package analysis

import "time"

// FailureKind 失败模式分类。
//
// 与 domains/credentialhealth 的错误分类（transient/timeout/...）正交：
// 本枚举关注"用户可见的失败行为模式"，由 FailurePatternMiner 离线聚合。
type FailureKind string

const (
	FailureToolTimeout       FailureKind = "tool_timeout"
	FailureLowQuality        FailureKind = "low_quality_response"
	FailureContextTruncation FailureKind = "context_truncation"
	FailureAuth              FailureKind = "auth_error"
	FailureRateLimit         FailureKind = "rate_limit"
	FailureUpstream          FailureKind = "upstream_error"
	FailureModelDegraded     FailureKind = "model_degraded"
	FailurePolicyViolation   FailureKind = "policy_violation"
)

// FailurePattern 失败模式聚合记录。
//
// Signature 是模式签名（可序列化的特征 map），用于在 miner 中按 signature 去重。
type FailurePattern struct {
	ID          int64
	TenantID    string
	PatternType FailureKind
	Signature   map[string]any
	Occurrences int
	FirstSeen   time.Time
	LastSeen    time.Time
	LastSession string
	LastRequest string
}
