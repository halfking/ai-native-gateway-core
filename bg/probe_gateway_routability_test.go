package bg

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// ── 2026-11-26（gpt-image 探针风暴轮 · 第三处修正）────────────────────────
//
// 211 真机实测坐实：残留的 gpt-image 失败请求（traffic_class='probe'、
// credential_id 为空、error_kind='no_candidate'）全部来自凭据 99023（freellm）
// 的 `credential_recovery` 复检 —— 直连轮 429/502 失败后，钉住同一凭据的网关轮
// 在路由层找不到候选、必然 503 no_candidate、永远触达不到上游。
//
// 这些 (凭据, 模型) 对在路由层自己的资格视图 `v_routable_credential_models`
// 里读数全是 is_routable=false（7 个 node_probe_failed、3 个
// binding_unavailable），而同批读数为 true 的 3 个模型对不在残留名单里 ⇒
// 视图有判别力。
//
// 本文件钉住这道新判据的三条性质：① 只在直连轮失败时生效；② 只判否、不判未知；
// ③ 查询出错 ⇒ 照跑网关轮（fail-open）。

// TestProbeGatewayRoundDecisionSkipsUnroutable pins the decision table of the
// pinned gateway round: the round runs whenever it can still reach upstream,
// and is skipped only when it provably cannot.
func TestProbeGatewayRoundDecisionSkipsUnroutable(t *testing.T) {
	cases := []struct {
		name       string
		direct     nodeProbeRoundResult
		routable   bool
		wantRun    bool
		wantReason string
	}{
		{
			name:     "direct ok ⇒ gateway round runs even if the view still says unroutable",
			direct:   nodeProbeRoundResult{ok: true, httpStatus: 200},
			routable: false,
			wantRun:  true,
		},
		{
			name:     "direct ok ⇒ gateway round runs (routable pair)",
			direct:   nodeProbeRoundResult{ok: true, httpStatus: 200},
			routable: true,
			wantRun:  true,
		},
		{
			name:       "auth family ⇒ redundant (unchanged first skip reason)",
			direct:     nodeProbeRoundResult{errCode: "http_401", httpStatus: 401},
			routable:   true,
			wantRun:    false,
			wantReason: "gateway_round_skipped_redundant",
		},
		{
			name:       "quota family ⇒ redundant",
			direct:     nodeProbeRoundResult{errCode: "http_402", httpStatus: 402},
			routable:   true,
			wantRun:    false,
			wantReason: "gateway_round_skipped_redundant",
		},
		{
			name:       "protocol family ⇒ redundant",
			direct:     nodeProbeRoundResult{errCode: "http_404", httpStatus: 404, rootCause: ProbeRootCauseProtocol},
			routable:   true,
			wantRun:    false,
			wantReason: "gateway_round_skipped_redundant",
		},
		{
			name: "node family (502) + pair unroutable ⇒ skip (zero information)",
			direct:     nodeProbeRoundResult{errCode: "http_502", httpStatus: 502},
			routable:   false,
			wantRun:    false,
			wantReason: "gateway_round_skipped_unroutable",
		},
		{
			name:       "rate limit (429) + pair unroutable ⇒ skip (zero information)",
			direct:     nodeProbeRoundResult{errCode: "http_429", httpStatus: 429},
			routable:   false,
			wantRun:    false,
			wantReason: "gateway_round_skipped_unroutable",
		},
		{
			name:     "rate limit (429) + pair routable ⇒ gateway round kept (rate-window calibration path)",
			direct:   nodeProbeRoundResult{errCode: "http_429", httpStatus: 429},
			routable: true,
			wantRun:  true,
		},
		{
			name:     "node family (502) + pair routable ⇒ gateway round kept (different network path)",
			direct:   nodeProbeRoundResult{errCode: "http_502", httpStatus: 502},
			routable: true,
			wantRun:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run, reason := probeGatewayRoundDecision(tc.direct, tc.routable)
			if run != tc.wantRun {
				t.Fatalf("run = %v, want %v (reason %q)", run, tc.wantRun, reason)
			}
			if !tc.wantRun && reason != tc.wantReason {
				t.Fatalf("skip reason = %q, want %q", reason, tc.wantReason)
			}
			if tc.wantRun && reason != "" {
				t.Fatalf("run path must not carry a skip reason, got %q", reason)
			}
		})
	}
}

// TestProbeGatewayRoundRoutableSQLUsesRoutingView guards the **single source**
// of the routability judgement: the query must ask the routing layer's own
// eligibility view, and must bind both the credential and the model. A copy of
// the view's conjunction pasted into Go would drift silently — this test is
// what makes that paste fail loudly instead.
func TestProbeGatewayRoundRoutableSQLUsesRoutingView(t *testing.T) {
	sql := probeGatewayRoundRoutableSQL
	if !strings.Contains(sql, "v_routable_credential_models") {
		t.Fatalf("the routability judgement must read the routing view, got:\n%s", sql)
	}
	if !strings.Contains(sql, "credential_id = $1") {
		t.Fatalf("the judgement must bind the credential, got:\n%s", sql)
	}
	if !strings.Contains(sql, "raw_model_name = $2") {
		t.Fatalf("the judgement must bind the model, got:\n%s", sql)
	}
	if !strings.Contains(sql, "is_routable") {
		t.Fatalf("the judgement must read the view's own is_routable verdict, got:\n%s", sql)
	}
}

// TestNodeProbeWorkerGatewayRoundRoutableFailsOpen pins the failure direction:
// a lookup error must NOT silently disable the gateway round (that would trade
// a noisy request record for a lost diagnostic round).
func TestNodeProbeWorkerGatewayRoundRoutableFailsOpen(t *testing.T) {
	w := &NodeProbeWorker{
		gatewayRoutableLookup: func(ctx context.Context, credID int, model string) (bool, error) {
			return false, errors.New("boom")
		},
	}
	if !w.gatewayRoundRoutable(context.Background(), 99023, "gpt-image-2") {
		t.Fatal("a failed lookup must report routable (fail-open) so the gateway round still runs")
	}

	w.gatewayRoutableLookup = func(ctx context.Context, credID int, model string) (bool, error) {
		return false, nil
	}
	if w.gatewayRoundRoutable(context.Background(), 99023, "gpt-image-2") {
		t.Fatal("an explicit non-routable verdict must be reported as non-routable")
	}
}

// TestNodeProbeWorkerGatewayRoundRoutableWithoutLookupFailsOpen is the
// regression guard for the first version of this helper: with neither a test
// seam nor a database, the zero value used to be read as "not routable" and
// the transient-failure gateway round was killed (caught by
// TestProbeServiceKeepsGatewayRoundOnTransientDirectFailure).
func TestNodeProbeWorkerGatewayRoundRoutableWithoutLookupFailsOpen(t *testing.T) {
	w := &NodeProbeWorker{}
	if !w.gatewayRoundRoutable(context.Background(), 99023, "gpt-image-2") {
		t.Fatal("a worker with no lookup capability must report routable (fail-open)")
	}
}

// TestProbeGatewayRoundSkipDetailHasBothReasons keeps the two skip reasons
// distinguishable in the audit trail — an operator reading node_probe_runs must
// be able to tell "the upstream already refused" from "the router had no
// candidate anyway".
func TestProbeGatewayRoundSkipDetailHasBothReasons(t *testing.T) {
	redundant := probeGatewayRoundSkipDetail("gateway_round_skipped_redundant")
	unroutable := probeGatewayRoundSkipDetail("gateway_round_skipped_unroutable")
	if redundant == "" || unroutable == "" {
		t.Fatal("both skip reasons must carry an explanation")
	}
	if redundant == unroutable {
		t.Fatal("the two skip reasons must not read identically")
	}
	if !strings.Contains(unroutable, "no_candidate") {
		t.Fatalf("the unroutable reason must name the observed symptom, got %q", unroutable)
	}
}
