package security

import (
	"context"
	"errors"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domain/governance"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
)

// SecurityHook 安全检查 Hook
type SecurityHook struct {
	intent *IntentAnalyzer
	threat *ThreatDetector
}

// NewSecurityHook 创建安全检查 Hook
func NewSecurityHook(intent *IntentAnalyzer, threat *ThreatDetector) *SecurityHook {
	return &SecurityHook{intent: intent, threat: threat}
}

// Name 返回 Hook 名称
func (h *SecurityHook) Name() string { return "security.check" }

// Priority 返回优先级
func (h *SecurityHook) Priority() int { return 100 }

// Enabled 是否启用
func (h *SecurityHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	return env != nil
}

// Execute 执行安全检查
//
// PR-V4-04 收口：除保留 env.Metadata["security_verdict"] 以兼容既有读取方外，
// 同时把判定写入 env.EnsureGovernance()，使 domains/interception.Engine
// 能与 v4 安全插件 verdicts 一同参与决策。
func (h *SecurityHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	content, _ := env.Metadata["user_content"].(string)
	if content == "" {
		return nil // 无内容跳过
	}

	verdict := &Verdict{Allow: true}
	verdict.Intent = h.intent.Analyze(content)
	verdict.Threats = h.threat.Detect(content)

	if h.threat.IsCritical(verdict.Threats) {
		verdict.Allow = false
		verdict.Reason = "critical threat detected"
	}

	// 向后兼容：保留 metadata 路径，给旧代码读
	env.Metadata["security_verdict"] = verdict
	env.Metadata["security_checked_at"] = time.Now()

	// V4 收口：写入 governance 子结构
	gv := toGovernanceVerdict(verdict)
	if gv != nil {
		env.EnsureGovernance().RecordVerdict(gv)
	}

	if !verdict.Allow {
		return errors.New("security: request blocked: " + verdict.Reason)
	}
	return nil
}

// toGovernanceVerdict 把 v3 私有 Verdict 转写到共享 governance.Verdict。
func toGovernanceVerdict(v *Verdict) *governance.Verdict {
	if v == nil {
		return nil
	}
	gv := &governance.Verdict{
		PluginName: "security.legacy",
		Allow:      v.Allow,
		Severity:   0,
		Code:       "legacy_bridge",
		Reason:     v.Reason,
	}
	if !v.Allow {
		gv.Severity = 3
		gv.Code = "legacy.critical"
	}
	return gv
}

// OnError 错误处理（安全阻断必须上报）
func (h *SecurityHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	if env != nil {
		env.StatusCode = 403
	}
	return err
}

var _ pipeline.Hook = (*SecurityHook)(nil)
