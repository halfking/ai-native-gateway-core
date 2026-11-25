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
// 这些 (凭据, 模型) 对在**路由层自己的候选面** `model_offers` 里读数全是
// available=false（7 个 node_probe_failed、3 个 binding_unavailable —— 这句是
// 第一版按资格视图读到的，已按事实订正：路由候选面是 model_offers，钉住方
// 99023 的 10 个 gpt-image 对里 8 个在 model_offers 上是停放态），而同批读数为
// true 的模型对不在残留名单里 ⇒ 判据有判别力。
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

// TestProbeGatewayRoundRoutableSQLUsesRoutingSources guards the **single
// source** of the routability judgement: the query must ask the routing
// layer's own candidate table (`model_offers` — the table the gateway's
// candidate_diag counts as `offers_total`) plus the credential-level gates
// (`v_routable_credential_models`), and must bind both the credential and the
// model.
//
// ★ Why `model_offers` and NOT only the view: 211 实测两表已漂移 —— 99023 的
// gpt-image-2.5-sunburst 在 `credential_model_bindings` 上 available=true、在
// `model_offers` 上 available=false/probe_http_429 ⇒ 只问视图会读成「可路由」
// ⇒ 第一版判据对实测残留**不咬**。
func TestProbeGatewayRoundRoutableSQLUsesRoutingSources(t *testing.T) {
	sql := probeGatewayRoundRoutableSQL
	for _, want := range []string{
		"model_offers",
		"v_routable_credential_models",
		"credential_id = $1",
		"raw_model_name = $2",
		"o.available IS TRUE",
		"unavailable_reason",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("the routability judgement is missing %q\ngot:\n%s", want, sql)
		}
	}
	// 凭据级排除原因用**前缀**判（provider_ / credential_ / lifecycle_ /
	// availability_ / quota_ / recent_probe_unreachable），不枚举状态码 ——
	// 枚举必然随视图演进而漂移。
	for _, want := range []string{"provider_", "credential_", "availability_", "quota_"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("the credential-level reason match is missing the %q prefix\ngot:\n%s", want, sql)
		}
	}
}

// TestNodeProbeWorkerGatewayRoundRoutableFailsOpen pins the failure direction:
// a lookup error must NOT silently disable the gateway round (that would trade
// a noisy request record for a lost diagnostic round).
//
// ★语义：查读桩返回的是「**可证明零候选**」（true ⇒ 该跳过网关轮），
// 工作层的包装把它翻成「可路由」（可路由 ⇒ 照跑网关轮）。
func TestNodeProbeWorkerGatewayRoundRoutableFailsOpen(t *testing.T) {
	w := &NodeProbeWorker{
		gatewayRoutableLookup: func(ctx context.Context, credID int, model string) (bool, error) {
			return true, errors.New("boom")
		},
	}
	if !w.gatewayRoundRoutable(context.Background(), 99023, "gpt-image-2") {
		t.Fatal("a failed lookup must report routable (fail-open) so the gateway round still runs")
	}

	w.gatewayRoutableLookup = func(ctx context.Context, credID int, model string) (bool, error) {
		return true, nil
	}
	if w.gatewayRoundRoutable(context.Background(), 99023, "gpt-image-2") {
		t.Fatal("a provably zero-candidate verdict must be reported as non-routable")
	}

	w.gatewayRoutableLookup = func(ctx context.Context, credID int, model string) (bool, error) {
		return false, nil
	}
	if !w.gatewayRoundRoutable(context.Background(), 99023, "gpt-image-2") {
		t.Fatal("a routable verdict must be reported as routable")
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
