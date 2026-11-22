package sanitize

// history_memo.go —— 历史守卫判决的缓存（2026-11-23，大请求体归约提速轮）。
//
// # 为什么可以缓存
//
// 历史守卫对一条消息的判决是**纯函数**：
//
//	判决 = f(编译后的规则策略, 消息的原始字节)
//
// 策略由 HistoryGuard 的 policy key（规则集 JSON 的 sha256，已按租户过滤）唯一
// 确定；消息字节由 sha256 确定。两者都不含可变状态（守卫只用 ctx 做取消）。
// ⇒ 相同 (策略, 消息) 第二次遇到时，判决必然相同，直接复用。
//
// # 为什么值得缓存
//
// agent 会话（ZCode 等）每一轮都把**整段历史**重发一遍：第 N 轮的历史 = 第
// N-1 轮的历史 + 新增的几条消息。旧消息的守卫判决每轮被原样重算——211 实测
// 7.7MB / 3953 条的会话，带守卫归约 5 秒，其中守卫占约 88%（约 3.6 秒正则 +
// 文本解码）。缓存后每轮只需判定**新增**的消息。
//
// # 两层，各自的边界写明
//
//   - 本地层（进程内、有界、FIFO 淘汰）：默认开。重启即失——这是有意的：
//     它只是一份纯函数结果的备忘，丢了重算即可，绝不能成为正确性的依赖。
//   - 共享层（Redis 之类，带 TTL）：可选，**默认不启用**（装配处决定）。只有
//     多个实例同时服务、同一会话的相邻两轮可能落到不同进程时才有意义；单实例
//     （含蓝绿——同一时刻只有一个槽在服务）用本地层就够，重启后第一轮走冷路径。
//     ★ 启用时自带硬边界，因为它住在一台与限流/会话/队列共用的 Redis 上
//     （211 实测 maxmemory=0 / noeviction，没有内存上限也不淘汰）：
//       · TTL + 每小时写入预算 + used_memory 水位线 —— 防「写太多」；
//       · 熔断 —— 防「后端卡住」。连续失败或变慢达到阈值即绕过共享层一段
//         时间（本地层照常），冷却后半开放一个探测，探测失败立即重开。
//         没有它，一次 Redis 卡顿会被逐条消息放大成几千次 50ms 超时。
//
// # 只存判决，不存内容
//
// 键是哈希、值是一个布尔：缓存里**没有会话明文**。被判「含敏感内容」的消息
// 也只留下一个 false。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// HistoryVerdictMemo 缓存历史守卫对单条消息的判决。
//
// 两个方法都必须可并发调用，且都是**尽力而为**：失败只计数，绝不改变守卫
// 的结论（未命中就照常判定）。
type HistoryVerdictMemo interface {
	// Lookup 返回 key 对应的判决；ok=false 表示没有缓存。
	Lookup(ctx context.Context, key [32]byte) (safe, ok bool)
	// Store 记录一次判决。
	Store(ctx context.Context, key [32]byte, safe bool)
}

// VerdictKV 是共享层的最小键值接口（Get / 带 TTL 的 Set / 内存水位）。
// Redis 适配器在 internal/verdictkv——sanitize 包不引 Redis 依赖。
type VerdictKV interface {
	// Get 返回键的值；键不存在时返回 ("", false, nil)。
	Get(ctx context.Context, key string) (val string, ok bool, err error)
	// Set 写入键值并设置过期。
	Set(ctx context.Context, key, val string, ttl time.Duration) error
	// UsedMemory 返回后端已用内存字节数（用于水位线保护）。
	UsedMemory(ctx context.Context) (int64, error)
}

// HistoryMemoConfig 是分层缓存的配置（零值取默认）。
type HistoryMemoConfig struct {
	// LocalEntries 是本地层的条目上限（默认 131072，约 16MB）。
	LocalEntries int
	// Shared 是共享层后端；nil ⇒ 只有本地层。
	Shared VerdictKV
	// SharedTTL 是共享层键的存活时间；≤0 ⇒ 不使用共享层（即使设了 Shared）。
	SharedTTL time.Duration
	// SharedMaxUsedBytes 是共享后端的内存水位线：后端已用内存超过它即停写。
	// ≤0 ⇒ 取默认 512 MiB。
	SharedMaxUsedBytes int64
	// SharedMaxWritesPerHour 是本进程每小时最多写入共享层的条数（≤0 ⇒ 默认 50 万）。
	SharedMaxWritesPerHour int64
	// now 由测试注入；生产留空。
	now func() time.Time
}

const (
	defaultMemoLocalEntries = 1 << 17
	// 共享层默认边界按 211 实测定：Redis 已用 ~283MB、无上限不淘汰 ⇒ 水位线
	// 512MB 只留约 200MB 余量；每条键约 100 字节 ⇒ 50 万次/小时最多约 50MB。
	defaultMemoSharedMaxUsed   = 512 << 20
	defaultMemoSharedMaxWrites = 500_000
	verdictMemoShards          = 32
	sharedMemoOpTimeout        = 50 * time.Millisecond
	sharedMemoMemCheckInterval = 30 * time.Second
	sharedMemoWriteWindow      = time.Hour

	// 熔断：连续失败/变慢这么多次 ⇒ 打开；打开期间绕过共享层这么久。
	sharedMemoFailThreshold = 5
	sharedMemoCooldown      = 30 * time.Second
	// 单次共享层操作超过这个时长算「变慢」（本地回环上的 Redis 正常在 1ms 内）。
	sharedMemoSlowOp = 5 * time.Millisecond
)

// HistoryMemoStats 是缓存的运行计数（观测/管理面用）。
type HistoryMemoStats struct {
	LocalHits    uint64
	SharedHits   uint64
	Misses       uint64
	SharedErrors uint64
	SharedWrites uint64
	// SharedSkipped 是因预算/水位线/熔断而**没有**写共享层的条数。
	SharedSkipped uint64
	// BreakerOpens 是熔断打开的次数（正常应为 0；不为 0 说明共享层曾连续失败或变慢）。
	BreakerOpens uint64
}

// TieredVerdictMemo 是「本地 → 共享」两层缓存。
type TieredVerdictMemo struct {
	local  *localVerdictMemo
	shared VerdictKV
	ttl    time.Duration
	now    func() time.Time

	sharedHits   atomic.Uint64
	misses       atomic.Uint64
	sharedErrors atomic.Uint64
	sharedWrites atomic.Uint64
	sharedSkips  atomic.Uint64
	breakerOpens atomic.Uint64

	// 熔断状态。临界区里只有几个字段的读写，不含任何后端调用。
	breakerMu sync.Mutex
	openUntil time.Time // 零值 ⇒ 闭合
	failures  int       // 连续失败/变慢的次数
	probing   bool      // 冷却期满后放出去的那个探测尚未返回

	// 写入保护状态（只在写路径上用，单独一把小锁）。
	guardMu      sync.Mutex
	maxUsed      int64
	maxWrites    int64
	windowStart  time.Time
	windowWrites int64
	memCheckedAt time.Time
	memOver      bool
}

// NewHistoryVerdictMemo 构造分层缓存。
func NewHistoryVerdictMemo(cfg HistoryMemoConfig) *TieredVerdictMemo {
	entries := cfg.LocalEntries
	if entries <= 0 {
		entries = defaultMemoLocalEntries
	}
	m := &TieredVerdictMemo{
		local:     newLocalVerdictMemo(entries),
		shared:    cfg.Shared,
		now:       cfg.now,
		maxUsed:   cfg.SharedMaxUsedBytes,
		maxWrites: cfg.SharedMaxWritesPerHour,
	}
	if m.ttl = cfg.SharedTTL; m.ttl < 0 {
		m.ttl = 0
	}
	if m.maxUsed <= 0 {
		m.maxUsed = defaultMemoSharedMaxUsed
	}
	if m.maxWrites <= 0 {
		m.maxWrites = defaultMemoSharedMaxWrites
	}
	if m.now == nil {
		m.now = time.Now
	}
	m.windowStart, m.memCheckedAt = m.now(), m.now()
	return m
}

// verdictKey = sha256(policyKey ‖ 0x00 ‖ 消息字节)。policyKey 定长 32 字节 ⇒
// 与消息字节之间无歧义，不需要再加分隔长度。
func verdictKey(policyKey [32]byte, msg []byte) [32]byte {
	h := sha256.New()
	_, _ = h.Write(policyKey[:])
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(msg)
	var key [32]byte
	copy(key[:], h.Sum(nil))
	return key
}

func (m *TieredVerdictMemo) Lookup(ctx context.Context, key [32]byte) (bool, bool) {
	if safe, ok := m.local.get(key); ok {
		return safe, true
	}
	// Misses 的口径 = 「两层都没答上来、必须真的算一次」的次数 ⇒
	// 命中率 = (LocalHits + SharedHits) / (LocalHits + SharedHits + Misses)
	// 直接就是「省掉了多少次守卫计算」。
	if !m.sharedReady() {
		m.misses.Add(1)
		return false, false
	}
	start := m.now()
	sctx, cancel := context.WithTimeout(ctx, sharedMemoOpTimeout)
	val, ok, err := m.shared.Get(sctx, encodeVerdictKey(key))
	cancel()
	if err != nil {
		// 共享层故障：按未命中处理（本地照常重算）。连片报错会刷屏 ⇒ 限频。
		m.sharedErrors.Add(1)
		if n := m.sharedErrors.Load(); n == 1 || n%1000 == 0 {
			slog.Warn("sanitize: history verdict shared cache read failed", "errors", n, "error", err)
		}
		m.sharedFailed(start)
		m.misses.Add(1)
		return false, false
	}
	m.sharedDone(start)
	if !ok || (val != "0" && val != "1") {
		// 没有缓存，或值不认识（不拿坏数据当判决）⇒ 当未命中重算。
		m.misses.Add(1)
		return false, false
	}
	safe := val == "1"
	m.sharedHits.Add(1)
	// 提升到本地层：同一条消息在本进程内的后续查找不再触共享层。
	m.local.put(key, safe)
	return safe, true
}

func (m *TieredVerdictMemo) Store(ctx context.Context, key [32]byte, safe bool) {
	m.local.put(key, safe)
	if m.shared == nil || m.ttl <= 0 {
		return
	}
	if !m.sharedReady() || !m.sharedWritable() {
		m.sharedSkips.Add(1)
		return
	}
	val := "0"
	if safe {
		val = "1"
	}
	start := m.now()
	sctx, cancel := context.WithTimeout(ctx, sharedMemoOpTimeout)
	err := m.shared.Set(sctx, encodeVerdictKey(key), val, m.ttl)
	cancel()
	if err != nil {
		m.sharedErrors.Add(1)
		m.sharedFailed(start)
		return
	}
	m.sharedDone(start)
	m.sharedWrites.Add(1)
}

// sharedReady 报告现在是否可以触共享层。熔断打开期间返回 false；冷却期满后
// 半开——把闭合状态恢复并放**一个**探测出去（probing）。
func (m *TieredVerdictMemo) sharedReady() bool {
	if m.shared == nil || m.ttl <= 0 {
		return false
	}
	m.breakerMu.Lock()
	defer m.breakerMu.Unlock()
	if m.openUntil.IsZero() {
		return true
	}
	if m.now().Before(m.openUntil) {
		return false
	}
	m.openUntil, m.failures, m.probing = time.Time{}, 0, true
	return true
}

// sharedFailed 记一次失败（出错，或慢到超过 sharedMemoSlowOp）。连续达到阈值
// ⇒ 打开熔断；半开探测失败 ⇒ 立即重开（后端还没好，不必再赔几次超时）。
func (m *TieredVerdictMemo) sharedFailed(start time.Time) {
	m.breakerMu.Lock()
	defer m.breakerMu.Unlock()
	m.failures++
	if !m.probing && m.failures < sharedMemoFailThreshold {
		return
	}
	m.openUntil = m.now().Add(sharedMemoCooldown)
	m.failures, m.probing = 0, false
	m.breakerOpens.Add(1)
	slog.Warn("sanitize: history verdict shared cache tripped; bypassing it for a cooldown",
		"cooldown", sharedMemoCooldown.String(), "last_op", m.now().Sub(start).Round(time.Millisecond))
}

// sharedDone 记一次成功（慢操作按失败计）。
func (m *TieredVerdictMemo) sharedDone(start time.Time) {
	if m.now().Sub(start) >= sharedMemoSlowOp {
		m.sharedFailed(start)
		return
	}
	m.breakerMu.Lock()
	m.failures, m.probing = 0, false
	m.breakerMu.Unlock()
}

// sharedWritable 实现共享层的自我保护：每小时写入预算 + 每 30 秒一次的
// 内存水位线检查。任一触线 ⇒ 停写（读不受影响），下一个窗口/下一次检查
// 自动恢复。
func (m *TieredVerdictMemo) sharedWritable() bool {
	m.guardMu.Lock()
	defer m.guardMu.Unlock()
	now := m.now()
	if now.Sub(m.windowStart) >= sharedMemoWriteWindow {
		m.windowStart, m.windowWrites = now, 0
	}
	if m.windowWrites >= m.maxWrites {
		return false
	}
	if now.Sub(m.memCheckedAt) >= sharedMemoMemCheckInterval {
		m.memCheckedAt = now
		ctx, cancel := context.WithTimeout(context.Background(), sharedMemoOpTimeout)
		used, err := m.shared.UsedMemory(ctx)
		cancel()
		if err != nil {
			// 读不到水位：保守起见按未触线处理（TTL 与写入预算仍在兜底）。
			slog.Debug("sanitize: history verdict shared cache memory probe failed", "error", err)
		} else if used > m.maxUsed {
			if !m.memOver {
				slog.Warn("sanitize: history verdict shared cache over memory watermark; writes paused",
					"used_bytes", used, "max_bytes", m.maxUsed, "recheck", sharedMemoMemCheckInterval.String())
			}
			m.memOver = true
			return false
		} else if m.memOver {
			slog.Info("sanitize: history verdict shared cache under memory watermark; writes resumed", "used_bytes", used)
			m.memOver = false
		}
	} else if m.memOver {
		return false
	}
	m.windowWrites++
	return true
}

// Stats 返回运行计数快照。
func (m *TieredVerdictMemo) Stats() HistoryMemoStats {
	return HistoryMemoStats{
		LocalHits:     m.local.hits.Load(),
		SharedHits:    m.sharedHits.Load(),
		Misses:        m.misses.Load(),
		SharedErrors:  m.sharedErrors.Load(),
		SharedWrites:  m.sharedWrites.Load(),
		SharedSkipped: m.sharedSkips.Load(),
		BreakerOpens:  m.breakerOpens.Load(),
	}
}

func encodeVerdictKey(key [32]byte) string {
	return base64.RawURLEncoding.EncodeToString(key[:])
}

// localVerdictMemo 是进程内的有界缓存：分片 map + 每片一条 FIFO 淘汰环。
//
// 为什么是 FIFO 而不是 LRU：agent 会话是追加式的——一条消息要么在接下来的
// 每一轮都被复用，要么再也不出现，「最近用过」和「最早进来」对命中率的
// 影响几乎一样，而 FIFO 不需要在读路径上改任何状态（读只需一把读锁）。
type localVerdictMemo struct {
	shards [verdictMemoShards]verdictShard
	hits   atomic.Uint64
}

type verdictShard struct {
	mu   sync.Mutex
	vals map[[32]byte]bool
	ring [][32]byte
	// next 是环写满之后的淘汰游标。
	next int
	cap  int
}

func newLocalVerdictMemo(capacity int) *localVerdictMemo {
	per := (capacity + verdictMemoShards - 1) / verdictMemoShards
	if per < 16 {
		per = 16
	}
	m := &localVerdictMemo{}
	for i := range m.shards {
		m.shards[i].vals = make(map[[32]byte]bool, per)
		m.shards[i].ring = make([][32]byte, 0, per)
		m.shards[i].cap = per
	}
	return m
}

func (m *localVerdictMemo) shard(key [32]byte) *verdictShard {
	// key 本身是 sha256 ⇒ 首字节均匀分布，直接用它选片。
	return &m.shards[key[0]%verdictMemoShards]
}

func (m *localVerdictMemo) get(key [32]byte) (bool, bool) {
	s := m.shard(key)
	s.mu.Lock()
	safe, ok := s.vals[key]
	s.mu.Unlock()
	if ok {
		m.hits.Add(1)
	}
	return safe, ok
}

func (m *localVerdictMemo) put(key [32]byte, safe bool) {
	s := m.shard(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.vals[key]; exists {
		s.vals[key] = safe
		return
	}
	if len(s.ring) >= s.cap {
		delete(s.vals, s.ring[s.next])
		s.ring[s.next] = key
		s.next++
		if s.next >= s.cap {
			s.next = 0
		}
	} else {
		s.ring = append(s.ring, key)
	}
	s.vals[key] = safe
}

// memoizeHistoryGuard 给一个历史守卫包上判决缓存。
//
//   - inner        被包装的守卫（编译后的策略）
//   - policyKey    策略 key（进缓存键：规则一变，旧判决全部自然失效）
//   - memo         缓存；nil ⇒ 原样返回 inner
//   - hasMatches   策略里是否有需要匹配的规则组；没有 ⇒ 不必读源、不必缓存
//
// 返回的守卫可并发调用（自身无共享可变状态）。
func memoizeHistoryGuard(
	inner func(context.Context, func() io.Reader) (bool, error),
	policyKey [32]byte,
	memo HistoryVerdictMemo,
	hasMatches bool,
) func(context.Context, func() io.Reader) (bool, error) {
	if memo == nil || !hasMatches {
		return inner
	}
	return func(ctx context.Context, open func() io.Reader) (bool, error) {
		// 源读取错误与 inner 的分类保持一致：inner 只要有规则组就一定会读到
		// 这个错误并原样返回 ⇒ 这里读失败同样原样返回，不吞、不缓存。
		data, err := readAllBounded(open(), historyMemoMaxBytes)
		if err != nil {
			if errors.Is(err, errVerdictTooLarge) {
				// 超大消息不进缓存：照旧交给 inner 流式判定。
				return inner(ctx, open)
			}
			return false, err
		}
		key := verdictKey(policyKey, data)
		if safe, ok := memo.Lookup(ctx, key); ok {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			return safe, nil
		}
		safe, err := inner(ctx, func() io.Reader { return bytes.NewReader(data) })
		if err != nil {
			// 判定过程出错（含 ctx 取消、转义损坏）⇒ 不缓存，原样返回。
			return false, err
		}
		memo.Store(ctx, key, safe)
		return safe, nil
	}
}

// historyMemoMaxBytes 是进缓存的消息上限。超过它的消息走原来的流式判定
// （不把大消息整条读进内存只为算一个哈希）。
const historyMemoMaxBytes = 1 << 20

var errVerdictTooLarge = errors.New("sanitize: history message too large to memoize")

func readAllBounded(r io.Reader, max int) ([]byte, error) {
	if r == nil {
		return nil, errors.New("sanitize: nil history source")
	}
	data, err := io.ReadAll(io.LimitReader(r, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > max {
		return nil, errVerdictTooLarge
	}
	return data, nil
}
