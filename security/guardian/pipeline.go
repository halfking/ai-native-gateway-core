package guardian

import (
	"context"
	"fmt"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domain"
)

// PipelineInputGuardHook 将 Guardian.InputGuard 转化为 Pipeline Hook。
//
// 在 PhasePreRouting 阶段挂载，对所有请求执行输入侧安全检查。
type PipelineInputGuardHook struct {
	guard    InputGuard
	decider  *GuardDecider
	auditor  *Auditor
	tenantFn func(*domain.PipelineRequest) string
}

// NewPipelineInputGuardHook 创建 Pipeline Hook 适配器。
//
//	guard: 实际执行检查的 InputGuard
//	decider: 决策引擎（nil → ModeObserve 默认）
//	auditor: 审计器（nil → 默认）
//	tenantFn: 从 PipelineRequest 中提取 tenantID 的函数（nil → ""）
func NewPipelineInputGuardHook(guard InputGuard, decider *GuardDecider, auditor *Auditor, tenantFn func(*domain.PipelineRequest) string) *PipelineInputGuardHook {
	if decider == nil {
		decider = NewGuardDecider(ModeObserve)
	}
	if auditor == nil {
		auditor = NewAuditor(nil)
	}
	if tenantFn == nil {
		tenantFn = func(*domain.PipelineRequest) string { return "" }
	}
	return &PipelineInputGuardHook{
		guard:    guard,
		decider:  decider,
		auditor:  auditor,
		tenantFn: tenantFn,
	}
}

func (h *PipelineInputGuardHook) Name() string {
	return "guardian_input_" + h.guard.Name()
}

func (h *PipelineInputGuardHook) Priority() int {
	return 100
}

func (h *PipelineInputGuardHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	body := string(env.TransformedRequest)
	tenantID := h.tenantFn(env)
	mode := h.decider.Mode(tenantID, h.guard.Name())

	v, err := h.guard.CheckInput(ctx, body)
	if err != nil {
		return fmt.Errorf("guardian %s: %w", h.guard.Name(), err)
	}
	if v == nil {
		return nil
	}

	h.auditor.Log(ctx, &AuditEvent{
		TenantID: tenantID,
		Guard:    h.guard.Name(),
		Action:   string(v.Action),
		Message:  strings.TrimSpace(v.Message),
		Blocked:  v.Action == ActionBlock && mode == ModeBlock,
	})

	if v.Action == ActionBlock && mode == ModeBlock {
		return fmt.Errorf("blocked by %s: %s", h.guard.Name(), v.Message)
	}
	if v.Action == ActionRewrite && len(v.RewrittenBody) > 0 {
		env.TransformedRequest = v.RewrittenBody
	}
	return nil
}

func (h *PipelineInputGuardHook) Enabled(_ context.Context, _ *domain.PipelineRequest) bool {
	return true
}

func (h *PipelineInputGuardHook) OnError(_ context.Context, _ *domain.PipelineRequest, err error) error {
	return err
}

// PipelineOutputGuardHook 将 Guardian.OutputGuard 转化为 Pipeline Hook。
type PipelineOutputGuardHook struct {
	guard    OutputGuard
	decider  *GuardDecider
	auditor  *Auditor
	tenantFn func(*domain.PipelineRequest) string
}

func NewPipelineOutputGuardHook(guard OutputGuard, decider *GuardDecider, auditor *Auditor, tenantFn func(*domain.PipelineRequest) string) *PipelineOutputGuardHook {
	if decider == nil {
		decider = NewGuardDecider(ModeObserve)
	}
	if auditor == nil {
		auditor = NewAuditor(nil)
	}
	if tenantFn == nil {
		tenantFn = func(*domain.PipelineRequest) string { return "" }
	}
	return &PipelineOutputGuardHook{
		guard:    guard,
		decider:  decider,
		auditor:  auditor,
		tenantFn: tenantFn,
	}
}

func (h *PipelineOutputGuardHook) Name() string {
	return "guardian_output_" + h.guard.Name()
}

func (h *PipelineOutputGuardHook) Priority() int {
	return 100
}

func (h *PipelineOutputGuardHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	reqBody := string(env.TransformedRequest)
	respBody := string(env.UpstreamResponse)
	tenantID := h.tenantFn(env)
	mode := h.decider.Mode(tenantID, h.guard.Name())

	v, err := h.guard.CheckOutput(ctx, reqBody, respBody)
	if err != nil {
		return fmt.Errorf("guardian %s: %w", h.guard.Name(), err)
	}
	if v == nil {
		return nil
	}

	h.auditor.Log(ctx, &AuditEvent{
		TenantID: tenantID,
		Guard:    h.guard.Name(),
		Action:   string(v.Action),
		Message:  strings.TrimSpace(v.Message),
		Blocked:  v.Action == ActionBlock && mode == ModeBlock,
	})

	if v.Action == ActionBlock && mode == ModeBlock {
		return fmt.Errorf("blocked by %s: %s", h.guard.Name(), v.Message)
	}
	if v.Action == ActionRewrite && len(v.RewrittenBody) > 0 {
		env.UpstreamResponse = v.RewrittenBody
	}
	return nil
}

func (h *PipelineOutputGuardHook) Enabled(_ context.Context, _ *domain.PipelineRequest) bool {
	return true
}

func (h *PipelineOutputGuardHook) OnError(_ context.Context, _ *domain.PipelineRequest, err error) error {
	return err
}
