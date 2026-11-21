// Package layeredsummary 实现会话的分层总结功能
// 支持三层架构: window (滑动窗口) / segment (段落) / meta (元总结)
package layeredsummary

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// LayerType 定义总结层级类型
type LayerType string

const (
	LayerTypeWindow  LayerType = "window"  // 滑动��口 (10轮)
	LayerTypeSegment LayerType = "segment" // 段落 (50轮)
	LayerTypeMeta    LayerType = "meta"    // 元总结 (会话级)
)

// SummaryLayer 表示一层总结
type SummaryLayer struct {
	ID              int64
	TenantID        string
	GwSessionID     string
	LayerType       LayerType
	LayerSequence   int
	FromTurn        *int
	ToTurn          *int
	FromRequestID   *string
	ToRequestID     *string
	Summary         string
	KeyPoints       []string
	ParentLayerIDs  []int64
	TurnCount       *int
	MessageCount    *int
	TokenCount      *int
	CreatedAt       time.Time
	ContentHash     string
}

// LayeredSummarizer 分层总结器
type LayeredSummarizer struct {
	pool *pgxpool.Pool
	llm  LLMClient

	// 配置
	windowSize   int // 窗口大小 (轮数)
	segmentSize  int // 段落大小 (轮数)
	maxWindows   int // 最多保留窗口数
	overlapSize  int // 窗口重叠大小
}

// LLMClient 定义 LLM 调用接口
type LLMClient interface {
	Complete(ctx context.Context, prompt string, opts ...interface{}) (string, error)
}

// NewLayeredSummarizer 创建分层总结器
func NewLayeredSummarizer(pool *pgxpool.Pool, llm LLMClient) *LayeredSummarizer {
	return &LayeredSummarizer{
		pool:         pool,
		llm:          llm,
		windowSize:   10,
		segmentSize:  50,
		maxWindows:   5,
		overlapSize:  3,
	}
}

// SetConfig 设置配置 (用于测试)
func (s *LayeredSummarizer) SetConfig(windowSize, segmentSize, maxWindows, overlapSize int) {
	s.windowSize = windowSize
	s.segmentSize = segmentSize
	s.maxWindows = maxWindows
	s.overlapSize = overlapSize
}

// GenerateWindowSummary 生成滑动窗口总结
// 输入: 最近 windowSize 轮的消息 + 上一个窗口的摘要
// 输出: 当前窗口的摘要
func (s *LayeredSummarizer) GenerateWindowSummary(
	ctx context.Context,
	tenantID, sessionID string,
	messages []Message,
	prevSummary string,
) (*SummaryLayer, error) {
	if len(messages) == 0 {
		return nil, fmt.Errorf("no messages to summarize")
	}

	// 构建 prompt
	prompt := s.buildWindowPrompt(messages, prevSummary)

	// 调用 LLM
	summary, err := s.llm.Complete(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("llm complete failed: %w", err)
	}

	// 计算序号 (查询已有窗口数)
	sequence, err := s.getNextSequence(ctx, tenantID, sessionID, LayerTypeWindow)
	if err != nil {
		return nil, fmt.Errorf("get next sequence failed: %w", err)
	}

	// 构建 layer 对象
	layer := &SummaryLayer{
		TenantID:      tenantID,
		GwSessionID:   sessionID,
		LayerType:     LayerTypeWindow,
		LayerSequence: sequence,
		Summary:       summary,
		KeyPoints:     extractKeyPoints(summary),
		ContentHash:   computeHash(summary),
		CreatedAt:     time.Now(),
	}

	// 设置范围
	if len(messages) > 0 {
		firstTurn := messages[0].TurnNumber
		lastTurn := messages[len(messages)-1].TurnNumber
		layer.FromTurn = &firstTurn
		layer.ToTurn = &lastTurn
		layer.FromRequestID = &messages[0].RequestID
		layer.ToRequestID = &messages[len(messages)-1].RequestID
		turnCount := len(messages)
		layer.TurnCount = &turnCount
	}

	// 保存到数据库
	if err := s.saveLayer(ctx, layer); err != nil {
		return nil, fmt.Errorf("save layer failed: %w", err)
	}

	// 清理旧窗口 (保留最近 maxWindows 个)
	if err := s.cleanupOldWindows(ctx, tenantID, sessionID); err != nil {
		// 清理失败不影响主流程
		fmt.Printf("warn: cleanup old windows failed: %v\n", err)
	}

	return layer, nil
}

// GenerateSegmentSummary 生成段落总结
// 输入: 最近 maxWindows 个窗口的摘要
// 输出: 段落摘要
func (s *LayeredSummarizer) GenerateSegmentSummary(
	ctx context.Context,
	tenantID, sessionID string,
	windowLayers []SummaryLayer,
) (*SummaryLayer, error) {
	if len(windowLayers) == 0 {
		return nil, fmt.Errorf("no window layers to summarize")
	}

	// 构建 prompt
	prompt := s.buildSegmentPrompt(windowLayers)

	// 调用 LLM
	summary, err := s.llm.Complete(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("llm complete failed: %w", err)
	}

	// 计算序号
	sequence, err := s.getNextSequence(ctx, tenantID, sessionID, LayerTypeSegment)
	if err != nil {
		return nil, fmt.Errorf("get next sequence failed: %w", err)
	}

	// 提取父层级 ID
	parentIDs := make([]int64, len(windowLayers))
	for i, w := range windowLayers {
		parentIDs[i] = w.ID
	}

	// 构建 layer 对象
	layer := &SummaryLayer{
		TenantID:       tenantID,
		GwSessionID:    sessionID,
		LayerType:      LayerTypeSegment,
		LayerSequence:  sequence,
		Summary:        summary,
		KeyPoints:      extractKeyPoints(summary),
		ParentLayerIDs: parentIDs,
		ContentHash:    computeHash(summary),
		CreatedAt:      time.Now(),
	}

	// 设置范围 (从第一个窗口到最后一个窗口)
	if len(windowLayers) > 0 {
		layer.FromTurn = windowLayers[0].FromTurn
		layer.ToTurn = windowLayers[len(windowLayers)-1].ToTurn
		layer.FromRequestID = windowLayers[0].FromRequestID
		layer.ToRequestID = windowLayers[len(windowLayers)-1].ToRequestID
	}

	// 保存到数据库
	if err := s.saveLayer(ctx, layer); err != nil {
		return nil, fmt.Errorf("save layer failed: %w", err)
	}

	return layer, nil
}

// GenerateMetaSummary 生成元总结 (会话关闭时)
// 输入: 所有段落摘要
// 输出: 元摘要 (存入 session_summaries.summary)
func (s *LayeredSummarizer) GenerateMetaSummary(
	ctx context.Context,
	tenantID, sessionID string,
	segmentLayers []SummaryLayer,
) (*SummaryLayer, error) {
	if len(segmentLayers) == 0 {
		return nil, fmt.Errorf("no segment layers to summarize")
	}

	// 构建 prompt
	prompt := s.buildMetaPrompt(segmentLayers)

	// 调用 LLM
	summary, err := s.llm.Complete(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("llm complete failed: %w", err)
	}

	// 提取父层级 ID
	parentIDs := make([]int64, len(segmentLayers))
	for i, seg := range segmentLayers {
		parentIDs[i] = seg.ID
	}

	// 构建 layer 对象
	layer := &SummaryLayer{
		TenantID:       tenantID,
		GwSessionID:    sessionID,
		LayerType:      LayerTypeMeta,
		LayerSequence:  0, // 元总结通常只有一条
		Summary:        summary,
		KeyPoints:      extractKeyPoints(summary),
		ParentLayerIDs: parentIDs,
		ContentHash:    computeHash(summary),
		CreatedAt:      time.Now(),
	}

	// 设置范围 (整个会话)
	if len(segmentLayers) > 0 {
		layer.FromTurn = segmentLayers[0].FromTurn
		layer.ToTurn = segmentLayers[len(segmentLayers)-1].ToTurn
		layer.FromRequestID = segmentLayers[0].FromRequestID
		layer.ToRequestID = segmentLayers[len(segmentLayers)-1].ToRequestID
	}

	// 保存到数据库
	if err := s.saveLayer(ctx, layer); err != nil {
		return nil, fmt.Errorf("save layer failed: %w", err)
	}

	return layer, nil
}

// Message 表示一条消息
type Message struct {
	RequestID  string
	TurnNumber int
	Role       string
	Content    string
	Timestamp  time.Time
}

// buildWindowPrompt 构建窗口总结 prompt
func (s *LayeredSummarizer) buildWindowPrompt(messages []Message, prevSummary string) string {
	prompt := "请基于以下对话生成简洁的窗口摘要（50-100字）：\n\n"

	if prevSummary != "" {
		prompt += "【上一窗口摘要】\n" + prevSummary + "\n\n"
	}

	prompt += "【本窗口对话】\n"
	for i, msg := range messages {
		role := "用户"
		if msg.Role == "assistant" {
			role = "助手"
		}
		content := msg.Content
		if len(content) > 200 {
			content = content[:200] + "..."
		}
		prompt += fmt.Sprintf("[%d] %s: %s\n", i+1, role, content)
	}

	prompt += "\n请生成一个简洁的摘要，突出本窗口的核心内容和进展。"
	return prompt
}

// buildSegmentPrompt 构建段落总结 prompt
func (s *LayeredSummarizer) buildSegmentPrompt(windowLayers []SummaryLayer) string {
	prompt := "请基于以下窗口摘要生成段落总结（100-200字）：\n\n"

	for i, w := range windowLayers {
		prompt += fmt.Sprintf("[窗口 %d] %s\n", i+1, w.Summary)
	}

	prompt += "\n请生成一个段落总结，概括这一阶段的主要活动、进展和成果。"
	return prompt
}

// buildMetaPrompt 构建元总结 prompt
func (s *LayeredSummarizer) buildMetaPrompt(segmentLayers []SummaryLayer) string {
	prompt := "请基于以下段落总结生成会话的元总结（200-300字）：\n\n"

	for i, seg := range segmentLayers {
		prompt += fmt.Sprintf("[阶段 %d] %s\n", i+1, seg.Summary)
	}

	prompt += "\n请生成一个元总结，包括：\n"
	prompt += "1. 会话的整体目标和演进路径\n"
	prompt += "2. 各阶段的主要成果\n"
	prompt += "3. 遇到的关键问题和解决方案\n"
	prompt += "4. 最终达成的结果"
	return prompt
}

// extractKeyPoints 从摘要中提取关键要点 (简单实现)
func extractKeyPoints(summary string) []string {
	// TODO: 实现更智能的关键点提取
	// 当前简单返回空数组，后续可以用 NLP 或 LLM 提取
	return []string{}
}

// computeHash 计算内容哈希
func computeHash(content string) string {
	h := sha256.New()
	h.Write([]byte(content))
	return hex.EncodeToString(h.Sum(nil))
}

// getNextSequence 获取下一个序号
func (s *LayeredSummarizer) getNextSequence(
	ctx context.Context,
	tenantID, sessionID string,
	layerType LayerType,
) (int, error) {
	if s.pool == nil {
		return 0, fmt.Errorf("pool not configured")
	}

	query := `
		SELECT COALESCE(MAX(layer_sequence), -1) + 1
		FROM session_summary_layers
		WHERE tenant_id = $1 AND gw_session_id = $2 AND layer_type = $3
	`

	var seq int
	err := s.pool.QueryRow(ctx, query, tenantID, sessionID, string(layerType)).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("query sequence failed: %w", err)
	}

	return seq, nil
}

// saveLayer 保存层级总结到数据库
func (s *LayeredSummarizer) saveLayer(ctx context.Context, layer *SummaryLayer) error {
	if s.pool == nil {
		return fmt.Errorf("pool not configured")
	}

	query := `
		INSERT INTO session_summary_layers (
			tenant_id, gw_session_id, layer_type, layer_sequence,
			from_turn, to_turn, from_request_id, to_request_id,
			summary, key_points, parent_layer_ids,
			turn_count, message_count, token_count,
			content_hash, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		ON CONFLICT (tenant_id, gw_session_id, layer_type, layer_sequence)
		DO UPDATE SET
			summary = EXCLUDED.summary,
			key_points = EXCLUDED.key_points,
			content_hash = EXCLUDED.content_hash
		RETURNING id
	`

	err := s.pool.QueryRow(ctx, query,
		layer.TenantID, layer.GwSessionID, string(layer.LayerType), layer.LayerSequence,
		layer.FromTurn, layer.ToTurn, layer.FromRequestID, layer.ToRequestID,
		layer.Summary, layer.KeyPoints, layer.ParentLayerIDs,
		layer.TurnCount, layer.MessageCount, layer.TokenCount,
		layer.ContentHash, layer.CreatedAt,
	).Scan(&layer.ID)

	if err != nil {
		return fmt.Errorf("insert layer failed: %w", err)
	}

	return nil
}

// cleanupOldWindows 清理旧窗口 (保留最近 maxWindows 个)
func (s *LayeredSummarizer) cleanupOldWindows(
	ctx context.Context,
	tenantID, sessionID string,
) error {
	if s.pool == nil {
		return fmt.Errorf("pool not configured")
	}

	query := `
		DELETE FROM session_summary_layers
		WHERE tenant_id = $1 AND gw_session_id = $2 AND layer_type = 'window'
		AND layer_sequence < (
			SELECT MAX(layer_sequence) - $3
			FROM session_summary_layers
			WHERE tenant_id = $1 AND gw_session_id = $2 AND layer_type = 'window'
		)
	`

	_, err := s.pool.Exec(ctx, query, tenantID, sessionID, s.maxWindows-1)
	if err != nil {
		return fmt.Errorf("cleanup windows failed: %w", err)
	}

	return nil
}

// GetLayers 查询指定会话的所有层级
func (s *LayeredSummarizer) GetLayers(
	ctx context.Context,
	tenantID, sessionID string,
	layerType *LayerType,
) ([]SummaryLayer, error) {
	if s.pool == nil {
		return nil, fmt.Errorf("pool not configured")
	}

	query := `
		SELECT id, tenant_id, gw_session_id, layer_type, layer_sequence,
			   from_turn, to_turn, from_request_id, to_request_id,
			   summary, key_points, parent_layer_ids,
			   turn_count, message_count, token_count,
			   created_at, content_hash
		FROM session_summary_layers
		WHERE tenant_id = $1 AND gw_session_id = $2
	`
	args := []interface{}{tenantID, sessionID}

	if layerType != nil {
		query += " AND layer_type = $3"
		args = append(args, string(*layerType))
	}

	query += " ORDER BY layer_sequence ASC"

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query layers failed: %w", err)
	}
	defer rows.Close()

	var layers []SummaryLayer
	for rows.Next() {
		var layer SummaryLayer
		err := rows.Scan(
			&layer.ID, &layer.TenantID, &layer.GwSessionID,
			&layer.LayerType, &layer.LayerSequence,
			&layer.FromTurn, &layer.ToTurn,
			&layer.FromRequestID, &layer.ToRequestID,
			&layer.Summary, &layer.KeyPoints, &layer.ParentLayerIDs,
			&layer.TurnCount, &layer.MessageCount, &layer.TokenCount,
			&layer.CreatedAt, &layer.ContentHash,
		)
		if err != nil {
			return nil, fmt.Errorf("scan layer failed: %w", err)
		}
		layers = append(layers, layer)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration failed: %w", err)
	}

	return layers, nil
}
