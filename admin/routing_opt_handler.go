// routing_opt_handler.go — P2.2 Track C: routing-opt admin API。
//
// 端点（均走 h.admin 鉴权中间件，注册见 handler.go）：
//
//	GET /api/admin/routing-opt/stats       整体加权准确率 + 参数版本 + 标注利用数
//	GET /api/admin/routing-opt/accuracy    hours 窗口内按小时×任务类型聚合准确率
//	GET /api/admin/routing-opt/parameters  当前激活参数版本（JSON）
//
// 设计约束：
//   - handler 只依赖 DB pool（直接聚合 routing_feedback_log /
//     routing_optimization_state），不依赖内存 optimizer —— admin 包无需
//     import routingopt。
//   - pool 为 nil → 503；所有 DB 错误 → 500 + slog 日志，绝不 panic。
//   - SQL 注入防护硬要求：全部语句为包级常量、全参数化（$1 占位），
//     绝无用户输入拼进 SQL。hours 参数 clamp 为 int(1..720, 默认 24) 后
//     作为 $1 传入。
//   - 加权口径与 AdaptiveLearner 一致：(auto + 2×human) / (total + 2×human)，
//     人工标注（P2.1 ground truth）权重 ×2；无反馈样本时回退到
//     optimization_state.overall_accuracy。
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// routingOptDB 是 routing-opt 端点依赖的最小 DB 接口：*pgxpool.Pool 天然
// 满足；pgxmock.PgxPoolIface 也满足，便于无 Postgres 的单元测试。
type routingOptDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// routingOptDBOverride 是 pgxmock 测试注入 seam（与 beginApprovalTxOverride
// 同款模式）。生产路径永远返回 h.db，恒为 nil。
var routingOptDBOverride routingOptDB

// routingOptPool 解析本组端点使用的 DB pool。测试可注入 mock；生产环境
// 返回 h.db（可能为 nil → 调用方 503）。注意 typed-nil 陷阱：h.db == nil
// 时必须显式返回 nil 接口，否则接口非 nil 而 pool.Query 会 panic。
func (h *Handler) routingOptPool() routingOptDB {
	if routingOptDBOverride != nil {
		return routingOptDBOverride
	}
	if h.db == nil {
		return nil
	}
	return h.db
}

// =============================================================================
// SQL 常量 —— 全参数化，绝不拼接用户输入（静态检查见 *_test.go）
// =============================================================================

const (
	// routingOptStatsAutoSQL：auto 反馈计数（success 列，24h 滑动窗口）。
	routingOptStatsAutoSQL = `
		SELECT COALESCE(SUM(CASE WHEN success THEN 1 ELSE 0 END), 0),
		       COUNT(*)
		FROM routing_feedback_log
		WHERE created_at >= $1
	`

	// routingOptStatsHumanSQL：人工纠正计数（一致 = 预测命中人工 ground truth）。
	routingOptStatsHumanSQL = `
		SELECT COALESCE(SUM(CASE WHEN predicted_provider = correct_provider THEN 1 ELSE 0 END), 0),
		       COUNT(*)
		FROM routing_feedback_log
		WHERE has_human_correction = TRUE
		  AND created_at >= $1
	`

	// routingOptStatsStateSQL：最新激活参数版本（用于版本号 + 无样本时
	// 的持久化准确率回退）。
	routingOptStatsStateSQL = `
		SELECT version, overall_accuracy, updated_at
		FROM routing_optimization_state
		WHERE deactivated_at IS NULL
		ORDER BY activated_at DESC
		LIMIT 1
	`

	// routingOptAccuracySQL：小时 × 任务类型聚合（auto 计数 + 人工计数）。
	routingOptAccuracySQL = `
		SELECT date_trunc('hour', created_at),
		       task_type,
		       COALESCE(SUM(CASE WHEN success THEN 1 ELSE 0 END), 0),
		       COUNT(*),
		       COALESCE(SUM(CASE WHEN has_human_correction THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN has_human_correction AND predicted_provider = correct_provider THEN 1 ELSE 0 END), 0)
		FROM routing_feedback_log
		WHERE created_at >= $1
		GROUP BY 1, 2
		ORDER BY 1 DESC, 2
	`

	// routingOptParametersSQL：激活版本全参数（JSONB 原样透传）。
	routingOptParametersSQL = `
		SELECT version, classifier_weights, confidence_thresholds, recommender_weights,
		       exploration_rate, learning_rate, adaptation_window, overall_accuracy,
		       activated_at, created_by, notes
		FROM routing_optimization_state
		WHERE deactivated_at IS NULL
		ORDER BY activated_at DESC
		LIMIT 1
	`

	// routingOptMetricsSQL：5 分钟聚合表只读投影（R30 审计 P3 遗留债：
	// 该表此前写-only，GetAggregatedMetrics 零调用方——本端点是其首个
	// 生产读者，同时提供 30 天长窗口趋势而无需扫 raw feedback log）。
	// 过滤参数语义：$2/$3 为空串 = 不过滤（含 NULL 维度的 global/provider
	// 行一并返回）；非空 = 精确匹配。用户值只作为绑定参数，绝不进 SQL 文本。
	// LIMIT 是常量 2000 防护（30 天 × 288 桶/天 × 维度组合，正常远小于此）。
	routingOptMetricsSQL = `
		SELECT time_bucket, task_type, predicted_provider,
		       total_requests, successful_requests, failed_requests, accuracy_rate,
		       avg_confidence, avg_latency_ms, avg_cost,
		       p50_latency_ms, p95_latency_ms, p99_latency_ms,
		       human_corrections, human_accuracy_rate
		FROM routing_optimization_metrics
		WHERE time_bucket >= $1
		  AND ($2::text = '' OR task_type = $2)
		  AND ($3::text = '' OR predicted_provider = $3)
		ORDER BY time_bucket DESC, task_type NULLS LAST, predicted_provider NULLS LAST
		LIMIT 2000
	`
)

// hours 窗口 clamp 边界。
const (
	routingOptDefaultHours = 24
	routingOptMinHours     = 1
	routingOptMaxHours     = 720 // 30 天
)

// parseRoutingOptHours 解析 ?hours= 并 clamp 到 [1,720]，默认 24。
// 语义：非数字 / 0 / 负数 → 默认 24；超过 720 → 720。
// 用户输入永远不会进入 SQL 文本，只作为 $1 的时间参数间接生效。
func parseRoutingOptHours(raw string) int {
	h, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || h < routingOptMinHours {
		return routingOptDefaultHours
	}
	if h > routingOptMaxHours {
		return routingOptMaxHours
	}
	return h
}

// routingOptWeightedAccuracy 镜像 routingopt.WeightedAccuracy 口径：
// (auto + 2×human) / (total + 2×human)。无样本时 hasData=false。
func routingOptWeightedAccuracy(autoCorrect, autoTotal, humanCorrect, humanTotal int64) (accuracy float64, hasData bool) {
	denom := autoTotal + 2*humanTotal
	if denom <= 0 {
		return 0, false
	}
	num := autoCorrect + 2*humanCorrect
	if num > denom {
		num = denom
	}
	return float64(num) / float64(denom), true
}

// =============================================================================
// GET /api/admin/routing-opt/stats
// =============================================================================

// RoutingOptStatsResponse 是 routing-opt stats 端点的响应。
type RoutingOptStatsResponse struct {
	// OverallAccuracy 加权准确率 (auto+2×human)/(total+2×human)，[0,1]。
	OverallAccuracy float64 `json:"overall_accuracy"`
	// AccuracySource 标记口径来源：weighted_feedback（反馈聚合）|
	// persisted_state（无样本回退 optimization_state.overall_accuracy）| none。
	AccuracySource string `json:"accuracy_source"`
	// ParameterVersion 当前激活参数版本（无激活行时为 0）。
	ParameterVersion int `json:"parameter_version"`
	// HumanAnnotationsUsed 窗口内人工标注利用数。
	HumanAnnotationsUsed int64 `json:"human_annotations_used"`
	WindowHours          int   `json:"window_hours"`
	// AutoSamples / HumanSamples 窗口内 auto 反馈与人工纠正行数。
	AutoSamples    int64      `json:"auto_samples"`
	HumanSamples   int64      `json:"human_samples"`
	StateUpdatedAt *time.Time `json:"state_updated_at,omitempty"`
}

// handleRoutingOptStats handles GET /api/admin/routing-opt/stats
func (h *Handler) handleRoutingOptStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	db := h.routingOptPool()
	if db == nil {
		http.Error(w, "Database not available", http.StatusServiceUnavailable)
		return
	}
	ctx := r.Context()

	const windowHours = routingOptDefaultHours
	since := time.Now().Add(-windowHours * time.Hour)

	var autoCorrect, autoTotal int64
	if err := db.QueryRow(ctx, routingOptStatsAutoSQL, since).Scan(&autoCorrect, &autoTotal); err != nil {
		slog.ErrorContext(ctx, "admin/routing-opt: auto accuracy query failed", "err", err)
		http.Error(w, "Failed to query routing feedback stats", http.StatusInternalServerError)
		return
	}

	var humanCorrect, humanTotal int64
	if err := db.QueryRow(ctx, routingOptStatsHumanSQL, since).Scan(&humanCorrect, &humanTotal); err != nil {
		slog.ErrorContext(ctx, "admin/routing-opt: human correction query failed", "err", err)
		http.Error(w, "Failed to query routing feedback stats", http.StatusInternalServerError)
		return
	}

	var version int
	var stateAccuracy *float64
	var stateUpdatedAt *time.Time
	if err := db.QueryRow(ctx, routingOptStatsStateSQL).Scan(&version, &stateAccuracy, &stateUpdatedAt); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.ErrorContext(ctx, "admin/routing-opt: optimization state query failed", "err", err)
		http.Error(w, "Failed to query routing optimization state", http.StatusInternalServerError)
		return
	}

	accuracy, hasData := routingOptWeightedAccuracy(autoCorrect, autoTotal, humanCorrect, humanTotal)
	accuracySource := "weighted_feedback"
	if !hasData {
		if stateAccuracy != nil {
			accuracy = *stateAccuracy
			accuracySource = "persisted_state"
		} else {
			accuracySource = "none"
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(RoutingOptStatsResponse{
		OverallAccuracy:      accuracy,
		AccuracySource:       accuracySource,
		ParameterVersion:     version,
		HumanAnnotationsUsed: humanTotal,
		WindowHours:          windowHours,
		AutoSamples:          autoTotal,
		HumanSamples:         humanTotal,
		StateUpdatedAt:       stateUpdatedAt,
	})
}

// =============================================================================
// GET /api/admin/routing-opt/accuracy?hours=24
// =============================================================================

// RoutingOptAccuracyBucket 是一个 (hour, task_type) 聚合桶。
type RoutingOptAccuracyBucket struct {
	Hour         time.Time `json:"hour"`
	TaskType     string    `json:"task_type"`
	Accuracy     float64   `json:"accuracy"` // (auto + 2×human) / (total + 2×human)
	Samples      int64     `json:"samples"`
	HumanSamples int64     `json:"human_samples"`
}

// RoutingOptAccuracyResponse 是 accuracy 端点的响应。
type RoutingOptAccuracyResponse struct {
	Hours   int                        `json:"hours"`
	Since   time.Time                  `json:"since"`
	Buckets []RoutingOptAccuracyBucket `json:"buckets"`
}

// handleRoutingOptAccuracy handles GET /api/admin/routing-opt/accuracy?hours=24
func (h *Handler) handleRoutingOptAccuracy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	db := h.routingOptPool()
	if db == nil {
		http.Error(w, "Database not available", http.StatusServiceUnavailable)
		return
	}
	ctx := r.Context()

	// hours clamp 为 int 后只作为 $1 时间参数，绝不进 SQL 文本。
	hours := parseRoutingOptHours(r.URL.Query().Get("hours"))
	since := time.Now().Add(-time.Duration(hours) * time.Hour)

	rows, err := db.Query(ctx, routingOptAccuracySQL, since)
	if err != nil {
		slog.ErrorContext(ctx, "admin/routing-opt: accuracy aggregation query failed", "hours", hours, "err", err)
		http.Error(w, "Failed to query routing accuracy", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	buckets := make([]RoutingOptAccuracyBucket, 0)
	for rows.Next() {
		var b RoutingOptAccuracyBucket
		var correct, total, humanCorrect, humanTotal int64
		if err := rows.Scan(&b.Hour, &b.TaskType, &correct, &total, &humanCorrect, &humanTotal); err != nil {
			slog.ErrorContext(ctx, "admin/routing-opt: accuracy row scan failed", "err", err)
			http.Error(w, "Failed to read routing accuracy rows", http.StatusInternalServerError)
			return
		}
		b.Accuracy, _ = routingOptWeightedAccuracy(correct, total, humanCorrect, humanTotal)
		b.Samples = total
		b.HumanSamples = humanTotal
		buckets = append(buckets, b)
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(ctx, "admin/routing-opt: accuracy rows iteration failed", "err", err)
		http.Error(w, "Failed to read routing accuracy rows", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(RoutingOptAccuracyResponse{
		Hours:   hours,
		Since:   since,
		Buckets: buckets,
	})
}

// =============================================================================
// GET /api/admin/routing-opt/parameters
// =============================================================================

// RoutingOptParametersResponse 是激活参数版本的 JSON 投影
// （JSONB 列原样透传，避免 admin 包重复定义权重 schema）。
type RoutingOptParametersResponse struct {
	Version              int             `json:"version"`
	ClassifierWeights    json.RawMessage `json:"classifier_weights"`
	ConfidenceThresholds json.RawMessage `json:"confidence_thresholds"`
	RecommenderWeights   json.RawMessage `json:"recommender_weights"`
	ExplorationRate      float64         `json:"exploration_rate"`
	LearningRate         float64         `json:"learning_rate"`
	AdaptationWindow     int             `json:"adaptation_window"`
	OverallAccuracy      *float64        `json:"overall_accuracy"`
	ActivatedAt          time.Time       `json:"activated_at"`
	CreatedBy            string          `json:"created_by"`
	Notes                *string         `json:"notes"`
}

// handleRoutingOptParameters handles GET /api/admin/routing-opt/parameters
func (h *Handler) handleRoutingOptParameters(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	db := h.routingOptPool()
	if db == nil {
		http.Error(w, "Database not available", http.StatusServiceUnavailable)
		return
	}
	ctx := r.Context()

	var resp RoutingOptParametersResponse
	var classifierJSON, thresholdsJSON, recommenderJSON []byte
	err := db.QueryRow(ctx, routingOptParametersSQL).Scan(
		&resp.Version, &classifierJSON, &thresholdsJSON, &recommenderJSON,
		&resp.ExplorationRate, &resp.LearningRate, &resp.AdaptationWindow,
		&resp.OverallAccuracy, &resp.ActivatedAt, &resp.CreatedBy, &resp.Notes,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "No active optimization state", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.ErrorContext(ctx, "admin/routing-opt: parameters query failed", "err", err)
		http.Error(w, "Failed to query routing optimization parameters", http.StatusInternalServerError)
		return
	}

	// JSONB 列 schema 上 NOT NULL，防御性兜底：NULL → "{}"。
	resp.ClassifierWeights = rawJSONOrDefault(classifierJSON)
	resp.ConfidenceThresholds = rawJSONOrDefault(thresholdsJSON)
	resp.RecommenderWeights = rawJSONOrDefault(recommenderJSON)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// rawJSONOrDefault 空字节 → "{}"，否则原样返回（供 json.RawMessage）。
func rawJSONOrDefault(b []byte) json.RawMessage {
	if len(strings.TrimSpace(string(b))) == 0 {
		return json.RawMessage("{}")
	}
	return json.RawMessage(b)
}

// =============================================================================
// GET /api/admin/routing-opt/metrics?hours=24&task_type=&provider=
// =============================================================================

// RoutingOptMetricsRow 是聚合表一行（5 分钟桶）的直接投影。
// TaskType/PredictedProvider 为 NULL 时表示 GROUPING SETS 的聚合维度：
// 双 NULL = 全局行，单 NULL = 按另一维度聚合的行（与 migration 670 契约一致）。
type RoutingOptMetricsRow struct {
	TimeBucket time.Time `json:"time_bucket"`
	TaskType   *string   `json:"task_type"`
	Provider   *string   `json:"predicted_provider"`

	TotalRequests      int64 `json:"total_requests"`
	SuccessfulRequests int64 `json:"successful_requests"`
	FailedRequests     int64 `json:"failed_requests"`
	AccuracyRate       *float64 `json:"accuracy_rate"`

	AvgConfidence *float64 `json:"avg_confidence"`
	AvgLatencyMs  *int     `json:"avg_latency_ms"`
	AvgCost       *float64 `json:"avg_cost"`

	P50LatencyMs *int `json:"p50_latency_ms"`
	P95LatencyMs *int `json:"p95_latency_ms"`
	P99LatencyMs *int `json:"p99_latency_ms"`

	HumanCorrections  int64    `json:"human_corrections"`
	HumanAccuracyRate *float64 `json:"human_accuracy_rate"`
}

// RoutingOptMetricsResponse 是 metrics 端点的响应。
type RoutingOptMetricsResponse struct {
	Hours int                    `json:"hours"`
	Since time.Time              `json:"since"`
	Rows  []RoutingOptMetricsRow `json:"rows"`
	// Truncated 为 true 时结果被 LIMIT 2000 截断（收窄 hours 或加过滤重查）。
	Truncated bool `json:"truncated"`
}

// handleRoutingOptMetrics handles GET /api/admin/routing-opt/metrics
//
// R30 审计 P3 遗留债处置（docs/audit/2026-09-16-r30-24h-audit-round.md §四）：
// 聚合表首个生产读者。数据来源是 bg/routing_metrics_aggregator 每 5 分钟
// 重算的聚合桶（含人工标注×2 加权），保留期 30 天与 hours 上限 720 一致，
// 支持长窗口趋势查询而无需聚合 raw feedback log。
func (h *Handler) handleRoutingOptMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	db := h.routingOptPool()
	if db == nil {
		http.Error(w, "Database not available", http.StatusServiceUnavailable)
		return
	}
	ctx := r.Context()

	hours := parseRoutingOptHours(r.URL.Query().Get("hours"))
	since := time.Now().Add(-time.Duration(hours) * time.Hour)
	taskType := strings.TrimSpace(r.URL.Query().Get("task_type"))
	provider := strings.TrimSpace(r.URL.Query().Get("provider"))

	rows, err := db.Query(ctx, routingOptMetricsSQL, since, taskType, provider)
	if err != nil {
		slog.ErrorContext(ctx, "admin/routing-opt: metrics query failed", "hours", hours, "err", err)
		http.Error(w, "Failed to query routing optimization metrics", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	resp := RoutingOptMetricsResponse{
		Hours: hours,
		Since: since,
		Rows:  make([]RoutingOptMetricsRow, 0),
	}
	for rows.Next() {
		var row RoutingOptMetricsRow
		if err := rows.Scan(
			&row.TimeBucket, &row.TaskType, &row.Provider,
			&row.TotalRequests, &row.SuccessfulRequests, &row.FailedRequests, &row.AccuracyRate,
			&row.AvgConfidence, &row.AvgLatencyMs, &row.AvgCost,
			&row.P50LatencyMs, &row.P95LatencyMs, &row.P99LatencyMs,
			&row.HumanCorrections, &row.HumanAccuracyRate,
		); err != nil {
			slog.ErrorContext(ctx, "admin/routing-opt: metrics row scan failed", "err", err)
			http.Error(w, "Failed to read routing optimization metrics", http.StatusInternalServerError)
			return
		}
		resp.Rows = append(resp.Rows, row)
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(ctx, "admin/routing-opt: metrics rows iteration failed", "err", err)
		http.Error(w, "Failed to read routing optimization metrics", http.StatusInternalServerError)
		return
	}
	// 命中 LIMIT 2000 时 pgx 不报错，只能靠行数推断（>= LIMIT 即视为截断）。
	resp.Truncated = len(resp.Rows) >= 2000

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
