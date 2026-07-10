// bg/session_health_worker.go — Session Health Score Background Worker
//
// 定期扫描未计算健康分的会话，自动计算并写入。
// Ref: docs/session-management-analytics-plan.md 第 11.4.4 节
//
// 设计：
//   - 每 60 分钟扫一次（health 是非实时指标，1h 周期足够）
//   - 扫描条件：last_request_at < now - 1h AND health_score IS NULL
//   - 批量处理，每批最多 100 条（避免单次过长）
//   - 调用 admin.ComputeHealth() 计算分数，UPDATE session_summaries
//   - Prometheus 指标：session_health_computed_total{source="worker"}
//
// 接入点：cmd/gateway/main.go 在 init bg services 时构造 + Start。

package bg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/moduleexec"
	"github.com/kaixuan/llm-gateway-go/domains/moduleregistry"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// sessionRecord 会话健康计算所需字段
type sessionRecord struct {
	sessionKey              string
	tenantID                string
	requestCount            int
	successCount            int
	errorCount              int
	avgLatencyMs            int
	modelSwitchCount        int
	complianceIssuesCount   int
	promptInjectionDetected bool
	piiDetected             bool
	toxicOutputDetected     bool
}

var (
	sessionHealthWorkerComputedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_session_health_worker_computed_total",
			Help: "Total sessions processed by health worker",
		},
		[]string{"status"}, // success / error
	)
)

// SessionHealthWorker 后台定期计算会话健康分
//
// 2026-07-10: 集成模块执行器，相同会话的健康分会被缓存（TTL 1小时），
// 避免重复计算。
type SessionHealthWorker struct {
	db       *pgxpool.Pool
	executor *moduleexec.Executor // 模块执行记录器（可选）
	cancel   context.CancelFunc
	done     chan struct{}
}

// NewSessionHealthWorker 构造 worker
func NewSessionHealthWorker(db *pgxpool.Pool) *SessionHealthWorker {
	return &SessionHealthWorker{
		db:   db,
		done: make(chan struct{}),
	}
}

// SetExecutor 注入模块执行器
func (w *SessionHealthWorker) SetExecutor(exec *moduleexec.Executor) {
	w.executor = exec
}

// Start 启动后台 goroutine。Stop 之前不能重复 Start。
func (w *SessionHealthWorker) Start(ctx context.Context) {
	ctx, w.cancel = context.WithCancel(ctx)
	go w.run(ctx)
	slog.Info("session health worker started", "interval", "60m")
}

// Stop 取消并等待 goroutine 退出。
func (w *SessionHealthWorker) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
	<-w.done
}

func (w *SessionHealthWorker) run(ctx context.Context) {
	defer close(w.done)

	ticker := time.NewTicker(60 * time.Minute)
	defer ticker.Stop()

	// 启动后等待 5 分钟再首次执行（避免冷启时与其他 worker 竞争）
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Minute):
		w.sweep(ctx)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.sweep(ctx)
		}
	}
}

func (w *SessionHealthWorker) sweep(ctx context.Context) {
	sweepCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	// 1. 查询需要计算健康分的会话（批量，限制 100 条）
	query := `
		SELECT session_key, tenant_id,
		       request_count, success_count, error_count,
		       avg_latency_ms, model_switch_count,
		       compliance_issues_count,
		       prompt_injection_detected, pii_detected, toxic_output_detected
		FROM session_summaries
		WHERE last_request_at < NOW() - INTERVAL '1 hour'
		  AND health_score IS NULL
		ORDER BY last_request_at DESC
		LIMIT 100
	`

	rows, err := w.db.Query(sweepCtx, query)
	if err != nil {
		slog.Warn("session health worker query failed", "error", err)
		return
	}
	defer rows.Close()

	var sessions []sessionRecord
	for rows.Next() {
		var s sessionRecord
		if err := rows.Scan(
			&s.sessionKey, &s.tenantID,
			&s.requestCount, &s.successCount, &s.errorCount,
			&s.avgLatencyMs, &s.modelSwitchCount,
			&s.complianceIssuesCount,
			&s.promptInjectionDetected, &s.piiDetected, &s.toxicOutputDetected,
		); err != nil {
			slog.Warn("session health worker scan failed", "error", err)
			continue
		}
		sessions = append(sessions, s)
	}

	if len(sessions) == 0 {
		return
	}

	slog.Info("session health worker processing batch", "count", len(sessions))

	// 2. 批量计算并更新
	successCount := 0
	for _, s := range sessions {
		if err := w.computeAndUpdate(sweepCtx, s); err != nil {
			slog.Warn("session health compute failed",
				"gw_session_id", s.sessionKey,
				"error", err)
			sessionHealthWorkerComputedTotal.WithLabelValues("error").Inc()
		} else {
			successCount++
			sessionHealthWorkerComputedTotal.WithLabelValues("success").Inc()
		}
	}

	slog.Info("session health worker batch completed",
		"total", len(sessions),
		"success", successCount,
		"failed", len(sessions)-successCount)
}

func (w *SessionHealthWorker) computeAndUpdate(ctx context.Context, s sessionRecord) error {
	// 2026-07-10: 通过执行器计算健康分，结果会被缓存（TTL 1小时）
	if w.executor != nil {
		return w.computeWithExecutor(ctx, s)
	}
	return w.computeDirectly(ctx, s)
}

// computeWithExecutor 通过执行器计算健康分（带缓存）
func (w *SessionHealthWorker) computeWithExecutor(ctx context.Context, s sessionRecord) error {
	params := sessionRecordToParams(s)

	execResult, err := w.executor.CheckAndExecute(
		ctx, s.sessionKey, s.tenantID,
		moduleregistry.ModuleSessionHealth,
		params, 0, // 使用模块默认 TTL（1小时）
		func(ctx context.Context) (*moduleexec.ExecuteResult, error) {
			health := w.doCompute(s)
			// 写入数据库
			if err := w.updateHealth(ctx, s.sessionKey, health); err != nil {
				return nil, err
			}
			return &moduleexec.ExecuteResult{
				ResultSummary: map[string]interface{}{
					"health_score": health.HealthScore,
					"health_grade": health.HealthGrade,
					"outcome":      health.Outcome,
				},
			}, nil
		},
	)
	if err != nil {
		return err
	}

	// 如果是从缓存获取的，不需要再次更新数据库
	if execResult.FromCache {
		return nil
	}
	return nil
}

// computeDirectly 直接计算健康分（不经过执行器）
func (w *SessionHealthWorker) computeDirectly(ctx context.Context, s sessionRecord) error {
	health := w.doCompute(s)
	return w.updateHealth(ctx, s.sessionKey, health)
}

// doCompute 执行健康分计算逻辑
func (w *SessionHealthWorker) doCompute(s sessionRecord) sessionHealthResult {
	summary := struct {
		RequestCount            int
		ErrorCount              int
		AvgLatencyMs            int
		ModelSwitchCount        int
		ComplianceIssuesCount   int
		PromptInjectionDetected bool
		PIIDetected             bool
		ToxicOutputDetected     bool
	}{
		RequestCount:            s.requestCount,
		ErrorCount:              s.errorCount,
		AvgLatencyMs:            s.avgLatencyMs,
		ModelSwitchCount:        s.modelSwitchCount,
		ComplianceIssuesCount:   s.complianceIssuesCount,
		PromptInjectionDetected: s.promptInjectionDetected,
		PIIDetected:             s.piiDetected,
		ToxicOutputDetected:     s.toxicOutputDetected,
	}
	config := defaultHealthScoreConfig()
	return computeHealthFromFields(summary, config)
}

// updateHealth 更新数据库中的健康分
func (w *SessionHealthWorker) updateHealth(ctx context.Context, sessionKey string, health sessionHealthResult) error {
	updateQuery := `
		UPDATE session_summaries
		SET health_score = $1,
		    health_grade = $2,
		    outcome = $3,
		    last_health_at = NOW(),
		    updated_at = NOW()
		WHERE session_key = $4
	`
	_, err := w.db.Exec(ctx, updateQuery,
		health.HealthScore,
		health.HealthGrade,
		health.Outcome,
		sessionKey,
	)
	return err
}

// sessionRecordToParams 将会话记录转换为缓存参数
func sessionRecordToParams(s sessionRecord) map[string]interface{} {
	data, _ := json.Marshal(struct {
		RequestCount            int  `json:"rc"`
		ErrorCount              int  `json:"ec"`
		AvgLatencyMs            int  `json:"al"`
		ModelSwitchCount        int  `json:"ms"`
		ComplianceIssuesCount   int  `json:"ci"`
		PromptInjectionDetected bool `json:"pi"`
		PIIDetected             bool `json:"pii"`
		ToxicOutputDetected     bool `json:"tox"`
	}{
		RequestCount:            s.requestCount,
		ErrorCount:              s.errorCount,
		AvgLatencyMs:            s.avgLatencyMs,
		ModelSwitchCount:        s.modelSwitchCount,
		ComplianceIssuesCount:   s.complianceIssuesCount,
		PromptInjectionDetected: s.promptInjectionDetected,
		PIIDetected:             s.piiDetected,
		ToxicOutputDetected:     s.toxicOutputDetected,
	})
	h := sha256.Sum256(data)
	return map[string]interface{}{
		"fields_hash": hex.EncodeToString(h[:])[:16],
	}
}

// ── Health Score Logic (duplicated to avoid circular dependency) ──────

type healthScoreConfig struct {
	ErrorEndedPenalty        int
	AbandonedPenalty         int
	PerErrorPenalty          int
	PerErrorCap              int
	PerCompliancePenalty     int
	PerComplianceCap         int
	HighLatencyThresholdMs   int
	HighLatencyPenalty       int
	ModelSwitchThreshold     int
	ModelSwitchPenalty       int
	PromptInjectionPenalty   int
	PIIPenalty               int
	ToxicOutputPenalty       int
	SensitivePenaltyCap      int
}

func defaultHealthScoreConfig() healthScoreConfig {
	return healthScoreConfig{
		ErrorEndedPenalty:      30,
		AbandonedPenalty:       15,
		PerErrorPenalty:        3,
		PerErrorCap:            30,
		PerCompliancePenalty:   10,
		PerComplianceCap:       30,
		HighLatencyThresholdMs: 5000,
		HighLatencyPenalty:     15,
		ModelSwitchThreshold:   3,
		ModelSwitchPenalty:     10,
		PromptInjectionPenalty: 20,
		PIIPenalty:             15,
		ToxicOutputPenalty:     15,
		SensitivePenaltyCap:    30,
	}
}

type sessionHealthResult struct {
	HealthScore  int
	HealthGrade  string
	Outcome      string
}

func computeHealthFromFields(summary interface{}, config healthScoreConfig) sessionHealthResult {
	// 使用类型断言获取字段
	type healthSummary interface {
		getRequestCount() int
		getErrorCount() int
		getAvgLatencyMs() int
		getModelSwitchCount() int
		getComplianceIssuesCount() int
		getPromptInjectionDetected() bool
		getPIIDetected() bool
		getToxicOutputDetected() bool
	}

	// 直接从传入的 struct 提取字段（Go 1.18+ 通过反射或类型断言）
	// 为简化，直接用结构体字段访问
	s := summary.(struct {
		RequestCount            int
		ErrorCount              int
		AvgLatencyMs            int
		ModelSwitchCount        int
		ComplianceIssuesCount   int
		PromptInjectionDetected bool
		PIIDetected             bool
		ToxicOutputDetected     bool
	})

	score := 100

	// 1. 会话以错误结束
	errorRate := 0.0
	if s.RequestCount > 0 {
		errorRate = float64(s.ErrorCount) / float64(s.RequestCount)
	}
	if s.ErrorCount > 0 && errorRate > 0.5 {
		score -= config.ErrorEndedPenalty
	}

	// 2. 会话被放弃
	if s.RequestCount <= 1 {
		score -= config.AbandonedPenalty
	}

	// 3. 错误扣分（封顶）
	errorDeduction := min(s.ErrorCount*config.PerErrorPenalty, config.PerErrorCap)
	score -= errorDeduction

	// 4. 合规问题扣分（封顶）
	complianceDeduction := min(s.ComplianceIssuesCount*config.PerCompliancePenalty, config.PerComplianceCap)
	score -= complianceDeduction

	// 5. 高延迟
	if s.AvgLatencyMs > config.HighLatencyThresholdMs {
		score -= config.HighLatencyPenalty
	}

	// 6. 频繁模型切换
	if s.ModelSwitchCount > config.ModelSwitchThreshold {
		score -= config.ModelSwitchPenalty
	}

	// 7. 提示注入
	if s.PromptInjectionDetected {
		score -= config.PromptInjectionPenalty
	}

	// 8. PII / 毒性输出（封顶）
	sensitiveDeduction := 0
	if s.PIIDetected {
		sensitiveDeduction += config.PIIPenalty
	}
	if s.ToxicOutputDetected {
		sensitiveDeduction += config.ToxicOutputPenalty
	}
	sensitiveDeduction = min(sensitiveDeduction, config.SensitivePenaltyCap)
	score -= sensitiveDeduction

	// 下限 0
	if score < 0 {
		score = 0
	}

	// 等级映射
	grade := gradeFromScore(score)

	// 结果分类
	outcome := classifyOutcome(s.RequestCount, s.ErrorCount)

	return sessionHealthResult{
		HealthScore:  score,
		HealthGrade:  grade,
		Outcome:      outcome,
	}
}

func gradeFromScore(score int) string {
	switch {
	case score >= 90:
		return "A"
	case score >= 75:
		return "B"
	case score >= 60:
		return "C"
	case score >= 40:
		return "D"
	default:
		return "F"
	}
}

func classifyOutcome(requestCount, errorCount int) string {
	if requestCount == 0 {
		return "unknown"
	}

	errorRate := float64(errorCount) / float64(requestCount)

	if errorRate > 0.5 {
		return "error"
	}

	if requestCount <= 1 {
		return "abandoned"
	}

	if requestCount >= 2 {
		return "completed"
	}

	return "unknown"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
