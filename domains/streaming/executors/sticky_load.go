package executors

import (
	"context"
	"log/slog"
	"os"
	"reflect"
	"strconv"
	"sync"
	"time"
)

// StickyLoadInfo 是路由评分消费的每凭据 sticky 负载视图。
type StickyLoadInfo struct {
	// Sessions 是最近 window（默认 5 分钟）内 sticky 到该凭据的会话数。
	// 有 Redis 时是跨实例真值（蓝绿双活共用一个 ZSET）；无 Redis 时是
	// 本实例镜像。0 = 无信号（不惩罚）。
	Sessions int
	// LastActivityMs 是该凭据最近一次已知请求/会话观察的 unix 毫秒。
	// 0 = 未知（视为空闲，不惩罚）。取本实例 activity 与 Redis 滑窗
	// 最高分的较大者。
	LastActivityMs int64
}

// StickyLoadStore 是 domains/ursm/v2/cache.StickyLoadStore 的最小接口。
// 与 StickyRedisStore 同理：只用原生类型，避免 executors → cache 硬依赖。
type StickyLoadStore interface {
	Observe(ctx context.Context, credentialID int, sessionKey string, ts time.Time, window time.Duration) error
	LoadBatch(ctx context.Context, credentialIDs []int, window time.Duration) (sessions map[int]int, lastSeen map[int]int64)
}

// StickyLoadView 是 Router 消费的读写接口：读（Refresh 由 planCandidates
// 每请求触发一次，内部按 refresh TTL 节流；Info 是 O(1) map 读）＋
// 写（Observe* 由 recordStickySuccess 在成功路径调用）。
type StickyLoadView interface {
	Refresh(credIDs []int)
	Info(credentialID int) StickyLoadInfo
	ObserveSession(credentialID int, sessionKey string)
	ObserveActivity(credentialID int)
}

const (
	stickyLoadDefaultWindow  = 5 * time.Minute
	stickyLoadDefaultRefresh = 3 * time.Second
	stickyLoadObserveTimeout = 50 * time.Millisecond
	stickyLoadRefreshTimeout = 250 * time.Millisecond
	// stickyLoadActivityRetention：activity 时间戳只需覆盖评分的 recency
	// 地平线（默认 30s），保留 10 分钟足够且限界内存。
	stickyLoadActivityRetention = 10 * time.Minute
	// stickyLoadSnapshotStaleCap 是快照新鲜度上限的绝对顶（R47）：R46 F1
	// 把 Info 的快照覆盖判据钉在 window 上，但 window 是运维 env——调得
	// 极大时（如 86400s）Redis 故障下的冻结快照仍可整天参与评分，偏置
	// 重新打开。快照年龄超过 min(window, 该顶) 即回落本实例内存镜像。
	stickyLoadSnapshotStaleCap = 10 * time.Minute
)

// StickyLoadTracker 维护每凭据的 sticky 会话滑窗（2026-09-19
// sticky-session load balancing）。
//
// 数据面：
//   - 进程内镜像 sessions（credID → sessionKey → lastSeen unix 秒）——
//     本实例写路径同步更新，读路径在 Redis 不可用时兜底；
//   - activity（credID → 最近任意成功请求的 unix 毫秒）——仅本实例，
//     用于"最近请求时间"信号（recency penalty）；
//   - Redis 快照 snapshot —— 跨实例会话计数 + 最近会话活跃时间，
//     由 Refresh 按 refreshTTL 节流批量刷新（单飞：刷新进行中其余
//     调用直接用过期快照，热路径永不等待 Redis）。
//
// 会话完成与否不可判定（用户规格也承认），窗口即语义：5 分钟内活跃的
// 会话全部计数，超窗自然丢弃。会话改绑新凭据后旧凭据的计数最多残留
// 一个窗口，属规格容忍范围。
type StickyLoadTracker struct {
	window  time.Duration
	refresh time.Duration
	// snapshotMaxAge = min(window, stickyLoadSnapshotStaleCap)，构造期
	// 定死（window 构造后不变），Info 快照新鲜度判据用（R47）。
	snapshotMaxAge time.Duration

	store StickyLoadStore // nil → 纯内存（无 Redis 部署）

	mu       sync.Mutex
	sessions map[int]map[string]int64
	activity map[int]int64

	snapMu     sync.Mutex
	snapshot   map[int]StickyLoadInfo
	snapAt     time.Time
	refreshing bool

	stopSweep chan struct{}
	sweepDone sync.WaitGroup
	closeOnce sync.Once
}

// NewStickyLoadTracker 构造滑窗跟踪器并启动后台清扫。
// env：
//   - LLM_GATEWAY_STICKYLOAD_WINDOW_SECONDS（默认 300）
//   - LLM_GATEWAY_STICKYLOAD_REFRESH_MS（默认 3000，快照刷新节流）
func NewStickyLoadTracker() *StickyLoadTracker {
	window := time.Duration(envFloat("LLM_GATEWAY_STICKYLOAD_WINDOW_SECONDS", stickyLoadDefaultWindow.Seconds())) * time.Second
	if window <= 0 {
		window = stickyLoadDefaultWindow
	}
	refresh := time.Duration(envFloat("LLM_GATEWAY_STICKYLOAD_REFRESH_MS", float64(stickyLoadDefaultRefresh.Milliseconds()))) * time.Millisecond
	if refresh <= 0 {
		refresh = stickyLoadDefaultRefresh
	}
	t := &StickyLoadTracker{
		window:         window,
		refresh:        refresh,
		snapshotMaxAge: window,
		sessions:       make(map[int]map[string]int64),
		activity:       make(map[int]int64),
		stopSweep:      make(chan struct{}),
	}
	if t.snapshotMaxAge > stickyLoadSnapshotStaleCap {
		t.snapshotMaxAge = stickyLoadSnapshotStaleCap
	}
	t.sweepDone.Add(1)
	go t.sweepLoop()
	return t
}

// SetStore 注入 Redis 滑窗存储；nil 退化为纯内存。
// Typed-nil 归一化与 StickyCache.SetRedisStore 同因（252 无 Redis 部署
// 实抓的 typed-nil 接口判空失效）。
func (t *StickyLoadTracker) SetStore(store StickyLoadStore) {
	if store != nil {
		if v := reflect.ValueOf(store); v.Kind() == reflect.Pointer && v.IsNil() {
			store = nil
		}
	}
	t.mu.Lock()
	t.store = store
	t.mu.Unlock()
}

// Close 停止后台清扫（幂等；Once 防并发双 close panic）。
func (t *StickyLoadTracker) Close() {
	if t == nil {
		return
	}
	t.closeOnce.Do(func() {
		close(t.stopSweep)
		t.sweepDone.Wait()
	})
}

func (t *StickyLoadTracker) sweepLoop() {
	defer t.sweepDone.Done()
	// 顶层 recover：pruneMemory 纯 map 操作，panic 概率低，但后台循环
	// 裸奔会击穿进程——对齐 bg worker run-loop 纪律（R47）。
	defer func() {
		if r := recover(); r != nil {
			slog.Error("sticky-load sweep panic", "panic", r)
		}
	}()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-t.stopSweep:
			return
		case <-ticker.C:
			t.pruneMemory()
		}
	}
}

// pruneMemory 清理进程内镜像的过期会话与 activity，防止只写不读的
// 凭据无限增长。
func (t *StickyLoadTracker) pruneMemory() {
	now := time.Now()
	sessCutoff := now.Add(-t.window).Unix()
	actCutoff := now.Add(-t.activityRetention()).UnixMilli()
	t.mu.Lock()
	defer t.mu.Unlock()
	for cred, m := range t.sessions {
		for s, ts := range m {
			if ts < sessCutoff {
				delete(m, s)
			}
		}
		if len(m) == 0 {
			delete(t.sessions, cred)
		}
	}
	for cred, ts := range t.activity {
		if ts < actCutoff {
			delete(t.activity, cred)
		}
	}
}

// activityRetention 是 activity 时间戳的内存保留线。固定 10 分钟覆盖
// 默认 recency 地平线；window 被运维调大时跟随 window；recency 地平线
// env（LLM_GATEWAY_ROUTING_RECENCY_HORIZON_SECONDS，recentRequestPenalty
// 消费）调得比二者都大时同样跟随——否则 recency 信号比语义窗先归零，
// 惩罚提前消失（R46 F8⑪ / R47 补地平线维度）。
func (t *StickyLoadTracker) activityRetention() time.Duration {
	ret := stickyLoadActivityRetention
	if t.window > ret {
		ret = t.window
	}
	if h := envFloat("LLM_GATEWAY_ROUTING_RECENCY_HORIZON_SECONDS", 30); h > 0 {
		// +1s 余量：Duration 截断不得让 activity 比地平线先过期。
		if horizon := time.Duration(h*float64(time.Second)) + time.Second; horizon > ret {
			ret = horizon
		}
	}
	return ret
}

// ObserveSession 记录"该会话此刻绑定在该凭据上"。在 sticky 绑定写入
// （recordStickySuccess）时调用：绑定改写即观察，会话持续使用会持续
// 刷新 score，空闲超窗自然跌出——正是"5 分钟内全部计数"的语义。
// 内存同步更新；Redis best-effort 异步（50ms 超时，失败仅 Debug 日志）。
func (t *StickyLoadTracker) ObserveSession(credentialID int, sessionKey string) {
	if t == nil || credentialID <= 0 || sessionKey == "" {
		return
	}
	now := time.Now()
	t.mu.Lock()
	if t.sessions[credentialID] == nil {
		t.sessions[credentialID] = make(map[string]int64)
	}
	t.sessions[credentialID][sessionKey] = now.Unix()
	if now.UnixMilli() > t.activity[credentialID] {
		t.activity[credentialID] = now.UnixMilli()
	}
	store := t.store
	window := t.window
	t.mu.Unlock()

	if store != nil {
		go func() {
			// R46 F8⑫：与 dispatch worker 的 recover-per-item 纪律对齐，
			// best-effort 观察路径不允许 panic 击穿进程。
			defer func() {
				if r := recover(); r != nil {
					slog.Warn("sticky-load observe panic", "credential_id", credentialID, "panic", r)
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), stickyLoadObserveTimeout)
			defer cancel()
			if err := store.Observe(ctx, credentialID, sessionKey, now, window); err != nil {
				slog.Debug("sticky-load observe failed",
					"credential_id", credentialID, "error", err)
			}
		}()
	}
}

// ObserveActivity 记录该凭据最近一次成功请求（无论有无会话标识），
// 仅内存——"最近请求时间"是突发平滑提示，实时并发已由 LiveLoad 覆盖。
func (t *StickyLoadTracker) ObserveActivity(credentialID int) {
	if t == nil || credentialID <= 0 {
		return
	}
	now := time.Now().UnixMilli()
	t.mu.Lock()
	if now > t.activity[credentialID] {
		t.activity[credentialID] = now
	}
	t.mu.Unlock()
}

// Refresh 触发一次跨实例快照刷新（planCandidates 每请求调用一次）。
// refresh TTL 内直接返回；已有刷新在途（单飞）直接返回；Redis 失败
// 保留旧快照（过期数据胜过无数据），纯内存模式为 no-op。
func (t *StickyLoadTracker) Refresh(credIDs []int) {
	if t == nil || len(credIDs) == 0 {
		return
	}
	t.snapMu.Lock()
	if t.refreshing || time.Since(t.snapAt) < t.refresh {
		t.snapMu.Unlock()
		return
	}
	t.refreshing = true
	t.snapMu.Unlock()

	t.mu.Lock()
	store := t.store
	window := t.window
	t.mu.Unlock()
	if store == nil {
		t.snapMu.Lock()
		t.refreshing = false
		t.snapMu.Unlock()
		return
	}

	go func() {
		// 单飞标志在 defer 中复位：LoadBatch panic（自定义 store 实现等）
		// 不得永久卡死 refreshing 使快照冻结（R46 F1）。
		defer func() {
			if r := recover(); r != nil {
				slog.Warn("sticky-load refresh panic", "panic", r)
			}
			t.snapMu.Lock()
			t.refreshing = false
			t.snapMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), stickyLoadRefreshTimeout)
		defer cancel()
		sessions, lastSeen := store.LoadBatch(ctx, dedupeInts(credIDs), window)
		if sessions == nil {
			return
		}
		snap := make(map[int]StickyLoadInfo, len(sessions))
		for id, n := range sessions {
			snap[id] = StickyLoadInfo{Sessions: n, LastActivityMs: lastSeen[id] * 1000}
		}
		t.snapMu.Lock()
		t.snapshot = snap
		t.snapAt = time.Now()
		t.snapMu.Unlock()
	}()
}

// Info 返回某凭据的 sticky 负载视图（热路径，O(1)）。
// 会话数优先取跨实例快照（覆盖到该凭据且快照未过期时）；否则用本实例
// 镜像。最近活跃取本实例 activity 与快照的较大者。
//
// R46 F1：快照新鲜度上限。快照的语义是"window 内活跃会话数"——快照
// 年龄超过 window 本身即失去语义。Redis 持续故障时 Refresh 失败保留旧
// 快照（短闪断兜底），但陈旧快照不得无限期参与评分（否则早已空闲的
// 凭据被记满 sticky 会话数，流量系统性偏向"冻结时看起来空闲"的节点），
// 超限回落本实例内存镜像。
func (t *StickyLoadTracker) Info(credentialID int) StickyLoadInfo {
	if t == nil || credentialID <= 0 {
		return StickyLoadInfo{}
	}
	var info StickyLoadInfo

	t.snapMu.Lock()
	snap, covered := t.snapshot[credentialID]
	// R46 F1 + R47：新鲜度判据用 min(window, 绝对顶)——window 被运维调大
	// 时冻结快照的偏置窗口仍有界（stickyLoadSnapshotStaleCap）。
	if covered && time.Since(t.snapAt) >= t.snapshotMaxAge {
		covered = false
	}
	t.snapMu.Unlock()

	t.mu.Lock()
	info.Sessions = t.memoryCountLocked(credentialID)
	info.LastActivityMs = t.activity[credentialID]
	t.mu.Unlock()

	if covered {
		info.Sessions = snap.Sessions
		if snap.LastActivityMs > info.LastActivityMs {
			info.LastActivityMs = snap.LastActivityMs
		}
	}
	return info
}

// memoryCountLocked 计算窗口内的本实例会话数（惰性裁剪过期项）。
func (t *StickyLoadTracker) memoryCountLocked(credentialID int) int {
	m, ok := t.sessions[credentialID]
	if !ok {
		return 0
	}
	cutoff := time.Now().Add(-t.window).Unix()
	count := 0
	for _, ts := range m {
		if ts >= cutoff {
			count++
		}
	}
	return count
}

func dedupeInts(ids []int) []int {
	seen := make(map[int]struct{}, len(ids))
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// stickyLoadEnvInt 读 int env 配 fallback（sticky 会话容量默认值用，
// stickySessionCapacity 消费）。非正/非法值一律回落 fallback。
func stickyLoadEnvInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}
