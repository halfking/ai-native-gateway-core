// Package bg — model_quality_worker.go
//
// ModelQualityWorker 模型质量监控后台任务
// 定期对特色模型进行MMLU测试，记录评分历史，检测质量变化
//
// 集成到统一的自检worker系统中，与 CredentialSelfcheckWorker 等并列
// 
// 配置项见 settings/spec_model_quality.go
package bg

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/modelquality"
)

// modelQualityDB 是 ModelQualityWorker 需要的数据库接口
// 当前使用文件存储，未来可扩展为数据库存储
type modelQualityDB interface {
	// 未来扩展：从数据库读取配置、保存结果等
}

// ModelQualityWorker 模型质量监控后台任务
type ModelQualityWorker struct {
	dataDir  string
	apiKey   string // 系统API key，用于调用网关
	baseURL  string // 网关基础URL
	
	config   *modelquality.MonitorConfig
	monitor  *modelquality.QualityMonitor
	
	mu       sync.RWMutex
	running  bool
	stopChan chan struct{}
	stopped  chan struct{}
}

// NewModelQualityWorker 创建模型质量监控worker
// apiKey: 系统API key，用于调用网关进行测试
// baseURL: 网关地址，为空则使用默认
func NewModelQualityWorker(dataDir string, apiKey string, baseURL string) *ModelQualityWorker {
	if baseURL == "" {
		baseURL = "http://localhost:8787" // 默认本地网关
	}
	
	return &ModelQualityWorker{
		dataDir:  dataDir,
		apiKey:   apiKey,
		baseURL:  baseURL,
		stopChan: make(chan struct{}),
		stopped:  make(chan struct{}),
	}
}

// Start 启动worker
// 遵循统一worker模式：接收context，异步运行，通过Stop()停止
func (w *ModelQualityWorker) Start(ctx context.Context) {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		slog.Warn("model quality worker already running, skipping duplicate start")
		return
	}
	w.running = true
	w.mu.Unlock()

	// 初始化组件
	storageDir := filepath.Join(w.dataDir, "model-quality")
	storage, err := modelquality.NewFileStorage(storageDir)
	if err != nil {
		slog.Error("model quality worker: failed to init storage", "error", err)
		w.mu.Lock()
		w.running = false
		w.mu.Unlock()
		close(w.stopped)
		return
	}

	alertLogFile := filepath.Join(storageDir, "alerts.log")
	alerter, err := modelquality.NewLogAlerter(alertLogFile)
	if err != nil {
		slog.Error("model quality worker: failed to init alerter", "error", err)
		w.mu.Lock()
		w.running = false
		w.mu.Unlock()
		close(w.stopped)
		return
	}

	// 使用真实的网关调用器
	var invoker modelquality.ModelInvoker
	if w.apiKey != "" {
		// 有API key，使用真实网关调用
		slog.Info("model quality worker: using real gateway invoker", "base_url", w.baseURL)
		invoker = modelquality.NewGatewayModelInvoker(w.baseURL, w.apiKey)
	} else {
		// 没有API key，使用Mock调用器（用于测试）
		slog.Warn("model quality worker: no API key provided, using mock invoker for testing only")
		invoker = modelquality.NewMockModelInvoker()
	}
	executor := modelquality.NewBenchmarkExecutor(invoker, 30*time.Second)

	// 配置监控
	targetModels := modelquality.GetDefaultMonitorModels()
	w.config = &modelquality.MonitorConfig{
		EnableScheduled:      true,
		ScheduleInterval:     24 * time.Hour, // 每天检测一次
		UseLiteBenchmark:     true,           // 使用快速测试
		EnableAnomalyTrigger: true,
		ErrorRateThreshold:   0.3,
		LatencyThreshold:     5000,
		AlertOnQualityDrop:   true,
		QualityDropThreshold: 5.0, // 准确率下降5%告警
		TargetModels:         targetModels,
	}

	// 创建监控器
	w.monitor = modelquality.NewQualityMonitor(w.config, executor, storage, alerter)

	// 启动监控
	if err := w.monitor.Start(ctx); err != nil {
		slog.Error("model quality worker: failed to start monitor", "error", err)
		w.mu.Lock()
		w.running = false
		w.mu.Unlock()
		close(w.stopped)
		return
	}

	slog.Info("model quality worker started",
		"models", len(targetModels),
		"interval", w.config.ScheduleInterval.String(),
		"storage", storageDir)

	// 异步运行主循环
	go w.loop(ctx)
}

// Stop 停止worker
// 遵循统一worker模式：幂等、阻塞直到完全停止
func (w *ModelQualityWorker) Stop() {
	w.mu.Lock()
	if !w.running {
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()

	slog.Info("stopping model quality worker...")
	close(w.stopChan)
	
	// 等待完全停止
	<-w.stopped
	
	// 停止监控器
	if w.monitor != nil {
		w.monitor.Stop()
	}

	w.mu.Lock()
	w.running = false
	w.mu.Unlock()
	
	slog.Info("model quality worker stopped")
}

// loop 主循环
func (w *ModelQualityWorker) loop(ctx context.Context) {
	defer close(w.stopped)
	
	// worker自身不需要循环，监控器内部已有定时逻辑
	// 这里只需要等待停止信号
	select {
	case <-w.stopChan:
		return
	case <-ctx.Done():
		return
	}
}

// TriggerCheck 手动触发检测(用于异常场景)
func (w *ModelQualityWorker) TriggerCheck(ctx context.Context, provider string, modelName string, reason string) error {
	w.mu.RLock()
	running := w.running
	monitor := w.monitor
	w.mu.RUnlock()
	
	if !running {
		return fmt.Errorf("worker not started")
	}

	if monitor == nil {
		return fmt.Errorf("monitor not initialized")
	}

	return monitor.TriggerAnomalyCheck(ctx, provider, modelName, reason)
}

// GetCurrentScores 获取当前评分
func (w *ModelQualityWorker) GetCurrentScores() map[string]*modelquality.QualityScore {
	w.mu.RLock()
	running := w.running
	monitor := w.monitor
	w.mu.RUnlock()
	
	if !running || monitor == nil {
		return nil
	}

	return monitor.GetCurrentScores()
}

// UpdateConfig 更新配置(热更新)
func (w *ModelQualityWorker) UpdateConfig(config *modelquality.MonitorConfig) {
	w.mu.Lock()
	defer w.mu.Unlock()
	
	w.config = config
	// TODO: 重启监控器以应用新配置
}
