package executors

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/domains/nodehealth"
	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// P0-4 接线判据：ApplyNodeHealthDecision 的 EffectRecordCircuitFailure 臂
// 按错误正文分派三种记录方式——模型指名体跳过凭据熔断、quota 重置体对齐
// 冷却、其余走既有 billing-mode 记录。断言与分派逻辑同层（直接驱动
// adapter），并配「前提在场」断言（解析器确实吃下同一 detail 文本）。

type nodeHealthStateSpy struct {
	bindingFailures    []credential.Failure
	credentialFailures []credential.Failure
}

func (s *nodeHealthStateSpy) Enabled() bool { return true }

func (s *nodeHealthStateSpy) RestoreOnSuccess(context.Context, int, string) error { return nil }

func (s *nodeHealthStateSpy) WriteOnError(_ context.Context, _ int, _ string, failure credential.Failure) error {
	s.bindingFailures = append(s.bindingFailures, failure)
	return nil
}

func (s *nodeHealthStateSpy) SetCredentialUnavailable(_ context.Context, _ int, failure credential.Failure) error {
	s.credentialFailures = append(s.credentialFailures, failure)
	return nil
}

func p04CircuitDecision(kind nodehealth.ErrorKind, detail string, effects ...nodehealth.EffectKind) nodehealth.Decision {
	if len(effects) == 0 {
		effects = []nodehealth.EffectKind{nodehealth.EffectRecordCircuitFailure}
	}
	effs := make([]nodehealth.Effect, 0, len(effects))
	for _, k := range effects {
		effs = append(effs, nodehealth.Effect{Kind: k})
	}
	return nodehealth.Decision{
		Node:        nodehealth.NodeKey{TenantID: "default", ProviderID: 9, CredentialID: 31, Model: "vendor-m"},
		AttemptID:   "p04-attempt",
		Phase:       nodehealth.PhaseRequest,
		Outcome:     requestjourney.OutcomeFailure,
		ErrorKind:   kind,
		HTTPStatus:  429,
		RequestID:   "p04-req",
		BillingMode: "paid",
		ErrorDetail: detail,
		Effects:     effs,
	}
}

func TestP04Wiring_QuotaResetAlignsCircuitCooling(t *testing.T) {
	// 前提在场：GradeQuotaRest 必须吃下这条 detail（绝对时刻 +10min）。
	detail := `{"error":{"message":"rate limited, will reset at ` + time.Now().Add(10*time.Minute).UTC().Format(time.RFC3339) + `"}}`
	if hint := errorsx.GradeQuotaRest([]byte(detail), time.Now()); !hint.HasReset {
		t.Fatalf("premise failed: GradeQuotaRest did not accept the detail body (class=%s)", hint.Class)
	}

	exec := &Executor{Circuit: credential.NewManager()}
	adapter := executorNodeHealthAdapter{executor: exec}
	if err := adapter.ApplyNodeHealthDecision(context.Background(),
		p04CircuitDecision(nodehealth.ErrorKindQuota, detail)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	brk := exec.Circuit.Get(9, 31)
	if brk == nil {
		t.Fatal("breaker not created")
	}
	if brk.State() != credential.StateOpen {
		t.Fatalf("state = %s, want open (single quota failure aligned to reset)", brk.State())
	}
	stats := brk.Stats()
	expires, _ := stats["cooling_expires"].(string)
	if expires == "" {
		t.Fatal("cooling_expires missing on aligned open")
	}
	expAt, err := time.Parse(time.RFC3339, expires)
	if err != nil {
		t.Fatalf("cooling_expires unparseable: %v", err)
	}
	lead := time.Until(expAt)
	if lead < 8*time.Minute || lead > 11*time.Minute {
		t.Fatalf("cooling lead = %v, want ~10min (aligned to reset, not the exponential ladder)", lead)
	}
}

func TestP04Wiring_ModelScopedRejectionNarrowsFullReducerDecision(t *testing.T) {
	// 既知登记项形态：正文指名模型，但分类落在非 binding 档（upstream/transient）。
	detail := `{"error":{"message":"The model 'glm-5.2-flash' does not exist or is not available","type":"invalid_request_error"}}`
	// 前提在场：提取器必须从这条 detail 里拿到模型名。
	model, ok := errorsx.ModelScopedRejection([]byte(detail))
	if !ok || model != "glm-5.2-flash" {
		t.Fatalf("premise failed: ModelScopedRejection = (%q,%v), want (glm-5.2-flash,true)", model, ok)
	}

	observer := &flashBlipObserver{mockStateObserver: &mockStateObserver{}}
	state := &nodeHealthStateSpy{}
	exec := &Executor{
		Circuit:         credential.NewManager(),
		StateObserver:   observer,
		NodeHealthState: state,
	}
	adapter := executorNodeHealthAdapter{executor: exec}
	reducer := nodehealth.NewOutcomeReducer()
	node := nodehealth.NodeKey{TenantID: "default", ProviderID: 9, CredentialID: 31, Model: "glm-5.2-flash"}

	var decision nodehealth.Decision
	for i := 1; i <= 3; i++ {
		observation := nodehealth.Observation{
			Node:        node,
			AttemptID:   fmt.Sprintf("p04-model-scope-%d", i),
			Phase:       nodehealth.PhaseRequest,
			Outcome:     requestjourney.OutcomeFailure,
			ErrorKind:   nodehealth.ErrorKindUpstream,
			HTTPStatus:  400,
			RequestID:   fmt.Sprintf("p04-model-scope-req-%d", i),
			BillingMode: "paid",
			ErrorDetail: detail,
		}
		var err error
		decision, err = reducer.ReduceAndApply(context.Background(), observation, adapter)
		if err != nil {
			t.Fatalf("ReduceAndApply failure %d: %v", i, err)
		}
	}

	for _, want := range []nodehealth.EffectKind{
		nodehealth.EffectRecordCircuitFailure,
		nodehealth.EffectSetBindingUnavailable,
		nodehealth.EffectSetCredentialUnavailable,
		nodehealth.EffectUpdateURSM,
		nodehealth.EffectInvalidateCandidateCache,
		nodehealth.EffectScheduleProbe,
	} {
		found := false
		for _, effect := range decision.Effects {
			if effect.Kind == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("third reducer decision missing premise effect %s: %+v", want, decision.Effects)
		}
	}

	// The full-effect filter must skip breaker creation entirely; no
	// credential-wide side effect is allowed to observe this model rejection.
	if brk := exec.Circuit.Get(9, 31); brk != nil {
		t.Fatalf("breaker = %+v, want nil (model-scoped must not touch credential breaker)", brk.Stats())
	}
	if failures := observer.getFailures(); len(failures) != 0 {
		t.Fatalf("legacy UpdateOnFailure calls = %+v, want none for model-scoped override", failures)
	}
	if got := len(state.bindingFailures); got != 1 {
		t.Fatalf("binding-unavailable writes = %d, want 1", got)
	}
	if got := len(state.credentialFailures); got != 0 {
		t.Fatalf("credential-unavailable writes = %d, want 0", got)
	}
}

func TestP04Wiring_QuotaWithoutResetKeepsLegacyRecording(t *testing.T) {
	// quota 族但正文无任何重置信号 → 既有逐字节行为（billing-mode 记录，不触发单次 OPEN 对齐）。
	detail := `{"error":{"message":"request throttled, please retry later"}}`
	if hint := errorsx.GradeQuotaRest([]byte(detail), time.Now()); hint.HasReset {
		t.Fatalf("premise failed: throttle boilerplate must not parse as a reset signal (class=%s)", hint.Class)
	}

	exec := &Executor{Circuit: credential.NewManager()}
	adapter := executorNodeHealthAdapter{executor: exec}
	if err := adapter.ApplyNodeHealthDecision(context.Background(),
		p04CircuitDecision(nodehealth.ErrorKindQuota, detail)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	brk := exec.Circuit.Get(9, 31)
	if brk == nil {
		t.Fatal("breaker not created")
	}
	// 单次非永久 quota 失败在旧语义下只累计、不开门（阈值 3）。
	if brk.State() != credential.StateClosed {
		t.Fatalf("state = %s, want closed (legacy single-failure behaviour preserved)", brk.State())
	}
	if n := brk.ConsecutiveFailures(); n != 1 {
		t.Fatalf("consecutive = %d, want 1 (legacy accumulation)", n)
	}
}

func TestP04Wiring_NonQuotaUnaffected(t *testing.T) {
	// 非 quota、非模型指名的普通失败 → billing-mode 记录路径原样（守卫不误伤）。
	detail := "connection reset by peer"
	if _, ok := errorsx.ModelScopedRejection([]byte(detail)); ok {
		t.Fatal("premise failed: plain network detail misread as model-scoped")
	}
	if errorsx.IsQuotaExhaustion(errorsx.KindTransient) {
		t.Fatal("premise failed: KindTransient misread as quota exhaustion")
	}

	exec := &Executor{Circuit: credential.NewManager()}
	adapter := executorNodeHealthAdapter{executor: exec}
	if err := adapter.ApplyNodeHealthDecision(context.Background(),
		p04CircuitDecision(nodehealth.ErrorKindUpstream, strings.Repeat("x", 8))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	brk := exec.Circuit.Get(9, 31)
	if brk == nil || brk.ConsecutiveFailures() != 1 {
		t.Fatalf("legacy recording broken: breaker=%v consecutive=%d", brk != nil, func() int {
			if brk == nil {
				return -1
			}
			return brk.ConsecutiveFailures()
		}())
	}
}
