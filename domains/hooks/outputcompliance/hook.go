// Package outputcompliance 把 domains/outputcompliance 接入 V4 Pipeline。
//
// 设计：Hook 不直接依赖 *outputcompliance.Checker 类型，而是依赖最小接口
// Checker，由调用方注入具体实现。这样本包可以独立编译、单元测试，无需 DB。
//
// 与 promptinjection Hook 不同，本 Hook 在 PhasePostUpstream 执行，
// 读 env.FinalResponse 做检测；命中阈值时：
//   - Blocked=true → 返回 error，Pipeline 中断，dispatch gate 写 403
//   - Blocked=false 但有 redact → 用 RedactedOutput 回写 FinalResponse
package outputcompliance

import (
	"context"
	"errors"

	"github.com/kaixuan/llm-gateway-go/domain"            //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domain/governance" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"  //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// ComplianceIssue 是 outputcompliance.ComplianceIssue 的精简镜像。
type ComplianceIssue struct {
	Type        string
	Subtype     string
	Severity    int
	Location    string
	Content     string
	Score       float64
	Redacted    bool
	Description string
}

// ComplianceResult 是 outputcompliance.ComplianceResult 的精简镜像。
type ComplianceResult struct {
	Compliant      bool
	Issues         []ComplianceIssue
	RedactedOutput string
	Blocked        bool
}

// Checker 是 Hook 所需的最小接口；*outputcompliance.Checker 天然实现。
type Checker interface {
	Check(ctx context.Context, tenantID, output string) (*ComplianceResult, error)
}

// Hook 把输出合规检测接入 V4 Pipeline。
type Hook struct {
	checker Checker
}

// NewHook 构造 Hook。checker 为 nil 时 Enabled() 返回 false。
func NewHook(checker Checker) *Hook {
	return &Hook{checker: checker}
}

// Name 实现 pipeline.Hook。
func (h *Hook) Name() string { return "output_compliance.check" }

// Priority 在 PostUpstream 阶段中先执行（50）。
func (h *Hook) Priority() int { return 50 }

// Enabled 当 checker 非 nil 且 FinalResponse 非空时启用。
func (h *Hook) Enabled(_ context.Context, env *domain.PipelineRequest) bool {
	if h == nil || h.checker == nil || env == nil {
		return false
	}
	return len(env.FinalResponse) > 0
}

// Execute 检查输出合规性。
//
// 行为：
//   - check 失败：降级为 verdict=allow + metadata log，不阻断（避免故障误杀）
//   - Blocked=true：返回 error，Pipeline 中断
//   - Blocked=false 但 RedactedOutput != 原 output：回写 env.FinalResponse
func (h *Hook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	output := string(env.FinalResponse)
	if output == "" {
		return nil
	}
	if env.Metadata == nil {
		env.Metadata = make(map[string]any)
	}
	result, err := h.checker.Check(ctx, env.TenantID, output)
	if err != nil {
		env.Metadata["output_compliance_error"] = err.Error()
		return nil
	}
	env.Metadata["output_compliance_result"] = map[string]any{
		"compliant": result.Compliant,
		"blocked":   result.Blocked,
		"issues":    len(result.Issues),
	}
	gv := toGovernanceVerdict(result)
	if gv != nil {
		env.EnsureGovernance().RecordVerdict(gv)
	}
	// 写回脱敏内容
	if !result.Blocked && result.RedactedOutput != "" && result.RedactedOutput != output {
		env.FinalResponse = []byte(result.RedactedOutput)
		env.Metadata["output_compliance_redacted"] = true
	}
	if result.Blocked {
		return errors.New("output_compliance: blocked by policy")
	}
	return nil
}

// OnError 阻断时设 403。
func (h *Hook) OnError(_ context.Context, env *domain.PipelineRequest, _ error) error {
	if env != nil {
		env.StatusCode = 403
	}
	return nil
}

// toGovernanceVerdict 把检测结果映射到 governance.Verdict。
//
// Severity 由 issue 最大 severity 决定：
//   - >=9 → 3 (critical)
//   - >=7 → 2 (block)
//   - >=4 → 1 (warn)
//   - 其它 → 0 (info)
func toGovernanceVerdict(r *ComplianceResult) *governance.Verdict {
	if r == nil {
		return nil
	}
	maxSev := 0
	issueTypes := make(map[string]int)
	for _, iss := range r.Issues {
		if iss.Severity > maxSev {
			maxSev = iss.Severity
		}
		issueTypes[iss.Type]++
	}
	gv := &governance.Verdict{
		PluginName: "output_compliance",
		Allow:      !r.Blocked,
		Reason:     "issues=" + itoa(len(r.Issues)),
		Evidence: map[string]any{
			"issue_types": issueTypes,
			"compliant":   r.Compliant,
			"redacted":    r.RedactedOutput != "",
		},
	}
	switch {
	case maxSev >= 9:
		gv.Severity = 3
		gv.Code = "output_compliance.critical"
	case maxSev >= 7:
		gv.Severity = 2
		gv.Code = "output_compliance.block"
	case maxSev >= 4:
		gv.Severity = 1
		gv.Code = "output_compliance.warn"
	default:
		gv.Code = "output_compliance.info"
	}
	if r.Blocked {
		gv.FixAction = "block_output"
	} else if r.RedactedOutput != "" {
		gv.FixAction = "redact_output"
	}
	return gv
}

// itoa 极简实现，避免引入 strconv（减少依赖噪音）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

var _ pipeline.Hook = (*Hook)(nil)
