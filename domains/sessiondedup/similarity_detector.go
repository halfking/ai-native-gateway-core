// Package sessiondedup 实现会话去重和相似度检测
package sessiondedup

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SimilarityDetector 相似度检测器
type SimilarityDetector struct {
	pool      *pgxpool.Pool
	embedding EmbeddingClient
	threshold float64 // 相似度阈值 (0.85)
}

// EmbeddingClient 嵌入向量客户端接口
type EmbeddingClient interface {
	// GetEmbedding 获取文本的嵌入向量
	GetEmbedding(ctx context.Context, text string) ([]float64, error)
}

// NewSimilarityDetector 创建相似度检测器
func NewSimilarityDetector(pool *pgxpool.Pool, embedding EmbeddingClient, threshold float64) *SimilarityDetector {
	if threshold <= 0 || threshold > 1 {
		threshold = 0.85 // 默认阈值
	}

	return &SimilarityDetector{
		pool:      pool,
		embedding: embedding,
		threshold: threshold,
	}
}

// SessionInfo 会话信息
type SessionInfo struct {
	SessionKey  string
	Title       string
	Summary     string
	KeyTopics   []string
	Tags        []string
	TaskID      string
	CreatedAt   string
}

// SimilarSession 相似会话
type SimilarSession struct {
	SessionKey string
	Title      string
	Similarity float64
}

// FindSimilar 查找相似会话
// 返回与给定会话相似的其他会话列表
func (d *SimilarityDetector) FindSimilar(
	ctx context.Context,
	tenantID, sessionKey, summary string,
	taskID *string,
) ([]SimilarSession, error) {
	if d.pool == nil {
		return nil, fmt.Errorf("pool not configured")
	}

	// 如果没有嵌入客户端，降级为基于关键词的简单匹配
	if d.embedding == nil {
		return d.findSimilarByKeywords(ctx, tenantID, sessionKey, summary, taskID)
	}

	// 1. 获取当前会话的嵌入向量
	embedding, err := d.embedding.GetEmbedding(ctx, summary)
	if err != nil {
		// 嵌入失败，降级为关键词匹配
		return d.findSimilarByKeywords(ctx, tenantID, sessionKey, summary, taskID)
	}

	// 2. 查询同一任务下的其他会话
	candidates, err := d.getCandidateSessions(ctx, tenantID, sessionKey, taskID)
	if err != nil {
		return nil, fmt.Errorf("get candidates failed: %w", err)
	}

	// 3. 计算相似度
	similar := []SimilarSession{}
	for _, candidate := range candidates {
		// 获取候选会话的嵌入向量
		candidateEmbedding, err := d.embedding.GetEmbedding(ctx, candidate.Summary)
		if err != nil {
			continue // 跳过嵌入失败的会话
		}

		// 计算余弦相似度
		similarity := cosineSimilarity(embedding, candidateEmbedding)

		// 超过阈值的加入结果
		if similarity >= d.threshold {
			similar = append(similar, SimilarSession{
				SessionKey: candidate.SessionKey,
				Title:      candidate.Title,
				Similarity: similarity,
			})
		}
	}

	// 4. 按相似度降序排序
	sortBySimilarity(similar)

	// 5. 最多返回前5个
	if len(similar) > 5 {
		similar = similar[:5]
	}

	return similar, nil
}

// findSimilarByKeywords 基于关键词的简单相似度匹配（降级方案）
func (d *SimilarityDetector) findSimilarByKeywords(
	ctx context.Context,
	tenantID, sessionKey, summary string,
	taskID *string,
) ([]SimilarSession, error) {
	// 提取关键词
	keywords := extractKeywords(summary)
	if len(keywords) == 0 {
		return []SimilarSession{}, nil
	}

	// 查询包含相同关键词的会话
	query := `
		SELECT session_key, title, summary
		FROM session_summaries
		WHERE tenant_id = $1 
		  AND session_key != $2
	`
	args := []interface{}{tenantID, sessionKey}

	if taskID != nil && *taskID != "" {
		query += " AND task_id = $3"
		args = append(args, *taskID)
	}

	query += " ORDER BY created_at DESC LIMIT 20"

	rows, err := d.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	var similar []SimilarSession
	for rows.Next() {
		var key, title, candidateSummary string
		if err := rows.Scan(&key, &title, &candidateSummary); err != nil {
			continue
		}

		// 计算关键词重叠度
		overlap := calculateKeywordOverlap(keywords, extractKeywords(candidateSummary))
		if overlap >= 0.5 { // 至少50%重叠
			similar = append(similar, SimilarSession{
				SessionKey: key,
				Title:      title,
				Similarity: overlap,
			})
		}
	}

	sortBySimilarity(similar)

	if len(similar) > 5 {
		similar = similar[:5]
	}

	return similar, rows.Err()
}

// getCandidateSessions 获取候选会话列表
func (d *SimilarityDetector) getCandidateSessions(
	ctx context.Context,
	tenantID, sessionKey string,
	taskID *string,
) ([]SessionInfo, error) {
	query := `
		SELECT session_key, title, summary, key_topics, tags, 
		       COALESCE(task_id, '') as task_id, created_at::TEXT
		FROM session_summaries
		WHERE tenant_id = $1 
		  AND session_key != $2
		  AND summary IS NOT NULL 
		  AND summary != ''
	`
	args := []interface{}{tenantID, sessionKey}

	if taskID != nil && *taskID != "" {
		query += " AND task_id = $3"
		args = append(args, *taskID)
	}

	query += " ORDER BY created_at DESC LIMIT 50" // 最多检查最近50个会话

	rows, err := d.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	var sessions []SessionInfo
	for rows.Next() {
		var s SessionInfo
		err := rows.Scan(
			&s.SessionKey, &s.Title, &s.Summary,
			&s.KeyTopics, &s.Tags, &s.TaskID, &s.CreatedAt,
		)
		if err != nil {
			continue
		}
		sessions = append(sessions, s)
	}

	return sessions, rows.Err()
}

// MarkAsDuplicate 标记会话为重复
func (d *SimilarityDetector) MarkAsDuplicate(
	ctx context.Context,
	tenantID, sessionKey, canonicalSessionKey string,
	similarKeys []string,
) error {
	if d.pool == nil {
		return fmt.Errorf("pool not configured")
	}

	query := `
		UPDATE session_summaries
		SET is_duplicate = TRUE,
		    canonical_session_key = $3,
		    similar_session_keys = $4
		WHERE tenant_id = $1 AND session_key = $2
	`

	_, err := d.pool.Exec(ctx, query,
		tenantID, sessionKey, canonicalSessionKey, similarKeys,
	)

	if err != nil {
		return fmt.Errorf("mark duplicate failed: %w", err)
	}

	return nil
}

// UpdateSimilarSessions 更新会话的相似会话列表
func (d *SimilarityDetector) UpdateSimilarSessions(
	ctx context.Context,
	tenantID, sessionKey string,
	similarKeys []string,
) error {
	if d.pool == nil {
		return fmt.Errorf("pool not configured")
	}

	query := `
		UPDATE session_summaries
		SET similar_session_keys = $3
		WHERE tenant_id = $1 AND session_key = $2
	`

	_, err := d.pool.Exec(ctx, query, tenantID, sessionKey, similarKeys)
	if err != nil {
		return fmt.Errorf("update similar sessions failed: %w", err)
	}

	return nil
}

// cosineSimilarity 计算余弦相似度
func cosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) {
		return 0
	}

	var dotProduct, normA, normB float64
	for i := range a {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
}

// extractKeywords 提取关键词（简单实现）
func extractKeywords(text string) []string {
	// 转小写
	text = strings.ToLower(text)

	// 分词（简单按空格和标点分割）
	words := strings.FieldsFunc(text, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_')
	})

	// 去重并过滤短词
	seen := make(map[string]bool)
	keywords := []string{}
	for _, w := range words {
		if len(w) >= 3 && !seen[w] {
			seen[w] = true
			keywords = append(keywords, w)
		}
	}

	return keywords
}

// calculateKeywordOverlap 计算关键词重叠度
func calculateKeywordOverlap(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}

	setA := make(map[string]bool)
	for _, word := range a {
		setA[word] = true
	}

	overlap := 0
	for _, word := range b {
		if setA[word] {
			overlap++
		}
	}

	// 使用 Jaccard 相似度: |A ∩ B| / |A ∪ B|
	union := len(a) + len(b) - overlap
	if union == 0 {
		return 0
	}

	return float64(overlap) / float64(union)
}

// sortBySimilarity 按相似度降序排序
func sortBySimilarity(sessions []SimilarSession) {
	for i := 0; i < len(sessions); i++ {
		for j := i + 1; j < len(sessions); j++ {
			if sessions[i].Similarity < sessions[j].Similarity {
				sessions[i], sessions[j] = sessions[j], sessions[i]
			}
		}
	}
}
