package routingopt

import (
	"context"
	"errors"
	"log/slog"
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
// Persistence (P2.2 Track B): instances built through NewFeedbackIntegrator
// push feedback onto an async batch writer (buffered queue 10000, batch
// INSERT every 100 entries or 5s via pgx.Batch). SetControlledSyncFallback
// restores the legacy synchronous INSERT path. Instances built via struct
// literal (tests) leave batch nil and keep the legacy synchronous path.
//
// Design: docs/p2-ml-routing/p2.2-routing-optimization-plugin-design.md §2.3
type FeedbackIntegrator struct {
	feedback feedbackWriter
	enhancer affinityUpdater
	pool     *pgxpool.Pool // for the P2.1 annotation lookup; nil in tests

	// batch is the async batch writer (nil → legacy synchronous INSERT path).
	// The background worker starts lazily on first enqueue.
	batch *FeedbackBatchWriter
	// controlledSyncFallback forces the legacy synchronous INSERT path even
	// when a batch writer is wired (Options.ControlledSyncFallback=true).
	controlledSyncFallback bool
}

// NewFeedbackIntegrator constructs an integrator instance. When pool is
// non-nil the async batch writer is wired (worker starts lazily on first
// RecordFeedback); with a nil pool (tests / unwired) the integrator keeps
// the legacy synchronous path.
func NewFeedbackIntegrator(pool *pgxpool.Pool, enhancer *ClassificationEnhancer) *FeedbackIntegrator {
	i := &FeedbackIntegrator{
		feedback: NewFeedbackLogDAO(pool),
		enhancer: enhancer,
		pool:     pool,
	}
	if pool != nil {
		// P2.2 Track B: async batched persistence. The annotation write-back
		// runs inside the writer right before each batch INSERT.
		i.batch = NewFeedbackBatchWriter(pool, defaultFeedbackBatchConfig(), i.enrichFromHumanAnnotations)
	}
	return i
}

// SetControlledSyncFallback toggles the legacy synchronous INSERT path.
// Wire this from Options.ControlledSyncFallback (cmd/gateway /
// NewRealOptimizerWithOptions): true → every RecordFeedback blocks on a
// single-row INSERT like pre-P2.2; false → async batched writes.
func (i *FeedbackIntegrator) SetControlledSyncFallback(v bool) {
	i.controlledSyncFallback = v
}

// Flush drains the async feedback queue (see FeedbackBatchWriter.Flush).
// Called by graceful shutdown wiring: pending feedback is written before
// process exit. No-op when the integrator runs the synchronous path.
func (i *FeedbackIntegrator) Flush(ctx context.Context) {
	if i == nil || i.batch == nil {
		return
	}
	i.batch.Flush(ctx)
}

// FeedbackBatch exposes the async batch writer for wiring (nil when the
// integrator runs the legacy synchronous path):
//   - graceful shutdown: optimizer.integrator.FeedbackBatch().Flush(ctx)
//   - Track C metrics:   routingopt.AttachFeedbackCounters(w.Counters())
func (i *FeedbackIntegrator) FeedbackBatch() *FeedbackBatchWriter {
	if i == nil {
		return nil
	}
	return i.batch
}

// RecordFeedback implements the RecordFeedback hook.
// Records routing feedback asynchronously (fire-and-forget).
//
// P2.2 Track B（默认路径）：
//   - 非阻塞入队 async batch writer（队列满丢弃 + 计数，绝不阻塞路由）；
//     批量 INSERT（pgx.Batch，满 100 条或每 5s）由后台 worker 完成
//   - P2.1 人工标注回写（lookupAnnotation + MarkHumanCorrection）改由
//     worker 在批量刷盘前逐条执行（best-effort）
//   - user affinity 维持现状：UserID>0 时同步 best-effort 更新，错误忽略
//
// Options.ControlledSyncFallback=true（或 batch writer 未接线）时走旧的
// 同步路径：单条 INSERT → affinity → 标注回写。
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

	// 2. Async batched path (P2.2 Track B): non-blocking enqueue; the worker
	// runs the annotation write-back then the batch INSERT. Errors surface
	// later as counters + slog, never on this hot path.
	if i.batch != nil && !i.controlledSyncFallback {
		i.batch.Enqueue(log)

		// 3. Update user affinity (best-effort, don't fail on error) —
		// semantics unchanged from the synchronous path.
		if feedback.UserID > 0 {
			_ = i.enhancer.UpdateUserAffinity(ctx, strconv.Itoa(feedback.UserID), feedback.TaskType, feedback.PredictedProvider)
		}
		return nil
	}

	// Legacy synchronous INSERT path (ControlledSyncFallback / unwired batch).
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
// Best-effort: annotation enrichment must never break feedback writes, so
// failures are logged and counted-not — they never propagate to the caller
// (in the async path this runs inside the batch worker before the INSERT).
func (i *FeedbackIntegrator) enrichFromHumanAnnotations(ctx context.Context, log *FeedbackLog) {
	ann, err := i.lookupAnnotation(ctx, log.RequestID)
	if err != nil {
		slog.WarnContext(ctx, "routingopt: human annotation lookup failed (ignored)",
			"err", err, "request_id", log.RequestID)
		return
	}
	if ann == nil {
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
		slog.WarnContext(ctx, "routingopt: human annotation write-back failed (ignored)",
			"err", err, "request_id", log.RequestID)
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
