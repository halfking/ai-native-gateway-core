package routingopt

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// =============================================================================
// ClassificationEnhancer: PreClassify 特征增强
// =============================================================================

// ClassificationEnhancer enriches classification signals before the heuristic
// classifier runs. It adds:
//   - User affinity: historical task type distribution (last 100 requests)
//   - Session mode: IDE/CLI/Web detection from User-Agent
//   - Time context: peak/off-peak hour indicator
//
// Design: docs/p2-ml-routing/p2.2-routing-optimization-plugin-design.md §2.1
type ClassificationEnhancer struct {
	affinityDAO *UserAffinityDAO
	// redisCache  *redis.Client  // Week 2: Redis cache (TTL=1h)
}

// NewClassificationEnhancer constructs an enhancer instance.
func NewClassificationEnhancer(pool *pgxpool.Pool) *ClassificationEnhancer {
	return &ClassificationEnhancer{
		affinityDAO: NewUserAffinityDAO(pool),
	}
}

// Enhance implements the PreClassify hook.
// Returns EnhancedSignals with user affinities, session mode, and time context.
//
// Week 1 骨架版本：
//   - User affinity: 从 routing_user_affinity 表查询（无 Redis 缓存）
//   - Session mode: 基于 User-Agent 简单识别
//   - Time context: UTC 时间判断 peak hour (9am-6pm Mon-Fri)
//
// Week 2 完整版本：
//   - Redis 缓存（TTL=1h，miss 时从 DB 加载）
//   - 更精准的 session mode 识别（请求频率、任务类型分布）
//   - 用户时区支持（从 API key metadata 获取）
func (e *ClassificationEnhancer) Enhance(ctx context.Context, signals interface{}, userID string, userAgent string) (*EnhancedSignals, error) {
	enhanced := &EnhancedSignals{
		Original:       signals,
		UserAffinities: make(map[string]float64),
		SessionMode:    "unknown",
		TimeContext:    TimeContext{},
	}
	
	// 1. User affinity (从 DB 查询，Week 2 添加 Redis 缓存)
	if userID != "" && userID != "0" {
		affinity, err := e.affinityDAO.GetByUserID(ctx, userID)
		if err == nil && affinity != nil {
			// 将 task type count 转换为 normalized distribution [0, 1]
			enhanced.UserAffinities = normalizeDistribution(affinity.TaskTypeDistribution)
		}
		// 忽略错误（首次请求或数据库不可用），返回空 affinities
	}
	
	// 2. Session mode detection (基于 User-Agent)
	enhanced.SessionMode = detectSessionMode(userAgent)
	
	// 3. Time context (UTC 时间，Week 2 支持用户时区)
	now := time.Now().UTC()
	enhanced.TimeContext = TimeContext{
		IsPeakHour: isPeakHour(now),
		Hour:       now.Hour(),
		Weekday:    int(now.Weekday()),
	}
	
	return enhanced, nil
}

// normalizeDistribution converts task type counts to normalized probabilities.
// Example: {"code": 60, "chat": 30, "reasoning": 10} → {"code": 0.6, "chat": 0.3, "reasoning": 0.1}
func normalizeDistribution(counts map[string]int) map[string]float64 {
	if len(counts) == 0 {
		return nil
	}
	
	total := 0
	for _, count := range counts {
		total += count
	}
	
	if total == 0 {
		return nil
	}
	
	normalized := make(map[string]float64, len(counts))
	for taskType, count := range counts {
		normalized[taskType] = float64(count) / float64(total)
	}
	
	return normalized
}

// detectSessionMode identifies the session mode from User-Agent.
//
// Week 1 简单识别规则：
//   - IDE: "Cursor", "Claude Code", "Windsurf", "VSCode", "JetBrains"
//   - CLI: "curl", "httpie", "wget", "python-requests"
//   - Web: "Mozilla", "Chrome", "Safari", "Firefox", "Edge"
//   - API: 其他（或空 User-Agent）
//
// Week 2 完整识别：
//   - 结合请求频率（IDE 高频 code，Web 低频 chat）
//   - 任务类型分布（CLI 混合 reasoning+code）
func detectSessionMode(userAgent string) string {
	ua := strings.ToLower(userAgent)
	
	// IDE patterns
	idePatterns := []string{
		"cursor", "claude code", "windsurf", "vscode", "jetbrains",
		"intellij", "pycharm", "webstorm", "goland",
	}
	for _, pattern := range idePatterns {
		if strings.Contains(ua, pattern) {
			return "ide"
		}
	}
	
	// CLI patterns
	cliPatterns := []string{
		"curl", "httpie", "wget", "python-requests", "go-http-client",
		"axios", "node-fetch",
	}
	for _, pattern := range cliPatterns {
		if strings.Contains(ua, pattern) {
			return "cli"
		}
	}
	
	// Web patterns (browser)
	webPatterns := []string{
		"mozilla", "chrome", "safari", "firefox", "edge", "opera",
	}
	for _, pattern := range webPatterns {
		if strings.Contains(ua, pattern) {
			return "web"
		}
	}
	
	// Default: API client
	return "api"
}

// isPeakHour returns true if the given time is during peak hours.
// Peak hours: 9am-6pm Monday-Friday (UTC).
//
// Week 1: 使用 UTC 时间
// Week 2: 支持用户时区（从 API key metadata 获取 timezone）
func isPeakHour(t time.Time) bool {
	// Weekend: off-peak
	weekday := t.Weekday()
	if weekday == time.Saturday || weekday == time.Sunday {
		return false
	}
	
	// Monday-Friday: 9am-6pm UTC
	hour := t.Hour()
	return hour >= 9 && hour < 18
}

// UpdateUserAffinity updates user affinity after each request.
// Called by FeedbackIntegrator after recording feedback.
//
// Week 1 骨架版本：
//   - 简单计数更新（last 100 requests sliding window）
//
// Week 2 完整版本：
//   - 双写 Redis + PostgreSQL
//   - Exponential moving average (recent requests weighted higher)
func (e *ClassificationEnhancer) UpdateUserAffinity(ctx context.Context, userID string, taskType string, provider string) error {
	if userID == "" || userID == "0" {
		return nil // 匿名用户不更新
	}
	
	// 获取当前 affinity（如果不存在则初始化）
	affinity, err := e.affinityDAO.GetByUserID(ctx, userID)
	if err != nil {
		// 首次请求：创建新记录
		affinity = &UserAffinity{
			UserID:               userID,
			TaskTypeDistribution: make(map[string]int),
			PreferredProviders:   make(map[string]float64),
			TotalRequests:        0,
			LastRequestAt:        time.Now(),
		}
	}
	
	// 更新 task type distribution (简单计数，Week 2 改为滑动窗口)
	affinity.TaskTypeDistribution[taskType]++
	affinity.TotalRequests++
	affinity.LastRequestAt = time.Now()
	
	// 更新 provider preference (简单 EMA，Week 2 改为基于成功率)
	if affinity.PreferredProviders == nil {
		affinity.PreferredProviders = make(map[string]float64)
	}
	// EMA: α=0.1 (10% weight to new sample)
	alpha := 0.1
	currentScore := affinity.PreferredProviders[provider]
	affinity.PreferredProviders[provider] = currentScore*(1-alpha) + 1.0*alpha
	
	// Week 1: 简单 session pattern 推断（基于 task type 分布）
	affinity.SessionPattern = inferSessionPattern(affinity.TaskTypeDistribution)
	
	// Upsert to DB (Week 2: 双写 Redis)
	return e.affinityDAO.Upsert(ctx, affinity)
}

// inferSessionPattern infers session mode from task type distribution.
//
// Week 1 简单规则：
//   - IDE: code > 60%
//   - CLI: reasoning + code > 70%
//   - Web: chat > 50%
//   - API: 其他
func inferSessionPattern(distribution map[string]int) *string {
	if len(distribution) == 0 {
		return nil
	}
	
	total := 0
	for _, count := range distribution {
		total += count
	}
	if total == 0 {
		return nil
	}
	
	codeRatio := float64(distribution["code"]) / float64(total)
	chatRatio := float64(distribution["chat"]) / float64(total)
	reasoningRatio := float64(distribution["reasoning"]) / float64(total)
	
	var pattern string
	if codeRatio > 0.6 {
		pattern = "ide"
	} else if reasoningRatio+codeRatio > 0.7 {
		pattern = "cli"
	} else if chatRatio > 0.5 {
		pattern = "web"
	} else {
		pattern = "api"
	}
	
	return &pattern
}
