// reference.go — 能力参考系：同款模型的同伴对比 + 外部基线校准。
//
// reference.go — capability reference: peer parity across nodes serving the
// same canonical model, plus a platform-wide calibration of the external
// standard-IQ baseline.
//
// # 为什么需要「同伴对比」
//
// 同族降级（Sonnet→Haiku、Opus→Sonnet）有两条指纹都抓不到：
//   - 身份自洽：上游自报 "claude-sonnet-4-5"（中转把 model 字段改写了）。
//   - 请求签名：同族模型共用分词器，prompt token 计数不变。
// ⇒ 只有**能力实测**能抓：同一个模型名，在不同供应商上的实测准确率
// 不该差出一大截。差得多 ⇒ 这个节点在跑弱一档的模型。
//
// # 同伴对比的两个前提（缺一不可）
//
//  1. **同伴必须来自不同供应商**。
//     同一家中转的多个凭据背后是同一个后端 ⇒ 它们会**一起**降级，
//     对比它们等于自己跟自己比，什么都测不出来。
//     ⇒ 只有当同伴里至少有两家不同供应商时，"谁最好"才有意义。
//  2. **差距必须超过噪声带**。
//     同一模型不同节点的实测准确率本来就有几个百分点的抖动（题库采样、
//     温度、并发负载）。差距小于噪声带时不下结论——否则每个节点都会被
//     判成"比最好的那个差一点"，满屏假红。
//
// # 外部基线（standard_iq）为什么不能直接比
//
// models_canonical.standard_iq 来自 Artificial Analysis 的 Intelligence Index，
// 而本地实测的是 MMLU 精简题库准确率。两者**不是同一把尺**：题库不同、
// 权重不同、满分口径不同。直接相减得到的差值没有意义。
//
// ⇒ 本包的处理是：用**全平台自己的实测数据**拟合一条
// standard_iq → 期望 MMLU 准确率 的映射，再拿节点实测值与这条映射比。
// 拟合不达标（样本太少 / 拟合太差 / 斜率非正）时该维度**不参与判定**——
// 宁可缺一维，不给假结论。
package modelcredibility

import (
	"math"
	"sort"
	"time"
)

// PeerSample 是同款 canonical 模型在某个节点上的一次能力实测。
type PeerSample struct {
	CredentialID  int64
	ProviderID    int64
	ProviderName  string
	RawModelName  string
	Accuracy      float64 // 0-100，来自 model_iq_runs.accuracy
	SampleCount   int     // 该节点的实测次数
	TestedAt      time.Time
}

// PeerReference 是同一 canonical 模型在各节点上的实测能力分布。
type PeerReference struct {
	CanonicalID   int64
	CanonicalName string
	Samples       []PeerSample
}

// Count 返回样本数。
func (r PeerReference) Count() int { return len(r.Samples) }

// DistinctProviders 返回样本里出现的不同供应商数。
func (r PeerReference) DistinctProviders() int {
	seen := map[int64]struct{}{}
	for _, s := range r.Samples {
		seen[s.ProviderID] = struct{}{}
	}
	return len(seen)
}

// Best 返回最高准确率（无样本时返回 0）。
func (r PeerReference) Best() float64 {
	best := 0.0
	for _, s := range r.Samples {
		if s.Accuracy > best {
			best = s.Accuracy
		}
	}
	return best
}

// Median 返回准确率中位数。
func (r PeerReference) Median() float64 {
	if len(r.Samples) == 0 {
		return 0
	}
	vals := make([]float64, 0, len(r.Samples))
	for _, s := range r.Samples {
		vals = append(vals, s.Accuracy)
	}
	sort.Float64s(vals)
	n := len(vals)
	if n%2 == 1 {
		return vals[n/2]
	}
	return (vals[n/2-1] + vals[n/2]) / 2
}

// ProviderCountByName 返回按供应商名统计的样本数（诊断用）。
func (r PeerReference) ProviderCountByName() map[string]int {
	out := map[string]int{}
	for _, s := range r.Samples {
		out[s.ProviderName]++
	}
	return out
}

// PeerParity 是能力对比的结果。
type PeerParity struct {
	// Usable=true 表示这次对比**有资格下结论**（数据够：本次有准确率、
	// 同伴有样本、同伴来自足够多的不同供应商）。
	//
	// ★ Usable 与「有没有差距」是**两件事**，必须分开：
	//   一个节点**恰好是同伴里最好的那个** ⇒ 差距为 0 ⇒ 它与同伴持平
	//   ⇒ 这是一个**肯定结论**（能力维度已测量、得分 100），
	//   不是「没测出来」。把「差距小」当成「没数据」会让所有健康的节点
	//   的能力维度全部落空 ⇒ 判定永远只剩一两个维度 ⇒ 永远 unknown。
	Usable bool
	// WithinNoiseBand=true 表示差距在噪声带内（GapPP < MinGapPP）。
	// 噪声带内的差距**不足以**指控掺水 ⇒ 不产生异常项。
	WithinNoiseBand bool
	// Ratio 是本节点准确率与同伴最好成绩之比（0-1）。
	Ratio float64
	// GapPP 是与同伴最好成绩的差距（百分点）。
	GapPP float64
	// Best 是同伴最好成绩。
	Best float64
	// PeerCount 是同伴样本数。
	PeerCount int
	// ProviderCount 是同伴里不同供应商的数量。
	ProviderCount int
	// Reason 在 Usable=false 时说明为什么不下结论。
	Reason string
}

// PeerParityConfig 是能力对比的门限。
type PeerParityConfig struct {
	// MinProviders 是同伴里不同供应商数量的下限。低于它不下结论
	// （同一家中转的多个凭据背后是同一个后端，比了也白比）。
	MinProviders int
	// MinGapPP 是与同伴最好成绩的最小差距（百分点），低于它视为噪声。
	MinGapPP float64
	// HealthyRatio 是「与同伴持平」的比率下限。
	HealthyRatio float64
	// SuspectRatio 是「存疑」的比率下限，低于它判为渗水。
	SuspectRatio float64
}

// DefaultPeerParityConfig 返回默认门限。
//
// 门限取值的理由：
//   - HealthyRatio 0.92：MMLU 精简题库 50 题 ⇒ 1 题 = 2 个百分点，
//     同模型不同节点的抖动通常在 4~6 个百分点内 ⇒ 92% 之内算持平。
//   - SuspectRatio 0.80：差 20% 以上，正常抖动解释不了 ⇒ 判存疑/渗水。
//   - MinGapPP 8：8 个百分点 = 4 道题，超过题库采样噪声。
func DefaultPeerParityConfig() PeerParityConfig {
	return PeerParityConfig{
		MinProviders:  2,
		MinGapPP:      8,
		HealthyRatio:  0.92,
		SuspectRatio:  0.80,
	}
}

// ComputePeerParity 把节点准确率与同伴分布比较。
//
// ★ accuracy 为 0（没测到 / 测崩了）时直接判不可用——
// 「没测到」与「测到是 0 分」是两件事，不能混。
func ComputePeerParity(accuracy float64, ref PeerReference, cfg PeerParityConfig) PeerParity {
	out := PeerParity{PeerCount: ref.Count(), ProviderCount: ref.DistinctProviders()}
	if accuracy <= 0 {
		out.Reason = "no_accuracy_measurement"
		return out
	}
	if ref.Count() == 0 {
		out.Reason = "no_peer_samples"
		return out
	}
	out.Best = ref.Best()
	if out.Best <= 0 {
		out.Reason = "peer_best_is_zero"
		return out
	}
	out.Ratio = accuracy / out.Best
	out.GapPP = out.Best - accuracy
	if out.ProviderCount < cfg.MinProviders {
		out.Reason = "insufficient_distinct_providers"
		return out
	}
	// ★ 数据够 ⇒ 可下结论。差距大小只影响「结论有多严重」，
	// 不影响「能不能下结论」——见 PeerParity.Usable 的注释。
	out.WithinNoiseBand = out.GapPP < cfg.MinGapPP
	out.Usable = true
	return out
}

// Calibration 是 standard_iq → 期望 MMLU 准确率 的平台级拟合。
//
// 标准形：expected = Slope * standard_iq + Intercept。
type Calibration struct {
	Slope      float64
	Intercept  float64
	RSquared   float64
	SampleN    int     // 参与拟合的 canonical 模型数
	ProviderN  int     // 拟合数据覆盖的不同供应商数
	BuiltAt    time.Time
}

// CalibrationConfig 是校准的准入门槛。
type CalibrationConfig struct {
	// MinModels 是参与拟合的 canonical 模型数下限。
	MinModels int
	// MinRSquared 是拟合优度下限，低于它认为拟合不可信。
	MinRSquared float64
	// MinProviders 是拟合数据须覆盖的供应商数下限。
	MinProviders int
}

// DefaultCalibrationConfig 返回默认门槛。
func DefaultCalibrationConfig() CalibrationConfig {
	return CalibrationConfig{
		MinModels:    8,
		MinRSquared:  0.50,
		MinProviders: 2,
	}
}

// Usable 判断这条校准是否可以用于下结论。
//
// ★ 四条都要满足：样本够、拟合够好、斜率为正、覆盖够广。
// 斜率为负或接近零意味着"标准智商"与实测准确率在本平台数据上不相关
// ⇒ 用它下结论就是在制造噪声。
func (c Calibration) Usable(cfg CalibrationConfig) bool {
	return c.SampleN >= cfg.MinModels &&
		c.RSquared >= cfg.MinRSquared &&
		c.Slope > 0 &&
		c.ProviderN >= cfg.MinProviders
}

// ExpectedAccuracy 返回某标准智商对应的期望准确率。
func (c Calibration) ExpectedAccuracy(standardIQ float64) float64 {
	return c.Slope*standardIQ + c.Intercept
}

// CalibrationPoint 是校准拟合用的一个点：一个 canonical 模型的
// 标准智商与该模型各节点实测准确率的中位数。
type CalibrationPoint struct {
	CanonicalID    int64
	CanonicalName  string
	StandardIQ     float64
	MedianAccuracy float64
	// ProviderIDs 是该模型实测覆盖到的供应商 id 集合（去重前亦可）。
	ProviderIDs  []int64
	SampleCount  int
}

// FitCalibration 用最小二乘拟合 standard_iq → 准确率 的线性映射。
//
// ★ 返回的 Calibration 可能不可用（Usable=false）——调用方必须检查，
// 不可用时该维度记为「未测量」，而不是按 0 分算。
func FitCalibration(points []CalibrationPoint, cfg CalibrationConfig) Calibration {
	cal := Calibration{}
	if len(points) < 2 {
		return cal
	}
	var n, sumX, sumY, sumXY, sumXX float64
	providers := map[int64]struct{}{}
	for _, p := range points {
		if p.StandardIQ <= 0 || p.MedianAccuracy <= 0 {
			continue
		}
		n++
		sumX += p.StandardIQ
		sumY += p.MedianAccuracy
		sumXY += p.StandardIQ * p.MedianAccuracy
		sumXX += p.StandardIQ * p.StandardIQ
	}
	if n < 2 {
		return cal
	}
	denom := n*sumXX - sumX*sumX
	if math.Abs(denom) < 1e-9 {
		return cal
	}
	slope := (n*sumXY - sumX*sumY) / denom
	intercept := (sumY - slope*sumX) / n
	cal.Slope = slope
	cal.Intercept = intercept
	cal.SampleN = int(n)
	cal.BuiltAt = nowFunc()

	// R² = 1 - SS_res / SS_tot
	meanY := sumY / n
	var ssRes, ssTot float64
	for _, p := range points {
		if p.StandardIQ <= 0 || p.MedianAccuracy <= 0 {
			continue
		}
		pred := slope*p.StandardIQ + intercept
		ssRes += (p.MedianAccuracy - pred) * (p.MedianAccuracy - pred)
		ssTot += (p.MedianAccuracy - meanY) * (p.MedianAccuracy - meanY)
	}
	if ssTot > 1e-9 {
		cal.RSquared = 1 - ssRes/ssTot
	}
	for _, p := range points {
		for _, pid := range p.ProviderIDs {
			providers[pid] = struct{}{}
		}
	}
	cal.ProviderN = len(providers)
	return cal
}

// ReferenceParity 是参考基线维度的结果。
type ReferenceParity struct {
	Usable         bool
	Ratio          float64 // 实测准确率 / 期望准确率
	GapPP          float64 // 期望 - 实测（百分点，负值表示实测高于期望）
	Expected       float64
	Measured       float64
	StandardIQ     float64
	Reason         string
}

// ComputeReferenceParity 把节点实测准确率与校准基线比较。
//
// ★ 判定门限与同伴对比**刻意不同**：参考基线是全平台平均水平的期望，
// 单个节点本来就可能略低于均值 ⇒ 门槛更松（HealthyRatio 0.85）。
// 同伴对比是「与最好的那个比」，那是上界，门槛才紧。
func ComputeReferenceParity(accuracy float64, standardIQ float64, cal Calibration, cfg CalibrationConfig) ReferenceParity {
	out := ReferenceParity{Measured: accuracy, StandardIQ: standardIQ}
	if accuracy <= 0 {
		out.Reason = "no_accuracy_measurement"
		return out
	}
	if standardIQ <= 0 {
		out.Reason = "no_standard_iq"
		return out
	}
	if !cal.Usable(cfg) {
		out.Reason = "calibration_not_usable"
		return out
	}
	out.Expected = cal.ExpectedAccuracy(standardIQ)
	if out.Expected <= 0 {
		out.Reason = "calibration_expected_non_positive"
		return out
	}
	out.Ratio = accuracy / out.Expected
	out.GapPP = out.Expected - accuracy
	out.Usable = true
	return out
}

// ReferenceHealthyRatio 是参考基线维度的「持平」下限。
const ReferenceHealthyRatio = 0.85

// ReferenceSuspectRatio 是参考基线维度的「渗水」下限。
const ReferenceSuspectRatio = 0.70
