package guardian

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// GuardAction 守卫判定动作
type GuardAction string

const (
	ActionPass    GuardAction = "pass"    // 放行
	ActionWarn    GuardAction = "warn"    // 告警（记录+通知）
	ActionBlock   GuardAction = "block"   // 阻断（返回 4xx）
	ActionRewrite GuardAction = "rewrite" // 改写（脱敏/替换）
)

func (a GuardAction) Valid() bool {
	switch a {
	case ActionPass, ActionWarn, ActionBlock, ActionRewrite:
		return true
	}
	return false
}

// GuardVerdict 单次守卫检查结果
type GuardVerdict struct {
	Action        GuardAction
	GuardName     string
	Message       string
	RewrittenBody []byte
}

// InputGuard 输入侧守卫接口
type InputGuard interface {
	Name() string
	CheckInput(ctx context.Context, reqBody string) (*GuardVerdict, error)
}

// OutputGuard 输出侧守卫接口
type OutputGuard interface {
	Name() string
	CheckOutput(ctx context.Context, reqBody, respBody string) (*GuardVerdict, error)
}

// Guardian 统一安全审查入口
//
// 三层架构：
//
//	L1 InputGuard  — 输入侧（用户→LLM）
//	L2 LLM 自身    — 内置 RLHF 安全训练（信任，不做干预）
//	L3 OutputGuard — 输出侧（LLM→用户）
type Guardian struct {
	inputGuards  []InputGuard
	outputGuards []OutputGuard
	decider      *GuardDecider
	auditor      *Auditor
	logger       *slog.Logger
}

// GuardianOption 构建选项
type GuardianOption func(*Guardian)

func WithLogger(l *slog.Logger) GuardianOption {
	return func(g *Guardian) { g.logger = l }
}

func WithDecider(d *GuardDecider) GuardianOption {
	return func(g *Guardian) { g.decider = d }
}

func WithAuditor(a *Auditor) GuardianOption {
	return func(g *Guardian) { g.auditor = a }
}

// NewGuardian 创建 Guardian
func NewGuardian(inputs []InputGuard, outputs []OutputGuard, opts ...GuardianOption) *Guardian {
	g := &Guardian{
		inputGuards:  inputs,
		outputGuards: outputs,
		logger:       slog.Default(),
	}
	for _, opt := range opts {
		opt(g)
	}
	if g.decider == nil {
		g.decider = NewGuardDecider(ModeObserve)
	}
	if g.auditor == nil {
		g.auditor = NewAuditor(g.logger)
	}
	return g
}

// GuardInput 执行输入侧所有守卫（按注册顺序，快速失败）
//
//   - 任一守卫返回 ActionBlock → 立即返回该 verdict，后续守卫不执行
//   - ActionRewrite → 更新 reqBody，继续下一守卫
//   - ActionWarn    → 审计记录，继续下一守卫
//   - ActionPass    → 继续下一守卫
func (g *Guardian) GuardInput(ctx context.Context, tenantID string, reqBody string) (string, error) {
	body := reqBody
	for _, guard := range g.inputGuards {
		mode := g.decider.Mode(tenantID, guard.Name())
		if mode == ModeObserve {
			v, err := guard.CheckInput(ctx, body)
			if err != nil {
				g.logger.Warn("guardian input observe error", "guard", guard.Name(), "error", err)
			}
			if v != nil && v.Action == ActionBlock {
				g.auditor.Log(ctx, &AuditEvent{
					TenantID: tenantID,
					Guard:    guard.Name(),
					Action:   string(v.Action),
					Message:  fmt.Sprintf("[observe] would block: %s", v.Message),
				})
			}
			continue
		}

		v, err := guard.CheckInput(ctx, body)
		if err != nil {
			return "", fmt.Errorf("guardian input %s failed: %w (tenant=%s)", guard.Name(), err, tenantID)
		}
		if v == nil {
			continue
		}

		reason := strings.TrimSpace(v.Message)
		isBlock := (mode == ModeBlock && v.Action == ActionBlock)
		isWarnBlock := v.Action == ActionBlock

		g.auditor.Log(ctx, &AuditEvent{
			TenantID: tenantID,
			Guard:    guard.Name(),
			Action:   string(v.Action),
			Message:  reason,
			Blocked:  isBlock || isWarnBlock,
		})

		if isBlock || isWarnBlock {
			return "", fmt.Errorf("guardian input blocked by %s: %s (tenant=%s)", guard.Name(), reason, tenantID)
		}

		if v.Action == ActionRewrite && len(v.RewrittenBody) > 0 {
			body = string(v.RewrittenBody)
		}
	}
	return body, nil
}

// GuardOutput 执行输出侧所有守卫（按注册顺序）
func (g *Guardian) GuardOutput(ctx context.Context, tenantID, reqBody, respBody string) (string, error) {
	body := respBody
	for _, guard := range g.outputGuards {
		mode := g.decider.Mode(tenantID, guard.Name())
		if mode == ModeObserve {
			v, err := guard.CheckOutput(ctx, reqBody, body)
			if err != nil {
				g.logger.Warn("guardian output observe error", "guard", guard.Name(), "error", err)
			}
			if v != nil && v.Action == ActionBlock {
				g.auditor.Log(ctx, &AuditEvent{
					TenantID: tenantID,
					Guard:    guard.Name(),
					Action:   string(v.Action),
					Message:  fmt.Sprintf("[observe] would block: %s", v.Message),
				})
			}
			continue
		}

		v, err := guard.CheckOutput(ctx, reqBody, body)
		if err != nil {
			return "", fmt.Errorf("guardian output %s failed: %w (tenant=%s)", guard.Name(), err, tenantID)
		}
		if v == nil {
			continue
		}

		reason := strings.TrimSpace(v.Message)
		isBlock := (mode == ModeBlock && v.Action == ActionBlock)
		isWarnBlock := v.Action == ActionBlock

		g.auditor.Log(ctx, &AuditEvent{
			TenantID: tenantID,
			Guard:    guard.Name(),
			Action:   string(v.Action),
			Message:  reason,
			Blocked:  isBlock || isWarnBlock,
		})

		if isBlock || isWarnBlock {
			return "", fmt.Errorf("guardian output blocked by %s: %s (tenant=%s)", guard.Name(), reason, tenantID)
		}

		if v.Action == ActionRewrite && len(v.RewrittenBody) > 0 {
			body = string(v.RewrittenBody)
		}
	}
	return body, nil
}
