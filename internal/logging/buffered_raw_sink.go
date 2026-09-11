package logging

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/metrics"
)

// BufferedRawSinkConfig 是 BufferedRawSink 的窗口配置（R12，预研 §4）。
// 零值字段取默认：BatchSize=50、FlushEvery=100ms、MaxBytes=4MiB、
// MaxWriteAttempts=3。默认与现网 AsyncRawDataLogger 节奏对齐以便灰度对比；
// 第一版不暴露为环境变量，灰度期用代码默认值（预研 §10 问题 2）。
type BufferedRawSinkConfig struct {
	// BatchSize 是触发预写（wake flush）的缓冲条数阈值。
	BatchSize int
	// FlushEvery 是定时刷盘间隔（决定 fsync 节奏）。
	FlushEvery time.Duration
	// MaxBytes 是缓冲字节预算上限（软 OOM 防线，按估算值计）。
	MaxBytes int
	// MaxWriteAttempts 是单批写失败的重试总次数（含首写），
	// 超限丢弃剩余条目（指数退避）。
	MaxWriteAttempts int
}

// 默认窗口（R12 预研 §4：与现网 Async 的 batchSize=50 / flushDelay=100ms
// 对齐；MaxBytes=4MiB 防高负载下缓冲无限增长）。
const (
	defaultBufferedBatchSize      = 50
	defaultBufferedFlushEvery     = 100 * time.Millisecond
	defaultBufferedMaxBytes       = 4 * 1024 * 1024
	defaultBufferedWriteAttempts  = 3
	bufferedWriteRetryBaseBackoff = 5 * time.Millisecond
)

// BufferedRawSink 是与 AsyncRawDataLogger 平级的第三实现（R12，预研 §4）：
// 满足 RawSink + EnvelopeRawSink + FrameLookup，把「写出 page cache」与
// 「fsync」拆成两个可配置窗口，把持久性/性能做成显式权衡。
//
// 包装而非自建 I/O：底层复用 RawDataLogger 的 writeEntries/rotate/
// cleanupOldFiles/peekPostWriteLocation，杜绝 JSONL 格式与轮转策略双实现
// 漂移。崩溃丢失窗口 L_crash ≈ min(MaxBytes, R×FlushEvery) + flush 期间
// 新增（R=产生速率）；Close 强制排空 + sync。
type BufferedRawSink struct {
	base rawEntryWriter

	flushEvery      time.Duration
	batchSize       int
	maxBytes        int
	maxWriteAttempt int

	// bufMu 包围「检查 closed + 入缓冲」消除关停竞态（预研 §4，沿用
	// AsyncRawDataLogger stateMu 模式；此处入缓冲需独占，故用 Mutex）。
	bufMu    sync.Mutex
	entries  []RawDataEntry
	bufBytes int
	closed   bool
	closeErr error

	// flushMu（R12 审计修正）串行化「换出→写盘→逐帧索引」整段：Sync 与
	// flushWorker 并发刷盘时，各批的反推偏移必须基于本批写完后的游标，
	// 否则 LookupFrame 得到看似合法实则错位的结果（逻辑竞态，-race 不可
	// 检出）。Close 的排空/stub/底层 Sync+Close 同样在其保护内。锁序
	// flushMu → bufMu → baseLogger 内部锁，无反向嵌套。
	flushMu sync.Mutex

	wake   chan struct{} // 容量 1 的非阻塞通知
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	closeOnce sync.Once

	accepted    atomic.Uint64
	dropped     atomic.Uint64
	flushCount  atomic.Uint64
	writeErrors atomic.Uint64

	// lastDropWarn 上次缓冲满告警的 UnixNano，限流（与 Async 对齐，10s 窗口）
	lastDropWarn atomic.Int64

	overflowReporter *LockFreeAnomalyReporter
	frameIndex       rawFrameIndex
}

// BufferedSinkStats 是 BufferedRawSink 专属统计。刻意不伪装 QueueStats
// （预研 §3.3：不伪造 Async 的队列语义）。
type BufferedSinkStats struct {
	Accepted      uint64 // 已入缓冲条目数（含 overflow stub）
	Dropped       uint64 // 无法入缓冲 / 重试超限丢弃条目数
	FlushCount    uint64 // flush 轮次
	WriteErrors   uint64 // 写失败轮次（重试内每次失败各计一次）
	Buffered      int    // 当前缓冲条目数
	BufferedBytes int    // 当前缓冲估算字节数
}

// NewBufferedRawSink 创建缓冲原始数据日志记录器。enabled=false 返回禁用
// 实例（与 RawDataLogger/Async 语义一致，不创建文件）。
func NewBufferedRawSink(baseDir string, maxSize int64, enabled bool, cfg BufferedRawSinkConfig) (*BufferedRawSink, error) {
	base, err := NewRawDataLogger(baseDir, maxSize, enabled)
	if err != nil {
		return nil, err
	}
	return newBufferedRawSinkWithBase(base, cfg), nil
}

// newBufferedRawSinkWithBase 供测试注入 rawEntryWriter stub（写失败注入，
// 预研 §6 测试矩阵）。生产路径走 NewBufferedRawSink。
func newBufferedRawSinkWithBase(base rawEntryWriter, cfg BufferedRawSinkConfig) *BufferedRawSink {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaultBufferedBatchSize
	}
	if cfg.FlushEvery <= 0 {
		cfg.FlushEvery = defaultBufferedFlushEvery
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = defaultBufferedMaxBytes
	}
	if cfg.MaxWriteAttempts <= 0 {
		cfg.MaxWriteAttempts = defaultBufferedWriteAttempts
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &BufferedRawSink{
		base:            base,
		flushEvery:      cfg.FlushEvery,
		batchSize:       cfg.BatchSize,
		maxBytes:        cfg.MaxBytes,
		maxWriteAttempt: cfg.MaxWriteAttempts,
		wake:            make(chan struct{}, 1),
		ctx:             ctx,
		cancel:          cancel,
		done:            make(chan struct{}),
	}
	go s.flushWorker()
	return s
}

// Config 返回规范化后的窗口配置，供启动日志打印核对。
func (s *BufferedRawSink) Config() BufferedRawSinkConfig {
	return BufferedRawSinkConfig{
		BatchSize:        s.batchSize,
		FlushEvery:       s.flushEvery,
		MaxBytes:         s.maxBytes,
		MaxWriteAttempts: s.maxWriteAttempt,
	}
}

// SetOverflowReporter wires an anomaly reporter that is invoked on
// buffer overflow (raw_log_overflow) and on close-with-remaining-items
// (raw_log_close_drained)。语义与 AsyncRawDataLogger.SetOverflowReporter
// 对齐；nil 禁用两个钩子。
func (s *BufferedRawSink) SetOverflowReporter(rep *LockFreeAnomalyReporter) {
	if s == nil {
		return
	}
	s.bufMu.Lock()
	s.overflowReporter = rep
	s.bufMu.Unlock()
}

// LogClientRequest 缓冲记录客户端请求
func (s *BufferedRawSink) LogClientRequest(requestID, protocol string, body []byte, headers map[string]string, conversionStep string) {
	s.LogClientRequestWithEnvelope(requestID, protocol, body, headers, conversionStep, RawCorrelationEnvelope{})
}

func (s *BufferedRawSink) LogClientRequestWithEnvelope(requestID, protocol string, body []byte, headers map[string]string, conversionStep string, env RawCorrelationEnvelope) {
	s.offerWithEnvelope("client_request", requestID, protocol, body, headers, conversionStep, env)
}

// LogUpstreamRequest 缓冲记录上游请求
func (s *BufferedRawSink) LogUpstreamRequest(requestID, protocol string, body []byte, conversionStep string) {
	s.LogUpstreamRequestWithEnvelope(requestID, protocol, body, conversionStep, RawCorrelationEnvelope{})
}

func (s *BufferedRawSink) LogUpstreamRequestWithEnvelope(requestID, protocol string, body []byte, conversionStep string, env RawCorrelationEnvelope) {
	s.offerWithEnvelope("upstream_request", requestID, protocol, body, nil, conversionStep, env)
}

// LogUpstreamResponse 缓冲记录上游响应
func (s *BufferedRawSink) LogUpstreamResponse(requestID, protocol string, body []byte, conversionStep string) {
	s.LogUpstreamResponseWithEnvelope(requestID, protocol, body, conversionStep, RawCorrelationEnvelope{})
}

func (s *BufferedRawSink) LogUpstreamResponseWithEnvelope(requestID, protocol string, body []byte, conversionStep string, env RawCorrelationEnvelope) {
	s.offerWithEnvelope("upstream_response", requestID, protocol, body, nil, conversionStep, env)
}

// LogClientResponse 缓冲记录客户端响应
func (s *BufferedRawSink) LogClientResponse(requestID, protocol string, body []byte, conversionStep string) {
	s.LogClientResponseWithEnvelope(requestID, protocol, body, conversionStep, RawCorrelationEnvelope{})
}

func (s *BufferedRawSink) LogClientResponseWithEnvelope(requestID, protocol string, body []byte, conversionStep string, env RawCorrelationEnvelope) {
	s.offerWithEnvelope("client_response", requestID, protocol, body, nil, conversionStep, env)
}

// LogConversionError 缓冲记录转换错误
func (s *BufferedRawSink) LogConversionError(requestID, protocol, direction, step string, body []byte, err error) {
	s.bufMu.Lock()
	if s.closed || s.base == nil || !s.base.sinkEnabled() {
		s.bufMu.Unlock()
		return
	}
	entry := makeRawEntry(direction, requestID, protocol, body, nil, step, RawCorrelationEnvelope{})
	entry.Error = err.Error()
	dropped := s.offerLocked(entry, requestID, direction, protocol, step, RawCorrelationEnvelope{}, err.Error())
	rep := s.overflowReporter
	s.bufMu.Unlock()
	if dropped {
		s.noteDroppedEntry(requestID, "error", RawCorrelationEnvelope{}, rep)
	}
}

// offerWithEnvelope 统一的入缓冲入口：closed/禁用早退（沿用 Async
// stateMu 包围模式），成功路径凑满 batchSize 以非阻塞 wake 预刷。
func (s *BufferedRawSink) offerWithEnvelope(direction, requestID, protocol string, body []byte, headers map[string]string, conversionStep string, env RawCorrelationEnvelope) {
	s.bufMu.Lock()
	if s.closed || s.base == nil || !s.base.sinkEnabled() {
		s.bufMu.Unlock()
		return
	}
	entry := makeRawEntry(direction, requestID, protocol, body, headers, conversionStep, env)
	dropped := s.offerLocked(entry, requestID, direction, protocol, conversionStep, env, "")
	rep := s.overflowReporter
	s.bufMu.Unlock()
	if dropped {
		s.noteDroppedEntry(requestID, direction, env, rep)
	}
}

// offerLocked 入缓冲（调用方必须持有 bufMu）。缓冲字节预算不足时改写为
// overflow stub（与 Async 队列满语义对齐：原 payload 丢弃、信封保留）；
// stub 也放不下则丢弃并返回 true，由调用方在锁外做限频告警 + anomaly 上报。
// stubErr 非空时覆写 stub 的 Error（conversion error 路径与 Async 对齐：
// 用原始错误文本替代 queue-full marker）；Headers 非空时随 stub 保留，
// client_request 方向保留原始 DataSize（R12 审计奇偶性修正，与 Async
// 各方向 stub 字段一一对应）。
func (s *BufferedRawSink) offerLocked(entry RawDataEntry, requestID, direction, protocol, conversionStep string, env RawCorrelationEnvelope, stubErr string) bool {
	est := estimateEntryBytes(entry)
	if s.bufBytes+est <= s.maxBytes {
		s.entries = append(s.entries, entry)
		s.bufBytes += est
		s.accepted.Add(1)
		if len(s.entries) >= s.batchSize {
			s.signalWakeLocked()
		}
		return false
	}
	stub := makeOverflowStub(direction, requestID, protocol, conversionStep, env)
	stub.Headers = entry.Headers
	if direction == "client_request" {
		stub.DataSize = entry.DataSize
	}
	if stubErr != "" {
		stub.Error = stubErr
	}
	estStub := estimateEntryBytes(stub)
	if s.bufBytes+estStub <= s.maxBytes {
		s.entries = append(s.entries, stub)
		s.bufBytes += estStub
		s.accepted.Add(1)
		return false
	}
	s.dropped.Add(1)
	return true
}

func (s *BufferedRawSink) signalWakeLocked() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// noteDroppedEntry 丢弃限频告警 + 可选 anomaly 上报。rep 由调用方在锁内
// 捕获，HTTP 上报绝不在 bufMu 内进行（Async noteDroppedEntry 的无锁读在
// 这里收敛为快照传参）。
func (s *BufferedRawSink) noteDroppedEntry(requestID, direction string, env RawCorrelationEnvelope, rep *LockFreeAnomalyReporter) {
	now := time.Now().UnixNano()
	last := s.lastDropWarn.Load()
	if now-last < int64(dropWarnInterval) {
		return
	}
	if !s.lastDropWarn.CompareAndSwap(last, now) {
		return
	}
	slog.Warn("buffered_raw_sink: buffer full, dropping log entries",
		"request_id", requestID,
		"direction", direction,
		"dropped_total", s.dropped.Load())
	if rep != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		rep.ReportRawLogOverflow(ctx, env, s.dropped.Load())
	}
}

// estimateEntryBytes 估算条目 JSONL 行字节数。MaxBytes 是软 OOM 防线，
// 入缓冲路径不值得为精确值做一次 json.Marshal（热路径编码成本翻倍），
// 以 base64 主体长度 + 信封字段 + 固定余量估算即可。
func estimateEntryBytes(e RawDataEntry) int {
	n := len(e.RawData)
	n += len(e.Error) + len(e.Reason) + len(e.ConversionStep) + len(e.Protocol) + len(e.RequestID)
	n += len(e.Headers) * 96
	n += 512 // 时间戳 + envelope 各字段 + JSON 结构余量
	return n + 1
}

// flushWorker 后台刷新协程（ticker/wake/stop 三路 select；退出前补一次
// 刷盘，与 Async flushWorker 对齐）。
func (s *BufferedRawSink) flushWorker() {
	defer close(s.done)
	ticker := time.NewTicker(s.flushEvery)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			s.flushBuffered()
			return
		case <-s.wake:
			s.flushBuffered()
		case <-ticker.C:
			s.flushBuffered()
		}
	}
}

// flushBuffered 排空式刷盘：在 bufMu 下整体换出缓冲，一次写出（单次
// fsync，fsync 节奏由此由 FlushEvery 决定——这正是 Buffered 与 Async 的
// 差异点），随后更新逐帧索引。worker tick/wake、Sync、Close 共用。
// 整段持 flushMu（审计修正）：换出与写盘/索引的顺序对并发 flusher 唯一化，
// 反推偏移不被并发批写盘污染。
func (s *BufferedRawSink) flushBuffered() {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	s.flushBufferedLocked()
}

func (s *BufferedRawSink) flushBufferedLocked() {
	s.bufMu.Lock()
	if len(s.entries) == 0 {
		s.bufMu.Unlock()
		return
	}
	batch := s.entries
	s.entries = nil
	s.bufBytes = 0
	s.bufMu.Unlock()

	s.writeWithRetry(batch)
	s.flushCount.Add(1)
	s.frameIndex.record(batch, s.base.peekPostWriteLocation)
}

// writeWithRetry 带有限重试的写出（预研 §4 写失败降级）：对尚未写出的
// 剩余条目按指数退避重试；超限丢弃 + dropped 计数 + metric + 告警，不
// 阻塞热路径（本方法只在 worker/Sync/Close 上下文运行）。全批已写出时
// （如仅 fsync 失败）不重试，避免重复写已落盘条目。
func (s *BufferedRawSink) writeWithRetry(batch []RawDataEntry) {
	remaining := batch
	backoff := bufferedWriteRetryBaseBackoff
	for attempt := 1; ; attempt++ {
		n, err := s.base.writeEntriesFallible(remaining)
		if err == nil {
			return
		}
		s.writeErrors.Add(1)
		remaining = remaining[n:]
		if len(remaining) == 0 {
			return
		}
		if attempt >= s.maxWriteAttempt {
			s.dropped.Add(uint64(len(remaining)))
			metrics.Global().RecordRawAuditWriteFailure()
			slog.Error("buffered_raw_sink: write failed, dropping batch remainder",
				"dropped", len(remaining),
				"attempts", attempt,
				"err", err)
			return
		}
		time.Sleep(backoff)
		backoff *= 2
	}
}

// Sync 持久化屏障（RawSink 契约，预研 §4）：在 bufMu 下排空缓冲后对底层
// 文件 fsync。调用前已接受的条目与入缓冲共用 bufMu，构成 happens-before；
// 随后的 base.Sync() 与仍在途的 worker 批次经底层互斥锁串行，保证其在本
// 调用返回前完成。相比「向 worker 发 flush 请求等待完成」的形态，这里
// 直排缓冲同样满足屏障语义，且不存在关停期 worker 退出导致的应答死锁窗。
func (s *BufferedRawSink) Sync() error {
	s.bufMu.Lock()
	closed := s.closed
	base := s.base
	enabled := base != nil && base.sinkEnabled()
	s.bufMu.Unlock()
	if closed || !enabled {
		return nil
	}
	s.flushBuffered()
	return base.Sync()
}

// Close 幂等关停（RawSink 契约，预研 §4）：置位 closed → 停 worker →
// 排空缓冲 → 写 close_drained stub（与 Async 现网行为对齐，预研 §8：
// buffered 灰度硬前置）→ base.Sync → base.Close。首次调用记住错误，
// 后续调用返回同一错误。
//
// R12 审计修正：排空批同样写入逐帧索引（与 Async 的 Close 经 flushBatch
// 被索引保持对称），整段持 flushMu 与并发 Sync/worker 串行。
// raw_log_close_drained anomaly 在 Buffered 结构上不可触达：closed 置位与
// 缓冲换出在同一 bufMu 临界区完成，此后 offer 全部早退，不存在"关停后
// 仍有残留"路径——stub 由 Accepted>0 触发，等价能力不缺失。
func (s *BufferedRawSink) Close() error {
	s.closeOnce.Do(func() {
		s.bufMu.Lock()
		if s.closed {
			s.bufMu.Unlock()
			return
		}
		s.closed = true
		batch := s.entries
		s.entries = nil
		s.bufBytes = 0
		s.bufMu.Unlock()

		s.cancel()
		<-s.done // worker 退出（其退出前补刷的缓冲已被换出，为 no-op）

		// 与并发 Sync/worker 的刷盘段串行（含 stub 与底层 Sync/Close）。
		s.flushMu.Lock()
		defer s.flushMu.Unlock()

		if s.base == nil {
			return
		}
		if s.base.sinkEnabled() {
			if len(batch) > 0 {
				s.writeWithRetry(batch)
				s.frameIndex.record(batch, s.base.peekPostWriteLocation)
			}
			stats := s.Stats()
			if stats.Accepted > 0 {
				closeEntry := RawDataEntry{
					Timestamp:       time.Now(),
					RequestID:       "raw_logger",
					Direction:       "close_drained",
					Protocol:        "raw_logger",
					DataSize:        stats.Buffered,
					ConversionStep:  "shutdown",
					Error:           fmt.Sprintf("accepted=%d dropped=%d flushed=%d remaining=%d write_errors=%d", stats.Accepted, stats.Dropped, stats.FlushCount, stats.Buffered, stats.WriteErrors),
					RawDataEncoding: "json",
				}
				s.base.writeEntry(closeEntry)
			}
			if err := s.base.Sync(); err != nil && s.closeErr == nil {
				s.closeErr = err
			}
		}
		if err := s.base.Close(); err != nil && s.closeErr == nil {
			s.closeErr = err
		}
	})
	return s.closeErr
}

// LookupFrame 返回 (requestID, direction) 最近一次已落盘条目的位置
// （FrameLookup 能力，Buffered 灰度硬前置，预研 §10 问题 4）。
func (s *BufferedRawSink) LookupFrame(requestID, direction string) (file string, offset int64, ok bool) {
	return s.frameIndex.lookup(requestID, direction)
}

// Stats 返回缓冲统计（Buffered 专属，不伪装 QueueStats）。
func (s *BufferedRawSink) Stats() BufferedSinkStats {
	s.bufMu.Lock()
	defer s.bufMu.Unlock()
	return BufferedSinkStats{
		Accepted:      s.accepted.Load(),
		Dropped:       s.dropped.Load(),
		FlushCount:    s.flushCount.Load(),
		WriteErrors:   s.writeErrors.Load(),
		Buffered:      len(s.entries),
		BufferedBytes: s.bufBytes,
	}
}

// CurrentLocation 转发底层执行器，只反映已 flush 条目（RawSink 契约）；
// disabled / 已关闭返回空值（与 Async 对齐）。
func (s *BufferedRawSink) CurrentLocation() (file string, offset int64) {
	s.bufMu.Lock()
	defer s.bufMu.Unlock()
	if s.closed || s.base == nil || !s.base.sinkEnabled() {
		return "", 0
	}
	return s.base.CurrentLocation()
}

// HasFile 报告底层执行器是否有打开的审计文件（RawSink 契约）。
func (s *BufferedRawSink) HasFile() bool {
	s.bufMu.Lock()
	defer s.bufMu.Unlock()
	if s.closed || s.base == nil || !s.base.sinkEnabled() {
		return false
	}
	return s.base.HasFile()
}
