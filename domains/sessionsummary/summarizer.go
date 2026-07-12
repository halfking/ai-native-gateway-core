// Package sessionsummary 实现会话总结与标题生成服务
// 参考 Langfuse 的会话分析能力，结合 LLM 驱动的智能总结
package sessionsummary

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Summarizer 会话总结器
type Summarizer struct {
	db          *sql.DB
	redisClient *redis.Client
	llmClient   LLMClient
	model       string
}

// LLMClient 定义 LLM 客户端接口（便于测试和替换）
type LLMClient interface {
	Complete(ctx context.Context, prompt string, opts ...CompletionOption) (string, error)
}

// CompletionOption LLM 完成选项
type CompletionOption func(*CompletionConfig)

// CompletionConfig LLM 完成配置
type CompletionConfig struct {
	Model        string
	MaxTokens    int
	Temperature  float64
	SystemPrompt string
}

// SessionSummary 会话总结结果
type SessionSummary struct {
	SessionKey  string    `json:"session_key"`
	Title       string    `json:"title"`
	Summary     string    `json:"summary"`
	KeyTopics   []string  `json:"key_topics"`
	UserIntent  string    `json:"user_intent"`
	ContentHash string    `json:"content_hash"`
	GeneratedAt time.Time `json:"generated_at"`
	Version     int       `json:"version"`
}

// SessionMessage 会话中的单条消息
type SessionMessage struct {
	RequestID string    `json:"request_id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Model     string    `json:"model"`
	Timestamp time.Time `json:"timestamp"`
}

// NewSummarizer 创建会话总结器
func NewSummarizer(db *sql.DB, redisClient *redis.Client, llmClient LLMClient) *Summarizer {
	return &Summarizer{
		db:          db,
		redisClient: redisClient,
		llmClient:   llmClient,
		model:       "summary-fast",
	}
}

// SetModel 设置总结使用的稳定模型别名。
func (s *Summarizer) SetModel(model string) {
	if model = strings.TrimSpace(model); model != "" {
		s.model = model
	}
}

// GenerateSummary 生成完整的会话总结（异步调用）
func (s *Summarizer) GenerateSummary(ctx context.Context, tenantID, sessionKey string) (*SessionSummary, error) {
	// 1. 检查缓存
	cached, err := s.getCachedSummary(ctx, tenantID, sessionKey)
	if err == nil && cached != nil {
		return cached, nil
	}

	// 2. 从数据库获取会话消息
	messages, err := s.getSessionMessages(ctx, tenantID, sessionKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get session messages: %w", err)
	}

	if len(messages) == 0 {
		return nil, fmt.Errorf("no messages found for session: %s", sessionKey)
	}

	// 3. 构建分析 Prompt
	prompt := s.buildSummaryPrompt(messages)

	// 4. 调用 LLM 生成总结
	response, err := s.llmClient.Complete(ctx, prompt,
		WithModel(s.model),
		WithMaxTokens(500),
		WithTemperature(0.3),
		WithSystemPrompt("你是一个专业的会话分析助手，擅长提取会话的核心信息并生成简洁的标题和摘要。"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to generate summary: %w", err)
	}

	// 5. 解析 LLM 响应
	summary, err := s.parseSummaryResponse(response, sessionKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse summary response: %w", err)
	}

	// 6. 保存到数据库
	if err := s.saveSummaryToDB(ctx, tenantID, summary); err != nil {
		return nil, fmt.Errorf("failed to save summary: %w", err)
	}

	// 7. 缓存结果（24小时）
	if err := s.cacheSummary(ctx, tenantID, summary, 24*time.Hour); err != nil {
		// 缓存失败不影响主流程
		fmt.Printf("warn: failed to cache summary: %v\n", err)
	}

	return summary, nil
}

// GenerateTitle 快速生成标题（仅基于首条消息）
func (s *Summarizer) GenerateTitle(ctx context.Context, tenantID, sessionKey, firstMessage string) (string, error) {
	// 1. 检查缓存
	if s.redisClient != nil {
		cacheKey := fmt.Sprintf("session:title:%s:%s", tenantID, sessionKey)
		cached, err := s.redisClient.Get(ctx, cacheKey).Result()
		if err == nil && cached != "" {
			return cached, nil
		}
	}

	// 2. 提取前 200 字符（避免 Prompt 过长）
	truncated := firstMessage
	if len(truncated) > 200 {
		truncated = truncated[:200] + "..."
	}

	// 3. 构建 Prompt
	prompt := fmt.Sprintf(`请用10个字以内概括以下对话的主题（不要加引号）：

%s`, truncated)

	// 4. 调用 LLM
	title, err := s.llmClient.Complete(ctx, prompt,
		WithModel(s.model),
		WithMaxTokens(30),
		WithTemperature(0.5),
	)
	if err != nil {
		// 失败时使用首句作为标题
		return s.extractTitleFromFirstMessage(firstMessage), nil
	}

	title = strings.TrimSpace(title)
	title = strings.Trim(title, "\"'`")

	// 限制长度
	if len(title) > 50 {
		title = title[:50]
	}

	// 5. 保存到数据库
	if err := s.updateSessionTitle(ctx, tenantID, sessionKey, title); err != nil {
		fmt.Printf("warn: failed to update title: %v\n", err)
	}

	// 6. 缓存（7天）
	if s.redisClient != nil {
		cacheKey := fmt.Sprintf("session:title:%s:%s", tenantID, sessionKey)
		_ = s.redisClient.Set(ctx, cacheKey, title, 7*24*time.Hour).Err()
	}

	return title, nil
}

// buildSummaryPrompt 构建总结 Prompt
func (s *Summarizer) buildSummaryPrompt(messages []SessionMessage) string {
	var sb strings.Builder
	sb.WriteString("请分析以下对话，按 JSON 格式返回：\n")
	sb.WriteString("{\n")
	sb.WriteString("  \"title\": \"会话标题（20字以内）\",\n")
	sb.WriteString("  \"summary\": \"会话摘要（100-200字）\",\n")
	sb.WriteString("  \"key_topics\": [\"主题1\", \"主题2\", \"主题3\"],\n")
	sb.WriteString("  \"user_intent\": \"chat|code|tool_use|data_analysis|creative|unknown\"\n")
	sb.WriteString("}\n\n")
	sb.WriteString("对话内容：\n---\n")

	// 最多包含前 10 条消息
	maxMessages := 10
	if len(messages) > maxMessages {
		messages = messages[:maxMessages]
	}

	for i, msg := range messages {
		role := "用户"
		if msg.Role == "assistant" {
			role = "助手"
		}

		content := msg.Content
		if len(content) > 500 {
			content = content[:500] + "..."
		}

		fmt.Fprintf(&sb, "\n[消息 %d - %s]:\n%s\n", i+1, role, content)
	}

	sb.WriteString("---\n")
	return sb.String()
}

// buildRollingPrompt 构建增量滚动摘要 Prompt。
//
// 与全量 buildSummaryPrompt 不同：传入「上一次摘要」+「新增消息」，
// LLM 只需融合新信息更新摘要，避免每次全量重算（省 token）。
// 适用 summary_strategy=rolling（默认推荐）。
func (s *Summarizer) buildRollingPrompt(prevSummary string, newMessages []SessionMessage) string {
	var sb strings.Builder
	sb.WriteString("请基于已有摘要和新增对话，更新会话总结。按 JSON 格式返回：\n")
	sb.WriteString("{\n")
	sb.WriteString("  \"title\": \"会话标题（20字以内）\",\n")
	sb.WriteString("  \"summary\": \"更新后的会话摘要（100-200字）\",\n")
	sb.WriteString("  \"key_topics\": [\"主题1\", \"主题2\", \"主题3\"],\n")
	sb.WriteString("  \"user_intent\": \"chat|code|tool_use|data_analysis|creative|unknown\"\n")
	sb.WriteString("}\n\n")

	sb.WriteString("已有摘要：\n")
	if prevSummary == "" {
		sb.WriteString("（无，这是首次总结）\n")
	} else {
		sb.WriteString(prevSummary + "\n")
	}
	sb.WriteString("\n新增对话：\n---\n")

	// 最多包含最新 10 条消息
	maxMessages := 10
	if len(newMessages) > maxMessages {
		newMessages = newMessages[len(newMessages)-maxMessages:]
	}
	for i, msg := range newMessages {
		role := "用户"
		if msg.Role == "assistant" {
			role = "助手"
		}
		content := msg.Content
		if len(content) > 300 {
			content = content[:300] + "..."
		}
		fmt.Fprintf(&sb, "\n[消息 %d - %s]:\n%s\n", i+1, role, content)
	}
	sb.WriteString("---\n")
	return sb.String()
}

// GenerateRollingSummary 增量滚动摘要。
//
// 读取上次摘要 + 自上次以来的新消息 → LLM 融合更新。
func (s *Summarizer) GenerateRollingSummary(ctx context.Context, tenantID, sessionKey string) (*SessionSummary, error) {
	if s.llmClient == nil {
		// 无 LLM，降级全量（全量内部也会因无 LLM 失败）
		return s.GenerateSummary(ctx, tenantID, sessionKey)
	}

	// 读取上次摘要
	prevSummary, lastSummarizedAt, err := s.getPrevSummary(ctx, tenantID, sessionKey)
	if err != nil {
		// 读取失败 → 全量
		return s.GenerateSummary(ctx, tenantID, sessionKey)
	}

	// 读取自上次摘要以来的新消息（无上次则取最近 20 条）
	messages, err := s.getMessagesSince(ctx, tenantID, sessionKey, lastSummarizedAt)
	if err != nil || len(messages) == 0 {
		// 无新消息或出错 → 复用缓存/全量
		return s.GenerateSummary(ctx, tenantID, sessionKey)
	}

	prompt := s.buildRollingPrompt(prevSummary, messages)
	response, err := s.llmClient.Complete(ctx, prompt,
		WithModel(s.model),
		WithMaxTokens(500),
		WithTemperature(0.3),
		WithSystemPrompt("你是一个专业的会话分析助手，擅长增量更新会话总结。"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to generate rolling summary: %w", err)
	}

	summary, err := s.parseSummaryResponse(response, sessionKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse rolling summary: %w", err)
	}

	if err := s.saveSummaryToDB(ctx, tenantID, summary); err != nil {
		return nil, fmt.Errorf("failed to save rolling summary: %w", err)
	}
	if err := s.cacheSummary(ctx, tenantID, summary, 24*time.Hour); err != nil {
		fmt.Printf("warn: failed to cache summary: %v\n", err)
	}
	return summary, nil
}

// getPrevSummary 读取上次摘要文本与时间。
func (s *Summarizer) getPrevSummary(ctx context.Context, tenantID, sessionKey string) (summary string, summarizedAt time.Time, err error) {
	query := `SELECT COALESCE(summary,''), last_summarized_at
		FROM session_summaries WHERE session_key = $1`
	args := []any{sessionKey}
	if tenantID != "" {
		query += " AND tenant_id = $2"
		args = append(args, tenantID)
	}
	var lastAt sql.NullTime
	err = s.db.QueryRowContext(ctx, query, args...).Scan(&summary, &lastAt)
	if lastAt.Valid {
		summarizedAt = lastAt.Time
	}
	return
}

// getMessagesSince 读取自指定时间以来的新消息（rolling 用）。
// since 为零值时返回最近 20 条（首次总结）。
func (s *Summarizer) getMessagesSince(ctx context.Context, tenantID, sessionKey string, since time.Time) ([]SessionMessage, error) {
	query := `
		SELECT request_id,
		       COALESCE(request_body->>'role', 'user') as role,
		       COALESCE(request_body->'messages'->-1->>'content', '') as content,
		       outbound_model, ts
		FROM request_logs
		WHERE gw_session_id = $1`
	args := []any{sessionKey}
	argN := 2
	if tenantID != "" {
		query += " AND tenant_id = $" + strconv.Itoa(argN)
		args = append(args, tenantID)
		argN++
	}
	if !since.IsZero() {
		query += " AND ts > $" + strconv.Itoa(argN)
		args = append(args, since)
		argN++
	}
	query += " ORDER BY ts ASC LIMIT 20"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var messages []SessionMessage
	for rows.Next() {
		var msg SessionMessage
		if err := rows.Scan(&msg.RequestID, &msg.Role, &msg.Content, &msg.Model, &msg.Timestamp); err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}
	return messages, rows.Err()
}

// parseSummaryResponse 解析 LLM 响应
func (s *Summarizer) parseSummaryResponse(response, sessionKey string) (*SessionSummary, error) {
	jsonStr := extractJSON(response)

	var result struct {
		Title      string   `json:"title"`
		Summary    string   `json:"summary"`
		KeyTopics  []string `json:"key_topics"`
		UserIntent string   `json:"user_intent"`
	}

	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	if result.Title == "" {
		result.Title = "未命名会话"
	}
	if result.Summary == "" {
		result.Summary = "暂无总结"
	}

	contentHash := computeContentHash(result.Title + result.Summary)

	return &SessionSummary{
		SessionKey:  sessionKey,
		Title:       result.Title,
		Summary:     result.Summary,
		KeyTopics:   result.KeyTopics,
		UserIntent:  result.UserIntent,
		ContentHash: contentHash,
		GeneratedAt: time.Now(),
		Version:     1,
	}, nil
}

// getSessionMessages 从数据库获取会话消息。
// 注意：request_logs 使用 gw_session_id（350 迁移修正）。
func (s *Summarizer) getSessionMessages(ctx context.Context, tenantID, sessionKey string) ([]SessionMessage, error) {
	query := `
		SELECT
			request_id,
			COALESCE(request_body->>'role', 'user') as role,
			COALESCE(request_body->'messages'->-1->>'content', '') as content,
			outbound_model,
			ts
		FROM request_logs
		WHERE tenant_id = $1 AND gw_session_id = $2
		ORDER BY ts ASC
		LIMIT 20
	`

	rows, err := s.db.QueryContext(ctx, query, tenantID, sessionKey)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	messages := []SessionMessage{}
	for rows.Next() {
		var msg SessionMessage
		if err := rows.Scan(&msg.RequestID, &msg.Role, &msg.Content, &msg.Model, &msg.Timestamp); err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}

	return messages, rows.Err()
}

// saveSummaryToDB 保存总结到数据库（upsert：首次写入创建行，后续更新）
// session_summaries 的 PK 是 session_key，因此用 ON CONFLICT (session_key)
// 实现幂等 upsert。
func (s *Summarizer) saveSummaryToDB(ctx context.Context, tenantID string, summary *SessionSummary) error {
	query := `
		INSERT INTO session_summaries (
			session_key, tenant_id, title, summary, key_topics,
			user_intent, last_summarized_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, NOW(), NOW())
		ON CONFLICT (session_key) DO UPDATE SET
			title = EXCLUDED.title,
			summary = EXCLUDED.summary,
			key_topics = EXCLUDED.key_topics,
			user_intent = EXCLUDED.user_intent,
			last_summarized_at = EXCLUDED.last_summarized_at,
			summary_version = session_summaries.summary_version + 1,
			updated_at = NOW()
	`

	_, err := s.db.ExecContext(ctx, query,
		summary.SessionKey,
		tenantID,
		summary.Title,
		summary.Summary,
		summary.KeyTopics,
		summary.UserIntent,
		summary.GeneratedAt,
	)

	return err
}

// updateSessionTitle 更新会话标题
func (s *Summarizer) updateSessionTitle(ctx context.Context, tenantID, sessionKey, title string) error {
	query := `
		UPDATE session_summaries
		SET title = $1, updated_at = NOW()
		WHERE session_key = $2 AND tenant_id = $3
	`

	_, err := s.db.ExecContext(ctx, query, title, sessionKey, tenantID)
	return err
}

// getCachedSummary 从缓存获取总结
func (s *Summarizer) getCachedSummary(ctx context.Context, tenantID, sessionKey string) (*SessionSummary, error) {
	if s.redisClient == nil {
		return nil, redis.Nil
	}
	cacheKey := fmt.Sprintf("session:summary:%s:%s", tenantID, sessionKey)
	data, err := s.redisClient.Get(ctx, cacheKey).Result()
	if err != nil {
		return nil, err
	}

	var summary SessionSummary
	if err := json.Unmarshal([]byte(data), &summary); err != nil {
		return nil, err
	}

	return &summary, nil
}

// cacheSummary 缓存总结
func (s *Summarizer) cacheSummary(ctx context.Context, tenantID string, summary *SessionSummary, ttl time.Duration) error {
	if s.redisClient == nil {
		return nil
	}
	cacheKey := fmt.Sprintf("session:summary:%s:%s", tenantID, summary.SessionKey)
	data, err := json.Marshal(summary)
	if err != nil {
		return err
	}

	return s.redisClient.Set(ctx, cacheKey, data, ttl).Err()
}

// extractTitleFromFirstMessage 从首条消息提取标题（降级方案）
func (s *Summarizer) extractTitleFromFirstMessage(message string) string {
	message = strings.TrimSpace(message)
	if len(message) > 50 {
		message = message[:50] + "..."
	}
	return message
}

// 辅助函数

// extractJSON 从可能包含 markdown 的文本中提取 JSON
func extractJSON(text string) string {
	start := strings.Index(text, "```json")
	if start != -1 {
		start += 7
		end := strings.Index(text[start:], "```")
		if end != -1 {
			return strings.TrimSpace(text[start : start+end])
		}
	}

	start = strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start != -1 && end != -1 && end > start {
		return strings.TrimSpace(text[start : end+1])
	}

	return text
}

// computeContentHash 计算内容哈希
func computeContentHash(content string) string {
	hash := sha256.Sum256([]byte(content))
	return hex.EncodeToString(hash[:])
}

// Completion 选项函数

func WithModel(model string) CompletionOption {
	return func(c *CompletionConfig) {
		c.Model = model
	}
}

func WithMaxTokens(maxTokens int) CompletionOption {
	return func(c *CompletionConfig) {
		c.MaxTokens = maxTokens
	}
}

func WithTemperature(temperature float64) CompletionOption {
	return func(c *CompletionConfig) {
		c.Temperature = temperature
	}
}

func WithSystemPrompt(systemPrompt string) CompletionOption {
	return func(c *CompletionConfig) {
		c.SystemPrompt = systemPrompt
	}
}

// HandoffMetricsSummary captures the fields handoff writes back into
// session_summaries after a successful handoff. Kept as a plain struct so
// callers (currently domains/hooks/handoff) can pass values without pulling
// in the full Summarizer dependency graph (redis/llmClient).
type HandoffMetricsSummary struct {
	SessionKey        string
	HandoffCount      int // ignored if zero — caller usually passes current+1
	LastHandoffAt     time.Time
	TokensAtTrigger   int
	MessagesAtTrigger int
	LastTriggerReason string
	LastTriggerAt     time.Time
}

// UpdateHandoffMetrics is the canonical writer for the handoff-tracking
// columns on session_summaries. Other modules (e.g. domains/hooks/handoff)
// MUST call this instead of hand-rolling their own UPDATE so that schema
// changes to session_summaries only need to be reflected in one place.
//
// The UPDATE is a no-op if the session_key does not exist (0 rows affected);
// callers that need an upsert should ensure the summary row exists first via
// the normal summarization flow.
//
// This is a package-level helper (not a Summarizer method) deliberately: it
// avoids forcing every handoff call site to construct a full *Summarizer
// (which carries redis + llmClient deps) just to update two tracking columns.
func UpdateHandoffMetrics(ctx context.Context, db *sql.DB, m *HandoffMetricsSummary) error {
	if db == nil {
		return nil
	}
	if m.HandoffCount > 0 {
		_, err := db.ExecContext(ctx, `
			UPDATE session_summaries
			   SET handoff_count = $2,
			       last_handoff_at = $3,
			       tokens_at_trigger = $4,
			       messages_at_trigger = $5,
			       last_trigger_reason = $6,
			       last_trigger_at = $7
			 WHERE session_key = $1`,
			m.SessionKey, m.HandoffCount, m.LastHandoffAt,
			m.TokensAtTrigger, m.MessagesAtTrigger,
			m.LastTriggerReason, m.LastTriggerAt)
		return err
	}
	// Preserve the increment semantics handoff previously used (COALESCE +1)
	// when caller does not pass an absolute count.
	_, err := db.ExecContext(ctx, `
		UPDATE session_summaries
		   SET handoff_count = COALESCE(handoff_count, 0) + 1,
		       last_handoff_at = $2,
		       tokens_at_trigger = $3,
		       messages_at_trigger = $4,
		       last_trigger_reason = $5,
		       last_trigger_at = $6
		 WHERE session_key = $1`,
		m.SessionKey, m.LastHandoffAt,
		m.TokensAtTrigger, m.MessagesAtTrigger, m.LastTriggerReason,
		m.LastTriggerAt)
	return err
}
