package nodestatecache

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// benchCache 构建 10k 节点宇宙：全注册喂入可用，10% 满载，5% 冷却，
// 每节点带历史统计；Scorer 为进程内直通评分（模拟位图预过滤后至多一次
// 镜像调用的下界）。
func benchCache(tb testing.TB, nodes int) (*Cache, []int32) {
	tb.Helper()
	clk := newFakeClock(testBase)
	scorer := &passthroughScorer{}
	c := New(Options{
		Capacity:     int32(nodes * 2),
		StartTickers: false,
		Clock:        clk.Now,
		Scorer:       scorer,
	})
	ids := make([]int32, 0, nodes)
	for i := 0; i < nodes; i++ {
		id, err := c.Register(NodeRef{
			TenantID:     "tenant-bench",
			CredentialID: int64(i + 1),
			RawModel:     "m-bench",
		})
		require.NoError(tb, err)
		ids = append(ids, id)
	}
	var wg sync.WaitGroup
	part := len(ids)/8 + 1
	for w := 0; w < 8; w++ {
		lo := w * part
		hi := lo + part
		if hi > len(ids) {
			hi = len(ids)
		}
		if lo >= hi {
			break
		}
		wg.Add(1)
		go func(chunk []int32) {
			defer wg.Done()
			for i, id := range chunk {
				c.Update(id, true, ErrKindNone, 20)
				c.Update(id, true, ErrKindNone, 25)
				if i%10 == 0 { // 10% 满载
					c.SetLimits(id, 1, 0, 0)
					c.TryAcquire(id, ResourceConcurrency)
				}
				if i%20 == 5 { // 5% 冷却（连续失败转 probing）
					c.Update(id, false, ErrKindUpstream, 100)
					c.Update(id, false, ErrKindUpstream, 100)
					c.Update(id, false, ErrKindUpstream, 100)
				}
			}
		}(ids[lo:hi])
	}
	wg.Wait()
	return c, ids
}

// passthroughScorer 直通评分：survivors 原序返回（进程内下界）。
type passthroughScorer struct{}

func (passthroughScorer) Score(_ context.Context, _, _ string, survivors []int32) []ScoredNode {
	out := make([]ScoredNode, len(survivors))
	for i, id := range survivors {
		out[i] = ScoredNode{NodeID: id, Score: float64(len(survivors) - i)}
	}
	return out
}

// BenchmarkSelect 单次 Select（10k 节点宇宙，含 5 个已试排除）。
func BenchmarkSelect(b *testing.B) {
	c, ids := benchCache(b, 10000)
	defer c.Close()
	ctx := context.Background()
	used := []NodeUseRecord{
		{Seq: 1, NodeID: ids[1]},
		{Seq: 2, NodeID: ids[2]},
		{Seq: 3, NodeID: ids[3]},
		{Seq: 4, NodeID: ids[4]},
		{Seq: 5, NodeID: ids[5]},
	}
	q := SelectQuery{Model: "m-bench", Used: used, RetryCount: 1}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res := c.Select(ctx, q)
		if res.Exhausted {
			b.Fatal("unexpected exhausted")
		}
	}
}

// BenchmarkSelectSticky sticky 路径（位图 + 注册表查，短路）。
func BenchmarkSelectSticky(b *testing.B) {
	c, ids := benchCache(b, 10000)
	defer c.Close()
	ctx := context.Background()
	ref, _ := c.NodeRef(ids[7])
	st := &fakeSticky{ref: ref, valid: true}
	c.sticky = st
	q := SelectQuery{Model: "m-bench", Flags: FlagSticky}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res := c.Select(ctx, q)
		if res.NodeID != ids[7] {
			b.Fatal("sticky miss")
		}
	}
}

// BenchmarkUpdater 单次 Updater（统计槽 + 位图维护）。
func BenchmarkUpdater(b *testing.B) {
	c, ids := benchCache(b, 10000)
	defer c.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Update(ids[i%len(ids)], i%2 == 0, ErrKindUpstream, 50)
	}
}

// BenchmarkTryAcquireRelease 资源仪表 CAS 往返。
func BenchmarkTryAcquireRelease(b *testing.B) {
	c, ids := benchCache(b, 10000)
	defer c.Close()
	id := ids[0]
	c.SetLimits(id, 0, 0, 0) // 不限：纯 CAS 计数
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.TryAcquire(id, ResourceConcurrency)
		c.Release(id, ResourceConcurrency)
	}
}

// UT-NS-12：单次 Select P99 < 1ms 性能门禁（10k 节点宇宙，4096 次采样）。
// race detector 插桩延迟不代表生产口径，-race 下跳过。
func TestSelectP99Under1ms(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("perf gate skipped in -short / -race")
	}
	c, ids := benchCache(t, 10000)
	defer c.Close()
	ctx := context.Background()
	used := []NodeUseRecord{{Seq: 1, NodeID: ids[1]}}
	q := SelectQuery{Model: "m-bench", Used: used}

	const samples = 4096
	latencies := make([]time.Duration, 0, samples)
	for i := 0; i < samples; i++ {
		start := time.Now()
		res := c.Select(ctx, q)
		d := time.Since(start)
		if res.Exhausted {
			t.Fatal("unexpected exhausted")
		}
		latencies = append(latencies, d)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p50 := latencies[samples/2]
	p99 := latencies[samples*99/100]
	t.Logf("Select 10k-node universe: p50=%v p99=%v max=%v (gate: p99 < 1ms)", p50, p99, latencies[samples-1])
	assert.Less(t, p99, time.Millisecond, "R10.6: single Select p99 must stay under 1ms")
	fmt.Printf("UT-NS-12 gate: p50=%v p99=%v max=%v over %d samples\n", p50, p99, latencies[samples-1], samples)
}
