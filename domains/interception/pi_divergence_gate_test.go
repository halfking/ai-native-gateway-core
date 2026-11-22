package interception

// pi_divergence_gate_test.go —— 「PI 家族判决分歧」观测的性质门（★ 2026-11-23 N8）
//
// # 这道门守什么
//
// 治理链里有**两套提示词注入判定**同时跑在**同一段文本**上：
//
//	★ `prompt_injection`（domains/security/plugins/prompt_injection.go）
//	  —— 包装 v3 ThreatDetector（词表 + PII 正则、阈值 7）⇒ **命中即 deny**；
//	★ `prompt_injection_enhanced`（核心 promptinjection.Detector）
//	  —— 6 层检测（规则/启发式/Canary/向量/LLM）+ 评分 + 分级动作
//	  ⇒ 同一段文本可能只给 `risk_level=high action=warn`（**不拦**）。
//
// ⇒ ★ 两者口径相反时链子取最严 ⇒ ★ 实际拦截由前者说了算。
// ⇒ ★ ★ ★ 但**有一类分歧测不到**（★ 本轮查实链子的判决规则后区分开的）：
// ⇒ ★ 归因日志只在 short-circuit（被拦）时打
// ⇒ ★ **放行侧的分歧永远不落判决记录**
// ⇒   （★ 形态 b：★ enhanced 判 medium ⇒ severity 1 ⇒ ★ 低于 BlockThreshold=2
// ⇒   ⇒ ★ 链子不拦、★ 请求正常放行）
// ⇒ ★ 「**有人认为该拦、却没人拦**」在生产完全不可见
// ⇒ ★ N8（定权威判定方）只能靠推理。
//
// ⇒ 本门钉住新加的**分歧观测**：★ 只记分歧、★ 罕见事件、★ 形态字段、★ 零泄露。
//
// ★ 通用式：★ **「谁说了算」要能被观测**，★ 否则就只能靠推理、★ 而推理会被
// ★ 「看起来像」说服（★ 本轮就差点把 enhanced 当成权威——★ 实际上它 DB 不可用时
// ★ 会静默跳过、★ 根本不在链子里说话）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domain/governance"
)

func piEnv(verdicts ...*governance.Verdict) *domain.PipelineRequest {
	return &domain.PipelineRequest{
		Governance: &governance.GovernanceState{Verdicts: verdicts},
	}
}

func piVerdict(plugin string, allow bool, sev int, reason string) *governance.Verdict {
	return &governance.Verdict{
		PluginName: plugin,
		Allow:      allow,
		Severity:   sev,
		Code:       "c",
		Reason:     reason,
		Evidence:   map[string]any{"matched_pattern": "ignore previous", "input_sample": "用户原文"},
	}
}

func TestDetectPIVergenceFindsCrossPluginDisagreement(t *testing.T) {
	// ① 词表库 deny + 评分库 allow ⇒ 必须判为分歧。
	d := DetectPIVergence(piEnv(
		piVerdict("prompt_injection", false, 3, "critical threat detected"),
		piVerdict("prompt_injection_enhanced", true, 2, "risk_level=high action=warn"),
	))
	if d == nil {
		t.Fatal("★ 没有检出分歧（★ 词表 deny + 评分 allow 就是分歧）")
	}
	if len(d.Denied) != 1 || d.Denied[0] != "prompt_injection" {
		t.Errorf("分歧的 deny 侧不对: %v", d.Denied)
	}
	if len(d.Allowed) != 1 || d.Allowed[0] != "prompt_injection_enhanced" {
		t.Errorf("分歧的 allow 侧不对: %v", d.Allowed)
	}

	// ② 全部同向（都 allow / 都 deny）⇒ 不是分歧。
	if d := DetectPIVergence(piEnv(
		piVerdict("prompt_injection", true, 0, ""),
		piVerdict("prompt_injection_enhanced", true, 2, "risk_level=high action=warn"),
	)); d != nil {
		t.Errorf("同向却报了分歧: %+v", d)
	}
	if d := DetectPIVergence(piEnv(
		piVerdict("prompt_injection", false, 3, "critical threat detected"),
		piVerdict("prompt_injection_enhanced", false, 3, "critical"),
	)); d != nil {
		t.Errorf("同向却报了分歧: %+v", d)
	}

	// ③ ★ 非 PI 家族的插件差异**不算**分歧（★ 敏感词 deny + PI allow 是两件不同的事）。
	if d := DetectPIVergence(piEnv(
		piVerdict("sensitive_word_input", false, 3, "hit"),
		piVerdict("prompt_injection", true, 0, ""),
	)); d != nil {
		t.Errorf("把非 PI 家族的插件差异误报成分歧: %+v", d)
	}

	// ③b ★ **形态 b**（★ 放行侧的分歧）：★ enhanced 判 medium ⇒ severity 1
	//   而词表库 allow ⇒ ★ 链子不拦（★ severity 1 < BlockThreshold=2）
	//   ⇒ ★ 请求**正常放行** ⇒ ★ **short-circuit 日志永远看不到它**
	//   ⇒ ★ 这正是本观测点存在的理由 ⇒ ★ 必须能被检出。
	if d := DetectPIVergence(piEnv(
		piVerdict("prompt_injection_enhanced", false, 1, "risk_level=medium action=warn"),
		piVerdict("prompt_injection", true, 0, ""),
	)); d == nil {
		t.Error("★ 放行侧的分歧（形态 b）没被检出 ⇒ ★ 「有人认为该拦、却没人拦」" +
			"在生产仍然不可见 ⇒ ★ 本观测点白做了")
	}

	// ④ 空输入 / nil 环境 ⇒ 不炸、不报。
	if d := DetectPIVergence(nil); d != nil {
		t.Errorf("nil env 报了分歧: %+v", d)
	}
	if d := DetectPIVergence(piEnv()); d != nil {
		t.Errorf("无 verdict 却报了分歧: %+v", d)
	}
}

func TestPIDivergenceLogFieldsExcludeEvidence(t *testing.T) {
	d := DetectPIVergence(piEnv(
		piVerdict("prompt_injection", false, 3, "critical threat detected"),
		piVerdict("prompt_injection_enhanced", true, 2, "risk_level=high action=warn"),
	))
	if d == nil {
		t.Fatal("前置：应当检出分歧")
	}
	fields := PIDivergenceLogFields(d)
	if len(fields) == 0 {
		t.Fatal("没有日志字段")
	}
	blob, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	line := string(blob)
	// ★ 泄露边界：★ evidence 里的命中片段与用户原文**不得**出现在日志字段里。
	for _, forbidden := range []string{"matched_pattern", "input_sample", "ignore previous", "用户原文"} {
		if strings.Contains(line, forbidden) {
			t.Errorf("日志字段里出现了 evidence 内容 %q ⇒ ★ 泄露边界破了", forbidden)
		}
	}
	// ★ 但形态字段必须在（★ 否则观测没有信息量）。
	for _, want := range []string{"pi_divergence", "pi_divergence_denied", "pi_divergence_allowed"} {
		if !strings.Contains(line, want) {
			t.Errorf("日志字段缺少 %q", want)
		}
	}
}

// TestPIDivergenceObserverIsWiredToThePassPath —— ★ 观测必须真的接在**放行路径**上。
//
// ★ 为什么这条重要：★ 分歧**只可能发生在没被拦的请求上**
// （★ 被拦的那批已经被 short-circuit 日志记下来了）
// ⇒ ★ 观测点接错位置（★ 比如只接在拦截分支里）⇒ ★ 它永远打不出东西。
func TestPIDivergenceObserverIsWiredToThePassPath(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "cmd", "gateway", "main_pipeline.go"))
	if err != nil {
		t.Fatalf("读 main_pipeline.go: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "DetectPIVergence(env)") {
		t.Error("main_pipeline.go 没有调用 DetectPIVergence ⇒ ★ 分歧观测没接线")
	}
	if !strings.Contains(src, "PI verdict divergence observed") {
		t.Error("main_pipeline.go 没有分歧观测的日志标记")
	}
	// ★ 必须落在 short-circuit 的 return **之后**（★ 放行路径）
	//   —— ★ 理由不是「分歧只发生在放行侧」（★ 那不成立：★ 形态 a 在拦截侧）
	//   ★ 而是「★ 放行侧的分歧**永远不会被 short-circuit 日志记到**」
	//   ★ ⇒ ★ 观测点必须挂在放行路径上才补得上那个洞。
	idxDiv := strings.Index(src, "DetectPIVergence(env)")
	idxSC := strings.Index(src, "v2 dispatch gate: short-circuit")
	if idxDiv < 0 || idxSC < 0 {
		t.Fatalf("缺少接线（div=%d sc=%d）", idxDiv, idxSC)
	}
	if idxDiv < idxSC {
		t.Errorf("分歧观测接在 short-circuit 之前（div=%d < sc=%d）⇒ ★ 它会落在被拦的分支上、★ 而分歧只发生在放行侧",
			idxDiv, idxSC)
	}
}
