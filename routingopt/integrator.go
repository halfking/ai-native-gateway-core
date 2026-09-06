package routingopt

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// =============================================================================
// FeedbackIntegrator: RecordFeedback 反馈记录与人工标注集成
// =============================================================================

// feedbackWriter abstracts feedback persistence so tests can stub the DAO.
type feedbackWriter interface {
	Insert(ctx context.Context, log *FeedbackLog) (int64, error)
	MarkHumanCorrection(ctx context.Context, requestID string, p markHumanCorrectionParams) error
}

// affinityUpdater abstracts user-affinity updates (satisfied by ClassificationEnhancer).
type affinityUpdater interface {
	UpdateUserAffinity(ctx context.Context, userID string, taskType string, provider string) error
}

// humanAnnotation mirrors the columns P2.2 needs from training_human_annotations.
type humanAnnotation struct {
	HumanLabel string
	Reason     string
	Annotator  string
}

// FeedbackIntegrator records routing feedback for learning and integrates
// human annotations from P2.1 training_human_annotations table.
//
// Design: docs/p2-ml-routing/p2.2-routing-optimization-plugin-design.md §2.3
type FeedbackIntegrator struct {
	feedback feedbackWriter
	enhancer affinityUpdater
	pool     *pgxpool.Pool // for the P2.1 annotation lookup; nil in tests
}

// NewFeedbackIntegrator constructs an integrator instance.
func NewFeedbackIntegrator(pool *pgxpool.Pool, enhancer *ClassificationEnhancer) *FeedbackIntegrator {
	return &FeedbackIntegrator{
		feedback: NewFeedbackLogDAO(pool),
		enhancer: enhancer,
		pool:     pool,
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

		// Context from the routing request (populated by autoroute.Decider)
		Profile:   nil,
		UserID:    toStrPtr(strconv.Itoa(feedback.UserID)),
		SessionID: toStrPtrNonEmpty(feedback.SessionID),

		// Human correction (Week 2: query from training_human_annotations)
		HasHumanCorrection: feedback.HumanCorrection != nil,
		CorrectProvider:    feedback.HumanCorrection,
		CorrectionReason:   nil,
		Annotator:          nil,
		AnnotatedAt:        nil,
	}
	// UserID "0" means unauthenticated — store NULL instead.
	if feedback.UserID <= 0 {
		log.UserID = nil
	}

	// Set error type if failed
	if !feedback.IsSuccess {
		errorType := "unknown"
		log.ErrorType = &errorType
	}

	// 2. Insert into routing_feedback_log (同步写入，Week 2 改为异步)
	_, err := i.feedback.Insert(ctx, log)
	if err != nil {
		// Log error but don't fail the request
		// Week 2: push to retry queue
		return err
	}

	// 3. Update user affinity (best-effort, don't fail on error)
	if feedback.UserID > 0 {
		_ = i.enhancer.UpdateUserAffinity(ctx, strconv.Itoa(feedback.UserID), feedback.TaskType, feedback.PredictedProvider)
	}

	// 4. Enrich from P2.1 human annotations when one exists for this request
	// (best-effort; B3 closes the human-annotation loop).
	i.enrichFromHumanAnnotations(ctx, log)

	return nil
}

// enrichFromHumanAnnotations looks up training_human_annotations by request_id
// and marks the just-inserted feedback row when an annotation exists.
// Errors are swallowed: annotation enrichment must never break feedback writes.
func (i *FeedbackIntegrator) enrichFromHumanAnnotations(ctx context.Context, log *FeedbackLog) {
	ann, err := i.lookupAnnotation(ctx, log.RequestID)
	if err != nil || ann == nil {
		return
	}
	now := time.Now()
	reason := ann.Reason
	annotator := ann.Annotator
	if err := i.feedback.MarkHumanCorrection(ctx, log.RequestID, markHumanCorrectionParams{
		CorrectProvider: ann.HumanLabel,
		Reason:          &reason,
		Annotator:       &annotator,
		AnnotatedAt:     &now,
	}); err != nil {
		return
	}
	log.HasHumanCorrection = true
}

// lookupAnnotation queries the P2.1 annotation table for a request.
// Returns (nil, nil) when no annotation exists or no pool is wired (tests).
func (i *FeedbackIntegrator) lookupAnnotation(ctx context.Context, requestID string) (*humanAnnotation, error) {
	if i.pool == nil || requestID == "" {
		return nil, nil
	}
	var ann humanAnnotation
	err := i.pool.QueryRow(ctx, `
		SELECT human_label, COALESCE(annotation_reason, ''), annotator
		FROM training_human_annotations
		WHERE request_id = $1
		ORDER BY annotated_at DESC
		LIMIT 1
	`, requestID).Scan(&ann.HumanLabel, &ann.Reason, &ann.Annotator)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &ann, nil
}

// IntegrateHumanAnnotation integrates a human annotation from P2.1.
// Called by the annotation API when a user submits a correction.
func (i *FeedbackIntegrator) IntegrateHumanAnnotation(ctx context.Context, requestID string, correctProvider string, annotator string, reason string) error {
	now := time.Now()
	return i.feedback.MarkHumanCorrection(ctx, requestID, markHumanCorrectionParams{
		CorrectProvider: correctProvider,
		Reason:          &reason,
		Annotator:       &annotator,
		AnnotatedAt:     &now,
	})
}

// GetHumanAnnotationStats returns statistics on human annotation usage.
// Used by AdaptiveLearner.GetStats() to report human_annotations_used.
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

// toStrPtr converts string to *string.
func toStrPtr(v string) *string {
	return &v
}

// toStrPtrNonEmpty converts string to *string, or nil when empty.
func toStrPtrNonEmpty(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
