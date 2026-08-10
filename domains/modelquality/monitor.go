package modelquality

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// MonitorConfig 监控配置
type MonitorConfig struct {
	// 定时检测配置
	EnableScheduled  bool          `json:"enable_scheduled"`
	ScheduleInterval time.Duration `json:"schedule_interval"`  // 定时检测间隔(如每天)
	UseLiteBenchmark bool          `json:"use_lite_benchmark"` // 是否使用精简版测试

	// 异常触发配置
	EnableAnomalyTrigger bool    `json:"enable_anomaly_trigger"` // 启用异常触发
	ErrorRateThreshold   float64 `json:"error_rate_threshold"`   // 错误率阈值(如0.3表示30%)
	LatencyThreshold     int64   `json:"latency_threshold_ms"`   // 延迟阈值(毫秒)

	// 测试目标
	TargetModels []ModelTarget `json:"target_models"` // 要监控的模型列表

	// 告警配置
	AlertOnQualityDrop   bool    `json:"alert_on_quality_drop"`  // 质量下降时告警
	QualityDropThreshold float64 `json:"quality_drop_threshold"` // 质量下降阈值(如5表示下降5%)

	// 2026-08-10: 按凭据节点测试（直连节点，绕过网关）。默认 false，向后兼容。
	EnablePerNodeTesting bool `json:"enable_per_node_testing,omitempty"`
}

// ModelTarget 监控目标模型
type ModelTarget struct {
	Provider       string `json:"provider"`                  // 供应商
	ModelName      string `json:"model_name"`                // 模型名称
	Alias          string `json:"alias"`                     // 别名(用于显示)
	CredentialID   int    `json:"credential_id,omitempty"`   // 2026-08-10: 直连特定凭据节点；0=经网关
	CanonicalModel string `json:"canonical_model,omitempty"` // 目录模型名（聚合用）
	Label          string `json:"label,omitempty"`           // 凭据标签（展示用）
}

// QualityMonitor 质量监控器
type QualityMonitor struct {
	config   *MonitorConfig
	executor BenchmarkExecutor
	storage  MonitorStorage
	alerter  Alerter

	mu         sync.RWMutex
	running    bool
	stopChan   chan struct{}
	lastScores map[string]*QualityScore // key: provider:model
}

// MonitorStorage 监控数据存储接口
type MonitorStorage interface {
	// SaveReport 保存测试报告
	SaveReport(ctx context.Context, report *BenchmarkReport) error

	// SaveScore 保存质量评分
	SaveScore(ctx context.Context, score *QualityScore) error

	// GetLatestScore 获取最新评分（credentialID=0 表示经网关聚合）
	GetLatestScore(ctx context.Context, provider string, modelName string, credentialID int) (*QualityScore, error)

	// GetScoreHistory 获取历史评分（credentialID=0 表示经网关聚合）
	GetScoreHistory(ctx context.Context, provider string, modelName string, credentialID int, limit int) ([]*QualityScore, error)

	// ListAllScores 列出所有已落盘评分（聚合用：模型目录平均智商 / 单节点智商）。
	// limit<=0 表示不限制；按时间倒序。
	ListAllScores(ctx context.Context, limit int) ([]*QualityScore, error)
}

// Alerter 告警接口
type Alerter interface {
	// Alert 发送告警
	Alert(ctx context.Context, level string, title string, message string) error
}

// NewQualityMonitor 创建质量监控器
func NewQualityMonitor(config *MonitorConfig, executor BenchmarkExecutor, storage MonitorStorage, alerter Alerter) *QualityMonitor {
	return &QualityMonitor{
		config:     config,
		executor:   executor,
		storage:    storage,
		alerter:    alerter,
		lastScores: make(map[string]*QualityScore),
		stopChan:   make(chan struct{}),
	}
}

// Start 启动监控
func (m *QualityMonitor) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return fmt.Errorf("monitor already running")
	}
	m.running = true
	m.mu.Unlock()

	// 初始化:加载上次的评分
	m.loadLastScores(ctx)

	// 启动定时检测
	if m.config.EnableScheduled {
		go m.scheduledCheckLoop(ctx)
	}

	slog.Info("quality monitor started", "target_models", len(m.config.TargetModels))
	return nil
}

// Stop 停止监控
func (m *QualityMonitor) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.running {
		return
	}

	m.running = false
	close(m.stopChan)
	slog.Info("quality monitor stopped")
}

// scheduledCheckLoop 定时检测循环
func (m *QualityMonitor) scheduledCheckLoop(ctx context.Context) {
	// 2026-08-07 audit fix: guard against ScheduleInterval == 0, which causes
	// time.NewTicker to panic. The main.go production path floors mqIntervalHours
	// to >=1 (main.go:2803), but UpdateConfig or future callers could pass 0.
	// Floor to 24h instead of panicking.
	interval := m.config.ScheduleInterval
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// 启动时立即执行一次
	m.runScheduledCheck(ctx)

	for {
		select {
		case <-ticker.C:
			m.runScheduledCheck(ctx)
		case <-m.stopChan:
			return
		case <-ctx.Done():
			return
		}
	}
}

// runScheduledCheck 执行定时检测
func (m *QualityMonitor) runScheduledCheck(ctx context.Context) {
	// 2026-08-07 audit fix: check ctx.Done() at the start so Stop() can
	// interrupt before the (multi-minute, synchronous) benchmark loop starts.
	// Without this, Stop() during the startup benchmark waits indefinitely
	// because the ctx passed from main.go was context.Background() (never
	// cancels) — now it's WithCancel and Stop() calls cancel().
	select {
	case <-ctx.Done():
		slog.Info("quality monitor: scheduled check cancelled before start")
		return
	default:
	}

	slog.Info("quality monitor: starting scheduled check",
		"time", time.Now().Format(time.RFC3339))

	// 选择测试套件
	var suite *BenchmarkSuite
	if m.config.UseLiteBenchmark {
		suite = GetMMLULiteSuite()
	} else {
		suite = GetMMLUFullSuite()
	}

	// 对每个目标模型执行测试
	for _, target := range m.config.TargetModels {
		m.testModel(ctx, target, suite, "scheduled")
	}
}

// TriggerAnomalyCheck 触发异常检测(外部调用)
func (m *QualityMonitor) TriggerAnomalyCheck(ctx context.Context, provider string, modelName string, reason string) error {
	if !m.config.EnableAnomalyTrigger {
		return fmt.Errorf("anomaly trigger is disabled")
	}

	slog.Info("quality monitor: anomaly triggered",
		"provider", provider, "model", modelName, "reason", reason)

	// 找到目标模型
	var target *ModelTarget
	for _, t := range m.config.TargetModels {
		if t.Provider == provider && t.ModelName == modelName {
			target = &t
			break
		}
	}

	if target == nil {
		return fmt.Errorf("model %s:%s not in monitoring targets", provider, modelName)
	}

	// 使用精简测试快速检测
	suite := GetMMLULiteSuite()
	return m.testModel(ctx, *target, suite, "anomaly:"+reason)
}

// normalizeTriggerKind maps internal trigger labels to the stable DB enum.
func normalizeTriggerKind(trigger string) string {
	switch {
	case strings.HasPrefix(trigger, "anomaly"):
		return "anomaly"
	case trigger == "on_demand":
		return "on_demand"
	default:
		return "scheduled"
	}
}

// testModel 测试单个模型
func (m *QualityMonitor) testModel(ctx context.Context, target ModelTarget, suite *BenchmarkSuite, trigger string) error {
	// 2026-08-07 audit fix: check ctx.Done() before launching the (potentially
	// multi-minute) benchmark so Stop() can interrupt between models in the loop.
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	displayName := target.Alias
	if displayName == "" {
		displayName = fmt.Sprintf("%s:%s", target.Provider, target.ModelName)
	}

	slog.Info("quality monitor: testing model",
		"model", displayName, "trigger", trigger)

	// 执行测试
	report, err := m.executor.Execute(ctx, target.ModelName, target.Provider, suite)
	if err != nil {
		slog.Warn("quality monitor: test failed",
			"model", displayName, "error", err)
		return err
	}

	// The trigger is persisted with the score so the audit history can
	// distinguish scheduled, manual, and anomaly-triggered runs.
	report.TriggerKind = normalizeTriggerKind(trigger)

	// 保存报告
	if err := m.storage.SaveReport(ctx, report); err != nil {
		slog.Warn("quality monitor: failed to save report", "error", err)
	}

	// 计算评分
	calculator := &ScoreCalculator{}
	score := calculator.CalculateScore(report)

	// 保存评分
	if err := m.storage.SaveScore(ctx, score); err != nil {
		slog.Warn("quality monitor: failed to save score", "error", err)
	}

	// 输出结果
	slog.Info("quality monitor: test completed",
		"model", displayName,
		"accuracy", score.Accuracy,
		"stability", score.Stability,
		"latency_p95", score.Latency,
		"overall_score", score.OverallScore,
		"grade", score.Grade)

	// 检查质量下降
	if m.config.AlertOnQualityDrop {
		m.checkQualityDrop(ctx, target, score)
	}

	// 更新缓存
	key := nodeKey(target.Provider, target.ModelName, target.CredentialID)
	m.mu.Lock()
	m.lastScores[key] = score
	m.mu.Unlock()

	return nil
}

// checkQualityDrop 检查质量下降
func (m *QualityMonitor) checkQualityDrop(ctx context.Context, target ModelTarget, newScore *QualityScore) {
	key := nodeKey(target.Provider, target.ModelName, target.CredentialID)

	m.mu.RLock()
	lastScore, exists := m.lastScores[key]
	m.mu.RUnlock()

	if !exists || lastScore == nil {
		return
	}

	// 计算准确率下降
	accuracyDrop := lastScore.Accuracy - newScore.Accuracy
	if accuracyDrop >= m.config.QualityDropThreshold {
		displayName := target.Alias
		if displayName == "" {
			displayName = fmt.Sprintf("%s:%s", target.Provider, target.ModelName)
		}

		alertMsg := fmt.Sprintf(
			"模型 %s 质量下降检测!\n"+
				"准确率: %.2f%% -> %.2f%% (下降 %.2f%%)\n"+
				"综合评分: %.2f (%s) -> %.2f (%s)\n"+
				"建议: 检查供应商模型是否更新或降级",
			displayName,
			lastScore.Accuracy, newScore.Accuracy, accuracyDrop,
			lastScore.OverallScore, lastScore.Grade, newScore.OverallScore, newScore.Grade,
		)

		if err := m.alerter.Alert(ctx, "warning", "LLM模型质量下降告警", alertMsg); err != nil {
			slog.Warn("quality monitor: failed to send alert", "error", err)
		}
	}
}

// loadLastScores 加载上次的评分
func (m *QualityMonitor) loadLastScores(ctx context.Context) {
	for _, target := range m.config.TargetModels {
		score, err := m.storage.GetLatestScore(ctx, target.Provider, target.ModelName, target.CredentialID)
		if err != nil {
			continue
		}
		if score != nil {
			key := nodeKey(target.Provider, target.ModelName, target.CredentialID)
			m.lastScores[key] = score
		}
	}
}

// GetCurrentScores 获取当前所有评分
func (m *QualityMonitor) GetCurrentScores() map[string]*QualityScore {
	m.mu.RLock()
	defer m.mu.RUnlock()

	scores := make(map[string]*QualityScore)
	for k, v := range m.lastScores {
		scores[k] = v
	}
	return scores
}
