package scheduler

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWRRScheduler_Distribution 测试权重分布
func TestWRRScheduler_Distribution(t *testing.T) {
	// 准备凭据: A=5, B=1, C=1 (总权重 7)
	creds := []*Credential{
		{ID: 1, Quota: 5},
		{ID: 2, Quota: 1},
		{ID: 3, Quota: 1},
	}

	scheduler := NewWRRScheduler(creds)
	ctx := context.Background()

	// 选择 10000 次
	counts := make(map[int]int)
	for i := 0; i < 10000; i++ {
		cred, err := scheduler.Select(ctx)
		require.NoError(t, err)
		counts[cred.ID]++
	}

	// 验证分布 (允许 5% 偏差)
	total := 10000
	expectedA := total * 5 / 7 // ≈ 7143
	expectedB := total * 1 / 7 // ≈ 1429
	expectedC := total * 1 / 7 // ≈ 1429

	assertWithin(t, counts[1], expectedA, 0.05)
	assertWithin(t, counts[2], expectedB, 0.05)
	assertWithin(t, counts[3], expectedC, 0.05)
}

// TestWRRScheduler_SmoothDistribution 测试平滑分布
func TestWRRScheduler_SmoothDistribution(t *testing.T) {
	// 权重: A=5, B=1
	creds := []*Credential{
		{ID: 1, Quota: 5},
		{ID: 2, Quota: 1},
	}

	scheduler := NewWRRScheduler(creds)
	ctx := context.Background()

	// 记录前 12 次选择序列
	var sequence []int
	for i := 0; i < 12; i++ {
		cred, err := scheduler.Select(ctx)
		require.NoError(t, err)
		sequence = append(sequence, cred.ID)
	}

	// 验证平滑性: Smooth WRR 应避免长时间连续选择同一凭据
	// 但由于权重 5:1，允许最多 5 次连续（极端情况）
	maxConsecutive := getMaxConsecutive(sequence)
	assert.LessOrEqual(t, maxConsecutive, 6, "should not have excessive consecutive selections")

	// 验证在 12 次选择中，ID 2 至少出现 1 次（权重 1/6）
	count2 := 0
	for _, id := range sequence {
		if id == 2 {
			count2++
		}
	}
	assert.GreaterOrEqual(t, count2, 1, "ID 2 should appear at least once in 12 selections")
}

// TestWRRScheduler_DynamicWeight 测试动态权重调整
func TestWRRScheduler_DynamicWeight(t *testing.T) {
	creds := []*Credential{
		{ID: 1, Quota: 10},
		{ID: 2, Quota: 10},
	}

	scheduler := NewWRRScheduler(creds)
	ctx := context.Background()

	// 初始分布应为 50:50
	counts1 := selectN(scheduler, ctx, 1000)
	assertWithin(t, counts1[1], 500, 0.1)
	assertWithin(t, counts1[2], 500, 0.1)

	// 降低凭据 2 的权重到 1
	scheduler.UpdateWeight(2, 1)

	// 新分布应为 ~91:9 (10:1)
	counts2 := selectN(scheduler, ctx, 1000)
	assertWithin(t, counts2[1], 910, 0.1)
	assertWithin(t, counts2[2], 90, 0.1)
}

// TestWRRScheduler_EmptyCredentials 测试空凭据列表
func TestWRRScheduler_EmptyCredentials(t *testing.T) {
	scheduler := NewWRRScheduler([]*Credential{})
	ctx := context.Background()

	_, err := scheduler.Select(ctx)
	assert.Equal(t, ErrNoAvailableCredential, err)
}

// TestWRRScheduler_SingleCredential 测试单个凭据
func TestWRRScheduler_SingleCredential(t *testing.T) {
	creds := []*Credential{
		{ID: 1, Quota: 10},
	}

	scheduler := NewWRRScheduler(creds)
	ctx := context.Background()

	// 所有请求应选中唯一凭据
	for i := 0; i < 100; i++ {
		cred, err := scheduler.Select(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, cred.ID)
	}
}

// TestWRRScheduler_Metrics 测试统计指标
func TestWRRScheduler_Metrics(t *testing.T) {
	creds := []*Credential{
		{ID: 1, Quota: 5},
		{ID: 2, Quota: 1},
	}

	scheduler := NewWRRScheduler(creds)
	ctx := context.Background()

	// 选择 100 次
	for i := 0; i < 100; i++ {
		scheduler.Select(ctx)
	}

	metrics := scheduler.Metrics()
	assert.Equal(t, int64(100), metrics.TotalSelections)

	total := int64(0)
	for _, count := range metrics.PerCredential {
		total += count
	}
	assert.Equal(t, int64(100), total)
}

// TestWRRScheduler_Concurrent 测试并发安全
func TestWRRScheduler_Concurrent(t *testing.T) {
	creds := []*Credential{
		{ID: 1, Quota: 5},
		{ID: 2, Quota: 1},
	}

	scheduler := NewWRRScheduler(creds)
	ctx := context.Background()

	// 100 个 goroutine 并发选择
	var wg sync.WaitGroup
	counts := make(map[int]*atomic.Int64)
	counts[1] = &atomic.Int64{}
	counts[2] = &atomic.Int64{}

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				cred, err := scheduler.Select(ctx)
				require.NoError(t, err)
				counts[cred.ID].Add(1)
			}
		}()
	}

	wg.Wait()

	// 验证分布 (10000 次，权重 5:1)
	total := 10000
	expectedA := total * 5 / 6 // ≈ 8333
	expectedB := total * 1 / 6 // ≈ 1667

	assertWithin(t, int(counts[1].Load()), expectedA, 0.1)
	assertWithin(t, int(counts[2].Load()), expectedB, 0.1)

	// 验证总数
	assert.Equal(t, int64(total), counts[1].Load()+counts[2].Load())
}

// TestWRRScheduler_ZeroWeight 测试零权重凭据
func TestWRRScheduler_ZeroWeight(t *testing.T) {
	creds := []*Credential{
		{ID: 1, Quota: 10},
		{ID: 2, Quota: 10},
	}

	scheduler := NewWRRScheduler(creds)
	ctx := context.Background()

	// 将凭据 2 权重设为 0
	scheduler.UpdateWeight(2, 0)

	// 所有请求应只选中凭据 1
	for i := 0; i < 100; i++ {
		cred, err := scheduler.Select(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, cred.ID)
	}
}

// BenchmarkWRRScheduler_Select 性能测试
func BenchmarkWRRScheduler_Select(b *testing.B) {
	creds := []*Credential{
		{ID: 1, Quota: 10},
		{ID: 2, Quota: 5},
		{ID: 3, Quota: 1},
	}

	scheduler := NewWRRScheduler(creds)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = scheduler.Select(ctx)
	}
}

// BenchmarkWRRScheduler_Concurrent 并发性能测试
func BenchmarkWRRScheduler_Concurrent(b *testing.B) {
	creds := []*Credential{
		{ID: 1, Quota: 10},
		{ID: 2, Quota: 5},
		{ID: 3, Quota: 1},
	}

	scheduler := NewWRRScheduler(creds)
	ctx := context.Background()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = scheduler.Select(ctx)
		}
	})
}

// Helper functions

func assertWithin(t *testing.T, actual, expected int, tolerance float64) {
	diff := math.Abs(float64(actual - expected))
	maxDiff := float64(expected) * tolerance
	assert.LessOrEqual(t, diff, maxDiff,
		"actual=%d, expected=%d, tolerance=%.1f%%",
		actual, expected, tolerance*100)
}

func selectN(scheduler *WRRScheduler, ctx context.Context, n int) map[int]int {
	counts := make(map[int]int)
	for i := 0; i < n; i++ {
		cred, _ := scheduler.Select(ctx)
		counts[cred.ID]++
	}
	return counts
}

func getMaxConsecutive(sequence []int) int {
	if len(sequence) == 0 {
		return 0
	}

	maxConsecutive := 1
	currentConsecutive := 1
	prev := sequence[0]

	for i := 1; i < len(sequence); i++ {
		if sequence[i] == prev {
			currentConsecutive++
			if currentConsecutive > maxConsecutive {
				maxConsecutive = currentConsecutive
			}
		} else {
			currentConsecutive = 1
			prev = sequence[i]
		}
	}

	return maxConsecutive
}
