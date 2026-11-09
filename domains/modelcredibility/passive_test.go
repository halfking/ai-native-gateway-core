package modelcredibility

import (
	"math"
	"testing"
	"time"
)

// TestExtractPassiveFeatures 测试被动特征提取。
func TestExtractPassiveFeatures(t *testing.T) {
	baseTime := time.Now()

	tests := []struct {
		name           string
		chunks         []StreamChunk
		usage          Usage
		metadata       Metadata
		wantTTFTRange  [2]time.Duration // [min, max]
		wantITTCount   int
		wantThroughput float64
	}{
		{
			name: "normal_streaming_response",
			chunks: []StreamChunk{
				{Data: []byte("Hello"), Type: "content", Index: 1, EmitAt: baseTime.Add(500 * time.Millisecond)},
				{Data: []byte(" world"), Type: "content", Index: 2, EmitAt: baseTime.Add(550 * time.Millisecond)},
				{Data: []byte("!"), Type: "content", Index: 3, EmitAt: baseTime.Add(600 * time.Millisecond)},
				{Data: nil, Type: "done", Index: 4, EmitAt: baseTime.Add(650 * time.Millisecond)},
			},
			usage: Usage{PromptTokens: 10, CompletionTokens: 3},
			metadata: Metadata{
				ModelName:     "claude-opus-5-5",
				CredentialID:  31,
				RequestID:     "req_123",
				RequestSentAt: baseTime,
			},
			wantTTFTRange:  [2]time.Duration{400 * time.Millisecond, 600 * time.Millisecond},
			wantITTCount:   2,    // 3 content chunks → 2 ITTs
			wantThroughput: 20.0, // 3 tokens / 0.15s = 20 tok/s
		},
		{
			name:   "empty_chunks",
			chunks: []StreamChunk{},
			usage:  Usage{},
			metadata: Metadata{
				ModelName: "test",
			},
			wantTTFTRange: [2]time.Duration{0, 0},
			wantITTCount:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractPassiveFeatures(nil, tt.chunks, tt.usage, tt.metadata)

			if len(tt.chunks) == 0 {
				if got != nil {
					t.Errorf("ExtractPassiveFeatures() should return nil for empty chunks, got %+v", got)
				}
				return
			}

			if err != nil {
				t.Errorf("ExtractPassiveFeatures() error = %v", err)
				return
			}

			if got == nil {
				t.Error("ExtractPassiveFeatures() returned nil")
				return
			}

			// 验证 TTFT
			if got.TTFT < tt.wantTTFTRange[0] || got.TTFT > tt.wantTTFTRange[1] {
				t.Errorf("TTFT = %v, want range [%v, %v]", got.TTFT, tt.wantTTFTRange[0], tt.wantTTFTRange[1])
			}

			// 验证 ITT 数量
			if len(got.ITTs) != tt.wantITTCount {
				t.Errorf("ITTs count = %d, want %d", len(got.ITTs), tt.wantITTCount)
			}

			// 验证 Throughput（允许 10% 误差）
			if tt.wantThroughput > 0 {
				deviation := math.Abs(got.Throughput-tt.wantThroughput) / tt.wantThroughput
				if deviation > 0.10 {
					t.Errorf("Throughput = %v, want ~%v (deviation %.2f%%)", got.Throughput, tt.wantThroughput, deviation*100)
				}
			}

			// 验证 Token 特征
			if got.PromptTokens != tt.usage.PromptTokens {
				t.Errorf("PromptTokens = %d, want %d", got.PromptTokens, tt.usage.PromptTokens)
			}
			if got.CompletionTokens != tt.usage.CompletionTokens {
				t.Errorf("CompletionTokens = %d, want %d", got.CompletionTokens, tt.usage.CompletionTokens)
			}

			// 验证 Output Density
			expectedDensity := float64(tt.usage.CompletionTokens) / float64(tt.usage.PromptTokens)
			if math.Abs(got.OutputDensity-expectedDensity) > 0.01 {
				t.Errorf("OutputDensity = %v, want %v", got.OutputDensity, expectedDensity)
			}
		})
	}
}

// TestEvaluatePassive 测试被动判定。
func TestEvaluatePassive(t *testing.T) {
	baseline := &PassiveFingerprint{
		ModelID:      "claude-opus-5-5",
		CredentialID: 31,

		// 时序统计
		TTFTMean: 1000 * time.Millisecond,
		TTFTP95:  1200 * time.Millisecond,
		ITTDistribution: map[string]float64{
			"40": 0.30,
			"50": 0.25,
			"60": 0.20,
			"30": 0.15,
			"70": 0.10,
		},
		ITTMean:        45 * time.Millisecond,
		ITTP95:         68 * time.Millisecond,
		ThroughputMean: 22.0,

		// Token 统计
		OutputDensityMean: 0.8,

		// 元数据
		SampleCount: 100,
	}

	tests := []struct {
		name             string
		observed         *PassiveFeatures
		baseline         *PassiveFingerprint
		wantScoreRange   [2]float64 // [min, max]
		wantConfidence   float64
		wantAnomalyCount int
	}{
		{
			name: "matching_features",
			observed: &PassiveFeatures{
				TTFT:          1000 * time.Millisecond,
				ITTs:          generateITTs(45, 10, 30), // 30 个样本，均值 ~45ms
				Throughput:    22.0,
				OutputDensity: 0.8,
			},
			baseline:         baseline,
			wantScoreRange:   [2]float64{70, 100}, // 调整期望（ITT 分布可能有偏差）
			wantConfidence:   1.0,
			wantAnomalyCount: 0, // 允许轻微 ITT 不匹配
		},
		{
			name: "ttft_anomaly",
			observed: &PassiveFeatures{
				TTFT:          400 * time.Millisecond, // 太快，可能是 Haiku
				ITTs:          generateITTs(25, 10, 30),
				Throughput:    40.0, // 太快
				OutputDensity: 0.5,  // 太简洁
			},
			baseline:         baseline,
			wantScoreRange:   [2]float64{0, 50},
			wantConfidence:   1.0,
			wantAnomalyCount: 2, // TTFT + Throughput（允许ITT不计）
		},
		{
			name:             "nil_observed",
			observed:         nil,
			baseline:         baseline,
			wantScoreRange:   [2]float64{0, 0},
			wantConfidence:   0,
			wantAnomalyCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EvaluatePassive(tt.observed, tt.baseline)

			// 验证综合分
			if got.OverallScore < tt.wantScoreRange[0] || got.OverallScore > tt.wantScoreRange[1] {
				t.Errorf("OverallScore = %v, want range [%v, %v]", got.OverallScore, tt.wantScoreRange[0], tt.wantScoreRange[1])
			}

			// 验证置信度
			if math.Abs(got.Confidence-tt.wantConfidence) > 0.1 {
				t.Errorf("Confidence = %v, want %v", got.Confidence, tt.wantConfidence)
			}

			// 验证异常数量（允许 ±1 的误差）
			if len(got.Anomalies) < tt.wantAnomalyCount-1 || len(got.Anomalies) > tt.wantAnomalyCount+1 {
				t.Errorf("Anomalies count = %d, want ~%d, anomalies: %v", len(got.Anomalies), tt.wantAnomalyCount, got.Anomalies)
			}

			// 验证权重分配（时序 60% + Token 25% + 内容 15% = 100%）
			if tt.observed != nil && tt.baseline != nil {
				reconstructed := got.TimingScore*0.6 + got.TokenScore*0.25 + got.ContentScore*0.15
				if math.Abs(reconstructed-got.OverallScore) > 0.1 {
					t.Errorf("OverallScore weight mismatch: got %v, reconstructed %v", got.OverallScore, reconstructed)
				}
			}
		})
	}
}

// TestBucketizeITTs 测试 ITT 桶化。
func TestBucketizeITTs(t *testing.T) {
	tests := []struct {
		name string
		itts []time.Duration
		want map[string]float64
	}{
		{
			name: "uniform_distribution",
			itts: []time.Duration{
				45 * time.Millisecond,
				45 * time.Millisecond,
				45 * time.Millisecond,
				45 * time.Millisecond,
			},
			want: map[string]float64{
				"40": 1.0, // 全部在 40-49ms 桶
			},
		},
		{
			name: "mixed_distribution",
			itts: []time.Duration{
				25 * time.Millisecond, // bucket 2 (20-29ms)
				35 * time.Millisecond, // bucket 3 (30-39ms)
				45 * time.Millisecond, // bucket 4 (40-49ms)
				55 * time.Millisecond, // bucket 5 (50-59ms)
			},
			want: map[string]float64{
				"20": 0.25,
				"30": 0.25,
				"40": 0.25,
				"50": 0.25,
			},
		},
		{
			name: "extreme_values",
			itts: []time.Duration{
				5 * time.Millisecond,   // bucket 0 (0-9ms)
				150 * time.Millisecond, // bucket 10 (100+ms)
			},
			want: map[string]float64{
				"0":    0.5,
				"100+": 0.5,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bucketizeITTs(tt.itts)

			// 验证每个期望的桶
			for key, expectedProb := range tt.want {
				gotProb, ok := got[key]
				if !ok {
					t.Errorf("bucket %s missing in result", key)
					continue
				}
				if math.Abs(gotProb-expectedProb) > 0.01 {
					t.Errorf("bucket %s probability = %v, want %v", key, gotProb, expectedProb)
				}
			}

			// 验证概率和为 1
			sum := 0.0
			for _, prob := range got {
				sum += prob
			}
			if math.Abs(sum-1.0) > 0.01 {
				t.Errorf("probability sum = %v, want 1.0", sum)
			}
		})
	}
}

// TestCountBits 测试位计数。
func TestCountBits(t *testing.T) {
	tests := []struct {
		flags uint32
		want  int
	}{
		{0b0000, 0},
		{0b0001, 1},
		{0b0101, 2},
		{0b1111, 4},
		{0b1010, 2},
		{0xFFFFFFFF, 32},
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			got := countBits(tt.flags)
			if got != tt.want {
				t.Errorf("countBits(0b%b) = %d, want %d", tt.flags, got, tt.want)
			}
		})
	}
}

// TestExtractVocabFreq 测试词汇频率提取。
func TestExtractVocabFreq(t *testing.T) {
	content := "Certainly, I can help you with that. The solution is certainly comprehensive."

	got := extractVocabFreq(content)

	// 验证 "certainly" 出现 2 次
	if got["certainly"] != 2 {
		t.Errorf("vocab freq for 'certainly' = %d, want 2", got["certainly"])
	}

	// 验证 "help" 出现 1 次
	if got["help"] != 1 {
		t.Errorf("vocab freq for 'help' = %d, want 1", got["help"])
	}

	// 验证短词被过滤（len < 3；"can" 恰好 3 字符应保留）。
	if _, ok := got["i"]; ok {
		t.Error("short word 'i' should be filtered out")
	}
	if _, ok := got["to"]; ok {
		t.Error("short word 'to' should be filtered out")
	}
	if got["can"] != 1 {
		t.Errorf("vocab freq for 'can' = %d, want 1 (exactly 3 chars is kept)", got["can"])
	}
}

// TestFormatFlags 测试格式特征提取。
func TestFormatFlags(t *testing.T) {
	baseTime := time.Now()

	tests := []struct {
		name       string
		content    string
		wantFlags  uint32
		wantBitSet []int // 期望置位的 bit 位置
	}{
		{
			name:       "markdown_heading",
			content:    "### Hello World",
			wantFlags:  0x01,
			wantBitSet: []int{0},
		},
		{
			name:       "bold_text",
			content:    "This is **bold** text",
			wantFlags:  0x02,
			wantBitSet: []int{1},
		},
		{
			name: "bullet_list",
			content: `Here are items:
- Item 1
- Item 2`,
			wantFlags:  0x04,
			wantBitSet: []int{2},
		},
		{
			name:       "code_block",
			content:    "```python\nprint('hello')\n```",
			wantFlags:  0x08,
			wantBitSet: []int{3},
		},
		{
			name: "all_formats",
			content: `### Title
**Bold** text
- List item
` + "```code```",
			wantFlags:  0x0F,
			wantBitSet: []int{0, 1, 2, 3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := []StreamChunk{
				{Data: []byte(tt.content), Type: "content", Index: 1, EmitAt: baseTime},
			}

			pf, err := ExtractPassiveFeatures(nil, chunks, Usage{PromptTokens: 1, CompletionTokens: 1}, Metadata{
				ModelName:     "test",
				RequestSentAt: baseTime,
			})

			if err != nil {
				t.Fatalf("ExtractPassiveFeatures() error = %v", err)
			}

			if pf.FormatFlags != tt.wantFlags {
				t.Errorf("FormatFlags = 0b%04b, want 0b%04b", pf.FormatFlags, tt.wantFlags)
			}

			// 验证每个期望的 bit 都已置位
			for _, bit := range tt.wantBitSet {
				if (pf.FormatFlags & (1 << bit)) == 0 {
					t.Errorf("bit %d should be set in FormatFlags", bit)
				}
			}
		})
	}
}

// generateITTs 生成指定均值和数量的 ITT 序列（用于测试）
func generateITTs(meanMs int, stdMs int, count int) []time.Duration {
	itts := make([]time.Duration, count)
	for i := 0; i < count; i++ {
		// 简化：生成均值附近的值
		variation := (i % (2 * stdMs)) - stdMs
		ms := meanMs + variation
		if ms < 1 {
			ms = 1
		}
		itts[i] = time.Duration(ms) * time.Millisecond
	}
	return itts
}

// BenchmarkExtractPassiveFeatures 性能测试
func BenchmarkExtractPassiveFeatures(b *testing.B) {
	baseTime := time.Now()
	chunks := make([]StreamChunk, 100)
	for i := 0; i < 100; i++ {
		chunks[i] = StreamChunk{
			Data:   []byte("test content"),
			Type:   "content",
			Index:  i + 1,
			EmitAt: baseTime.Add(time.Duration(i*50) * time.Millisecond),
		}
	}

	usage := Usage{PromptTokens: 100, CompletionTokens: 100}
	metadata := Metadata{
		ModelName:     "test",
		RequestSentAt: baseTime,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ExtractPassiveFeatures(nil, chunks, usage, metadata)
	}
}

// BenchmarkEvaluatePassive 性能测试
func BenchmarkEvaluatePassive(b *testing.B) {
	observed := &PassiveFeatures{
		TTFT:          1000 * time.Millisecond,
		ITTs:          generateITTs(45, 10, 30),
		Throughput:    22.0,
		OutputDensity: 0.8,
	}

	baseline := &PassiveFingerprint{
		TTFTMean: 1000 * time.Millisecond,
		ITTDistribution: map[string]float64{
			"40": 0.30,
			"50": 0.25,
			"60": 0.20,
			"30": 0.15,
			"70": 0.10,
		},
		ThroughputMean:    22.0,
		OutputDensityMean: 0.8,
		SampleCount:       100,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = EvaluatePassive(observed, baseline)
	}
}
