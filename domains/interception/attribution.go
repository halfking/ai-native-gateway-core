package interception

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domain" //nolint:depguard // historical violation, B1 routing.go CQOS will fix
)

// 治理拦截归因（attribution）——回答「这次为什么被拦」。
//
// ★ 背景（2026-11-23，211 实测）：dispatch gate 的 short-circuit 日志只记
// `decision_kind`，不记 reason / 来源插件 ⇒ 线上出现 403 时无法归因
// 「是哪个插件、哪条规则、什么 severity 挡的」；容器重启后日志滚走，事后
// 任何审计表都补不回来（prompt_injection_detections 全 blocked=false，
// session_audit_records 空表 ⇒ 拦截根本没落库）。
//
// ★ 本文件只提供「读侧归因」，不改判定语义、不改状态码、不改响应体。
//
// ★ 泄露边界（重要）：**只取 verdict 的形态字段**（plugin / allow /
// severity / code / reason），**绝不取 Evidence**——Evidence 里可能有
// 命中片段、敏感词原文、input_sample 等用户内容；而各插件写入的 Reason
// 是形态化文本（"risk_level=high action=warn" / "issues=3" /
// "critical threat detected"），不含用户原文。
//
// 通用式：**「治理链生效」与「治理链可归因」是两件事**——前者是判定问题，
// 后者是观测问题；只修判定不修观测，等下次出事仍然说不清是谁挡的。

// maxAttributionReasonLen 单条 reason 的最大长度（防御性截断）。
//
// 形态化 reason 正常只有几十字符；超长说明上游写入了自由文本，截断以免
// 日志被单条记录撑爆。
const maxAttributionReasonLen = 200

// VerdictSummary 是单个 verdict 的可归因摘要（形态字段，无 evidence）。
type VerdictSummary struct {
	Plugin   string `json:"plugin"`
	Allow    bool   `json:"allow"`
	Severity int    `json:"severity"`
	Code     string `json:"code"`
	Reason   string `json:"reason"`
}

// DecisionAttribution 是一次治理决策的可归因摘要。
type DecisionAttribution struct {
	Kind         string           `json:"kind"`
	Reason       string           `json:"reason"`
	SourcePlugin string           `json:"source_plugin,omitempty"`
	TraceID      string           `json:"trace_id,omitempty"`
	Verdicts     []VerdictSummary `json:"verdicts,omitempty"`
	// DenyPlugins 是所有 Allow=false 的插件名（按出现顺序，去重）。
	//
	// 这是归因时最常被问的一句「谁挡的」——单独抽出来免得运维在
	// verdicts 列表里翻。
	DenyPlugins []string `json:"deny_plugins,omitempty"`
}

// AttributeDecision 从 env 抽取决策的可归因摘要。
//
// env 为 nil / 无 Governance / 无 Decision 时返回 nil（调用方应据此跳过
// 归因字段，而不是打空串）。
func AttributeDecision(env *domain.PipelineRequest) *DecisionAttribution {
	if env == nil || env.Governance == nil || env.Governance.Decision == nil {
		return nil
	}
	d := env.Governance.Decision
	out := &DecisionAttribution{
		Kind:         string(d.Kind),
		Reason:       truncateAttribution(d.Reason),
		SourcePlugin: d.SourcePlugin,
		TraceID:      d.TraceID,
	}
	seen := map[string]bool{}
	for _, v := range env.Governance.Verdicts {
		if v == nil {
			continue
		}
		out.Verdicts = append(out.Verdicts, VerdictSummary{
			Plugin:   v.PluginName,
			Allow:    v.Allow,
			Severity: v.Severity,
			Code:     v.Code,
			Reason:   truncateAttribution(v.Reason),
		})
		if !v.Allow && v.PluginName != "" && !seen[v.PluginName] {
			seen[v.PluginName] = true
			out.DenyPlugins = append(out.DenyPlugins, v.PluginName)
		}
	}
	return out
}

// AttributionLogFields 把归因摘要转成 slog 的 key-value 对。
//
// 形态是 "key" → value 的扁平对，直接 splat 进 slog.Info(...)。
// verdict 列表转成一行紧凑文本（而不是结构体数组）——slog 的
// JSON handler 对 []any 支持各家实现不一，扁平字符串最稳。
func AttributionLogFields(a *DecisionAttribution) []any {
	if a == nil {
		return nil
	}
	fields := []any{
		"decision_reason", a.Reason,
	}
	if a.SourcePlugin != "" {
		fields = append(fields, "decision_source_plugin", a.SourcePlugin)
	}
	if a.TraceID != "" {
		fields = append(fields, "decision_trace_id", a.TraceID)
	}
	if len(a.DenyPlugins) > 0 {
		fields = append(fields, "decision_deny_plugins", strings.Join(a.DenyPlugins, ","))
	}
	if len(a.Verdicts) > 0 {
		fields = append(fields, "decision_verdicts", formatVerdictSummaries(a.Verdicts))
	}
	return fields
}

// formatVerdictSummaries 把 verdict 列表压成一行紧凑文本。
//
// 形如：security.legacy(deny,sev3,legacy.critical); prompt_injection(allow,sev1,...)
// 只取形态字段；按 (allow, severity 降序, plugin 名) 稳定排序 ⇒ 同一份
// 决策每次输出同一行，便于日志对拍。
func formatVerdictSummaries(vs []VerdictSummary) string {
	sorted := make([]VerdictSummary, len(vs))
	copy(sorted, vs)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Allow != sorted[j].Allow {
			// deny 在前（先看谁挡的）。
			return !sorted[i].Allow
		}
		if sorted[i].Severity != sorted[j].Severity {
			return sorted[i].Severity > sorted[j].Severity
		}
		return sorted[i].Plugin < sorted[j].Plugin
	})
	parts := make([]string, 0, len(sorted))
	for _, v := range sorted {
		allow := "allow"
		if !v.Allow {
			allow = "deny"
		}
		seg := fmt.Sprintf("%s(%s,sev%d", v.Plugin, allow, v.Severity)
		if v.Code != "" {
			seg += "," + v.Code
		}
		if v.Reason != "" {
			seg += "," + v.Reason
		}
		parts = append(parts, seg+")")
	}
	return strings.Join(parts, "; ")
}

// truncateAttribution 截断超长 reason。
func truncateAttribution(s string) string {
	if len(s) <= maxAttributionReasonLen {
		return s
	}
	return s[:maxAttributionReasonLen] + "…"
}

// ── PI 家族判决分歧的观测（★ 2026-11-23 N8）────────────────────────────────
//
// ★ 为什么需要它（N8 的观测缺口）：
//
//	治理链里有**两套提示词注入判定**同时跑在**同一段文本**上：
//	★ `prompt_injection`（domains/security/plugins/prompt_injection.go）
//	  —— 包装 v3 ThreatDetector（词表 + PII 正则、阈值 7）⇒ **命中即 deny**；
//	★ `prompt_injection_enhanced`（核心 promptinjection.Detector）
//	  —— 6 层检测（规则/启发式/Canary/向量/LLM）+ 评分 + 分级动作
//	  ⇒ 同一段文本可能只给 `risk_level=high action=warn`（**不拦**）。
//	⇒ ★ 两者**口径相反**时，★ 链子取最严 ⇒ ★ 实际拦截由前者说了算。
//
//	★ ★ 但**测不到**：★ 归因日志只在 **short-circuit（被拦）** 时打
//	⇒ ★ **放行的请求永远不落判决记录** ⇒ ★ 「两套口径的分歧率与方向」
//	★ 在生产**没有任何数据** ⇒ ★ N8（定权威）就永远只能靠推理。
//
// ⇒ 修法：★ **只记分歧**（★ 罕见事件 ⇒ 噪声低、★ 不逐请求打日志）：
//	★ 当 PI 家族的判决里**同时存在 allow 与 deny** ⇒ 记一行形态化观测。
//
// ★ 泄露边界（同上）：★ 只取形态字段（plugin/allow/severity/code/reason）、
// ★ **绝不取 Evidence**。

// piFamilyPlugins —— 提示词注入家族的插件名（★ 判定「谁和谁在比」的口径）。
//
// ★ 为什么用白名单而不是「前缀匹配」：★ 治理链里还有别的插件
// ★ （敏感词、工具风险、数据外泄、策略合规……）⇒ ★ 它们与 PI 家族
// ★ **口径不同是正常的**（★ 例如敏感词插件 deny、PI 插件 allow ⇒ ★ 那是
// ★ **两件不同的事**，★ 不是「同一件事的两个实现」）⇒ ★ 混进分歧口径
// ★ 会把「插件之间的正常差异」误报成「实现分歧」。
var piFamilyPlugins = map[string]bool{
	"prompt_injection":          true,
	"prompt_injection_enhanced": true,
	"security.legacy":           true,
}

// PIDivergence 是一次「PI 家族内部判决分歧」的形态化摘要。
type PIDivergence struct {
	// Denied 插件名（★ 判 deny 的那些）。
	Denied []string `json:"denied,omitempty"`
	// Allowed 插件名（★ 判 allow 的那些）。
	Allowed []string `json:"allowed,omitempty"`
	// Verdicts 形态摘要（★ 无 evidence）。
	Verdicts []VerdictSummary `json:"verdicts,omitempty"`
}

// DetectPIVergence 从 env 里找出 PI 家族内部的判决分歧。
//
// ★ 返回 nil 表示「没有分歧」（★ 包括：★ 无 PI 家族插件 / ★ 全部同向）。
func DetectPIVergence(env *domain.PipelineRequest) *PIDivergence {
	if env == nil || env.Governance == nil {
		return nil
	}
	var denied, allowed []string
	var summaries []VerdictSummary
	for _, v := range env.Governance.Verdicts {
		if v == nil || !piFamilyPlugins[v.PluginName] {
			continue
		}
		if v.Allow {
			allowed = append(allowed, v.PluginName)
		} else {
			denied = append(denied, v.PluginName)
		}
		summaries = append(summaries, VerdictSummary{
			Plugin:   v.PluginName,
			Allow:    v.Allow,
			Severity: v.Severity,
			Code:     v.Code,
			Reason:   truncateAttribution(v.Reason),
		})
	}
	if len(denied) == 0 || len(allowed) == 0 {
		// ★ 全部同向（★ 全 allow 或全 deny）⇒ ★ 不是分歧。
		return nil
	}
	return &PIDivergence{Denied: denied, Allowed: allowed, Verdicts: summaries}
}

// PIDivergenceLogFields 把分歧摘要转成 slog 的 key-value 对。
func PIDivergenceLogFields(d *PIDivergence) []any {
	if d == nil {
		return nil
	}
	fields := []any{
		"pi_divergence", "true",
	}
	if len(d.Denied) > 0 {
		fields = append(fields, "pi_divergence_denied", strings.Join(d.Denied, ","))
	}
	if len(d.Allowed) > 0 {
		fields = append(fields, "pi_divergence_allowed", strings.Join(d.Allowed, ","))
	}
	if len(d.Verdicts) > 0 {
		fields = append(fields, "pi_divergence_verdicts", formatVerdictSummaries(d.Verdicts))
	}
	return fields
}
