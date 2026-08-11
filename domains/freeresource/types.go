package freeresource

import (
	"time"
)

// WindowType 定义配额追踪窗口类型
type WindowType string

const (
	WindowTypeHour5  WindowType = "hour-5"  // 5 小时滚动窗口
	WindowTypeDay1   WindowType = "day-1"   // UTC 日历日
	WindowTypeDay7   WindowType = "day-7"   // 7 日滚动窗口
	WindowTypeMonth1 WindowType = "month-1" // UTC 日历月
)

// FreeType 免费资源类型
type FreeType string

const (
	FreeTypeRecurringDaily    FreeType = "recurring-daily"    // 每日刷新配额
	FreeTypeRecurringMonthly  FreeType = "recurring-monthly"  // 每月刷新配额
	FreeTypeOneTimeInitial    FreeType = "one-time-initial"   // 注册赠送
	FreeTypeRecurringCredit   FreeType = "recurring-credit"   // 每月美元额度
	FreeTypeRecurringUncapped FreeType = "recurring-uncapped" // 永久免费无上限
	FreeTypeKeyless           FreeType = "keyless"            // 无需认证
	FreeTypeDiscontinued      FreeType = "discontinued"       // 已停止
)

// ToSVerdict ToS 合规判定
type ToSVerdict string

const (
	ToSOK        ToSVerdict = "ok"        // ToS 明确允许或无限制
	ToSCaution   ToSVerdict = "caution"   // 灰色地带
	ToSAmbiguous ToSVerdict = "ambiguous" // 未找到明确条款
	ToSAvoid     ToSVerdict = "avoid"     // ToS 明确禁止
	ToSUnknown   ToSVerdict = "unknown"   // 未审查
)

// QuotaWindow 配额窗口元数据
type QuotaWindow struct {
	Type      WindowType
	Start     time.Time
	End       time.Time
	Limit     int64 // 配额上限（从文档或429校准）
	Used      int64 // 已使用
	Exhausted bool  // 是否耗尽
	ResetAt   *time.Time
}

// RecordRequest 记录请求参数
type RecordRequest struct {
	CredentialID int64
	ProviderCode string
	ModelID      string
	TokenCount   int64
	Success      bool
	WindowTypes  []WindowType
	TenantID     string
	Timestamp    time.Time
}

// CorrectionRequest 429校准请求参数
type CorrectionRequest struct {
	CredentialID int64
	ProviderCode string
	ModelID      string
	Headers      map[string]string // HTTP响应头
	Body         []byte            // HTTP响应体（可选，用于 body 关键词甄别 rate_limit vs quota_exhausted）
	TenantID     string
}

// PreflightRequest 配额预检请求参数
type PreflightRequest struct {
	CredentialID    int64
	ProviderCode    string
	ModelID         string
	WindowType      WindowType
	DefaultLimit    int64
	MinRemainingPct float64 // 最少剩余百分比（如0.1表示10%）
	TenantID        string
}

// ApplyRequest carries a proactively-fetched upstream quota snapshot to be
// written into free_quota_tracker, so the existing Preflight reads accurate
// values instead of the 429-reactive defaults. Fed by the quotafetcher package.
type ApplyRequest struct {
	CredentialID int64
	ProviderCode string
	ModelID      string
	TenantID     string
	Total        int64      // corrected_limit (0 = unknown, keep existing)
	LimitReached bool       // mark is_exhausted = TRUE when upstream says exhausted
	ResetAt      *time.Time // auto_reset_at (nil = keep existing / unknown)
}

// FreeResourceEntry 免费资源目录条目
type FreeResourceEntry struct {
	ID            int64
	ProviderCode  string
	ModelID       string
	DisplayName   string
	FreeType      FreeType
	MonthlyTokens int64
	DailyTokens   int64
	CreditTokens  int64
	PoolKey       string
	ToSVerdict    ToSVerdict
	ToSNotes      string
	Enabled       bool
	TenantID      string
}
