package main

// v1_write_liveness_worker.go —— 把 §9.264 的 v1 写入腿存活判决接成常驻信号。
//
// R48 §五-1 / R48-A5 的催办落地：`v1WriteLiveness()`（v1_write_liveness.go）
// 在 §9.264 落地时是一个「可调用但全仓零调用点」的分类器——§9.238 要惩治的
// 「5 天写入中断、全程无任何信号」在运行时原样成立。本文件补三样东西：
//
//  1. 常驻 worker：每 interval（默认 5m，窗口默认 30m）跑一次判决，
//     快照存 atomic.Value，`CheckedAt` 即「worker 还活着吗」的读数
//     （与 pg17 心跳的 lastrun mtime 同一惯例）；
//  2. 信号策略：**dead 每 tick 一条 ERROR**——停机期间信号持续存在，而不是
//     只在转换点响一次然后沉默 5 天（§9.238 的失效形态正是「响过一次就没有了」
//     都不如，是压根没有）；dead→alive/quiet 恢复时一条 WARN；测量失败每 tick
//     一条 WARN；steady alive/quiet 刻意静默（集群安静不是事件）；
//  3. admin 拉取面：GET /internal/v1-write-liveness 返回最近一次快照，
//     verdict=dead 时 HTTP 503（curl -f 族告警脚本零解析成本），
//     与 /internal/telemetry/fallback-buffer 同前缀同鉴权惯例。
//
// 刻意不做的事：不把 verdict 掺进 /healthz（那是「网关进程活着」的语义，
// 掺入「v1 写腿活着」会让 K8s 重启一个重启修不好的进程）；不挂 /metrics gauge
// （对外表面扩张属属主决定，本轮只做「信号存在」的最小闭环）。
//
// 单实例部署（2098/2101 蓝绿各一）下两实例都会报 dead——这是特性不是噪音：
// 两实例写同一条 v1 腿，任一实例看到 dead 都是真 dead。

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// livenessUnknown 是快照层的第 4 个值：**测量本身失败**。
// 分类器（ClassifyV1WriteLiveness）永远不会返回它——三值语义
// （alive/dead/quiet，见 v1_write_liveness.go）是判决层的契约；
// unknown 只出现在 worker 快照里，表示「这一 tick 什么都没量到」。
const livenessUnknown = "unknown"

// V1WriteLivenessInterval / Window 的默认值。窗口 30m 沿用 §9.264 的选择
// （该文档已声明「未论证」）；interval 必须显著小于窗口——5m/30m 意味着
// 一次新鲜停机在窗口内还有约 5 次检出机会。
const (
	v1WriteLivenessDefaultInterval = 5 * time.Minute
	v1WriteLivenessDefaultWindow   = 30 * time.Minute
)

// V1WriteLivenessSnapshot 是端点返回的最近一次读数。
type V1WriteLivenessSnapshot struct {
	Verdict         string    `json:"verdict"`
	CheckedAt       time.Time `json:"checked_at"`
	WindowMinutes   int       `json:"window_minutes"`
	V1HotRows       int64     `json:"v1_hot_rows"`
	V1ParentRows    int64     `json:"v1_parent_rows"`
	TurnsHotRows    int64     `json:"turns_hot_rows"`
	TurnsParentRows int64     `json:"turns_parent_rows"`
	ConsecutiveDead int       `json:"consecutive_dead_ticks"`
	LastError       string    `json:"last_error,omitempty"`
}

// V1WriteLivenessWorker 周期性评估 v1 写入腿存活并发出信号。
// probe 是可注入的读数函数，测试用它驱动判决序列。
type V1WriteLivenessWorker struct {
	probe    func(ctx context.Context) (v1WriteLivenessInput, string, error)
	interval time.Duration
	window   time.Duration
	logger   *slog.Logger

	snapshot atomic.Value // V1WriteLivenessSnapshot
	prev     string       // 上一 tick 的判决，仅 loop goroutine 访问

	stopCh    chan struct{}
	stopOnce  sync.Once
	startOnce sync.Once
}

// NewV1WriteLivenessWorker 用真实 pool 构造（生产接线点）。
func NewV1WriteLivenessWorker(pool *pgxpool.Pool, window, interval time.Duration) *V1WriteLivenessWorker {
	if window <= 0 {
		window = v1WriteLivenessDefaultWindow
	}
	return newV1WriteLivenessWorker(func(ctx context.Context) (v1WriteLivenessInput, string, error) {
		return v1WriteLiveness(ctx, pool, window)
	}, window, interval, slog.Default())
}

func newV1WriteLivenessWorker(
	probe func(ctx context.Context) (v1WriteLivenessInput, string, error),
	window time.Duration,
	interval time.Duration,
	logger *slog.Logger,
) *V1WriteLivenessWorker {
	if interval <= 0 {
		interval = v1WriteLivenessDefaultInterval
	}
	if window <= 0 {
		window = v1WriteLivenessDefaultWindow
	}
	if logger == nil {
		logger = slog.Default()
	}
	w := &V1WriteLivenessWorker{
		probe:    probe,
		interval: interval,
		window:   window,
		logger:   logger,
		stopCh:   make(chan struct{}),
	}
	w.snapshot.Store(V1WriteLivenessSnapshot{Verdict: livenessUnknown})
	return w
}

// Start 启动常驻循环；首 tick 立即执行，端点尽早有数据。
func (w *V1WriteLivenessWorker) Start(ctx context.Context) {
	if w == nil {
		return
	}
	w.startOnce.Do(func() {
		go w.loop(ctx)
		w.logger.Info("v1_write_liveness_worker started",
			"interval", w.interval.String())
	})
}

// Stop 请求终止；重复调用安全。
func (w *V1WriteLivenessWorker) Stop() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() { close(w.stopCh) })
}

// Snapshot 返回最近一次读数（未跑过首 tick 时 verdict=unknown）。
func (w *V1WriteLivenessWorker) Snapshot() V1WriteLivenessSnapshot {
	if w == nil {
		return V1WriteLivenessSnapshot{Verdict: livenessUnknown}
	}
	return w.snapshot.Load().(V1WriteLivenessSnapshot)
}

func (w *V1WriteLivenessWorker) loop(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	w.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

// tick 跑一次读数、更新快照、按策略发信号。信号策略见文件头：
// dead 每 tick 一条 ERROR；恢复一条 WARN；测量失败每 tick 一条 WARN；
// 其余静默。
func (w *V1WriteLivenessWorker) tick(ctx context.Context) {
	queryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	in, verdict, err := w.probe(queryCtx)
	snap := V1WriteLivenessSnapshot{
		CheckedAt:       time.Now(),
		WindowMinutes:   int(w.window.Minutes()),
		V1HotRows:       in.V1HotRows,
		V1ParentRows:    in.V1ParentRows,
		TurnsHotRows:    in.TurnsHotRows,
		TurnsParentRows: in.TurnsParent,
	}
	switch {
	case err != nil:
		snap.Verdict = livenessUnknown
		snap.LastError = err.Error()
		w.logger.Warn("v1_write_liveness: measurement failed (verdict unknown)",
			"error", err)
	case verdict == livenessDead:
		// prev 在 err 分支不参与：测量失败期间保持的「曾经 dead」计数
		// 由 ConsecutiveDead=0 如实清零——快照量的是本 tick 观察到的事实。
		if w.prev == livenessDead {
			snap.ConsecutiveDead = w.snapshot.Load().(V1WriteLivenessSnapshot).ConsecutiveDead + 1
		} else {
			snap.ConsecutiveDead = 1
		}
		snap.Verdict = livenessDead
		w.logger.Error("v1_write_liveness: DEAD — v1 write leg has zero rows while session side is writing",
			"window_minutes", snap.WindowMinutes,
			"v1_hot_rows", snap.V1HotRows,
			"v1_parent_rows", snap.V1ParentRows,
			"turns_hot_rows", snap.TurnsHotRows,
			"turns_parent_rows", snap.TurnsParentRows,
			"consecutive_dead_ticks", snap.ConsecutiveDead)
	case w.prev == livenessDead:
		// 恢复只在离开 dead 的那一 tick 响一次；steady 态静默。
		snap.Verdict = verdict
		w.logger.Warn("v1_write_liveness: recovered from dead",
			"verdict", verdict)
	default:
		snap.Verdict = verdict
	}
	w.prev = snap.Verdict
	w.snapshot.Store(snap)
}

// NewV1WriteLivenessHandler 返回 admin 拉取面。verdict=dead → 503
// （供 curl -f 族零解析告警）；alive/quiet/unknown → 200（unknown 的
// 「测量坏了」由 body 的 last_error 说明，不用 5xx 二次编码——那会让
// 「PG 挂了」和「这条腿挂了」在无 body 消费方看来不可区分）。
func NewV1WriteLivenessHandler(w *V1WriteLivenessWorker) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			rw.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		snap := w.Snapshot()
		rw.Header().Set("Content-Type", "application/json")
		if snap.Verdict == livenessDead {
			rw.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(rw).Encode(snap)
	})
}
