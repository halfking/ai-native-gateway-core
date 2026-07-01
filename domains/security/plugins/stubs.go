package plugins

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/domain"            //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domain/governance" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// SensitiveInputChecker 输入敏感信息检查（PR-V4-03 占位）。
//
// PR-V4-04 之前不接入真实 PII/SDP 检测，仅返回 allow=true verdict。
// PR-V4-04 起将复用 domains/security/armor 的检测器。
type SensitiveInputChecker struct{}

// NewSensitiveInputChecker 构造占位插件。
func NewSensitiveInputChecker() *SensitiveInputChecker { return &SensitiveInputChecker{} }

// Name 实现 security.Plugin。
func (p *SensitiveInputChecker) Name() string { return "sensitive_input" }

// Direction input。
func (p *SensitiveInputChecker) Direction() string { return "input" }

// Inspect 占位实现。
func (p *SensitiveInputChecker) Inspect(_ context.Context, _ *domain.PipelineRequest) (*governance.Verdict, error) {
	return &governance.Verdict{
		PluginName: p.Name(),
		Allow:      true,
		Severity:   0,
		Code:       "stub",
		Reason:     "PR-V4-03 stub; real impl in PR-V4-04",
	}, nil
}

// SensitiveOutputChecker 输出敏感信息检查（PR-V4-03 占位）。
type SensitiveOutputChecker struct{}

// NewSensitiveOutputChecker 构造占位插件。
func NewSensitiveOutputChecker() *SensitiveOutputChecker { return &SensitiveOutputChecker{} }

// Name 实现 security.Plugin。
func (p *SensitiveOutputChecker) Name() string { return "sensitive_output" }

// Direction output：在响应回客户端前运行。
func (p *SensitiveOutputChecker) Direction() string { return "output" }

// Inspect 占位实现。
func (p *SensitiveOutputChecker) Inspect(_ context.Context, _ *domain.PipelineRequest) (*governance.Verdict, error) {
	return &governance.Verdict{
		PluginName: p.Name(),
		Allow:      true,
		Severity:   0,
		Code:       "stub",
		Reason:     "PR-V4-03 stub; real impl in PR-V4-04",
	}, nil
}

// PolicyComplianceChecker 策略合规检查（PR-V4-03 占位）。
type PolicyComplianceChecker struct{}

// NewPolicyComplianceChecker 构造占位插件。
func NewPolicyComplianceChecker() *PolicyComplianceChecker { return &PolicyComplianceChecker{} }

// Name 实现 security.Plugin。
func (p *PolicyComplianceChecker) Name() string { return "policy_compliance" }

// Direction both。
func (p *PolicyComplianceChecker) Direction() string { return "both" }

// Inspect 占位实现。
func (p *PolicyComplianceChecker) Inspect(_ context.Context, _ *domain.PipelineRequest) (*governance.Verdict, error) {
	return &governance.Verdict{
		PluginName: p.Name(),
		Allow:      true,
		Severity:   0,
		Code:       "stub",
		Reason:     "PR-V4-03 stub; real impl in PR-V4-05",
	}, nil
}

// ToolRiskChecker 工具调用风险检查（PR-V4-03 占位）。
type ToolRiskChecker struct{}

// NewToolRiskChecker 构造占位插件。
func NewToolRiskChecker() *ToolRiskChecker { return &ToolRiskChecker{} }

// Name 实现 security.Plugin。
func (p *ToolRiskChecker) Name() string { return "tool_risk" }

// Direction input：tool 调用进入前。
func (p *ToolRiskChecker) Direction() string { return "input" }

// Inspect 占位实现。
func (p *ToolRiskChecker) Inspect(_ context.Context, _ *domain.PipelineRequest) (*governance.Verdict, error) {
	return &governance.Verdict{
		PluginName: p.Name(),
		Allow:      true,
		Severity:   0,
		Code:       "stub",
		Reason:     "PR-V4-03 stub; real impl in PR-V4-05",
	}, nil
}

// DataExfiltrationChecker 数据外泄检查（PR-V4-03 占位）。
type DataExfiltrationChecker struct{}

// NewDataExfiltrationChecker 构造占位插件。
func NewDataExfiltrationChecker() *DataExfiltrationChecker { return &DataExfiltrationChecker{} }

// Name 实现 security.Plugin。
func (p *DataExfiltrationChecker) Name() string { return "data_exfiltration" }

// Direction both。
func (p *DataExfiltrationChecker) Direction() string { return "both" }

// Inspect 占位实现。
func (p *DataExfiltrationChecker) Inspect(_ context.Context, _ *domain.PipelineRequest) (*governance.Verdict, error) {
	return &governance.Verdict{
		PluginName: p.Name(),
		Allow:      true,
		Severity:   0,
		Code:       "stub",
		Reason:     "PR-V4-03 stub; real impl in PR-V4-05",
	}, nil
}
