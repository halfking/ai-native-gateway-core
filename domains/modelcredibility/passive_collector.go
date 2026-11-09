// passive_collector.go — 被动基线采集器：内存累积真实流量的被动特征，
// 周期刷库时与既有基线**加权合并**（而不是整行覆盖）。
//
// passive_collector.go — the passive baseline collector: buffer per-request
// passive features in memory, then on flush merge them (weighted by sample
// counts) into the persisted baseline of migration 955.
//
// # 为什么合并而不是覆盖
//
// 基线的意义是「这个节点长期的行为画像」。整行覆盖 ⇒ 低流量模型两次
// 刷库之间基线来回跳，判定（EvaluatePassive）对着一个移动靶。
// 加权合并（old×old_n + window×window_n / (old_n+window_n)）让基线
// 平滑演进；sample_count 有上限（PassiveBaselineSampleCap）⇒ 老数据
// 不会永久压制新观察（cap 之后再合并，权重比保持，计数截断）。
//
// # 陈旧基线
//
// collected_to 距今超过 PassiveBaselineStaleAfter 的基线**不合并**、
// 直接重建：上游换版/换后端后老画像只会拖慢再校准。
//
// # 桶化口径
//
// ITT 分布桶化**只走** passive.go 的 bucketizeITTs（955 迁移注释与契约门
// 钉死的 10ms 桶宽 / "100+" 顶桶的唯一实现）——本文件不复制第二份桶逻辑。
package modelcredibility

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

const (
	// PassiveBaselineSampleCap 是基线行保留的最大样本数。
	// 达到后合并仍按真实计数算均值，但落库计数截断到 cap ——
	// 置信度门（min(1, n/100)）早在 100 就饱和，cap 只影响未来合并权重。
	PassiveBaselineSampleCap = 5000

	// PassiveBaselineStaleAfter 是基线的保鲜期。
	// collected_to 早于该窗口的基线视为陈旧（上游换版/换后端后老画像
	// 只会拖慢再校准），flush 时直接重建而不是合并。
	PassiveBaselineStaleAfter = 30 * 24 * time.Hour

	// passiveMaxITTsPerWindow 是单节点单窗口保留的 ITT 原始样本上限
	// （防异常流量把内存吃穿；超过后仍在计数、分布近似不受影响）。
	passiveMaxITTsPerWindow = 20000

	// passiveMaxTTFTsPerWindow 是单节点单窗口保留的 TTFT 原始样本上限。
	passiveMaxTTFTsPerWindow = 4096
)

// passiveKey 是采集窗口的节点键。
type passiveKey struct {
	tenant     string
	credential int64
	model      string
}

// passiveWindow 是一个节点在当前刷库窗口内的累积量。
type passiveWindow struct {
	from time.Time
	to   time.Time

	n int // 有 usage 的请求数（样本数）

	// TTFT（毫秒）：均值/方差用和与平方和；p95 用原始样本。
	ttftSum   float64
	ttftSumSq float64
	ttfts     []float64

	// ITT：留原始 Duration（桶化只走 bucketizeITTs），统计时换算毫秒。
	itts       []time.Duration
	ittDropped int

	// Throughput（tokens/s）。
	tpSum   float64
	tpSumSq float64
	tpN     int

	// OutputDensity（completion/prompt）。
	densitySum   float64
	densitySumSq float64
	densityN     int

	// 格式位标：窗口内并集（OR）。
	formatFlags uint32
}

// PassiveCollector 是线程安全的被动特征累积器。
//
// 生产接线（部署轮）：streaming 路径 ExtractPassiveFeatures 后调用 Add；
// 后台 worker 周期调用 Flush。
type PassiveCollector struct {
	mu      sync.Mutex
	windows map[passiveKey]*passiveWindow
	nowFn   func() time.Time // 可注入时钟（测试用）
}

// NewPassiveCollector 创建采集器。
func NewPassiveCollector() *PassiveCollector {
	return &PassiveCollector{
		windows: make(map[passiveKey]*passiveWindow),
		nowFn:   time.Now,
	}
}

// Add 累积一次请求的被动特征。nil / 空模型名的特征安全忽略。
func (c *PassiveCollector) Add(pf *PassiveFeatures) {
	if pf == nil || pf.ModelName == "" {
		return
	}
	nowFn := c.nowFn
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.windows == nil {
		c.windows = make(map[passiveKey]*passiveWindow)
	}

	key := passiveKey{tenant: pf.TenantID, credential: pf.CredentialID, model: pf.ModelName}
	w, ok := c.windows[key]
	if !ok {
		w = &passiveWindow{from: now}
		c.windows[key] = w
	}
	w.to = now

	// 样本数按「有 usage 的请求」计（token 特征都来自 usage）。
	if pf.PromptTokens > 0 || pf.CompletionTokens > 0 {
		w.n++
	}

	// TTFT（毫秒）。
	if pf.TTFT > 0 {
		ms := durationToMS(pf.TTFT)
		w.ttftSum += ms
		w.ttftSumSq += ms * ms
		if len(w.ttfts) < passiveMaxTTFTsPerWindow {
			w.ttfts = append(w.ttfts, ms)
		}
	}

	// ITT（原始 Duration，>0 且 <5s 的过滤已在提取侧做了，这里再兜一道）。
	for _, itt := range pf.ITTs {
		if itt <= 0 || itt > 5*time.Second {
			continue
		}
		if len(w.itts) >= passiveMaxITTsPerWindow {
			w.ittDropped++
			continue
		}
		w.itts = append(w.itts, itt)
	}

	// Throughput。
	if pf.Throughput > 0 {
		w.tpSum += pf.Throughput
		w.tpSumSq += pf.Throughput * pf.Throughput
		w.tpN++
	}

	// OutputDensity。
	if pf.OutputDensity > 0 {
		w.densitySum += pf.OutputDensity
		w.densitySumSq += pf.OutputDensity * pf.OutputDensity
		w.densityN++
	}

	// 格式位标：并集。
	w.formatFlags |= pf.FormatFlags
}

// Pending 返回当前待刷库的节点数（观测/测试用）。
func (c *PassiveCollector) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.windows)
}

// Flush 把所有窗口与库中基线合并后落库，返回落库的节点数。
//
// ★ 单节点失败不阻断其它节点（记最后一个错误但继续处理余下节点）。
func (c *PassiveCollector) Flush(ctx context.Context, sink PassiveBaselineSink) (int, error) {
	if sink == nil {
		return 0, fmt.Errorf("passive collector: nil sink")
	}

	c.mu.Lock()
	windows := c.windows
	c.windows = make(map[passiveKey]*passiveWindow)
	c.mu.Unlock()

	flushed := 0
	var lastErr error
	for key, w := range windows {
		baseline := c.buildMergedBaseline(ctx, sink, key, w)
		if err := sink.UpsertPassiveBaseline(ctx, baseline); err != nil {
			lastErr = err
			continue
		}
		flushed++
	}
	return flushed, lastErr
}

// buildMergedBaseline 把窗口聚合与库中既有基线合并成一个待落库的基线。
//
// 合并规则（每个统计量独立）：
//   - 均值/分布：按样本数加权（old_n×old + win_n×win)/(old_n+win_n)；
//   - 标准差：加权合并（近似，够判定用）；
//   - p50/p95：从合并后的桶化分布近似（桶中点 × 累积概率）；
//   - 格式位标：OR 并集；
//   - 样本数：真实求和后截断到 cap；
//   - 陈旧基线（collected_to 早于 StaleAfter）或样本为 0 的基线：丢弃重建。
func (c *PassiveCollector) buildMergedBaseline(ctx context.Context, sink PassiveBaselineSink, key passiveKey, w *passiveWindow) *PassiveFingerprint {
	nowFn := c.nowFn
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn()

	// ── 窗口自身的聚合 ──
	ittMS := msSlice(w.itts)
	winDist := bucketizeITTs(w.itts)
	winN := len(w.itts)
	win := windowStats{
		n:           w.n,
		ttftMean:    meanOf(w.ttftSum, w.n),
		ttftP95:     percentileOf(w.ttfts, 0.95),
		ittMean:     meanOf(sumOf(ittMS), winN),
		ittStd:      stdOf(ittMS),
		ittDist:     winDist,
		tpMean:      meanOf(w.tpSum, w.tpN),
		tpStd:       stdOfSample(w.tpSum, w.tpSumSq, w.tpN),
		densityMean: meanOf(w.densitySum, w.densityN),
		densityStd:  stdOfSample(w.densitySum, w.densitySumSq, w.densityN),
	}

	out := &PassiveFingerprint{
		TenantID:      key.tenant,
		ModelID:       key.model,
		CredentialID:  key.credential,
		CollectedFrom: w.from,
		CollectedTo:   w.to,
		UpdatedAt:     now,
	}

	// ── 读取既有基线 ──
	existing, err := sink.LoadPassiveBaseline(ctx, key.credential, key.model)
	if err == nil && existing != nil &&
		existing.SampleCount > 0 &&
		!existing.CollectedTo.IsZero() &&
		now.Sub(existing.CollectedTo) < PassiveBaselineStaleAfter {

		oldN := existing.SampleCount
		newN := oldN + win.n
		// ITT 的权重与请求数解耦：ITT 是 per-token 间隔样本。
		ittTotal := oldN + winN
		if ittTotal <= 0 {
			ittTotal = 1
		}

		out.TTFTMean = mergeMS(existing.TTFTMean, oldN, win.ttftMean, win.n)
		out.TTFTP95 = mergeMS(existing.TTFTP95, oldN, win.ttftP95, win.n)
		out.ITTMean = mergeMS(existing.ITTMean, oldN, win.ittMean, winN)
		out.ITTStd = mergeMS(existing.ITTStd, oldN, win.ittStd, winN)
		out.ThroughputMean = mergeFloat(existing.ThroughputMean, oldN, win.tpMean, w.tpN)
		out.ThroughputStd = mergeFloat(existing.ThroughputStd, oldN, win.tpStd, w.tpN)
		out.OutputDensityMean = mergeFloat(existing.OutputDensityMean, oldN, win.densityMean, w.densityN)
		out.OutputDensityStd = mergeFloat(existing.OutputDensityStd, oldN, win.densityStd, w.densityN)
		out.ITTDistribution = mergeDistribution(existing.ITTDistribution, ittTotal, win.ittDist, winN)
		out.FormatFlags = existing.FormatFlags | w.formatFlags
		out.SampleCount = minInt(newN, PassiveBaselineSampleCap)
		out.CollectedFrom = earlier(existing.CollectedFrom, w.from)
	} else {
		// 没有可合并的基线（无行 / 陈旧 / 样本为 0）⇒ 以本窗口重建。
		out.TTFTMean = msOf(win.ttftMean)
		out.TTFTP95 = msOf(win.ttftP95)
		out.ITTMean = msOf(win.ittMean)
		out.ITTStd = msOf(win.ittStd)
		out.ThroughputMean = win.tpMean
		out.ThroughputStd = win.tpStd
		out.OutputDensityMean = win.densityMean
		out.OutputDensityStd = win.densityStd
		out.ITTDistribution = win.ittDist
		out.FormatFlags = w.formatFlags
		out.SampleCount = minInt(win.n, PassiveBaselineSampleCap)
	}

	// p50/p95 从合并分布近似（桶中点 × 累积概率）。
	if len(out.ITTDistribution) > 0 {
		out.ITTP50 = msOf(PercentileFromDistribution(out.ITTDistribution, 0.50))
		out.ITTP95 = msOf(PercentileFromDistribution(out.ITTDistribution, 0.95))
	}
	return out
}

// windowStats 是窗口聚合的中间形状（毫秒世界）。
type windowStats struct {
	n           int
	ttftMean    float64
	ttftP95     float64
	ittMean     float64
	ittStd      float64
	ittDist     map[string]float64
	tpMean      float64
	tpStd       float64
	densityMean float64
	densityStd  float64
}

// ── 纯函数（单测直测）───────────────────────────────────────────────

// durationToMS 把 Duration 换算成毫秒（浮点，保留亚毫秒精度）。
func durationToMS(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000.0
}

// msSlice 批量换算。
func msSlice(ds []time.Duration) []float64 {
	if len(ds) == 0 {
		return nil
	}
	out := make([]float64, len(ds))
	for i, d := range ds {
		out[i] = durationToMS(d)
	}
	return out
}

// msOf 毫秒浮点转回 Duration（0 保持 0）。
func msOf(ms float64) time.Duration {
	if ms <= 0 {
		return 0
	}
	return time.Duration(ms * float64(time.Millisecond))
}

func meanOf(sum float64, n int) float64 {
	if n <= 0 {
		return 0
	}
	return sum / float64(n)
}

func sumOf(xs []float64) float64 {
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s
}

// stdOf 样本标准差（n≤1 ⇒ 0）。
func stdOf(xs []float64) float64 {
	if len(xs) <= 1 {
		return 0
	}
	mean := sumOf(xs) / float64(len(xs))
	ss := 0.0
	for _, x := range xs {
		d := x - mean
		ss += d * d
	}
	return sqrtOf(ss / float64(len(xs)-1))
}

// stdOfSample 从和与平方和算**样本**标准差（n-1 分母，与 stdOf 同一口径；
// n≤1 ⇒ 0）。
func stdOfSample(sum, sumSq float64, n int) float64 {
	if n <= 1 {
		return 0
	}
	mean := sum / float64(n)
	variance := (sumSq - float64(n)*mean*mean) / float64(n-1)
	if variance < 0 {
		variance = 0
	}
	return sqrtOf(variance)
}

func sqrtOf(x float64) float64 {
	if x <= 0 {
		return 0
	}
	// 牛顿法开方（避免仅为 sqrt 引入额外依赖路径；40 轮对 float64 足够收敛）。
	z := x
	for i := 0; i < 40; i++ {
		z = (z + x/z) / 2
	}
	return z
}

// percentileOf 原始样本的百分位（线性索引近似）。
func percentileOf(xs []float64, q float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sorted := make([]float64, len(xs))
	copy(sorted, xs)
	sort.Float64s(sorted)
	idx := int(q * float64(len(sorted)-1))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// PercentileFromDistribution 从桶化分布近似百分位（桶中点代表值）。
// "100+" 桶的中点取 150ms（开放桶的约定代表值，只用于 p50/p95 展示）。
func PercentileFromDistribution(dist map[string]float64, q float64) float64 {
	if len(dist) == 0 {
		return 0
	}
	labels := make([]string, 0, len(dist))
	for l := range dist {
		labels = append(labels, l)
	}
	sort.Slice(labels, func(i, j int) bool {
		return ittBucketOrder(labels[i]) < ittBucketOrder(labels[j])
	})
	acc := 0.0
	for _, l := range labels {
		acc += dist[l]
		if acc >= q {
			return ittBucketMidpoint(l)
		}
	}
	return ittBucketMidpoint(labels[len(labels)-1])
}

// ittBucketOrder 桶标签 → 排序序号（"0"→0，"10"→1，…，"100+"→10）。
func ittBucketOrder(label string) int {
	if label == "100+" {
		return 10
	}
	var v int
	if _, err := fmt.Sscanf(label, "%d", &v); err != nil {
		return 0
	}
	return v / 10
}

// ittBucketMidpoint 桶标签 → 中点代表值（毫秒）。
func ittBucketMidpoint(label string) float64 {
	if label == "100+" {
		return 150
	}
	var v int
	if _, err := fmt.Sscanf(label, "%d", &v); err != nil {
		return 0
	}
	return float64(v) + 5
}

// mergeMS / mergeFloat —— 加权均值合并（权重 = 样本数）。
// 0 视为「未测量」按仓内口径处理（不把 0 当有效观测合并进来）。
func mergeMS(old time.Duration, oldN int, winMS float64, winN int) time.Duration {
	return msOf(mergeFloat(durationToMS(old), oldN, winMS, winN))
}

func mergeFloat(old float64, oldN int, win float64, winN int) float64 {
	total := oldN + winN
	if total <= 0 {
		return 0
	}
	if old == 0 {
		return win
	}
	if win == 0 {
		return old
	}
	return (old*float64(oldN) + win*float64(winN)) / float64(total)
}

// mergeDistribution 加权合并两个桶化分布（键取并集，权重 = 样本数）。
func mergeDistribution(old map[string]float64, oldN int, win map[string]float64, winN int) map[string]float64 {
	total := oldN + winN
	if total <= 0 {
		return nil
	}
	if len(old) == 0 {
		return win
	}
	if len(win) == 0 {
		return old
	}
	keys := make(map[string]struct{}, len(old)+len(win))
	for k := range old {
		keys[k] = struct{}{}
	}
	for k := range win {
		keys[k] = struct{}{}
	}
	merged := make(map[string]float64, len(keys))
	for k := range keys {
		merged[k] = (old[k]*float64(oldN) + win[k]*float64(winN)) / float64(total)
	}
	return merged
}

func earlier(a, b time.Time) time.Time {
	if a.IsZero() {
		return b
	}
	if b.IsZero() {
		return a
	}
	if a.Before(b) {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
