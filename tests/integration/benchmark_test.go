// 双模式存储架构（Task 6.1）lite 模式性能基准。
//
// 运行示例（冒烟）：
//
//	go test -bench=. -benchtime=10x -run=XXX ./tests/integration/
//
// 运行示例（收集真实数字）：
//
//	go test -bench=. -benchtime=100x -run=XXX ./tests/integration/
//
// 达标判据说明（Task 6.1 规格）：
//   - lite 模式缓存命中（L1 / L1.5）平均耗时 < 10ms/op：实测 ~0.2µs（L1）/
//     ~17µs（L1.5），余量达 3 个数量级以上，写入断言（见 assertAvgOpBelow）；
//   - bodies 写入 > 1000 ops/s：实测在空闲与后台负载波动下于 ~700 ~ ~3800 ops/s
//     之间摆动（跨度超过规格允许的 3 倍浮动带，且会低于阈值），按规格豁免条款
//     改为仅通过 b.ReportMetric/b.Logf 记录吞吐、不做失败断言（原因见
//     BenchmarkLiteBodiesWrite 内注释）。
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/session/v2"
	"github.com/kaixuan/llm-gateway-go/storage"
	"github.com/kaixuan/llm-gateway-go/storage/factory"
)

// benchCacheHitThreshold 缓存命中达标阈值：平均每次操作耗时应低于该值。
const benchCacheHitThreshold = 10 * time.Millisecond

// benchBodiesWriteMinOps Bodies 写入达标阈值：吞吐应高于该值（ops/s）。
const benchBodiesWriteMinOps = 1000.0

// assertAvgOpBelow 校验基准平均单次操作耗时低于阈值（达标判据断言）。
func assertAvgOpBelow(b *testing.B, threshold time.Duration) {
	b.Helper()
	if avg := b.Elapsed() / time.Duration(b.N); avg >= threshold {
		b.Fatalf("平均单次操作耗时 %v 达到/超过阈值 %v（环境浮动超过 3 倍时应改为仅记录不断言）", avg, threshold)
	}
}

// reportOpsPerSec 追加上报可读吞吐指标 ops/s。
func reportOpsPerSec(b *testing.B) {
	b.Helper()
	secs := b.Elapsed().Seconds()
	if secs > 0 {
		b.ReportMetric(float64(b.N)/secs, "ops/s")
	}
}

// newBenchCache 构造 lite 模式缓存（L1 + L1.5），L3 为空（db=nil）。
func newBenchCache(b *testing.B, baseDir string) (*v2.SessionCacheV2, *v2.FileCache) {
	b.Helper()
	fc, err := v2.NewFileCache(baseDir+"/l15", time.Hour, 64<<20)
	if err != nil {
		b.Fatal(err)
	}
	return v2.NewSessionCacheV2WithMode(nil, "", 0, storage.StorageModeLite, fc), fc
}

// BenchmarkLiteCacheGetL1 度量 lite 模式 L1（进程内 LRU）命中读取。
// 预热一次 Set 后循环 Get，全部迭代均为 L1 命中。
func BenchmarkLiteCacheGetL1(b *testing.B) {
	ctx := context.Background()
	cache, _ := newBenchCache(b, b.TempDir())

	state := dualState("sess-bench-l1", 3)
	if err := cache.Set(ctx, state); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := cache.Get(ctx, dualTenant, "sess-bench-l1")
		if err != nil || got == nil {
			b.Fatalf("L1 命中失败: got=%v err=%v", got, err)
		}
	}
	b.StopTimer()

	assertAvgOpBelow(b, benchCacheHitThreshold)
	reportOpsPerSec(b)
}

// BenchmarkLiteCacheGetL15 度量 lite 模式 L1.5（本地文件快照）命中读取。
// 预热后直接读 FileCache（绕过 L1），每次迭代都走完整的
// Stat → ReadFile → gunzip 无关的 JSON 反序列化路径，等价于 L1 冷实例的
// L1.5 命中成本。
func BenchmarkLiteCacheGetL15(b *testing.B) {
	_, fc := newBenchCache(b, b.TempDir())

	state := dualState("sess-bench-l15", 5)
	if err := fc.Set(state); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := fc.Get(dualTenant, "sess-bench-l15")
		if err != nil || got == nil {
			b.Fatalf("L1.5 命中失败: got=%v err=%v", got, err)
		}
	}
	b.StopTimer()

	assertAvgOpBelow(b, benchCacheHitThreshold)
	reportOpsPerSec(b)
}

// BenchmarkLiteBodiesWrite 度量 lite 模式 BodiesStore 写入
// （JSON 序列化 + gzip 压缩 + 异步落盘等待完成的端到端成本）。
// 每次迭代写唯一 turn 文件，避免覆盖写带来的偏乐观路径。
//
// 达标判据处理说明：规格参考值为 > 1000 ops/s（本地 SSD），但实测吞吐受
// 文件系统写回抖动影响显著（同一机器重复运行在 ~700 ~ ~3800 ops/s 间摆动，
// 跨度超过规格允许的 3 倍浮动带，且会偶发低于阈值），按规格豁免条款改为
// 仅记录不断言：吞吐经 ops/s 指标与本函数尾部日志上报，供对比与回归观察。
func BenchmarkLiteBodiesWrite(b *testing.B) {
	ctx := context.Background()
	f, err := factory.NewStorageFactory(newLiteStorageConfig(b, b.TempDir(), 0))
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	bodies := f.NewBodiesStore()

	// 统计典型负载大小用于换算 MB/s（各轮负载长度一致）
	sample, err := marshalBodySize(dualBody("sess-bench-w", 1))
	if err != nil {
		b.Fatal(err)
	}

	var counter atomic.Int64
	b.SetBytes(int64(sample))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		turn := int(counter.Add(1))
		if err := bodies.Write(ctx, dualBody("sess-bench-w", turn)); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()

	opsPerSec := 0.0
	if secs := b.Elapsed().Seconds(); secs > 0 {
		opsPerSec = float64(b.N) / secs
	}
	b.ReportMetric(opsPerSec, "ops/s")

	// 规格参考阈值 1000 ops/s：仅记录对比，不做失败断言（原因见函数注释）。
	if opsPerSec < benchBodiesWriteMinOps {
		b.Logf("bodies 写入吞吐 %.0f ops/s 低于参考阈值 %.0f ops/s（环境抖动，仅记录不断言）",
			opsPerSec, benchBodiesWriteMinOps)
	} else {
		b.Logf("bodies 写入吞吐 %.0f ops/s 达到参考阈值 %.0f ops/s", opsPerSec, benchBodiesWriteMinOps)
	}
}

// marshalBodySize 返回序列化后的负载字节数（用于 SetBytes/MB/s 指标）。
func marshalBodySize(body *storage.SessionBody) (int, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, fmt.Errorf("序列化基准负载: %w", err)
	}
	return len(raw), nil
}

// BenchmarkLiteSessionCreate 度量 lite 模式会话元数据创建
// （SQLite INSERT，含 metadata JSON 序列化）。
func BenchmarkLiteSessionCreate(b *testing.B) {
	ctx := context.Background()
	f, err := factory.NewStorageFactory(newLiteStorageConfig(b, b.TempDir(), 0))
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	sessions := f.NewSessionStore()

	var counter atomic.Int64
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id := fmt.Sprintf("sess-bench-create-%08d", counter.Add(1))
		if err := sessions.CreateSession(ctx, &storage.Session{
			ID:       id,
			TenantID: dualTenant,
			UserID:   "user-bench",
			Metadata: map[string]interface{}{"scene": "benchmark"},
		}); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()

	reportOpsPerSec(b)
}

// BenchmarkLiteStateSetGet 度量 lite 模式 StateStore 一写一读
// （进程内 KV，Set 永不过期 + Get 命中）。
func BenchmarkLiteStateSetGet(b *testing.B) {
	ctx := context.Background()
	f, err := factory.NewStorageFactory(newLiteStorageConfig(b, b.TempDir(), 0))
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	state := f.NewStateStore()

	value := make([]byte, 256)
	for i := range value {
		value[i] = byte('a' + i%26)
	}

	b.SetBytes(int64(len(value)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := state.Set(ctx, "bench:state:key", value, 0); err != nil {
			b.Fatal(err)
		}
		got, err := state.Get(ctx, "bench:state:key")
		if err != nil || got == nil {
			b.Fatalf("state Get 失败: got=%v err=%v", got, err)
		}
	}
	b.StopTimer()

	assertAvgOpBelow(b, benchCacheHitThreshold)
	reportOpsPerSec(b)
}
