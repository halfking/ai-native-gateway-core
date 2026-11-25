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
		planEmpty  bool
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
			name:     "direct ok ⇒ gateway round runs even when the model has zero candidates",
			direct:   nodeProbeRoundResult{ok: true, httpStatus: 200},
			routable: true,
			planEmpty: true,
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
		{
			// ★ 第36轮：模型级零候选（plan_count=0）⇒ 钉住哪一把凭据都一样是
			// 503 no_candidate ⇒ 零信息量 ⇒ 跳过。
			name:       "node family (502) + model has zero candidates ⇒ skip",
			direct:     nodeProbeRoundResult{errCode: "http_502", httpStatus: 502},
			routable:   true,
			planEmpty:  true,
			wantRun:    false,
			wantReason: "gateway_round_skipped_no_plan",
		},
		{
			name:       "rate limit (429) + model has zero candidates ⇒ skip",
			direct:     nodeProbeRoundResult{errCode: "http_429", httpStatus: 429},
			routable:   true,
			planEmpty:  true,
			wantRun:    false,
			wantReason: "gateway_round_skipped_no_plan",
		},
		{
			// 凭据级理由优先于模型级理由（便宜的判据先问，理由也先记它）。
			name:       "pair unroutable + model empty ⇒ the credential-level reason wins",
			direct:     nodeProbeRoundResult{errCode: "http_502", httpStatus: 502},
			routable:   false,
			planEmpty:  true,
			wantRun:    false,
			wantReason: "gateway_round_skipped_unroutable",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run, reason := probeGatewayRoundDecision(tc.direct, tc.routable, tc.planEmpty)
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
// layer's own candidate table (`model_offers` — the view the gateway's
// candidate_diag counts as `offers_total`) plus the routing layer's own gate
// (`v_routable_credential_models.is_routable` — the very column the routing
// candidate query filters on, provider/client.go:2068), and must bind both the
// credential and the model.
//
// ★ 2026-11-26 12h 审计轮订正（★ 前一版的注释把两张表当成可漂移的两张表，
// ★ 事实是 `model_offers` **本身就是视图**、`available` 就是 `cmb.available`
// ★ ⇒ 不存在漂移；★ 且路由层的候选查询用的**就是**资格视图的 `is_routable` ★）。
//
// ★★ 判据因此**直接读视图自己那一列**，而不是枚举视图的排除原因文本：★★
//	· 前一版用「凭据级前缀」匹配 `unavailable_reason` ⇒ **漏掉**绑定/模型级与
//	  探针级的四类理由（binding_unavailable / model_unavailable /
//	  model_manual_disabled / **node_probe_failed**）⇒ 其中 `node_probe_failed`
//	  正是上一版自己实测到的残留主因 ⇒ 判据对它不咬；
//	· 枚举 CASE 出来的文本必然随视图演进而漂移 ⇒ 改为读 `is_routable` 本列。
func TestProbeGatewayRoundRoutableSQLUsesRoutingSources(t *testing.T) {
	sql := probeGatewayRoundRoutableSQL
	for _, want := range []string{
		"model_offers",
		"v_routable_credential_models",
		"credential_id = $1",
		"raw_model_name = $2",
		"o.available IS TRUE",
		// ★ 判据读路由层自己的那一列（is_routable），不枚举排除原因文本。
		"is_routable",
		"NOT bool_or(v.is_routable)",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("the routability judgement is missing %q\ngot:\n%s", want, sql)
		}
	}
	// ★ 排除原因**不再**用前缀枚举（★ 那是漏掉 node_probe_failed 的根因 ★）：
	// 视图的排除原因是 CASE 出来的文本，★ 枚举它必然随视图演进而漂移 ★。
	for _, unwanted := range []string{
		"unavailable_reason ~",
		"provider_|credential_",
		"node_probe_failed",
	} {
		if strings.Contains(sql, unwanted) {
			t.Fatalf("the routability judgement must not enumerate exclusion reasons (%q)\ngot:\n%s", unwanted, sql)
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

// TestProbeGatewayRoundSkipDetailHasBothReasons keeps the skip reasons
// distinguishable in the audit trail — an operator reading node_probe_runs must
// be able to tell "the upstream already refused" from "the router had no
// candidate anyway" from "the model has no candidate at all".
func TestProbeGatewayRoundSkipDetailHasBothReasons(t *testing.T) {
	redundant := probeGatewayRoundSkipDetail("gateway_round_skipped_redundant")
	unroutable := probeGatewayRoundSkipDetail("gateway_round_skipped_unroutable")
	noPlan := probeGatewayRoundSkipDetail(gatewayRoundSkippedNoPlan)
	if redundant == "" || unroutable == "" || noPlan == "" {
		t.Fatal("every skip reason must carry an explanation")
	}
	if redundant == unroutable || unroutable == noPlan || redundant == noPlan {
		t.Fatal("the skip reasons must not read identically")
	}
	if !strings.Contains(unroutable, "no_candidate") {
		t.Fatalf("the unroutable reason must name the observed symptom, got %q", unroutable)
	}
	if !strings.Contains(noPlan, "no candidate at all") {
		t.Fatalf("the no-plan reason must say the model itself has no candidate, got %q", noPlan)
	}
}
