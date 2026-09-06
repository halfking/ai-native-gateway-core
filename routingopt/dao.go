// Package routingopt provides data access objects for P2.2 routing optimization.
//
// DAO layer handles all database interactions for the 4 core tables:
//   - routing_optimization_state: plugin parameters and performance metrics
//   - routing_feedback_log: real-time routing feedback
//   - routing_optimization_metrics: 5-minute aggregated metrics
//   - routing_user_affinity: user task type distribution and provider preferences
//
// Design: docs/p2-ml-routing/p2.2-routing-optimization-plugin-design.md §3
package routingopt

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// =============================================================================
// OptimizationState DAO
// =============================================================================

// OptimizationState represents a versioned snapshot of plugin parameters.
type OptimizationState struct {
	ID      int64 `db:"id"`
	Version int   `db:"version"`

	// Classifier parameters (PostClassify confidence adjustment)
	ClassifierWeights    map[string]float64 `db:"classifier_weights"`
	ConfidenceThresholds map[string]float64 `db:"confidence_thresholds"`

	// Recommender parameters (RecommendModel multi-objective optimization)
	RecommenderWeights map[string]float64 `db:"recommender_weights"` // quality/cost/latency/availability
	ExplorationRate    float64            `db:"exploration_rate"`

	// Online learning parameters (AdaptiveLearner)
	LearningRate     float64 `db:"learning_rate"`
	AdaptationWindow int     `db:"adaptation_window"`

	// Performance metrics (aggregated from routing_optimization_metrics)
	OverallAccuracy    *float64           `db:"overall_accuracy"`
	AccuracyByTask     map[string]float64 `db:"accuracy_by_task"`
	AccuracyByProvider map[string]float64 `db:"accuracy_by_provider"`

	// Version management
	ActivatedAt   time.Time  `db:"activated_at"`
	DeactivatedAt *time.Time `db:"deactivated_at"`
	CreatedBy     string     `db:"created_by"`
	Notes         *string    `db:"notes"`

	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

// OptimizationStateDAO provides CRUD operations for routing_optimization_state table.
type OptimizationStateDAO struct {
	pool *pgxpool.Pool
}

// NewOptimizationStateDAO constructs a DAO instance.
func NewOptimizationStateDAO(pool *pgxpool.Pool) *OptimizationStateDAO {
	return &OptimizationStateDAO{pool: pool}
}

// GetActive returns the currently active optimization state (deactivated_at IS NULL).
// Returns sql.ErrNoRows if no active state exists.
func (dao *OptimizationStateDAO) GetActive(ctx context.Context) (*OptimizationState, error) {
	query := `
		SELECT id, version, classifier_weights, confidence_thresholds,
		       recommender_weights, exploration_rate, learning_rate, adaptation_window,
		       overall_accuracy, accuracy_by_task, accuracy_by_provider,
		       activated_at, deactivated_at, created_by, notes, created_at, updated_at
		FROM routing_optimization_state
		WHERE deactivated_at IS NULL
		ORDER BY activated_at DESC
		LIMIT 1
	`

	row := dao.pool.QueryRow(ctx, query)
	state := &OptimizationState{}

	var classifierWeightsJSON, confidenceThresholdsJSON, recommenderWeightsJSON []byte
	var accuracyByTaskJSON, accuracyByProviderJSON []byte

	err := row.Scan(
		&state.ID, &state.Version, &classifierWeightsJSON, &confidenceThresholdsJSON,
		&recommenderWeightsJSON, &state.ExplorationRate, &state.LearningRate, &state.AdaptationWindow,
		&state.OverallAccuracy, &accuracyByTaskJSON, &accuracyByProviderJSON,
		&state.ActivatedAt, &state.DeactivatedAt, &state.CreatedBy, &state.Notes,
		&state.CreatedAt, &state.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	// Unmarshal JSONB fields
	if err := json.Unmarshal(classifierWeightsJSON, &state.ClassifierWeights); err != nil {
		return nil, fmt.Errorf("unmarshal classifier_weights: %w", err)
	}
	if err := json.Unmarshal(confidenceThresholdsJSON, &state.ConfidenceThresholds); err != nil {
		return nil, fmt.Errorf("unmarshal confidence_thresholds: %w", err)
	}
	if err := json.Unmarshal(recommenderWeightsJSON, &state.RecommenderWeights); err != nil {
		return nil, fmt.Errorf("unmarshal recommender_weights: %w", err)
	}
	if accuracyByTaskJSON != nil {
		if err := json.Unmarshal(accuracyByTaskJSON, &state.AccuracyByTask); err != nil {
			return nil, fmt.Errorf("unmarshal accuracy_by_task: %w", err)
		}
	}
	if accuracyByProviderJSON != nil {
		if err := json.Unmarshal(accuracyByProviderJSON, &state.AccuracyByProvider); err != nil {
			return nil, fmt.Errorf("unmarshal accuracy_by_provider: %w", err)
		}
	}

	return state, nil
}

// Create inserts a new optimization state and deactivates the previous active state.
// Returns the ID of the newly created state.
func (dao *OptimizationStateDAO) Create(ctx context.Context, state *OptimizationState) (int64, error) {
	// Marshal JSONB fields
	classifierWeightsJSON, err := json.Marshal(state.ClassifierWeights)
	if err != nil {
		return 0, fmt.Errorf("marshal classifier_weights: %w", err)
	}
	confidenceThresholdsJSON, err := json.Marshal(state.ConfidenceThresholds)
	if err != nil {
		return 0, fmt.Errorf("marshal confidence_thresholds: %w", err)
	}
	recommenderWeightsJSON, err := json.Marshal(state.RecommenderWeights)
	if err != nil {
		return 0, fmt.Errorf("marshal recommender_weights: %w", err)
	}
	accuracyByTaskJSON, err := json.Marshal(state.AccuracyByTask)
	if err != nil {
		return 0, fmt.Errorf("marshal accuracy_by_task: %w", err)
	}
	accuracyByProviderJSON, err := json.Marshal(state.AccuracyByProvider)
	if err != nil {
		return 0, fmt.Errorf("marshal accuracy_by_provider: %w", err)
	}

	// Begin transaction: deactivate old + insert new
	tx, err := dao.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Deactivate previous active state
	_, err = tx.Exec(ctx, `
		UPDATE routing_optimization_state
		SET deactivated_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE deactivated_at IS NULL
	`)
	if err != nil {
		return 0, fmt.Errorf("deactivate old state: %w", err)
	}

	// Insert new state
	var id int64
	err = tx.QueryRow(ctx, `
		INSERT INTO routing_optimization_state (
			version, classifier_weights, confidence_thresholds,
			recommender_weights, exploration_rate, learning_rate, adaptation_window,
			overall_accuracy, accuracy_by_task, accuracy_by_provider,
			activated_at, created_by, notes
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING id
	`, state.Version, classifierWeightsJSON, confidenceThresholdsJSON,
		recommenderWeightsJSON, state.ExplorationRate, state.LearningRate, state.AdaptationWindow,
		state.OverallAccuracy, accuracyByTaskJSON, accuracyByProviderJSON,
		state.ActivatedAt, state.CreatedBy, state.Notes,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert new state: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit transaction: %w", err)
	}

	return id, nil
}

// UpdateMetrics updates the performance metrics of the active state.
// This is called by the metrics aggregation worker (5-minute interval).
func (dao *OptimizationStateDAO) UpdateMetrics(ctx context.Context, overallAccuracy float64, accuracyByTask, accuracyByProvider map[string]float64) error {
	accuracyByTaskJSON, err := json.Marshal(accuracyByTask)
	if err != nil {
		return fmt.Errorf("marshal accuracy_by_task: %w", err)
	}
	accuracyByProviderJSON, err := json.Marshal(accuracyByProvider)
	if err != nil {
		return fmt.Errorf("marshal accuracy_by_provider: %w", err)
	}

	_, err = dao.pool.Exec(ctx, `
		UPDATE routing_optimization_state
		SET overall_accuracy = $1,
		    accuracy_by_task = $2,
		    accuracy_by_provider = $3,
		    updated_at = CURRENT_TIMESTAMP
		WHERE deactivated_at IS NULL
	`, overallAccuracy, accuracyByTaskJSON, accuracyByProviderJSON)

	return err
}

// =============================================================================
// FeedbackLog DAO
// =============================================================================

// FeedbackLog represents a single routing decision feedback entry.
type FeedbackLog struct {
	ID        int64  `db:"id"`
	RequestID string `db:"request_id"`

	// Routing decision
	TaskType          string  `db:"task_type"`
	PredictedProvider string  `db:"predicted_provider"`
	Confidence        float64 `db:"confidence"`

	// Actual result
	ActualLatencyMs *int     `db:"actual_latency_ms"`
	ActualCost      *float64 `db:"actual_cost"`
	Success         bool     `db:"success"`
	ErrorType       *string  `db:"error_type"`

	// Context
	Profile   *string `db:"profile"`
	UserID    *string `db:"user_id"`
	SessionID *string `db:"session_id"`

	// Human correction (from P2.1 training_human_annotations)
	HasHumanCorrection bool       `db:"has_human_correction"`
	CorrectProvider    *string    `db:"correct_provider"`
	CorrectionReason   *string    `db:"correction_reason"`
	Annotator          *string    `db:"annotator"`
	AnnotatedAt        *time.Time `db:"annotated_at"`

	CreatedAt time.Time `db:"created_at"`
}

// FeedbackLogDAO provides CRUD operations for routing_feedback_log table.
type FeedbackLogDAO struct {
	pool *pgxpool.Pool
}

// NewFeedbackLogDAO constructs a DAO instance.
func NewFeedbackLogDAO(pool *pgxpool.Pool) *FeedbackLogDAO {
	return &FeedbackLogDAO{pool: pool}
}

// Insert records a new routing feedback entry.
func (dao *FeedbackLogDAO) Insert(ctx context.Context, log *FeedbackLog) (int64, error) {
	var id int64
	err := dao.pool.QueryRow(ctx, `
		INSERT INTO routing_feedback_log (
			request_id, task_type, predicted_provider, confidence,
			actual_latency_ms, actual_cost, success, error_type,
			profile, user_id, session_id,
			has_human_correction, correct_provider, correction_reason, annotator, annotated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		RETURNING id
	`, log.RequestID, log.TaskType, log.PredictedProvider, log.Confidence,
		log.ActualLatencyMs, log.ActualCost, log.Success, log.ErrorType,
		log.Profile, log.UserID, log.SessionID,
		log.HasHumanCorrection, log.CorrectProvider, log.CorrectionReason, log.Annotator, log.AnnotatedAt,
	).Scan(&id)

	return id, err
}

// GetByRequestID retrieves feedback by request_id.
func (dao *FeedbackLogDAO) GetByRequestID(ctx context.Context, requestID string) (*FeedbackLog, error) {
	query := `
		SELECT id, request_id, task_type, predicted_provider, confidence,
		       actual_latency_ms, actual_cost, success, error_type,
		       profile, user_id, session_id,
		       has_human_correction, correct_provider, correction_reason, annotator, annotated_at,
		       created_at
		FROM routing_feedback_log
		WHERE request_id = $1
		ORDER BY created_at DESC
		LIMIT 1
	`

	log := &FeedbackLog{}
	err := dao.pool.QueryRow(ctx, query, requestID).Scan(
		&log.ID, &log.RequestID, &log.TaskType, &log.PredictedProvider, &log.Confidence,
		&log.ActualLatencyMs, &log.ActualCost, &log.Success, &log.ErrorType,
		&log.Profile, &log.UserID, &log.SessionID,
		&log.HasHumanCorrection, &log.CorrectProvider, &log.CorrectionReason, &log.Annotator, &log.AnnotatedAt,
		&log.CreatedAt,
	)
	if err != nil {
		return nil, err
	}

	return log, nil
}

// GetRecentLogs returns the most recent N feedback logs (for sliding window learning).
func (dao *FeedbackLogDAO) GetRecentLogs(ctx context.Context, limit int) ([]*FeedbackLog, error) {
	query := `
		SELECT id, request_id, task_type, predicted_provider, confidence,
		       actual_latency_ms, actual_cost, success, error_type,
		       profile, user_id, session_id,
		       has_human_correction, correct_provider, correction_reason, annotator, annotated_at,
		       created_at
		FROM routing_feedback_log
		ORDER BY created_at DESC
		LIMIT $1
	`

	rows, err := dao.pool.Query(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []*FeedbackLog
	for rows.Next() {
		log := &FeedbackLog{}
		err := rows.Scan(
			&log.ID, &log.RequestID, &log.TaskType, &log.PredictedProvider, &log.Confidence,
			&log.ActualLatencyMs, &log.ActualCost, &log.Success, &log.ErrorType,
			&log.Profile, &log.UserID, &log.SessionID,
			&log.HasHumanCorrection, &log.CorrectProvider, &log.CorrectionReason, &log.Annotator, &log.AnnotatedAt,
			&log.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		logs = append(logs, log)
	}

	return logs, rows.Err()
}

// GetHumanCorrections returns all human-corrected feedback logs (for weighted accuracy).
func (dao *FeedbackLogDAO) GetHumanCorrections(ctx context.Context, since time.Time, limit int) ([]*FeedbackLog, error) {
	query := `
		SELECT id, request_id, task_type, predicted_provider, confidence,
		       actual_latency_ms, actual_cost, success, error_type,
		       profile, user_id, session_id,
		       has_human_correction, correct_provider, correction_reason, annotator, annotated_at,
		       created_at
		FROM routing_feedback_log
		WHERE has_human_correction = TRUE
		  AND created_at >= $1
		ORDER BY created_at DESC
		LIMIT $2
	`

	rows, err := dao.pool.Query(ctx, query, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []*FeedbackLog
	for rows.Next() {
		log := &FeedbackLog{}
		err := rows.Scan(
			&log.ID, &log.RequestID, &log.TaskType, &log.PredictedProvider, &log.Confidence,
			&log.ActualLatencyMs, &log.ActualCost, &log.Success, &log.ErrorType,
			&log.Profile, &log.UserID, &log.SessionID,
			&log.HasHumanCorrection, &log.CorrectProvider, &log.CorrectionReason, &log.Annotator, &log.AnnotatedAt,
			&log.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		logs = append(logs, log)
	}

	return logs, rows.Err()
}

// =============================================================================
// OptimizationMetrics DAO
// =============================================================================

// OptimizationMetrics represents a 5-minute aggregated metrics bucket.
type OptimizationMetrics struct {
	ID         int64     `db:"id"`
	TimeBucket time.Time `db:"time_bucket"`

	// Grouping dimensions (NULL = global aggregation)
	TaskType          *string `db:"task_type"`
	PredictedProvider *string `db:"predicted_provider"`

	// Aggregated metrics
	TotalRequests      int      `db:"total_requests"`
	SuccessfulRequests int      `db:"successful_requests"`
	FailedRequests     int      `db:"failed_requests"`
	AccuracyRate       *float64 `db:"accuracy_rate"`

	AvgConfidence *float64 `db:"avg_confidence"`
	AvgLatencyMs  *int     `db:"avg_latency_ms"`
	AvgCost       *float64 `db:"avg_cost"`

	P50LatencyMs *int `db:"p50_latency_ms"`
	P95LatencyMs *int `db:"p95_latency_ms"`
	P99LatencyMs *int `db:"p99_latency_ms"`

	// Human annotation feedback
	HumanCorrections  int      `db:"human_corrections"`
	HumanAccuracyRate *float64 `db:"human_accuracy_rate"`

	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

// OptimizationMetricsDAO provides CRUD operations for routing_optimization_metrics table.
type OptimizationMetricsDAO struct {
	pool *pgxpool.Pool
}

// NewOptimizationMetricsDAO constructs a DAO instance.
func NewOptimizationMetricsDAO(pool *pgxpool.Pool) *OptimizationMetricsDAO {
	return &OptimizationMetricsDAO{pool: pool}
}

// Upsert inserts or updates a metrics bucket.
func (dao *OptimizationMetricsDAO) Upsert(ctx context.Context, metrics *OptimizationMetrics) error {
	_, err := dao.pool.Exec(ctx, `
		INSERT INTO routing_optimization_metrics (
			time_bucket, task_type, predicted_provider,
			total_requests, successful_requests, failed_requests, accuracy_rate,
			avg_confidence, avg_latency_ms, avg_cost,
			p50_latency_ms, p95_latency_ms, p99_latency_ms,
			human_corrections, human_accuracy_rate
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		ON CONFLICT (time_bucket, task_type, predicted_provider) DO UPDATE SET
			total_requests = EXCLUDED.total_requests,
			successful_requests = EXCLUDED.successful_requests,
			failed_requests = EXCLUDED.failed_requests,
			accuracy_rate = EXCLUDED.accuracy_rate,
			avg_confidence = EXCLUDED.avg_confidence,
			avg_latency_ms = EXCLUDED.avg_latency_ms,
			avg_cost = EXCLUDED.avg_cost,
			p50_latency_ms = EXCLUDED.p50_latency_ms,
			p95_latency_ms = EXCLUDED.p95_latency_ms,
			p99_latency_ms = EXCLUDED.p99_latency_ms,
			human_corrections = EXCLUDED.human_corrections,
			human_accuracy_rate = EXCLUDED.human_accuracy_rate,
			updated_at = CURRENT_TIMESTAMP
	`, metrics.TimeBucket, metrics.TaskType, metrics.PredictedProvider,
		metrics.TotalRequests, metrics.SuccessfulRequests, metrics.FailedRequests, metrics.AccuracyRate,
		metrics.AvgConfidence, metrics.AvgLatencyMs, metrics.AvgCost,
		metrics.P50LatencyMs, metrics.P95LatencyMs, metrics.P99LatencyMs,
		metrics.HumanCorrections, metrics.HumanAccuracyRate,
	)

	return err
}

// GetAggregatedMetrics returns aggregated metrics over a time range.
func (dao *OptimizationMetricsDAO) GetAggregatedMetrics(ctx context.Context, since time.Time) (overallAccuracy float64, accuracyByTask, accuracyByProvider map[string]float64, err error) {
	// Overall accuracy
	err = dao.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(successful_requests)::float / NULLIF(SUM(total_requests), 0), 0)
		FROM routing_optimization_metrics
		WHERE time_bucket >= $1
	`, since).Scan(&overallAccuracy)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("query overall accuracy: %w", err)
	}

	// Accuracy by task type
	accuracyByTask = make(map[string]float64)
	rows, err := dao.pool.Query(ctx, `
		SELECT task_type, SUM(successful_requests)::float / NULLIF(SUM(total_requests), 0)
		FROM routing_optimization_metrics
		WHERE time_bucket >= $1 AND task_type IS NOT NULL
		GROUP BY task_type
	`, since)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("query accuracy by task: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var taskType string
		var accuracy float64
		if err := rows.Scan(&taskType, &accuracy); err != nil {
			return 0, nil, nil, err
		}
		accuracyByTask[taskType] = accuracy
	}
	if err := rows.Err(); err != nil {
		return 0, nil, nil, err
	}

	// Accuracy by provider
	accuracyByProvider = make(map[string]float64)
	rows, err = dao.pool.Query(ctx, `
		SELECT predicted_provider, SUM(successful_requests)::float / NULLIF(SUM(total_requests), 0)
		FROM routing_optimization_metrics
		WHERE time_bucket >= $1 AND predicted_provider IS NOT NULL
		GROUP BY predicted_provider
	`, since)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("query accuracy by provider: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var provider string
		var accuracy float64
		if err := rows.Scan(&provider, &accuracy); err != nil {
			return 0, nil, nil, err
		}
		accuracyByProvider[provider] = accuracy
	}
	if err := rows.Err(); err != nil {
		return 0, nil, nil, err
	}

	return overallAccuracy, accuracyByTask, accuracyByProvider, nil
}

// =============================================================================
// UserAffinity DAO
// =============================================================================

// UserAffinity represents user task type distribution and provider preferences.
type UserAffinity struct {
	ID     int64  `db:"id"`
	UserID string `db:"user_id"`

	// Task type distribution (recent 100 requests)
	TaskTypeDistribution map[string]int     `db:"task_type_distribution"`
	PreferredProviders   map[string]float64 `db:"preferred_providers"`

	// Statistics
	TotalRequests int       `db:"total_requests"`
	LastRequestAt time.Time `db:"last_request_at"`

	// Session pattern (auto-detected)
	SessionPattern *string `db:"session_pattern"`

	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

// UserAffinityDAO provides CRUD operations for routing_user_affinity table.
type UserAffinityDAO struct {
	pool *pgxpool.Pool
}

// NewUserAffinityDAO constructs a DAO instance.
func NewUserAffinityDAO(pool *pgxpool.Pool) *UserAffinityDAO {
	return &UserAffinityDAO{pool: pool}
}

// GetByUserID retrieves user affinity by user_id.
func (dao *UserAffinityDAO) GetByUserID(ctx context.Context, userID string) (*UserAffinity, error) {
	query := `
		SELECT id, user_id, task_type_distribution, preferred_providers,
		       total_requests, last_request_at, session_pattern,
		       created_at, updated_at
		FROM routing_user_affinity
		WHERE user_id = $1
	`

	affinity := &UserAffinity{}
	var taskTypeDistributionJSON, preferredProvidersJSON []byte

	err := dao.pool.QueryRow(ctx, query, userID).Scan(
		&affinity.ID, &affinity.UserID, &taskTypeDistributionJSON, &preferredProvidersJSON,
		&affinity.TotalRequests, &affinity.LastRequestAt, &affinity.SessionPattern,
		&affinity.CreatedAt, &affinity.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	// Unmarshal JSONB fields
	if err := json.Unmarshal(taskTypeDistributionJSON, &affinity.TaskTypeDistribution); err != nil {
		return nil, fmt.Errorf("unmarshal task_type_distribution: %w", err)
	}
	if err := json.Unmarshal(preferredProvidersJSON, &affinity.PreferredProviders); err != nil {
		return nil, fmt.Errorf("unmarshal preferred_providers: %w", err)
	}

	return affinity, nil
}

// Upsert inserts or updates user affinity.
func (dao *UserAffinityDAO) Upsert(ctx context.Context, affinity *UserAffinity) error {
	taskTypeDistributionJSON, err := json.Marshal(affinity.TaskTypeDistribution)
	if err != nil {
		return fmt.Errorf("marshal task_type_distribution: %w", err)
	}
	preferredProvidersJSON, err := json.Marshal(affinity.PreferredProviders)
	if err != nil {
		return fmt.Errorf("marshal preferred_providers: %w", err)
	}

	_, err = dao.pool.Exec(ctx, `
		INSERT INTO routing_user_affinity (
			user_id, task_type_distribution, preferred_providers,
			total_requests, last_request_at, session_pattern
		) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (user_id) DO UPDATE SET
			task_type_distribution = EXCLUDED.task_type_distribution,
			preferred_providers = EXCLUDED.preferred_providers,
			total_requests = EXCLUDED.total_requests,
			last_request_at = EXCLUDED.last_request_at,
			session_pattern = EXCLUDED.session_pattern,
			updated_at = CURRENT_TIMESTAMP
	`, affinity.UserID, taskTypeDistributionJSON, preferredProvidersJSON,
		affinity.TotalRequests, affinity.LastRequestAt, affinity.SessionPattern,
	)

	return err
}

// markHumanCorrectionParams carries the P2.1 annotation fields written back
// onto a routing_feedback_log row.
type markHumanCorrectionParams struct {
	CorrectProvider string
	Reason          *string
	Annotator       *string
	AnnotatedAt     *time.Time
}

// MarkHumanCorrection flags the feedback row(s) for a request as
// human-corrected. Idempotent: repeated calls refresh the correction fields.
func (dao *FeedbackLogDAO) MarkHumanCorrection(ctx context.Context, requestID string, p markHumanCorrectionParams) error {
	_, err := dao.pool.Exec(ctx, `
		UPDATE routing_feedback_log
		SET has_human_correction = TRUE,
		    correct_provider = $2,
		    correction_reason = $3,
		    annotator = $4,
		    annotated_at = $5
		WHERE request_id = $1
	`, requestID, p.CorrectProvider, p.Reason, p.Annotator, p.AnnotatedAt)
	return err
}

// GetAutoAccuracyCounts returns (successful, total) feedback counts since the
// given time, based on the success column (auto feedback only — human
// corrections are weighted separately by the learner).
func (dao *FeedbackLogDAO) GetAutoAccuracyCounts(ctx context.Context, since time.Time) (successful, total int, err error) {
	err = dao.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(CASE WHEN success THEN 1 ELSE 0 END), 0),
		       COUNT(*)
		FROM routing_feedback_log
		WHERE created_at >= $1
	`, since).Scan(&successful, &total)
	return successful, total, err
}

// GetHumanCorrectionCounts returns (agreeing, total) human-corrected feedback
// counts since the given time. "Agreeing" means the AUTO prediction matched
// the annotator's correct provider (predicted_provider == correct_provider).
func (dao *FeedbackLogDAO) GetHumanCorrectionCounts(ctx context.Context, since time.Time) (agreeing, total int, err error) {
	err = dao.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(CASE WHEN predicted_provider = correct_provider THEN 1 ELSE 0 END), 0),
		       COUNT(*)
		FROM routing_feedback_log
		WHERE has_human_correction = TRUE
		  AND created_at >= $1
	`, since).Scan(&agreeing, &total)
	return agreeing, total, err
}

// TaskAccuracyStat is the per-task-type routing accuracy over a time window.
type TaskAccuracyStat struct {
	Accuracy float64 // successful / total, [0, 1]
	Samples  int     // total feedback rows for the task type
}

// GetTaskTypeAccuracy returns routing accuracy grouped by task_type since the
// given time. Used by the confidence adjuster to dampen confidence for task
// types where AUTO classification has been unreliable.
func (dao *FeedbackLogDAO) GetTaskTypeAccuracy(ctx context.Context, since time.Time) (map[string]TaskAccuracyStat, error) {
	if dao.pool == nil {
		return map[string]TaskAccuracyStat{}, nil
	}
	rows, err := dao.pool.Query(ctx, `
		SELECT task_type,
		       SUM(CASE WHEN success THEN 1 ELSE 0 END)::float / NULLIF(COUNT(*), 0),
		       COUNT(*)
		FROM routing_feedback_log
		WHERE created_at >= $1
		GROUP BY task_type
	`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]TaskAccuracyStat)
	for rows.Next() {
		var taskType string
		var stat TaskAccuracyStat
		if err := rows.Scan(&taskType, &stat.Accuracy, &stat.Samples); err != nil {
			return nil, err
		}
		out[taskType] = stat
	}
	return out, rows.Err()
}
