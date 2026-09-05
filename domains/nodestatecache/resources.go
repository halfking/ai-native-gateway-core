package nodestatecache

import (
	"math"
	"sync/atomic"
	"time"
)

// NodeResourceSlot 每节点 16B 资源仪表（R10.4，逐字段照抄规格）：
// 并发 / 指纹 slot / RPM 三对 Used+Limit，原子 CAS 快速更新。
//
//	ConcUsed/ConcLimit 源 = credentials.concurrency_mode/limits（dispatch Governor）
//	FPUsed/FPLimit     源 = credentialfpslot 配额
//	RPMUsed/RPMLimit   源 = Governor RPM；分钟级翻转窗口
//
// 本缓存持有的是派生镜像值，权威准入仍在 Governor 与 fpslot；
// 缓存内不另建上限配置（Limit 由集成者/对账注入）。
type NodeResourceSlot struct {
	ConcUsed, ConcLimit uint16 // 在途并发（Limit=0 不限）
	FPUsed, FPLimit     uint16 // 指纹 slot
	RPMUsed, RPMLimit   uint16 // 每分钟请求（分钟级翻转窗口，定时重置）
	WindowID, Flags     uint16 // 重置窗口号；Flags.bit0=满载（联动 Full 位图）
}

// ResourceFlagFull Flags.bit0：满载（任一资源达限），联动位图 Full 平面。
const ResourceFlagFull uint16 = 1 << 0

// ResourceKind 资源仪表种类。
type ResourceKind uint8

const (
	ResourceConcurrency ResourceKind = iota + 1
	ResourceFPSlot
	ResourceRPM
)

// resourceTable 以 [2]uint64 打包字保存每节点 16B 槽：
//
//	word0 = ConcUsed(16) | ConcLimit(16) | FPUsed(16) | FPLimit(16)
//	word1 = RPMUsed(16) | RPMLimit(16) | WindowID(16) | Flags(16)
//
// 增减全部为 atomic.CompareAndSwap 循环（无锁、不经过 Redis，O(1)）。
type resourceTable struct {
	words   []uint64
	onFull  func(id int32) // 联动 Full 位图置位（Cache 注入）
	onClear func(id int32) // 联动 Full 位图清除（Cache 注入）
}

func newResourceTable(capacity int32, onFull, onClear func(int32)) *resourceTable {
	return &resourceTable{
		words:   make([]uint64, 2*(int(capacity)+1)),
		onFull:  onFull,
		onClear: onClear,
	}
}

func packResourceWord0(s NodeResourceSlot) uint64 {
	return uint64(s.ConcUsed) |
		uint64(s.ConcLimit)<<16 |
		uint64(s.FPUsed)<<32 |
		uint64(s.FPLimit)<<48
}

func packResourceWord1(s NodeResourceSlot) uint64 {
	return uint64(s.RPMUsed) |
		uint64(s.RPMLimit)<<16 |
		uint64(s.WindowID)<<32 |
		uint64(s.Flags)<<48
}

func unpackResource(w0, w1 uint64) NodeResourceSlot {
	return NodeResourceSlot{
		ConcUsed:  uint16(w0),
		ConcLimit: uint16(w0 >> 16),
		FPUsed:    uint16(w0 >> 32),
		FPLimit:   uint16(w0 >> 48),
		RPMUsed:   uint16(w1),
		RPMLimit:  uint16(w1 >> 16),
		WindowID:  uint16(w1 >> 32),
		Flags:     uint16(w1 >> 48),
	}
}

func satInc16r(v uint16) uint16 {
	if v == math.MaxUint16 {
		return v
	}
	return v + 1
}

// minuteWindow derives the 16-bit reset-window id from the wall clock.
// WindowID is 16-bit by packed-layout contract: the id wraps every
// 65536 minutes (~45 days). On wrap a stored id compares unequal to the
// current one, so the window is treated as flipped and RPM counters reset —
// a benign once-per-45-day stats blip. Widening needs a word re-layout,
// tracked in docs/架构优化v6/02 (D32).
func minuteWindow(now time.Time) uint16 { return uint16(now.Unix() / 60) }

// computeFull 满载判定：任一资源 Limit>0 且 Used>=Limit。
func computeFull(s NodeResourceSlot) bool {
	if s.ConcLimit > 0 && s.ConcUsed >= s.ConcLimit {
		return true
	}
	if s.FPLimit > 0 && s.FPUsed >= s.FPLimit {
		return true
	}
	if s.RPMLimit > 0 && s.RPMUsed >= s.RPMLimit {
		return true
	}
	return false
}

func (t *resourceTable) load(id int32) NodeResourceSlot {
	base := 2 * int(uint32(id))
	if base < 0 || base+1 >= len(t.words) {
		return NodeResourceSlot{}
	}
	return unpackResource(atomic.LoadUint64(&t.words[base]), atomic.LoadUint64(&t.words[base+1]))
}

// evaluateFullness 重算满载态并联动 Full 位图（任一达限→置；全释放→清）。
// 幂等：并发调用安全，CAS 循环直到标志与判定一致。
func (t *resourceTable) evaluateFullness(id int32) {
	base := 2 * int(uint32(id))
	if base < 0 || base+1 >= len(t.words) {
		return
	}
	for {
		w0 := atomic.LoadUint64(&t.words[base])
		w1 := atomic.LoadUint64(&t.words[base+1])
		slot := unpackResource(w0, w1)
		want := computeFull(slot)
		has := slot.Flags&ResourceFlagFull != 0
		if want == has {
			return
		}
		if want {
			slot.Flags |= ResourceFlagFull
		} else {
			slot.Flags &^= ResourceFlagFull
		}
		nw1 := packResourceWord1(slot)
		if atomic.CompareAndSwapUint64(&t.words[base+1], w1, nw1) {
			if want {
				t.onFull(id)
			} else {
				t.onClear(id)
			}
			return
		}
	}
}

// TryAcquire 尝试占用一个 kind 资源。Limit=0 表示不限（Used 饱和计数防回绕）；
// 达限返回 false（不占用）。RPM 按当前分钟窗翻转重置后判定。
// 成功占用后重算满载联动 Full 位图。
func (t *resourceTable) TryAcquire(id int32, kind ResourceKind, now time.Time) bool {
	base := 2 * int(uint32(id))
	if base < 0 || base+1 >= len(t.words) {
		return false
	}
	switch kind {
	case ResourceConcurrency, ResourceFPSlot:
		shift := uint(0)
		if kind == ResourceFPSlot {
			shift = 32
		}
		for {
			w0 := atomic.LoadUint64(&t.words[base])
			used := uint16(w0 >> shift)
			limit := uint16(w0 >> (shift + 16))
			if limit > 0 && used >= limit {
				t.evaluateFullness(id) // 达限必然满载，确保联动
				return false
			}
			nw0 := (w0 &^ (0xffff << shift)) | uint64(satInc16r(used))<<shift
			if atomic.CompareAndSwapUint64(&t.words[base], w0, nw0) {
				t.evaluateFullness(id)
				return true
			}
		}
	case ResourceRPM:
		minute := minuteWindow(now)
		for {
			w1 := atomic.LoadUint64(&t.words[base+1])
			slot := unpackResource(0, w1)
			if slot.WindowID != minute {
				slot.RPMUsed = 0
				slot.WindowID = minute
			}
			if slot.RPMLimit > 0 && slot.RPMUsed >= slot.RPMLimit {
				t.evaluateFullness(id)
				return false
			}
			slot.RPMUsed = satInc16r(slot.RPMUsed)
			if atomic.CompareAndSwapUint64(&t.words[base+1], w1, packResourceWord1(slot)) {
				t.evaluateFullness(id)
				return true
			}
		}
	default:
		return false
	}
}

// Release 释放一个 kind 资源（下限 0）。RPM 名额按分钟消耗、不做释放
// （窗口翻转重置）。释放后重算满载：全释放（各资源回到限内）→ 清 Full。
func (t *resourceTable) Release(id int32, kind ResourceKind) {
	base := 2 * int(uint32(id))
	if base < 0 || base+1 >= len(t.words) {
		return
	}
	switch kind {
	case ResourceConcurrency, ResourceFPSlot:
		shift := uint(0)
		if kind == ResourceFPSlot {
			shift = 32
		}
		for {
			w0 := atomic.LoadUint64(&t.words[base])
			used := uint16(w0 >> shift)
			if used == 0 {
				return // 无泄漏：不可释放到负
			}
			nw0 := (w0 &^ (0xffff << shift)) | uint64(used-1)<<shift
			if atomic.CompareAndSwapUint64(&t.words[base], w0, nw0) {
				t.evaluateFullness(id)
				return
			}
		}
	case ResourceRPM:
		// no-op：分钟窗翻转由 ResetWindow 统一重置。
	}
}

// SetLimits 注入上限（源 = credentials 表与 fpslot 配额；对账/配置变更时调用）。
func (t *resourceTable) SetLimits(id int32, conc, fp, rpm uint16) {
	base := 2 * int(uint32(id))
	if base < 0 || base+1 >= len(t.words) {
		return
	}
	for {
		w0 := atomic.LoadUint64(&t.words[base])
		slot := unpackResource(w0, 0)
		if slot.ConcLimit == conc && slot.FPLimit == fp {
			break
		}
		slot.ConcLimit, slot.FPLimit = conc, fp
		if atomic.CompareAndSwapUint64(&t.words[base], w0, packResourceWord0(slot)) {
			break
		}
	}
	for {
		w1 := atomic.LoadUint64(&t.words[base+1])
		slot := unpackResource(0, w1)
		if slot.RPMLimit == rpm {
			break
		}
		slot.RPMLimit = rpm
		if atomic.CompareAndSwapUint64(&t.words[base+1], w1, packResourceWord1(slot)) {
			break
		}
	}
	t.evaluateFullness(id)
}

// Override 以权威值覆盖整个槽（对账用）。调用方提供的 RPM 值视为
// 当前分钟窗的权威计数：保留 RPMUsed 并盖上当前分钟 WindowID
// （忽略缓存侧过期的 WindowID，保证对账幂等）。
func (t *resourceTable) Override(id int32, s NodeResourceSlot, now time.Time) {
	base := 2 * int(uint32(id))
	if base < 0 || base+1 >= len(t.words) {
		return
	}
	s.WindowID = minuteWindow(now)
	atomic.StoreUint64(&t.words[base], packResourceWord0(s))
	atomic.StoreUint64(&t.words[base+1], packResourceWord1(s))
	t.evaluateFullness(id)
}

// ForceRateLimited 429 限流联动（N16）：把 RPM 镜像视为本分钟满载——
// 有限额则 Used=Limit；无限额则临时 RPMUsed=RPMLimit=1，分钟窗翻转后回归。
func (t *resourceTable) ForceRateLimited(id int32, now time.Time) {
	base := 2 * int(uint32(id))
	if base < 0 || base+1 >= len(t.words) {
		return
	}
	minute := minuteWindow(now)
	for {
		w1 := atomic.LoadUint64(&t.words[base+1])
		slot := unpackResource(0, w1)
		if slot.WindowID != minute {
			slot.RPMUsed = 0
			slot.WindowID = minute
		}
		switch {
		case slot.RPMLimit > 0:
			slot.RPMUsed = slot.RPMLimit
		default:
			slot.RPMLimit, slot.RPMUsed = 1, 1
		}
		if atomic.CompareAndSwapUint64(&t.words[base+1], w1, packResourceWord1(slot)) {
			t.evaluateFullness(id)
			return
		}
	}
}

// Held 是否持有占用（并发或 FP slot 在途）——注册表 LRU 回收的「活跃」判定。
func (t *resourceTable) Held(id int32) bool {
	s := t.load(id)
	return s.ConcUsed > 0 || s.FPUsed > 0
}

// resetMinute 全量翻转过期分钟窗（ResetWindow 定时调用；跳过从未使用的槽）。
// 返回重置的槽位数。
func (t *resourceTable) resetMinute(now time.Time) int {
	minute := minuteWindow(now)
	n := 0
	for base := 1; base < len(t.words); base += 2 {
		w1 := atomic.LoadUint64(&t.words[base])
		if w1 == 0 {
			continue
		}
		slot := unpackResource(0, w1)
		if slot.WindowID == minute {
			continue
		}
		id := int32((base - 1) / 2)
		for {
			cur := atomic.LoadUint64(&t.words[base])
			s := unpackResource(0, cur)
			if s.WindowID == minute {
				break
			}
			s.RPMUsed = 0
			s.WindowID = minute
			if atomic.CompareAndSwapUint64(&t.words[base], cur, packResourceWord1(s)) {
				t.evaluateFullness(id)
				n++
				break
			}
		}
	}
	return n
}

// reset 清零槽（注册表回收 id 时调用）。
func (t *resourceTable) reset(id int32) {
	base := 2 * int(uint32(id))
	if base < 0 || base+1 >= len(t.words) {
		return
	}
	atomic.StoreUint64(&t.words[base], 0)
	atomic.StoreUint64(&t.words[base+1], 0)
}
