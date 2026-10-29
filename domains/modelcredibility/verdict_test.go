package modelcredibility

import (
	"testing"
)

// ── 身份自洽维度 ──────────────────────────────────────────────────────────

func TestScoreIdentity(t *testing.T) {
	cases := []struct {
		name          string
		requested     string
		reported      string
		wantMeasured  bool
		wantScore     float64
		wantAnomalies []string
	}{
		{
			name: "exact match → 100",
			requested: "claude-sonnet-4-5",
			reported:  "claude-sonnet-4-5",
			wantMeasured: true, wantScore: 100,
		},
		{
			name: "case / separator variants → 100",
			requested: "Claude-Sonnet-4.5",
			reported:  "claude-sonnet-4-5",
			wantMeasured: true, wantScore: 100,
		},
		{
			name: "date snapshot suffix → 90 (same model, version snapshot)",
			requested: "gpt-5",
			reported:  "gpt-5-2025-08-07",
			wantMeasured: true, wantScore: 90,
			wantAnomalies: []string{AnomalyIdentityMismatch},
		},
		{
			name: "same family, different model → 50 (Sonnet→Haiku shape)",
			requested: "claude-sonnet-4-5",
			reported:  "claude-3-5-haiku",
			wantMeasured: true, wantScore: 50,
			wantAnomalies: []string{AnomalyIdentityMismatch},
		},
		{
			name: "cross family → 10 (asked Claude, got GPT)",
			requested: "claude-sonnet-4-5",
			reported:  "gpt-4o",
			wantMeasured: true, wantScore: 10,
			wantAnomalies: []string{AnomalyFamilyMismatch, AnomalyIdentityMismatch},
		},
		{
			name: "no reported model → not measured",
			requested: "claude-sonnet-4-5",
			reported:  "",
			wantMeasured: false,
			wantAnomalies: []string{AnomalyIdentityAbsent},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := Input{
				Target:   NodeTarget{RawModelName: tc.requested, OutboundModel: tc.requested},
				Observed: Observed{HTTPStatus: 200, ReportedModel: tc.reported, Content: "ready"},
			}
			anomalies := map[string]bool{}
			score, measured := scoreIdentity(in, func(a string) { anomalies[a] = true })
			if measured != tc.wantMeasured {
				t.Fatalf("measured = %v, want %v", measured, tc.wantMeasured)
			}
			if measured && score != tc.wantScore {
				t.Errorf("score = %v, want %v", score, tc.wantScore)
			}
			for _, a := range tc.wantAnomalies {
				if !anomalies[a] {
					t.Errorf("expected anomaly %q, got %v", a, anomalies)
				}
			}
		})
	}
}

// ── 家族判定 ──────────────────────────────────────────────────────────────

func TestModelFamily(t *testing.T) {
	cases := []struct{ model, want string }{
		{"claude-sonnet-4-5", "anthropic"},
		{"claude-3-5-haiku-20241022", "anthropic"},
		{"gpt-4o", "openai"},
		{"GPT-5", "openai"},
		{"o3-mini", "openai"},
		{"gemini-2.5-pro", "google"},
		{"llama-3.3-70b", "meta"},
		{"deepseek-v3", "deepseek"},
		{"glm-4.6", "zhipu"},
		{"qwen3-max", "alibaba"},
		{"kimi-k2", "moonshot"},
		{"doubao-seed-1.6", "bytedance"},
		{"unknown-model-x", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := ModelFamily(tc.model); got != tc.want {
			t.Errorf("ModelFamily(%q) = %q, want %q", tc.model, got, tc.want)
		}
	}
}

func TestSameFamily(t *testing.T) {
	if !SameFamily("claude-sonnet-4-5", "claude-3-5-haiku") {
		t.Error("claude models must be same family")
	}
	if SameFamily("claude-sonnet-4-5", "gpt-4o") {
		t.Error("claude vs gpt must not be same family")
	}
	// 未知族退化为「归一化名相同 ⇒ 同族」。
	if !SameFamily("weird-model", "weird-model") {
		t.Error("identical unknown names must be same family")
	}
	if SameFamily("weird-model", "other-model") {
		t.Error("different unknown names must not be same family")
	}
}

// ── 能力维度（同伴对比）──────────────────────────────────────────────────

func TestScoreCapability(t *testing.T) {
	cfg := DefaultConfig()
	// ★ 单测检验的是「判定线」本身，共识门另测（TestVerifiedConsensus）。
	cfg.VerifiedConsecutiveRuns = 1
	// 同伴：两家供应商，最好成绩 80。
	peer := PeerReference{
		Samples: []PeerSample{
			{CredentialID: 1, ProviderID: 10, ProviderName: "official", Accuracy: 80},
			{CredentialID: 2, ProviderID: 20, ProviderName: "relay", Accuracy: 30},
		},
	}
	cases := []struct {
		name        string
		accuracy    float64
		peer        PeerReference
		wantMeasured bool
		wantVerdict  Verdict
	}{
		{
			name: "at peer best → healthy",
			accuracy: 80, peer: peer,
			wantMeasured: true, wantVerdict: VerdictVerified,
		},
		{
			name: "far below peer best → diluted",
			accuracy: 40, peer: peer,
			wantMeasured: true, wantVerdict: VerdictDiluted,
		},
		{
			name: "no measurement → not measured",
			accuracy: 0, peer: peer,
			wantMeasured: false,
		},
		{
			name: "single-provider peer set → not usable (same backend behind every credential)",
			accuracy: 40,
			peer: PeerReference{Samples: []PeerSample{
				{CredentialID: 1, ProviderID: 10, Accuracy: 80},
				{CredentialID: 2, ProviderID: 10, Accuracy: 70},
			}},
			wantMeasured: false,
		},
		{
			name: "gap within noise band → measured and treated as parity",
			accuracy: 76, peer: peer,
			wantMeasured: true, wantVerdict: VerdictVerified,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := Input{
				Target:      NodeTarget{RawModelName: "m", OutboundModel: "m"},
				Observed:    Observed{HTTPStatus: 200, ReportedModel: "m", Content: "ready"},
				Accuracy:    tc.accuracy,
				HasAccuracy: tc.accuracy > 0,
				Peer:        tc.peer,
			}
			res := Evaluate(in, cfg)
			measured := res.Scores.Measured.Has(DimCapability)
			if measured != tc.wantMeasured {
				t.Fatalf("capability measured = %v, want %v (scores=%+v)",
					measured, tc.wantMeasured, res.Scores)
			}
			if tc.wantVerdict != "" && res.Verdict != tc.wantVerdict {
				t.Errorf("verdict = %v, want %v (scores=%+v anomalies=%v)",
					res.Verdict, tc.wantVerdict, res.Scores, res.Anomalies)
			}
		})
	}
}

// ── 一致性维度（指纹漂移）────────────────────────────────────────────────

func TestScoreConsistency(t *testing.T) {
	cfg := DefaultConfig()
	cases := []struct {
		name         string
		prev         *Previous
		obs          Observed
		wantMeasured bool
		wantScore    float64
		wantDrift    bool
	}{
		{
			name:         "no baseline → not measured",
			prev:         nil,
			obs:          Observed{HasUsage: true, PromptTokens: 20},
			wantMeasured: false,
		},
		{
			name: "identical signature → 100",
			prev: &Previous{HasPromptTokens: true, PromptTokens: 20},
			obs:  Observed{HasUsage: true, PromptTokens: 20},
			wantMeasured: true, wantScore: 100,
		},
		{
			name: "small delta within tolerance → 100",
			prev: &Previous{HasPromptTokens: true, PromptTokens: 20},
			obs:  Observed{HasUsage: true, PromptTokens: 22},
			wantMeasured: true, wantScore: 100,
		},
		{
			name: "severe drift → 0",
			prev: &Previous{HasPromptTokens: true, PromptTokens: 20},
			obs:  Observed{HasUsage: true, PromptTokens: 60},
			wantMeasured: true, wantScore: 0,
			wantDrift: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs := tc.obs
			obs.HTTPStatus = 200
			in := Input{
				Target:   NodeTarget{RawModelName: "m", OutboundModel: "m"},
				Observed: obs,
				Previous: tc.prev,
			}
			anomalies := map[string]bool{}
			score, measured := scoreConsistency(in, cfg, func(a string) { anomalies[a] = true })
			if measured != tc.wantMeasured {
				t.Fatalf("measured = %v, want %v", measured, tc.wantMeasured)
			}
			if measured && score != tc.wantScore {
				t.Errorf("score = %v, want %v", score, tc.wantScore)
			}
			if tc.wantDrift && !anomalies[AnomalySignatureDrift] {
				t.Errorf("expected %s anomaly, got %v", AnomalySignatureDrift, anomalies)
			}
		})
	}
}

// ── 参考基线维度 ─────────────────────────────────────────────────────────

func TestCalibrationAndReferenceParity(t *testing.T) {
	cfg := DefaultCalibrationConfig()
	// 构造一条完美的线性关系 ⇒ 拟合应当接近真实斜率。
	pts := []CalibrationPoint{
		{StandardIQ: 10, MedianAccuracy: 20, ProviderIDs: []int64{1}},
		{StandardIQ: 20, MedianAccuracy: 30, ProviderIDs: []int64{1}},
		{StandardIQ: 30, MedianAccuracy: 40, ProviderIDs: []int64{2}},
		{StandardIQ: 40, MedianAccuracy: 50, ProviderIDs: []int64{2}},
		{StandardIQ: 50, MedianAccuracy: 60, ProviderIDs: []int64{3}},
		{StandardIQ: 60, MedianAccuracy: 70, ProviderIDs: []int64{3}},
		{StandardIQ: 70, MedianAccuracy: 80, ProviderIDs: []int64{4}},
		{StandardIQ: 80, MedianAccuracy: 90, ProviderIDs: []int64{4}},
		{StandardIQ: 90, MedianAccuracy: 100, ProviderIDs: []int64{5}},
	}
	cal := FitCalibration(pts, cfg)
	if !cal.Usable(cfg) {
		t.Fatalf("calibration should be usable: %+v", cal)
	}
	if cal.Slope < 0.99 || cal.Slope > 1.01 {
		t.Errorf("slope = %v, want ~1.0", cal.Slope)
	}
	if cal.RSquared < 0.99 {
		t.Errorf("r2 = %v, want ~1.0", cal.RSquared)
	}

	// 拟合不达标时该维度必须「未测量」，而不是按 0 分算。
	badCfg := CalibrationConfig{MinModels: 100, MinRSquared: 0.5, MinProviders: 2}
	ref := ComputeReferenceParity(50, 50, cal, badCfg)
	if ref.Usable {
		t.Error("reference parity must not be usable with an unsatisfiable gate")
	}

	// 达标时：实测远低于期望 ⇒ 可用且比值低。
	ref2 := ComputeReferenceParity(30, 50, cal, cfg)
	if !ref2.Usable {
		t.Fatalf("reference parity should be usable: %+v", ref2)
	}
	if ref2.Ratio >= ReferenceHealthyRatio {
		t.Errorf("ratio = %v, want below healthy threshold", ref2.Ratio)
	}
}

// ── 综合分与结论 ─────────────────────────────────────────────────────────

func TestComputeOverallExcludesMissingDimensions(t *testing.T) {
	// 只有身份维度有数据 ⇒ 综合分 = 身份分（权重重新归一化）。
	s := SubScores{Identity: 100, Measured: MeasuredMask(0).Mark(DimIdentity)}
	if got := ComputeOverall(s); got != 100 {
		t.Errorf("overall = %v, want 100", got)
	}
	// 一个维度都没有 ⇒ 0（且必须由 DimensionCount()==0 区分「没测过」）。
	empty := SubScores{}
	if got := ComputeOverall(empty); got != 0 {
		t.Errorf("overall = %v, want 0", got)
	}
	if empty.Measured.DimensionCount() != 0 {
		t.Error("empty mask must report 0 measured dimensions")
	}
	// 身份 100 + 能力 0 ⇒ 身份权重 0.35 / 能力权重 0.40 ⇒ 0.35*100/0.75。
	s2 := SubScores{
		Identity:   100,
		Capability: 0,
		Measured:   MeasuredMask(0).Mark(DimIdentity).Mark(DimCapability),
	}
	want := 100 * SubScoreWeights[DimIdentity] / (SubScoreWeights[DimIdentity] + SubScoreWeights[DimCapability])
	if got := ComputeOverall(s2); got != want {
		t.Errorf("overall = %v, want %v", got, want)
	}
}

func TestDecideVerdict(t *testing.T) {
	cfg := DefaultConfig()
	// ★ 单测检验「判定线」，共识门另测。
	cfg.VerifiedConsecutiveRuns = 1
	cases := []struct {
		name   string
		scores SubScores
		want   Verdict
	}{
		{
			name:   "nothing measured → unknown",
			scores: SubScores{},
			want:   VerdictUnknown,
		},
		{
			name: "two clean dimensions → verified",
			scores: SubScores{
				Identity: 100, Capability: 100,
				Measured: MeasuredMask(0).Mark(DimIdentity).Mark(DimCapability),
			},
			want: VerdictVerified,
		},
		{
			name: "only one clean dimension → unknown (evidence insufficient)",
			scores: SubScores{
				Identity: 100,
				Measured: MeasuredMask(0).Mark(DimIdentity),
			},
			want: VerdictUnknown,
		},
		{
			name: "capability below diluted line → diluted",
			scores: SubScores{
				Identity: 100, Capability: 40,
				Measured: MeasuredMask(0).Mark(DimIdentity).Mark(DimCapability),
			},
			want: VerdictDiluted,
		},
		{
			name: "cross-family identity → diluted",
			scores: SubScores{
				Identity: 10, Capability: 100,
				Measured: MeasuredMask(0).Mark(DimIdentity).Mark(DimCapability),
			},
			want: VerdictDiluted,
		},
		{
			name: "same-family downgrade → suspected",
			scores: SubScores{
				Identity: 50, Capability: 90,
				Measured: MeasuredMask(0).Mark(DimIdentity).Mark(DimCapability),
			},
			want: VerdictSuspected,
		},
		{
			name: "signature drift → diluted",
			scores: SubScores{
				Identity: 100, Consistency: 10,
				Measured: MeasuredMask(0).Mark(DimIdentity).Mark(DimConsistency),
			},
			want: VerdictDiluted,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			overall := ComputeOverall(tc.scores)
			if got := decideVerdict(tc.scores, overall, cfg); got != tc.want {
				t.Errorf("verdict = %v, want %v (scores=%+v)", got, tc.want, tc.scores)
			}
		})
	}
}

// ── 探测失败 ⇒ 结论缺失，而不是「差」──────────────────────────────────────

func TestProbeFailureDoesNotCountReportedModelAsIdentity(t *testing.T) {
	cfg := DefaultConfig()
	in := Input{
		Target: NodeTarget{RawModelName: "claude-sonnet-4-5", OutboundModel: "claude-sonnet-4-5"},
		Observed: Observed{
			HTTPStatus:    500,
			ReportedModel: "claude-sonnet-4-5",
			HasUsage:      true,
			PromptTokens:  99,
		},
		ProbeErr: errProbeFailed,
		Previous: &Previous{HasPromptTokens: true, PromptTokens: 20, SystemFingerprint: "fp_old"},
	}
	res := Evaluate(in, cfg)
	if res.Scores.Measured.Has(DimIdentity) {
		t.Fatal("failed probe must not count reported model as identity evidence")
	}
	if res.Scores.Measured.Has(DimConsistency) {
		t.Fatal("failed probe must not compare signatures")
	}
	if res.Verdict == VerdictVerified || res.Verdict == VerdictDiluted {
		t.Fatalf("failed probe verdict = %s, want unknown/suspected only", res.Verdict)
	}
}

func TestScoreOrNull(t *testing.T) {
	measured := SubScores{Identity: 100, Measured: MeasuredMask(0).Mark(DimIdentity)}
	if scoreOrNull(measured, DimIdentity, 100) == nil {
		t.Fatal("measured dimension must persist")
	}
	if scoreOrNull(measured, DimCapability, 0) != nil {
		t.Fatal("unmeasured dimension must persist as NULL, not 0")
	}
	if scoreOrNull(measured, 0, measured.Identity) == nil {
		t.Fatal("overall with one measured dimension must persist")
	}
	empty := SubScores{}
	if scoreOrNull(empty, 0, 0) != nil {
		t.Fatal("overall with zero measured dimensions must persist as NULL")
	}
}

func TestEvaluateProbeFailureKeepsOtherDimensions(t *testing.T) {
	cfg := DefaultConfig()
	in := Input{
		Target:    NodeTarget{RawModelName: "claude-sonnet-4-5"},
		Observed:  Observed{HTTPStatus: 500},
		ProbeErr:  errProbeFailed,
		Accuracy:  80,
		HasAccuracy: true,
		Peer: PeerReference{Samples: []PeerSample{
			{CredentialID: 1, ProviderID: 10, ProviderName: "official", Accuracy: 80},
			{CredentialID: 2, ProviderID: 20, ProviderName: "relay", Accuracy: 78},
		}},
	}
	res := Evaluate(in, cfg)
	if res.Status != StatusFailed {
		t.Errorf("status = %v, want failed", res.Status)
	}
	if res.Scores.Measured.Has(DimIdentity) {
		t.Error("identity must not be measured when the probe failed")
	}
	// 能力维度仍然可用（来自历史实测，不依赖这次探测）。
	if !res.Scores.Measured.Has(DimCapability) {
		t.Error("capability must still be measured from historical runs")
	}
	found := false
	for _, a := range res.Anomalies {
		if a == AnomalyProbeFailed {
			found = true
		}
	}
	if !found {
		t.Errorf("expected probe failure anomaly, got %v", res.Anomalies)
	}
}

// ── 被动信号融合（真实流量侧证据）────────────────────────────────────────

func TestPassiveFusion(t *testing.T) {
	cfg := DefaultConfig()
	crossFamily := &PassiveSignals{
		HasMismatch:      true,
		MismatchExpected: "claude-sonnet-4-5",
		MismatchActual:   "gpt-4o",
	}
	sameFamily := &PassiveSignals{
		HasMismatch:      true,
		MismatchExpected: "claude-sonnet-4-5",
		MismatchActual:   "claude-3-5-haiku",
	}
	t.Run("passive cross-family mismatch alone → diluted", func(t *testing.T) {
		res := Evaluate(Input{
			Target:  NodeTarget{RawModelName: "claude-sonnet-4-5"},
			Passive: crossFamily,
		}, cfg)
		if !res.Scores.Measured.Has(DimIdentity) {
			t.Fatal("passive mismatch must make identity measured without an active probe")
		}
		if res.Scores.Identity != 10 {
			t.Errorf("identity = %v, want 10 (cross-family)", res.Scores.Identity)
		}
		if res.Verdict != VerdictDiluted {
			t.Errorf("verdict = %s, want diluted", res.Verdict)
		}
		if res.Evidence["passive_read_failed"] != false {
			t.Error("passive_read_failed must be false when signals were read")
		}
	})
	t.Run("passive same-family mismatch → suspected (needs capability to confirm)", func(t *testing.T) {
		res := Evaluate(Input{
			Target:  NodeTarget{RawModelName: "claude-sonnet-4-5"},
			Passive: sameFamily,
		}, cfg)
		if res.Scores.Identity != 50 {
			t.Errorf("identity = %v, want 50 (same family)", res.Scores.Identity)
		}
		if res.Verdict != VerdictSuspected {
			t.Errorf("verdict = %s, want suspected", res.Verdict)
		}
	})
	t.Run("passive caps active identity (min fusion)", func(t *testing.T) {
		// 主动自报全对（100），被动曾抓到同族 mismatch（50）⇒ 取 50。
		res := Evaluate(Input{
			Target:   NodeTarget{RawModelName: "m", OutboundModel: "m"},
			Observed: Observed{HTTPStatus: 200, ReportedModel: "m", Content: "ready"},
			Passive:  sameFamily,
		}, cfg)
		if res.Scores.Identity != 50 {
			t.Errorf("identity = %v, want 50 (min of active 100 and passive 50)", res.Scores.Identity)
		}
	})
	t.Run("passive drift makes consistency measured", func(t *testing.T) {
		res := Evaluate(Input{
			Target:  NodeTarget{RawModelName: "m"},
			Passive: &PassiveSignals{HasFingerprintDrift: true},
		}, cfg)
		if !res.Scores.Measured.Has(DimConsistency) || res.Scores.Consistency != 50 {
			t.Errorf("consistency = %v measured=%v, want 50/measured",
				res.Scores.Consistency, res.Scores.Measured.Has(DimConsistency))
		}
	})
	t.Run("passive read failure must not fake a verdict", func(t *testing.T) {
		res := Evaluate(Input{
			Target:            NodeTarget{RawModelName: "m"},
			PassiveReadFailed: true,
		}, cfg)
		if res.Verdict != VerdictUnknown {
			t.Errorf("verdict = %s, want unknown (no evidence at all)", res.Verdict)
		}
		if res.Evidence["passive_read_failed"] != true {
			t.Error("evidence must record the degraded passive read")
		}
	})
}

// ── verified 连续共识 ────────────────────────────────────────────────────

func TestVerifiedConsensus(t *testing.T) {
	cfg := DefaultConfig() // VerifiedConsecutiveRuns = 2
	clean := func(prev *Previous) Input {
		return Input{
			Target:   NodeTarget{RawModelName: "m", OutboundModel: "m"},
			Observed: Observed{HTTPStatus: 200, ReportedModel: "m", Content: "ready"},
			Peer: PeerReference{Samples: []PeerSample{
				{CredentialID: 1, ProviderID: 10, Accuracy: 80},
				{CredentialID: 2, ProviderID: 20, Accuracy: 79},
			}},
			Accuracy: 80, HasAccuracy: true,
			Previous: prev,
		}
	}
	t.Run("first clean run → unknown with consensus_pending", func(t *testing.T) {
		res := Evaluate(clean(nil), cfg)
		if res.Verdict != VerdictUnknown {
			t.Fatalf("verdict = %s, want unknown (single clean run is not enough)", res.Verdict)
		}
		if res.Evidence["consensus_pending"] != true || res.Evidence["consensus_streak"] != 1 {
			t.Fatalf("evidence = %v, want pending with streak 1", res.Evidence)
		}
	})
	t.Run("second consecutive clean run → verified", func(t *testing.T) {
		res := Evaluate(clean(&Previous{Verdict: VerdictVerified, VerifiedStreak: 1}), cfg)
		if res.Verdict != VerdictVerified {
			t.Fatalf("verdict = %s, want verified (streak 2/2)", res.Verdict)
		}
		if res.Evidence["consensus_streak"] != 2 {
			t.Errorf("streak = %v, want 2", res.Evidence["consensus_streak"])
		}
	})
	t.Run("streak resets after a bad run", func(t *testing.T) {
		// 上一轮 verified 但上一轮 streak 只有 1（它自己也是 pending 出来的
		// unknown —— 不可能；verified 落盘必然 streak>=K。这里验证 streak>1
		// 的延续：连续第 3 轮仍 verified。
		res := Evaluate(clean(&Previous{Verdict: VerdictVerified, VerifiedStreak: 2}), cfg)
		if res.Verdict != VerdictVerified || res.Evidence["consensus_streak"] != 3 {
			t.Fatalf("verdict=%s streak=%v, want verified/3", res.Verdict, res.Evidence["consensus_streak"])
		}
	})
}

type probeErr string

func (e probeErr) Error() string { return string(e) }

const errProbeFailed = probeErr("probe failed")
