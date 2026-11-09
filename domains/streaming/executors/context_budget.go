package executors

import (
	"context"
	"errors"
	"fmt"

	"github.com/kaixuan/llm-gateway-go/internal/contextbudget"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// ContextBudgetError is a local capacity mismatch, not supplier health evidence.
// It survives dispatch exhaustion so HTTP handlers can return an explicit 422.
type ContextBudgetError struct {
	CredentialID int
	Model        string
	Cause        error
}

func (e *ContextBudgetError) Error() string {
	return fmt.Sprintf("context_budget_exceeded: model %s: %v", e.Model, e.Cause)
}

func (e *ContextBudgetError) Unwrap() error { return e.Cause }

func IsContextBudgetError(err error) bool {
	var budgetErr *ContextBudgetError
	return errors.As(err, &budgetErr)
}

func (e *Executor) candidateBudgetSpec(params *ExecParams, cand provider.Candidate) (contextbudget.Spec, bool) {
	ctx := context.Background()
	if params != nil && params.R != nil {
		ctx = params.R.Context()
	}
	spec, inherited := contextbudget.SpecFromContext(ctx)
	if !inherited && (e == nil || !e.EnforceContextBudget) {
		return spec, false
	}
	// A failover must use the new bucket's specification, never the preceding
	// bucket's window carried by a shared request context.
	spec.ContextWindow = 0
	if cand.ContextWindow != nil {
		spec.ContextWindow = *cand.ContextWindow
	}
	if discovered := discoveredContextLimit(params, cand); discovered > 0 && (spec.ContextWindow <= 0 || discovered < spec.ContextWindow) {
		spec.ContextWindow = discovered
	}
	return spec, true
}

func wrapContextBudget(cand provider.Candidate, err error) error {
	if err == nil {
		return nil
	}
	return &ContextBudgetError{CredentialID: cand.CredentialID, Model: cand.RawModel, Cause: err}
}

func (e *Executor) checkCandidateContextBudget(params *ExecParams, cand provider.Candidate, _ string, body []byte) error {
	spec, enabled := e.candidateBudgetSpec(params, cand)
	if !enabled {
		return nil
	}
	_, err := contextbudget.Check(body, spec)
	return wrapContextBudget(cand, err)
}

func (e *Executor) checkCandidateContextSpec(params *ExecParams, cand provider.Candidate) error {
	spec, enabled := e.candidateBudgetSpec(params, cand)
	if !enabled {
		return nil
	}
	_, err := contextbudget.Resolve(spec)
	return wrapContextBudget(cand, err)
}

func canFitBudget(err error) bool {
	var budgetErr *contextbudget.Error
	return errors.As(err, &budgetErr) && (budgetErr.Kind == contextbudget.TokensExceeded || budgetErr.Kind == contextbudget.BytesExceeded)
}

// fitCandidateContextBudget only changes an attempt-local body. Each candidate
// starts from its source again, and each retry is checked after wire conversion.
func (e *Executor) fitCandidateContextBudget(params *ExecParams, cand provider.Candidate, _ string, body []byte) ([]byte, error) {
	spec, enabled := e.candidateBudgetSpec(params, cand)
	if !enabled {
		return body, nil
	}
	_, err := contextbudget.Check(body, spec)
	if err == nil {
		return body, nil
	}
	if e == nil || e.FitContext == nil || !canFitBudget(err) {
		return nil, wrapContextBudget(cand, err)
	}
	budget, resolveErr := contextbudget.Resolve(spec)
	if resolveErr != nil {
		return nil, wrapContextBudget(cand, resolveErr)
	}
	// 2026-11-10（用户规则：大输入要压缩，不能直接报错）：Fit 按字节裁剪，
	// 校准估计器下 bytes ≈ tokens×3（CJK ~3B/token、代码 ~3.5B/token，blob
	// 5B/token）⇒ 字节目标取 InputTokens×3，紧缩后估计 ≈ 0.86×limit，Check
	// 必然通过。旧目标 InputTokens/2 把 256k-token 预算（minimax 512k 窗）
	// 压成 128KB 字节——ZCode 受保护区（系统指令+工具链+最近轮）本身就有
	// 60-100KB+，Fit 必然 ErrProtectedContext ⇒ 全候选 422，压缩机制名存
	// 实亡（生产日志 16:44 "cannot contain the protected task…" 实证）。
	// Check 仍是最终权威：fit 结果必须过校准估计器。
	limit := min(budget.JSONBytes, budget.InputTokens*3)
	for i := 0; i < 3 && limit > 0; i++ {
		out, fitErr := e.FitContext(append([]byte(nil), body...), limit)
		if fitErr != nil {
			return nil, wrapContextBudget(cand, fitErr)
		}
		_, err = contextbudget.Check(out, spec)
		if err == nil {
			return out, nil
		}
		if !canFitBudget(err) {
			break
		}
		limit = limit * 3 / 4
	}
	return nil, wrapContextBudget(cand, err)
}
