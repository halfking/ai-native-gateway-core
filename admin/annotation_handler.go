package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/annotation"
)

// AnnotationSample represents a sample for annotation with optional annotation data.
// auto_route_selections is privacy-minimal: it records the chosen model and
// decision snapshot but no prompt token counts, streaming/vision flags, or
// region — those struct fields stay in the payload (zero-valued) to keep the
// API shape stable for the web UI.
type AnnotationSample struct {
	RequestID     string  `json:"request_id"`
	ModelName     string  `json:"model_name"`
	TaskType      string  `json:"task_type"`
	PromptTokens  int     `json:"prompt_tokens"`
	IsStreaming   bool    `json:"is_streaming"`
	HasVision     bool    `json:"has_vision"`
	Region        string  `json:"region"`
	Profile       string  `json:"profile"`
	AutoProvider  string  `json:"auto_provider"`
	Confidence    float64 `json:"confidence"`
	// Annotation fields (populated if annotated)
	HumanProvider *string    `json:"human_provider,omitempty"`
	IsCorrect     *bool      `json:"is_correct,omitempty"`
	Reason        *string    `json:"reason,omitempty"`
	Annotator     *string    `json:"annotator,omitempty"`
	AnnotatedAt   *time.Time `json:"annotated_at,omitempty"`
}

// SamplesResponse contains paginated samples
type SamplesResponse struct {
	Samples []AnnotationSample `json:"samples"`
	Total   int                `json:"total"`
}

// CreateAnnotationRequest is the request body for creating an annotation
type CreateAnnotationRequest struct {
	RequestID     string `json:"request_id"`
	HumanProvider string `json:"human_provider"`
	IsCorrect     bool   `json:"is_correct"`
	Reason        string `json:"reason"`
	Annotator     string `json:"annotator"`
}

// BatchAnnotateRequest is the request body for batch annotation
type BatchAnnotateRequest struct {
	RequestIDs    []string `json:"request_ids"`
	HumanProvider string   `json:"human_provider"`
	IsCorrect     bool     `json:"is_correct"`
	Reason        string   `json:"reason"`
	Annotator     string   `json:"annotator"`
}

// BatchAnnotateResponse contains batch annotation results
type BatchAnnotateResponse struct {
	Success int      `json:"success"`
	Failed  int      `json:"failed"`
	Errors  []string `json:"errors,omitempty"`
}

// AnnotationStatsResponse wraps all statistics
type AnnotationStatsResponse struct {
	Overall    *annotation.AnnotationStats     `json:"overall"`
	ByProvider []annotation.ProviderAccuracy   `json:"by_provider"`
	ByAnnotator []annotation.AnnotatorStats    `json:"by_annotator"`
	ByReason   []annotation.ReasonDistribution `json:"by_reason"`
}

// handleAnnotationSamples handles GET /api/admin/annotations/samples
func (h *Handler) handleAnnotationSamples(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	pool := h.db
	if pool == nil {
		http.Error(w, "Database not available", http.StatusServiceUnavailable)
		return
	}

	// Parse query parameters
	query := r.URL.Query()
	page, _ := strconv.Atoi(query.Get("page"))
	if page < 1 {
		page = 1
	}
	size, _ := strconv.Atoi(query.Get("size"))
	if size < 1 {
		size = 50
	}
	if size > 200 {
		size = 200
	}

	startDate := query.Get("start_date")
	endDate := query.Get("end_date")
	minConfidence, _ := strconv.ParseFloat(query.Get("min_confidence"), 64)
	maxConfidence, _ := strconv.ParseFloat(query.Get("max_confidence"), 64)
	if maxConfidence == 0 {
		maxConfidence = 1.0
	}

	// Parse annotated filter (optional)
	var annotatedFilter *bool
	if query.Has("annotated") {
		val := query.Get("annotated") == "true"
		annotatedFilter = &val
	}

	annotatorFilter := query.Get("annotator")

	// Build the query
	offset := (page - 1) * size
	samples, total, err := querySamples(ctx, pool, startDate, endDate, minConfidence, maxConfidence, annotatedFilter, annotatorFilter, size, offset)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to query samples: %v", err), http.StatusInternalServerError)
		return
	}

	resp := SamplesResponse{
		Samples: samples,
		Total:   total,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// querySamples fetches samples with optional annotation data
func querySamples(
	ctx context.Context,
	pool *pgxpool.Pool,
	startDate, endDate string,
	minConfidence, maxConfidence float64,
	annotatedFilter *bool,
	annotatorFilter string,
	limit, offset int,
) ([]AnnotationSample, int, error) {
	// Build WHERE clauses
	var conditions []string
	var args []interface{}
	argIdx := 1

	if startDate != "" {
		conditions = append(conditions, fmt.Sprintf("ars.ts >= $%d", argIdx))
		args = append(args, startDate)
		argIdx++
	}
	if endDate != "" {
		conditions = append(conditions, fmt.Sprintf("ars.ts <= $%d", argIdx))
		args = append(args, endDate)
		argIdx++
	}

	conditions = append(conditions, fmt.Sprintf("ars.confidence >= $%d AND ars.confidence <= $%d", argIdx, argIdx+1))
	args = append(args, minConfidence, maxConfidence)
	argIdx += 2

	if annotatedFilter != nil {
		if *annotatedFilter {
			conditions = append(conditions, "tha.request_id IS NOT NULL")
		} else {
			conditions = append(conditions, "tha.request_id IS NULL")
		}
	}

	if annotatorFilter != "" {
		conditions = append(conditions, fmt.Sprintf("tha.annotator = $%d", argIdx))
		args = append(args, annotatorFilter)
		argIdx++
	}

	whereClause := "WHERE " + strings.Join(conditions, " AND ")

	// Count query
	countSQL := fmt.Sprintf(`
		SELECT COUNT(*)
		FROM auto_route_selections_all ars
		LEFT JOIN training_human_annotations tha ON tha.request_id = ars.request_id
		%s
	`, whereClause)

	var total int
	err := pool.QueryRow(ctx, countSQL, args...).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("count query failed: %w", err)
	}

	// Data query — columns per the real auto_route_selections schema
	// (migrations 478/658): chosen_model is both the displayed model name
	// and the auto label the annotator confirms or corrects; ts orders and
	// date-filters.
	dataSQL := fmt.Sprintf(`
		SELECT
			ars.request_id,
			ars.chosen_model,
			ars.task_type,
			ars.profile,
			ars.chosen_model,
			ars.confidence,
			tha.human_label,
			tha.is_correct,
			tha.annotation_reason AS reason,
			tha.annotator,
			tha.annotated_at
		FROM auto_route_selections_all ars
		LEFT JOIN training_human_annotations tha ON tha.request_id = ars.request_id
		%s
		ORDER BY ars.ts DESC
		LIMIT $%d OFFSET $%d
	`, whereClause, argIdx, argIdx+1)

	args = append(args, limit, offset)

	rows, err := pool.Query(ctx, dataSQL, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("data query failed: %w", err)
	}
	defer rows.Close()

	samples := make([]AnnotationSample, 0)
	for rows.Next() {
		var s AnnotationSample
		var humanProvider, reason, annotator *string
		var isCorrect *bool
		var annotatedAt *time.Time

		err := rows.Scan(
			&s.RequestID,
			&s.ModelName,
			&s.TaskType,
			&s.Profile,
			&s.AutoProvider,
			&s.Confidence,
			&humanProvider,
			&isCorrect,
			&reason,
			&annotator,
			&annotatedAt,
		)
		if err != nil {
			return nil, 0, fmt.Errorf("scan failed: %w", err)
		}

		// Populate annotation fields if present
		s.HumanProvider = humanProvider
		s.IsCorrect = isCorrect
		s.Reason = reason
		s.Annotator = annotator
		s.AnnotatedAt = annotatedAt

		samples = append(samples, s)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("rows iteration failed: %w", err)
	}

	return samples, total, nil
}

// lookupAutoSelection fetches the auto label (chosen model) and its
// confidence for a request. training_human_annotations.auto_label /
// auto_confidence are NOT NULL and the web UI only annotates samples that
// came from the samples list, so the values are derived here rather than
// trusted from the client.
func lookupAutoSelection(ctx context.Context, pool *pgxpool.Pool, requestID string) (string, float64, error) {
	var autoLabel string
	var confidence float64
	err := pool.QueryRow(ctx, `
		SELECT chosen_model, confidence
		FROM auto_route_selections_all
		WHERE request_id = $1
		ORDER BY ts DESC
		LIMIT 1
	`, requestID).Scan(&autoLabel, &confidence)
	if err != nil {
		return "", 0, err
	}
	return autoLabel, confidence, nil
}

// handleCreateAnnotation handles POST /api/admin/annotations
func (h *Handler) handleCreateAnnotation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	pool := h.db
	if pool == nil {
		http.Error(w, "Database not available", http.StatusServiceUnavailable)
		return
	}

	var req CreateAnnotationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	// Validate fields
	if req.RequestID == "" || req.HumanProvider == "" || req.Reason == "" || req.Annotator == "" {
		http.Error(w, "Missing required fields", http.StatusBadRequest)
		return
	}

	if !annotation.IsValidReason(req.Reason) {
		http.Error(w, fmt.Sprintf("Invalid reason: must be one of %v", annotation.ValidAnnotationReasons()), http.StatusBadRequest)
		return
	}

	autoLabel, autoConfidence, err := lookupAutoSelection(ctx, pool, req.RequestID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Unknown request_id: no auto_route_selections row to annotate"), http.StatusBadRequest)
		return
	}

	// Insert annotation
	sql := `
		INSERT INTO training_human_annotations (
			request_id, auto_label, auto_confidence, human_label, is_correct,
			annotation_reason, annotator, annotated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
		ON CONFLICT (request_id) DO NOTHING
	`

	result, err := pool.Exec(ctx, sql, req.RequestID, autoLabel, autoConfidence, req.HumanProvider, req.IsCorrect, req.Reason, req.Annotator)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to create annotation: %v", err), http.StatusInternalServerError)
		return
	}

	rowsAffected := result.RowsAffected()
	if rowsAffected == 0 {
		http.Error(w, "Annotation already exists for this request_id", http.StatusConflict)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Annotation created successfully",
	})
}

// handleBatchAnnotate handles POST /api/admin/annotations/batch
func (h *Handler) handleBatchAnnotate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	pool := h.db
	if pool == nil {
		http.Error(w, "Database not available", http.StatusServiceUnavailable)
		return
	}

	var req BatchAnnotateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	// Validate fields
	if len(req.RequestIDs) == 0 || req.HumanProvider == "" || req.Reason == "" || req.Annotator == "" {
		http.Error(w, "Missing required fields", http.StatusBadRequest)
		return
	}

	if !annotation.IsValidReason(req.Reason) {
		http.Error(w, fmt.Sprintf("Invalid reason: must be one of %v", annotation.ValidAnnotationReasons()), http.StatusBadRequest)
		return
	}

	// Batch insert
	success := 0
	failed := 0
	var errors []string

	sql := `
		INSERT INTO training_human_annotations (
			request_id, auto_label, auto_confidence, human_label, is_correct,
			annotation_reason, annotator, annotated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
		ON CONFLICT (request_id) DO NOTHING
	`

	for _, requestID := range req.RequestIDs {
		autoLabel, autoConfidence, err := lookupAutoSelection(ctx, pool, requestID)
		if err != nil {
			failed++
			errors = append(errors, fmt.Sprintf("%s: no auto_route_selections row", requestID))
			continue
		}

		result, err := pool.Exec(ctx, sql, requestID, autoLabel, autoConfidence, req.HumanProvider, req.IsCorrect, req.Reason, req.Annotator)
		if err != nil {
			failed++
			errors = append(errors, fmt.Sprintf("%s: %v", requestID, err))
			continue
		}

		if result.RowsAffected() > 0 {
			success++
		} else {
			failed++
			errors = append(errors, fmt.Sprintf("%s: already annotated", requestID))
		}
	}

	resp := BatchAnnotateResponse{
		Success: success,
		Failed:  failed,
		Errors:  errors,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleDeleteAnnotation handles DELETE /api/admin/annotations/{request_id}
func (h *Handler) handleDeleteAnnotation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	pool := h.db
	if pool == nil {
		http.Error(w, "Database not available", http.StatusServiceUnavailable)
		return
	}

	// Extract request_id from path
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/annotations/")
	requestID := strings.TrimSpace(path)
	if requestID == "" {
		http.Error(w, "Missing request_id", http.StatusBadRequest)
		return
	}

	sql := `DELETE FROM training_human_annotations WHERE request_id = $1`
	result, err := pool.Exec(ctx, sql, requestID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to delete annotation: %v", err), http.StatusInternalServerError)
		return
	}

	if result.RowsAffected() == 0 {
		http.Error(w, "Annotation not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
	})
}

// handleAnnotationStats handles GET /api/admin/annotations/stats
func (h *Handler) handleAnnotationStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	pool := h.db
	if pool == nil {
		http.Error(w, "Database not available", http.StatusServiceUnavailable)
		return
	}

	querier := annotation.NewStatsQuerier(pool)

	overall, err := querier.GetOverallStats(ctx)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get overall stats: %v", err), http.StatusInternalServerError)
		return
	}

	byProvider, err := querier.GetProviderAccuracy(ctx)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get provider accuracy: %v", err), http.StatusInternalServerError)
		return
	}

	byAnnotator, err := querier.GetAnnotatorStats(ctx)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get annotator stats: %v", err), http.StatusInternalServerError)
		return
	}

	byReason, err := querier.GetReasonDistribution(ctx)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get reason distribution: %v", err), http.StatusInternalServerError)
		return
	}

	resp := AnnotationStatsResponse{
		Overall:     overall,
		ByProvider:  byProvider,
		ByAnnotator: byAnnotator,
		ByReason:    byReason,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
