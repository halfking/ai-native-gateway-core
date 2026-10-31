package sanitizerules

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type snapshotKey struct{}
type Manager struct {
	store     Store
	opts      Options
	fallback  Config
	current   atomic.Pointer[Snapshot]
	refreshMu sync.Mutex
	statusMu  sync.Mutex
	status    Instance
	queue     chan Event
	errors    chan string
	dropped   atomic.Int64
	// lastQueueFullReport 是 queue-full WARN 的节流戳（unix nanos；0 = 未报过）。
	lastQueueFullReport atomic.Int64
	running             atomic.Bool
	stopped             atomic.Bool
}

func NewManager(store Store, fallback Config, opts Options) (*Manager, error) {
	if store == nil {
		return nil, errors.New("sanitize rule store is required")
	}
	snap, err := Compile(fallback, 0)
	if err != nil {
		return nil, err
	}
	if opts.InstanceID == "" {
		host, _ := os.Hostname()
		host = strings.Map(func(r rune) rune {
			if r > 127 || r == ' ' || r == '=' {
				return '-'
			}
			return r
		}, host)
		if len(host) > 60 {
			host = host[:60]
		}
		var nonce [8]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return nil, errors.New("sanitize instance identity unavailable")
		}
		opts.InstanceID = fmt.Sprintf("%s:%d:%s", host, os.Getpid(), hex.EncodeToString(nonce[:]))
	}
	if !validID(opts.InstanceID) {
		return nil, fmt.Errorf("%w: invalid instance id", ErrInvalid)
	}
	size := opts.QueueSize
	if size <= 0 {
		// 2026-10-31 24h 审计第 12 轮：256 → 4096。输出合规上线后生产峰值
		// ~5 条/秒，256 的余量在单轮排水抖动下不到一分钟就灌满 ⇒ 审计事件
		// 成批丢弃。4096 ≈ 13 分钟的峰值生产量，内存开销可忽略。
		size = 4096
	}
	if size > 10000 {
		size = 10000
	}
	m := &Manager{store: store, opts: opts, fallback: cloneConfig(fallback), queue: make(chan Event, size), errors: make(chan string, 16), status: Instance{InstanceID: opts.InstanceID, ConfigDigest: snap.ConfigDigest}}
	m.current.Store(snap)
	return m, nil
}
func (m *Manager) Current() *Snapshot { return m.current.Load() }
func (m *Manager) Store() Store       { return m.store }
func (m *Manager) SnapshotFor(ctx context.Context) *Snapshot {
	if pinned, ok := ctx.Value(snapshotKey{}).(*Snapshot); ok {
		return pinned
	}
	return m.Current()
}
func (m *Manager) Pin(ctx context.Context) context.Context {
	if _, ok := ctx.Value(snapshotKey{}).(*Snapshot); ok {
		return ctx
	}
	return context.WithValue(ctx, snapshotKey{}, m.Current())
}
func (m *Manager) Status() Instance { m.statusMu.Lock(); defer m.statusMu.Unlock(); return m.status }
func (m *Manager) report(message string) {
	if m.opts.OnError != nil {
		m.opts.OnError(errors.New(message))
	}
}
func (m *Manager) setStatus(errText string, refreshed bool) {
	now := time.Now().UTC()
	snap := m.Current()
	m.statusMu.Lock()
	m.status.Version = snap.Version
	m.status.ConfigDigest = snap.ConfigDigest
	m.status.LastSeen = now
	m.status.Error = errText
	if refreshed {
		m.status.LastRefresh = now
	}
	m.statusMu.Unlock()
}

// Refresh serializes the read/compile/swap sequence so a slow read cannot replace
// a newer snapshot. A missing head is initialized with one immutable DB baseline.
func (m *Manager) Refresh(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	v, err := m.store.Current(ctx)
	if err == nil && v.ID == 0 {
		baseline := m.fallback
		if m.opts.BaselineLoader != nil {
			baseline, err = m.opts.BaselineLoader(ctx)
		}
		if err == nil {
			v, err = m.store.Publish(ctx, 0, baseline, "system:baseline", "Initial built-in policy", 0)
		}
		if errors.Is(err, ErrConflict) {
			v, err = m.store.Current(ctx)
		}
	}
	if err != nil {
		m.setStatus("rule refresh failed", false)
		m.report("sanitize rule refresh failed")
		m.heartbeat(ctx)
		return errors.New("sanitize rule refresh failed")
	}
	if v.ID <= 0 {
		m.setStatus("rule refresh failed", false)
		m.heartbeat(ctx)
		return errors.New("sanitize rule head unavailable")
	}
	current := m.Current()
	if v.ID < current.Version {
		m.setStatus("rule head is older than applied version", false)
		m.heartbeat(ctx)
		return errors.New("sanitize rule head regression")
	}
	if v.ID != current.Version {
		snap, compileErr := Compile(v.Config, v.ID)
		if compileErr != nil {
			m.setStatus("published rule validation failed", false)
			m.report("sanitize published rule validation failed")
			m.heartbeat(ctx)
			return errors.New("sanitize published rule validation failed")
		}
		m.current.Store(snap)
	}
	m.setStatus("", true)
	return m.heartbeat(ctx)
}
func (m *Manager) heartbeat(ctx context.Context) error {
	err := m.store.Heartbeat(ctx, m.Status())
	if err != nil {
		m.setStatus("rule heartbeat failed", false)
		m.report("sanitize rule heartbeat failed")
		return errors.New("sanitize rule heartbeat failed")
	}
	return nil
}
func (m *Manager) Publish(ctx context.Context, base int64, cfg Config, actor, comment string, links ...ReviewLink) (Version, error) {
	if base <= 0 {
		return Version{}, ErrConflict
	}
	if err := validatePublish(cfg, actor, comment, 0); err != nil {
		return Version{}, err
	}
	v, err := m.store.Publish(ctx, base, cfg, actor, comment, 0, links...)
	if err != nil {
		return Version{}, err
	}
	m.afterPublish(ctx, v.ID)
	return v, nil
}
func (m *Manager) Rollback(ctx context.Context, base, target int64, actor, comment string) (Version, error) {
	if target <= 0 {
		return Version{}, fmt.Errorf("%w: rollback target must be a published version", ErrInvalid)
	}
	old, err := m.store.GetVersion(ctx, target)
	if err != nil {
		return Version{}, err
	}
	v, err := m.store.Publish(ctx, base, old.Config, actor, comment, target)
	if err != nil {
		return Version{}, err
	}
	m.afterPublish(ctx, v.ID)
	return v, nil
}
func (m *Manager) afterPublish(ctx context.Context, id int64) {
	_ = m.Refresh(ctx)
	if m.opts.Notifications != nil {
		if err := m.opts.Notifications.Publish(ctx, id); err != nil {
			m.report("sanitize rule notification failed")
		}
	}
}

// Record accepts metadata into a bounded queue. It never performs a DB operation
// or reads dynamic settings. The background writer applies the live audit switch.
func (m *Manager) Record(ctx context.Context, e Event) bool {
	if ctx.Err() != nil || m.stopped.Load() {
		return false
	}
	clean, err := prepareEvent(e)
	if err != nil {
		m.queueError("sanitize rule event metadata invalid")
		return false
	}
	select {
	case m.queue <- clean:
		return true
	default:
		total := m.dropped.Add(1)
		m.reportQueueFull(total)
		return false
	}
}

// queueFullReportInterval 抑制「每丢一条就告警一条」的自我刷屏：丢事件本身
// 已经是审计损失，再用同频 WARN 淹没告警面是第二次损失（211 实测 44250 条
// WARN/3h ≈ 同额事件被丢，把其它告警全部挤出视野）。改为至多 30s 一条，
// 且消息带上累计 dropped_total —— 幅度进告警面，而不是只留翻日志才知道。
const queueFullReportInterval = 30 * time.Second

func (m *Manager) reportQueueFull(total int64) {
	now := time.Now().UnixNano()
	last := m.lastQueueFullReport.Load()
	if last != 0 && now-last < int64(queueFullReportInterval) {
		return
	}
	if !m.lastQueueFullReport.CompareAndSwap(last, now) {
		return
	}
	m.queueError(fmt.Sprintf("sanitize rule audit queue full: dropped_total=%d (throttled to one line per %s)", total, queueFullReportInterval))
}
func (m *Manager) queueError(message string) {
	select {
	case m.errors <- message:
	default:
	}
}

// DroppedEvents reports metadata records rejected by the full audit queue.
func (m *Manager) DroppedEvents() int64 { return m.dropped.Load() }
func (m *Manager) pollInterval() time.Duration {
	d := 30 * time.Second
	if m.opts.PollInterval != nil {
		d = m.opts.PollInterval()
	}
	if d < 10*time.Millisecond {
		d = 10 * time.Millisecond
	}
	if d > 5*time.Minute {
		d = 5 * time.Minute
	}
	return d
}
func (m *Manager) writeEvent(ctx context.Context, e Event) {
	// Settings can read the database. They belong to this bounded background
	// worker; Record must never wait on a settings query in the request path.
	if m.opts.AuditEnabled != nil && !m.opts.AuditEnabled() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	limit := 10000
	if m.opts.EventLimit != nil {
		limit = m.opts.EventLimit()
	}
	if err := m.store.RecordEvent(ctx, e, limit); err != nil {
		m.report("sanitize rule audit write failed")
	}
}

// auditBatchSize / auditBatchLinger：每轮排水最多合并多少条、为等突发在队列
// 空时最多逗留多久。审计数据不要求低延迟，逗留换事务次数的下降（211 生产
// 峰值下每批 10~50 条 ⇒ 每批锁/保留清理只付一次）。
const (
	auditBatchSize   = 128
	auditBatchLinger = 100 * time.Millisecond
)

// collectBatch 从队列继续收事件凑批。linger=false（停机排水）只收当前已在
// 队列里的，不追加等待 —— 停机预算（2s）是硬的；linger=true 时队列暂时空
// 也最多等 auditBatchLinger，让突发流量合并进同一事务。
func (m *Manager) collectBatch(first Event, linger bool) []Event {
	batch := append(make([]Event, 0, auditBatchSize), first)
	for len(batch) < auditBatchSize {
		if !linger {
			select {
			case e := <-m.queue:
				batch = append(batch, e)
				continue
			default:
			}
			return batch
		}
		timer := time.NewTimer(auditBatchLinger)
		select {
		case e := <-m.queue:
			timer.Stop()
			batch = append(batch, e)
		case <-timer.C:
			return batch
		}
	}
	return batch
}

// writeBatch 写一批审计事件：batcher 存在时整批一个事务（一次 settings 读、
// 一次保留清理），否则退回逐条 —— 外部 Store 实现与测试 spy 未实现
// BatchEventStore 时行为与旧版完全一致。
func (m *Manager) writeBatch(ctx context.Context, batcher BatchEventStore, batch []Event) {
	if len(batch) == 0 {
		return
	}
	if batcher == nil {
		for _, e := range batch {
			m.writeEvent(ctx, e)
		}
		return
	}
	if m.opts.AuditEnabled != nil && !m.opts.AuditEnabled() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	limit := 10000
	if m.opts.EventLimit != nil {
		limit = m.opts.EventLimit()
	}
	if err := batcher.RecordEvents(ctx, batch, limit); err != nil {
		m.report("sanitize rule audit write failed")
	}
}

func (m *Manager) auditWorker(ctx context.Context) {
	batcher, canBatch := m.store.(BatchEventStore)
	flush := func(writeCtx context.Context, first Event, linger bool) {
		if canBatch {
			m.writeBatch(writeCtx, batcher, m.collectBatch(first, linger))
			return
		}
		m.writeEvent(writeCtx, first)
	}
	for {
		select {
		case <-ctx.Done():
			drainCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			for {
				select {
				case e := <-m.queue:
					flush(drainCtx, e, false)
					if drainCtx.Err() != nil {
						return
					}
				default:
					return
				}
			}
		case e := <-m.queue:
			flush(ctx, e, true)
		}
	}
}

// Run owns background polling and bounded audit writes until cancellation. Audit
// persistence has its own worker so a slow write cannot delay policy convergence.
// OnError is called from background/control operations and must return promptly.
func (m *Manager) Run(ctx context.Context) {
	if !m.running.CompareAndSwap(false, true) {
		m.report("sanitize rule manager already running")
		return
	}
	auditDone := make(chan struct{})
	go func() { defer close(auditDone); m.auditWorker(ctx) }()
	defer func() { m.stopped.Store(true); <-auditDone }()

	var events <-chan int64
	type subscriptionResult struct {
		events <-chan int64
		err    error
	}
	subscribed := make(chan subscriptionResult, 1)
	pending := false
	subscribe := func() {
		if events != nil || pending || m.opts.Notifications == nil {
			return
		}
		pending = true
		go func() {
			ch, err := m.opts.Notifications.Subscribe(ctx)
			select {
			case subscribed <- subscriptionResult{ch, err}:
			case <-ctx.Done():
			}
		}()
	}

	_ = m.Refresh(ctx)
	subscribe()
	timer := time.NewTimer(m.pollInterval())
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			m.stopped.Store(true)
			return
		case result := <-subscribed:
			pending = false
			if result.err != nil {
				m.report("sanitize rule subscription failed")
			} else {
				events = result.events
			}
		case message := <-m.errors:
			m.report(message)
		case _, open := <-events:
			if !open {
				events = nil
			} else {
				_ = m.Refresh(ctx)
			}
		case <-timer.C:
			_ = m.Refresh(ctx)
			subscribe()
			timer.Reset(m.pollInterval())
		}
	}
}
