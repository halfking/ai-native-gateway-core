package intentconfig

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

// EvolutionStore 意图演化存储接口
type EvolutionStore interface {
	// Save 保存意图演化记录
	Save(ctx context.Context, evo *IntentEvolution) error

	// GetSessionHistory 获取会话历史意图（最近N轮）
	GetSessionHistory(ctx context.Context, sessionID string, limit int) ([]IntentEvolution, error)

	// GetByRequestID 根据请求ID查询
	GetByRequestID(ctx context.Context, requestID string) (*IntentEvolution, error)
}

// PGEvolutionStore PostgreSQL 实现
type PGEvolutionStore struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// NewPGEvolutionStore 创建存储实例
func NewPGEvolutionStore(pool *pgxpool.Pool, logger *slog.Logger) *PGEvolutionStore {
	if logger == nil {
		logger = slog.Default()
	}
	return &PGEvolutionStore{
		pool:   pool,
		logger: logger,
	}
}

// Save 保存意图演化记录
func (s *PGEvolutionStore) Save(ctx context.Context, evo *IntentEvolution) error {
	// 序列化候选意图
	candidatesJSON, err := json.Marshal(evo.IntentCandidates)
	if err != nil {
		return fmt.Errorf("marshal intent_candidates: %w", err)
	}

	// 计算内容哈希（如果提供了用户内容）
	var contentHash *string
	if evo.UserContent != nil && *evo.UserContent != "" {
		hash := sha256.Sum256([]byte(*evo.UserContent))
		hashStr := hex.EncodeToString(hash[:])
		contentHash = &hashStr
	}

	query := `
		INSERT INTO session_intent_evolution (
			session_id, tenant_id, request_id, turn_number,
			intent_candidates, primary_intent, primary_confidence,
			previous_primary_intent, intent_drift_score, is_intent_changed,
			classifier_version, classification_latency_ms,
			user_content, user_content_hash, context_length, has_images, tool_count,
			classified_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18
		)
		ON CONFLICT (session_id, turn_number) DO UPDATE SET
			intent_candidates = EXCLUDED.intent_candidates,
			primary_intent = EXCLUDED.primary_intent,
			primary_confidence = EXCLUDED.primary_confidence,
			previous_primary_intent = EXCLUDED.previous_primary_intent,
			intent_drift_score = EXCLUDED.intent_drift_score,
			is_intent_changed = EXCLUDED.is_intent_changed,
			classifier_version = EXCLUDED.classifier_version,
			classification_latency_ms = EXCLUDED.classification_latency_ms,
			user_content = EXCLUDED.user_content,
			user_content_hash = EXCLUDED.user_content_hash,
			context_length = EXCLUDED.context_length,
			has_images = EXCLUDED.has_images,
			tool_count = EXCLUDED.tool_count,
			classified_at = EXCLUDED.classified_at
		RETURNING id
	`

	err = s.pool.QueryRow(ctx, query,
		evo.SessionID, evo.TenantID, evo.RequestID, evo.TurnNumber,
		candidatesJSON, evo.PrimaryIntent, evo.PrimaryConfidence,
		evo.PreviousPrimaryIntent, evo.IntentDriftScore, evo.IsIntentChanged,
		evo.ClassifierVersion, evo.ClassificationLatencyMs,
		evo.UserContent, contentHash, evo.ContextLength, evo.HasImages, evo.ToolCount,
		evo.ClassifiedAt,
	).Scan(&evo.ID)

	if err != nil {
		return fmt.Errorf("insert evolution: %w", err)
	}

	s.logger.Debug("intentconfig: evolution saved",
		"id", evo.ID,
		"session_id", evo.SessionID,
		"turn", evo.TurnNumber,
		"intent", evo.PrimaryIntent,
		"confidence", evo.PrimaryConfidence,
		"drift", evo.IntentDriftScore)

	return nil
}

// GetSessionHistory 获取会话历史意图（最近N轮，按轮次倒序）
func (s *PGEvolutionStore) GetSessionHistory(ctx context.Context, sessionID string, limit int) ([]IntentEvolution, error) {
	if limit <= 0 {
		limit = 5 // 默认5轮
	}

	query := `
		SELECT 
			id, session_id, tenant_id, request_id, turn_number,
			intent_candidates, primary_intent, primary_confidence,
			previous_primary_intent, intent_drift_score, is_intent_changed,
			classifier_version, classification_latency_ms,
			user_content, user_content_hash, context_length, has_images, tool_count,
			classified_at
		FROM session_intent_evolution
		WHERE session_id = $1
		ORDER BY turn_number DESC
		LIMIT $2
	`

	rows, err := s.pool.Query(ctx, query, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("query history: %w", err)
	}
	defer rows.Close()

	var history []IntentEvolution
	for rows.Next() {
		var evo IntentEvolution
		var candidatesJSON []byte

		err := rows.Scan(
			&evo.ID, &evo.SessionID, &evo.TenantID, &evo.RequestID, &evo.TurnNumber,
			&candidatesJSON, &evo.PrimaryIntent, &evo.PrimaryConfidence,
			&evo.PreviousPrimaryIntent, &evo.IntentDriftScore, &evo.IsIntentChanged,
			&evo.ClassifierVersion, &evo.ClassificationLatencyMs,
			&evo.UserContent, &evo.UserContentHash, &evo.ContextLength, &evo.HasImages, &evo.ToolCount,
			&evo.ClassifiedAt,
		)
		if err != nil {
			s.logger.Warn("intentconfig: scan history failed", "error", err)
			continue
		}

		// 解析候选意图
		if err := json.Unmarshal(candidatesJSON, &evo.IntentCandidates); err != nil {
			s.logger.Warn("intentconfig: parse candidates failed", "error", err)
			continue
		}

		history = append(history, evo)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration: %w", err)
	}

	return history, nil
}

// GetByRequestID 根据请求ID查询
func (s *PGEvolutionStore) GetByRequestID(ctx context.Context, requestID string) (*IntentEvolution, error) {
	query := `
		SELECT 
			id, session_id, tenant_id, request_id, turn_number,
			intent_candidates, primary_intent, primary_confidence,
			previous_primary_intent, intent_drift_score, is_intent_changed,
			classifier_version, classification_latency_ms,
			user_content, user_content_hash, context_length, has_images, tool_count,
			classified_at
		FROM session_intent_evolution
		WHERE request_id = $1
		LIMIT 1
	`

	var evo IntentEvolution
	var candidatesJSON []byte

	err := s.pool.QueryRow(ctx, query, requestID).Scan(
		&evo.ID, &evo.SessionID, &evo.TenantID, &evo.RequestID, &evo.TurnNumber,
		&candidatesJSON, &evo.PrimaryIntent, &evo.PrimaryConfidence,
		&evo.PreviousPrimaryIntent, &evo.IntentDriftScore, &evo.IsIntentChanged,
		&evo.ClassifierVersion, &evo.ClassificationLatencyMs,
		&evo.UserContent, &evo.UserContentHash, &evo.ContextLength, &evo.HasImages, &evo.ToolCount,
		&evo.ClassifiedAt,
	)

	if err != nil {
		return nil, fmt.Errorf("query by request_id: %w", err)
	}

	// 解析候选意图
	if err := json.Unmarshal(candidatesJSON, &evo.IntentCandidates); err != nil {
		return nil, fmt.Errorf("parse candidates: %w", err)
	}

	return &evo, nil
}

// FeedbackStore 反馈存储接口
type FeedbackStore interface {
	// Save 保存反馈记录
	Save(ctx context.Context, fb *Feedback) error

	// GetRecentFeedback 获取最近的反馈（用于效果评估）
	GetRecentFeedback(ctx context.Context, tenantID string, limit int) ([]Feedback, error)

	// UpdateAnnotation 更新人工标注
	UpdateAnnotation(ctx context.Context, requestID string, actualIntent string, annotatorID string, notes *string) error
}

// PGFeedbackStore PostgreSQL 实现
type PGFeedbackStore struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// NewPGFeedbackStore 创建反馈存储实例
func NewPGFeedbackStore(pool *pgxpool.Pool, logger *slog.Logger) *PGFeedbackStore {
	if logger == nil {
		logger = slog.Default()
	}
	return &PGFeedbackStore{
		pool:   pool,
		logger: logger,
	}
}

// Save 保存反馈记录
func (s *PGFeedbackStore) Save(ctx context.Context, fb *Feedback) error {
	// 序列化分类上下文
	var contextJSON []byte
	var err error
	if fb.ClassificationContext != nil {
		contextJSON, err = json.Marshal(fb.ClassificationContext)
		if err != nil {
			return fmt.Errorf("marshal classification_context: %w", err)
		}
	}

	query := `
		INSERT INTO intent_classification_feedback (
			session_id, request_id, tenant_id,
			predicted_intent, predicted_confidence,
			actual_intent, is_correct, annotator_id, annotated_at, annotation_notes,
			user_accepted_model, user_switched_to_model, user_retry_count,
			session_duration_sec, user_satisfaction_score,
			user_content_hash, classification_context, evolution_id,
			created_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19
		)
		ON CONFLICT (request_id) DO UPDATE SET
			actual_intent = EXCLUDED.actual_intent,
			is_correct = EXCLUDED.is_correct,
			annotator_id = EXCLUDED.annotator_id,
			annotated_at = EXCLUDED.annotated_at,
			annotation_notes = EXCLUDED.annotation_notes,
			user_accepted_model = EXCLUDED.user_accepted_model,
			user_switched_to_model = EXCLUDED.user_switched_to_model,
			user_retry_count = EXCLUDED.user_retry_count,
			session_duration_sec = EXCLUDED.session_duration_sec,
			user_satisfaction_score = EXCLUDED.user_satisfaction_score
		RETURNING id
	`

	err = s.pool.QueryRow(ctx, query,
		fb.SessionID, fb.RequestID, fb.TenantID,
		fb.PredictedIntent, fb.PredictedConfidence,
		fb.ActualIntent, fb.IsCorrect, fb.AnnotatorID, fb.AnnotatedAt, fb.AnnotationNotes,
		fb.UserAcceptedModel, fb.UserSwitchedToModel, fb.UserRetryCount,
		fb.SessionDurationSec, fb.UserSatisfactionScore,
		fb.UserContentHash, contextJSON, fb.EvolutionID,
		fb.CreatedAt,
	).Scan(&fb.ID)

	if err != nil {
		return fmt.Errorf("insert feedback: %w", err)
	}

	return nil
}

// GetRecentFeedback 获取最近的反馈
func (s *PGFeedbackStore) GetRecentFeedback(ctx context.Context, tenantID string, limit int) ([]Feedback, error) {
	if limit <= 0 {
		limit = 100
	}

	query := `
		SELECT 
			id, session_id, request_id, tenant_id,
			predicted_intent, predicted_confidence,
			actual_intent, is_correct, annotator_id, annotated_at, annotation_notes,
			user_accepted_model, user_switched_to_model, user_retry_count,
			session_duration_sec, user_satisfaction_score,
			user_content_hash, classification_context, evolution_id,
			created_at
		FROM intent_classification_feedback
		WHERE tenant_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`

	rows, err := s.pool.Query(ctx, query, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("query feedback: %w", err)
	}
	defer rows.Close()

	var feedbacks []Feedback
	for rows.Next() {
		var fb Feedback
		var contextJSON []byte

		err := rows.Scan(
			&fb.ID, &fb.SessionID, &fb.RequestID, &fb.TenantID,
			&fb.PredictedIntent, &fb.PredictedConfidence,
			&fb.ActualIntent, &fb.IsCorrect, &fb.AnnotatorID, &fb.AnnotatedAt, &fb.AnnotationNotes,
			&fb.UserAcceptedModel, &fb.UserSwitchedToModel, &fb.UserRetryCount,
			&fb.SessionDurationSec, &fb.UserSatisfactionScore,
			&fb.UserContentHash, &contextJSON, &fb.EvolutionID,
			&fb.CreatedAt,
		)
		if err != nil {
			s.logger.Warn("intentconfig: scan feedback failed", "error", err)
			continue
		}

		// 解析上下文
		if len(contextJSON) > 0 {
			if err := json.Unmarshal(contextJSON, &fb.ClassificationContext); err != nil {
				s.logger.Warn("intentconfig: parse context failed", "error", err)
			}
		}

		feedbacks = append(feedbacks, fb)
	}

	return feedbacks, rows.Err()
}

// UpdateAnnotation 更新人工标注
func (s *PGFeedbackStore) UpdateAnnotation(ctx context.Context, requestID string, actualIntent string, annotatorID string, notes *string) error {
	query := `
		UPDATE intent_classification_feedback
		SET 
			actual_intent = $1,
			annotator_id = $2,
			annotation_notes = $3,
			annotated_at = NOW()
		WHERE request_id = $4
	`

	result, err := s.pool.Exec(ctx, query, actualIntent, annotatorID, notes, requestID)
	if err != nil {
		return fmt.Errorf("update annotation: %w", err)
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("feedback not found: %s", requestID)
	}

	s.logger.Info("intentconfig: annotation updated",
		"request_id", requestID,
		"actual_intent", actualIntent,
		"annotator", annotatorID)

	return nil
}
