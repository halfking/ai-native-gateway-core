// Package main — 治理拦截归因守卫（2026-11-23，211 生产取证驱动）
//
// 背景（211 生产实测）：
//
//	v2 dispatch gate: short-circuit  request_id=… status=403 decision_kind=block
//
// 线上确实出现了治理链的 403 拦截（新容器 476 笔 http_request 中 1 笔，
// 旧容器 5924 笔中 0 笔）——这是 **L3 修复的预期后果**：此前 v2 管线在
// provider_discovery 阶段就中止，决策闸（dispatch gate）对真实流量根本
// 不可达 ⇒ 治理链形同虚设；修复后闸门第一次真正生效。
//
// 但日志只记 `decision_kind`，**不记 reason / 来源插件 / verdicts** ⇒
// 「谁挡的、哪条规则」无从查证。更糟的是事后也补不回来：
//
//	· prompt_injection_detections 近 24h 7299 条，blocked=true 0 条
//	  ⇒ 拦截不落该表；
//	· session_audit_records 空表（0 行）⇒ 也不落该表；
//	· 容器 20:26:56Z 被重启后，旧实例日志随 docker json-file 滚走
//	  ⇒ 那一笔 403 的归因永久丢失。
//
// 这些门钉住四件事：
//
//	① dispatch gate 的 short-circuit 日志**必须**带上归因字段
//	   （decision_reason / deny_plugins / verdicts 摘要）——接线守卫，
//	   走真实源码文本，防「函数写了但没接线」；
//	② 归因内容正确：reason / 来源插件 / deny 插件名单取自真实 Decision；
//	③ 归因**不含 evidence**（命中片段等用户内容不进日志）；
//	④ 归因是**纯读侧**——不改判定、不改状态码、不改响应体。
package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domain/governance"
	"github.com/kaixuan/llm-gateway-go/domains/interception"
)

// ── 门 1（接线守卫）：dispatch gate 日志必须带归因字段 ─────────────────
func TestDispatchGateLogCarriesAttributionFields(t *testing.T) {
	src, err := os.ReadFile("main_pipeline.go")
	if err != nil {
		t.Fatalf("读 main_pipeline.go 失败: %v", err)
	}
	text := string(src)

	anchor := `"v2 dispatch gate: short-circuit"`
	idx := strings.Index(text, anchor)
	if idx < 0 {
		t.Fatalf("找不到 dispatch gate 的 short-circuit 日志锚点 %s", anchor)
	}
	// 取锚点**前后**的窗口：归因字段在 slog.Info 的参数区里，位置随代码
	// 排版变动（本次归因调用在锚点之前）。窗口取 1200 前 + 800 后。
	winStart := idx - 1200
	if winStart < 0 {
		winStart = 0
	}
	winEnd := idx + 800
	if winEnd > len(text) {
		winEnd = len(text)
	}
	tail := text[winStart:winEnd]

	for _, want := range []string{
		"interception.AttributeDecision(env)",
		"interception.AttributionLogFields(",
		"decision_kind",
	} {
		if !strings.Contains(tail, want) {
			t.Errorf("dispatch gate 日志缺少 %q —— 线上 403 将无法归因「谁挡的」\n"+
				"  ★ 容器重启后日志滚走、拦截又不落审计表 ⇒ 归因窗口只有这一次", want)
		}
	}
}

// ── 门 2（行为守卫）：归因取到真实 reason / 来源插件 / deny 名单 ─────────
func TestAttributeDecisionCapturesReasonAndDenyPlugins(t *testing.T) {
	env := domain.NewRequestEnvelope(context.Background(), nil)
	state := env.EnsureGovernance()
	state.RecordVerdict(&governance.Verdict{
		PluginName: "security.legacy",
		Allow:      false,
		Severity:   3,
		Code:       "legacy.critical",
		Reason:     "critical threat detected",
	})
	state.RecordVerdict(&governance.Verdict{
		PluginName: "prompt_injection",
		Allow:      true,
		Severity:   1,
		Code:       "prompt_injection.medium",
		Reason:     "risk_level=medium action=warn",
	})
	state.RecordDecision(&governance.Decision{
		Kind:         governance.DecisionBlock,
		Reason:       "[security.legacy] critical threat detected",
		SourcePlugin: "security.legacy",
		TraceID:      "trace-abc",
	})

	a := interception.AttributeDecision(env)
	if a == nil {
		t.Fatal("AttributeDecision 返回 nil（env 有 Decision 时不该为 nil）")
	}
	if a.Kind != "block" {
		t.Errorf("Kind = %q，期望 %q", a.Kind, "block")
	}
	if !strings.Contains(a.Reason, "critical threat detected") {
		t.Errorf("Reason = %q，未包含实际决策原因", a.Reason)
	}
	if a.SourcePlugin != "security.legacy" {
		t.Errorf("SourcePlugin = %q，期望 %q", a.SourcePlugin, "security.legacy")
	}
	if len(a.DenyPlugins) != 1 || a.DenyPlugins[0] != "security.legacy" {
		t.Errorf("DenyPlugins = %v，期望 [security.legacy]（只有 deny 的插件进名单）", a.DenyPlugins)
	}
	if len(a.Verdicts) != 2 {
		t.Errorf("Verdicts 条数 = %d，期望 2", len(a.Verdicts))
	}
}

// ── 门 3（泄露边界守卫）：归因不得包含 evidence（用户原文） ────────────
func TestAttributeDecisionExcludesEvidence(t *testing.T) {
	const secret = "用户手机号 13800001111"
	env := domain.NewRequestEnvelope(context.Background(), nil)
	state := env.EnsureGovernance()
	state.RecordVerdict(&governance.Verdict{
		PluginName: "prompt_injection",
		Allow:      false,
		Severity:   2,
		Code:       "prompt_injection.high",
		Reason:     "risk_level=high action=reject",
		Evidence: map[string]any{
			"matched_pattern": secret,
			"input_sample":    secret,
		},
	})
	state.RecordDecision(&governance.Decision{
		Kind:   governance.DecisionBlock,
		Reason: "[prompt_injection] risk_level=high action=reject",
	})

	a := interception.AttributeDecision(env)
	if a == nil {
		t.Fatal("AttributeDecision 返回 nil")
	}
	fields := interception.AttributionLogFields(a)
	var flat strings.Builder
	for _, f := range fields {
		if s, ok := f.(string); ok {
			flat.WriteString(s)
			flat.WriteString(" ")
		}
	}
	if strings.Contains(flat.String(), secret) {
		t.Errorf("归因日志字段包含 evidence 里的用户原文 %q —— evidence 不得进日志", secret)
	}
	// 但形态字段必须在，否则归因就失去了意义。
	if !strings.Contains(flat.String(), "prompt_injection") {
		t.Errorf("归因日志字段缺少插件名，实际=%q", flat.String())
	}
	if !strings.Contains(flat.String(), "decision_reason") {
		t.Errorf("归因日志字段缺少 decision_reason，实际=%q", flat.String())
	}
}

// ── 门 4（纯读侧守卫）：归因不改判定语义、不改响应体 ────────────────────
func TestAttributionDoesNotMutateDecisionOrResponse(t *testing.T) {
	env := domain.NewRequestEnvelope(context.Background(), nil)
	state := env.EnsureGovernance()
	state.RecordVerdict(&governance.Verdict{
		PluginName: "p", Allow: false, Severity: 3, Reason: "x",
	})
	state.RecordDecision(&governance.Decision{
		Kind:   governance.DecisionBlock,
		Reason: "[p] x",
	})

	codeBefore, bodyBefore := interception.WriteDecisionResponse(env)
	kindBefore := env.Governance.Decision.Kind
	reasonBefore := env.Governance.Decision.Reason

	_ = interception.AttributeDecision(env)
	_ = interception.AttributionLogFields(interception.AttributeDecision(env))

	if env.Governance.Decision.Kind != kindBefore {
		t.Errorf("归因改了 Decision.Kind：%q → %q", kindBefore, env.Governance.Decision.Kind)
	}
	if env.Governance.Decision.Reason != reasonBefore {
		t.Errorf("归因改了 Decision.Reason：%q → %q", reasonBefore, env.Governance.Decision.Reason)
	}
	codeAfter, bodyAfter := interception.WriteDecisionResponse(env)
	if codeBefore != codeAfter || string(bodyBefore) != string(bodyAfter) {
		t.Errorf("归因改了响应：(code=%d body=%s) → (code=%d body=%s)",
			codeBefore, bodyBefore, codeAfter, bodyAfter)
	}
}

// ── 门 5（空态守卫）：无 Decision 时归因返回 nil，日志字段不炸 ──────────
func TestAttributeDecisionNilSafe(t *testing.T) {
	if a := interception.AttributeDecision(nil); a != nil {
		t.Errorf("AttributeDecision(nil) = %+v，期望 nil", a)
	}
	env := domain.NewRequestEnvelope(context.Background(), nil)
	if a := interception.AttributeDecision(env); a != nil {
		t.Errorf("AttributeDecision(无 Decision) = %+v，期望 nil", a)
	}
	if f := interception.AttributionLogFields(nil); len(f) != 0 {
		t.Errorf("AttributionLogFields(nil) = %v，期望空", f)
	}
}
