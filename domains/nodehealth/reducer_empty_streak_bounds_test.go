// reducer_empty_streak_bounds_test.go —— 空响应阈值的**下限口径**单点化
// （★ 2026-11-11 审计 B-7 的钉测）
//
// ★ B-7 的原形态：★ 阈值**下限**散在**三处**且彼此不一致 ——
//
//	① settings 的 admin UI 契约（Min: 2）
//	② settings 的运行时 clamp（clampThresholdInt(..., 2, 50)）
//	③ reducer 的注入值有效性守卫（`n > 0` ⇒ ★ **1 会被接受**）
//
// ★★ 三处各写各的 ⇒ ★★ 迟早漂移 ⇒ ★★ 现在下限**只有一处**定义
// ★★（settings.MinEmptyResponseStreakThreshold）★★ ①②③ 一律引用它。
//
// ★ 本用例钉的是**第三处**的行为：★ 注入一个**低于下限**的阈值
// ★ ★⇒ ★★ reducer 不得把它当成有效阈值（★ 改前：★ 1 会被接受）
// ★ ★⇒ ★★ 不得据此触发降级 ⇒ ★★ 应当回落到默认阈值。
//
// ★ 行为兼容性说明：★ 生产注入点是 settings.EmptyResponseStreakThresholdCached
// ★（★ 已 clamp 到 ≥2）⇒ ★★ 生产上**永远传不进 1** ⇒ ★★ 本守卫改前改后
// ★★ 生产行为**逐字相同**；★ 本用例防的是「未来有人绕过 clamp」。

package nodehealth_test

import (
	"fmt"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/nodehealth"
	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
)

func boundsEmptyObservation(node nodehealth.NodeKey, attempt string) nodehealth.Observation {
	return nodehealth.Observation{
		Node: node, AttemptID: attempt, Phase: nodehealth.PhaseRequest,
		Outcome: requestjourney.OutcomeFailure, ErrorKind: nodehealth.ErrorKindEmptyResponse,
	}
}

// TestEmptyResponseStreakBelowDeclaredBoundFallsBackToDefault 断言注入一个
// 低于声明下限的阈值时，reducer 回落到默认阈值（★ 而不是接受它）。
func TestEmptyResponseStreakBelowDeclaredBoundFallsBackToDefault(t *testing.T) {
	// ★ 低于下限（★ 下限是 2 ⇒ 1 越界）。
	const injected = 1
	if injected >= 2 {
		t.Fatalf("本用例的前提失效：注入值 %d 已不在下限之下（★ 下限口径变了 ⇒ 请更新本用例并写明理由）", injected)
	}

	r := nodehealth.NewOutcomeReducerWithConfig(nodehealth.ReducerConfig{
		EmptyResponseStreakThreshold: func() int { return injected },
	})
	node := nodehealth.NodeKey{TenantID: "t", CredentialID: 7, Model: "glm-5.3-flash"}

	// ★ 连击到「注入值」那么多（1 次）⇒ 若越界值被接受，★ 这里就会触发降级。
	// ★ 由于它低于下限、已被拒绝 ⇒ ★ 实际生效的是默认阈值（5）⇒ 1 次不够。
	for i := 1; i <= injected; i++ {
		d, err := r.Reduce(boundsEmptyObservation(node, fmt.Sprintf("a%d", i)))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range d.Effects {
			if e.Kind == nodehealth.EffectSetBindingUnavailable {
				t.Fatalf("attempt %d：越界阈值 %d 被接受了（触发了绑定降级）⇒期望回落到默认阈值 %d",
					i, injected, nodehealth.DefaultEmptyResponseStreakThreshold)
			}
		}
	}

	// ★ 反向：默认阈值那么多次 ⇒ 必须触发降级（★ 证明回落的是默认阈值，
	// ★ 而非「阈值一律失效 ⇒ 永不降级」）。
	for i := injected + 1; i <= nodehealth.DefaultEmptyResponseStreakThreshold; i++ {
		d, err := r.Reduce(boundsEmptyObservation(node, fmt.Sprintf("b%d", i)))
		if err != nil {
			t.Fatal(err)
		}
		if i == nodehealth.DefaultEmptyResponseStreakThreshold {
			found := false
			for _, e := range d.Effects {
				if e.Kind == nodehealth.EffectSetBindingUnavailable {
					found = true
				}
			}
			if !found {
				t.Fatalf("第 %d 次（默认阈值 %d）没有触发绑定降级 ⇒ 回落口径不是默认阈值",
					i, nodehealth.DefaultEmptyResponseStreakThreshold)
			}
		}
	}
}
