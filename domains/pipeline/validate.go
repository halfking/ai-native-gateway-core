package pipeline

import (
	"fmt"

	"github.com/kaixuan/llm-gateway-go/internal/phasepolicy"
)

// TaskHandlerLookup 供注册期 V2 对拍：TaskType 必须已有 Handler。
// 由 internal/taskbus.Registry 实现；pipeline 不直接依赖 taskbus，避免环。
type TaskHandlerLookup interface {
	HasHandler(taskType string) bool
}

// ValidateStage 执行注册期验证 V1 / V2 / V5。
// V3 / V4 是静态扫描门（ursmcheck G1），不在此处。
//
// 失败语义：调用方（AddStage）panic ⇒ 启动失败，错误信息含 Vx 规则号。
func ValidateStage(stage *PipelineStage, lookup TaskHandlerLookup) error {
	if stage == nil {
		return nil
	}
	var sawInline, sawTaskBus bool
	for _, h := range stage.Hooks {
		if h == nil {
			continue
		}
		kind := ResolveDispatchKind(h)
		switch kind {
		case DispatchTaskBus:
			sawTaskBus = true
			if err := validateTaskBusHook(h, stage.Phase, lookup); err != nil {
				return err
			}
		default:
			sawInline = true
		}
	}
	// V5: 同一 Stage 内 Inline 与 TaskBus 混排 ⇒ 拒绝
	if sawInline && sawTaskBus {
		return fmt.Errorf("V5: stage %q mixes Inline and TaskBus hooks; "+
			"move TaskBus hooks into a dedicated taskbus_outbox stage", stage.Name)
	}
	return nil
}

func validateTaskBusHook(h Hook, phase Phase, lookup TaskHandlerLookup) error {
	// V1: TaskBus 只允许 phasepolicy=continue 的阶段
	policy, ok := phasepolicy.PolicyOf(string(phase))
	if !ok {
		return fmt.Errorf("V1: hook %q declares TaskBus on unknown phase %q", h.Name(), phase)
	}
	if policy != phasepolicy.PolicyContinue {
		return fmt.Errorf("V1: hook %q declares TaskBus on phase %q (policy=%s); "+
			"TaskBus only allowed on continue phases", h.Name(), phase, policy)
	}

	specced, ok := h.(TaskSpecced)
	if !ok {
		return fmt.Errorf("V2: hook %q declares TaskBus but does not implement TaskSpecced", h.Name())
	}
	spec := specced.TaskSpec()
	if spec.TaskType == "" {
		return fmt.Errorf("V2: hook %q TaskSpec.TaskType is empty", h.Name())
	}
	if spec.IdempotencyOf == nil {
		return fmt.Errorf("V2: hook %q TaskSpec.IdempotencyOf is nil", h.Name())
	}
	if spec.SchemaVersion <= 0 {
		return fmt.Errorf("V2: hook %q TaskSpec.SchemaVersion must be > 0, got %d", h.Name(), spec.SchemaVersion)
	}
	if lookup == nil || !lookup.HasHandler(spec.TaskType) {
		return fmt.Errorf("V2: hook %q TaskType %q has no registered handler", h.Name(), spec.TaskType)
	}
	return nil
}
