package nodestatecache

import (
	"sync"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"
)

// 规格 16B 结构布局钉死：三个紧凑结构必须恰好 16 字节。
func TestStructSizes16Bytes(t *testing.T) {
	assert.EqualValues(t, 16, unsafe.Sizeof(NodeUseRecord{}))
	assert.EqualValues(t, 16, unsafe.Sizeof(NodeStatsSlot{}))
	assert.EqualValues(t, 16, unsafe.Sizeof(NodeResourceSlot{}))
}

func TestBitmapBasicSetClearTest(t *testing.T) {
	b := newBitmapTable(1000)
	assert.False(t, b.TestAvail(1))
	b.SetAvail(1)
	b.SetAvail(64)
	b.SetAvail(65)
	assert.True(t, b.TestAvail(1))
	assert.True(t, b.TestAvail(64))
	assert.True(t, b.TestAvail(65))
	assert.False(t, b.TestAvail(2))
	assert.Equal(t, 3, b.countOnes(planeAvail))

	b.ClearAvail(64)
	assert.False(t, b.TestAvail(64))
	assert.Equal(t, 2, b.countOnes(planeAvail))

	// Snapshot 拷贝与原数据一致（word0 仅剩 id1；id65 在 word1）。
	snap := b.Snapshot()
	assert.Equal(t, uint64(0b10), snap.Avail[0])
	assert.Equal(t, uint64(0b10), snap.Avail[1])

	var got []int32
	b.foreachSet(planeAvail, func(id int32) { got = append(got, id) })
	assert.Equal(t, []int32{1, 65}, got)
}

// UT-NS-02：Avail/Probe/Full 分片并发置位/清零无丢失（race detector 下验证）。
func TestBitmapConcurrentNoLoss(t *testing.T) {
	const workers = 16
	const perWorker = 500
	b := newBitmapTable(int32(workers*perWorker) + 64)

	// 阶段一：每 worker 独占分区置位 → 汇合后一位不丢。
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			base := int32(w * perWorker)
			for i := int32(0); i < perWorker; i++ {
				id := base + i + 1
				b.SetAvail(id)
				b.SetProbe(id)
				b.SetFull(id)
			}
		}(w)
	}
	// 干扰者：并发做 Test 与复合迁移（独立等待组，避免与 stop 信道互等；
	// 操作 worker 分区之外的 id，不干扰置位计数）。
	stop := make(chan struct{})
	var dwg sync.WaitGroup
	dwg.Add(1)
	go func() {
		defer dwg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				b.TestAvail(8001)
				b.TestProbe(8002)
				b.MarkProbing(8003)
				b.MarkAvailable(8003)
			}
		}
	}()
	wg.Wait()
	close(stop)
	dwg.Wait()
	// 清理干扰者残留的复合迁移终态，不与分区计数混淆。
	b.ClearAvail(8003)
	b.ClearProbe(8003)

	assert.Equal(t, workers*perWorker, b.countOnes(planeAvail))
	assert.Equal(t, workers*perWorker, b.countOnes(planeProbe))
	assert.Equal(t, workers*perWorker, b.countOnes(planeFull))

	// 阶段二：并发交替 MarkProbing/MarkAvailable 于同 id，终态必为二选一，
	// 永不出现 Avail 与 Probe 同时置位的撕裂（分片锁复合迁移保证）。
	var wg2 sync.WaitGroup
	const id = 42
	for w := 0; w < workers; w++ {
		wg2.Add(1)
		go func(w int) {
			defer wg2.Done()
			for i := 0; i < 200; i++ {
				if (w+i)%2 == 0 {
					b.MarkProbing(id)
				} else {
					b.MarkAvailable(id)
				}
			}
		}(w)
	}
	wg2.Wait()
	avail, probe := b.TestAvail(id), b.TestProbe(id)
	assert.NotEqual(t, avail, probe, "torn transition: avail and probe must be exclusive")
}

// 位寻址边界：跨 64 位字边界与 id 0 保留位。
func TestBitmapWordBoundaries(t *testing.T) {
	b := newBitmapTable(200)
	for _, id := range []int32{63, 64, 127, 128, 199} {
		b.SetFull(id)
	}
	assert.Equal(t, 5, b.countOnes(planeFull))
	for _, id := range []int32{63, 64, 127, 128, 199} {
		assert.True(t, b.TestFull(id))
	}
}
