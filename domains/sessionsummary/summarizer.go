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

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/secretmask"
	"github.com/kaixuan/llm-gateway-go/internal/summarystore"
)

// Summarizer 会话总结器
//
// 2026-08-06: 迁移到 *pgxpool.Pool + summarystore.Upsert，消除了与
// internal/summarystore/store.go 重复的 SQL（之前 saveSummaryToDB 与
// summarystore.Upsert 并存）。其它读路径（getPrevSummary / getMessagesSince /
// getSessionMessages / updateSessionTitle）改用 pgxpool 直查，不再
// 经 *sql.DB 桥接。cmd/gateway/main_pipeline.go 不再为 summarizer 调用
// stdlib.OpenDB（仅 EnhancedPIPlugin 仍需要 *sql.DB）。
type Summarizer struct {
	store         *summarystore.Store
	redisClient   *redis.Client
	llmClient     LLMClient
	model         string
	messageSource MessageSource // where conversation messages are read from; defaults to request_logs
}

// MessageSource is the storage-agnostic read surface for a session's messages.
//
// Historically the summarizer read directly from request_logs /
// request_logs_bodies (the V1 per-turn full-body store). A2/A6 in
// docs/omni-ref3 require the summarizer to keep working once V1 request bodies
// are retired in favor of the V2 incremental session_bodies store. Extracting
// these two reads behind an interface lets a V2 implementation be registered
// (SetMessageSource) without touching GenerateSummary / GenerateRollingSummary.
//
// Implementations must be safe for concurrent use (multiple summaries may run
// in parallel goroutines). Both methods return up to 20 messages in ascending
// chronological order, matching the original request_logs query semantics.
type MessageSource interface {
	// GetSessionMessages returns the most recent messages for a session
	// (the full-summary input).
	GetSessionMessages(ctx context.Context, tenantID, sessionKey string) ([]SessionMessage, error)
	// GetMessagesSince returns messages newer than `since`; when since is the
	// zero time, implementations return the most recent messages (first-summary
	// input). This is the rolling-summary delta input.
	GetMessagesSince(ctx context.Context, tenantID, sessionKey string, since time.Time) ([]SessionMessage, error)
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
//
// 2026-08-06: 接受 *pgxpool.Pool 而非 *sql.DB。nil pool 时仍可构造
// （与之前 *sql.DB=nil 行为一致）—— 所有 DB 方法会 nil-check 后返回
// error 而不 panic。summarystore.NewStore 同样 nil-safe。
func NewSummarizer(pool *pgxpool.Pool, redisClient *redis.Client, llmClient LLMClient) *Summarizer {
	s := &Summarizer{
		store:       summarystore.NewStore(pool),
		redisClient: redisClient,
		llmClient:   llmClient,
		model:       "summary-fast",
	}
	// Default MessageSource reads from request_logs / request_logs_bodies (V1),
	// preserving the pre-A6 behavior exactly. Call SetMessageSource to swap in
	// a V2 session_bodies implementation once V2 read is enabled (A1).
	s.messageSource = &pgRequestLogsSource{pool: pool}
	return s
}

// SetMessageSource overrides where the summarizer reads conversation messages
// from. Intended for registering a V2 session_bodies-backed MessageSource; nil
// is ignored to avoid a nil-dereference on the read path.
func (s *Summarizer) SetMessageSource(src MessageSource) {
	if src == nil {
		return
	}
	s.messageSource = src
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
	if err := s.syncCanonicalSessionTitle(ctx, tenantID, summary.SessionKey, summary.Title); err != nil {
		return nil, fmt.Errorf("failed to synchronize session title: %w", err)
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
	// 2026-07-23: TTL 跟随 SessionTTL（3d），保持数据生命周期一致。
	// session 已经缩短到 3d，title 缓存不应该超过 session 本身。
	if s.redisClient != nil {
		cacheKey := fmt.Sprintf("session:title:%s:%s", tenantID, sessionKey)
		_ = s.redisClient.Set(ctx, cacheKey, title, 3*24*time.Hour).Err()
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

		// docs/omni-ref3 C2: redact pasted API keys / bearer tokens before this
		// content reaches the summary LLM. Masks the summary input only; the
		// persisted session messages are unaffected.
		content = secretmask.MaskSecrets(content)

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
	if err := s.syncCanonicalSessionTitle(ctx, tenantID, summary.SessionKey, summary.Title); err != nil {
		return nil, fmt.Errorf("failed to synchronize rolling session title: %w", err)
	}
	if err := s.cacheSummary(ctx, tenantID, summary, 24*time.Hour); err != nil {
		fmt.Printf("warn: failed to cache summary: %v\n", err)
	}
	return summary, nil
}

// getPrevSummary 读取上次摘要文本与时间。
//
// 2026-08-06: 改用 pgxpool.Pool.Query + pgtype.Timestamptz 替代
// database/sql.NullTime（pgx 没有内置 NullTime，用 *time.Time 即可）。
func (s *Summarizer) getPrevSummary(ctx context.Context, tenantID, sessionKey string) (summary string, summarizedAt time.Time, err error) {
	if s.store == nil {
		return "", time.Time{}, fmt.Errorf("sessionsummary: store not configured")
	}
	query := `SELECT COALESCE(summary,''), last_summarized_at
		FROM session_summaries WHERE session_key = $1`
	args := []any{sessionKey}
	if tenantID != "" {
		query += " AND tenant_id = $2"
		args = append(args, tenantID)
	}
	var lastAt *time.Time  // pgx scans NULL timestamp into *time.Time = nil
	pool := s.store.Pool() // expose a getter on Store (added in this commit)
	if pool == nil {
		return "", time.Time{}, fmt.Errorf("sessionsummary: store pool is nil")
	}
	err = pool.QueryRow(ctx, query, args...).Scan(&summary, &lastAt)
	if lastAt != nil {
		summarizedAt = *lastAt
	}
	return
}

// getMessagesSince delegates to the configured MessageSource (rolling-summary
// delta input). See the MessageSource docs for the V1→V2 migration rationale
// (docs/omni-ref3 A6).
func (s *Summarizer) getMessagesSince(ctx context.Context, tenantID, sessionKey string, since time.Time) ([]SessionMessage, error) {
	if s.messageSource == nil {
		return nil, fmt.Errorf("sessionsummary: message source not configured")
	}
	return s.messageSource.GetMessagesSince(ctx, tenantID, sessionKey, since)
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

// getSessionMessages delegates to the configured MessageSource (full-summary
// input). See the MessageSource docs for the V1→V2 migration rationale
// (docs/omni-ref3 A6).
func (s *Summarizer) getSessionMessages(ctx context.Context, tenantID, sessionKey string) ([]SessionMessage, error) {
	if s.messageSource == nil {
		return nil, fmt.Errorf("sessionsummary: message source not configured")
	}
	return s.messageSource.GetSessionMessages(ctx, tenantID, sessionKey)
}

// pgRequestLogsSource is the default MessageSource: it reads from the V1
// request_logs / request_logs_bodies tables. The SQL is the exact code the
// summarizer ran before A6, moved here verbatim so behavior is unchanged when
// the default source is in use (NewSummarizer installs it automatically).
//
// A nil pool yields errors rather than panics, matching the prior nil-safety
// contract. Safe for concurrent use: each Query opens its own rows.
type pgRequestLogsSource struct {
	pool *pgxpool.Pool
}

func (m *pgRequestLogsSource) getSessionMessagesQuery() string {
	return `
		SELECT
			rl.request_id,
			COALESCE(COALESCE(rb.request_body, rl.request_body)->>'role', 'user') as role,
			COALESCE(COALESCE(rb.request_body, rl.request_body)->'messages'->-1->>'content', '') as content,
			rl.outbound_model,
			rl.ts
		FROM request_logs rl
		LEFT JOIN request_logs_bodies rb
		  ON rb.request_id = rl.request_id
		WHERE rl.tenant_id = $1 AND rl.gw_session_id = $2
		ORDER BY rl.ts ASC
		LIMIT 20
	`
}

func (m *pgRequestLogsSource) GetSessionMessages(ctx context.Context, tenantID, sessionKey string) ([]SessionMessage, error) {
	if m.pool == nil {
		return nil, fmt.Errorf("sessionsummary: store pool is nil")
	}
	rows, err := m.pool.Query(ctx, m.getSessionMessagesQuery(), tenantID, sessionKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

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

// 注意：request_logs 使用 gw_session_id（350 迁移修正）。
// 2026-07-21 Ticket #11: Modified to use LEFT JOIN with request_logs_bodies
// to retrieve full request_body (moved to separate table in #10).
func (m *pgRequestLogsSource) GetMessagesSince(ctx context.Context, tenantID, sessionKey string, since time.Time) ([]SessionMessage, error) {
	if m.pool == nil {
		return nil, fmt.Errorf("sessionsummary: store pool is nil")
	}
	query := `
		SELECT rl.request_id,
		       COALESCE(COALESCE(rb.request_body, rl.request_body)->>'role', 'user') as role,
		       COALESCE(COALESCE(rb.request_body, rl.request_body)->'messages'->-1->>'content', '') as content,
		       rl.outbound_model, rl.ts
		FROM request_logs rl
		LEFT JOIN request_logs_bodies rb
		  ON rb.request_id = rl.request_id
		WHERE rl.gw_session_id = $1`
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

	rows, err := m.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

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

// saveSummaryToDB 保存总结到数据库（upsert：首次写入创建行，后续更新）
//
// 2026-08-06: 改用 internal/summarystore.Upsert 统一持久化路径
// （消除与 admin/auto_summary_generator.go 的 SQL 重复）。summarystore
// 已经处理 RETURNING summary_version + xmax=0 INSERT-vs-UPDATE 区分 + COALESCE
// NULL safety。这里只是把 SessionSummary 转成 summarystore.Summary。
func (s *Summarizer) saveSummaryToDB(ctx context.Context, tenantID string, summary *SessionSummary) error {
	if s.store == nil {
		return fmt.Errorf("sessionsummary: store not configured (pool was nil at NewSummarizer)")
	}
	_, err := s.store.Upsert(ctx, summarystore.Summary{
		SessionKey:     summary.SessionKey,
		TenantID:       tenantID,
		Title:          summary.Title,
		Summary:        summary.Summary,
		KeyTopics:      summary.KeyTopics,
		UserIntent:     summary.UserIntent,
		LastSummarized: summary.GeneratedAt,
	})
	return err
}

// syncCanonicalSessionTitle updates the request-log title source after a
// successful summary. Session summaries do not carry a task ID, so resolve it
// from the latest successful user request and retain the legacy auto scope when
// the session predates task IDs.
func (s *Summarizer) syncCanonicalSessionTitle(ctx context.Context, tenantID, sessionKey, title string) error {
	if s.store == nil {
		return fmt.Errorf("sessionsummary: store not configured")
	}
	pool := s.store.Pool()
	if pool == nil {
		return fmt.Errorf("sessionsummary: store pool is nil")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return fmt.Errorf("sessionsummary: generated title is empty")
	}

	var taskID string
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(NULLIF(TRIM(gw_task_id), ''), 'auto')
		FROM request_logs_with_current_month
		WHERE gw_session_id = $1
		  AND tenant_id = $2
		  AND success = TRUE
		  AND COALESCE(is_auto_request, FALSE) = FALSE
		ORDER BY ts DESC, id DESC
		LIMIT 1
	`, sessionKey, tenantID).Scan(&taskID); err != nil {
		return fmt.Errorf("resolve title task: %w", err)
	}

	_, err := pool.Exec(ctx, `
		INSERT INTO session_titles (task_id, scoped_session_id, title, generated_at, model, api_key_id)
		VALUES ($1, $2, $3, NOW(), 'session-summary', NULL)
		ON CONFLICT (task_id, scoped_session_id) DO UPDATE SET
			title = EXCLUDED.title,
			generated_at = EXCLUDED.generated_at,
			model = EXCLUDED.model,
			api_key_id = EXCLUDED.api_key_id
	`, taskID, sessionKey, title)
	return err
}

// updateSessionTitle 更新会话标题
//
// 2026-08-06: 改用 pgxpool.Pool.Exec 替代 database/sql ExecContext。
// title 仍直接 UPDATE（不走 summarystore.Upsert），因为不需要 summary_version
// 自增 — 标题是辅助字段，与 summary_version 分离。如果以后要审计 title
// 变更历史，可以再考虑加 trigger。
func (s *Summarizer) updateSessionTitle(ctx context.Context, tenantID, sessionKey, title string) error {
	if s.store == nil {
		return fmt.Errorf("sessionsummary: store not configured")
	}
	pool := s.store.Pool()
	if pool == nil {
		return fmt.Errorf("sessionsummary: store pool is nil")
	}
	query := `
		UPDATE session_summaries
		SET title = $1, updated_at = NOW()
		WHERE session_key = $2 AND tenant_id = $3
	`
	_, err := pool.Exec(ctx, query, title, sessionKey, tenantID)
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
//
// 2026-08-06: signature stays as *sql.DB on purpose — the handoff trigger
// hook (domains/hooks/handoff/trigger_hook.go) is the only caller and
// still uses *sql.DB. Migrating the handoff hook to pgxpool is a much
// larger refactor and is out of scope for the Summarizer→pgxpool migration
// (the principal win — saveSummaryToDB consolidating into summarystore —
// is preserved either way). Keep this signature stable for now.
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
