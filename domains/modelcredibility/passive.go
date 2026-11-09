// passive.go — 被动式模型指纹：从真实用户会话提取统计特征。
//
// passive.go — passive model fingerprinting: extract statistical features
// from real user conversations without probing.
//
// # 为什么被动比主动更好
//
// 主动探针的问题：
//   - 成本：240 次请求/节点/周 = $0.0053/节点
//   - 可规避：上游可识别探针特征（固定提示词、temperature=1.0）
//   - 覆盖有限：定期扫描，无法实时监控
//
// 被动分析的优势：
//   - ✅ 零成本：利用真实流量，不发额外请求
//   - ✅ 难规避：上游无法区分监控流量和正常流量
//   - ✅ 全量覆盖：每个用户请求都是数据点
//   - ✅ 实时检测：流式响应时即可分析
//
// # 可提取的被动特征
//
// 1. **时序特征**（最有效，60% 权重）
//   - Time-to-First-Token (TTFT)：Opus ~1000ms, Haiku ~300ms
//   - Inter-Token Time (ITT) 分布：每个模型生成速度不同
//   - Throughput：Opus ~22 tok/s, Haiku ~40 tok/s
//
// 2. **Token 签名**（25% 权重）
//   - Prompt Token 计数：固定 prompt → 不同模型 token 数不同
//   - Output Density：completion_tokens / prompt_tokens
//
// 3. **内容特征**（15% 权重）
//   - 词汇偏好：Opus 偏好 "certainly", GPT-4 偏好 "absolutely"
//   - 格式风格：Markdown 使用习惯
//
// # 学术支撑
//
// - Traffic Analysis on Cloud LLM (arXiv:2609.31877): 97.7% 准确率
// - Inter-Token Timing (arXiv:2502.20589): 实时、加密流量可用
// - Browser Agent Fingerprinting (arXiv:2605.14786): 96% F1 score
package modelcredibility

import (
	"context"
	"math"
	"strings"
	"time"
)

// PassiveFeatures 是从单次真实会话提取的被动特征。
type PassiveFeatures struct {
	// ===== 时序特征 (Timing Fingerprints) =====

	// TTFT 是 Time-to-First-Token（首 token 延迟）
	// Opus: ~1000ms, Sonnet: ~500ms, Haiku: ~300ms
	TTFT time.Duration

	// ITTs 是 Inter-Token Times（Token 间隔时间序列）
	// 每个模型的 ITT 分布不同，可用 JSD 对比
	ITTs []time.Duration

	// Throughput 是吞吐量（tokens/second）
	// Opus: ~22, Sonnet: ~28, Haiku: ~40
	Throughput float64

	// ===== Token 特征 (Usage Fingerprints) =====

	// PromptTokens 是 prompt token 计数
	// 用于验证分词器（同族模型应相同）
	PromptTokens int

	// CompletionTokens 是 completion token 计数
	CompletionTokens int

	// OutputDensity 是输出密度（completion / prompt）
	// Opus 倾向更详细（密度高），Haiku 更简洁
	OutputDensity float64

	// ===== 内容特征 (Content Fingerprints) =====

	// ContentLength 是响应内容长度（字节）
	ContentLength int

	// VocabFreq 是高频词频率（可选，轻量采集时为 nil）
	VocabFreq map[string]int

	// FormatFlags 是格式特征位标
	// bit 0: 使用 ###
	// bit 1: 使用 **bold**
	// bit 2: 使用 list (- / 1.)
	// bit 3: 使用 code block ```
	FormatFlags uint32

	// ===== 元数据 =====

	// TenantID 是租户（空 ⇒ default）
	TenantID string

	// ModelName 是请求的模型名
	ModelName string

	// CredentialID 是凭据 ID
	CredentialID int64

	// RequestID 是请求标识
	RequestID string

	// MeasuredAt 是测量时间
	MeasuredAt time.Time
}

// PassiveFingerprint 是一个模型的被动指纹（聚合统计）。
//
// 从多次真实请求聚合得出，用于对比检测。
type PassiveFingerprint struct {
	// TenantID 是租户（空 ⇒ default）
	TenantID string

	// ModelID 是模型标识（raw_model_name 或 canonical_name）
	ModelID string

	// CredentialID 是凭据 ID（0 表示跨凭据聚合）
	CredentialID int64

	// ===== 时序统计 =====

	// TTFT 统计
	TTFTMean time.Duration
	TTFTP95  time.Duration

	// ITT 分布（用于 JSD 对比）
	ITTDistribution map[string]float64 // 桶化：0-10ms, 10-20ms, ...
	ITTMean         time.Duration
	ITTStd          time.Duration
	ITTP50          time.Duration
	ITTP95          time.Duration

	// Throughput 统计
	ThroughputMean float64
	ThroughputStd  float64

	// ===== Token 统计 =====

	// PromptTokenSignature 是标准 prompt 的 token 签名
	// key: prompt 内容 hash, value: token count
	PromptTokenSignature map[string]int

	// OutputDensity 统计
	OutputDensityMean float64
	OutputDensityStd  float64

	// ===== 内容统计 =====

	// VocabFreq 是高频词频率
	VocabFreq map[string]float64

	// FormatFlags 是格式特征位标（聚合后的众数）
	FormatFlags uint32

	// ===== 元数据 =====

	// SampleCount 是样本数
	SampleCount int

	// CollectedFrom 是采集起始时间
	CollectedFrom time.Time

	// CollectedTo 是采集结束时间
	CollectedTo time.Time

	// UpdatedAt 是更新时间
	UpdatedAt time.Time
}

// PassiveVerdict 是被动判定结果。
type PassiveVerdict struct {
	// TimingScore 是时序特征匹配度（0-100）
	TimingScore float64

	// TokenScore 是 Token 签名匹配度（0-100）
	TokenScore float64

	// ContentScore 是内容特征匹配度（0-100）
	ContentScore float64

	// OverallScore 是综合分（0-100）
	// = TimingScore * 0.6 + TokenScore * 0.25 + ContentScore * 0.15
	OverallScore float64

	// Confidence 是置信度（0-1），取决于样本量
	// confidence = min(1.0, sampleCount / 100.0)
	Confidence float64

	// Anomalies 是异常项
	Anomalies []string

	// Evidence 是判定证据（结构化）
	Evidence map[string]any
}

// ExtractPassiveFeatures 从流式响应提取被动特征。
//
// ★ 集成点：在 domains/streaming 的流处理完成后调用，传入：
//   - chunks: StreamChunk 序列（已包含 EmitAt 时间戳）
//   - usage: 最终的 usage 信息（prompt_tokens, completion_tokens）
//
// 返回提取的被动特征。
func ExtractPassiveFeatures(ctx context.Context, chunks []StreamChunk, usage Usage, metadata Metadata) (*PassiveFeatures, error) {
	if len(chunks) == 0 {
		return nil, nil // 无 chunk，无法提取
	}

	pf := &PassiveFeatures{
		ModelName:    metadata.ModelName,
		CredentialID: metadata.CredentialID,
		RequestID:    metadata.RequestID,
		MeasuredAt:   time.Now(),
	}

	// ===== 1. 提取时序特征 =====

	// TTFT: 第一个 content chunk 的 EmitAt - 请求发送时间
	var firstContentTime time.Time
	for i, chunk := range chunks {
		if chunk.Type == "content" && i == 0 {
			firstContentTime = chunk.EmitAt
			break
		}
	}
	if !firstContentTime.IsZero() && !metadata.RequestSentAt.IsZero() {
		pf.TTFT = firstContentTime.Sub(metadata.RequestSentAt)
	}

	// ITTs: 相邻 content chunk 的时间差
	var lastContentTime time.Time
	for _, chunk := range chunks {
		if chunk.Type != "content" {
			continue
		}
		if !lastContentTime.IsZero() {
			itt := chunk.EmitAt.Sub(lastContentTime)
			if itt > 0 && itt < 5*time.Second { // 过滤异常值
				pf.ITTs = append(pf.ITTs, itt)
			}
		}
		lastContentTime = chunk.EmitAt
	}

	// Throughput: tokens / (最后 chunk - 第一 chunk)
	if len(chunks) >= 2 && usage.CompletionTokens > 0 {
		lastTime := chunks[len(chunks)-1].EmitAt
		duration := lastTime.Sub(firstContentTime).Seconds()
		if duration > 0 {
			pf.Throughput = float64(usage.CompletionTokens) / duration
		}
	}

	// ===== 2. 提取 Token 特征 =====

	pf.PromptTokens = usage.PromptTokens
	pf.CompletionTokens = usage.CompletionTokens

	if usage.PromptTokens > 0 {
		pf.OutputDensity = float64(usage.CompletionTokens) / float64(usage.PromptTokens)
	}

	// ===== 3. 提取内容特征 =====

	// 拼接所有 content chunk
	var contentBuilder strings.Builder
	for _, chunk := range chunks {
		if chunk.Type == "content" {
			contentBuilder.Write(chunk.Data)
		}
	}
	content := contentBuilder.String()
	pf.ContentLength = len(content)

	// 格式特征位标（轻量级，不需要完整解析）
	if strings.Contains(content, "###") {
		pf.FormatFlags |= 0x01 // bit 0: 使用 ###
	}
	if strings.Contains(content, "**") {
		pf.FormatFlags |= 0x02 // bit 1: 使用 **bold**
	}
	if strings.Contains(content, "\n- ") || strings.Contains(content, "\n1. ") {
		pf.FormatFlags |= 0x04 // bit 2: 使用 list
	}
	if strings.Contains(content, "```") {
		pf.FormatFlags |= 0x08 // bit 3: 使用 code block
	}

	// 词汇频率（可选，轻量采集时跳过）
	if metadata.EnableVocabAnalysis {
		pf.VocabFreq = extractVocabFreq(content)
	}

	return pf, nil
}

// StreamChunk 是流式块（复用 domains/streaming/types.go 的定义）
type StreamChunk struct {
	Data   []byte
	Type   string
	Index  int
	EmitAt time.Time
}

// Usage 是 token 使用情况
type Usage struct {
	PromptTokens     int
	CompletionTokens int
}

// Metadata 是提取所需的元数据
type Metadata struct {
	TenantID            string
	ModelName           string
	CredentialID        int64
	RequestID           string
	RequestSentAt       time.Time
	EnableVocabAnalysis bool // 是否启用词汇分析（成本考量）
}

// extractVocabFreq 提取词汇频率（简化版）
func extractVocabFreq(content string) map[string]int {
	freq := make(map[string]int)

	// 简单分词：按空格分割，转小写
	words := strings.Fields(strings.ToLower(content))

	for _, word := range words {
		// 过滤标点符号
		word = strings.Trim(word, ".,!?;:\"'()[]{}—-")
		if len(word) < 3 { // 过滤短词
			continue
		}
		freq[word]++
	}

	return freq
}

// EvaluatePassive 执行被动判定。
//
// 对比观测特征与基线指纹，输出匹配度分数。
func EvaluatePassive(observed *PassiveFeatures, baseline *PassiveFingerprint) PassiveVerdict {
	if observed == nil || baseline == nil {
		return PassiveVerdict{
			OverallScore: 0,
			Confidence:   0,
			Anomalies:    []string{AnomalyPassiveNoData},
		}
	}

	verdict := PassiveVerdict{
		Evidence: make(map[string]any),
	}

	// ===== 1. 时序特征匹配度 (60% 权重) =====

	var timingScore float64

	// 1.1 ITT 分布对比（JSD）
	if len(observed.ITTs) >= 10 && len(baseline.ITTDistribution) > 0 {
		observedDist := bucketizeITTs(observed.ITTs)
		jsd := ComputeJSD(observedDist, baseline.ITTDistribution)

		// JSD → 分数：≤0.30 = 100, ≥0.50 = 0
		var ittScore float64
		if jsd <= 0.30 {
			ittScore = 100
		} else if jsd >= 0.50 {
			ittScore = 0
		} else {
			ittScore = 100 - ((jsd-0.30)/0.20)*100
		}

		timingScore += ittScore * 0.5 // ITT 占时序分的 50%
		verdict.Evidence["itt_jsd"] = jsd

		if jsd > 0.40 {
			verdict.Anomalies = append(verdict.Anomalies, AnomalyPassiveITTMismatch)
		}
	}

	// 1.2 TTFT 偏差
	if observed.TTFT > 0 && baseline.TTFTMean > 0 {
		deviation := math.Abs(float64(observed.TTFT-baseline.TTFTMean)) / float64(baseline.TTFTMean)

		// 偏差 ≤30% = 100, ≥50% = 0
		var ttftScore float64
		if deviation <= 0.30 {
			ttftScore = 100
		} else if deviation >= 0.50 {
			ttftScore = 0
		} else {
			ttftScore = 100 - ((deviation-0.30)/0.20)*100
		}

		timingScore += ttftScore * 0.25 // TTFT 占时序分的 25%
		verdict.Evidence["ttft_deviation"] = deviation

		if deviation > 0.40 {
			verdict.Anomalies = append(verdict.Anomalies, AnomalyPassiveTTFTAnomaly)
		}
	}

	// 1.3 Throughput 偏差
	if observed.Throughput > 0 && baseline.ThroughputMean > 0 {
		deviation := math.Abs(observed.Throughput-baseline.ThroughputMean) / baseline.ThroughputMean

		// 偏差 ≤20% = 100, ≥40% = 0
		var tpScore float64
		if deviation <= 0.20 {
			tpScore = 100
		} else if deviation >= 0.40 {
			tpScore = 0
		} else {
			tpScore = 100 - ((deviation-0.20)/0.20)*100
		}

		timingScore += tpScore * 0.25 // Throughput 占时序分的 25%
		verdict.Evidence["throughput_deviation"] = deviation

		if deviation > 0.50 {
			verdict.Anomalies = append(verdict.Anomalies, AnomalyPassiveThroughputAnomaly)
		}
	}

	verdict.TimingScore = timingScore

	// ===== 2. Token 签名匹配度 (25% 权重) =====

	var tokenScore float64 = 100.0 // 默认满分

	// Prompt Token 验证（简化：只检查是否在合理范围内）
	// 真实场景：需要针对具体 prompt 内容对比
	if observed.PromptTokens > 0 && baseline.SampleCount > 0 {
		// 这里简化处理，实际需要建立 prompt → token 映射表
		// 暂时给满分，待 P2 实施 prompt 签名表
	}

	// Output Density 偏差
	if observed.OutputDensity > 0 && baseline.OutputDensityMean > 0 {
		deviation := math.Abs(observed.OutputDensity-baseline.OutputDensityMean) / baseline.OutputDensityMean

		if deviation > 0.30 {
			tokenScore = 100 - (deviation-0.30)*100
			if tokenScore < 0 {
				tokenScore = 0
			}
		}

		verdict.Evidence["output_density_deviation"] = deviation
	}

	verdict.TokenScore = tokenScore

	// ===== 3. 内容特征匹配度 (15% 权重) =====

	var contentScore float64 = 100.0 // 默认满分

	// 格式风格对比
	if baseline.FormatFlags > 0 {
		// 计算位标重叠度
		overlap := countBits(observed.FormatFlags & baseline.FormatFlags)
		total := countBits(baseline.FormatFlags)
		if total > 0 {
			contentScore = float64(overlap) / float64(total) * 100
		}
	}

	verdict.ContentScore = contentScore

	// ===== 4. 综合打分 =====

	verdict.OverallScore = verdict.TimingScore*0.6 + verdict.TokenScore*0.25 + verdict.ContentScore*0.15

	// 置信度取决于基线样本量
	verdict.Confidence = math.Min(1.0, float64(baseline.SampleCount)/100.0)

	return verdict
}

// bucketizeITTs 将 ITT 序列桶化为分布
//
// 桶划分：0-10ms, 10-20ms, 20-30ms, ..., 90-100ms, 100+ms
func bucketizeITTs(itts []time.Duration) map[string]float64 {
	buckets := make(map[int]int) // ms bucket -> count

	for _, itt := range itts {
		ms := int(itt.Milliseconds())
		bucket := ms / 10 // 0-9ms → 0, 10-19ms → 1, ...
		if bucket > 10 {
			bucket = 10 // 100+ms → bucket 10
		}
		buckets[bucket]++
	}

	// 转换为概率分布
	total := len(itts)
	dist := make(map[string]float64)
	for bucket, count := range buckets {
		var key string
		if bucket < 10 {
			// "0", "10", "20", ..., "90"
			key = formatBucket(bucket)
		} else {
			key = "100+"
		}
		dist[key] = float64(count) / float64(total)
	}

	return dist
}

// formatBucket 格式化桶标签
func formatBucket(bucket int) string {
	// bucket 0 → "0", bucket 1 → "10", bucket 2 → "20", ...
	ms := bucket * 10
	if ms < 10 {
		return "0"
	}
	if ms < 100 {
		return string([]byte{byte('0' + ms/10), byte('0')})
	}
	return "100+"
}

// countBits 计算位标中 1 的个数
func countBits(flags uint32) int {
	count := 0
	for flags > 0 {
		count += int(flags & 1)
		flags >>= 1
	}
	return count
}

// ===== 异常常量 =====

const (
	// AnomalyPassiveNoData 被动数据缺失
	AnomalyPassiveNoData = "passive_no_data"

	// AnomalyPassiveITTMismatch ITT 分布不匹配
	AnomalyPassiveITTMismatch = "passive_itt_mismatch"

	// AnomalyPassiveTTFTAnomaly TTFT 异常
	AnomalyPassiveTTFTAnomaly = "passive_ttft_anomaly"

	// AnomalyPassiveThroughputAnomaly 吞吐量异常
	AnomalyPassiveThroughputAnomaly = "passive_throughput_anomaly"

	// AnomalyPassiveTimingMismatch 时序特征整体不匹配
	AnomalyPassiveTimingMismatch = "passive_timing_mismatch"
)
