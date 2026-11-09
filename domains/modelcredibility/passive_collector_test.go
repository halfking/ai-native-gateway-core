package modelcredibility

import (
	"context"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"
)

// fakePassiveSink 是 PassiveBaselineSink 的内存替身（单测用）。
type fakePassiveSink struct {
	mu        sync.Mutex
	baselines map[string]*PassiveFingerprint // key: cred|model
	upsertErr error
	loadErr   error
}

func fakeKey(credentialID int64, model string) string {
	return fmt.Sprintf("%d|%s", credentialID, model)
}

func (f *fakePassiveSink) UpsertPassiveBaseline(_ context.Context, fp *PassiveFingerprint) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *fp
	f.baselines[fakeKey(fp.CredentialID, fp.ModelID)] = &cp
	return nil
}

func (f *fakePassiveSink) LoadPassiveBaseline(_ context.Context, credentialID int64, modelName string) (*PassiveFingerprint, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if fp, ok := f.baselines[fakeKey(credentialID, modelName)]; ok {
		cp := *fp
		return &cp, nil
	}
	return nil, nil
}

// makeFeatures 构造一批带固定 ITT/TTFT 的合成特征。
func makeFeatures(model string, cred int64, ittMS, ttftMS int, n int) []*PassiveFeatures {
	out := make([]*PassiveFeatures, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, &PassiveFeatures{
			TenantID:         "default",
			ModelName:        model,
			CredentialID:     cred,
			TTFT:             time.Duration(ttftMS) * time.Millisecond,
			ITTs:             []time.Duration{time.Duration(ittMS) * time.Millisecond, time.Duration(ittMS) * time.Millisecond},
			Throughput:       22.0,
			PromptTokens:     100,
			CompletionTokens: 80,
			OutputDensity:    0.8,
			FormatFlags:      0x01,
		})
	}
	return out
}

// TestPassiveCollectorWindowAggregate 验证窗口聚合（无既有基线时重建）。
func TestPassiveCollectorWindowAggregate(t *testing.T) {
	sink := &fakePassiveSink{baselines: map[string]*PassiveFingerprint{}}
	c := NewPassiveCollector()

	for _, pf := range makeFeatures("claude-opus-5-5", 31, 45, 1000, 10) {
		c.Add(pf)
	}
	if got := c.Pending(); got != 1 {
		t.Fatalf("Pending = %d, want 1", got)
	}

	flushed, err := c.Flush(context.Background(), sink)
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if flushed != 1 {
		t.Fatalf("flushed = %d, want 1", flushed)
	}
	if c.Pending() != 0 {
		t.Fatalf("Pending after flush = %d, want 0", c.Pending())
	}

	fp, err := sink.LoadPassiveBaseline(context.Background(), 31, "claude-opus-5-5")
	if err != nil || fp == nil {
		t.Fatalf("load baseline: %v, %+v", err, fp)
	}

	// TTFT 均值 = 1000ms。
	if math.Abs(durationToMS(fp.TTFTMean)-1000) > 0.01 {
		t.Errorf("TTFTMean = %v ms, want 1000", durationToMS(fp.TTFTMean))
	}
	// ITT 均值 = 45ms（10 个请求 × 2 ITT）。
	if math.Abs(durationToMS(fp.ITTMean)-45) > 0.01 {
		t.Errorf("ITTMean = %v ms, want 45", durationToMS(fp.ITTMean))
	}
	// 全部 ITT 都在 40-49ms 桶 ⇒ 分布只有一个桶。
	if len(fp.ITTDistribution) != 1 {
		t.Errorf("ITTDistribution buckets = %d (%v), want 1", len(fp.ITTDistribution), fp.ITTDistribution)
	}
	if p, ok := fp.ITTDistribution["40"]; !ok || math.Abs(p-1.0) > 0.001 {
		t.Errorf("ITT bucket '40' = %v (ok=%v), want 1.0", p, ok)
	}
	// 样本数 = 10。
	if fp.SampleCount != 10 {
		t.Errorf("SampleCount = %d, want 10", fp.SampleCount)
	}
	// 吞吐 = 22。
	if math.Abs(fp.ThroughputMean-22.0) > 0.001 {
		t.Errorf("ThroughputMean = %v, want 22", fp.ThroughputMean)
	}
	// 输出密度 = 0.8。
	if math.Abs(fp.OutputDensityMean-0.8) > 0.001 {
		t.Errorf("OutputDensityMean = %v, want 0.8", fp.OutputDensityMean)
	}
	// 格式位标 OR = 0x01。
	if fp.FormatFlags != 0x01 {
		t.Errorf("FormatFlags = %#x, want 0x01", fp.FormatFlags)
	}
}

// TestPassiveCollectorMergesWithExisting 验证与既有基线的加权合并。
func TestPassiveCollectorMergesWithExisting(t *testing.T) {
	sink := &fakePassiveSink{baselines: map[string]*PassiveFingerprint{}}
	// 既有基线：TTFT 1000ms × 100 样本。
	existing := &PassiveFingerprint{
		TenantID:       "default",
		ModelID:        "claude-opus-5-5",
		CredentialID:   31,
		TTFTMean:       1000 * time.Millisecond,
		ThroughputMean: 22.0,
		SampleCount:    100,
		CollectedFrom:  time.Now().Add(-2 * time.Hour),
		CollectedTo:    time.Now().Add(-1 * time.Hour),
		FormatFlags:    0x02,
	}
	if err := sink.UpsertPassiveBaseline(context.Background(), existing); err != nil {
		t.Fatalf("seed baseline: %v", err)
	}

	// 新窗口：TTFT 500ms × 100 样本（骤降一半 —— 若被合并覆盖，均值应为 750）。
	c := NewPassiveCollector()
	for _, pf := range makeFeatures("claude-opus-5-5", 31, 45, 500, 100) {
		c.Add(pf)
	}
	if _, err := c.Flush(context.Background(), sink); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	fp, _ := sink.LoadPassiveBaseline(context.Background(), 31, "claude-opus-5-5")
	if fp == nil {
		t.Fatal("baseline missing after flush")
	}
	gotMS := durationToMS(fp.TTFTMean)
	if math.Abs(gotMS-750) > 0.01 {
		t.Errorf("merged TTFTMean = %v ms, want 750 (=(1000×100+500×100)/200)", gotMS)
	}
	if fp.SampleCount != 200 {
		t.Errorf("merged SampleCount = %d, want 200", fp.SampleCount)
	}
	// 格式位标 = 既有 0x02 | 窗口 0x01 = 0x03。
	if fp.FormatFlags != 0x03 {
		t.Errorf("merged FormatFlags = %#x, want 0x03", fp.FormatFlags)
	}
	// CollectedFrom 应延续既有基线的更早时间。
	if !fp.CollectedFrom.Equal(existing.CollectedFrom) {
		t.Errorf("merged CollectedFrom = %v, want %v (earlier wins)", fp.CollectedFrom, existing.CollectedFrom)
	}
}

// TestPassiveCollectorStaleBaselineRebuilt 验证陈旧基线不合并、直接重建。
func TestPassiveCollectorStaleBaselineRebuilt(t *testing.T) {
	sink := &fakePassiveSink{baselines: map[string]*PassiveFingerprint{}}
	stale := &PassiveFingerprint{
		TenantID:      "default",
		ModelID:       "claude-opus-5-5",
		CredentialID:  31,
		TTFTMean:      5000 * time.Millisecond,
		SampleCount:   100,
		CollectedFrom: time.Now().Add(-40 * 24 * time.Hour),
		CollectedTo:   time.Now().Add(-39 * 24 * time.Hour), // 早于 StaleAfter（30d）
	}
	if err := sink.UpsertPassiveBaseline(context.Background(), stale); err != nil {
		t.Fatalf("seed stale baseline: %v", err)
	}

	c := NewPassiveCollector()
	for _, pf := range makeFeatures("claude-opus-5-5", 31, 45, 1000, 20) {
		c.Add(pf)
	}
	if _, err := c.Flush(context.Background(), sink); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	fp, _ := sink.LoadPassiveBaseline(context.Background(), 31, "claude-opus-5-5")
	if fp == nil {
		t.Fatal("baseline missing")
	}
	// 陈旧基线（5000ms）不应把新窗口（1000ms）拉偏 —— 重建后均值就是窗口值。
	if math.Abs(durationToMS(fp.TTFTMean)-1000) > 0.01 {
		t.Errorf("rebuilt TTFTMean = %v ms, want 1000 (stale baseline must be discarded)", durationToMS(fp.TTFTMean))
	}
	if fp.SampleCount != 20 {
		t.Errorf("rebuilt SampleCount = %d, want 20", fp.SampleCount)
	}
}

// TestPassiveCollectorSampleCap 验证样本数截断到 cap（合并权重仍按真实计数）。
func TestPassiveCollectorSampleCap(t *testing.T) {
	sink := &fakePassiveSink{baselines: map[string]*PassiveFingerprint{}}
	c := NewPassiveCollector()

	// cap=5000：塞 6000 个样本，落库计数应为 5000。
	for _, pf := range makeFeatures("m", 1, 45, 1000, 6000) {
		c.Add(pf)
	}
	if _, err := c.Flush(context.Background(), sink); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	fp, _ := sink.LoadPassiveBaseline(context.Background(), 1, "m")
	if fp == nil {
		t.Fatal("baseline missing")
	}
	if fp.SampleCount != PassiveBaselineSampleCap {
		t.Errorf("SampleCount = %d, want cap %d", fp.SampleCount, PassiveBaselineSampleCap)
	}
	// 均值不受截断影响。
	if math.Abs(durationToMS(fp.TTFTMean)-1000) > 0.01 {
		t.Errorf("TTFTMean = %v, want 1000", durationToMS(fp.TTFTMean))
	}
}

// TestPassiveCollectorFlushErrorKeepsOtherNodes 验证单节点落库失败不阻断其余节点。
func TestPassiveCollectorFlushErrorKeepsOtherNodes(t *testing.T) {
	// upsertErr 恒失败 ⇒ Flush 返回错误且 flushed=0，但不 panic、窗口被消费。
	sink := &fakePassiveSink{baselines: map[string]*PassiveFingerprint{}, upsertErr: fmt.Errorf("db down")}
	c := NewPassiveCollector()
	for _, pf := range makeFeatures("m", 1, 45, 1000, 5) {
		c.Add(pf)
	}
	flushed, err := c.Flush(context.Background(), sink)
	if err == nil {
		t.Fatal("expected error from failing sink")
	}
	if flushed != 0 {
		t.Errorf("flushed = %d, want 0", flushed)
	}
	if c.Pending() != 0 {
		t.Errorf("window should be consumed even on failure, Pending=%d", c.Pending())
	}
}

// TestPassiveCollectorNilAndEmptyAdds 验证 nil / 空特征安全忽略。
func TestPassiveCollectorNilAndEmptyAdds(t *testing.T) {
	c := NewPassiveCollector()
	c.Add(nil)
	c.Add(&PassiveFeatures{})               // 空模型名
	c.Add(&PassiveFeatures{ModelName: "m"}) // 无 usage / 无时序
	if c.Pending() != 1 {
		t.Errorf("Pending = %d, want 1（无 usage 的特征也建窗口，但样本数为 0）", c.Pending())
	}
}

// TestPassiveCollectorConcurrentAdd 并发 Add 不丢样本（race 检测辅助）。
func TestPassiveCollectorConcurrentAdd(t *testing.T) {
	c := NewPassiveCollector()
	sink := &fakePassiveSink{baselines: map[string]*PassiveFingerprint{}}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, pf := range makeFeatures("m", 1, 45, 1000, 50) {
				c.Add(pf)
			}
		}()
	}
	wg.Wait()
	if _, err := c.Flush(context.Background(), sink); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	fp, _ := sink.LoadPassiveBaseline(context.Background(), 1, "m")
	if fp == nil || fp.SampleCount != 400 {
		t.Fatalf("SampleCount = %+v, want 400 (8×50, no lost samples)", fp)
	}
}

// TestBucketizeAndPercentileFromDistribution 验证桶化与分布百分位。
//
// ★ 桶化只有一个实现（passive.go 的 bucketizeITTs，955 迁移契约门钉死），
// 本测试统一走 Duration 入口。
func TestBucketizeAndPercentileFromDistribution(t *testing.T) {
	// 20 个样本：18 个 45ms（40 桶）+ 2 个 120ms（100+ 桶）。
	durs := make([]time.Duration, 0, 20)
	for i := 0; i < 18; i++ {
		durs = append(durs, 45*time.Millisecond)
	}
	durs = append(durs, 120*time.Millisecond, 120*time.Millisecond)
	dist := bucketizeITTs(durs)
	if len(dist) != 2 {
		t.Fatalf("buckets = %v, want 2", dist)
	}
	if math.Abs(dist["40"]-0.9) > 0.001 || math.Abs(dist["100+"]-0.1) > 0.001 {
		t.Errorf("dist = %v, want {40:0.9, 100+:0.1}", dist)
	}

	// p50 应落在 40 桶（中点 45ms），p95 落在 100+ 桶（中点 150ms）。
	p50 := PercentileFromDistribution(dist, 0.50)
	if math.Abs(p50-45) > 0.001 {
		t.Errorf("p50 = %v, want 45", p50)
	}
	p95 := PercentileFromDistribution(dist, 0.95)
	if math.Abs(p95-150) > 0.001 {
		t.Errorf("p95 = %v, want 150", p95)
	}
}

// TestStdOfSample 验证单遍标准差。
func TestStdOfSample(t *testing.T) {
	// 样本 {2, 4, 4, 4, 5, 5, 7, 9}：样本标准差 = 2.138...。
	xs := []float64{2, 4, 4, 4, 5, 5, 7, 9}
	sum, sumSq := 0.0, 0.0
	for _, x := range xs {
		sum += x
		sumSq += x * x
	}
	got := stdOfSample(sum, sumSq, len(xs))
	want := stdOf(xs)
	if math.Abs(got-want) > 0.001 {
		t.Errorf("stdOfSample = %v, want %v", got, want)
	}
}
