// verdict.go — 判定引擎：把四把尺的结果合成一个可信度分与一个结论。
//
// verdict.go — the verdict engine: combine the four independent measurements
// into one credibility score and one verdict.
//
// ★ 三条判定纪律（贯穿本文件）：
//
//  1. **缺失维度不计入**。某维度没测到，就不进加权平均，也不按 0 分算。
//     四个维度都没测到 ⇒ 综合分 0、结论 unknown —— 而不是「综合分 0 ⇒
//     一塌糊涂」。这两种「0」含义完全不同，混起来会误伤。
//  2. **阴性结论要当缺陷一样复核**。测不出来就写 unknown，不写 verified。
//  3. **一个维度不够定性**。verified 要求至少两个维度都有数据且全部达标；
//     只有一个维度（例如上游自报模型名对得上）不足以说「没问题」——
//     因为中转改写 model 字段是常态，那条证据的信息量很低。
package modelcredibility

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Config 是判定参数。
type Config struct {
	PeerParity    PeerParityConfig
	Calibration   CalibrationConfig
	// SignatureDriftTolerance 是 prompt token 计数漂移的噪声容差。
	// ≤ 该值算稳定；超过则按漂移幅度扣分。
	SignatureDriftTolerance int
	// SignatureDriftSevere 是「严重漂移」下限（后端确定被换）。
	SignatureDriftSevere int
	// VerifiedMinScore 是判 verified 所需的最低综合分。
	VerifiedMinScore float64
	// VerifiedMinDimensions 是判 verified 所需的最少已测量维度数。
	VerifiedMinDimensions int
	// DilutedCapabilityScore 是能力维度判「渗水」的分数上限。
	DilutedCapabilityScore float64
	// SuspectCapabilityScore 是能力维度判「存疑」的分数上限。
	SuspectCapabilityScore float64
	// DilutedIdentityScore 是身份维度判「渗水」的分数上限（跨族换模型）。
	DilutedIdentityScore float64
	// SuspectIdentityScore 是身份维度判「存疑」的分数上限（同族换模型）。
	SuspectIdentityScore float64
	// DilutedConsistencyScore 是一致性维度判「渗水」的分数上限。
	DilutedConsistencyScore float64
	// SuspectConsistencyScore 是一致性维度判「存疑」的分数上限。
	SuspectConsistencyScore float64
	// SuspectReferenceScore 是参考基线维度判「存疑」的分数上限。
	SuspectReferenceScore float64
}

// DefaultConfig 返回默认判定参数。
//
// ★ 身份维度的两档门槛为什么差这么多：
//   - 跨族（D=10）⇒ 直接判渗水：向 GPT 供应商要 Claude 却拿到 GPT 家族
//     的回答，这不是"可能"，这是换模型。
//   - 同族不同名（D=50）⇒ 只判存疑：同族里换档位（Sonnet→Haiku）是渗水的
//     典型形态，但同族里也存在纯命名差异（"claude-sonnet-4-5" 与
//     "claude-3-5-sonnet"），光看名字定不了性 ⇒ 必须与能力维度合看。
func DefaultConfig() Config {
	return Config{
		PeerParity:               DefaultPeerParityConfig(),
		Calibration:              DefaultCalibrationConfig(),
		SignatureDriftTolerance:  2,
		SignatureDriftSevere:     15,
		VerifiedMinScore:         85,
		VerifiedMinDimensions:    2,
		DilutedCapabilityScore:   50,
		SuspectCapabilityScore:   85,
		DilutedIdentityScore:     30,
		SuspectIdentityScore:     70,
		DilutedConsistencyScore:  30,
		SuspectConsistencyScore:  70,
		SuspectReferenceScore:    70,
	}
}

// Previous 是上一次测得的签名基线，用于一致性维度。
type Previous struct {
	PromptTokens      int
	HasPromptTokens   bool
	SystemFingerprint string
	ReportedModel     string
	MeasuredAt        time.Time
}

// Input 是判定的全部输入。
type Input struct {
	Target   NodeTarget
	Observed Observed
	// ProbeErr 非空表示这次探测失败（传输错误 / 非 200）。
	ProbeErr error

	// Accuracy 是本节点最近一次**成功**实测的准确率（0-100）。
	// HasAccuracy=false 表示没有可用实测（没测过 / 只测崩过）。
	Accuracy    float64
	HasAccuracy bool

	// Peer 是同款 canonical 模型的同伴实测分布。
	Peer PeerReference

	// CanonicalName 是同款 canonical 模型的名字（展示用）。
	CanonicalName string

	// StandardIQ 是 models_canonical.standard_iq（0 表示无外部基线）。
	StandardIQ float64

	// Calibration 是平台级校准。
	Calibration Calibration

	// Previous 是上一次的本节点签名基线（nil = 首次测量）。
	Previous *Previous

	// Now 是判定时刻（零值取当前时间）。
	Now time.Time
}

// Evaluate 执行判定。
func Evaluate(in Input, cfg Config) Result {
	now := in.Now
	if now.IsZero() {
		now = nowFunc()
	}
	res := Result{
		TenantID:       "default",
		ProviderID:     in.Target.ProviderID,
		CredentialID:   in.Target.CredentialID,
		RawModelName:   in.Target.RawModelName,
		CanonicalID:    in.Target.CanonicalID,
		CanonicalName:  in.CanonicalName,
		RequestedModel: in.Target.outbound(),
		Observed:       in.Observed,
		MeasuredAt:     now,
		Status:         StatusSuccess,
		Evidence:       map[string]any{},
	}
	anomalies := map[string]struct{}{}
	addAnomaly := func(a string) { anomalies[a] = struct{}{} }

	probeOK := in.ProbeErr == nil && in.Observed.HTTPStatus == 200
	switch {
	case in.ProbeErr != nil:
		res.Status = StatusFailed
		addAnomaly(AnomalyProbeFailed)
	case !probeOK:
		res.Status = StatusFailed
		addAnomaly(AnomalyProbeFailed)
	case in.Observed.Content == "":
		// 空回答：可能是推理模型被预算烧穿、也可能是拒绝。
		// ★ 不把它当成"探测成功"——但 usage / model 字段仍然可用，
		// 所以身份与签名维度照常判定，只是状态标成 partial。
		res.Status = StatusPartial
		addAnomaly(AnomalyProbeEmpty)
	}

	// ── 维度 1：身份自洽 ───────────────────────────────────────────────
	identity, identityMeasured := scoreIdentity(in, addAnomaly)
	if identityMeasured {
		res.Scores.Identity = identity
		res.Scores.Measured = res.Scores.Measured.Mark(DimIdentity)
	}

	// ── 维度 2：能力水位 ───────────────────────────────────────────────
	capability, capabilityMeasured := scoreCapability(in, cfg, addAnomaly)
	if capabilityMeasured {
		res.Scores.Capability = capability
		res.Scores.Measured = res.Scores.Measured.Mark(DimCapability)
	}

	// ── 维度 3：指纹一致性 ─────────────────────────────────────────────
	consistency, consistencyMeasured := scoreConsistency(in, cfg, addAnomaly)
	if consistencyMeasured {
		res.Scores.Consistency = consistency
		res.Scores.Measured = res.Scores.Measured.Mark(DimConsistency)
	}

	// ── 维度 4：参考基线 ───────────────────────────────────────────────
	reference, referenceMeasured := scoreReference(in, cfg, addAnomaly)
	if referenceMeasured {
		res.Scores.Reference = reference
		res.Scores.Measured = res.Scores.Measured.Mark(DimReference)
	}

	res.Overall = ComputeOverall(res.Scores)
	res.Verdict = decideVerdict(res.Scores, res.Overall, cfg)

	for a := range anomalies {
		res.Anomalies = append(res.Anomalies, a)
	}
	sort.Strings(res.Anomalies)

	res.Evidence["measured_dimensions"] = res.Scores.Measured.DimensionCount()
	res.Evidence["dimension_scores"] = map[string]float64{
		"identity":    res.Scores.Identity,
		"capability":  res.Scores.Capability,
		"consistency": res.Scores.Consistency,
		"reference":   res.Scores.Reference,
	}
	return res
}

// scoreIdentity 判定身份自洽维度。
//
// 返回 (分数, 是否有数据)。没有数据 = 上游没报模型名 ⇒ 无法自证。
func scoreIdentity(in Input, addAnomaly func(string)) (float64, bool) {
	// ★ 探测失败（非 200 / 传输错误）时，响应体里的 model 字段不可信：
	// 错误 JSON、代理拦截页、中转的回显都可能带一个「看起来对」的模型名。
	// 用它判身份会把一次失败写成「身份自洽 100」。
	if in.ProbeErr != nil || in.Observed.HTTPStatus != 200 {
		return 0, false
	}
	reported := in.Observed.ReportedModel
	if reported == "" {
		addAnomaly(AnomalyIdentityAbsent)
		return 0, false
	}
	requested := in.Target.outbound()
	if requested == "" {
		return 0, false
	}
	reqKey := normalizeModelKey(requested)
	repKey := normalizeModelKey(reported)
	if reqKey == repKey {
		return 100, true
	}
	if sameFamilySameName(reqKey, repKey) {
		// 同族 + 一方是另一方的 token 前缀 ⇒ 命名/日期快照差异，
		// 视为同一个模型（如 "gpt-5" vs "gpt-5-2025-08-07"）。
		addAnomaly(AnomalyIdentityMismatch)
		return 90, true
	}
	if SameFamily(requested, reported) {
		// 同族但名字不同 —— 同族降级（Sonnet→Haiku）的典型形态。
		addAnomaly(AnomalyIdentityMismatch)
		return 50, true
	}
	// 跨族：向 A 厂商要模型却拿到 B 厂商的模型。
	addAnomaly(AnomalyIdentityMismatch)
	addAnomaly(AnomalyFamilyMismatch)
	return 10, true
}

// sameFamilySameName 判断两个归一化名是否是"同族且一方为另一方前缀"。
func sameFamilySameName(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if !SameFamily(a, b) {
		return false
	}
	if strings.HasPrefix(a, b+"-") || strings.HasPrefix(b, a+"-") {
		return true
	}
	return false
}

// scoreCapability 判定能力水位维度。
func scoreCapability(in Input, cfg Config, addAnomaly func(string)) (float64, bool) {
	if !in.HasAccuracy || in.Accuracy <= 0 {
		addAnomaly(AnomalyNoCapabilityData)
		return 0, false
	}
	parity := ComputePeerParity(in.Accuracy, in.Peer, cfg.PeerParity)
	if !parity.Usable {
		if parity.Reason == "insufficient_distinct_providers" ||
			parity.Reason == "no_peer_samples" {
			addAnomaly(AnomalyPeerSampleTooSmall)
		}
		return 0, false
	}
	switch {
	case parity.Ratio > 1.05:
		// 比同伴最好成绩还高 5%：要么同伴测崩了，要么这个节点被针对性训练过。
		addAnomaly(AnomalyCapabilityLead)
		return 100, true
	case parity.Ratio >= cfg.PeerParity.HealthyRatio:
		return 100, true
	case parity.WithinNoiseBand:
		// 差距在噪声带内（绝对百分点 < MinGapPP）⇒ 没有证据指控掺水
		// ⇒ 与同伴持平处理，**不产生异常项**。
		//
		// ★ 这一支与「ratio < HealthyRatio」并存是必要的：比值门限是
		//   相对量、噪声带是绝对量，两个都要满足才算「差得可疑」。
		//   只看比值 ⇒ 小分母上会把 1~2 道题的差别当成掺水；
		//   只看绝对差 ⇒ 大分母上会漏掉比例明显偏低的情况。
		return 100, true
	case parity.Ratio >= cfg.PeerParity.SuspectRatio:
		addAnomaly(AnomalyCapabilityGap)
		return interpolate(parity.Ratio, cfg.PeerParity.SuspectRatio, cfg.PeerParity.HealthyRatio, 60, 95), true
	default:
		addAnomaly(AnomalyCapabilitySevereGap)
		return interpolate(parity.Ratio, 0, cfg.PeerParity.SuspectRatio, 0, 50), true
	}
}

// scoreConsistency 判定指纹一致性维度。
//
// ★ 强信号是 prompt token 计数漂移（换分词器 ⇒ 换后端）；
// system_fingerprint 漂移是弱信号（上游发版也会变）⇒ 权重低。
func scoreConsistency(in Input, cfg Config, addAnomaly func(string)) (float64, bool) {
	prev := in.Previous
	if prev == nil {
		return 0, false
	}
	var weighted, totalWeight float64
	// prompt token 计数（权重 0.7）。探测失败时 usage 不可信，不比。
	if prev.HasPromptTokens && in.Observed.HasUsage && in.ProbeErr == nil && in.Observed.HTTPStatus == 200 {
		delta := in.Observed.PromptTokens - prev.PromptTokens
		if delta < 0 {
			delta = -delta
		}
		var s float64
		switch {
		case delta <= cfg.SignatureDriftTolerance:
			s = 100
		case delta >= cfg.SignatureDriftSevere:
			s = 0
			addAnomaly(AnomalySignatureDrift)
		default:
			s = 50
			addAnomaly(AnomalySignatureDrift)
		}
		weighted += s * 0.7
		totalWeight += 0.7
	}
	// system_fingerprint（权重 0.3）。失败响应的头不是模型指纹。
	if in.ProbeErr == nil && in.Observed.HTTPStatus == 200 &&
		(prev.SystemFingerprint != "" || in.Observed.SystemFingerprint != "") {
		var s float64
		if prev.SystemFingerprint == in.Observed.SystemFingerprint {
			s = 100
		} else {
			s = 50
			addAnomaly(AnomalyFingerprintDrift)
		}
		weighted += s * 0.3
		totalWeight += 0.3
	}
	if totalWeight == 0 {
		return 0, false
	}
	return weighted / totalWeight, true
}

// scoreReference 判定参考基线维度。
func scoreReference(in Input, cfg Config, addAnomaly func(string)) (float64, bool) {
	if !in.HasAccuracy || in.Accuracy <= 0 {
		return 0, false
	}
	ref := ComputeReferenceParity(in.Accuracy, in.StandardIQ, in.Calibration, cfg.Calibration)
	if !ref.Usable {
		return 0, false
	}
	switch {
	case ref.Ratio >= ReferenceHealthyRatio:
		return 100, true
	case ref.Ratio >= ReferenceSuspectRatio:
		addAnomaly(AnomalyReferenceGap)
		return interpolate(ref.Ratio, ReferenceSuspectRatio, ReferenceHealthyRatio, 55, 95), true
	default:
		addAnomaly(AnomalyReferenceGap)
		return interpolate(ref.Ratio, 0, ReferenceSuspectRatio, 0, 45), true
	}
}

// ComputeOverall 按**有数据的维度**做加权平均（缺失维度不计入，权重重新归一）。
//
// ★ 没有任何一个维度有数据时返回 0。落库方必须用 Measured.DimensionCount()
// 把这个 0 写成 SQL NULL —— 0 与「没测过」在数值上无法区分，写成 0 会让
// 列表按分数排序时把「没测」排到「极差」前面。
func ComputeOverall(s SubScores) float64 {
	var weighted, totalWeight float64
	for _, d := range []Dimension{DimIdentity, DimCapability, DimConsistency, DimReference} {
		if !s.Measured.Has(d) {
			continue
		}
		w := SubScoreWeights[d]
		weighted += s.ScoreOf(d) * w
		totalWeight += w
	}
	if totalWeight == 0 {
		return 0
	}
	return weighted / totalWeight
}

// decideVerdict 按固定次序定结论。
//
// 判定次序（先重后轻，命中即返回）：
//  1. 一个维度都没测到 ⇒ unknown。
//  2. 能力 < 50 ⇒ 渗水（能力显著不足是最硬的渗水证据）。
//  3. 身份 < 30 ⇒ 渗水（跨族换模型）。
//  4. 一致性 < 30 ⇒ 渗水（后端被换）。
//  5. 任一维度 < 各自存疑线 ⇒ 存疑。
//  6. 已测量维度 ≥ 2 且全部达标且综合分达标 ⇒ 已验证。
//  7. 只测到 1 个维度 ⇒ unknown（证据不足，见纪律三）。
//  8. 其余 ⇒ 存疑。
func decideVerdict(s SubScores, overall float64, cfg Config) Verdict {
	n := s.Measured.DimensionCount()
	if n == 0 {
		return VerdictUnknown
	}
	if s.Measured.Has(DimCapability) && s.Capability < cfg.DilutedCapabilityScore {
		return VerdictDiluted
	}
	if s.Measured.Has(DimIdentity) && s.Identity < cfg.DilutedIdentityScore {
		return VerdictDiluted
	}
	if s.Measured.Has(DimConsistency) && s.Consistency < cfg.DilutedConsistencyScore {
		return VerdictDiluted
	}
	if s.Measured.Has(DimCapability) && s.Capability < cfg.SuspectCapabilityScore {
		return VerdictSuspected
	}
	if s.Measured.Has(DimIdentity) && s.Identity < cfg.SuspectIdentityScore {
		return VerdictSuspected
	}
	if s.Measured.Has(DimConsistency) && s.Consistency < cfg.SuspectConsistencyScore {
		return VerdictSuspected
	}
	if s.Measured.Has(DimReference) && s.Reference < cfg.SuspectReferenceScore {
		return VerdictSuspected
	}
	if n >= cfg.VerifiedMinDimensions && overall >= cfg.VerifiedMinScore {
		return VerdictVerified
	}
	return VerdictUnknown
}

// interpolate 在 [lo, hi] 区间上把 ratio 线性映射到 [outLo, outHi]。
func interpolate(ratio, lo, hi, outLo, outHi float64) float64 {
	if hi <= lo {
		return outLo
	}
	r := (ratio - lo) / (hi - lo)
	if r < 0 {
		r = 0
	}
	if r > 1 {
		r = 1
	}
	return outLo + r*(outHi-outLo)
}

// DescribeVerdict 给结论一句中文说明（CLI / 日志用；UI 走语言包）。
func DescribeVerdict(v Verdict) string {
	switch v {
	case VerdictVerified:
		return "身份与能力均与声称一致"
	case VerdictSuspected:
		return "存在异常信号，证据不足以定性"
	case VerdictDiluted:
		return "能力显著低于同款模型最好水平，或身份自证换族"
	case VerdictUnknown:
		return "证据不足，未能判定"
	default:
		return fmt.Sprintf("未知结论 %q", string(v))
	}
}

// round2 保留两位小数（写库前统一处理，避免各调用点各写一遍）。
func round2(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*100) / 100
}
