package safety

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrBlocked 表示内容被拦截
	ErrBlocked = errors.New("content blocked by safety filter")
)

// Filter 是内容安全过滤器接口
type Filter interface {
	// CheckRequest 检查请求内容
	CheckRequest(ctx context.Context, req *CheckRequest) (*CheckResult, error)

	// CheckResponse 检查响应内容
	CheckResponse(ctx context.Context, resp *CheckResponse) (*CheckResult, error)

	// UpdateRules 更新规则
	UpdateRules(rules []Rule) error

	// Metrics 返回统计指标
	Metrics() FilterMetrics
}

// CheckRequest 检查请求
type CheckRequest struct {
	Content  string
	UserID   string
	Metadata map[string]string
}

// CheckResponse 检查响应
type CheckResponse struct {
	Content  string
	ModelID  string
	Metadata map[string]string
}

// CheckResult 检查结果
type CheckResult struct {
	Safe             bool     // 是否安全
	Action           Action   // 动作
	Reason           string   // 原因
	MatchedRules     []string // 匹配的规则
	Hits             []Hit    // 匹配详情
	SanitizedContent string   // 脱敏后内容
}

// Action 动作类型
type Action string

const (
	ActionAllow    Action = "allow"
	ActionBlock    Action = "block"
	ActionWarn     Action = "warn"
	ActionSanitize Action = "sanitize"
)

// Hit 匹配项
type Hit struct {
	RuleID   string
	RuleName string
	Pattern  string
	Position int
	Length   int
	Severity Severity
}

// Severity 严重程度
type Severity string

const (
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

// Rule 规则
type Rule struct {
	ID          string
	Name        string
	Type        RuleType
	Pattern     string
	Action      Action
	Severity    Severity
	Enabled     bool
	WhiteList   []string
	Description string
}

// RuleType 规则类型
type RuleType string

const (
	RuleTypeKeyword RuleType = "keyword"
	RuleTypeRegex   RuleType = "regex"
)

// FilterMetrics 过滤器统计
type FilterMetrics struct {
	TotalChecks       int64
	TotalBlocked      int64
	TotalWarnings     int64
	TotalSanitized    int64
	AverageLatency    time.Duration
	HitsByRule        map[string]int64
	BlockedByRule     map[string]int64
	BlockedBySeverity map[Severity]int64
}
