package bg

import (
	"context"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// ── 2026-11-26（gpt-image 探针风暴轮）────────────────────────────────────
//
// 三台真机实测的共同形状：gpt-image 家族（gpt-image-2 / -1 / -1.5 /
// -1-mini / -2.5-* / -2.6-* 以及中转商前缀变体 community*gpt-image-* /
// openaigpt-image-*）在 211/154/245 的网关日志里持续出现，3 天内 211 有
// 1462 条、154/245 有 214 条请求，全部 traffic_class='probe'（业务流量 0 条，
// 154 的 journal 里 http_request 命中 gpt-image 的条数 = 0）。
//
// 归因：这些请求不是客户端发的，是网关自己的探针/自检发的，而且其中约六成
// 是**必然失败**记录 —— 探针的「网关轮」（钉住同一凭据再问一次上游）在直连
// 轮已经判死之后仍照发。

// TestProbeGatewayRoundRedundantSkipsTerminalVerdicts pins the redundancy
// verdict: a direct-round failure whose verdict is a property of
// (credential, model, upstream) makes the pinned gateway round provably
// redundant, while a transient/node-class failure keeps it (the two rounds
// travel different network paths and can legitimately disagree).
func TestProbeGatewayRoundRedundantSkipsTerminalVerdicts(t *testing.T) {
	cases := []struct {
		name   string
		direct nodeProbeRoundResult
		want   bool
	}{
		{
			name:   "direct ok → gateway round still runs",
			direct: nodeProbeRoundResult{ok: true, httpStatus: 200},
			want:   false,
		},
		{
			name:   "auth family (401 invalid key) → redundant",
			direct: nodeProbeRoundResult{errCode: "http_401", httpStatus: 401},
			want:   true,
		},
		{
			name:   "quota family (402 insufficient balance) → redundant",
			direct: nodeProbeRoundResult{errCode: "http_402", httpStatus: 402},
			want:   true,
		},
		{
			name:   "rate limit (429) → gateway round kept (business rate-window calibration path)",
			direct: nodeProbeRoundResult{errCode: "http_429", httpStatus: 429},
			want:   false,
		},
		{
			name:   "protocol family (404 model not served) → redundant",
			direct: nodeProbeRoundResult{errCode: "http_404", httpStatus: 404, rootCause: ProbeRootCauseProtocol},
			want:   true,
		},
		{
			name:   "node family (502 upstream) → gateway round kept (different network path)",
			direct: nodeProbeRoundResult{errCode: "http_502", httpStatus: 502, rootCause: ProbeRootCauseNode},
			want:   false,
		},
		{
			name:   "network family (timeout) → gateway round kept",
			direct: nodeProbeRoundResult{errCode: "network_error", rootCause: ProbeRootCauseNode},
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := probeGatewayRoundRedundant(tc.direct); got != tc.want {
				t.Fatalf("probeGatewayRoundRedundant = %v, want %v (direct=%+v)", got, tc.want, tc.direct)
			}
		})
	}
}

// TestProbeGatewayRoundRedundantUsesSharedFamilyClassifiers proves the
// predicate rides the package's single-source family classifiers rather than a
// private copy of the error taxonomy.
func TestProbeGatewayRoundRedundantUsesSharedFamilyClassifiers(t *testing.T) {
	auth := nodeProbeRoundResult{errCode: "http_401", httpStatus: 401}
	if k := errorsx.ClassifyErrorWithBody(auth.httpStatus, nil); !isAuthProbeKind(k) {
		t.Fatalf("401 must classify into the auth family, got %q", k)
	}
	if !probeGatewayRoundRedundant(auth) {
		t.Fatal("auth family must be redundant")
	}
	quota := nodeProbeRoundResult{errCode: "http_402", httpStatus: 402}
	if k := probeOutcomeKind(quota); !isQuotaProbeKind(k) {
		t.Fatalf("402 must classify into the quota family, got %q", k)
	}
	if !probeGatewayRoundRedundant(quota) {
		t.Fatal("quota family must be redundant")
	}
	if k := probeOutcomeKind(nodeProbeRoundResult{errCode: "http_500", httpStatus: 500}); k == errorsx.KindAuth || isQuotaProbeKind(k) {
		t.Fatalf("500 must not land in a terminal family, got %q", k)
	}
}

// TestProbeServiceSkipsGatewayRoundOnTerminalDirectFailure is the behavioural
// half: a queued probe whose direct round came back with an auth-family
// failure must NOT issue the pinned gateway round. That round is what produced
// the "no routable credential/model available" request records in production.
//
// The direct round is stubbed; the gateway round is stubbed to record whether
// it was called.
func TestProbeServiceSkipsGatewayRoundOnTerminalDirectFailure(t *testing.T) {
	gatewayCalled := false
	service := newTestProbeService(
		nodeProbeRoundResult{errCode: "http_401", httpStatus: 401},
		gatewayProbeResult{round: nodeProbeRoundResult{ok: true, httpStatus: 200}, pinned: true},
		func(_ context.Context, _ probeOutcome) {},
	)
	service.gatewayRoundFn = func(context.Context, int, string) gatewayProbeResult {
		gatewayCalled = true
		return gatewayProbeResult{round: nodeProbeRoundResult{ok: true, httpStatus: 200}, pinned: true}
	}

	if _, err := service.Run(context.Background(), probeServiceTask(1)); err != nil {
		t.Fatalf("probe run failed: %v", err)
	}
	if gatewayCalled {
		t.Fatal("gateway round ran after a terminal direct failure — it can only repeat the same upstream refusal")
	}
}

// TestProbeServiceKeepsGatewayRoundOnTransientDirectFailure is the other half:
// a transient (node-class) direct failure keeps the gateway round, because the
// two rounds travel different network paths and their disagreement is the
// diagnostic signal.
func TestProbeServiceKeepsGatewayRoundOnTransientDirectFailure(t *testing.T) {
	gatewayCalled := false
	service := newTestProbeService(
		nodeProbeRoundResult{errCode: "http_502", httpStatus: 502, rootCause: ProbeRootCauseNode},
		gatewayProbeResult{round: nodeProbeRoundResult{ok: true, httpStatus: 200}, pinned: true},
		func(_ context.Context, _ probeOutcome) {},
	)
	service.gatewayRoundFn = func(context.Context, int, string) gatewayProbeResult {
		gatewayCalled = true
		return gatewayProbeResult{round: nodeProbeRoundResult{ok: true, httpStatus: 200}, pinned: true}
	}

	if _, err := service.Run(context.Background(), probeServiceTask(1)); err != nil {
		t.Fatalf("probe run failed: %v", err)
	}
	if !gatewayCalled {
		t.Fatal("gateway round was skipped on a transient direct failure — it must stay (different network path)")
	}
}

// TestAutomaticProbeEligibilityRejectsQuotaDeadCredentials pins the second
// half of the fix: a credential whose quota is permanently exhausted (or whose
// balance needs a top-up) must not be probed by the automatic probe path —
// its key is dead, every probe is another upstream refusal.
//
// The predicate is shared with bg/model_probe.go and bg/credential_probe_v2.go;
// the recovery path (BalanceQuotaProbe / probe-v2 fast reprobe) runs on its own
// channel and is deliberately NOT gated by this SQL.
func TestAutomaticProbeEligibilityRejectsQuotaDeadCredentials(t *testing.T) {
	q := automaticProbeEligibilitySQL()
	for _, want := range []string{
		"COALESCE(c.quota_state, 'ok') NOT IN ('permanently_exhausted', 'balance_exhausted')",
	} {
		if !strings.Contains(q, want) {
			t.Fatalf("automatic eligibility SQL missing %q\ngot:\n%s", want, q)
		}
	}
}
