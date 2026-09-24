package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/annotation"
)

// nullStringPtr converts a nullable text column to an omitempty-friendly
// pointer (nil when the column was NULL).
func nullStringPtr(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	v := n.String
	return &v
}

// AnnotationSample represents a sample for annotation with optional annotation data.
// auto_route_selections is privacy-minimal: it records the chosen model and
// decision snapshot but no prompt token counts, streaming/vision flags, or
// region — those struct fields stay in the payload (zero-valued) to keep the
// API shape stable for the web UI.
type AnnotationSample struct {
	RequestID    string  `json:"request_id"`
	ModelName    string  `json:"model_name"`
	TaskType     string  `json:"task_type"`
	PromptTokens int     `json:"prompt_tokens"`
	IsStreaming  bool    `json:"is_streaming"`
	HasVision    bool    `json:"has_vision"`
	Region       string  `json:"region"`
	Profile      string  `json:"profile"`
	AutoProvider string  `json:"auto_provider"`
	Confidence   float64 `json:"confidence"`
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
	// Strategy echoes the active sampling strategy v2 (P0⑤, 2026-09-24):
	// "" (recent) for the default recency feed, "disagreement" for
	// classifier-disagreement-first ordering, "stratified" for the
	// task_type × confidence-bucket quota sample.
	Strategy string `json:"strategy,omitempty"`
}

// CreateAnnotationRequest is the request body for creating an annotation
type CreateAnnotationRequest struct {
	RequestID     string `json:"request_id"`
	HumanProvider string `json:"human_provider"`
	IsCorrect     bool   `json:"is_correct"`
	Reason        string `json:"reason"`
	Annotator     string `json:"annotator"`
	// First-turn workbench labels (2026-09-14): the annotator's ground-truth
	// task type and model choice, persisted into annotation_metadata JSONB
	// (zero-migration). Empty = not provided (batch/CSV callers unaffected).
	TaskType string `json:"task_type,omitempty"`
	Model    string `json:"model,omitempty"`
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

// FirstTurnSample is one row of the first-turn annotation workbench
// (2026-09-14): a session's turn_no=1 request joined with its auto-route
// decision and human annotation (if any). Session-level display fields
// (title / client) come from public.sessions (migration 467 title column);
// the exact task-type ground truth the annotator enters lands in
// training_human_annotations.annotation_metadata.
type FirstTurnSample struct {
	SessionID   string    `json:"session_id"`
	RequestID   string    `json:"request_id"`
	Ts          time.Time `json:"ts"`
	Title       *string   `json:"title"`
	Client      *string   `json:"client"`
	TaskType    string    `json:"task_type"`
	ChosenModel string    `json:"chosen_model"`
	Confidence  *float64  `json:"confidence"`
	StatusCode  *int      `json:"status_code"`
	Success     *bool     `json:"success"`
	LatencyMs   *int      `json:"latency_ms"`
	TotalTurns  *int      `json:"total_turns"`
	// Annotation fields (populated if annotated)
	HumanTaskType *string    `json:"human_task_type,omitempty"`
	HumanModel    *string    `json:"human_model,omitempty"`
	HumanProvider *string    `json:"human_provider,omitempty"`
	IsCorrect     *bool      `json:"is_correct,omitempty"`
	Reason        *string    `json:"reason,omitempty"`
	Annotator     *string    `json:"annotator,omitempty"`
	AnnotatedAt   *time.Time `json:"annotated_at,omitempty"`
}

// FirstTurnSamplesResponse contains paginated first-turn samples
type FirstTurnSamplesResponse struct {
	Samples []FirstTurnSample `json:"samples"`
	Total   int               `json:"total"`
}

// AnnotationStatsResponse wraps all statistics
type AnnotationStatsResponse struct {
	Overall     *annotation.AnnotationStats     `json:"overall"`
	ByProvider  []annotation.ProviderAccuracy   `json:"by_provider"`
	ByAnnotator []annotation.AnnotatorStats     `json:"by_annotator"`
	ByReason    []annotation.ReasonDistribution `json:"by_reason"`
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

	// 采样策略 v2（P0⑤，2026-09-24，v2 规划 §4.5）：
	//   recent（默认，向后兼容） — ts 倒序分页，现状行为不变；
	//   disagreement             — 分歧采样：LLM 兜底接管过的行（classifier
	//                             <> 'heuristic'）优先入队。启发式自身的结论
	//                             未落库，classifier 列的 llm/v3 值即"兜底
	//                             改判"的可判定证据（规划口径：分歧可判）；
	//   stratified               — 分层抽样：task_type × 置信度桶配额，
	//                             per_strata（默认 5，≤20），避免高频类垄断。
	strategy, perStrata, err := parseSamplingParams(query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Build the query
	offset := (page - 1) * size
	samples, total, err := querySamples(ctx, pool, samplesQueryOpts{
		startDate:       startDate,
		endDate:         endDate,
		minConfidence:   minConfidence,
		maxConfidence:   maxConfidence,
		annotatedFilter: annotatedFilter,
		annotatorFilter: annotatorFilter,
		strategy:        strategy,
		perStrata:       perStrata,
		limit:           size,
		offset:          offset,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to query samples: %v", err), http.StatusInternalServerError)
		return
	}

	resp := SamplesResponse{
		Samples: samples,
		Total:   total,
	}
	if strategy != "recent" {
		resp.Strategy = strategy
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// parseSamplingParams parses the sampling strategy v2 query parameters
// (P0⑤): strategy (recent|disagreement|stratified, default recent) and
// per_strata (stratified quota, default 5, capped at 20).
func parseSamplingParams(query url.Values) (strategy string, perStrata int, err error) {
	strategy = query.Get("strategy")
	if strategy == "" {
		strategy = "recent"
	}
	if strategy != "recent" && strategy != "disagreement" && strategy != "stratified" {
		return "", 0, fmt.Errorf("invalid strategy %q (want recent|disagreement|stratified)", strategy)
	}
	perStrata, _ = strconv.Atoi(query.Get("per_strata"))
	if perStrata < 1 {
		perStrata = 5
	}
	if perStrata > 20 {
		perStrata = 20
	}
	return strategy, perStrata, nil
}

// samplesQueryOpts is the fully-parsed input of querySamples.
type samplesQueryOpts struct {
	startDate, endDate           string
	minConfidence, maxConfidence float64
	annotatedFilter              *bool
	annotatorFilter              string
	// strategy: recent (default) | disagreement | stratified — see
	// handleAnnotationSamples for semantics.
	strategy string
	// perStrata is the per task_type × confidence-bucket quota for the
	// stratified strategy (ignored otherwise).
	perStrata     int
	limit, offset int
}

// buildSamplesWhere renders the shared WHERE clause. Returns the clause
// (without the leading WHERE keyword), the positional args, and the next
// free placeholder index.
func buildSamplesWhere(o samplesQueryOpts) (string, []any, int) {
	var conditions []string
	var args []any
	argIdx := 1

	if o.startDate != "" {
		conditions = append(conditions, fmt.Sprintf("ars.ts >= $%d", argIdx))
		args = append(args, o.startDate)
		argIdx++
	}
	if o.endDate != "" {
		conditions = append(conditions, fmt.Sprintf("ars.ts <= $%d", argIdx))
		args = append(args, o.endDate)
		argIdx++
	}

	conditions = append(conditions, fmt.Sprintf("ars.confidence >= $%d AND ars.confidence <= $%d", argIdx, argIdx+1))
	args = append(args, o.minConfidence, o.maxConfidence)
	argIdx += 2

	if o.annotatedFilter != nil {
		if *o.annotatedFilter {
			conditions = append(conditions, "tha.request_id IS NOT NULL")
		} else {
			conditions = append(conditions, "tha.request_id IS NULL")
		}
	}

	if o.annotatorFilter != "" {
		conditions = append(conditions, fmt.Sprintf("tha.annotator = $%d", argIdx))
		args = append(args, o.annotatorFilter)
		argIdx++
	}

	return strings.Join(conditions, " AND "), args, argIdx
}

// sampleColumns is the shared projection of every sampling strategy.
const sampleColumns = `
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
	tha.annotated_at`

const samplesFromJoin = `
	FROM auto_route_selections_all ars
	LEFT JOIN training_human_annotations tha ON tha.request_id = ars.request_id`

// buildSamplesDataSQL renders the data query for the given strategy. Pure
// function (unit-tested shape); whereSQL comes from buildSamplesWhere and
// argIdx is the next free placeholder index.
func buildSamplesDataSQL(o samplesQueryOpts, whereSQL string, argIdx int) string {
	sql, _ := buildSamplesDataSQLAndArgs(o, whereSQL, argIdx, nil)
	return sql
}

// buildSamplesDataSQLAndArgs is buildSamplesDataSQL plus the trailing args
// (stratified: quota; recent/disagreement: limit+offset).
func buildSamplesDataSQLAndArgs(o samplesQueryOpts, whereSQL string, argIdx int, args []any) (string, []any) {
	if o.strategy == "stratified" {
		// 分层抽样：每 (task_type, 桶) 取最近 perStrata 行，桶内同样让
		// 分歧行（llm/v3 兜底）排在前面，标注边际价值最大化。外层显式
		// 列投影（不含 sample_rn），Scan 列序与其它策略完全一致。
		dataSQL := fmt.Sprintf(`
			SELECT ranked.request_id, ranked.model_name, ranked.task_type, ranked.profile,
				ranked.auto_provider, ranked.confidence, ranked.human_label, ranked.is_correct,
				ranked.reason, ranked.annotator, ranked.annotated_at
			FROM (
				SELECT
					ars.request_id,
					ars.chosen_model AS model_name,
					ars.task_type,
					ars.profile,
					ars.chosen_model AS auto_provider,
					ars.confidence,
					tha.human_label,
					tha.is_correct,
					tha.annotation_reason AS reason,
					tha.annotator,
					tha.annotated_at,
					row_number() OVER (
						PARTITION BY ars.task_type,
							CASE WHEN ars.confidence >= 0.85 THEN 4
							     WHEN ars.confidence >= 0.70 THEN 3
							     WHEN ars.confidence >= 0.50 THEN 2
							     ELSE 1 END
						ORDER BY (ars.classifier <> 'heuristic') DESC, ars.ts DESC
					) AS sample_rn
					%s
					%s
				) ranked
				WHERE sample_rn <= $%d
				ORDER BY ranked.task_type, ranked.sample_rn
			`, samplesFromJoin, whereSQL, argIdx)
		return dataSQL, append(args, o.perStrata)
	}

	orderBy := "ars.ts DESC"
	if o.strategy == "disagreement" {
		orderBy = "(ars.classifier <> 'heuristic') DESC, ars.ts DESC"
	}

	// Data query — columns per the real auto_route_selections schema
	// (migrations 478/658): chosen_model is both the displayed model name
	// and the auto label the annotator confirms or corrects; ts orders and
	// date-filters.
	dataSQL := fmt.Sprintf(`
		SELECT%s
		%s
		%s
		ORDER BY %s
		LIMIT $%d OFFSET $%d
	`, sampleColumns, samplesFromJoin, whereSQL, orderBy, argIdx, argIdx+1)
	return dataSQL, append(args, o.limit, o.offset)
}

// querySamples fetches samples with optional annotation data.
//
// Strategy dispatch (P0⑤ sampling v2):
//   - recent:       ORDER BY ts DESC + LIMIT/OFFSET paging (legacy shape).
//   - disagreement: same filter/paging, but rows whose classifier was taken
//     over by the LLM fallback (classifier <> 'heuristic') sort first — those
//     are the requests where the two classifiers disagreed or the heuristic
//     was unconfident, i.e. the highest-value annotation targets.
//   - stratified:   window-function quota sample, task_type × confidence
//     bucket (bands 0.85+/0.70+/0.50+/rest aligned to the LLM fallback
//     threshold 0.70 and the strong-confidence band). Paging offsets don't
//     apply (one deterministic shot); total reports the selected row count.
func querySamples(ctx context.Context, pool *pgxpool.Pool, o samplesQueryOpts) ([]AnnotationSample, int, error) {
	whereClause, args, argIdx := buildSamplesWhere(o)
	whereSQL := ""
	if whereClause != "" {
		whereSQL = "WHERE " + whereClause
	}

	var total int
	if o.strategy != "stratified" {
		countSQL := fmt.Sprintf(`
			SELECT COUNT(*)
			FROM auto_route_selections_all ars
			LEFT JOIN training_human_annotations tha ON tha.request_id = ars.request_id
			%s
		`, whereSQL)
		if err := pool.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("count query failed: %w", err)
		}
	}

	dataSQL, args := buildSamplesDataSQLAndArgs(o, whereSQL, argIdx, args)

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

	if o.strategy == "stratified" {
		total = len(samples)
	}

	return samples, total, nil
}

// firstTurnQueryer abstracts the SQL surface queryFirstTurnSamples needs so
// both *pgxpool.Pool (tests) and pgx.Tx (RLS-scoped tx) satisfy it.
type firstTurnQueryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// handleAnnotationFirstTurnSamples handles GET
// /api/admin/annotations/first-turn-samples — the first-turn annotation
// workbench list. One row per session's turn_no=1 request that carries an
// auto-route decision. RLS-scoped tables (session_turns / sessions) are read
// inside a tenant GUC tx: tenant callers see their own tenant, super admins
// see all tenants.
func (h *Handler) handleAnnotationFirstTurnSamples(w http.ResponseWriter, r *http.Request) {
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

	// Default time range: today (UTC), half-open [day 00:00, day+1 00:00).
	startStr := query.Get("start_date")
	endStr := query.Get("end_date")
	startTS, endTS, startDay, endDay, derr := resolveFirstTurnDateRange(startStr, endStr)
	if derr != nil {
		http.Error(w, derr.Error(), http.StatusBadRequest)
		return
	}

	// Optional filters (applied only when present).
	var annotatedFilter *bool
	if query.Has("annotated") {
		val := query.Get("annotated") == "true"
		annotatedFilter = &val
	}
	var minConfidence, maxConfidence *float64
	if query.Has("min_confidence") {
		if v, perr := strconv.ParseFloat(query.Get("min_confidence"), 64); perr == nil {
			minConfidence = &v
		}
	}
	if query.Has("max_confidence") {
		if v, perr := strconv.ParseFloat(query.Get("max_confidence"), 64); perr == nil {
			maxConfidence = &v
		}
	}

	opts := firstTurnQueryOptions{
		StartTS:       startTS,
		EndTS:         endTS,
		StartDay:      startDay,
		EndDay:        endDay,
		TaskType:      strings.TrimSpace(query.Get("task_type")),
		Model:         strings.TrimSpace(query.Get("model")),
		HumanTaskType: strings.TrimSpace(query.Get("human_task_type")),
		Annotated:     annotatedFilter,
		MinConfidence: minConfidence,
		MaxConfidence: maxConfidence,
		Limit:         size,
		Offset:        (page - 1) * size,
		// Tenant predicate is belt-and-braces on top of the RLS GUC set by
		// the surrounding tx (mirrors tenantLogsClause, but qualified to ft).
		TenantID: GetTenantID(r),
		Scoped:   !IsSuperAdminOrLegacy(r),
	}

	var (
		samples []FirstTurnSample
		total   int
		qerr    error
	)
	run := func(tx pgx.Tx) error {
		samples, total, qerr = queryFirstTurnSamples(ctx, tx, opts)
		return qerr
	}
	// RLS-scoped tables (session_turns / sessions) require the GUC tx.
	if opts.Scoped {
		qerr = withTenantTx(ctx, pool, opts.TenantID, run)
	} else {
		qerr = withAllTenantReadOnlyTx(ctx, pool, run)
	}
	if qerr != nil {
		http.Error(w, fmt.Sprintf("Failed to query first-turn samples: %v", qerr), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(FirstTurnSamplesResponse{Samples: samples, Total: total})
}

// resolveFirstTurnDateRange parses the optional YYYY-MM-DD query dates into a
// half-open UTC range. Empty inputs default to "today" (UTC): [00:00, +1d
// 00:00). endDay doubles as the inclusive partition_date pruning upper bound.
func resolveFirstTurnDateRange(startStr, endStr string) (startTS, endTS, startDay, endDay time.Time, err error) {
	if startStr == "" {
		startStr = time.Now().UTC().Format("2006-01-02")
	}
	if endStr == "" {
		endStr = startStr
	}
	startDay, err = time.Parse("2006-01-02", startStr)
	if err != nil {
		return time.Time{}, time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("invalid start_date: want YYYY-MM-DD")
	}
	endDay, err = time.Parse("2006-01-02", endStr)
	if err != nil {
		return time.Time{}, time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("invalid end_date: want YYYY-MM-DD")
	}
	return startDay, endDay.AddDate(0, 0, 1), startDay, endDay, nil
}

// firstTurnQueryOptions carries the parsed query parameters for
// queryFirstTurnSamples.
type firstTurnQueryOptions struct {
	StartTS       time.Time
	EndTS         time.Time // exclusive
	StartDay      time.Time // partition pruning lower bound (date)
	EndDay        time.Time // partition pruning upper bound (date, inclusive)
	TaskType      string
	Model         string
	HumanTaskType string
	Annotated     *bool
	MinConfidence *float64
	MaxConfidence *float64
	Limit         int
	Offset        int
	TenantID      string
	Scoped        bool // true = restrict to TenantID
}

// firstTurnFromClause is the shared FROM block: first turns + auto-route
// decision + session snapshot + annotation. LATERAL LIMIT 1 keeps the row
// count equal to ft's (dedupes replayed auto_route_selections rows and
// multi-day sessions snapshots). Placeholders $1..$4 are the fixed
// ft-subquery args (startTS, endTS exclusive, startDay, endDay inclusive).
const firstTurnFromClause = `
		FROM (
			SELECT st.session_id, st.request_id, st.tenant_id, st.ts,
			       st.status_code, st.success, st.latency_ms
			FROM public.session_turns st
			WHERE st.turn_no = 1
			  AND st.ts >= $1 AND st.ts < $2
			  AND st.partition_date >= $3 AND st.partition_date <= $4
		) ft
		JOIN LATERAL (
			SELECT a.task_type, a.chosen_model, a.confidence
			FROM public.auto_route_selections_all a
			WHERE a.request_id = ft.request_id
			ORDER BY a.ts DESC
			LIMIT 1
		) ars ON true
		LEFT JOIN LATERAL (
			SELECT sc.title, sc.last_request_summary, sc.client_type, sc.total_turns
			FROM public.sessions sc
			WHERE sc.session_id = ft.session_id
			ORDER BY sc.created_at DESC
			LIMIT 1
		) s ON true
		LEFT JOIN public.training_human_annotations tha ON tha.request_id = ft.request_id
	`

// buildFirstTurnWhere renders the parameterized outer WHERE clause. args
// starts as the 4 fixed ft-subquery values; appended filter args keep the
// positional order used by both count and data queries.
func buildFirstTurnWhere(opts firstTurnQueryOptions) (string, []any) {
	args := []any{opts.StartTS, opts.EndTS, opts.StartDay, opts.EndDay}
	conditions := []string{"TRUE"}
	bind := func(fragment string, v any) {
		args = append(args, v)
		conditions = append(conditions, fmt.Sprintf(fragment, len(args)))
	}

	if opts.Scoped && opts.TenantID != "" && opts.TenantID != "default" {
		bind("ft.tenant_id = $%d", opts.TenantID)
	}
	if opts.TaskType != "" {
		bind("ars.task_type = $%d", opts.TaskType)
	}
	if opts.Model != "" {
		bind("ars.chosen_model = $%d", opts.Model)
	}
	if opts.HumanTaskType != "" {
		bind("tha.annotation_metadata->>'task_type' = $%d", opts.HumanTaskType)
	}
	if opts.Annotated != nil {
		if *opts.Annotated {
			conditions = append(conditions, "tha.request_id IS NOT NULL")
		} else {
			conditions = append(conditions, "tha.request_id IS NULL")
		}
	}
	if opts.MinConfidence != nil {
		bind("ars.confidence >= $%d", *opts.MinConfidence)
	}
	if opts.MaxConfidence != nil {
		bind("ars.confidence <= $%d", *opts.MaxConfidence)
	}
	return "WHERE " + strings.Join(conditions, " AND "), args
}

// queryFirstTurnSamples lists one row per session's first turn with its
// auto-route decision and human annotation.
func queryFirstTurnSamples(ctx context.Context, q firstTurnQueryer, opts firstTurnQueryOptions) ([]FirstTurnSample, int, error) {
	whereClause, args := buildFirstTurnWhere(opts)

	countSQL := "SELECT COUNT(*)" + firstTurnFromClause + whereClause
	var total int
	if err := q.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count query failed: %w", err)
	}

	dataSQL := `
		SELECT
			ft.session_id,
			ft.request_id,
			ft.ts,
			COALESCE(s.title, s.last_request_summary),
			s.client_type,
			ars.task_type,
			ars.chosen_model,
			ars.confidence,
			ft.status_code,
			ft.success,
			ft.latency_ms,
			s.total_turns,
			tha.human_label,
			tha.annotation_metadata->>'task_type',
			tha.annotation_metadata->>'model',
			tha.is_correct,
			tha.annotation_reason,
			tha.annotator,
			tha.annotated_at
	` + firstTurnFromClause + whereClause + fmt.Sprintf(`
		ORDER BY ft.ts DESC, ft.request_id
		LIMIT $%d OFFSET $%d
	`, len(args)+1, len(args)+2)

	args = append(args, opts.Limit, opts.Offset)

	rows, err := q.Query(ctx, dataSQL, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("data query failed: %w", err)
	}
	defer rows.Close()

	samples := make([]FirstTurnSample, 0)
	for rows.Next() {
		// Nullable columns scan into sql.Null* (portable across pgx and the
		// pgxmock test double, neither of which agree on **T semantics).
		var (
			title, clientType, humanProvider, humanTaskType, humanModel, reason, annotator sql.NullString
			confidence                                                                     sql.NullFloat64
			statusCode, latencyMs, totalTurns                                              sql.NullInt64
			success, isCorrect                                                             sql.NullBool
			annotatedAt                                                                    sql.NullTime
		)
		var s FirstTurnSample
		if err := rows.Scan(
			&s.SessionID,
			&s.RequestID,
			&s.Ts,
			&title,
			&clientType,
			&s.TaskType,
			&s.ChosenModel,
			&confidence,
			&statusCode,
			&success,
			&latencyMs,
			&totalTurns,
			&humanProvider,
			&humanTaskType,
			&humanModel,
			&isCorrect,
			&reason,
			&annotator,
			&annotatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan failed: %w", err)
		}
		s.Title = nullStringPtr(title)
		s.Client = nullStringPtr(clientType)
		if confidence.Valid {
			s.Confidence = &confidence.Float64
		}
		if statusCode.Valid {
			v := int(statusCode.Int64)
			s.StatusCode = &v
		}
		if success.Valid {
			s.Success = &success.Bool
		}
		if latencyMs.Valid {
			v := int(latencyMs.Int64)
			s.LatencyMs = &v
		}
		if totalTurns.Valid {
			v := int(totalTurns.Int64)
			s.TotalTurns = &v
		}
		s.HumanProvider = nullStringPtr(humanProvider)
		s.HumanTaskType = nullStringPtr(humanTaskType)
		s.HumanModel = nullStringPtr(humanModel)
		if isCorrect.Valid {
			s.IsCorrect = &isCorrect.Bool
		}
		s.Reason = nullStringPtr(reason)
		s.Annotator = nullStringPtr(annotator)
		if annotatedAt.Valid {
			t := annotatedAt.Time
			s.AnnotatedAt = &t
		}
		samples = append(samples, s)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("rows iteration failed: %w", err)
	}
	return samples, total, nil
}

// buildAnnotationMetadata renders the first-turn workbench labels
// (task_type / model) as an annotation_metadata JSONB payload. Returns an
// empty string when both are empty (legacy callers), and a user-facing error
// when a value exceeds its length cap. The JSON is passed as a string (not
// []byte) so pgx encodes it as text for PG to cast into jsonb — a []byte
// would be sent as bytea and rejected with 22P02.
func buildAnnotationMetadata(taskType, model string) (any, error) {
	if taskType == "" && model == "" {
		return nil, nil
	}
	if len(taskType) > 64 {
		return nil, fmt.Errorf("task_type too long (max 64 chars)")
	}
	if len(model) > 128 {
		return nil, fmt.Errorf("model too long (max 128 chars)")
	}
	meta := map[string]string{}
	if taskType != "" {
		meta["task_type"] = taskType
	}
	if model != "" {
		meta["model"] = model
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("marshal annotation metadata: %v", err)
	}
	return string(b), nil
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

	// Validate fields. The first-turn workbench form has no provider field
	// ("vendor doesn't matter"): an explicit model doubles as human_label.
	if req.HumanProvider == "" && req.Model != "" {
		req.HumanProvider = req.Model
	}
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

	// First-turn workbench labels (2026-09-14): merge task_type / model into
	// annotation_metadata JSONB. Zero-migration: the column already exists.
	metadataArg, merr := buildAnnotationMetadata(req.TaskType, req.Model)
	if merr != nil {
		http.Error(w, merr.Error(), http.StatusBadRequest)
		return
	}

	// Insert annotation
	sql := `
		INSERT INTO training_human_annotations (
			request_id, auto_label, auto_confidence, human_label, is_correct,
			annotation_reason, annotator, annotated_at, annotation_metadata
		) VALUES ($1, $2, $3, $4, $5, $6, $7, NOW(), $8)
		ON CONFLICT (request_id) DO NOTHING
	`

	result, err := pool.Exec(ctx, sql, req.RequestID, autoLabel, autoConfidence, req.HumanProvider, req.IsCorrect, req.Reason, req.Annotator, metadataArg)
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
