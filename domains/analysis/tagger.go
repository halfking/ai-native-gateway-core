// Package analysis — SessionTagger: 会话多维标签生成。
//
// 从 session_summaries 已有字段派生自动标签（task/client/llm/topic/intent/quality），
// 写入 session_tags（tag_source='auto'）。可选 LLM 增强（StageTags）。
//
// 标签维度：
//   - task:     work_types[]（如 code/chat/tool_use）
//   - client:   client_models[]（客户端模型）
//   - llm:      primary_model + models_used[]（实际使用的 LLM）
//   - topic:    key_topics[]（主题）
//   - intent:   user_intent（用户意图）
//   - provider: providers[]（提供商）
//   - quality:  quality_score 映射的等级
package analysis

import (
	"context"
	"fmt"
	"log/slog"
)

// SessionTagger 从 session_summaries 派生标签并写入 session_tags。
type SessionTagger struct {
	db     DB
	config *LLMStageConfig
	logger *slog.Logger
}

// NewSessionTagger 构造 tagger。db/config 为 nil 时为空操作。
func NewSessionTagger(db DB, config *LLMStageConfig, logger *slog.Logger) *SessionTagger {
	if logger == nil {
		logger = slog.Default()
	}
	if config == nil {
		config = NewLLMStageConfig(nil)
	}
	return &SessionTagger{db: db, config: config, logger: logger}
}

// sessionSummaryForTags 是 tagger 需要的 session_summaries 字段投影。
type sessionSummaryForTags struct {
	GwSessionID   string
	TenantID      string
	WorkTypes     []string
	ClientModels  []string
	ModelsUsed    []string
	PrimaryModel  *string
	KeyTopics     []string
	UserIntent    *string
	Providers     []string
	QualityScore  *int
}

// TagSession 为指定会话生成并持久化标签。
// 幂等：已有相同 (gw_session_id, tag_key, tag_value) 的 auto 标签不会重复插入。
func (t *SessionTagger) TagSession(ctx context.Context, tenantID, gwSessionID string) error {
	if t.db == nil {
		return nil
	}
	summary, err := t.loadSummary(ctx, tenantID, gwSessionID)
	if err != nil {
		return fmt.Errorf("tagger: load summary: %w", err)
	}
	if summary == nil {
		return nil
	}
	tags := t.deriveTags(summary)
	if len(tags) == 0 {
		return nil
	}
	return t.saveTags(ctx, tenantID, gwSessionID, tags)
}

// deriveTags 从摘要派生标签（纯规则，无 LLM）。
func (t *SessionTagger) deriveTags(s *sessionSummaryForTags) []tagEntry {
	var tags []tagEntry
	add := func(key, value string) {
		if value == "" {
			return
		}
		tags = append(tags, tagEntry{Key: key, Value: value, Source: "auto", Confidence: 1.0})
	}

	// task
	for _, w := range s.WorkTypes {
		add("task", w)
	}
	// client
	for _, c := range s.ClientModels {
		add("client", c)
	}
	// llm
	if s.PrimaryModel != nil && *s.PrimaryModel != "" {
		add("llm", *s.PrimaryModel)
	}
	for _, m := range s.ModelsUsed {
		if s.PrimaryModel == nil || m != *s.PrimaryModel {
			add("llm_secondary", m)
		}
	}
	// topic
	for _, topic := range s.KeyTopics {
		add("topic", topic)
	}
	// intent
	if s.UserIntent != nil {
		add("intent", *s.UserIntent)
	}
	// provider
	for _, p := range s.Providers {
		add("provider", p)
	}
	// quality 等级
	if s.QualityScore != nil {
		add("quality", qualityLevel(*s.QualityScore))
	}
	return tags
}

// qualityLevel 将 0-10 分映射为等级标签。
func qualityLevel(score int) string {
	switch {
	case score >= 9:
		return "excellent"
	case score >= 7:
		return "good"
	case score >= 5:
		return "fair"
	case score >= 3:
		return "poor"
	default:
		return "bad"
	}
}

type tagEntry struct {
	Key       string
	Value     string
	Source    string
	Confidence float32
}

// loadSummary 读取 session_summaries 的标签相关字段。
func (t *SessionTagger) loadSummary(ctx context.Context, tenantID, gwSessionID string) (*sessionSummaryForTags, error) {
	query := `
		SELECT session_key, tenant_id,
		       COALESCE(work_types, '{}'), COALESCE(client_models, '{}'),
		       COALESCE(models_used, '{}'), primary_model,
		       COALESCE(key_topics, '{}'), user_intent,
		       COALESCE(providers, '{}'), quality_score
		FROM session_summaries
		WHERE session_key = $1`
	args := []any{gwSessionID}
	if tenantID != "" {
		query += " AND tenant_id = $2"
		args = append(args, tenantID)
	}
	var s sessionSummaryForTags
	err := t.db.QueryRow(ctx, query, args...).Scan(
		&s.GwSessionID, &s.TenantID,
		&s.WorkTypes, &s.ClientModels,
		&s.ModelsUsed, &s.PrimaryModel,
		&s.KeyTopics, &s.UserIntent,
		&s.Providers, &s.QualityScore,
	)
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

// saveTags 批量 UPSERT 标签到 session_tags。
func (t *SessionTagger) saveTags(ctx context.Context, tenantID, gwSessionID string, tags []tagEntry) error {
	const upsertSQL = `
		INSERT INTO session_tags (gw_session_id, tenant_id, tag_key, tag_value, tag_source, confidence)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (gw_session_id, tag_key, tag_value) DO NOTHING`
	for _, tag := range tags {
		if _, err := t.db.Exec(ctx, upsertSQL, gwSessionID, tenantID, tag.Key, tag.Value, tag.Source, tag.Confidence); err != nil {
			t.logger.Warn("tagger: failed to save tag",
				"gw_session_id", gwSessionID, "tag_key", tag.Key, "error", err)
		}
	}
	return nil
}
