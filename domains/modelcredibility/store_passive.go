// store_passive.go — 被动指纹基线（migration 955）的读写。
//
// store_passive.go — persistence for the passive fingerprint baseline
// (migration 955, passive_model_fingerprints).
//
// ★ jsonb 绑定纪律（bfbbb5618 教训，仓内既有约定）：jsonb 列一律以
// **string** 传入（json.Marshal 后的字符串），裸 []byte 会被
// SimpleProtocol 内联成 bytea hex 落库（22P02 必炸）。
package modelcredibility

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// PassiveBaselineSink 是被动基线的读写面（Collector 依赖本接口，测试用
// fake 替身；*Store 是生产实现）。
type PassiveBaselineSink interface {
	// UpsertPassiveBaseline 按 (tenant, credential, model) 键写入/覆盖基线行。
	UpsertPassiveBaseline(ctx context.Context, fp *PassiveFingerprint) error
	// LoadPassiveBaseline 读取基线；没有行时返回 (nil, nil)。
	LoadPassiveBaseline(ctx context.Context, credentialID int64, modelName string) (*PassiveFingerprint, error)
}

// UpsertPassiveBaseline 实现 PassiveBaselineSink（生产实现）。
func (s *Store) UpsertPassiveBaseline(ctx context.Context, fp *PassiveFingerprint) error {
	if fp == nil {
		return fmt.Errorf("passive baseline: nil fingerprint")
	}
	if strings.TrimSpace(fp.ModelID) == "" {
		return fmt.Errorf("passive baseline: empty model id")
	}
	ittJSON, err := json.Marshal(fp.ITTDistribution)
	if err != nil {
		return fmt.Errorf("passive baseline: marshal itt distribution: %w", err)
	}
	var sigJSON any
	if len(fp.PromptTokenSignature) > 0 {
		b, err := json.Marshal(fp.PromptTokenSignature)
		if err != nil {
			return fmt.Errorf("passive baseline: marshal prompt token signature: %w", err)
		}
		sigJSON = string(b)
	}
	var vocabJSON any
	if len(fp.VocabFreq) > 0 {
		b, err := json.Marshal(fp.VocabFreq)
		if err != nil {
			return fmt.Errorf("passive baseline: marshal vocab freq: %w", err)
		}
		vocabJSON = string(b)
	}

	const q = `
INSERT INTO public.passive_model_fingerprints (
    tenant_id, credential_id, model_name,
    ttft_mean_ms, ttft_p95_ms,
    itt_mean_ms, itt_std_ms, itt_p50_ms, itt_p95_ms, itt_distribution,
    throughput_mean, throughput_std,
    prompt_token_signature, output_density_mean, output_density_std,
    vocab_freq, format_flags,
    sample_count, collected_from, collected_to, updated_at
) VALUES (
    $1, $2, $3,
    $4, $5,
    $6, $7, $8, $9, $10::jsonb,
    $11, $12,
    $13::jsonb, $14, $15,
    $16::jsonb, $17,
    $18, $19, $20, now()
)
ON CONFLICT (tenant_id, credential_id, model_name) DO UPDATE SET
    ttft_mean_ms           = EXCLUDED.ttft_mean_ms,
    ttft_p95_ms            = EXCLUDED.ttft_p95_ms,
    itt_mean_ms            = EXCLUDED.itt_mean_ms,
    itt_std_ms             = EXCLUDED.itt_std_ms,
    itt_p50_ms             = EXCLUDED.itt_p50_ms,
    itt_p95_ms             = EXCLUDED.itt_p95_ms,
    itt_distribution       = EXCLUDED.itt_distribution,
    throughput_mean        = EXCLUDED.throughput_mean,
    throughput_std         = EXCLUDED.throughput_std,
    prompt_token_signature = EXCLUDED.prompt_token_signature,
    output_density_mean    = EXCLUDED.output_density_mean,
    output_density_std     = EXCLUDED.output_density_std,
    vocab_freq             = EXCLUDED.vocab_freq,
    format_flags           = EXCLUDED.format_flags,
    sample_count           = EXCLUDED.sample_count,
    collected_from         = EXCLUDED.collected_from,
    collected_to           = EXCLUDED.collected_to,
    updated_at             = now()`

	tenant := fp.TenantID
	if tenant == "" {
		tenant = "default"
	}
	_, err = s.db.Exec(ctx, q,
		tenant, fp.CredentialID, fp.ModelID,
		msOrNil(fp.TTFTMean), msOrNil(fp.TTFTP95),
		msOrNil(fp.ITTMean), msOrNil(fp.ITTStd), msOrNil(fp.ITTP50), msOrNil(fp.ITTP95),
		string(ittJSON),
		floatOrNil(fp.ThroughputMean), floatOrNil(fp.ThroughputStd),
		sigJSON, floatOrNil(fp.OutputDensityMean), floatOrNil(fp.OutputDensityStd),
		vocabJSON, int(fp.FormatFlags),
		fp.SampleCount, timeOrNil(fp.CollectedFrom), timeOrNil(fp.CollectedTo),
	)
	if err != nil {
		return fmt.Errorf("passive baseline: upsert: %w", err)
	}
	return nil
}

// LoadPassiveBaseline 实现 PassiveBaselineSink；没有行时返回 (nil, nil)。
func (s *Store) LoadPassiveBaseline(ctx context.Context, credentialID int64, modelName string) (*PassiveFingerprint, error) {
	const q = `
SELECT
    tenant_id,
    ttft_mean_ms, ttft_p95_ms,
    itt_mean_ms, itt_std_ms, itt_p50_ms, itt_p95_ms, itt_distribution,
    throughput_mean, throughput_std,
    prompt_token_signature, output_density_mean, output_density_std,
    vocab_freq, format_flags,
    sample_count, collected_from, collected_to
FROM public.passive_model_fingerprints
WHERE tenant_id = 'default' AND credential_id = $1 AND model_name = $2`

	var (
		tenant      string
		ttftMeanMS  *float64
		ttftP95MS   *float64
		ittMeanMS   *float64
		ittStdMS    *float64
		ittP50MS    *float64
		ittP95MS    *float64
		ittDistRaw  *string
		tpMean      *float64
		tpStd       *float64
		sigRaw      *string
		densityMean *float64
		densityStd  *float64
		vocabRaw    *string
		formatFlags int
		sampleCount int
		colFrom     *time.Time
		colTo       *time.Time
	)
	err := s.db.QueryRow(ctx, q, credentialID, modelName).Scan(
		&tenant,
		&ttftMeanMS, &ttftP95MS,
		&ittMeanMS, &ittStdMS, &ittP50MS, &ittP95MS, &ittDistRaw,
		&tpMean, &tpStd,
		&sigRaw, &densityMean, &densityStd,
		&vocabRaw, &formatFlags,
		&sampleCount, &colFrom, &colTo,
	)
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("passive baseline: load: %w", err)
	}

	fp := &PassiveFingerprint{
		ModelID:      modelName,
		CredentialID: credentialID,
		SampleCount:  sampleCount,
		FormatFlags:  uint32(formatFlags),
	}
	if tenant != "" {
		fp.TenantID = tenant
	}
	if colFrom != nil {
		fp.CollectedFrom = *colFrom
	}
	if colTo != nil {
		fp.CollectedTo = *colTo
	}
	fp.TTFTMean = msToDuration(ttftMeanMS)
	fp.TTFTP95 = msToDuration(ttftP95MS)
	fp.ITTMean = msToDuration(ittMeanMS)
	fp.ITTStd = msToDuration(ittStdMS)
	fp.ITTP50 = msToDuration(ittP50MS)
	fp.ITTP95 = msToDuration(ittP95MS)
	fp.ThroughputMean = deref(tpMean)
	fp.ThroughputStd = deref(tpStd)
	fp.OutputDensityMean = deref(densityMean)
	fp.OutputDensityStd = deref(densityStd)

	if ittDistRaw != nil && *ittDistRaw != "" {
		dist := map[string]float64{}
		if err := json.Unmarshal([]byte(*ittDistRaw), &dist); err != nil {
			return nil, fmt.Errorf("passive baseline: decode itt distribution: %w", err)
		}
		fp.ITTDistribution = dist
	}
	if sigRaw != nil && *sigRaw != "" {
		sig := map[string]int{}
		if err := json.Unmarshal([]byte(*sigRaw), &sig); err != nil {
			return nil, fmt.Errorf("passive baseline: decode prompt token signature: %w", err)
		}
		fp.PromptTokenSignature = sig
	}
	if vocabRaw != nil && *vocabRaw != "" {
		vocab := map[string]float64{}
		if err := json.Unmarshal([]byte(*vocabRaw), &vocab); err != nil {
			return nil, fmt.Errorf("passive baseline: decode vocab freq: %w", err)
		}
		fp.VocabFreq = vocab
	}
	return fp, nil
}

// msOrNil / msToDuration / floatOrNil / deref / timeOrNil —— 毫秒 real 列与
// Go time.Duration 的边界换算（955 迁移口径：表里存毫秒，不存 Duration）。
// isNoRows 复用 util.go 的既有判据。

func msOrNil(d time.Duration) any {
	if d <= 0 {
		return nil
	}
	return float64(d.Microseconds()) / 1000.0
}

func msToDuration(ms *float64) time.Duration {
	if ms == nil || *ms <= 0 {
		return 0
	}
	return time.Duration(*ms * float64(time.Millisecond))
}

func floatOrNil(f float64) any {
	if f == 0 {
		return nil
	}
	return f
}

func deref(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

func timeOrNil(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
