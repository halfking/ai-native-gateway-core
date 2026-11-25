package promptinjection

// error_replay.go — 复测：对一条已入队的错误「再测一次」，看它还在不在（2026-11-25 取证轮）。
//
// ★ 两种主动复测，覆盖面与边界都如实写明：
//
//	① ReplayRules —— 规则层重放：把队列里存档的输入原文**重新过一遍确定性的
//	   本地检测层**（基础规则 / 高级规则 / 启发式），读出现在会命中哪些规则。
//	   · 用途：规则误报类错误（禁用规则 / 改 pattern 之后，同一段输入不再命中）。
//	   · 零外部调用、零写入：不调判官、不查 canary / 向量、不自动学习、不写
//	     检测审计、不采集错误（判官与向量层要么花钱要么有副作用，且与
//	     「规则改没改对」无关）。
//	   · 每次都**新建**检测器 ⇒ 规则从库里现读 ⇒ 刚应用的修复立刻可见。
//
//	② RecheckLayer —— 出错的那一层现在通不通：
//	   · canary：能不能查到该租户的活动令牌（只读 COUNT，不碰泄漏统计）；
//	   · policy：能不能加载策略与严重度动作矩阵；
//	   · llm judge：**显式勾选才调**（产生一次真实判官调用；用在线检测器的
//	     端点解析器）。
//
// 「在不在」的判定与复发统计在 admin 层（它才知道修复是什么时候应用的）。

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// RulesReplayResult 是一次规则层重放的读数。
type RulesReplayResult struct {
	// Available=false 表示没法重放（没有输入原文 / 策略或规则读不出来），原因在 Reason。
	Available      bool     `json:"available"`
	Reason         string   `json:"reason,omitempty"`
	PolicyEnabled  bool     `json:"policy_enabled"`
	Whitelisted    bool     `json:"whitelisted"`
	Layers         []string `json:"layers"`
	MatchedRules   []string `json:"matched_rules"`
	Score          int      `json:"score"`
	RiskLevel      string   `json:"risk_level"`
	InputBytes     int      `json:"input_bytes"`
	InputTruncated bool     `json:"input_truncated"`
	// RuleCount 是参与重放的启用规则数（证明「没命中」不是因为规则集是空的）。
	RuleCount int   `json:"rule_count"`
	LatencyMs int64 `json:"latency_ms"`
}

// ReplayRules 用当前库里的规则对存档输入重跑本地确定性检测层。
func ReplayRules(ctx context.Context, db *sql.DB, tenantID, input string) (*RulesReplayResult, error) {
	started := time.Now()
	if db == nil {
		return nil, fmt.Errorf("promptinjection: database not configured")
	}
	input = strings.TrimSpace(input)
	if input == "" {
		return &RulesReplayResult{Available: false, Reason: "no_input", Layers: []string{}, MatchedRules: []string{}}, nil
	}
	if tenantID == "" {
		tenantID = defaultTenantID
	}
	// 每次新建：规则现读（刚应用的修复立刻可见）；新实例没有挂错误采集器 ⇒
	// 过程中的任何失败都不会被当成「新错误」再入队一次（否则复测会自激）。
	d, err := NewDetector(db)
	if err != nil {
		return &RulesReplayResult{Available: false, Reason: "rules_unavailable: " + truncateRunesSafe(err.Error(), 160),
			Layers: []string{}, MatchedRules: []string{}}, nil
	}
	policy, err := d.getPolicy(ctx, tenantID)
	if err != nil {
		return &RulesReplayResult{Available: false, Reason: "policy_unavailable: " + truncateRunesSafe(err.Error(), 160),
			Layers: []string{}, MatchedRules: []string{}}, nil
	}

	res := &RulesReplayResult{
		Available:     true,
		PolicyEnabled: policy.Enabled,
		Layers:        []string{},
		MatchedRules:  []string{},
		InputBytes:    len(input),
	}
	trimmed := truncateForDetection(input, policy.MaxInputLength)
	res.InputTruncated = len(trimmed) != len(input)
	if !policy.Enabled {
		res.Reason = "policy_disabled"
		res.LatencyMs = msSince(started)
		return res, nil
	}
	if d.isWhitelisted(trimmed, policy) {
		res.Whitelisted = true
		res.LatencyMs = msSince(started)
		return res, nil
	}

	rules := d.ruleSnapshot()
	res.RuleCount = len(rules.basic) + len(rules.advanced)
	var matched []MatchedRule
	if policy.EnableBasicRules {
		matched = append(matched, d.detectWithRules(trimmed, rules.basic)...)
		res.Layers = append(res.Layers, "basic")
	}
	if policy.EnableAdvancedRules {
		matched = append(matched, d.detectWithRules(trimmed, rules.advanced)...)
		res.Layers = append(res.Layers, "advanced")
	}
	if policy.EnableHeuristics {
		matched = append(matched, d.heuristicEngine.Detect(trimmed)...)
		res.Layers = append(res.Layers, "heuristics")
	}
	seen := map[string]bool{}
	for _, m := range matched {
		if m.RuleName == "" || seen[m.RuleName] {
			continue
		}
		seen[m.RuleName] = true
		res.MatchedRules = append(res.MatchedRules, m.RuleName)
	}
	res.Score = d.calculateScore(matched)
	res.RiskLevel = d.calculateRiskLevel(res.Score)
	res.LatencyMs = msSince(started)
	return res, nil
}

// LayerRecheckResult 是一次「出错的那一层现在通不通」的读数。
type LayerRecheckResult struct {
	// Layer: canary | policy | llm_judge
	Layer string `json:"layer"`
	// Status: ok | failed | skipped | not_applicable
	Status    string `json:"status"`
	Detail    string `json:"detail,omitempty"`
	LatencyMs int64  `json:"latency_ms"`
}

// RecheckOptions 是层复查的可选项。
type RecheckOptions struct {
	// IncludeLLM：llm_detection_failed 的复查要不要真的调一次判官。
	IncludeLLM bool
	// Judge 是在线检测器（带端点解析器）；nil ⇒ 判官复查不可用。
	Judge *Detector
	// Timeout 给单次复查封顶（0 ⇒ 默认 30s）。
	Timeout time.Duration
}

const defaultRecheckTimeout = 30 * time.Second

// RecheckLayer 对出错的那一层做一次只读的健康复查。
//
//	不认识的错误类型 ⇒ Status=not_applicable（不是失败：那一层本来就没有可复查的东西）。
func RecheckLayer(ctx context.Context, db *sql.DB, errorType, tenantID, input string, opts RecheckOptions) (*LayerRecheckResult, error) {
	if tenantID == "" {
		tenantID = defaultTenantID
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultRecheckTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	switch errorType {
	case ErrorTypeCanaryDetectionFailed:
		started := time.Now()
		if db == nil {
			return nil, fmt.Errorf("promptinjection: database not configured")
		}
		// 只读计数：不调 CanaryDetector.Detect（命中时会累加泄漏统计——复测不该改统计）。
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM canary_tokens WHERE tenant_id = $1 AND active = true`, tenantID).Scan(&n); err != nil {
			return &LayerRecheckResult{Layer: "canary", Status: "failed",
				Detail: truncateRunesSafe(err.Error(), 300), LatencyMs: msSince(started)}, nil
		}
		return &LayerRecheckResult{Layer: "canary", Status: "ok",
			Detail:    fmt.Sprintf("canary token query ok (active tokens: %d)", n),
			LatencyMs: msSince(started)}, nil

	case ErrorTypePolicyLoadFailed:
		started := time.Now()
		if db == nil {
			return nil, fmt.Errorf("promptinjection: database not configured")
		}
		d, err := NewDetector(db)
		if err != nil {
			return &LayerRecheckResult{Layer: "policy", Status: "failed",
				Detail: truncateRunesSafe(err.Error(), 300), LatencyMs: msSince(started)}, nil
		}
		policy, err := d.getPolicy(ctx, tenantID)
		if err != nil {
			return &LayerRecheckResult{Layer: "policy", Status: "failed",
				Detail: truncateRunesSafe(err.Error(), 300), LatencyMs: msSince(started)}, nil
		}
		if _, err := d.getSeverityAction(ctx, tenantID, "medium"); err != nil {
			return &LayerRecheckResult{Layer: "policy", Status: "failed",
				Detail: "severity action matrix: " + truncateRunesSafe(err.Error(), 300), LatencyMs: msSince(started)}, nil
		}
		return &LayerRecheckResult{Layer: "policy", Status: "ok",
			Detail:    fmt.Sprintf("policy loaded (enabled=%t, mode=%s)", policy.Enabled, policy.DetectionMode),
			LatencyMs: msSince(started)}, nil

	case ErrorTypeLLMDetectionFailed:
		if !opts.IncludeLLM {
			return &LayerRecheckResult{Layer: "llm_judge", Status: "skipped",
				Detail: "judge call not requested"}, nil
		}
		return recheckLLMJudge(ctx, opts.Judge, tenantID, input)

	default:
		return &LayerRecheckResult{Layer: "none", Status: "not_applicable"}, nil
	}
}

// recheckLLMJudge 用在线检测器的判官引擎对存档输入调一次判官。
//
//	★ 会产生一次真实的判官调用（费用很小，但必须是管理员显式勾选的）。
//	输入压到 4KB：复查的是「判官通不通」，不是「这段输入判什么」。
func recheckLLMJudge(ctx context.Context, judge *Detector, tenantID, input string) (*LayerRecheckResult, error) {
	if judge == nil || judge.llmEngine == nil {
		return &LayerRecheckResult{Layer: "llm_judge", Status: "not_applicable",
			Detail: "live detector not wired on this instance"}, nil
	}
	started := time.Now()
	policy, err := judge.getPolicy(ctx, tenantID)
	if err != nil {
		return &LayerRecheckResult{Layer: "llm_judge", Status: "failed",
			Detail: truncateRunesSafe(err.Error(), 300), LatencyMs: msSince(started)}, nil
	}
	if !policy.EnableLLMDetection || policy.LLMEngineID == nil {
		return &LayerRecheckResult{Layer: "llm_judge", Status: "not_applicable",
			Detail: "policy has no judge engine configured"}, nil
	}
	if policy.LLMDetectionSampleRate == 0 {
		return &LayerRecheckResult{Layer: "llm_judge", Status: "not_applicable",
			Detail: "judge sampling rate is 0 (explicitly disabled)"}, nil
	}
	timeoutSec := policy.LLMDetectionTimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 30
	}
	if timeoutSec > 60 {
		timeoutSec = 60
	}
	probe := truncateUTF8Bytes(strings.TrimSpace(input), 4096)
	if probe == "" {
		probe = "ping"
	}
	result, err := judge.llmEngine.Detect(ctx, *policy.LLMEngineID, probe, timeoutSec)
	if err != nil {
		return &LayerRecheckResult{Layer: "llm_judge", Status: "failed",
			Detail: truncateRunesSafe(err.Error(), 300), LatencyMs: msSince(started)}, nil
	}
	detail := "judge responded"
	if result != nil {
		detail = fmt.Sprintf("judge responded (is_injection=%t, confidence=%.2f)", result.IsInjection, result.Confidence)
	}
	return &LayerRecheckResult{Layer: "llm_judge", Status: "ok", Detail: detail, LatencyMs: msSince(started)}, nil
}

func msSince(t time.Time) int64 {
	return time.Since(t).Milliseconds()
}
