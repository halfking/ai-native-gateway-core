package modelquality

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MonitorConfig 监控配置
type MonitorConfig struct {
	// 定时检测配置
	EnableScheduled     bool          `json:"enable_scheduled"`
	ScheduleInterval    time.Duration `json:"schedule_interval"`     // 定时检测间隔(如每天)
	UseLiteBenchmark    bool          `json:"use_lite_benchmark"`    // 是否使用精简版测试
	
	// 异常触发配置
	EnableAnomalyTrigger bool    `json:"enable_anomaly_trigger"` // 启用异常触发
	ErrorRateThreshold   float64 `json:"error_rate_threshold"`   // 错误率阈值(如0.3表示30%)
	LatencyThreshold     int64   `json:"latency_threshold_ms"`   // 延迟阈值(毫秒)
	
	// 测试目标
	TargetModels []ModelTarget `json:"target_models"` // 要监控的模型列表
	
	// 告警配置
	AlertOnQualityDrop bool    `json:"alert_on_quality_drop"` // 质量下降时告警
	QualityDropThreshold float64 `json:"quality_drop_threshold"` // 质量下降阈值(如5表示下降5%)
}

// ModelTarget 监控目标模型
type ModelTarget struct {
	Provider  string `json:"provider"`   // 供应商
	ModelName string `json:"model_name"` // 模型名称
	Alias     string `json:"alias"`      // 别名(用于显示)
}

// QualityMonitor 质量监控器
type QualityMonitor struct {
	config    *MonitorConfig
	executor  BenchmarkExecutor
	storage   MonitorStorage
	alerter   Alerter
	
	mu            sync.RWMutex
	running       bool
	stopChan      chan struct{}
	lastScores    map[string]*QualityScore // key: provider:model
}

// MonitorStorage 监控数据存储接口
type MonitorStorage interface {
	// SaveReport 保存测试报告
	SaveReport(ctx context.Context, report *BenchmarkReport) error
	
	// SaveScore 保存质量评分
	SaveScore(ctx context.Context, score *QualityScore) error
	
	// GetLatestScore 获取最新评分
	GetLatestScore(ctx context.Context, provider string, modelName string) (*QualityScore, error)
	
	// GetScoreHistory 获取历史评分
	GetScoreHistory(ctx context.Context, provider string, modelName string, limit int) ([]*QualityScore, error)
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

	fmt.Printf("[QualityMonitor] Started with %d target models\n", len(m.config.TargetModels))
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
	fmt.Println("[QualityMonitor] Stopped")
}

// scheduledCheckLoop 定时检测循环
func (m *QualityMonitor) scheduledCheckLoop(ctx context.Context) {
	ticker := time.NewTicker(m.config.ScheduleInterval)
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
	fmt.Printf("[QualityMonitor] Starting scheduled quality check at %s\n", time.Now().Format(time.RFC3339))

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

	fmt.Printf("[QualityMonitor] Anomaly triggered for %s:%s, reason: %s\n", provider, modelName, reason)

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

// testModel 测试单个模型
func (m *QualityMonitor) testModel(ctx context.Context, target ModelTarget, suite *BenchmarkSuite, trigger string) error {
	displayName := target.Alias
	if displayName == "" {
		displayName = fmt.Sprintf("%s:%s", target.Provider, target.ModelName)
	}

	fmt.Printf("[QualityMonitor] Testing model: %s (trigger: %s)\n", displayName, trigger)

	// 执行测试
	report, err := m.executor.Execute(ctx, target.ModelName, target.Provider, suite)
	if err != nil {
		fmt.Printf("[QualityMonitor] Test failed for %s: %v\n", displayName, err)
		return err
	}

	// 保存报告
	if err := m.storage.SaveReport(ctx, report); err != nil {
		fmt.Printf("[QualityMonitor] Failed to save report: %v\n", err)
	}

	// 计算评分
	calculator := &ScoreCalculator{}
	score := calculator.CalculateScore(report)

	// 保存评分
	if err := m.storage.SaveScore(ctx, score); err != nil {
		fmt.Printf("[QualityMonitor] Failed to save score: %v\n", err)
	}

	// 输出结果
	fmt.Printf("[QualityMonitor] Test completed for %s:\n", displayName)
	fmt.Printf("  Accuracy: %.2f%%, Stability: %.2f%%, Latency P95: %.0fms\n", 
		score.Accuracy, score.Stability, score.Latency)
	fmt.Printf("  Overall Score: %.2f (%s)\n", score.OverallScore, score.Grade)

	// 检查质量下降
	if m.config.AlertOnQualityDrop {
		m.checkQualityDrop(ctx, target, score)
	}

	// 更新缓存
	key := fmt.Sprintf("%s:%s", target.Provider, target.ModelName)
	m.mu.Lock()
	m.lastScores[key] = score
	m.mu.Unlock()

	return nil
}

// checkQualityDrop 检查质量下降
func (m *QualityMonitor) checkQualityDrop(ctx context.Context, target ModelTarget, newScore *QualityScore) {
	key := fmt.Sprintf("%s:%s", target.Provider, target.ModelName)
	
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
			fmt.Printf("[QualityMonitor] Failed to send alert: %v\n", err)
		}
	}
}

// loadLastScores 加载上次的评分
func (m *QualityMonitor) loadLastScores(ctx context.Context) {
	for _, target := range m.config.TargetModels {
		score, err := m.storage.GetLatestScore(ctx, target.Provider, target.ModelName)
		if err != nil {
			continue
		}
		if score != nil {
			key := fmt.Sprintf("%s:%s", target.Provider, target.ModelName)
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
