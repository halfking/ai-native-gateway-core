package securityevent

// 严重度归一化。
//
// 网关里各检测面的严重度刻度互不相同，直接透传会让消费侧没法排序。本文件
// 把它们折算到统一刻度，但**保留原始值**（各面的适配器负责把 raw_* 放进
// Metadata）。
//
// # 归一化映射表
//
//	原始刻度                       来源                       归一化
//	---------------------------   ------------------------   --------------------------
//	safe / pass / allow / ok       armor, guardian, sessionaudit   info
//	low                            anomaly, promptinjection         low
//	warn / warning                 guardian, sessionaudit, armor    medium
//	P2                             sensitive.AlertLevel             medium
//	rewrite                        guardian.GuardAction             medium
//	need_approval                  sessionaudit.Decision            medium
//	medium                         anomaly, promptinjection         medium
//	block / blocked / deny          armor, guardian, sessionaudit    high
//	high                           anomaly, promptinjection         high
//	P1                             sensitive.AlertLevel             high
//	critical                       anomaly, promptinjection         critical
//	P0                             sensitive.AlertLevel             critical
//
// ★ 两个数字刻度的**量纲不同**，不能靠同一个字符串规则处理：
//
//   - governance.Verdict.Severity 是 0-3 的**档位**（0=info,1=warn,2=block,
//     3=critical）；
//   - outputcompliance.ComplianceIssue.Severity 与 promptinjection 的
//     score 是 0-10 的**分数**。
//
// ⇒ 数字刻度一律走各自的显式转换函数（SeverityFromGovernanceLevel /
// SeverityFromScore），字符串刻度才走 NormalizeSeverity。**不要**把数字
// 当字符串喂给 NormalizeSeverity。

import (
	"strconv"
	"strings"
)

// NormalizeSeverity 把**字符串**刻度折算到归一化刻度。
//
// ★ 这里**只处理非数字的字符串刻度**。形如 "2" / "3" 的字符串是歧义的
// （governance 的 0-3 档位里 2=block，0-10 分刻度里 2≈low），放进同一个
// 规则里必然有一边是错的 ⇒ 数字一律走 SeverityFromGovernanceLevel /
// SeverityFromScore / SeverityFromNumeric。
//
// 第二个返回值表示是否命中已知刻度：未命中时返回 (SeverityInfo, false)，
// 由调用方决定兜底策略（**不要**静默当成 info —— 未知刻度当成"没问题"
// 是最危险的误读方向）。
func NormalizeSeverity(raw string) (Severity, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "info":
		return SeverityInfo, true
	case "low":
		return SeverityLow, true
	case "medium", "warn", "warning":
		return SeverityMedium, true
	case "high", "block", "blocked", "deny", "reject":
		return SeverityHigh, true
	case "critical":
		return SeverityCritical, true
	case "p0":
		return SeverityCritical, true
	case "p1":
		return SeverityHigh, true
	case "p2":
		return SeverityMedium, true
	case "safe", "pass", "allow", "ok", "none", "allowed":
		return SeverityInfo, true
	case "rewrite":
		// 护栏把内容改写了：确实发现了东西，但没放行原始内容 ⇒ medium。
		return SeverityMedium, true
	case "need_approval", "needs_approval", "approval_required":
		return SeverityMedium, true
	default:
		return SeverityInfo, false
	}
}

// SeverityFromGovernanceLevel 折算 governance.Verdict.Severity（0-3 档位）。
//
//	0 = info, 1 = warn, 2 = block, 3 = critical
func SeverityFromGovernanceLevel(level int) Severity {
	switch level {
	case 0:
		return SeverityInfo
	case 1:
		return SeverityMedium
	case 2:
		return SeverityHigh
	case 3:
		return SeverityCritical
	default:
		// 越界值不猜：当成 medium（需要人看一眼），并由调用方保留原值。
		return SeverityMedium
	}
}

// SeverityFromScore 折算 0-10 分刻度。
//
//	0      = info
//	1-3    = low
//	4-6    = medium
//	7-8    = high
//	9-10   = critical
//
// 越界分数按最接近的档位夹取（负数当 0，超过 10 当 10）。
func SeverityFromScore(score float64) Severity {
	switch {
	case score <= 0:
		return SeverityInfo
	case score < 4:
		return SeverityLow
	case score < 7:
		return SeverityMedium
	case score < 9:
		return SeverityHigh
	default:
		return SeverityCritical
	}
}

// SeverityFromNumeric 折算"看起来像数字"的字符串刻度。
//
// 用于那些只给出字符串数字、但语义未知的检测面：按 0-10 分处理。
// 解析失败返回 (SeverityInfo, false)。
func SeverityFromNumeric(raw string) (Severity, bool) {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return SeverityInfo, false
	}
	return SeverityFromScore(value), true
}
