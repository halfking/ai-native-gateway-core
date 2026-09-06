package routingopt

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// =============================================================================
// FeedbackIntegrator: RecordFeedback 反馈记录与人工标注集成
// =============================================================================

// FeedbackIntegrator records routing feedback for learning and integrates
// human annotations from P2.1 training_human_annotations table.
//
// Design: docs/p2-ml-routing/p2.2-routing-optimization-plugin-design.md §2.3
type FeedbackIntegrator struct {
	feedbackDAO *FeedbackLogDAO
	enhancer    *ClassificationEnhancer // for updating user affinity
	pool        *pgxpool.Pool
}

// NewFeedbackIntegrator constructs an integrator instance.
func NewFeedbackIntegrator(pool *pgxpool.Pool, enhancer *ClassificationEnhancer) *FeedbackIntegrator {
	return &FeedbackIntegrator{
		feedbackDAO: NewFeedbackLogDAO(pool),
		enhancer:    enhancer,
		pool:        pool,
	}
}

// RecordFeedback implements the RecordFeedback hook.
// Records routing feedback asynchronously (fire-and-forget).
//
// Week 1 骨架版本：
//   - 同步写入 routing_feedback_log 表
//   - 更新 user affinity (ClassificationEnhancer.UpdateUserAffinity)
//
// Week 2 完整版本：
//   - 异步写入（通过 channel + background worker）
//   - 集成 P2.1 人工标注（从 training_human_annotations 表关联）
//   - 批量写入优化（batch insert every 100 records or 5 seconds）
func (i *FeedbackIntegrator) RecordFeedback(ctx context.Context, feedback RoutingFeedback) error {
	// 1. Convert RoutingFeedback to FeedbackLog
	log := &FeedbackLog{
		RequestID:         feedback.RequestID,
		TaskType:          feedback.TaskType,
		PredictedProvider: feedback.PredictedProvider,
		Confidence:        0.8, // TODO: extract from Decision
		Success:           feedback.IsSuccess,
		
		// Actual metrics
		ActualLatencyMs: toIntPtr(int(feedback.Latency.Milliseconds())),
		ActualCost:      &feedback.Cost,
		
		// Context (extract from feedback if available)
		// Week 2: populate from request context
		Profile:   nil,
		UserID:    nil,
		SessionID: nil,
		
		// Human correction (Week 2: query from training_human_annotations)
		HasHumanCorrection: feedback.HumanCorrection != nil,
		CorrectProvider:    feedback.HumanCorrection,
		CorrectionReason:   nil,
		Annotator:          nil,
		AnnotatedAt:        nil,
	}
	
	// Set error type if failed
	if !feedback.IsSuccess {
		errorType := "unknown"
		log.ErrorType = &errorType
	}
	
	// 2. Insert into routing_feedback_log (同步写入，Week 2 改为异步)
	_, err := i.feedbackDAO.Insert(ctx, log)
	if err != nil {
		// Log error but don't fail the request
		// Week 2: push to retry queue
		return err
	}
	
	// 3. Update user affinity (best-effort, don't fail on error)
	if log.UserID != nil && *log.UserID != "" {
		_ = i.enhancer.UpdateUserAffinity(ctx, *log.UserID, feedback.TaskType, feedback.PredictedProvider)
	}
	
	return nil
}

// IntegrateHumanAnnotation integrates a human annotation from P2.1.
// Called by the annotation API when a user submits a correction.
//
// Week 1: 骨架版本（更新已有 feedback log）
// Week 2: 自动关联 training_human_annotations 表
func (i *FeedbackIntegrator) IntegrateHumanAnnotation(ctx context.Context, requestID string, correctProvider string, annotator string, reason string) error {
	// 1. Find existing feedback log
	log, err := i.feedbackDAO.GetByRequestID(ctx, requestID)
	if err != nil {
		return err
	}
	
	// 2. Update with human correction
	now := time.Now()
	log.HasHumanCorrection = true
	log.CorrectProvider = &correctProvider
	log.CorrectionReason = &reason
	log.Annotator = &annotator
	log.AnnotatedAt = &now
	
	// 3. Re-insert (UPDATE not implemented in Week 1 DAO, Week 2 add Update method)
	// Week 1: 仅记录到日志，Week 2 实现真正的 UPDATE
	// For now, we insert a new record (duplicate request_id allowed)
	_, err = i.feedbackDAO.Insert(ctx, log)
	return err
}

// GetHumanAnnotationStats returns statistics on human annotation usage.
// Used by AdaptiveLearner.GetStats() to report human_annotations_used.
//
// Week 1: 简单计数
// Week 2: 详细统计（by task_type, by annotator, accuracy impact）
func (i *FeedbackIntegrator) GetHumanAnnotationStats(ctx context.Context, since time.Time) (int, error) {
	// Query count of human-corrected feedback logs
	var count int
	err := i.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM routing_feedback_log
		WHERE has_human_correction = TRUE
		  AND created_at >= $1
	`, since).Scan(&count)
	
	return count, err
}

// toIntPtr converts int to *int (helper for nullable fields).
func toIntPtr(v int) *int {
	return &v
}
