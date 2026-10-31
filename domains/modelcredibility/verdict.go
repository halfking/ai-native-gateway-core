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
	// VerifiedConsecutiveRuns 是判 verified 所需的**连续达标轮数**（共识门）。
	//
	// ★ 借 bg/model_probe.go 的 RequiredConsensus=3 思路（ccfingerprint 的
	// 确定性重复评分同理）：单轮全绿不能下「已验证」——上游可能恰好在
	// 探测窗口内换了回来，或者中转按探测特征放行。需要连续 K 轮同一结论
	// 才落 verified；未满 K 轮时落 unknown 并在 evidence 里记
	// consensus_pending（「证据不足」语义成立：单轮证据不足以下已验证）。
	// =1 时退化为单轮判定。
	VerifiedConsecutiveRuns int
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
		VerifiedConsecutiveRuns:  2,
	}
}

// Previous 是上一次测得的签名基线，用于一致性维度。
type Previous struct {
	PromptTokens      int
	HasPromptTokens   bool
	SystemFingerprint string
	ReportedModel     string
	// Verdict 是上一次判定的结论（用于 verified 的连续共识）。
	Verdict Verdict
	// VerifiedStreak 是截至上一次为止「连续 verified 建议」的轮数。
	VerifiedStreak int
	MeasuredAt     time.Time
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

	// Passive 是真实流量侧的被动完整性信号（domains/streaming/integrity
	// 落的 model_integrity_events，未解决 + 近 7 天 + high/critical）。
	// nil = 没读到（没有事件，或读取失败降级）。★ 主动探测与被动信号
	// 是同一维度的两条证据通道，融合规则见 Evaluate 内注释。
	Passive *PassiveSignals

	// PassiveReadFailed=true 表示被动信号**读取失败**（权限/表缺失等降级），
	// 与「读了但无信号」是两件事 —— evidence 里分别留痕，不让降级伪装成干净。
	PassiveReadFailed bool

	// Now 是判定时刻（零值取当前时间）。
	Now time.Time
}

// PassiveSignals 是一个节点的被动完整性信号摘要。
//
// 数据来源：model_integrity_events（211 实测 14 天内 model_mismatch high 469 条，
// 未解决集中在少数凭据）⇒ 这是**真实用户流量**里已经撞上的偷换证据，
// 信息量高于主动探测的自报（上游可以骗自报，骗不过真实流量的响应体）。
type PassiveSignals struct {
	// HasMismatch=true 表示存在未解决的 model_mismatch。
	HasMismatch bool
	// MismatchExpected / MismatchActual 是最近一条事件的期望/实际模型名
	// （用于判同族降级还是跨族换模型；任一为空 ⇒ 取中性分）。
	MismatchExpected string
	MismatchActual   string
	// HasFingerprintDrift=true 表示存在未解决的 fingerprint_drift。
	HasFingerprintDrift bool
}

// passiveIdentityScore 由被动 mismatch 事件推出身份维分数。
//
//   - expected/actual 都可判族：跨族 ⇒ 10（与主动跨族同级：换模型）；
//     同族 ⇒ 50（与主动同族不同名同级：疑似同族降级，需能力维合看）。
//   - 任一名字缺失（事件里 expected/actual 为空）⇒ 35：确有 mismatch 但
//     定不了性 ⇒ suspected 级，不越权判 diluted。
//
// ★ 主动与被动取 **min** 融合：两条通道任一报警，该维分数就以更差的为准。
func (p *PassiveSignals) passiveIdentityScore() float64 {
	if p == nil || !p.HasMismatch {
		return 100
	}
	if p.MismatchExpected != "" && p.MismatchActual != "" {
		if SameFamily(p.MismatchExpected, p.MismatchActual) {
			return 50
		}
		return 10
	}
	return 35
}

// passiveConsistencyScore 由被动指纹漂移推出一致性维分数。
// 漂移是弱信号（上游发版也会变）⇒ 50，与主动 fingerprint 漂移同级。
func (p *PassiveSignals) passiveConsistencyScore() float64 {
	if p == nil || !p.HasFingerprintDrift {
		return 100
	}
	return 50
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

	// ── 被动信号融合（真实流量侧的证据，与主动探测取 min）──────────────
	//
	// ★ 为什么不是第五个维度：被动 mismatch 与主动身份自洽测的是**同一件事**
	// （上游给的是不是声称的模型），分开计维会重复计罪；正确做法是作为
	// 同一维度的**第二条证据通道**取 min —— 任一通道报警，该维就以更差为准。
	// ★ 被动信号可以让「未测」的维度变为「已测」：真实流量里已经撞上
	// model_mismatch，这就是身份证据，不需要等主动探测。
	if in.Passive != nil && in.Passive.HasMismatch {
		ps := in.Passive.passiveIdentityScore()
		if res.Scores.Measured.Has(DimIdentity) {
			if ps < res.Scores.Identity {
				res.Scores.Identity = ps
			}
		} else {
			res.Scores.Identity = ps
			res.Scores.Measured = res.Scores.Measured.Mark(DimIdentity)
		}
		addAnomaly(AnomalyIdentityPassiveMismatch)
	}
	if in.Passive != nil && in.Passive.HasFingerprintDrift {
		ps := in.Passive.passiveConsistencyScore()
		if res.Scores.Measured.Has(DimConsistency) {
			if ps < res.Scores.Consistency {
				res.Scores.Consistency = ps
			}
		} else {
			res.Scores.Consistency = ps
			res.Scores.Measured = res.Scores.Measured.Mark(DimConsistency)
		}
		addAnomaly(AnomalyConsistencyPassiveDrift)
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

	// verified 连续共识（见 Config.VerifiedConsecutiveRuns）。
	//
	// ★ streak 的口径：上一轮**最终落盘**的 verdict 是 verified ⇒ streak+1；
	// 否则 streak 重置为 1（本轮是 streak 的第一轮）。注意上一轮落盘的
	// verified 本身可能已经是「共识满」的结果——streak 只增不减地延续，
	// 不需要回溯历史（runs 表有全史，但 streak 一个数就够，不用查表）。
	if res.Verdict == VerdictVerified {
		streak := 1
		if in.Previous != nil && in.Previous.Verdict == VerdictVerified {
			streak = in.Previous.VerifiedStreak + 1
			if streak <= 0 {
				streak = 2
			}
		}
		need := cfg.VerifiedConsecutiveRuns
		if need < 1 {
			need = 1
		}
		res.Evidence["consensus_streak"] = streak
		res.Evidence["consensus_required"] = need
		if streak < need {
			// 共识未满：单轮良好不足以「已验证」。落 unknown（证据不足），
			// 不落 suspected——没有异常信号，只是证据还不够多。
			res.Verdict = VerdictUnknown
			res.Evidence["consensus_pending"] = true
		}
	}

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
	// 被动通道的读取状态：false=读了（有无信号都已看），true=降级没读到。
	res.Evidence["passive_read_failed"] = in.PassiveReadFailed
	if in.Passive != nil {
		res.Evidence["passive_mismatch"] = in.Passive.HasMismatch
		res.Evidence["passive_fingerprint_drift"] = in.Passive.HasFingerprintDrift
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
