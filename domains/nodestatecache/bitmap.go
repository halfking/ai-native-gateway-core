package nodestatecache

import (
	"math/bits"
	"sync"
	"sync/atomic"
)

// BitmapShards 写路径分片数，对齐 NodeMirror 的 16 分片惯例
// （domains/ursm/v2/cache/nodemirror.go NodeMirrorShards）。
const BitmapShards = 16

// NodeStateBitmap 按 dense node id 位寻址的三个位平面（R10.4，逐字段照抄规格）：
//
//	Avail bit=1 可参与路由；Probe bit=1 需自检；Full bit=1 满载。
//
// 本结构是只读快照视图；并发原子更新经 bitmapTable 方法进行。
type NodeStateBitmap struct {
	Avail []uint64
	Probe []uint64
	Full  []uint64
}

// bitmapTable 是 NodeStateBitmap 的并发安全宿主：单位置 Set/Clear 走无锁
// atomic Or/And；跨平面复合迁移（如清 Avail 同时置 Probe）按 16 分片加锁，
// 分片索引 = 字下标 & (BitmapShards-1)。读（Test/遍历）一律 atomic.Load，无锁。
type bitmapTable struct {
	planes  [3][]uint64 // [Avail, Probe, Full]
	shardMu [BitmapShards]sync.Mutex
}

// planeAvail/planeProbe/planeFull 是 planes 数组下标。
const (
	planeAvail = 0
	planeProbe = 1
	planeFull  = 2
)

func newBitmapTable(capacity int32) *bitmapTable {
	words := uint64Words(capacity)
	return &bitmapTable{
		planes: [3][]uint64{
			make([]uint64, words),
			make([]uint64, words),
			make([]uint64, words),
		},
	}
}

// uint64Words 计算覆盖 capacity+1 个位（id 从 1 起，0 保留）所需 uint64 字数。
func uint64Words(capacity int32) int {
	return int(uint32(capacity+1+63) / 64)
}

func wordOf(id int32) (idx int, mask uint64) {
	u := uint32(id)
	return int(u >> 6), uint64(1) << (u & 63)
}

// shardOfWord 字下标到分片的确定性映射（与 NodeMirror 分片目的一致：降低
// 复合迁移的锁竞争；同一下标恒落同分片）。
func shardOfWord(idx int) int { return idx & (BitmapShards - 1) }

// set 置位（无锁原子）。
func (b *bitmapTable) set(plane int, id int32) {
	idx, mask := wordOf(id)
	atomic.OrUint64(&b.planes[plane][idx], mask)
}

// clear 清零（无锁原子）。
func (b *bitmapTable) clear(plane int, id int32) {
	idx, mask := wordOf(id)
	atomic.AndUint64(&b.planes[plane][idx], ^mask)
}

// test 读位（无锁原子 Load）。
func (b *bitmapTable) test(plane int, id int32) bool {
	idx, mask := wordOf(id)
	return atomic.LoadUint64(&b.planes[plane][idx])&mask != 0
}

// transition 在同一分片锁下完成「清 Avail + 置 Probe」这类跨平面复合迁移，
// 保证观察者不会看到迁移中间态以外的丢失（单一位操作仍是无锁原子，
// Test 与 transition 并发安全）。
func (b *bitmapTable) transition(clearAvail, setProbe bool, id int32) {
	idx, _ := wordOf(id)
	mu := &b.shardMu[shardOfWord(idx)]
	mu.Lock()
	defer mu.Unlock()
	if clearAvail {
		b.clear(planeAvail, id)
	}
	if setProbe {
		b.set(planeProbe, id)
	}
}

func (b *bitmapTable) SetAvail(id int32)       { b.set(planeAvail, id) }
func (b *bitmapTable) ClearAvail(id int32)     { b.clear(planeAvail, id) }
func (b *bitmapTable) TestAvail(id int32) bool { return b.test(planeAvail, id) }

func (b *bitmapTable) SetProbe(id int32)       { b.set(planeProbe, id) }
func (b *bitmapTable) ClearProbe(id int32)     { b.clear(planeProbe, id) }
func (b *bitmapTable) TestProbe(id int32) bool { return b.test(planeProbe, id) }

func (b *bitmapTable) SetFull(id int32)       { b.set(planeFull, id) }
func (b *bitmapTable) ClearFull(id int32)     { b.clear(planeFull, id) }
func (b *bitmapTable) TestFull(id int32) bool { return b.test(planeFull, id) }

// MarkAvailable 复合迁移：置 Avail、清 Probe（成功且冷却中恢复）。
func (b *bitmapTable) MarkAvailable(id int32) {
	idx, _ := wordOf(id)
	mu := &b.shardMu[shardOfWord(idx)]
	mu.Lock()
	defer mu.Unlock()
	b.set(planeAvail, id)
	b.clear(planeProbe, id)
}

// MarkProbing 复合迁移：清 Avail、置 Probe（FailStreak 达阈值）。
func (b *bitmapTable) MarkProbing(id int32) { b.transition(true, true, id) }

// foreachSet 遍历平面内全部置位 id（atomic.Load 快照，回调期间并发置位
// 可能被遗漏——扫描器语义可接受，一致性由对账循环兜底）。
func (b *bitmapTable) foreachSet(plane int, fn func(id int32)) {
	words := b.planes[plane]
	for idx := 0; idx < len(words); idx++ {
		w := atomic.LoadUint64(&words[idx])
		for w != 0 {
			bit := bits.TrailingZeros64(w)
			fn(int32(idx*64 + bit))
			w &= w - 1
		}
	}
}

// countOnes 统计平面置位总数（atomic.Load 快照）。
func (b *bitmapTable) countOnes(plane int) int {
	n := 0
	words := b.planes[plane]
	for idx := range words {
		n += bits.OnesCount64(atomic.LoadUint64(&words[idx]))
	}
	return n
}

// Snapshot 返回三平面的拷贝（诊断/测试用）。
func (b *bitmapTable) Snapshot() NodeStateBitmap {
	cp := func(src []uint64) []uint64 {
		dst := make([]uint64, len(src))
		for i := range src {
			dst[i] = atomic.LoadUint64(&src[i])
		}
		return dst
	}
	return NodeStateBitmap{
		Avail: cp(b.planes[planeAvail]),
		Probe: cp(b.planes[planeProbe]),
		Full:  cp(b.planes[planeFull]),
	}
}
