// Package sessionsummary 实现会话总结与标题生成服务
// 参考 Langfuse 的会话分析能力，结合 LLM 驱动的智能总结
package sessionsummary

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	dbpkg "github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/domains/analysis/sessionmeta"
	"github.com/kaixuan/llm-gateway-go/domains/secretmask"
	"github.com/kaixuan/llm-gateway-go/internal/summarystore"
	"github.com/kaixuan/llm-gateway-go/internal/titlestore"
	"github.com/kaixuan/llm-gateway-go/telemetry"
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
	titleStore    *titlestore.Store
	redisClient   *redis.Client
	llmClient     LLMClient
	model         string
	messageSource MessageSource // where conversation messages are read from; defaults to _request_logs
}

// MessageSource is the storage-agnostic read surface for a session's messages.
//
// Historically the summarizer read directly from _request_logs /
// _request_logs_bodies (the V1 per-turn full-body store). A2/A6 in
// docs/omni-ref3 require the summarizer to keep working once V1 request bodies
// are retired in favor of the V2 incremental session_bodies store. Extracting
// these two reads behind an interface lets a V2 implementation be registered
// (SetMessageSource) without touching GenerateSummary / GenerateRollingSummary.
//
// Implementations must be safe for concurrent use (multiple summaries may run
// in parallel goroutines). Both methods return up to 20 messages in ascending
// chronological order, matching the original _request_logs query semantics.
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
	SessionKey string   `json:"session_key"`
	Title      string   `json:"title"`
	Summary    string   `json:"summary"`
	KeyTopics  []string `json:"key_topics"`
	UserIntent string   `json:"user_intent"`
	// 2026-08-26 (migration 606): 会话身份扩展 —— 由总结 LLM 基于系统
	// 提示词前缀识别，LLM 缺失时由规则兜底
	// (telemetry.DetectAgentFromSystemPrompt /
	//  sessionmeta.DetectExpertFromSystemPrompt)。
	AgentType   string    `json:"agent_type,omitempty"`
	ExpertType  string    `json:"expert_type,omitempty"`
	Tags        []string  `json:"tags,omitempty"`
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
		titleStore:  titlestore.New(pool),
		redisClient: redisClient,
		llmClient:   llmClient,
		model:       "summary-fast",
	}
	// Default MessageSource reads from _request_logs / _request_logs_bodies (V1),
	// preserving the pre-A6 behavior exactly. Call SetMessageSource to swap in
	// a V2 session_bodies implementation once V2 read is enabled (A1).
	s.messageSource = &pgRequestLogsSource{pool: pool}
	return s
}

// NewRequestLogsMessageSource returns the V1 _request_logs MessageSource.
func NewRequestLogsMessageSource(pool *pgxpool.Pool) MessageSource {
	return &pgRequestLogsSource{pool: pool}
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

// maxSummaryCompletionTokens 是摘要类调用的 max_tokens 上限。
//
// 2026-11-24 批判审计轮（F-A，P1）：此前 600 被上游 thinking 模型的
// <think> 推理段整段吃光，JSON 尚未输出即被截断 ⇒ 生产 6h 实测 111 次
// "unexpected end of JSON input"（占该 worker 解析失败的 74%）。提到
// 1500 给「thinking + JSON 本体（约 200-400 token）」留出余量；后台
// worker 调用量低（分钟级一次），成本影响可忽略。
const maxSummaryCompletionTokens = 1500

// GenerateSummary 生成完整的会话总结（异步调用）
func (s *Summarizer) GenerateSummary(ctx context.Context, tenantID, sessionKey string) (*SessionSummary, error) {
	// 0. 防御：GenerateRollingSummary 有 nil 检查（降级本函数后仍会走到这里
	// 的 Complete 调用），此前无 LLM 时在 nil interface 解引用处 panic。
	if s.llmClient == nil {
		return nil, fmt.Errorf("sessionsummary: llm client not configured")
	}

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

	// 2.5 提取系统提示词前缀（2026-08-26: 供 LLM 识别智能体/专家类型；
	// 取不到时静默降级为 ""。）
	sysPrefix := s.systemPromptPrefix(ctx, tenantID, sessionKey)

	// 3. 构建分析 Prompt（含系统提示词节选）
	prompt := s.buildSummaryPrompt(messages, sysPrefix)

	// 4. 调用 LLM 生成总结
	response, err := s.llmClient.Complete(ctx, prompt,
		WithModel(s.model),
		WithMaxTokens(maxSummaryCompletionTokens),
		WithTemperature(0.3),
		WithSystemPrompt("你是一个专业的会话分析助手，擅长提取会话的核心信息并生成简洁的标题和摘要，同时识别请求方的智能体类型与专家领域。"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to generate summary: %w", err)
	}

	// 5. 解析 LLM 响应 + 规则兜底（LLM 未返回 agent/expert 时用
	// system-prompt 规则检测补齐）
	summary, err := s.parseSummaryResponse(response, sessionKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse summary response: %w", err)
	}
	enrichSessionIdentity(summary, sysPrefix)

	// 6. 保存到数据库
	if err := s.saveSummaryToDB(ctx, tenantID, summary); err != nil {
		return nil, fmt.Errorf("failed to save summary: %w", err)
	}
	// 2026-11-24 批判审计轮（F-B，P1）：摘要已落库 ⇒ 标题同步失败不应把整个
	// 操作判死——此前 return err 导致 worker 对同一 event 无限重试（生产日志
	// "resolve title task: no rows in result set" 反复出现，每轮重跑一次 LLM）。
	// 标题是辅助字段；同步失败降级为 warn，摘要结果照常返回。
	if err := s.syncCanonicalSessionTitle(ctx, tenantID, summary.SessionKey, summary.Title); err != nil {
		slog.Warn("sessionsummary: canonical title sync failed (summary kept)",
			"session_key", summary.SessionKey, "error", err)
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
		// 2026-09-08 audit: cut on rune boundaries — byte slicing shredded
		// multi-byte CJK text into invalid UTF-8 that fed the summary LLM.
		truncated = truncateRunes(truncated, 200) + "..."
	}

	// 3. 构建 Prompt
	prompt := fmt.Sprintf(`请用10个字以内概括以下对话的主题（不要加引号）：

%s`, truncated)

	// 4. 调用 LLM
	// 2026-11-24 批判审计轮（F-C）：30 token 会被 thinking 模型的 <think> 段
	// 整段吃光 ⇒ 标题从未生成（静默走首句降级），或 <think> 正文被截成 50
	// rune 的垃圾文本直接落库。提到 200 给 thinking 留余量，返回后剥离
	// thinking 标签再取标题。
	title, err := s.llmClient.Complete(ctx, prompt,
		WithModel(s.model),
		WithMaxTokens(200),
		WithTemperature(0.5),
	)
	if err != nil {
		// 失败时使用首句作为标题
		return s.extractTitleFromFirstMessage(firstMessage), nil
	}

	title = stripThinkingTags(title)
	title = strings.TrimSpace(title)
	title = strings.Trim(title, "\"'`")
	// 剥离后可能为空（thinking 吃光预算且无正文）⇒ 走首句降级，不落空标题
	if title == "" || strings.HasPrefix(title, "<think") {
		return s.extractTitleFromFirstMessage(firstMessage), nil
	}

	// 限制长度
	if len(title) > 50 {
		title = truncateRunes(title, 50)
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

// capByTurn caps messages by distinct turn (RequestID), keeping the first
// maxTurns complete turns in slice order. 2026-09-09 audit round 3: the
// per-turn digest source expands each turn into a user+assistant pair
// sharing the turn's RequestID, so a plain [:10] slice silently halved the
// summarized window to 5 turns. For one-message-per-turn sources (V1/V2)
// this is byte-identical to the old [:n]. Messages with an empty RequestID
// each count as their own turn (never merged), preserving old behaviour for
// sources that don't populate it.
func capByTurn(messages []SessionMessage, maxTurns int) []SessionMessage {
	if len(messages) == 0 {
		return messages
	}
	seen := make(map[string]struct{}, maxTurns)
	turns := 0
	cut := len(messages)
	for i, m := range messages {
		if m.RequestID == "" {
			if turns == maxTurns {
				cut = i
				break
			}
			turns++
			continue
		}
		if _, dup := seen[m.RequestID]; !dup {
			if turns == maxTurns {
				cut = i
				break
			}
			seen[m.RequestID] = struct{}{}
			turns++
		}
	}
	return messages[:cut]
}

// buildSummaryPrompt 构建总结 Prompt。
//
// 2026-08-26: 新增 sysPrefix 参数 —— 会话系统提示词前缀（前
// SystemPromptPrefixBytes 字节）。智能体类型（Cursor/ZCode/opencode/...）
// 与专家类型（software_engineering/security/...）几乎都写在客户端注入的
// system prompt 开头，必须把它喂给总结 LLM 才能识别；同时输出 schema
// 扩展出 agent_type / expert_type / tags，为会话打标签与扩展属性。
func (s *Summarizer) buildSummaryPrompt(messages []SessionMessage, sysPrefix string) string {
	var sb strings.Builder
	sb.WriteString("请分析以下对话，按 JSON 格式返回：\n")
	sb.WriteString("{\n")
	sb.WriteString("  \"title\": \"会话标题（20字以内）\",\n")
	sb.WriteString("  \"summary\": \"会话摘要（100-200字）\",\n")
	sb.WriteString("  \"key_topics\": [\"主题1\", \"主题2\", \"主题3\"],\n")
	sb.WriteString("  \"user_intent\": \"chat|code|tool_use|data_analysis|creative|unknown\",\n")
	sb.WriteString("  \"agent_type\": \"发起该会话的智能体/客户端（如 cursor|zcode|opencode|claude-code|kiro|cline|aider；无法判断为 unknown）\",\n")
	sb.WriteString("  \"expert_type\": \"该智能体的专家领域（如 software_engineering|security|data_science|devops|testing|design|research；无法判断为 unknown）\",\n")
	sb.WriteString("  \"tags\": [\"会话标签（3-6个，简短小写英文或中文，如 debugging|go|sql 迁移）\"]\n")
	sb.WriteString("}\n\n")
	// 2026-11-24 批判审计轮（F-E）：生产观测到两类可避免的解析失败——
	// ① LLM 回声对话内容（把被截断的用户 JSON 原样输出，如 {"action":...}）；
	// ② 字段值用省略号截断（"..." 不是合法 JSON）。显式禁止。
	sb.WriteString("硬性要求：直接输出上述 JSON 对象，不要输出任何思考过程；不要复述或引用对话内容；字段值必须完整，禁止用省略号（...）截断。\n\n")
	sb.WriteString("提示：智能体的身份与专家领域通常写在客户端注入的系统提示词（system prompt）开头，请优先依据系统提示词节选判断；对话内容由用户决定，不代表智能体身份。\n\n")
	if strings.TrimSpace(sysPrefix) != "" {
		sb.WriteString("系统提示词节选（来自客户端，仅用于识别智能体与专家领域）：\n<system_prompt>\n")
		sb.WriteString(sysPrefix)
		sb.WriteString("\n</system_prompt>\n\n")
	}
	sb.WriteString("对话内容：\n---\n")

	// 最多包含前 10 轮（按 RequestID 计轮，见 capByTurn）
	messages = capByTurn(messages, 10)

	for i, msg := range messages {
		role := "用户"
		if msg.Role == "assistant" {
			role = "助手"
		}

		content := msg.Content
		if len(content) > 500 {
			content = truncateRunes(content, 500) + "..."
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
func (s *Summarizer) buildRollingPrompt(prevSummary string, newMessages []SessionMessage, sysPrefix string) string {
	var sb strings.Builder
	sb.WriteString("请基于已有摘要和新增对话，更新会话总结。按 JSON 格式返回：\n")
	sb.WriteString("{\n")
	sb.WriteString("  \"title\": \"会话标题（20字以内）\",\n")
	sb.WriteString("  \"summary\": \"更新后的会话摘要（100-200字）\",\n")
	sb.WriteString("  \"key_topics\": [\"主题1\", \"主题2\", \"主题3\"],\n")
	sb.WriteString("  \"user_intent\": \"chat|code|tool_use|data_analysis|creative|unknown\",\n")
	sb.WriteString("  \"agent_type\": \"发起该会话的智能体/客户端（如 cursor|zcode|opencode|claude-code|kiro|cline|aider；无法判断为 unknown）\",\n")
	sb.WriteString("  \"expert_type\": \"该智能体的专家领域（如 software_engineering|security|data_science|devops|testing|design|research；无法判断为 unknown）\",\n")
	sb.WriteString("  \"tags\": [\"会话标签（3-6个，简短小写英文或中文）\"]\n")
	sb.WriteString("}\n\n")

	// F-E 同款（2026-11-24 批判审计轮）：禁止回声对话内容与省略号截断。
	sb.WriteString("硬性要求：直接输出上述 JSON 对象，不要输出任何思考过程；不要复述或引用对话内容；字段值必须完整，禁止用省略号（...）截断。\n\n")

	sb.WriteString("提示：智能体的身份与专家领域通常写在客户端注入的系统提示词（system prompt）开头，请优先依据系统提示词节选判断。\n\n")
	if strings.TrimSpace(sysPrefix) != "" {
		sb.WriteString("系统提示词节选（来自客户端，仅用于识别智能体与专家领域）：\n<system_prompt>\n")
		sb.WriteString(sysPrefix)
		sb.WriteString("\n</system_prompt>\n\n")
	}
	sb.WriteString("已有摘要：\n")
	if prevSummary == "" {
		sb.WriteString("（无，这是首次总结）\n")
	} else {
		sb.WriteString(prevSummary + "\n")
	}
	sb.WriteString("\n新增对话：\n---\n")

	// 最多包含最新 10 轮（按 RequestID 计轮，见 capByTurn）
	slices.Reverse(newMessages)
	newMessages = capByTurn(newMessages, 10)
	slices.Reverse(newMessages)
	for i, msg := range newMessages {
		role := "用户"
		if msg.Role == "assistant" {
			role = "助手"
		}
		// 审计 R8：与全量摘要路径（buildSummaryPrompt）同级——粘贴进对话的
		// key/token 不得原文进入总结 LLM 的 prompt；截断走 rune 边界防止
		// CJK 字节切片产生非法 UTF-8。
		content := msg.Content
		if utf8.RuneCountInString(content) > 300 {
			content = truncateRunes(content, 300) + "..."
		}
		content = secretmask.MaskSecrets(content)
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

	// 系统提示词前缀（2026-08-26: 同全量路径，best-effort；取不到降级 "")
	sysPrefix := s.systemPromptPrefix(ctx, tenantID, sessionKey)

	prompt := s.buildRollingPrompt(prevSummary, messages, sysPrefix)
	response, err := s.llmClient.Complete(ctx, prompt,
		WithModel(s.model),
		WithMaxTokens(maxSummaryCompletionTokens),
		WithTemperature(0.3),
		WithSystemPrompt("你是一个专业的会话分析助手，擅长增量更新会话总结，同时识别请求方的智能体类型与专家领域。"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to generate rolling summary: %w", err)
	}

	summary, err := s.parseSummaryResponse(response, sessionKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse rolling summary: %w", err)
	}
	enrichSessionIdentity(summary, sysPrefix)

	if err := s.saveSummaryToDB(ctx, tenantID, summary); err != nil {
		return nil, fmt.Errorf("failed to save rolling summary: %w", err)
	}
	// F-B 同款（2026-11-24 批判审计轮）：滚动摘要已落库 ⇒ 标题同步失败降级为
	// warn，不再把整个操作判死（避免 worker 对已保存摘要无限重试）。
	if err := s.syncCanonicalSessionTitle(ctx, tenantID, summary.SessionKey, summary.Title); err != nil {
		slog.Warn("sessionsummary: canonical title sync failed (rolling summary kept)",
			"session_key", summary.SessionKey, "error", err)
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

	// 验证提取的内容是否看起来像 JSON
	trimmed := strings.TrimSpace(jsonStr)
	if !strings.HasPrefix(trimmed, "{") {
		// 调试预览（2026-11-24 2041 审计轮脱敏订正）：原始 LLM 响应由用户会话内容
		// 生成 ⇒ 全文（哪怕前 500 字符）进错误日志等于把会话片段落盘（隐私面）
		// ⇒ 只记形态特征（rune 长度 / 首字符类别 / 是否 HTML），不记原文。
		// 2026-11-25 12h 审计轮（0700）收口：safePrefix 只做 rune 截断不脱敏，
		// 前 20/100 rune 仍是用户会话衍生内容 ⇒ 两处 %q 回显一并改为首字符
		// **类别**（describeResponseShape 已含 first，这里不再重复回显原文）。
		return nil, fmt.Errorf("failed to parse JSON: response does not look like JSON (prefix_class=%s), response_shape: %s",
			firstRuneClass(trimmed), describeResponseShape(response))
	}

	var result struct {
		Title      string   `json:"title"`
		Summary    string   `json:"summary"`
		KeyTopics  []string `json:"key_topics"`
		UserIntent string   `json:"user_intent"`
		AgentType  string   `json:"agent_type"`
		ExpertType string   `json:"expert_type"`
		Tags       []string `json:"tags"`
	}

	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		// 同上：原始响应只记形态特征，不落原文（隐私面）。
		// 2026-11-25 12h 审计轮（0700）：extracted 回显改为「rune 长度 + 首键名」，
		// 不再回显原文前 100 rune（原 safePrefix(jsonStr,100) 是会话衍生内容）。
		return nil, fmt.Errorf("failed to parse JSON: %w, extracted_shape: runes=%d first_key=%s, response_shape: %s",
			err, len([]rune(strings.TrimSpace(jsonStr))), extractedFirstKey(jsonStr), describeResponseShape(response))
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
		AgentType:   normalizeIdentityToken(result.AgentType),
		ExpertType:  normalizeIdentityToken(result.ExpertType),
		Tags:        normalizeTags(result.Tags),
		ContentHash: contentHash,
		GeneratedAt: time.Now(),
		Version:     1,
	}, nil
}

// maxSessionTags 单个会话允许持久化的标签数上限（防止 LLM 输出超长 tag 列表
// 撑爆 TEXT[] 与 UI）。
const maxSessionTags = 8

// maxTagRunes 单个标签的长度上限（rune）。
const maxTagRunes = 32

// normalizeTags 对 LLM 输出的 tags 做规整：trim + lower（英文标签）+ 去重 +
// 数量/长度截断。空输入返回空 slice（非 nil，方便一直序列化为 []）。
func normalizeTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, t := range tags {
		v := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(t)), " "))
		if v == "" || v == "unknown" || seen[v] {
			continue
		}
		r := []rune(v)
		if len(r) > maxTagRunes {
			v = string(r[:maxTagRunes])
		}
		seen[v] = true
		out = append(out, v)
		if len(out) >= maxSessionTags {
			break
		}
	}
	return out
}

// normalizeIdentityToken 规整 LLM 输出的 agent_type / expert_type：
// 转小写、去空白，unknown 与空一律归并为 ""（未识别），交给规则兜底。
func normalizeIdentityToken(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" || v == "unknown" || v == "n/a" {
		return ""
	}
	return v
}

// enrichSessionIdentity 用规则兜底补齐 LLM 未识别的 agent_type / expert_type。
// 输入是会话系统提示词前缀（best-effort，可为空）；识别不到则保持 ""。
//
// 规则来源是既有 SSOT，不在此重复实现：
//   - telemetry.DetectAgentFromSystemPrompt（agent 16 类模式，cursor 由
//     "powered by composer" / "operate in cursor" 等特征命中）
//   - sessionmeta.DetectExpertFromSystemPrompt（expert 14 类模式，优先级
//     排序保证 "security engineer" 压过通用 "engineer"）
func enrichSessionIdentity(summary *SessionSummary, sysPrefix string) {
	if summary == nil || sysPrefix == "" {
		return
	}
	if summary.AgentType == "" {
		if agent := telemetry.DetectAgentFromSystemPrompt(sysPrefix); agent != "" {
			summary.AgentType = agent
		}
	}
	if summary.ExpertType == "" {
		if expert := sessionmeta.DetectExpertFromSystemPrompt(sysPrefix); expert != "" && expert != sessionmeta.ExpertUnknown {
			summary.ExpertType = expert
		}
	}
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
// _request_logs / _request_logs_bodies tables. The SQL is the exact code the
// summarizer ran before A6, moved here verbatim so behavior is unchanged when
// the default source is in use (NewSummarizer installs it automatically).
//
// A nil pool yields errors rather than panics, matching the prior nil-safety
// contract. Safe for concurrent use: each Query opens its own rows.
type pgRequestLogsSource struct {
	pool *pgxpool.Pool
}

func (m *pgRequestLogsSource) getSessionMessagesQuery() string {
	// 2026-08-26: 读面切到 _with_current_month 视图（hot ∪ parent）。
	// 此前读 parent _request_logs / _request_logs_bodies，migration 600 后请求体
	// 先落 _request_logs_bodies_hot，父表要等热点提升才有数据，导致会话刚
	// 关闭时总结读不到 fresh 消息（观测：hot-only 会话 GenerateSummary 报
	// "no messages found"）。_with_current_month 视图早已存在且
	// syncCanonicalSessionTitle 已在用，视图只加宽读面、不改语义。
	return `
		SELECT
			rl.request_id,
			COALESCE(rb.request_body->>'role', 'user') as role,
			COALESCE(rb.request_body->'messages'->-1->>'content', '') as content,
			rl.outbound_model,
			rl.ts
		FROM request_logs_with_current_month rl
		LEFT JOIN ` + dbpkg.SessionBodiesSourceSQL() + ` rb
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

// 注意：_request_logs 使用 gw_session_id（350 迁移修正）。
// 2026-07-21 Ticket #11: Modified to use LEFT JOIN with _request_logs_bodies
// to retrieve full request_body (moved to separate table in #10).
func (m *pgRequestLogsSource) GetMessagesSince(ctx context.Context, tenantID, sessionKey string, since time.Time) ([]SessionMessage, error) {
	if m.pool == nil {
		return nil, fmt.Errorf("sessionsummary: store pool is nil")
	}
	// 2026-08-26: 同 getSessionMessagesQuery，读 _with_current_month 视图。
	query := `
		SELECT rl.request_id,
		       COALESCE(rb.request_body->>'role', 'user') as role,
		       COALESCE(rb.request_body->'messages'->-1->>'content', '') as content,
		       rl.outbound_model, rl.ts
		FROM request_logs_with_current_month rl
		LEFT JOIN ` + dbpkg.SessionBodiesSourceSQL() + ` rb
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
		AgentType:      summary.AgentType,
		ExpertType:     summary.ExpertType,
		Tags:           summary.Tags,
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
		ORDER BY ts DESC, request_id DESC
		LIMIT 1
	`, sessionKey, tenantID).Scan(&taskID); err != nil {
		// 2026-11-24 批判审计轮（F-B）：会话没有任何「成功的非自动」请求行
		// （如纯自动/综合会话）⇒ 没有可挂标题的任务 ⇒ 直接跳过，不算失败。
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("resolve title task: %w", err)
	}

	owner := fmt.Sprintf("session-summary:%s:%d", sessionKey, time.Now().UnixNano())
	claim, err := s.titleStore.BeginMutation(ctx, titlestore.Claim{
		TenantID: tenantID, SessionID: sessionKey, Owner: owner,
		TTL: 60 * time.Second, Source: titlestore.SourceSessionSummary,
		SourcePriority: titlestore.SourcePrioritySummary, Explicit: true, TaskID: taskID,
	})
	if err != nil {
		return err
	}
	_, err = s.titleStore.CommitTitle(ctx, claim, tenantID, sessionKey, title, taskID)
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

// stripThinkingTags removes <think>...</think> tags and other XML-like wrapper
// tags that extended thinking models may output before the actual JSON response.
// 2026-11-24 批判审计轮（F-D）：同时覆盖 <thinking> 变体（部分上游模型
// 使用 <thinking> 而非 <think>）。
func stripThinkingTags(text string) string {
	// Remove all <think>...</think> / <thinking>...</thinking> blocks
	for _, pair := range [][2]string{{"<think>", "</think>"}, {"<thinking>", "</thinking>"}} {
		open, close := pair[0], pair[1]
		for {
			start := strings.Index(text, open)
			if start == -1 {
				break
			}
			end := strings.Index(text[start:], close)
			if end == -1 {
				// Unclosed tag, remove just the opening marker itself and let
				// extractJSON find the JSON in what remains
				text = text[:start] + text[start+len(open):]
				break
			}
			text = text[:start] + text[start+end+len(close):]
		}
	}

	// Remove other common XML-like wrapper tags
	for _, tag := range []string{"<response>", "<output>", "<answer>"} {
		if idx := strings.Index(text, tag); idx != -1 {
			text = text[idx+len(tag):]
		}
	}
	for _, tag := range []string{"</response>", "</output>", "</answer>"} {
		if idx := strings.LastIndex(text, tag); idx != -1 {
			text = text[:idx]
		}
	}

	return strings.TrimSpace(text)
}

// extractJSON 从可能包含 markdown 或 thinking tags 的文本中提取 JSON
func extractJSON(text string) string {
	// First strip thinking tags and XML wrappers.
	// ★ 误剥回退（2026-11-24 2041 审计轮）：stripThinkingTags 按**裸字符串匹配**剥
	//   <answer>/</output> 等标签 ⇒ JSON **字符串值内部**的字面标签（如摘要正文
	//   提及 "user asked <answer> what"）会被误切 ⇒ 提取出残缺 JSON。回退策略：
	//   剥离结果若不再是合法 JSON 片段（首尾大括号缺失），回退到原文重新提取
	//   —— 原文里 <think> 前缀形态已由调用方 parseSummaryResponse 的 HasPrefix("{")
	//   与剥离路径覆盖，正常 <think> 包裹场景不受影响。
	stripped := stripThinkingTags(text)
	// ★ 误剥回退判据（2026-11-25 12h 修订审计轮 P1 修正）：★★
	// ★★ 判据必须是「候选**能不能**被解析成 JSON」，而不是「剥离前后大括号
	// ★★ 计数有没有下降」。★★
	// ★★ 旧判据（2026-11-24 2041 轮）用 `strings.Count(stripped,"{") <
	// ★★ strings.Count(text,"{")` ⇒ ★★ **合法地移除整个 thinking 块**同样会让计数
	// ★★ 下降（只要 thinking 正文里出现 {...}）⇒ ★★ 误判成「误剥」⇒ ★★ 回退到
	// ★★ **未剥离**原文 ⇒ ★★ thinking 前缀留在结果里 ⇒ ★★ JSON 解析必失败。
	// ★★ 实测：`<think>{"draft":"x"}</think>{"title":"t"}` ⇒ 旧判据回退 ⇒ 解析失败
	// ★★ （★ 推理模型输出 thinking 里带 JSON 是**常见**形态 ⇒ 影响面不是边缘情况）。
	// ★★
	// ★★ 新判据：★ **两个候选各比一次，谁能被 json 解析就用谁**；★★
	// ★★   ① 剥离后的候选（正常路径：thinking 包裹 / 包装标签）；
	// ★★   ② 原文候选（误剥路径：JSON 字符串值内部含字面 <answer>/</output>，
	// ★★      剥离会把骨架切坏 ⇒ 只有原文能解）。
	// ★★ 两个都解不出来时，★ **返回剥离后的候选**（保持既有「剥离优先」语义，
	// ★★ 例如未闭合 thinking 且内容本身不是 JSON 的形态）。
	candStripped := extractJSONFrom(stripped)
	if json.Valid([]byte(candStripped)) {
		return candStripped
	}
	candRaw := extractJSONWithoutStripping(text)
	if json.Valid([]byte(candRaw)) {
		return candRaw
	}
	return candStripped
}

// extractJSONFrom 是 extractJSON 的主体（不做 thinking 剥离）。
func extractJSONFrom(text string) string {
	// Try markdown JSON blocks first
	start := strings.Index(text, "```json")
	if start != -1 {
		start += 7
		end := strings.Index(text[start:], "```")
		if end != -1 {
			return strings.TrimSpace(text[start : start+end])
		}
	}

	// Find the first { and last } to extract JSON object
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

// truncateRunes cuts s to at most n runes, keeping valid UTF-8. Byte slicing
// produced malformed CJK tails into prompts and stored titles (2026-09-08).
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// extractJSONWithoutStripping 不做 thinking 剥离的提取（误剥回退路径专用）。
func extractJSONWithoutStripping(text string) string {
	return extractJSONFrom(text)
}

// describeResponseShape 返回响应的**形态特征**摘要（2026-11-24 2041 审计轮新增）：
// rune 长度 / 首个可见 rune / 是否 HTML —— 足以判别「返回了 HTML 登录页」这类
// 常见故障形态，又不把会话衍生内容写进日志。全程 rune 安全（多字节不撕裂）。
func describeResponseShape(s string) string {
	runes := []rune(strings.TrimSpace(s))
	first := "-"
	for _, r := range runes {
		if !unicode.IsSpace(r) {
			first = string(r)
			break
		}
	}
	return fmt.Sprintf("runes=%d first=%q html=%v", len(runes), first, strings.Contains(strings.ToLower(s), "<html"))
}

// firstRuneClass 返回首 rune 的**类别**（letter/digit/space/symbol/other），
// 不返回原字符 —— 供错误日志描述响应形态而不落会话衍生内容（隐私面）。
func firstRuneClass(s string) string {
	s = strings.TrimSpace(s)
	for _, r := range s {
		switch {
		case unicode.IsLetter(r):
			return "letter"
		case unicode.IsDigit(r):
			return "digit"
		case unicode.IsSpace(r):
			return "space"
		case unicode.IsPunct(r) || unicode.IsSymbol(r):
			return "symbol"
		default:
			return "other"
		}
	}
	return "empty"
}

// extractedFirstKey 从（可能不完整的）JSON 文本里提取首个顶层键名，
// 只回键名形态，不回值 —— 供错误日志定位解析阶段而不落会话衍生内容。
// 提取不出时返回 "-"。
func extractedFirstKey(jsonStr string) string {
	s := strings.TrimSpace(jsonStr)
	if !strings.HasPrefix(s, "{") {
		return "-"
	}
	// 找第一个 `"key"` 后跟 `:` 的片段；只取 ASCII 可打印键名段。
	quote := strings.IndexByte(s, '"')
	if quote < 0 {
		return "-"
	}
	rest := s[quote+1:]
	end := strings.IndexByte(rest, '"')
	if end < 0 || end > 128 {
		return "-"
	}
	key := rest[:end]
	for _, r := range key {
		if r < 0x20 || r > 0x7E {
			return "-" // 键名含非可打印/非 ASCII 字符 ⇒ 只报形态不报内容
		}
	}
	if !strings.Contains(rest[end+1:], ":") {
		return "-"
	}
	return "len=" + strconv.Itoa(len(key))
}
