package bg

// probe_service_model_scope_bypass_test.go — T-P1（2026-11-08 探测空转根修）的门。
//
// 211 实测（2026-11-08）：提交侧豁免（probe_queue.go periodic/credential_recovery
// 绕过模型范围门）与消费侧（ProbeService.Run）不豁免不对称 ⇒ 泵按 periodic
// 入队 → Run 拒收 → settle success + 泵 45s holdoff ⇒ 每对每 45s 一条
// 「success 但零探测」的队列任务无限循环（24h 159,313 条任务、150,298 条
// success、~400 对 last_attempt 冻结 3 天）。
//
// 判据：
//
//	B1 periodic 源绕过消费侧范围门（豁免两端成对——本缺陷的回归门）；
//	B2 credential_recovery 源同样绕过；
//	B3 request_failure 源仍被范围门拒收（豁免闭集不放宽）+ 状态行被
//	   defer 到 15min 后重评（消费侧 pacing 兜底）；
//	B4 selfcheck 源同样仍被拒（Keep this set closed）。
//
// 红线对照：豁免只放行「天然带错误证据」的源（probe_model_scope.go 文件头），
// 常态巡检（INV-3）不重开。

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// recordingExecDB 捕获 scheduleExec 面的 Exec 调用（defer 断言用）。
type recordingExecDB struct {
	mu   sync.Mutex
	sqls []string
}

func (r *recordingExecDB) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sqls = append(r.sqls, sql)
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func (r *recordingExecDB) hasScopeDefer() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, sql := range r.sqls {
		if strings.Contains(sql, "UPDATE node_probe_state") && strings.Contains(sql, "next_retry_at = now() + $3::interval") {
			return true
		}
	}
	return false
}

func (r *recordingExecDB) execCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sqls)
}

// newScopeTestService 造一个带 modelScopeFn 桩与 defer 捕获的 ProbeService。
// direct/gateway 用双成功（若走到底 = 真实探测执行且恢复）。
func newScopeTestService(inScope bool) (*ProbeService, *probeOutcomeRecorder, *recordingExecDB) {
	execDB := &recordingExecDB{}
	recorder := &probeOutcomeRecorder{}
	svc := &ProbeService{
		worker: &NodeProbeWorker{stateExecDB: execDB},
		directRoundFn: func(context.Context, int, string) nodeProbeRoundResult {
			return nodeProbeRoundResult{ok: true, providerID: 7}
		},
		gatewayRoundFn: func(context.Context, int, string) gatewayProbeResult {
			return gatewayProbeResult{round: nodeProbeRoundResult{ok: true}, pinned: true}
		},
		applyOutcomeFn: recorder.apply,
		automaticEligibilityFn: func(context.Context, ProbeQueueTask) (bool, error) {
			return true, nil
		},
		modelScopeFn: func(context.Context, string) (bool, error) {
			return inScope, nil
		},
	}
	return svc, recorder, execDB
}

func scopeTestTask(source string) ProbeQueueTask {
	return ProbeQueueTask{
		CredentialID: 42,
		RawModel:     "cold-model-out-of-window",
		Automatic:    true,
		Source:       source,
		Attempt:      1,
	}
}

// B1：periodic 源绕过消费侧范围门 ⇒ 真实探测执行（不产生 out-of-scope 拒收）。
func TestProbeServicePeriodicBypassesModelScope(t *testing.T) {
	svc, recorder, _ := newScopeTestService(false)
	result, err := svc.Run(context.Background(), scopeTestTask("periodic"))
	if err != nil {
		t.Fatalf("periodic task must bypass model scope: %v", err)
	}
	if errors.Is(err, ErrProbeModelOutOfAutomaticScope) {
		t.Fatal("periodic task rejected by model scope = the 211 churn regression")
	}
	if result.ReasonCode == "automatic_probe_model_out_of_scope" {
		t.Fatalf("result = %+v, want probe to run", result)
	}
	if recorder.count() == 0 {
		t.Fatal("probe never executed — bypass must let Run reach the probe rounds")
	}
}

// B2：credential_recovery 源同样绕过。
func TestProbeServiceCredentialRecoveryBypassesModelScope(t *testing.T) {
	svc, recorder, _ := newScopeTestService(false)
	if _, err := svc.Run(context.Background(), scopeTestTask("credential_recovery")); err != nil {
		t.Fatalf("credential_recovery task must bypass model scope: %v", err)
	}
	if recorder.count() == 0 {
		t.Fatal("probe never executed for credential_recovery source")
	}
}

// B3：request_failure 源仍被范围门拒收（豁免闭集）+ 状态行 defer 15min。
func TestProbeServiceRequestFailureStillScopeGated(t *testing.T) {
	svc, recorder, execDB := newScopeTestService(false)
	result, err := svc.Run(context.Background(), scopeTestTask("request_failure"))
	if !errors.Is(err, ErrProbeModelOutOfAutomaticScope) {
		t.Fatalf("request_failure must stay scope-gated, got err=%v", err)
	}
	if result.ReasonCode != "automatic_probe_model_out_of_scope" {
		t.Fatalf("reason = %q want automatic_probe_model_out_of_scope", result.ReasonCode)
	}
	if recorder.count() != 0 {
		t.Fatal("gated task must not execute probe rounds")
	}
	// 消费侧 pacing 兜底：拒收的同时把状态行推到 15min 后（非 bypass 源
	// 理论上进不来——这是两端闸再漂移时的退化保护，仍必须有牙）。
	if !execDB.hasScopeDefer() {
		t.Fatalf("scope rejection must defer the state row (exec count = %d)", execDB.execCount())
	}
}

// B4：selfcheck 源同样仍被拒（豁免闭集不放宽——Keep this set closed）。
func TestProbeServiceSelfcheckStillScopeGated(t *testing.T) {
	svc, _, _ := newScopeTestService(false)
	if _, err := svc.Run(context.Background(), scopeTestTask("selfcheck")); !errors.Is(err, ErrProbeModelOutOfAutomaticScope) {
		t.Fatalf("selfcheck must stay scope-gated, got err=%v", err)
	}
}
