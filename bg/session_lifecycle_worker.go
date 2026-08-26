// bg/session_lifecycle_worker.go — Session Lifecycle Background Worker
//
// 周期性扫描不活跃会话并按配置策略回收（软关闭/仅通知）。
// 参照 bg/session_health_worker.go 的设计骨架。
//
// 适用场景：
//   - 会话超过 idle.timeout 未活跃 → 软关闭 (status='closed')
//   - 会话超过 absolute_max_lifetime → 强制关闭
//   - 单租户超过 max_sessions_per_tenant → 按 eviction_policy 驱逐最旧
//
// 配置项（来自 settings/spec_modules.go session_inspector.*）：
//   - idle.timeout              默认 30m
//   - idle.absolute_max_lifetime 默认 168h (7d)
//   - idle.cleanup_interval     默认 5m
//   - idle.cleanup_batch_size   默认 500
//   - idle.recycle_action       soft_close | notify_only
//   - lifecycle.max_sessions_per_tenant
//   - lifecycle.eviction_policy lru | fifo | none
//
// 接入点：cmd/gateway/main.go 与 cmd/gateway-v2/main.go 在 init bg services 时
//         构造 + Start。

package bg

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// sessionLifecycleMetrics 暴露给 Prometheus 的计数器。
var sessionLifecycleMetrics = struct {
	recycled     *prometheus.CounterVec
	softClosed   prometheus.Counter
	notified     prometheus.Counter
	evicted      *prometheus.CounterVec
	scanDuration prometheus.Histogram
	scanErrors   prometheus.Counter
}{
	recycled: promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_session_lifecycle_recycled_total",
			Help: "Total sessions recycled by the session lifecycle worker",
		},
		[]string{"reason"}, // idle | absolute_lifetime | evicted
	),
	softClosed: promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "llmgw_session_lifecycle_soft_closed_total",
			Help: "Total sessions soft-closed (status=closed)",
		},
	),
	notified: promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "llmgw_session_lifecycle_notified_total",
			Help: "Total sessions flagged for notification only",
		},
	),
	evicted: promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_session_lifecycle_evicted_total",
			Help: "Total sessions evicted due to per-tenant limits",
		},
		[]string{"policy"}, // lru | fifo
	),
	scanDuration: promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "llmgw_session_lifecycle_scan_duration_seconds",
			Help:    "Duration of a single lifecycle scan",
			Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 5, 30},
		},
	),
	scanErrors: promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "llmgw_session_lifecycle_scan_errors_total",
			Help: "Total scan errors in the session lifecycle worker",
		},
	),
}

// LifecycleEventPublisher 是可选的事件发布接口（注入后软关闭/驱逐会发布事件）。
// 定义为 interface 避免与 eventbus 包形成循环依赖。
type LifecycleEventPublisher interface {
	Publish(event LifecycleEvent) error
}

// LifecycleEvent 是 worker 暴露的最小事件接口。
// sessioninspector.SessionInspectorRecycleEvent 实现了此接口。
type LifecycleEvent interface {
	Type() string
	Timestamp() time.Time
}

// SessionLifecycleWorker 后台定期回收不活跃/超期会话。
type SessionLifecycleWorker struct {
	db     *pgxpool.Pool
	bus    LifecycleEventPublisher
	cancel context.CancelFunc
	done   chan struct{}

	// 可覆盖的配置（测试时使用）
	idleTimeout        time.Duration
	absoluteMaxLifetime time.Duration
	cleanupInterval    time.Duration
	cleanupBatchSize   int
	recycleAction      string
	maxPerTenant       int
	evictionPolicy     string
}

// SessionLifecycleWorkerOption 函数式选项。
type SessionLifecycleWorkerOption func(*SessionLifecycleWorker)

// WithRecycleConfig 覆盖回收配置（测试用）。
func WithRecycleConfig(idleTimeout, absoluteMax, cleanupInterval time.Duration, batchSize int, action string) SessionLifecycleWorkerOption {
	return func(w *SessionLifecycleWorker) {
		w.idleTimeout = idleTimeout
		w.absoluteMaxLifetime = absoluteMax
		w.cleanupInterval = cleanupInterval
		w.cleanupBatchSize = batchSize
		w.recycleAction = action
	}
}

// WithEventBus 注入事件总线。
func WithEventBus(bus LifecycleEventPublisher) SessionLifecycleWorkerOption {
	return func(w *SessionLifecycleWorker) {
		w.bus = bus
	}
}

// NewSessionLifecycleWorker 构造 worker。
// 默认配置从 spec 派生，可通过 Option 覆盖。
func NewSessionLifecycleWorker(db *pgxpool.Pool, opts ...SessionLifecycleWorkerOption) *SessionLifecycleWorker {
	w := &SessionLifecycleWorker{
		db:                 db,
		done:               make(chan struct{}),
		idleTimeout:        30 * time.Minute,
		absoluteMaxLifetime: 168 * time.Hour,
		cleanupInterval:    5 * time.Minute,
		cleanupBatchSize:   500,
		recycleAction:      "soft_close",
		maxPerTenant:       1000,
		evictionPolicy:     "lru",
	}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

// Start 启动后台 goroutine。
func (w *SessionLifecycleWorker) Start(ctx context.Context) {
	if w == nil {
		return
	}
	ctx, w.cancel = context.WithCancel(ctx)
	go w.run(ctx)
	slog.Info("session lifecycle worker started",
		"cleanup_interval", w.cleanupInterval,
		"idle_timeout", w.idleTimeout,
		"absolute_max_lifetime", w.absoluteMaxLifetime,
		"recycle_action", w.recycleAction)
}

// Stop 取消并等待 goroutine 退出。
func (w *SessionLifecycleWorker) Stop() {
	if w == nil || w.cancel == nil {
		return
	}
	w.cancel()
	<-w.done
}

func (w *SessionLifecycleWorker) run(ctx context.Context) {
	defer close(w.done)
	ticker := time.NewTicker(w.cleanupInterval)
	defer ticker.Stop()

	// 启动后立即执行一次扫描（运维可观察）
	w.sweep(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.sweep(ctx)
		}
	}
}

// sweep 执行一次完整扫描：处理三类回收场景。
//  1) 闲置超时（status='active' AND last_active_at < NOW() - $idle）
//  2) 绝对超期（status='active' AND created_at < NOW() - $abs）
//  3) 租户超限（每个 tenant 单独处理）
func (w *SessionLifecycleWorker) sweep(ctx context.Context) {
	start := time.Now()
	defer func() {
		sessionLifecycleMetrics.scanDuration.Observe(time.Since(start).Seconds())
	}()

	sweepCtx, cancel := context.WithTimeout(ctx, 2*w.cleanupInterval)
	defer cancel()

	// 1) 闲置超时
	idleRecycled, err := w.recycleIdle(sweepCtx)
	if err != nil {
		slog.Warn("session_lifecycle: recycle idle failed", "error", err)
		sessionLifecycleMetrics.scanErrors.Inc()
	}

	// 2) 绝对超期
	absRecycled, err := w.recycleAbsolute(sweepCtx)
	if err != nil {
		slog.Warn("session_lifecycle: recycle absolute failed", "error", err)
		sessionLifecycleMetrics.scanErrors.Inc()
	}

	// 3) 租户超限（按 eviction_policy）
	evicted, err := w.evictExcess(sweepCtx)
	if err != nil {
		slog.Warn("session_lifecycle: evict excess failed", "error", err)
		sessionLifecycleMetrics.scanErrors.Inc()
	}

	total := idleRecycled + absRecycled + evicted
	if total > 0 {
		slog.Info("session_lifecycle: sweep completed",
			"idle_recycled", idleRecycled,
			"absolute_recycled", absRecycled,
			"evicted", evicted,
			"duration", time.Since(start))
	}
}

// recycleIdle 处理闲置超时会话。返回本轮处理数。
func (w *SessionLifecycleWorker) recycleIdle(ctx context.Context) (int, error) {
	if w.idleTimeout <= 0 {
		return 0, nil
	}
	query := `
		SELECT gw_session_id, tenant_id, last_active_at
		FROM session_dim
		WHERE status = 'active'
		  AND last_active_at IS NOT NULL
		  AND last_active_at < NOW() - $1::interval
		ORDER BY last_active_at ASC
		LIMIT $2
	`
	rows, err := w.db.Query(ctx, query, intervalSeconds(w.idleTimeout), w.cleanupBatchSize)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	type candidate struct {
		id           string
		tenantID     string
		lastActiveAt time.Time
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		var lastActive *time.Time
		if err := rows.Scan(&c.id, &c.tenantID, &lastActive); err != nil {
			continue
		}
		if lastActive != nil {
			c.lastActiveAt = *lastActive
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	count := 0
	for _, c := range candidates {
		if err := w.applyRecycle(ctx, c.id, c.tenantID, "idle", c.lastActiveAt, time.Since(c.lastActiveAt)); err != nil {
			slog.Warn("session_lifecycle: recycle idle failed",
				"gw_session_id", c.id, "error", err)
			continue
		}
		sessionLifecycleMetrics.recycled.WithLabelValues("idle").Inc()
		count++
	}
	return count, nil
}

// recycleAbsolute 处理绝对超期会话。返回本轮处理数。
func (w *SessionLifecycleWorker) recycleAbsolute(ctx context.Context) (int, error) {
	if w.absoluteMaxLifetime <= 0 {
		return 0, nil
	}
	query := `
		SELECT gw_session_id, tenant_id, first_request_at
		FROM session_dim
		WHERE status = 'active'
		  AND first_request_at IS NOT NULL
		  AND first_request_at < NOW() - $1::interval
		ORDER BY first_request_at ASC
		LIMIT $2
	`
	rows, err := w.db.Query(ctx, query, intervalSeconds(w.absoluteMaxLifetime), w.cleanupBatchSize)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	type candidate struct {
		id       string
		tenantID string
		created  time.Time
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		var first *time.Time
		if err := rows.Scan(&c.id, &c.tenantID, &first); err != nil {
			continue
		}
		if first != nil {
			c.created = *first
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	count := 0
	for _, c := range candidates {
		if err := w.applyRecycle(ctx, c.id, c.tenantID, "absolute_lifetime", c.created, time.Since(c.created)); err != nil {
			slog.Warn("session_lifecycle: recycle absolute failed",
				"gw_session_id", c.id, "error", err)
			continue
		}
		sessionLifecycleMetrics.recycled.WithLabelValues("absolute_lifetime").Inc()
		count++
	}
	return count, nil
}

// evictExcess 按租户驱逐超限会话。返回本轮处理数。
func (w *SessionLifecycleWorker) evictExcess(ctx context.Context) (int, error) {
	if w.maxPerTenant <= 0 || w.evictionPolicy == "none" {
		return 0, nil
	}

	// 找出超限的租户
	findTenantsQuery := `
		SELECT tenant_id, COUNT(*) AS cnt
		FROM session_dim
		WHERE status = 'active'
		  AND tenant_id IS NOT NULL
		  AND tenant_id != ''
		GROUP BY tenant_id
		HAVING COUNT(*) > $1
	`
	rows, err := w.db.Query(ctx, findTenantsQuery, w.maxPerTenant)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var tenants []string
	for rows.Next() {
		var t string
		var cnt int
		if err := rows.Scan(&t, &cnt); err == nil {
			tenants = append(tenants, t)
		}
	}
	rows.Close()

	count := 0
	for _, tenantID := range tenants {
		evicted, err := w.evictOneTenant(ctx, tenantID)
		if err != nil {
			slog.Warn("session_lifecycle: evict tenant failed",
				"tenant_id", tenantID, "error", err)
			continue
		}
		count += evicted
	}
	return count, nil
}

// evictOneTenant 对单个租户按 eviction_policy 驱逐。
func (w *SessionLifecycleWorker) evictOneTenant(ctx context.Context, tenantID string) (int, error) {
	var orderBy string
	switch w.evictionPolicy {
	case "fifo":
		orderBy = "first_request_at ASC" // 最早创建
	default: // lru
		orderBy = "COALESCE(last_active_at, first_request_at) ASC" // 最久未活跃
	}

	// 计算要驱逐的数量 = 当前 active 数 - 上限
	var overflow int
	row := w.db.QueryRow(ctx, `
		SELECT COUNT(*) - $1 FROM session_dim
		WHERE status = 'active' AND tenant_id = $2
	`, w.maxPerTenant, tenantID)
	if err := row.Scan(&overflow); err != nil {
		return 0, err
	}
	if overflow <= 0 {
		return 0, nil
	}

	// 选出 overflow 个候选
	query := `
		SELECT gw_session_id, last_active_at, first_request_at
		FROM session_dim
		WHERE status = 'active' AND tenant_id = $1
		ORDER BY ` + orderBy + `
		LIMIT $2
	`
	rows, err := w.db.Query(ctx, query, tenantID, overflow)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	type cand struct {
		id          string
		lastActive  *time.Time
		firstActive *time.Time
	}
	var candidates []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.lastActive, &c.firstActive); err == nil {
			candidates = append(candidates, c)
		}
	}
	rows.Close()

	count := 0
	for _, c := range candidates {
		ref := c.lastActive
		if ref == nil {
			ref = c.firstActive
		}
		var dur time.Duration
		if ref != nil {
			dur = time.Since(*ref)
		}
		if err := w.applyRecycle(ctx, c.id, tenantID, "evicted", derefTime(ref), dur); err != nil {
			slog.Warn("session_lifecycle: evict failed",
				"gw_session_id", c.id, "error", err)
			continue
		}
		sessionLifecycleMetrics.evicted.WithLabelValues(w.evictionPolicy).Inc()
		sessionLifecycleMetrics.recycled.WithLabelValues("evicted").Inc()
		count++
	}
	return count, nil
}

// applyRecycle 按 recycle_action 决策（soft_close / notify_only）。
func (w *SessionLifecycleWorker) applyRecycle(ctx context.Context, sessionID, tenantID, reason string, lastActiveAt time.Time, idleFor time.Duration) error {
	// 1. 数据库动作（无论 recycle_action 是什么，都先发布事件）
	if w.recycleAction == "soft_close" {
		updateQuery := `
			UPDATE session_dim
			SET status = 'closed',
			    closed_at = NOW(),
			    stop_reason = $2,
			    updated_at = NOW()
			WHERE gw_session_id = $1 AND status = 'active'
		`
		if _, err := w.db.Exec(ctx, updateQuery, sessionID, reason); err != nil {
			return err
		}
		sessionLifecycleMetrics.softClosed.Inc()
	} else {
		// notify_only：不修改 DB，仅记录
		sessionLifecycleMetrics.notified.Inc()
	}

	// 2. 发布事件（如果注入了 bus）
	if w.bus != nil {
		ev := &recycleEventShim{
			sessionID:    sessionID,
			tenantID:     tenantID,
			action:       w.recycleAction,
			reason:       reason,
			lastActiveAt: lastActiveAt,
			idleFor:      idleFor.String(),
			timestamp:    time.Now(),
		}
		if err := w.bus.Publish(ev); err != nil {
			slog.Warn("session_lifecycle: publish event failed",
				"gw_session_id", sessionID, "error", err)
		}
	}
	return nil
}

// recycleEventShim 是 worker 内部的事件适配器，避免直接 import sessioninspector。
// 真实生产中由调用方注入 LifecycleEventPublisher，
// sessioninspector.SessionInspectorRecycleEvent 即满足此接口。
type recycleEventShim struct {
	sessionID    string
	tenantID     string
	action       string
	reason       string
	lastActiveAt time.Time
	idleFor      string
	timestamp    time.Time
}

func (e *recycleEventShim) Type() string      { return "session_inspector.recycle" }
func (e *recycleEventShim) Timestamp() time.Time { return e.timestamp }

// intervalSeconds 把 duration 转成 Postgres INTERVAL 字符串（秒）。
func intervalSeconds(d time.Duration) string {
	// 使用 $1::interval 语法，传入秒数
	return formatSeconds(int64(d.Seconds()))
}

func formatSeconds(s int64) string {
	if s <= 0 {
		return "0 seconds"
	}
	// 直接返回秒数字符串（pgx 接受 $1::interval 形式）
	return formatDuration(s) + " seconds"
}

func formatDuration(s int64) string {
	if s < 60 {
		return durationItoa(s)
	}
	if s < 3600 {
		return durationItoa(s/60) + " minutes"
	}
	if s < 86400 {
		return durationItoa(s/3600) + " hours"
	}
	return durationItoa(s/86400) + " days"
}

// durationItoa 是带负号支持的整型→字符串转换（避免与 bg 包内其他 itoa 重名）。
func durationItoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// derefTime 解引用 *time.Time。
func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
