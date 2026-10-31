// Package bg — model_credibility_worker.go
//
// 模型可信度（防掺水）扫描 worker：定时对每个 (凭据,模型) 节点发一次
// 身份探测，结合已有的智商实测数据判定「上游给的是不是它声称的那个模型」，
// 并把判定写进 provider_model_credibility / provider_model_credibility_runs
// 与 provider_quality_profiles.model_credibility_*。
//
// # 为什么扫描很便宜
//
// 每次扫描对每个节点只发**一次**极小的请求（标定提示词 ⇒ 约 20 token 输出），
// 能力维度**复用**已有的 MMLU 智商实测（model_iq_runs），不重跑题库。
// ⇒ 一个节点一次扫描的增量成本 ≈ 一次普通对话请求。
//
// # 扫描频率
//
// 默认 6 小时一次。理由：
//   - 身份漂移（上游把 Sonnet 切成 Haiku）是**持续行为**，不是瞬时事件
//     ⇒ 频率太高只是烧钱；
//   - 但也不能太慢：掺水方往往在「客户投诉后」才切回真货，扫描太慢会
//     错过那段窗口 ⇒ 6 小时是可接受的折中。
//
// # 与 model_quality_worker 的关系
//
// 两者**互补**，不重叠：
//   - model_quality_worker：跑 MMLU 题库 ⇒ 「这个模型答对多少题」（质量）。
//   - model_credibility_worker：发一次标定请求 + 复用上面的实测 ⇒
//     「这个模型是不是它声称的那个」（身份）。
//
// ⇒ 后者依赖前者的产出（model_iq_runs 的准确率）⇒ 前者没跑过时，
//
//	能力维度记为「未测量」，判定只靠身份与指纹两把尺。
package bg

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domains/modelcredibility"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// ModelCredibilityWorkerConfig 是 worker 的运行参数。
type ModelCredibilityWorkerConfig struct {
	// Interval 是扫描周期。
	Interval time.Duration
	// ProbeTimeout 是单次身份探测的超时。
	ProbeTimeout time.Duration
	// Enabled 为假时 worker 不启动（但仍可被 admin API 手动触发）。
	Enabled bool
	// Scope 是扫描面："featured"（默认，只扫 routing_policy.featured_models）
	// 或 "all"（全部活跃绑定）。
	Scope string
}

// DefaultModelCredibilityWorkerConfig 返回默认参数。
//
// ★ Scope 默认 featured：把真实 token 成本与对上游的请求量钉在
// routing_policy.featured_models 命中的绑定上，不扫全量目录。
func DefaultModelCredibilityWorkerConfig() ModelCredibilityWorkerConfig {
	return ModelCredibilityWorkerConfig{
		Interval:     6 * time.Hour,
		ProbeTimeout: 60 * time.Second,
		Enabled:      false,
		Scope:        modelcredibility.ScopeFeatured,
	}
}

// ModelCredibilityWorker 是可信度扫描 worker。
type ModelCredibilityWorker struct {
	db     *pgxpool.Pool
	source *CredibilityNodeSource
	cfg    ModelCredibilityWorkerConfig

	mu      sync.Mutex
	running bool
	stopCh  chan struct{}
	doneCh  chan struct{}
}

// NewModelCredibilityWorker 创建 worker。
func NewModelCredibilityWorker(db *pgxpool.Pool, keyring *secret.Keyring, encKey []byte, cfg ModelCredibilityWorkerConfig) *ModelCredibilityWorker {
	w := &ModelCredibilityWorker{db: db, cfg: cfg}
	if db != nil {
		w.source = NewCredibilityNodeSource(db, keyring, encKey)
	}
	return w
}

// scanner 构造一次扫描器（每次扫描新建 ⇒ 校准缓存按扫描周期失效，符合
// 「校准是平台级慢变量」的假设）。
func (w *ModelCredibilityWorker) scanner() *modelcredibility.Scanner {
	store := modelcredibility.NewStore(w.db)
	prober := modelcredibility.NewDirectProber(w.cfg.ProbeTimeout)
	return modelcredibility.NewScanner(store, prober, modelcredibility.DefaultConfig())
}

// Start 启动 worker（阻塞循环在独立 goroutine 里）。
func (w *ModelCredibilityWorker) Start(ctx context.Context) {
	if !w.cfg.Enabled || w.db == nil {
		slog.Info("model credibility worker: disabled or db unavailable, not starting")
		return
	}
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		slog.Warn("model credibility worker already running, skipping duplicate start")
		return
	}
	w.running = true
	w.stopCh = make(chan struct{})
	w.doneCh = make(chan struct{})
	stopCh, doneCh := w.stopCh, w.doneCh
	interval := w.cfg.Interval
	if interval <= 0 {
		interval = DefaultModelCredibilityWorkerConfig().Interval
	}
	w.mu.Unlock()

	slog.Info("model credibility worker started", "interval", interval, "scope", w.scope())
	// ★ Go（bg/spawn.go）而不是裸 go：本仓 2026-09-30（三十七轮审计）把 bg/ 内的
	// 裸 go 全部收口（panic recover + 日志 + 指标）。
	// 裸 go 里一个 panic 会带崩整个网关进程（所有在途请求陪葬）。
	// doneCh 握手保留在 loop 内（close 所有权仍归 loop 自己），
	// Stop 只等待 30s，语义不变。
	Go("model_credibility_worker.loop", func() {
		w.loop(ctx, interval, stopCh, doneCh)
	})
}

// Stop 停止 worker。
func (w *ModelCredibilityWorker) Stop() {
	w.mu.Lock()
	if !w.running {
		w.mu.Unlock()
		return
	}
	stopCh, doneCh := w.stopCh, w.doneCh
	w.running = false
	w.stopCh = nil
	w.doneCh = nil
	w.mu.Unlock()

	if stopCh != nil {
		close(stopCh)
	}
	if doneCh != nil {
		select {
		case <-doneCh:
		case <-time.After(30 * time.Second):
			slog.Warn("model credibility worker: stop timed out after 30s")
		}
	}
	slog.Info("model credibility worker stopped")
}

// loop 扫描主循环。由 Start 通过 bg.Go（panic 收口）拉起；
// close(doneCh) 的所有权归本函数。
func (w *ModelCredibilityWorker) loop(ctx context.Context, interval time.Duration, stopCh <-chan struct{}, doneCh chan<- struct{}) {
	defer close(doneCh)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// 启动后先等一个周期再跑第一次。刚重启就把所有节点探一遍会打出
	// 一轮失败判定（上游还没热），而且会覆盖上一次的指纹基线。
	for {
		select {
		case <-ticker.C:
			if n, err := w.ScanAll(ctx); err != nil {
				slog.Warn("model credibility scan error", "scanned", n, "error", err)
			} else {
				slog.Info("model credibility scan done", "scanned", n)
			}
		case <-stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

// scope 返回本次扫描面；配置缺失或非法时回落 featured。
//
// ★ 回落而不是报错：一个填错值的配置不应该让扫描整个停摆，
// 但必须打 WARN 让运维看到「他配的值没生效」。
func (w *ModelCredibilityWorker) scope() string {
	if w.cfg.Scope == "" {
		return modelcredibility.ScopeFeatured
	}
	if !modelcredibility.ValidScope(w.cfg.Scope) {
		slog.Warn("model credibility worker: invalid scope, falling back to featured",
			"configured", w.cfg.Scope)
		return modelcredibility.ScopeFeatured
	}
	return w.cfg.Scope
}

// ScanAll 扫描全部活跃节点。
func (w *ModelCredibilityWorker) ScanAll(ctx context.Context) (int, error) {
	if w.db == nil {
		return 0, fmt.Errorf("model credibility worker: db pool is nil")
	}
	targets, err := w.source.DiscoverTargets(ctx, w.scope())
	if err != nil {
		return 0, fmt.Errorf("discover credibility targets: %w", err)
	}
	if len(targets) == 0 {
		slog.Info("model credibility scan: no eligible targets")
		return 0, nil
	}
	scanner := w.scanner()
	results, scanErr := scanner.ScanTargets(ctx, targets)
	// 把供应商×模型粒度的汇总写回 provider_quality_profiles。
	// ★ 传 targets：只写本轮扫到的 (provider, model)，防止历史残留行
	// （已下线/已移出特色清单的模型）被误写。
	// ★ 独立 ctx：探测可能耗尽 HTTP 客户端的超时（scan 一轮 20+ 分钟），
	// 请求 ctx 取消不应连带取消汇总落库 —— 211 实测 2026-10-30：
	// 全量 scan 跑满客户端超时后 6 组判定只写回 1 组。
	//
	// ★★ 2026-10-30 12h 审计轮 A5 订正：★★ 汇总写入**必须**排在扫描错误的
	//   早退**之前** ⇒ ★★ 原实现把 `if err != nil { return }` 放在 rollupCtx
	//   创建**之前** ⇒ ★★ 而 ScanTargets 唯一的 error 出口就是 ctx 取消/超时
	//   ⇒ ★★ 请求 ctx 一断就早退 ⇒ ★★ 用独立 ctx 的汇总写入**永远执行不到**
	//   ⇒ ★★ 「解耦」这条修复在它要修的场景里**根本走不到** ⇒ ★★ 211 那个
	//   「6 组判定只写回 1 组」的现象**没有被修掉** ★★。
	// ⇒ ★★ 现在：扫描出错也**先写汇总**（汇总读的是已落库的当前态），
	//   写完再把扫描错误如实返回给调用方 ★★。
	rollupCtx, rollupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	if n, err := scanner.Store.WriteProviderQualityProfiles(rollupCtx, targets); err != nil {
		slog.Warn("model credibility: write provider quality profiles failed", "error", err)
	} else if n > 0 {
		slog.Info("model credibility: provider quality profiles updated", "rows", n)
	}
	rollupCancel()
	if scanErr != nil {
		return len(results), scanErr
	}
	return len(results), nil
}

// ScanNode 扫描单个节点（admin API 的「立即检测」按钮）。
func (w *ModelCredibilityWorker) ScanNode(ctx context.Context, credentialID int64, rawModelName string) (*modelcredibility.Result, error) {
	if w.db == nil {
		return nil, fmt.Errorf("model credibility worker: db pool is nil")
	}
	target, err := w.source.FindTarget(ctx, credentialID, rawModelName)
	if err != nil {
		return nil, err
	}
	scanner := w.scanner()
	res, err := scanner.ScanNode(ctx, *target)
	if err != nil {
		return nil, err
	}
	// ★ 同 ScanAll：汇总用独立 ctx（单点 trigger 的请求 ctx 取消不应连带取消落库）。
	rollupCtx, rollupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	if _, err := scanner.Store.WriteProviderQualityProfiles(rollupCtx, nil); err != nil {
		slog.Warn("model credibility: write provider quality profiles failed", "error", err)
	}
	rollupCancel()
	return &res, nil
}

// Running 返回 worker 是否在运行。
func (w *ModelCredibilityWorker) Running() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running
}
