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
	// loadAffinity gates the per-request routing_user_affinity query.
	// 默认 false：Classifier 尚不消费 EnhancedSignals（2026-09-07 审计：
	// 查询结果被整体丢弃，等于每次 auto 请求白付一次 DB 往返），Week 2
	// 把增强字段接入分类器后再由 Options.LoadUserAffinity 打开。
	loadAffinity bool
	// affinityCache 是可选的 Redis 亲和力缓存（P2.2 Track A）。nil =
	// 直查 DB（原行为）；非 nil 时 loadAffinity 读路径与
	// UpdateUserAffinity 的读-改-写基线都经缓存感知访问器 getAffinity，
	// 且 upsert 成功后 Invalidate 保持缓存一致性。
	affinityCache *AffinityCache
}

// NewClassificationEnhancer constructs an enhancer instance.
func NewClassificationEnhancer(pool *pgxpool.Pool) *ClassificationEnhancer {
	return &ClassificationEnhancer{
		affinityDAO: NewUserAffinityDAO(pool),
	}
}

// SetAffinityCache 注入 Redis 亲和力缓存（P2.2 Track A）。nil 是合法值
// （清除缓存，回到直查 DB）。经 RealOptimizer.WithAffinityCache 桥接，
// 由 cmd/gateway 在 redis 客户端可用且 ROUTING_OPT_AFFINITY_CACHE=true
// 时调用。
func (e *ClassificationEnhancer) SetAffinityCache(c *AffinityCache) {
	e.affinityCache = c
}

// Enhance implements the PreClassify hook.
// Returns EnhancedSignals with user affinities, session mode, and time context.
//
//   - User affinity: 经 getAffinity（P2.2 Track A 注入 Redis 缓存时走
//     缓存，否则直查 DB）；loadAffinity=false 时整体跳过（零行为变化）
//   - Session mode: 优先按 ClientType（cursor/claude-code 等）识别，UA 兜底
//   - Time context: UTC 时间判断 peak hour (9am-6pm Mon-Fri)
//
// Week 2 完整版本：
//   - 更精准的 session mode 识别（请求频率、任务类型分布）
//   - 用户时区支持（从 API key metadata 获取）
func (e *ClassificationEnhancer) Enhance(ctx context.Context, signals interface{}, userID string, clientType string) (*EnhancedSignals, error) {
	enhanced := &EnhancedSignals{
		Original:       signals,
		UserAffinities: make(map[string]float64),
		SessionMode:    "unknown",
		TimeContext:    TimeContext{},
	}

	// 1. User affinity（经缓存感知访问器：有 Redis 缓存走缓存，miss 回源
	// DB 并回填；无缓存直查 DB）。loadAffinity=false 时跳过——增强字段目
	// 前没有下游消费者，省掉每 auto 请求一次的无效查询（2026-09-07 审计 P1）。
	if e.loadAffinity && userID != "" && userID != "0" {
		affinity, err := e.getAffinity(ctx, userID)
		if err == nil && affinity != nil {
			// 将 task type count 转换为 normalized distribution [0, 1]
			enhanced.UserAffinities = normalizeDistribution(affinity.TaskTypeDistribution)
		}
		// 忽略错误（首次请求或数据库不可用），返回空 affinities
	}

	// 2. Session mode detection (ClientType 优先，匹配不到再走 UA 规则)
	enhanced.SessionMode = detectSessionMode(clientType, "")

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

// detectSessionMode identifies the session mode from ClientType (X-Gw-Client-Type,
// e.g. "cursor", "claude-code") with User-Agent fallback.
//
// ClientType 取值来自 ClassificationSignals.ClientType（网关已在 relay 层解析），
// 命中即返回；否则用 userAgent 的模式匹配兜底；两者都无 → "api"。
func detectSessionMode(clientType, userAgent string) string {
	// 1) ClientType exact/prefix match (normalized: lowercase, spaces→dash)
	ct := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(clientType), " ", "-"))
	switch {
	case matchAny(ct, "cursor", "claude-code", "windsurf", "vscode", "vs-code",
		"jetbrains", "intellij", "pycharm", "webstorm", "goland", "copilot", "roocode"):
		return "ide"
	case matchAny(ct, "curl", "httpie", "wget", "python-requests", "go-http-client",
		"axios", "node-fetch", "cli"):
		return "cli"
	case matchAny(ct, "web", "browser"):
		return "web"
	}
	if ct != "" {
		// 未知 ClientType：按 api 处理（非浏览器/IDE 形态的显式标识）
		return "api"
	}
	return detectSessionModeByUA(userAgent)
}

// matchAny reports whether s equals any of the candidates.
func matchAny(s string, candidates ...string) bool {
	if s == "" {
		return false
	}
	for _, c := range candidates {
		if s == c {
			return true
		}
	}
	return false
}

// detectSessionModeByUA is the User-Agent fallback (substring match).
func detectSessionModeByUA(ua string) string {
	lower := strings.ToLower(ua)

	// IDE patterns
	for _, pattern := range []string{
		"cursor", "claude code", "windsurf", "vscode", "jetbrains",
		"intellij", "pycharm", "webstorm", "goland",
	} {
		if strings.Contains(lower, pattern) {
			return "ide"
		}
	}

	// CLI patterns
	for _, pattern := range []string{
		"curl", "httpie", "wget", "python-requests", "go-http-client",
		"axios", "node-fetch",
	} {
		if strings.Contains(lower, pattern) {
			return "cli"
		}
	}

	// Web patterns (browser)
	for _, pattern := range []string{"mozilla", "chrome", "safari", "firefox", "edge", "opera"} {
		if strings.Contains(lower, pattern) {
			return "web"
		}
	}

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
// P2.2 Track A 缓存接入后：
//   - 读-改-写的读基线经 getAffinity（缓存命中时省一次 DB 往返；基线
//     可能滞后至多一个 TTL，与本就存在的并发 last-write-wins 同量级）
//   - DB upsert 成功后 Invalidate 删除 Redis key（del-on-write）：下次
//     读自然回填最新 DB 状态，实现最简单且不存在写缓存失败窗口
func (e *ClassificationEnhancer) UpdateUserAffinity(ctx context.Context, userID string, taskType string, provider string) error {
	if userID == "" || userID == "0" {
		return nil // 匿名用户不更新
	}

	// 获取当前 affinity（如果不存在则初始化）。缓存感知访问器对"无数据"
	// 返回 (nil, nil)，与 DAO 的 ErrNoRows 一样走首次建档分支。
	affinity, err := e.getAffinity(ctx, userID)
	if err != nil || affinity == nil {
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

	// Upsert to DB（写穿 DB，缓存一致性靠成功后的 del-on-write）
	if err := e.affinityDAO.Upsert(ctx, affinity); err != nil {
		return err
	}
	if e.affinityCache != nil {
		e.affinityCache.Invalidate(userID)
	}
	return nil
}

// getAffinity 是缓存感知的亲和力读取访问器：注入了 AffinityCache 时走
// 缓存（miss 回源 DB 并回填，Redis/DB 故障按缓存内部纪律降级），否则
// 保持原直查 DAO 行为。返回语义：(aff, nil)=有数据；(nil, nil)=确认
// 无数据；(nil, err)=DB 故障。
func (e *ClassificationEnhancer) getAffinity(ctx context.Context, userID string) (*UserAffinity, error) {
	if e.affinityCache != nil {
		return e.affinityCache.Get(ctx, userID)
	}
	return e.affinityDAO.GetByUserID(ctx, userID)
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
