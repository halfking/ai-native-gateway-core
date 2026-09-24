// runner.go —— 2x2 探测调度器 + 历史落库（2026-09-24，
// docs/design/2026-09-23-mock-probe-channel §3.5/§一）。
//
// 每 MockProbeInterval 跑一轮：mock-fast/mock-slow × stream/non-stream
// 共 4 次探测（协议锁定 OpenAI Chat Completions，协议不再是变量）。
// 每次探测双写：先指标（prometheus，实时拉取）再历史
// （mock_probe_history，回溯审计；异步 channel 写入，失败仅记日志）。
//
// 优雅停机：注册为 shutdown.KindNonStream；Stop 先停探测循环、再排空
// 历史写入队列（"先停 runner → 关 mux"，设计 §二）。
package mockprobe

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/internal/observability"
	"github.com/kaixuan/llm-gateway-go/internal/providers/mock"
	"github.com/kaixuan/llm-gateway-go/internal/shutdown"
)

// historyBufferLen 历史写入队列深度。每轮 4 条、默认 30s 一轮，256 深度
// 覆盖约 32 分钟的写入停顿；超限丢弃并计数（设计 §六：PG 慢不阻塞探测）。
const historyBufferLen = 256

// historyInsertTimeout 单条历史 INSERT 的超时。
const historyInsertTimeout = 5 * time.Second

// RunnerID 是注册到 shutdown.Manager 的标识。
const RunnerID = "mock-probe-runner"

// Runner 周期性执行 2x2 mock 探测。
type Runner struct {
	client       *Client
	interval     time.Duration
	threshold    int
	probeTimeout time.Duration
	history      *HistoryStore // 可为 nil（无 DB 部署只打指标）
	mgr          *shutdown.Manager

	stopCh     chan struct{}
	wg         sync.WaitGroup
	stopOnce   sync.Once
	started    atomic.Bool
	failureCnt atomic.Int64 // 累计失败探测数（测试观测用）

	mu      sync.Mutex
	streaks map[string]int // channel → 连续失败次数
}

// NewRunner 构造调度器。interval 由 config 钳制（≥1s）；threshold ≤0 时
// 视为 3（与 config 默认一致）；mgr 可为 nil（不注册停机框架）。
func NewRunner(client *Client, interval time.Duration, threshold int, history *HistoryStore, mgr *shutdown.Manager) *Runner {
	if threshold <= 0 {
		threshold = 3
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Runner{
		client:       client,
		interval:     interval,
		threshold:    threshold,
		probeTimeout: DefaultProbeTimeout,
		history:      history,
		mgr:          mgr,
		stopCh:       make(chan struct{}),
		streaks:      make(map[string]int),
	}
}

// Start 启动探测循环（立即跑第一轮，之后每 interval 一轮）。停机框架已
// 进入关闭状态时拒绝启动并返回 false。
func (r *Runner) Start(ctx context.Context) bool {
	if r.mgr != nil && !r.mgr.Register(shutdown.NonStream, RunnerID) {
		slog.Warn("mock probe runner not started: shutdown already in progress")
		return false
	}
	if !r.started.CompareAndSwap(false, true) {
		return false // 已启动（幂等）
	}
	r.wg.Add(1)
	go r.loop(ctx)
	slog.Info("mock probe runner started",
		"interval", r.interval.String(), "threshold", r.threshold,
		"base_url", r.client.BaseURL(), "history", r.history != nil)
	return true
}

func (r *Runner) loop(ctx context.Context) {
	defer r.wg.Done()
	r.runRound(ctx)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		case <-ticker.C:
			r.runRound(ctx)
		}
	}
}

// runRound 顺序执行 2x2 探测（设计 §一 伪代码顺序：
// mock-fast × {nonstream, stream} → mock-slow × {nonstream, stream}）。
func (r *Runner) runRound(ctx context.Context) {
	for _, supplier := range mock.Suppliers() {
		for _, stream := range []bool{false, true} {
			select {
			case <-r.stopCh:
				return
			default:
			}
			r.probeOne(ctx, supplier, stream)
		}
	}
}

func (r *Runner) probeOne(ctx context.Context, supplier string, stream bool) {
	probeCtx, cancel := context.WithTimeout(ctx, r.probeTimeout)
	res := r.client.Probe(probeCtx, supplier, stream)
	cancel()

	status := "error"
	if res.OK {
		status = "ok"
	}
	streamLabel := "false"
	if res.Stream {
		streamLabel = "true"
	}
	// 双写之一：指标（先写，保证 /metrics 实时可见）。
	observability.MockProbeRequestTotal.WithLabelValues(
		observability.ScopeMockProbe, res.Supplier, streamLabel, status).Inc()
	observability.MockProbeLatencySeconds.WithLabelValues(
		observability.ScopeMockProbe, res.Supplier, streamLabel).Observe(res.Latency.Seconds())

	// 连续失败跟踪 + 阈值告警（MockProbeFailureThreshold）。
	streak := r.trackStreak(res.Channel, res.OK)
	if !res.OK {
		r.failureCnt.Add(1)
		if streak >= r.threshold {
			// 达阈值升级为 Error：通道连续失败，链路（网关自身或配置）异常。
			slog.Error("mock probe channel failing continuously",
				"channel", res.Channel, "failure_streak", streak)
		} else {
			slog.Warn("mock probe failed",
				"channel", res.Channel, "status_code", res.StatusCode,
				"error_code", res.ErrorCode, "latency_ms", res.Latency.Milliseconds())
		}
	}

	// 双写之二：历史表（异步，nil 时跳过）。
	if r.history != nil {
		r.history.Insert(HistoryRecord{
			Channel:       res.Channel,
			Supplier:      res.Supplier,
			Stream:        res.Stream,
			Protocol:      res.Protocol,
			LatencyMs:     int(res.Latency.Milliseconds()),
			StatusCode:    res.StatusCode,
			ErrorCode:     nullIfOK(res.OK, res.ErrorCode),
			RequestID:     res.RequestID,
			FailureStreak: streak,
		})
	}
}

func (r *Runner) trackStreak(channel string, ok bool) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ok {
		r.streaks[channel] = 0
		return 0
	}
	r.streaks[channel]++
	return r.streaks[channel]
}

func nullIfOK(ok bool, errCode string) string {
	if ok || errCode == "" {
		return ""
	}
	return errCode
}

// Stop 停止探测循环并排空历史写入队列（"先停 runner → 关 mux"）。
// 幂等；ctx 控制排空等待上限。
func (r *Runner) Stop(ctx context.Context) {
	r.stopOnce.Do(func() {
		close(r.stopCh)
	})
	r.wg.Wait()
	if r.history != nil {
		r.history.Close(ctx)
	}
	if r.mgr != nil {
		r.mgr.Unregister(shutdown.NonStream, RunnerID)
	}
}

// FailureCount 返回累计失败探测数（测试观测）。
func (r *Runner) FailureCount() int64 { return r.failureCnt.Load() }

// ── 历史落库 ─────────────────────────────────────────────────────────────

// HistoryRecord 与 mock_probe_history 列一一对应（除 id/probe_time 由
// 库端生成）。
type HistoryRecord struct {
	Channel       string
	Supplier      string
	Stream        bool
	Protocol      string
	LatencyMs     int
	StatusCode    int
	ErrorCode     string
	RequestID     string
	FailureStreak int
}

// HistoryStore 把探测记录异步写入 mock_probe_history（migrations/036）。
// Insert 永不阻塞（队列满即丢弃并计数）；Close 排空队列。
type HistoryStore struct {
	pool    *pgxpool.Pool
	ch      chan HistoryRecord
	wg      sync.WaitGroup
	closeMu sync.Mutex
	closed  bool
	dropped atomic.Int64
}

// NewHistoryStore 创建写入器并 best-effort 兜底当日分区（设计 §六：
// "历史表分区未及时建 → 启动时调用 daily_partition"）。建表/建函数失败
// 仅告警——写入侧随后按同样策略逐条失败并记日志，不阻断探测。
func NewHistoryStore(ctx context.Context, pool *pgxpool.Pool) *HistoryStore {
	s := &HistoryStore{
		pool: pool,
		ch:   make(chan HistoryRecord, historyBufferLen),
	}
	if _, err := pool.Exec(ctx, "SELECT mock_probe_history_daily_partition()"); err != nil {
		slog.Warn("mock probe daily partition bootstrap failed (has migrations/036 been applied?)",
			"err", err)
	}
	s.wg.Add(1)
	go s.writeLoop()
	return s
}

// Insert 非阻塞投递一条记录；队列满时丢弃并计数（探测实时性优先）。
func (s *HistoryStore) Insert(rec HistoryRecord) {
	s.closeMu.Lock()
	closed := s.closed
	s.closeMu.Unlock()
	if closed {
		return
	}
	select {
	case s.ch <- rec:
	default:
		n := s.dropped.Add(1)
		if n%100 == 1 { // 首次与之后每 100 次告警一次，防日志风暴
			slog.Warn("mock probe history queue full, dropping records", "dropped_total", n)
		}
	}
}

func (s *HistoryStore) writeLoop() {
	defer s.wg.Done()
	for rec := range s.ch {
		ctx, cancel := context.WithTimeout(context.Background(), historyInsertTimeout)
		_, err := s.pool.Exec(ctx,
			`INSERT INTO mock_probe_history
			   (channel, supplier, stream, protocol, latency_ms, status_code,
			    error_code, request_id, failure_streak)
			 VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,''),NULLIF($8,''),$9)`,
			rec.Channel, rec.Supplier, rec.Stream, rec.Protocol, rec.LatencyMs,
			rec.StatusCode, rec.ErrorCode, rec.RequestID, rec.FailureStreak)
		cancel()
		if err != nil {
			slog.Warn("mock probe history insert failed",
				"channel", rec.Channel, "err", err)
		}
	}
}

// Close 关闭队列并等待在途记录写完（ctx 上限内）。
func (s *HistoryStore) Close(ctx context.Context) {
	s.closeMu.Lock()
	if !s.closed {
		s.closed = true
		close(s.ch)
	}
	s.closeMu.Unlock()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		slog.Warn("mock probe history drain timed out")
	}
}

// Dropped 返回累计丢弃数（测试/运维观测）。
func (s *HistoryStore) Dropped() int64 { return s.dropped.Load() }
