// Package analysis — SessionClusterer: 相似会话聚类（混合模式）。
//
// 两阶段：
//  1. 粗聚类（规则）：按 (user_intent, primary_model, key_topics[0]) 分组
//     → coarse_key
//  2. 细聚类（向量）：组内对 summary 做 embedding，层次聚类
//     → 仅当 pgvector 可用且有 EmbeddingClient 时执行
//
// 降级策略：cluster_mode=rule 或 pgvector 不可用 → 仅粗聚类。
package analysis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
)

// EmbeddingClient 生成文本向量（对接外部 embedding 模型）。
type EmbeddingClient interface {
	Embed(ctx context.Context, model, text string) ([]float32, error)
}

// BatchEmbeddingClient 可在一次请求中生成多个文本向量。
type BatchEmbeddingClient interface {
	EmbedBatch(ctx context.Context, model string, texts []string) ([][]float32, error)
}

// SessionClusterer 混合聚类器。
type SessionClusterer struct {
	db          DB
	config      *LLMStageConfig
	embedClient EmbeddingClient // 可选；nil 时仅粗聚类
	logger      *slog.Logger
}

// NewSessionClusterer 构造聚类器。
func NewSessionClusterer(db DB, config *LLMStageConfig, embed EmbeddingClient, logger *slog.Logger) *SessionClusterer {
	if logger == nil {
		logger = slog.Default()
	}
	if config == nil {
		config = NewLLMStageConfig(nil)
	}
	return &SessionClusterer{db: db, config: config, embedClient: embed, logger: logger}
}

// sessionForCluster 是聚类所需的会话投影。
type sessionForCluster struct {
	GwSessionID  string
	TenantID     string
	Summary      *string
	UserIntent   *string
	PrimaryModel *string
	KeyTopics    []string
	TotalCostUSD float64
	QualityScore *int
}

// ClusterTenant 对某租户的所有近期会话执行聚类。
// 触发方式：定时（cluster_schedule）或手动。
func (c *SessionClusterer) ClusterTenant(ctx context.Context, tenantID string, lookbackHours int) (int, error) {
	if c.db == nil {
		return 0, nil
	}
	mode := c.config.ClusterMode()
	if mode == "off" {
		return 0, nil
	}
	if lookbackHours <= 0 {
		lookbackHours = 168 // 默认 7 天
	}

	sessions, err := c.loadSessions(ctx, tenantID, lookbackHours)
	if err != nil {
		return 0, fmt.Errorf("clusterer: load sessions: %w", err)
	}
	if len(sessions) < 2 {
		return 0, nil
	}

	// 阶段 1：粗聚类（规则）
	groups := c.coarseGroup(sessions)

	clusterCount := 0
	for coarseKey, members := range groups {
		if len(members) == 0 {
			continue
		}
		// 阶段 2：细聚类（向量）—— 仅当启用且 embedding 可用
		subClusters := [][]sessionForCluster{members}
		if (mode == "vector" || mode == "hybrid") && c.embedClient != nil {
			subClusters = c.vectorCluster(ctx, members)
		}
		for _, sub := range subClusters {
			if len(sub) == 0 {
				continue
			}
			if err := c.persistCluster(ctx, coarseKey, sub); err != nil {
				c.logger.Warn("clusterer: persist failed", "coarse_key", coarseKey, "error", err)
				continue
			}
			clusterCount++
		}
	}
	return clusterCount, nil
}

// coarseGroup 按维度分组。
func (c *SessionClusterer) coarseGroup(sessions []sessionForCluster) map[string][]sessionForCluster {
	groups := make(map[string][]sessionForCluster)
	for _, s := range sessions {
		groups[c.coarseKey(s)] = append(groups[c.coarseKey(s)], s)
	}
	return groups
}

// coarseKey 生成粗聚类键。
func (c *SessionClusterer) coarseKey(s sessionForCluster) string {
	intent := ptrStr(s.UserIntent, "unknown")
	model := ptrStr(s.PrimaryModel, "unknown")
	topic := "none"
	if len(s.KeyTopics) > 0 {
		topic = s.KeyTopics[0]
	}
	return strings.Join([]string{intent, model, topic}, "|")
}

// vectorCluster 组内向量细分。
// 实现：对每个会话的 summary 生成 embedding，按余弦相似度做层次合并。
// （简化版：相似度 > 0.85 归为同簇；生产可换 HDBSCAN）
func (c *SessionClusterer) vectorCluster(ctx context.Context, members []sessionForCluster) [][]sessionForCluster {
	if len(members) <= 1 {
		return [][]sessionForCluster{members}
	}
	model := c.config.ModelFor(StageEmbedding)
	if !c.embeddingStorageAvailable(ctx) {
		return [][]sessionForCluster{members}
	}

	embeds := make([][]float32, len(members))
	missingIndexes := make([]int, 0, len(members))
	missingTexts := make([]string, 0, len(members))
	for index, session := range members {
		text := ptrStr(session.Summary, session.GwSessionID)
		vector, found := c.loadCachedEmbedding(ctx, session, text, model)
		if found {
			embeds[index] = vector
			continue
		}
		missingIndexes = append(missingIndexes, index)
		missingTexts = append(missingTexts, text)
	}

	if len(missingTexts) > 0 {
		vectors, err := c.embedMissing(ctx, model, missingTexts)
		if err != nil {
			c.logger.Debug("clusterer: batch embed failed, using rule clusters", "error", err)
			return [][]sessionForCluster{members}
		}
		for index, vector := range vectors {
			if len(vector) == 0 {
				continue
			}
			memberIndex := missingIndexes[index]
			embeds[memberIndex] = vector
			_ = c.saveEmbedding(ctx, members[memberIndex], vector, model)
		}
	}

	const threshold = 0.85
	assigned := make([]int, len(members))
	for i := range assigned {
		assigned[i] = -1
	}
	clusterID := 0
	for i := 0; i < len(members); i++ {
		if assigned[i] != -1 {
			continue
		}
		assigned[i] = clusterID
		if len(embeds[i]) > 0 {
			for j := i + 1; j < len(members); j++ {
				if assigned[j] == -1 && len(embeds[j]) > 0 && cosineSim(embeds[i], embeds[j]) >= threshold {
					assigned[j] = clusterID
				}
			}
		}
		clusterID++
	}

	result := make([][]sessionForCluster, clusterID)
	for i, cid := range assigned {
		result[cid] = append(result[cid], members[i])
	}
	return result
}

func (c *SessionClusterer) embeddingStorageAvailable(ctx context.Context) bool {
	var available bool
	err := c.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.columns
			WHERE table_schema = current_schema()
			  AND table_name = 'session_embeddings'
			  AND column_name = 'embedding_v2'
		)`).Scan(&available)
	return err == nil && available
}

func (c *SessionClusterer) embedMissing(ctx context.Context, model string, texts []string) ([][]float32, error) {
	batchClient, ok := c.embedClient.(BatchEmbeddingClient)
	if !ok {
		return nil, fmt.Errorf("batch embedding unavailable")
	}
	vectors, err := batchClient.EmbedBatch(ctx, model, texts)
	if err != nil {
		return nil, err
	}
	if len(vectors) != len(texts) {
		return nil, fmt.Errorf("embedding count %d does not match input count %d", len(vectors), len(texts))
	}
	return vectors, nil
}

func (c *SessionClusterer) loadCachedEmbedding(ctx context.Context, session sessionForCluster, text, model string) ([]float32, bool) {
	var vectorText string
	err := c.db.QueryRow(ctx, `
		SELECT embedding_v2::text
		FROM session_embeddings
		WHERE gw_session_id=$1 AND tenant_id=$2 AND content_hash=$3 AND model=$4`,
		session.GwSessionID, session.TenantID, hashContent(text), model).Scan(&vectorText)
	if err != nil {
		return nil, false
	}
	vector, err := parseVectorText(vectorText)
	return vector, err == nil && len(vector) > 0
}

// persistCluster 持久化一个聚类及其成员。
func (c *SessionClusterer) persistCluster(ctx context.Context, coarseKey string, members []sessionForCluster) error {
	clusterID := clusterIDOf(coarseKey, members)
	tenantID := members[0].TenantID

	// 聚类级聚合
	var totalCost float64
	var topics []string
	var qualities []int
	var centroidSum string
	for i, m := range members {
		totalCost += m.TotalCostUSD
		topics = appendUnique(topics, m.KeyTopics)
		if m.QualityScore != nil {
			qualities = append(qualities, *m.QualityScore)
		}
		if i == 0 {
			centroidSum = ptrStr(m.Summary, "")
		}
	}
	avgCost := totalCost / float64(len(members))
	avgQuality := avgInt(qualities)

	label := clusterLabel(coarseKey, topics)

	// UPSERT cluster
	if _, err := c.db.Exec(ctx, `
		INSERT INTO session_clusters
			(cluster_id, tenant_id, coarse_key, label, topic_path, centroid_summary,
			 member_count, avg_cost_usd, avg_quality_score, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,NOW())
		ON CONFLICT (cluster_id) DO UPDATE SET
			label = EXCLUDED.label,
			topic_path = EXCLUDED.topic_path,
			member_count = EXCLUDED.member_count,
			avg_cost_usd = EXCLUDED.avg_cost_usd,
			avg_quality_score = EXCLUDED.avg_quality_score,
			updated_at = NOW()`,
		clusterID, tenantID, coarseKey, label, topics, centroidSum,
		len(members), avgCost, avgQuality); err != nil {
		return err
	}

	// 清理旧成员后重写
	if _, err := c.db.Exec(ctx, `DELETE FROM session_cluster_members WHERE cluster_id=$1`, clusterID); err != nil {
		return err
	}
	for _, m := range members {
		if _, err := c.db.Exec(ctx, `
			INSERT INTO session_cluster_members (cluster_id, gw_session_id, tenant_id, score)
			VALUES ($1,$2,$3,$4)
			ON CONFLICT (cluster_id, gw_session_id) DO UPDATE SET score = EXCLUDED.score`,
			clusterID, m.GwSessionID, tenantID, 1.0); err != nil {
			return err
		}
	}
	return nil
}

// saveEmbedding 持久化会话向量。
func (c *SessionClusterer) saveEmbedding(ctx context.Context, s sessionForCluster, vec []float32, model string) error {
	hash := hashContent(ptrStr(s.Summary, s.GwSessionID))
	// embedding_v2 列是 vector(1024)；pgvector 不可用时聚类会提前降级
	_, err := c.db.Exec(ctx, `
		INSERT INTO session_embeddings (gw_session_id, tenant_id, embedding_v2, content_hash, model)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (gw_session_id) DO UPDATE SET
			embedding_v2 = EXCLUDED.embedding_v2,
			content_hash = EXCLUDED.content_hash,
			model = EXCLUDED.model,
			generated_at = NOW()`,
		s.GwSessionID, s.TenantID, vecToText(vec), hash, model)
	return err
}

// loadSessions 读取近期会话。
func (c *SessionClusterer) loadSessions(ctx context.Context, tenantID string, lookbackHours int) ([]sessionForCluster, error) {
	query := `
		SELECT session_key, tenant_id, summary, user_intent, primary_model,
		       COALESCE(key_topics, '{}'), total_cost_usd, quality_score
		FROM session_summaries
		WHERE last_request_at >= NOW() - ($1::TEXT)::INTERVAL`
	args := []any{fmt.Sprintf("%d hours", lookbackHours)}
	if tenantID != "" {
		query += " AND tenant_id = $2"
		args = append(args, tenantID)
	}
	rows, err := c.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []sessionForCluster
	for rows.Next() {
		var s sessionForCluster
		if err := rows.Scan(&s.GwSessionID, &s.TenantID, &s.Summary, &s.UserIntent,
			&s.PrimaryModel, &s.KeyTopics, &s.TotalCostUSD, &s.QualityScore); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ── helpers ───────────────────────────────────────────────────────────

func ptrStr(p *string, def string) string {
	if p == nil || *p == "" {
		return def
	}
	return *p
}

func cosineSim(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (sqrt(na) * sqrt(nb))
}

func sqrt(x float64) float64 {
	if x <= 0 {
		return 0
	}
	z := x
	for i := 0; i < 32; i++ {
		z = z - (z*z-x)/(2*z)
	}
	return z
}

func appendUnique(dst, src []string) []string {
	seen := map[string]bool{}
	for _, s := range dst {
		seen[s] = true
	}
	for _, s := range src {
		if !seen[s] {
			dst = append(dst, s)
			seen[s] = true
		}
	}
	// 保持稳定排序便于可复现的 clusterID
	sort.Strings(dst)
	return dst
}

func avgInt(xs []int) *int {
	if len(xs) == 0 {
		return nil
	}
	sum := 0
	for _, x := range xs {
		sum += x
	}
	avg := sum / len(xs)
	return &avg
}

func clusterIDOf(coarseKey string, members []sessionForCluster) string {
	ids := make([]string, len(members))
	for i, m := range members {
		ids[i] = m.GwSessionID
	}
	sort.Strings(ids)
	h := sha256.Sum256([]byte(coarseKey + "|" + strings.Join(ids, ",")))
	return "cl_" + hex.EncodeToString(h[:8])
}

func clusterLabel(coarseKey string, topics []string) string {
	if len(topics) > 0 {
		return strings.Join(topics[:min(3, len(topics))], " / ")
	}
	return coarseKey
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func hashContent(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// vecToText 把 []float32 转为 pgvector 文本格式 "[0.1,0.2,...]"。
func vecToText(vec []float32) string {
	var sb strings.Builder
	sb.WriteByte('[')
	for i, v := range vec {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, "%f", v)
	}
	sb.WriteByte(']')
	return sb.String()
}

func parseVectorText(value string) ([]float32, error) {
	value = strings.TrimSpace(value)
	if len(value) < 2 || value[0] != '[' || value[len(value)-1] != ']' {
		return nil, fmt.Errorf("invalid vector format")
	}
	value = strings.TrimSpace(value[1 : len(value)-1])
	if value == "" {
		return nil, nil
	}
	parts := strings.Split(value, ",")
	vector := make([]float32, len(parts))
	for index, part := range parts {
		number, err := strconv.ParseFloat(strings.TrimSpace(part), 32)
		if err != nil {
			return nil, err
		}
		vector[index] = float32(number)
	}
	return vector, nil
}
