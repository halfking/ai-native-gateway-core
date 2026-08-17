package nodestatecache

import (
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// NodeStatsSlot 每节点 16B 统计槽（R10.4，逐字段照抄规格）：
// 数组按 dense id 索引；饱和计数与秒级截断换密度与速度，
// 精确遥测不在此层重复（URSM 窗口 ZSET + journey 全量）。
type NodeStatsSlot struct {
	LastUsedUnixSec int32  // 最后请求时间（秒级截断）
	Succ1h          uint16 // 1h 成功次数（饱和计数）
	Fail1h          uint16 // 1h 失败次数（饱和计数）
	FailStreak      uint8  // 连续错误次数（阈值对齐 URSM FailStreakLimit=3，可配）
	State           uint8  // available/degraded/probing/offline 枚举
	WindowStart     uint32 // 1h 翻转窗口起点（读时比较重置）
}

// 节点状态枚举（State 字段取值）。0 保留为 unknown（置脏未重拉）。
const (
	StateUnknown   uint8 = 0
	StateAvailable uint8 = 1
	StateDegraded  uint8 = 2
	StateProbing   uint8 = 3
	StateOffline   uint8 = 4
)

// statsTable 以 [2]uint64 打包字保存每节点 16B 槽（内存布局仍为 16B/节点，
// 数组按 dense id 索引；字段视图经 pack/unpack 与 NodeStatsSlot 互转）：
//
//	word0 = LastUsedUnixSec(32) | Succ1h(16) | Fail1h(16)
//	word1 = FailStreak(8) | State(8) | WindowStart(32) | 预留(16)
//
// 写路径按 16 分片加锁（双字一致性）；读路径无锁 atomic.Load（单字内一致）。
type statsTable struct {
	words   []uint64
	shardMu [BitmapShards]sync.Mutex
}

func newStatsTable(capacity int32) *statsTable {
	return &statsTable{words: make([]uint64, 2*(int(capacity)+1))}
}

func statsShard(id int32) int { return int(uint32(id) & (BitmapShards - 1)) }

func packStatsWord0(s NodeStatsSlot) uint64 {
	return uint64(uint32(s.LastUsedUnixSec)) |
		uint64(s.Succ1h)<<32 |
		uint64(s.Fail1h)<<48
}

func packStatsWord1(s NodeStatsSlot) uint64 {
	return uint64(s.FailStreak) |
		uint64(s.State)<<8 |
		uint64(s.WindowStart)<<16
}

func unpackStats(w0, w1 uint64) NodeStatsSlot {
	return NodeStatsSlot{
		LastUsedUnixSec: int32(uint32(w0)),
		Succ1h:          uint16(w0 >> 32),
		Fail1h:          uint16(w0 >> 48),
		FailStreak:      uint8(w1),
		State:           uint8(w1 >> 8),
		WindowStart:     uint32(w1 >> 16),
	}
}

func satInc16(v uint16) uint16 {
	if v == math.MaxUint16 {
		return v // 饱和封顶，不回绕
	}
	return v + 1
}

func satInc8(v uint8) uint8 {
	if v == math.MaxUint8 {
		return v // 饱和封顶，不回绕
	}
	return v + 1
}

// hourWindow 当前 1h 窗口号（unix 秒 / 3600）。
func hourWindow(now time.Time) uint32 { return uint32(now.Unix() / 3600) }

// get 无锁读取槽快照（两字各一次 atomic.Load）。
func (t *statsTable) get(id int32) NodeStatsSlot {
	base := 2 * int(uint32(id))
	if base < 0 || base+1 >= len(t.words) {
		return NodeStatsSlot{}
	}
	w0 := atomic.LoadUint64(&t.words[base])
	w1 := atomic.LoadUint64(&t.words[base+1])
	return unpackStats(w0, w1)
}

// recordOutcome 记录一次结果（成功/失败）。窗口翻转时重置 Succ1h/Fail1h
// （FailStreak/State 跨窗保留，对齐 URSM 连续失败语义）。返回更新后的槽
// 快照与窗口是否翻转。成功清零 FailStreak 并置 State=Available。
func (t *statsTable) recordOutcome(id int32, success bool, now time.Time) (NodeStatsSlot, bool) {
	base := 2 * int(uint32(id))
	if base < 0 || base+1 >= len(t.words) {
		return NodeStatsSlot{}, false
	}
	mu := &t.shardMu[statsShard(id)]
	mu.Lock()
	defer mu.Unlock()
	slot := unpackStats(atomic.LoadUint64(&t.words[base]), atomic.LoadUint64(&t.words[base+1]))
	hour := hourWindow(now)
	flipped := slot.WindowStart != hour
	if flipped {
		slot.Succ1h, slot.Fail1h = 0, 0
		slot.WindowStart = hour
	}
	slot.LastUsedUnixSec = int32(now.Unix())
	if success {
		slot.Succ1h = satInc16(slot.Succ1h)
		slot.FailStreak = 0
		slot.State = StateAvailable
	} else {
		slot.Fail1h = satInc16(slot.Fail1h)
		slot.FailStreak = satInc8(slot.FailStreak)
		if slot.State == StateUnknown || slot.State == StateAvailable {
			slot.State = StateDegraded
		}
	}
	atomic.StoreUint64(&t.words[base], packStatsWord0(slot))
	atomic.StoreUint64(&t.words[base+1], packStatsWord1(slot))
	return slot, flipped
}

// setState 覆盖 State 字段（对账用），返回是否发生变化。
func (t *statsTable) setState(id int32, state uint8) bool {
	base := 2 * int(uint32(id))
	if base < 0 || base+1 >= len(t.words) {
		return false
	}
	mu := &t.shardMu[statsShard(id)]
	mu.Lock()
	defer mu.Unlock()
	slot := unpackStats(atomic.LoadUint64(&t.words[base]), atomic.LoadUint64(&t.words[base+1]))
	if slot.State == state {
		return false
	}
	slot.State = state
	atomic.StoreUint64(&t.words[base+1], packStatsWord1(slot))
	return true
}

// setWindowStart 覆盖 WindowStart（对账用），返回是否发生变化。
func (t *statsTable) setWindowStart(id int32, hour uint32) bool {
	base := 2 * int(uint32(id))
	if base < 0 || base+1 >= len(t.words) {
		return false
	}
	mu := &t.shardMu[statsShard(id)]
	mu.Lock()
	defer mu.Unlock()
	slot := unpackStats(atomic.LoadUint64(&t.words[base]), atomic.LoadUint64(&t.words[base+1]))
	if slot.WindowStart == hour {
		return false
	}
	slot.WindowStart = hour
	atomic.StoreUint64(&t.words[base+1], packStatsWord1(slot))
	return true
}

// sweep1h 全量翻转过期的 1h 窗口（ResetWindow 定时调用；跳过从未使用
// WindowStart==0 的槽，避免为空槽写放大）。返回重置的槽位数。
func (t *statsTable) sweep1h(now time.Time) int {
	hour := hourWindow(now)
	n := 0
	for base := 2; base+1 < len(t.words); base += 2 {
		w1 := atomic.LoadUint64(&t.words[base+1])
		if w1 == 0 {
			continue
		}
		slot := unpackStats(0, w1)
		if slot.WindowStart == hour {
			continue
		}
		id := int32(base / 2)
		mu := &t.shardMu[statsShard(id)]
		mu.Lock()
		// 双检：持锁后重新加载，避免覆盖并发写入的更新值。
		cur := unpackStats(atomic.LoadUint64(&t.words[base]), atomic.LoadUint64(&t.words[base+1]))
		if cur.WindowStart != hour {
			cur.Succ1h, cur.Fail1h = 0, 0
			cur.WindowStart = hour
			atomic.StoreUint64(&t.words[base], packStatsWord0(cur))
			atomic.StoreUint64(&t.words[base+1], packStatsWord1(cur))
			n++
		}
		mu.Unlock()
	}
	return n
}

// reset 清零槽（注册表回收 id 时调用）。
func (t *statsTable) reset(id int32) {
	base := 2 * int(uint32(id))
	if base < 0 || base+1 >= len(t.words) {
		return
	}
	mu := &t.shardMu[statsShard(id)]
	mu.Lock()
	defer mu.Unlock()
	atomic.StoreUint64(&t.words[base], 0)
	atomic.StoreUint64(&t.words[base+1], 0)
}
