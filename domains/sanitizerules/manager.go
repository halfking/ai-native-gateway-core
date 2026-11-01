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
	// 2026-10-31 24h 审计第 12 轮：批路径修的是「丢事件」，这条修的是丢事件
	// 发生时的告警风暴本身（211 实测 44250 WARN/3h 把其它告警挤出视野）。
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
		// 2026-10-31 24h 审计第 12 轮：256 → 4096。批路径（BatchEventWriter）
		// 已把排水速率提上来，4096 是给「上游 DB 抖动 + 命中风暴」同时发生的
		// 突发余量（≈13 分钟峰值生产量），内存开销可忽略；上限 10000 不变。
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
func (m *Manager) Status() Instance {
	m.statusMu.Lock()
	defer m.statusMu.Unlock()
	inst := m.status
	inst.DroppedEvents = m.dropped.Load()
	return inst
}
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
// 累计计数的管理面出口是 Status().DroppedEvents（第 11 轮遗留收口轮已接）。
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

// DroppedEvents reports audit events that were lost: the ones the full audit
// queue rejected, plus the ones whose write failed (2026-11-01 12h audit
// round — a failed batch write used to lose up to auditWriteBatchSize
// events with no counter anywhere).
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

// auditWriteBatchSize bounds how many queued audit events one background write
// persists per store call. 2026-10-31 211: per-hit observeRule producers outran
// the per-event transaction path (advisory lock + upsert + retention DELETE per
// event), the bounded queue stayed full for hours and 44250 audit events were
// dropped in 3h. Batching restores the drain rate without touching the request
// path or the persistence semantics.
const auditWriteBatchSize = 128

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

// writeBatch persists a drained batch in one store call. Stores implementing
// BatchEventWriter take the whole batch in one transaction; the fallback keeps
// the historical per-event path so third-party Store implementations keep
// working. The live audit switch filters per event, matching writeEvent: a
// disabled switch discards already-queued events at write time.
func (m *Manager) writeBatch(ctx context.Context, batch []Event) {
	if len(batch) == 0 {
		return
	}
	if len(batch) == 1 {
		m.writeEvent(ctx, batch[0])
		return
	}
	events := make([]Event, 0, len(batch))
	for _, e := range batch {
		if m.opts.AuditEnabled != nil && !m.opts.AuditEnabled() {
			continue
		}
		events = append(events, e)
	}
	if len(events) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	limit := 10000
	if m.opts.EventLimit != nil {
		limit = m.opts.EventLimit()
	}
	if writer, ok := m.store.(BatchEventWriter); ok {
		if err := writer.RecordEvents(ctx, events, limit); err != nil {
			// ★ 2026-11-01 12h 审计轮：批写把「一次写失败」的爆炸半径从**单条**
			// ★ 放大到了**整批（≤ auditWriteBatchSize = 128 条）** —— 而批写这一轮
			// ★ 本来就是**为了修「丢事件」**加的 ⇒ ⇒ **失败路径比成功路径更需要
			// ★ 退路** ⇒ ⇒ 退回逐条写：只有真正写不进去的那几条会丢，其余照常
			// ★ 落库 ★。
			//
			// ★ 退路用**独立**的 ctx（不继承上面那个可能已耗尽的批写 ctx）★。
			m.report(fmt.Sprintf("sanitize rule audit batch write failed (%d events), falling back to per-event writes: %v", len(events), err))
			m.writeEventsIndividually(context.WithoutCancel(ctx), events, limit)
		}
		return
	}
	m.writeEventsIndividually(ctx, events, limit)
}

// writeEventsIndividually 逐条落库（批写不可用时的常态路径 / 批写失败后的退路）。
//
// ★ 2026-11-01 12h 审计轮：写失败的那几条**计入 dropped** ★
//
// ★ 为什么必须计数（★ 而不只是打一条日志 ★）：★ 管理面的 DroppedEvents 必须
// ★ 能反映**全部**审计事件损失，而不只是「队列满」那一种 ★★ ⇒ ★★ 否则「写失败」
// ★★ 这条路径丢的事件在管理面上**完全不可见** —— ★★ 它比队列满更隐蔽：队列满
// ★★ 有节流 WARN + 累计计数，写失败此前只有一条**静态串**（没有数量、没有错误
// ★★ 内容）★★ ⇒ ★★ 操作者既不知道丢了多少、也不知道丢在哪 ★★。
func (m *Manager) writeEventsIndividually(ctx context.Context, events []Event, limit int) {
	for _, e := range events {
		if err := m.store.RecordEvent(ctx, e, limit); err != nil {
			m.dropped.Add(1)
			m.report(fmt.Sprintf("sanitize rule audit write failed: %v", err))
		}
	}
}

// drainBatch takes the already-dequeued event plus whatever else is ready in
// the queue without ever blocking: under flood each cycle drains a full batch,
// under sparse traffic it returns immediately with a single event (the old
// single-event shape, zero added latency).
func (m *Manager) drainBatch(first Event) []Event {
	batch := make([]Event, 0, auditWriteBatchSize)
	batch = append(batch, first)
	for len(batch) < auditWriteBatchSize {
		select {
		case e := <-m.queue:
			batch = append(batch, e)
		default:
			return batch
		}
	}
	return batch
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
func (m *Manager) auditWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			drainCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			for {
				select {
				case e := <-m.queue:
					m.writeBatch(drainCtx, m.drainBatch(e))
					if drainCtx.Err() != nil {
						return
					}
				default:
					return
				}
			}
		case e := <-m.queue:
			m.writeBatch(ctx, m.drainBatch(e))
		}
	}
}
