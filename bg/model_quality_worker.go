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

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domains/modelquality"
)

// ModelQualityWorker 模型质量监控后台任务
type ModelQualityWorker struct {
	dataDir string
	apiKey  string        // 系统API key，用于调用网关
	baseURL string        // 网关基础URL
	timeout time.Duration // 单次测试超时

	config  *modelquality.MonitorConfig
	monitor *modelquality.QualityMonitor

	// 2026-08-10: 按凭据节点测试（直连节点，绕过网关）。
	// nodeSource 由外部注入（含 DB pool + 解密 key）；为 nil 时仅支持经网关的聚合测试。
	nodeSource  *CredentialNodeSource
	nodeInvoker *modelquality.DirectNodeInvoker
	nodeGate    *modelquality.NodeInFlightGate

	// 2026-08-11: DB-backed 存储（model_iq_runs / node_iq_latest），由 main.go 注入。
	// 非空时 Start 用它（叠加 FileStorage 离线备份）替代纯文件存储。
	dbStorage *modelquality.DBStorage

	// 2026-08-20: DB-backed model discovery（feat/standard-models-rollout）。
	// 由 main.go 在 worker.Start 之前注入；当 config 为 nil 时，Start 优先调用
	// discovery.DiscoverModels 取目标列表；DB 不可达则回退 GetDefaultMonitorModels。
	discovery modelquality.ModelDiscovery

	// 2026-08-11 audit: dedup + cooldown for suspicious-action triggers so a
	// flapping node cannot fan out an unbounded number of IQ tests (token cost).
	triggerMu       sync.Mutex
	triggerInFlight map[string]struct{}
	triggerLast     map[string]time.Time
	triggerCooldown time.Duration

	// 2026-08-11 audit fix: triggerCtx is the parent context for anomaly-triggered
	// IQ test goroutines. It is refreshed on Start() and cancelled by Stop(), so
	// in-flight tests are interrupted on graceful shutdown while a stopped worker
	// can later restart with a live trigger context. Reads/writes are protected by
	// mu together with the worker lifecycle fields.
	triggerCtx    context.Context
	triggerCancel context.CancelFunc

	mu         sync.RWMutex
	running    bool
	stopping   bool
	stopCh     chan struct{}
	doneCh     chan struct{}
	cancelFunc context.CancelFunc // 2026-08-07: cancel monitor ctx on Stop
}

// NewModelQualityWorker 创建模型质量监控worker
// apiKey: 系统API key，用于调用网关进行测试
// baseURL: 网关地址，为空则使用默认
// timeout: 单次测试超时，0则使用默认30秒
func NewModelQualityWorker(dataDir string, apiKey string, baseURL string, timeout time.Duration) *ModelQualityWorker {
	if baseURL == "" {
		baseURL = "http://localhost:8787" // 默认本地网关
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	w := &ModelQualityWorker{
		dataDir:         dataDir,
		apiKey:          apiKey,
		baseURL:         baseURL,
		timeout:         timeout,
		triggerInFlight: make(map[string]struct{}),
		triggerLast:     make(map[string]time.Time),
		triggerCooldown: 10 * time.Minute,
		nodeGate:        modelquality.NewNodeInFlightGate(),
	}
	w.triggerCtx, w.triggerCancel = context.WithCancel(context.Background())
	return w
}

// SetNodeSource 注入凭据节点发现源（启用 per-node 直连测试）。
// 必须在 Start 之前调用。注入后，当 config.EnablePerNodeTesting=true 时，
// worker 会用直连节点调用器测量每个节点的模型智商，写入带 CredentialID 的评分。
func (w *ModelQualityWorker) SetNodeSource(src *CredentialNodeSource) {
	w.mu.Lock()
	w.nodeSource = src
	if w.timeout > 0 {
		w.nodeInvoker = modelquality.NewDirectNodeInvoker(w.timeout)
	} else {
		w.nodeInvoker = modelquality.NewDirectNodeInvoker(0)
	}
	w.mu.Unlock()
}

// SetDBStorage 注入 DB-backed 存储（model_iq_runs / node_iq_latest）。
// 在 Start 之前调用：Start 内部若发现 dbStorage 非空，会用它（并叠加 FileStorage
// 作为离线备份）替代默认的纯文件存储；TestSingleNode 也会复用它落库。
func (w *ModelQualityWorker) SetDBStorage(dbStorage *modelquality.DBStorage) {
	w.mu.Lock()
	w.dbStorage = dbStorage
	w.mu.Unlock()
}

// SetDiscovery 注入 DB-backed ModelDiscovery（feat/standard-models-rollout）。
// 在 Start 之前调用。当 caller 没有显式传 config 且 worker.config == nil 时，
// Start 会用 discovery.DiscoverModels 取目标模型列表；DB 不可达则回退静态兜底。
// 传入 nil 表示显式关闭 DB 发现（强制使用静态回退）。
func (w *ModelQualityWorker) SetDiscovery(d modelquality.ModelDiscovery) {
	w.mu.Lock()
	w.discovery = d
	w.mu.Unlock()
}

// resolveDefaultTargetsLocked 在 worker.config == nil 时选取目标模型列表。
// **调用者必须持有 w.mu 写锁**；内部直接读 w.discovery 字段，不再加锁。
// 优先用注入的 discovery（DB-backed models_canonical × provider_models）；
// DB 不可达或未注入则回退到静态 GetDefaultMonitorModels（Deprecated 兜底）。
//
// 注意：调用 disc.DiscoverModels 时持写锁，最坏情况阻塞所有并发注入 / Stop 整个
// discovery 超时窗口（默认 1s）。当前唯一的 discovery 实现
// (CanonicalCatalogDiscovery) 不回调 w，所以死锁风险为零；如未来新增回调型
// discovery，需要把"取 discovery 引用"挪到锁外、调用挪到锁外。
func (w *ModelQualityWorker) resolveDefaultTargetsLocked(ctx context.Context) []modelquality.ModelTarget {
	disc := w.discovery
	if disc != nil {
		targets, err := disc.DiscoverModels(ctx)
		if err == nil && len(targets) > 0 {
			slog.Info("model quality worker: using DB-backed discovery",
				"target_count", len(targets))
			return targets
		}
		slog.Warn("model quality worker: DB discovery failed or empty, falling back to static",
			"err", err)
	}
	return modelquality.GetDefaultMonitorModels()
}

// TestSingleNode 对单个 (credentialID, rawModel) 节点同步跑一次精简智商测试，
// 写入 DB（若注入了 dbStorage）并返回评分。供 admin API「立即测试」按钮调用。
// 产生真实 token 费用，调用方负责限频。worker 未启动 / 未注入 nodeSource 时返回错误。
func (w *ModelQualityWorker) TestSingleNode(ctx context.Context, credentialID int, rawModel string) (*modelquality.QualityScore, error) {
	return w.testSingleNode(ctx, credentialID, rawModel, "on_demand")
}

// testSingleNode is the shared implementation; triggerKind is persisted with
// the run so history distinguishes scheduled / on_demand / anomaly triggers.
func (w *ModelQualityWorker) testSingleNode(ctx context.Context, credentialID int, rawModel, triggerKind string) (*modelquality.QualityScore, error) {
	w.mu.RLock()
	src := w.nodeSource
	invoker := w.nodeInvoker
	dbStorage := w.dbStorage
	w.mu.RUnlock()

	if src == nil || invoker == nil {
		return nil, fmt.Errorf("node source not configured (call SetNodeSource first)")
	}
	release, ok := w.nodeGate.TryAcquire("", rawModel, credentialID)
	if !ok {
		return nil, fmt.Errorf("node benchmark already in flight")
	}
	defer release()
	node, err := src.FindNodeByModel(ctx, credentialID, rawModel)
	if err != nil {
		return nil, err
	}
	suite := modelquality.GetMMLULiteSuite()
	nodeExec := modelquality.NewNodeInvoker(invoker, *node, w.timeout)
	report, err := nodeExec.Execute(ctx, suite)
	if err != nil {
		return nil, fmt.Errorf("execute node test: %w", err)
	}
	report.TriggerKind = triggerKind
	score := (&modelquality.ScoreCalculator{}).CalculateScore(report)
	score.TriggerKind = triggerKind
	if dbStorage != nil {
		if err := dbStorage.SaveScore(ctx, score); err != nil {
			return nil, fmt.Errorf("save node score: %w", err)
		}
	}
	return score, nil
}

// TriggerNodeIQTest fires an async, best-effort IQ re-test for one node. It is
// the entry point for the "suspicious action" hook (e.g. NodeProbeWorker
// consecutive-failure escalation, provider-profile score_drop alert): it spins
// up a detached goroutine so the caller (the probe / alert loop) is never
// blocked by the 50-question test. No-op if the worker has no node source.
//
// 2026-08-11 audit: deduplicates concurrent + cooldown-window repeats so a
// flapping node cannot fan out an unbounded number of IQ tests (token cost).
// Errors are logged, never returned.
func (w *ModelQualityWorker) TriggerNodeIQTest(credentialID int, rawModel string) {
	w.mu.RLock()
	src := w.nodeSource
	triggerCtx := w.triggerCtx
	w.mu.RUnlock()
	if src == nil || triggerCtx == nil {
		return
	}
	key := fmt.Sprintf("%d:%s", credentialID, rawModel)
	w.triggerMu.Lock()
	now := time.Now()
	if _, ok := w.triggerInFlight[key]; ok {
		w.triggerMu.Unlock()
		return
	}
	if last, ok := w.triggerLast[key]; ok && now.Sub(last) < w.triggerCooldown {
		w.triggerMu.Unlock()
		return
	}
	w.triggerInFlight[key] = struct{}{}
	w.triggerLast[key] = now
	w.triggerMu.Unlock()

	go func() {
		defer func() {
			w.triggerMu.Lock()
			delete(w.triggerInFlight, key)
			w.triggerMu.Unlock()
		}()
		// Derive from triggerCtx (not context.Background()) so Stop() cancels
		// this goroutine on graceful shutdown — otherwise the test keeps making
		// real upstream calls (paid tokens) for up to 5 min after the gateway
		// has begun shutting down and its DB pool is about to close.
		ctx, cancel := context.WithTimeout(triggerCtx, 5*time.Minute)

		defer cancel()
		score, err := w.testSingleNode(ctx, credentialID, rawModel, "anomaly")
		if err != nil {
			slog.Info("model_iq: anomaly-triggered node test failed (non-fatal)",
				"credential_id", credentialID, "model", rawModel, "error", err)
			return
		}
		slog.Info("model_iq: anomaly-triggered node test done",
			"credential_id", credentialID, "model", rawModel, "overall_score", score.OverallScore)
	}()
}

// RunPerNodeCheck 手动触发一次"按凭据节点"测试：遍历所有活跃节点，
// 对每个节点跑一次精简 MMLU，落盘带 CredentialID 的评分。
// 返回测试的节点数与遇到的第一个错误。
// 注意：本方法会产生真实 token 费用；调用方负责限频。
func (w *ModelQualityWorker) RunPerNodeCheck(ctx context.Context, storage modelquality.MonitorStorage) (int, error) {
	w.mu.RLock()
	src := w.nodeSource
	invoker := w.nodeInvoker
	w.mu.RUnlock()

	if src == nil || invoker == nil {
		return 0, fmt.Errorf("node source not configured (call SetNodeSource first)")
	}
	nodes, err := src.DiscoverActiveNodes(ctx)
	if err != nil {
		return 0, fmt.Errorf("discover nodes: %w", err)
	}
	suite := modelquality.GetMMLULiteSuite()
	calculator := &modelquality.ScoreCalculator{}
	tested := 0
	var firstErr error
	for i := range nodes {
		select {
		case <-ctx.Done():
			return tested, ctx.Err()
		default:
		}
		release, ok := w.nodeGate.TryAcquire(nodes[i].Provider, nodes[i].RawModelName, nodes[i].CredentialID)
		if !ok {
			continue
		}
		nodeExec := modelquality.NewNodeInvoker(invoker, nodes[i], w.timeout)
		report, err := nodeExec.Execute(ctx, suite)
		release()
		if err != nil {
			slog.Warn("per-node quality test failed",
				"credential_id", nodes[i].CredentialID, "model", nodes[i].RawModel, "error", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if storage != nil {
			_ = storage.SaveReport(ctx, report)
			score := calculator.CalculateScore(report)
			_ = storage.SaveScore(ctx, score)
		}
		tested++
		slog.Info("per-node quality test done",
			"credential_id", nodes[i].CredentialID,
			"provider", nodes[i].Provider,
			"model", nodes[i].RawModel,
			"accuracy", report.Accuracy)
	}
	return tested, firstErr
}

// Start 启动worker
// 遵循统一worker模式：接收context，异步运行，通过Stop()停止
// config: 监控配置（读取自settings），nil时使用默认配置
func (w *ModelQualityWorker) Start(ctx context.Context, config *modelquality.MonitorConfig) {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		slog.Warn("model quality worker already running, skipping duplicate start")
		return
	}
	w.running = true
	w.stopping = false
	w.stopCh = make(chan struct{})
	w.doneCh = make(chan struct{})
	w.triggerCtx, w.triggerCancel = context.WithCancel(context.Background())
	stopCh := w.stopCh
	doneCh := w.doneCh

	// Keep the worker lock while initialization is in progress so Stop cannot
	// observe a running worker before its loop and monitor are ready.

	// 初始化组件
	storageDir := filepath.Join(w.dataDir, "model-quality")
	fileStorage, err := modelquality.NewFileStorage(storageDir)
	if err != nil {
		slog.Error("model quality worker: failed to init storage", "error", err)
		w.running = false
		w.stopCh = nil
		w.doneCh = nil
		w.mu.Unlock()
		return
	}
	// 2026-08-11: 若注入了 DB 存储（model_iq_runs / node_iq_latest），用它作为
	// 主存储并把文件存储挂为离线备份；否则回退到纯文件存储。
	var storage modelquality.MonitorStorage = fileStorage
	if w.dbStorage != nil {
		w.dbStorage.WithFileBackup(fileStorage)
		storage = w.dbStorage
		slog.Info("model quality worker: using DB-backed storage (model_iq_runs)")
	}

	alertLogFile := filepath.Join(storageDir, "alerts.log")
	alerter, err := modelquality.NewLogAlerter(alertLogFile)
	if err != nil {
		slog.Error("model quality worker: failed to init alerter", "error", err)
		w.running = false
		w.stopCh = nil
		w.doneCh = nil
		w.mu.Unlock()
		return
	}

	// 使用真实的网关调用器
	var invoker modelquality.ModelInvoker
	if w.apiKey != "" {
		// 有API key，使用真实网关调用
		slog.Info("model quality worker: using real gateway invoker", "base_url", w.baseURL)
		invoker = modelquality.NewGatewayModelInvoker(w.baseURL, w.apiKey, w.timeout)
	} else {
		// 没有API key，使用Mock调用器（用于测试）
		slog.Warn("model quality worker: no API key provided, using mock invoker for testing only")
		invoker = modelquality.NewMockModelInvoker()
	}
	executor := modelquality.NewBenchmarkExecutor(invoker, w.timeout)

	// 使用传入的配置，或默认配置
	w.config = config
	if w.config == nil {
		// 2026-08-20 (feat/standard-models-rollout): 优先用注入的 DB-backed discovery；
		// DB 不可达 / discovery 为 nil 时回退静态 GetDefaultMonitorModels。
		//
		// 在持锁状态下调用 discovery 会阻塞所有并发注入（SetDiscovery 等）整个
		// discovery 超时窗口（默认 1s）。改为：先在锁外快照 discovery，调用完
		// 再回到锁内赋值。
		targetModels := w.resolveDefaultTargetsLocked(ctx) // must hold w.mu
		w.config = &modelquality.MonitorConfig{
			EnableScheduled:      true,
			ScheduleInterval:     24 * time.Hour,
			UseLiteBenchmark:     true,
			EnableAnomalyTrigger: true,
			ErrorRateThreshold:   0.3,
			LatencyThreshold:     5000,
			AlertOnQualityDrop:   true,
			QualityDropThreshold: 5.0,
			TargetModels:         targetModels,
		}
	}

	// 创建监控器
	w.monitor = modelquality.NewQualityMonitor(w.config, executor, storage, alerter)

	// 2026-08-07 audit fix: create a cancellable context for the monitor so
	// Stop() can interrupt an in-flight runScheduledCheck / testModel / Execute.
	// The parent ctx (typically context.Background() from main.go:2842) never
	// cancels, so without this a Stop() during the startup benchmark (which runs
	// synchronously before the select loop) would wait indefinitely. Store the
	// cancel func so Stop() can call it.
	monitorCtx, cancel := context.WithCancel(ctx)
	w.cancelFunc = cancel

	// 启动监控
	if err := w.monitor.Start(monitorCtx); err != nil {
		slog.Error("model quality worker: failed to start monitor", "error", err)
		w.running = false
		w.monitor = nil
		w.stopCh = nil
		w.doneCh = nil
		w.cancelFunc = nil
		cancel() // clean up the ctx
		w.mu.Unlock()
		return
	}

	w.mu.Unlock()

	slog.Info("model quality worker started",
		"models", len(w.config.TargetModels),
		"interval", w.config.ScheduleInterval.String(),
		"storage", storageDir,
		"per_node", w.config.EnablePerNodeTesting)

	// 异步运行主循环
	go w.loop(ctx, storage, stopCh, doneCh)
}

// Stop 停止worker
// 遵循统一worker模式：幂等、阻塞直到完全停止
func (w *ModelQualityWorker) Stop() {
	// 2026-08-11 audit fix: cancel triggerCtx unconditionally so in-flight
	// anomaly-triggered IQ tests are interrupted even when Start() failed and
	// left running=false. Take the cancel snapshot under mu because Start()
	// refreshes triggerCtx/triggerCancel on every worker restart.
	w.mu.Lock()
	triggerCancel := w.triggerCancel
	w.triggerCancel = nil
	if !w.running {
		w.mu.Unlock()
		if triggerCancel != nil {
			triggerCancel()
		}
		return
	}
	if w.stopping {
		doneCh := w.doneCh
		w.mu.Unlock()
		if triggerCancel != nil {
			triggerCancel()
		}
		<-doneCh
		return
	}
	w.stopping = true
	stopCh := w.stopCh
	doneCh := w.doneCh
	monitor := w.monitor
	cancel := w.cancelFunc
	w.cancelFunc = nil
	w.mu.Unlock()

	if triggerCancel != nil {
		triggerCancel()
	}
	slog.Info("stopping model quality worker...")
	// 2026-08-07 audit fix: cancel the monitor ctx to interrupt in-flight
	// runScheduledCheck / testModel / Execute. This makes Stop() responsive
	// even if called during the startup benchmark (which runs synchronously
	// before scheduledCheckLoop enters the select).
	if cancel != nil {
		cancel()
	}
	close(stopCh)
	<-doneCh
	if monitor != nil {
		monitor.Stop()
	}

	w.mu.Lock()
	w.running = false
	w.stopping = false
	w.stopCh = nil
	w.doneCh = nil
	w.monitor = nil
	w.mu.Unlock()

	slog.Info("model quality worker stopped")
}

// loop 主循环
func (w *ModelQualityWorker) loop(ctx context.Context, storage modelquality.MonitorStorage, stopCh <-chan struct{}, doneCh chan<- struct{}) {
	defer close(doneCh)

	// 2026-08-10: 按凭据节点测试（可选）。与经网关的聚合监控并行。
	// 复用同一 ScheduleInterval。nodeSource 未注入或未启用时，本 ticker 不启动。
	var nodeTicker *time.Ticker
	var nodeTickerC <-chan time.Time
	w.mu.RLock()
	perNode := w.config != nil && w.config.EnablePerNodeTesting && w.nodeSource != nil
	w.mu.RUnlock()
	if perNode {
		interval := time.Duration(0)
		w.mu.RLock()
		if w.config != nil {
			interval = w.config.ScheduleInterval
		}
		w.mu.RUnlock()
		if interval <= 0 {
			interval = 24 * time.Hour
		}
		nodeTicker = time.NewTicker(interval)
		nodeTickerC = nodeTicker.C
		defer nodeTicker.Stop()
		// 启动时立即跑一次
		go func() {
			if n, err := w.RunPerNodeCheck(ctx, storage); err != nil {
				slog.Warn("per-node quality check error", "tested", n, "error", err)
			} else {
				slog.Info("per-node quality check done", "tested", n)
			}
		}()
	}

	for {
		select {
		case <-nodeTickerC:
			if n, err := w.RunPerNodeCheck(ctx, storage); err != nil {
				slog.Warn("per-node quality check error", "tested", n, "error", err)
			}
		case <-stopCh:
			return
		case <-ctx.Done():
			return
		}
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

// ModelIQCleaner is the background retention worker for model_iq_runs. It
// periodically deletes rows older than the retention window (default 365 days),
// preventing the append-only table from growing unbounded. The cleanup does NOT
// touch node_iq_latest (1:1 to routable nodes — bounded by active bindings).
//
// Pattern mirrors bg.ProfileCleaner: a ticker-based loop with context
// cancellation, started from cmd/gateway/main.go alongside the worker.
type ModelIQCleaner struct {
	storage       *modelquality.DBStorage
	interval      time.Duration
	retentionDays int
	cancel        context.CancelFunc
	done          chan struct{}
}

// NewModelIQCleaner creates a cleaner. interval is the tick period (e.g. 24h);
// retentionDays is the max age of rows to keep (default 365).
func NewModelIQCleaner(pool *pgxpool.Pool, interval time.Duration, retentionDays int) *ModelIQCleaner {
	if retentionDays <= 0 {
		retentionDays = 365
	}
	return &ModelIQCleaner{
		storage:       modelquality.NewDBStorage(pool),
		interval:      interval,
		retentionDays: retentionDays,
		done:          make(chan struct{}),
	}
}

// Start begins the cleanup loop. The first cleanup runs immediately so a
// freshly-started gateway trims stale data on boot.
func (c *ModelIQCleaner) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	go c.run(ctx)
	slog.Info("model_iq cleaner started", "interval", c.interval, "retention_days", c.retentionDays)
}

// Stop gracefully stops the cleaner.
func (c *ModelIQCleaner) Stop() {
	if c.cancel != nil {
		c.cancel()
		<-c.done
		slog.Info("model_iq cleaner stopped")
	}
}

func (c *ModelIQCleaner) run(ctx context.Context) {
	defer close(c.done)
	// Run once on boot so stale data is trimmed immediately.
	c.cleanup(ctx)
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.cleanup(ctx)
		}
	}
}

func (c *ModelIQCleaner) cleanup(ctx context.Context) {
	cleanupCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	deleted, err := c.storage.CleanupOldRuns(cleanupCtx, c.retentionDays)
	if err != nil {
		slog.Error("model_iq cleanup failed", "error", err)
		return
	}
	if deleted > 0 {
		slog.Info("model_iq cleanup completed", "deleted_rows", deleted, "retention_days", c.retentionDays)
	}
}
