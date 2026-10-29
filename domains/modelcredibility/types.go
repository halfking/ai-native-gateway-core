// Package modelcredibility 实现「模型可信度」判定：识别上游供应商是否
// 存在「渗水」（model downgrade / 偷换模型 / 套壳转售），并把判定结果写回
// 供应商的模型质量与可信度参数。
//
// # 为什么需要这一层
//
// 仓内已有的能力面覆盖了「质量」，但没有覆盖「身份」：
//
//   - domains/modelquality 用 MMLU 精简题库给每个 (凭据,模型) 节点打智商分，
//     写 model_iq_runs / node_iq_latest；但它只与**上一次**自己的成绩比较
//     （checkQualityDrop），不与同款模型的**其它节点**比较，也不校验上游
//     到底在跑哪个模型。
//   - domains/streaming/integrity 是**被动**检测：等真实流量里出现
//     model_mismatch / fingerprint_drift 才落一条事件。
//   - models_canonical.standard_iq 是外部基线（Artificial Analysis指数），
//     但它与本地 MMLU 准确率**不是同一把尺**，不能直接相减。
//
// 于是「你买的是 claude-sonnet-4-5，上游实际给你 Haiku」这类渗水，
// 在本仓当前是看不见的。本包补上这一层。
//
// # 判定架构：四把互补的尺
//
// 单靠任何一把尺都有盲区，四把尺组合起来覆盖面才完整：
//
//	1. 身份自洽（identity）
//	    上游响应体自报的 model 名，与我们请求的模型是否同族/同名。
//	    → 抓「上游不隐藏、直接自曝」的换模型。
//	 2. 能力水位（capability）
//	    同一 canonical 模型在**不同供应商节点**上的实测准确率对比。
//	    → 抓「同族降级」（Sonnet→Haiku），因为同族分词器相同、
//	      身份自洽与分词签名都抓不到，只有能力差能抓。
//	 3. 指纹一致性（consistency）
//	    同一个节点在**不同时间**的请求签名（prompt_tokens 计数 /
//	    system_fingerprint）是否稳定。
//	    → 抓「中途换后端」：跨族换模型会改分词器 ⇒ prompt_tokens 变。
//	 4. 参考基线（reference）
//	    用 standard_iq 外部基线在全平台实测数据上拟合出的「期望准确率」，
//	    与本节点实测准确率比。
//	    → 抓「全网只有这一家供应商」时没有同伴可比的情况；
//	      拟合质量不达标时该维度不参与判定（宁可缺维度，不给假结论）。
//
// ★ 口径纪律（与仓内既有判据一致）：
//   - **缺失维度不计入**（不按 0 分算），与 domains/providerprofile 的
//     "if score > 0 才计入加权" 同契约。
//   - **阴性结论要当缺陷一样复核**：无法判定时写 unknown，不写 verified。
//   - **同语种/同族规则必须一致**：家族判定用「厂商前缀表 +名称前缀」，
//     不掺任何针对具体模型的特例。
package modelcredibility

import (
	"strings"
	"time"
)

// Verdict 是单个 (凭据, 模型) 节点的可信度判定结论。
//
// ★ 刻意保留 unknown 这个第三态：探测证据不足时必须写 unknown，
// 不能写 verified（否则「没查过」会被读成「查过且没问题」）。
type Verdict string

const (
	// VerdictUnknown证据不足（未探测 / 探测失败 / 全部维度缺失）。
	VerdictUnknown Verdict = "unknown"
	// VerdictVerified 身份与能力均与声称一致，且证据强度足够。
	VerdictVerified Verdict = "verified"
	// VerdictSuspected 存在异常信号，但证据强度不足以定性（仍需复测）。
	VerdictSuspected Verdict = "suspected"
	// VerdictDiluted 能力显著低于同款模型的最好水平，或身份自证换族。
	VerdictDiluted Verdict = "diluted"
)

// VerdictRank 给结论排序用（越大越严重）。unknown 排在中间：
// 它既不是「好」也不是「坏」，但在列表里不应该被当成「好」隐藏。
func (v Verdict) VerdictRank() int {
	switch v {
	case VerdictVerified:
		return 0
	case VerdictUnknown:
		return 1
	case VerdictSuspected:
		return 2
	case VerdictDiluted:
		return 3
	default:
		return 1
	}
}

// ValidVerdict 判断字符串是否是合法结论值。
func ValidVerdict(s string) bool {
	switch Verdict(s) {
	case VerdictUnknown, VerdictVerified, VerdictSuspected, VerdictDiluted:
		return true
	default:
		return false
	}
}

// Dimension 是可参与判定的维度位掩码。
type Dimension uint16

const (
	// DimIdentity 身份自洽：上游自报的模型与请求模型是否一致。
	DimIdentity Dimension = 1 << iota
	// DimCapability 能力水位：与同款模型最好节点的实测差距。
	DimCapability
	// DimConsistency 指纹一致性：同一节点的请求签名是否稳定。
	DimConsistency
	// DimReference 参考基线：与 standard_iq 拟合出的期望准确率的差距。
	DimReference
)

// MeasuredMask 是维度位掩码（哪些维度本次真的有数据）。
type MeasuredMask uint16

// Has 判断某维度是否有数据。
func (m MeasuredMask) Has(d Dimension) bool { return MeasuredMask(uint16(m)&uint16(d)) != 0 }

// Mark标记某维度有数据，返回新的掩码。
func (m MeasuredMask) Mark(d Dimension) MeasuredMask { return m | MeasuredMask(uint16(d)) }

// DimensionCount 返回有数据的维度数。
func (m MeasuredMask) DimensionCount() int {
	n := 0
	for _, d := range []Dimension{DimIdentity, DimCapability, DimConsistency, DimReference} {
		if m.Has(d) {
			n++
		}
	}
	return n
}

// SubScores 是四个维度的得分（0-100）与哪些维度有数据。
type SubScores struct {
	Identity    float64
	Capability  float64
	Consistency float64
	Reference   float64

	Measured MeasuredMask
}

// ScoreOf 取某维度的得分。
func (s SubScores) ScoreOf(d Dimension) float64 {
	switch d {
	case DimIdentity:
		return s.Identity
	case DimCapability:
		return s.Capability
	case DimConsistency:
		return s.Consistency
	case DimReference:
		return s.Reference
	default:
		return 0
	}
}

// SubScoreWeights 是四个维度在综合可信度里的权重（总和 = 1.0）。
//
// ★ 权重之和必须为 1：综合分是「按有数据的维度重新归一化」的加权平均，
// 缺哪一维就在剩下几维之间按比例放大（见 ComputeOverall）。
var SubScoreWeights = map[Dimension]float64{
	DimIdentity:    0.35,
	DimCapability:  0.40,
	DimConsistency: 0.15,
	DimReference:   0.10,
}

// Result 是一个节点一次判定的完整结果。
type Result struct {
	TenantID       string
	ProviderID     int64
	CredentialID   int64
	RawModelName   string
	CanonicalID    int64
	CanonicalName  string
	RequestedModel string

	// Observed 是本次探测的原始观测（可为空，见 Status）。
	Observed Observed

	Scores SubScores
	Overall float64
	Verdict Verdict

	// Anomalies 是异常项清单（稳定字符串，便于聚合统计）。
	Anomalies []string
	// Evidence 是判定证据（结构化，JSONB 落盘）。
	Evidence map[string]any

	// Status: success / partial / failed / inconclusive
	Status string
	MeasuredAt time.Time
}

// Observed 是单次身份探测的原始观测。
type Observed struct {
	// ReportedModel 是上游响应体 self-reported 的 model 名（可能为空）。
	ReportedModel string
	// SystemFingerprint 是响应头 X-System-Fingerprint（可能为空）。
	SystemFingerprint string
	// PromptTokens 是标定提示词的 prompt token 计数（请求签名）。
	PromptTokens int
	// HasUsage 表示上游是否返回了 usage 块。
	HasUsage bool
	// LatencyMs 是探测请求耗时（毫秒）。
	LatencyMs int
	// HTTPStatus 是探测响应状态码。
	HTTPStatus int
	// Content 非空表示模型确实作答了（用于区分「空回答」与「拒绝回答」）。
	Content string
}

// ProbeStatus 取值。
const (
	StatusSuccess      = "success"
	StatusPartial      = "partial"
	StatusFailed       = "failed"
	StatusInconclusive = "inconclusive"
)

// 扫描面口径。
//
// ★ ScopeFeatured 是默认：只探测 routing_policy.featured_models 命中的绑定，
// 与 model_probe 的 featuredCycle / shared_pick 的 Priority 2 同一张清单。
// 目的是把「真实 token 成本 + 对上游的请求量」钉在运维真正在用的模型上，
// 而不是全量目录（全量目录里有大量未投流的长尾绑定）。
const (
	// ScopeFeatured 只扫特色模型（routing_policy.featured_models）。
	ScopeFeatured = "featured"
	// ScopeAll 扫全部活跃绑定。
	ScopeAll = "all"
)

// ValidScope 判断字符串是否是合法扫描面。
func ValidScope(s string) bool {
	switch s {
	case ScopeFeatured, ScopeAll:
		return true
	default:
		return false
	}
}

// Anomaly 字符串常量（异常项清单用，跨版本稳定）。
const (
	// AnomalyIdentityMismatch 上游自报的模型名与请求模型不同名。
	AnomalyIdentityMismatch = "identity_model_name_mismatch"
	// AnomalyFamilyMismatch 上游自报的模型与请求模型**不同族**（跨厂商换模型）。
	AnomalyFamilyMismatch = "identity_family_mismatch"
	// AnomalyIdentityAbsent 上游不返回 model 字段，无法自证身份（弱信号）。
	AnomalyIdentityAbsent = "identity_reported_model_absent"
	// AnomalyCapabilityGap 能力水位显著低于同款模型最好节点。
	AnomalyCapabilityGap = "capability_below_peer_best"
	// AnomalyCapabilitySevereGap 能力水位严重低于同款模型最好节点。
	AnomalyCapabilitySevereGap = "capability_severely_below_peer_best"
	// AnomalyCapabilityLead 能力水位高于同款模型最好节点（可疑：基准被针对性训练）。
	AnomalyCapabilityLead = "capability_above_peer_best"
	// AnomalySignatureDrift 同一节点的 prompt token 计数发生漂移（后端被换）。
	AnomalySignatureDrift = "consistency_prompt_token_drift"
	// AnomalyFingerprintDrift 同一节点的 system_fingerprint 发生漂移。
	AnomalyFingerprintDrift = "consistency_system_fingerprint_drift"
	// AnomalyReferenceGap 与参考基线拟合出的期望准确率差距过大。
	AnomalyReferenceGap = "reference_below_expected_accuracy"
	// AnomalyNoCapabilityData 本次没有可用的能力实测数据。
	AnomalyNoCapabilityData = "capability_no_measurement"
	// AnomalyPeerSampleTooSmall 同款模型的同伴样本不足，无法做能力对比。
	AnomalyPeerSampleTooSmall = "capability_peer_sample_too_small"
	// AnomalyProbeFailed 身份探测请求失败。
	AnomalyProbeFailed = "identity_probe_failed"
	// AnomalyProbeEmpty 身份探测没有得到任何内容。
	AnomalyProbeEmpty = "identity_probe_empty_response"
	// AnomalyIdentityPassiveMismatch 真实流量里出现过未解决的模型不一致
	// （domains/streaming/integrity 的 model_mismatch 事件）——被动证据。
	AnomalyIdentityPassiveMismatch = "identity_passive_mismatch"
	// AnomalyConsistencyPassiveDrift 真实流量里出现过未解决的指纹漂移。
	AnomalyConsistencyPassiveDrift = "consistency_passive_drift"
)

// modelFamilyOf 用厂商前缀表把模型名归到「族」。
//
// ★ 只认「厂商前缀」这一个信号，不掺任何针对具体模型的特例：
// 同族判定规则在所有供应商、所有模型之间一致，避免「只对某些模型有效」。
var modelFamilyOf = map[string]string{
	"claude":     "anthropic",
	"gpt":        "openai",
	"o1":         "openai",
	"o3":         "openai",
	"o4":         "openai",
	"chatgpt":    "openai",
	"gemini":     "google",
	"gemma":      "google",
	"llama":      "meta",
	"mistral":    "mistral",
	"mixtral":    "mistral",
	"codestral":  "mistral",
	"devstral":   "mistral",
	"magistral":  "mistral",
	"qwen":       "alibaba",
	"qwq":        "alibaba",
	"glm":        "zhipu",
	"chatglm":    "zhipu",
	"deepseek":   "deepseek",
	"kimi":       "moonshot",
	"moonshot":   "moonshot",
	"doubao":     "bytedance",
	"ernie":      "baidu",
	"grok":       "xai",
	"step":       "stepfun",
	"hunyuan":    "tencent",
	"abab":       "minimax",
	"minimax":    "minimax",
	"yi":         "01ai",
	"gpt-oss":    "openai",
	"command":    "cohere",
	"aya":        "cohere",
	"phi":        "microsoft",
	"nemotron":   "nvidia",
	"glm-4":      "zhipu",
	"internlm":   "shanghai-ai",
	"qwen2":      "alibaba",
	"qwen3":      "alibaba",
	"seed":       "bytedance",
	"mimo":       "xiaomi",
	"turbo":      "openai",
	"davinci":    "openai",
	"babbage":    "openai",
	"nano":       "openai",
	"gemini-1-5": "google",
}

// familyPrefixOrder 决定「前缀越长越优先」的匹配顺序需要的长度上限。
const familyPrefixOrder = 12

// ModelFamily 返回模型名所属的厂商族；识别不出时返回空串。
//
// 规则（对所有模型一致）：
//  1. 取路由归一化键（小写、去日期后缀、折叠连续分隔符）。
//  2. 按「最长前缀优先」匹配厂商前缀表。
//  3. 匹配不上返回 ""（未知族）。
//
// ★ 未知族不是错误：两个都未知时，跨族判定退化为「名字不同就是不同」，
// 见 SameFamily 的实现。
func ModelFamily(model string) string {
	key := normalizeModelKey(model)
	if key == "" {
		return ""
	}
	// 最长前缀优先：先试 12 字符，再逐步缩短。
	for n := familyPrefixOrder; n > 0; n-- {
		if n > len(key) {
			continue
		}
		if fam, ok := modelFamilyOf[key[:n]]; ok {
			return fam
		}
	}
	// 退化：取第一段（直到第一个分隔符）再试一次。
	if idx := strings.IndexAny(key, "-_"); idx > 0 {
		if fam, ok := modelFamilyOf[key[:idx]]; ok {
			return fam
		}
	}
	return ""
}

// SameFamily 判断两个模型名是否属于同一族。
//
// ★ 三态语义（必须区分，否则会把「未知」误判成「换族」）：
//   - 两个都能识别族：比较族名。
//   - 任一识别不出族：退化为「归一化名相同 ⇒ 同族，否则不同族」。
func SameFamily(a, b string) bool {
	fa, fb := ModelFamily(a), ModelFamily(b)
	if fa != "" && fb != "" {
		return fa == fb
	}
	return normalizeModelKey(a) == normalizeModelKey(b)
}

// normalizeModelKey 把模型名归一化成「可比形态」：小写、去目录前缀、
// 去日期后缀、点号与连字符统一、折叠重复分隔符。
func normalizeModelKey(model string) string {
	s := strings.ToLower(strings.TrimSpace(model))
	if s == "" {
		return ""
	}
	if idx := strings.LastIndex(s, "/"); idx >= 0 {
		s = s[idx+1:]
	}
	// 日期后缀：-20250514 / -2025-05-14 / -250514
	s = stripDateSuffix(s)
	s = strings.ReplaceAll(s, ".", "-")
	s = strings.ReplaceAll(s, "_", "-")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "- ")
}

// stripDateSuffix 去掉模型名末尾的日期/版本快照后缀。
func stripDateSuffix(s string) string {
	parts := strings.Split(s, "-")
	for len(parts) > 1 {
		last := parts[len(parts)-1]
		if isDateLikeToken(last) {
			parts = parts[:len(parts)-1]
			continue
		}
		break
	}
	return strings.Join(parts, "-")
}

// isDateLikeToken 判断一个短横线分段是否像日期快照（8 位数字或 6 位数字，
// 或形如 2025-05-14 的组合已被切分成多段）。
func isDateLikeToken(tok string) bool {
	if len(tok) != 8 && len(tok) != 6 {
		return false
	}
	for _, r := range tok {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// nowFunc便于测试注入时钟。
var nowFunc = time.Now
