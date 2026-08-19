package admin

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	gatewaymetrics "github.com/kaixuan/llm-gateway-go/metrics"
	"github.com/kaixuan/llm-gateway-go/settings"
)

const (
	statsShadowQueueSize = 32
	statsShadowWorkers   = 2
)

type statsSummaryValues struct {
	Requests     int64
	Success      int64
	Failures     int64
	HasOutcomes  bool
	PromptTokens int64
	Completion   int64
	TotalTokens  int64
	Credits      int64
	CostUSD      float64
	AvgLatencyMs float64
}

type statsShadowTask struct {
	endpoint string
	tenant   string
	start    time.Time
	end      time.Time
	legacy   statsSummaryValues
}

type statsShadowExecutor struct {
	db       *pgxpool.Pool
	queue    chan statsShadowTask
	sequence atomic.Uint64
	once     sync.Once
}

func newStatsShadowExecutor(db *pgxpool.Pool) *statsShadowExecutor {
	return &statsShadowExecutor{db: db, queue: make(chan statsShadowTask, statsShadowQueueSize)}
}

func (e *statsShadowExecutor) start() {
	e.once.Do(func() {
		for i := 0; i < statsShadowWorkers; i++ {
			go e.worker()
		}
	})
}

func (e *statsShadowExecutor) worker() {
	for task := range e.queue {
		started := time.Now()
		result := "query_error"
		if !completeUTCWindow(task.start, task.end) {
			result = "partial_window"
			gatewaymetrics.RecordStatsShadowComparison(task.endpoint, result)
			gatewaymetrics.ObserveStatsShadowDuration(task.endpoint, time.Since(started))
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), shadowTimeout())
		canonical, err := queryCanonicalSummary(ctx, e.db, task.start, task.end, task.tenant)
		deadlineExceeded := errors.Is(err, context.DeadlineExceeded)
		cancel()
		if err == nil {
			result = compareStatsSummaries(task.legacy, canonical)
			if result == "material_drift" {
				slog.Warn("stats shadow comparison drift",
					"endpoint", task.endpoint,
					"tenant_id", task.tenant,
					"diff_fields", statsSummaryMaterialDiffFields(task.legacy, canonical),
				)
			}
		} else if errors.Is(err, errStatsShadowDegraded) {
			result = "degraded"
		} else if deadlineExceeded {
			result = "timeout"
		}
		gatewaymetrics.RecordStatsShadowComparison(task.endpoint, result)
		gatewaymetrics.ObserveStatsShadowDuration(task.endpoint, time.Since(started))
	}
}

func (e *statsShadowExecutor) enqueue(task statsShadowTask) {
	if e == nil || e.db == nil || !statsShadowEnabled() || !statsShadowSample(e.sequence.Add(1)) {
		return
	}
	e.start()
	select {
	case e.queue <- task:
	default:
		gatewaymetrics.RecordStatsShadowDropped(task.endpoint)
	}
}

func statsShadowEnabled() bool {
	return settings.GetPlatformBool("stats.shadow_read.enabled", false)
}

func shadowTimeout() time.Duration {
	ms := settings.GetPlatformInt("stats.shadow_read.timeout_ms", 750)
	if ms < 100 {
		ms = 100
	}
	if ms > 5000 {
		ms = 5000
	}
	return time.Duration(ms) * time.Millisecond
}

func statsShadowSample(sequence uint64) bool {
	return statsShadowSamplePercent(settings.GetPlatformInt("stats.shadow_read.sample_percent", 0), sequence)
}

func statsShadowSamplePercent(percent int, sequence uint64) bool {
	if percent <= 0 {
		return false
	}
	if percent >= 100 {
		return true
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], sequence)
	hash := sha256.Sum256(buf[:])
	return binary.BigEndian.Uint64(hash[:8])%100 < uint64(percent)
}

var errStatsShadowDegraded = errors.New("stats shadow projection unavailable")

func completeUTCWindow(start, end time.Time) bool {
	start = start.UTC()
	end = end.UTC()
	return end.After(start) && start.Truncate(24*time.Hour).Equal(start) && end.Truncate(24*time.Hour).Equal(end)
}

func queryCanonicalSummary(ctx context.Context, db *pgxpool.Pool, start, end time.Time, tenant string) (statsSummaryValues, error) {
	start = start.UTC()
	end = end.UTC()
	if !end.After(start) {
		return statsSummaryValues{}, errors.New("stats shadow range must be positive")
	}
	where := "day_utc >= ($1 AT TIME ZONE 'UTC')::date AND day_utc < (($2 - INTERVAL '1 microsecond') AT TIME ZONE 'UTC')::date + 1 AND dimension_type = 'provider_model'"
	args := []any{start, end}
	if tenant != "" {
		where += " AND tenant_id = $3"
		args = append(args, tenant)
	}
	var out statsSummaryValues
	var latencyCount, latencySum int64
	err := db.QueryRow(ctx, `
		SELECT COALESCE(SUM(request_count),0), COALESCE(SUM(success_count),0),
		       COALESCE(SUM(failure_count),0), COALESCE(SUM(prompt_tokens),0),
		       COALESCE(SUM(completion_tokens),0), COALESCE(SUM(total_tokens),0),
		       COALESCE(SUM(credits_charged),0), COALESCE(SUM(cost_usd),0),
		       COALESCE(SUM(latency_count),0), COALESCE(SUM(latency_sum_ms),0)
		FROM stats_usage_daily WHERE `+where, args...).Scan(
		&out.Requests, &out.Success, &out.Failures, &out.PromptTokens,
		&out.Completion, &out.TotalTokens, &out.Credits, &out.CostUSD,
		&latencyCount, &latencySum)
	if err != nil {
		if isMissingStatsRelation(err) {
			return statsSummaryValues{}, errStatsShadowDegraded
		}
		return statsSummaryValues{}, err
	}
	if latencyCount > 0 {
		out.AvgLatencyMs = float64(latencySum) / float64(latencyCount)
	}
	return out, nil
}

func statsSummaryFromMap(values map[string]any) (statsSummaryValues, bool) {
	if values == nil {
		return statsSummaryValues{}, false
	}
	readInt := func(key string) (int64, bool) {
		value, ok := values[key]
		if !ok {
			return 0, false
		}
		switch typed := value.(type) {
		case int:
			return int64(typed), true
		case int64:
			return typed, true
		case float64:
			return int64(typed), true
		default:
			return 0, false
		}
	}
	readFloat := func(key string) (float64, bool) {
		value, ok := values[key]
		if !ok {
			return 0, false
		}
		switch typed := value.(type) {
		case float64:
			return typed, true
		case float32:
			return float64(typed), true
		case int:
			return float64(typed), true
		case int64:
			return float64(typed), true
		default:
			return 0, false
		}
	}
	requests, ok := readInt("total_requests")
	if !ok {
		return statsSummaryValues{}, false
	}
	out := statsSummaryValues{Requests: requests}
	if success, ok := readInt("success_count"); ok {
		out.Success = success
		if failure, ok := readInt("failure_count"); ok {
			out.Failures = failure
			out.HasOutcomes = true
		}
	}
	out.PromptTokens, _ = readInt("total_prompt_tokens")
	out.Completion, _ = readInt("total_completion_tokens")
	out.TotalTokens, _ = readInt("total_tokens")
	out.Credits, _ = readInt("total_credits_charged")
	out.CostUSD, _ = readFloat("total_cost_usd")
	out.AvgLatencyMs, _ = readFloat("avg_latency_ms")
	return out, true
}

func (h *Handler) enqueueStatsShadow(endpoint, tenant string, start, end time.Time, legacy statsSummaryValues) {
	if h != nil && h.statsShadowExecutor != nil {
		h.statsShadowExecutor.enqueue(statsShadowTask{endpoint: endpoint, tenant: tenant, start: start, end: end, legacy: legacy})
	}
}

func compareStatsSummaries(legacy, canonical statsSummaryValues) string {
	values := []struct {
		old, current, absolute, relative float64
	}{
		{float64(legacy.Requests), float64(canonical.Requests), 1, 0.01},
		{float64(legacy.PromptTokens), float64(canonical.PromptTokens), 10, 0.01},
		{float64(legacy.Completion), float64(canonical.Completion), 10, 0.01},
		{float64(legacy.TotalTokens), float64(canonical.TotalTokens), 10, 0.01},
		{float64(legacy.Credits), float64(canonical.Credits), 10, 0.01},
		{legacy.CostUSD, canonical.CostUSD, 0.01, 0.01},
		{legacy.AvgLatencyMs, canonical.AvgLatencyMs, 5, 0.02},
	}
	if legacy.HasOutcomes && canonical.HasOutcomes {
		values = append(values,
			struct {
				old, current, absolute, relative float64
			}{float64(legacy.Success), float64(canonical.Success), 1, 0.01},
			struct {
				old, current, absolute, relative float64
			}{float64(legacy.Failures), float64(canonical.Failures), 1, 0.01},
		)
	}
	for _, value := range values {
		if math.Abs(value.old-value.current) > value.absolute && relativeDifference(value.old, value.current) > value.relative {
			return "material_drift"
		}
	}
	for _, value := range values {
		if value.old != value.current {
			return "tolerated_drift"
		}
	}
	return "exact"
}

func statsSummaryMaterialDiffFields(legacy, canonical statsSummaryValues) []string {
	fields := make([]string, 0, 9)
	for _, value := range []struct {
		name                             string
		old, current, absolute, relative float64
	}{
		{"requests", float64(legacy.Requests), float64(canonical.Requests), 1, 0.01},
		{"success", float64(legacy.Success), float64(canonical.Success), 1, 0.01},
		{"failures", float64(legacy.Failures), float64(canonical.Failures), 1, 0.01},
		{"prompt_tokens", float64(legacy.PromptTokens), float64(canonical.PromptTokens), 10, 0.01},
		{"completion_tokens", float64(legacy.Completion), float64(canonical.Completion), 10, 0.01},
		{"total_tokens", float64(legacy.TotalTokens), float64(canonical.TotalTokens), 10, 0.01},
		{"credits_charged", float64(legacy.Credits), float64(canonical.Credits), 10, 0.01},
		{"cost_usd", legacy.CostUSD, canonical.CostUSD, 0.01, 0.01},
		{"avg_latency_ms", legacy.AvgLatencyMs, canonical.AvgLatencyMs, 5, 0.02},
	} {
		if math.Abs(value.old-value.current) > value.absolute && relativeDifference(value.old, value.current) > value.relative {
			fields = append(fields, value.name)
		}
	}
	return fields
}

func relativeDifference(old, current float64) float64 {
	denominator := math.Max(math.Abs(old), math.Abs(current))
	if denominator == 0 {
		return 0
	}
	return math.Abs(old-current) / denominator
}
