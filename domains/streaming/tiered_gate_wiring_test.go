package streaming

// 三级配额制（租户→组织→用户）音频面接线判据（2026-11-27）。
//
// team_budget_gate 教训：判定体写好但零调用点 ⇒ 门静默失效。
// 本判据钉住两点：① concrete *maas.Service 满足 audioTieredBalanceGate
// 接口（编译期断言）；② SetMaas 自动接入；③ 桩不接入（既有测试语义不变）。

import (
	"context"
	"errors"
	"testing"

	"github.com/kaixuan/llm-gateway-go/maas"
)

// 编译期接口契约：*maas.Service 必须满足音频面分级预检接口。
var _ audioTieredBalanceGate = (*maas.Service)(nil)

// stubAudioChargerOnly 只实现 audioMaasCharger（既有测试桩形状）。
type stubAudioChargerOnly struct{}

func (stubAudioChargerOnly) Enabled() bool { return true }
func (stubAudioChargerOnly) ChargeRequestMultimodalWithMultiplier(ctx context.Context, tenantID, ownerUser, requestID, canonicalName string, usage maas.TokenUsage, rateMultiplier float64) (int64, float64, error) {
	return 0, 0, errors.New("stub")
}

// TestAudioSetMaasWiresTieredGate：SetMaas 自动接入分级预检；桩不接入。
func TestAudioSetMaasWiresTieredGate(t *testing.T) {
	svc := &AudioService{}
	svc.SetMaas(&maas.Service{})
	if svc.tieredBalanceGate == nil {
		t.Fatal("SetMaas must wire tieredBalanceGate when svc implements audioTieredBalanceGate")
	}

	svc2 := &AudioService{}
	svc2.SetMaas(stubAudioChargerOnly{})
	if svc2.tieredBalanceGate != nil {
		t.Fatal("SetMaas must NOT wire tieredBalanceGate for stub without the interface")
	}
}
