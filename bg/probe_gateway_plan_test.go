package bg

import (
	"context"
	"errors"
	"testing"
)

// ── 2026-11-26（第36轮 · gpt-image 探针风暴收口 · 第四处修正）────────────
//
// 211 真机实测坐实：第三处修正（凭据级判据）落地后，切流后仍然发出的 15 轮
// 503，网关自己的诊断给出的全是 **`plan_count=0`** —— 路由层对**整个模型**
// 就没有候选（provider_disabled / cred_quota / gates_open 三类归因）。
// ⇒ 钉住**哪一把**凭据都一样是 503 no_candidate ⇒ 网关轮零信息量。
//
// 本文件钉住第四道判据的四条性质：
//   ① 参数与请求**严格对齐**（模型名 / 租户 / 模态），不是猜的；
//   ② 只在直连轮失败时被问（直连成功时网关轮有确认「路由层看见恢复」的职责）；
//   ③ 只判否、不判未知：查不到 ⇒ 照跑网关轮；
//   ④ 查询出错 ⇒ 照跑网关轮（fail-open）。

// TestGatewayPlanModalityForMatchesTheRequest pins the modality the criterion
// asks with against the modality the gateway round's own request carries.
//
// ★ 期望值是**具体模型名 → 具体模态**（不是从被测函数反推的）：判据问错模态
// ⇒问到的候选集与请求的候选集不是同一个 ⇒ 「零候选」的结论不成立。
func TestGatewayPlanModalityForMatchesTheRequest(t *testing.T) {
	cases := []struct {
		name        string
		model       string
		rawModality string
		want        string
	}{
		{
			name:  "纯文本模型：探针发纯文本 body ⇒ 网关 detect 出 text",
			model: "kimi-k3",
			want:  "text",
		},
		{
			// 名字里没有模态匹配 ⇒ 落回 text（211 实测，AGENTS L120 轮块已登记）。
			name:  "gpt-image-2 落回 text（名字无模态匹配）",
			model: "gpt-image-2",
			want:  "text",
		},
		{
			// bge-m3 名字里没有 embedding 字样 ⇒ 只能靠权威模态（生产实测）。
			name:  "名字里没有 embedding 字样的模型不靠名字判 embedding",
			model: "bge-m3",
			want:  "text",
		},
		{
			// 2026-11-02 211 事故里的那个模型：探针对它发 /v1/embeddings。
			name:  "embedding 形状 ⇒ 探针发 /v1/embeddings ⇒ 那一面固定用 embedding",
			model: "qwen3-embedding:0.6b",
			want:  "embedding",
		},
		{
			name:        "DB 权威模态是 embedding ⇒ 同样落到 embeddings 端点",
			model:       "bge-m3",
			rawModality: "embedding",
			want:        "embedding",
		},
		{
			name:        "DB 权威模态是 audio ⇒ body 是音频体 ⇒ 网关 detect 出 audio",
			model:       "mimo-audio",
			rawModality: "audio",
			want:        "audio",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := gatewayPlanModalityFor(tc.model, tc.rawModality); got != tc.want {
				t.Fatalf("modality = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestGatewayPlanEmptyFailsOpen is the fail-open guard: a worker without a
// lookup, or a lookup that failed, must NOT claim "the plan is empty" —
// otherwise a DB blip would silently kill the gateway round.
func TestGatewayPlanEmptyFailsOpen(t *testing.T) {
	w := &NodeProbeWorker{}
	if w.gatewayPlanEmpty(context.Background(), "kimi-k3", "") {
		t.Fatal("a worker without a plan lookup must not claim an empty plan (fail-open)")
	}

	w.gatewayPlanLookup = func(context.Context, string, string, string) (int, error) {
		return 0, errors.New("boom")
	}
	if w.gatewayPlanEmpty(context.Background(), "kimi-k3", "") {
		t.Fatal("a failed lookup must not claim an empty plan (fail-open)")
	}
}

// TestGatewayPlanEmptyDetectsZeroCandidates pins the verdict itself: zero
// candidates ⇒ empty plan; any candidate ⇒ not empty.
func TestGatewayPlanEmptyDetectsZeroCandidates(t *testing.T) {
	w := &NodeProbeWorker{
		gatewayPlanLookup: func(context.Context, string, string, string) (int, error) { return 0, nil },
	}
	if !w.gatewayPlanEmpty(context.Background(), "kimi-k3", "") {
		t.Fatal("zero candidates must be reported as an empty plan")
	}

	w.gatewayPlanLookup = func(context.Context, string, string, string) (int, error) { return 3, nil }
	if w.gatewayPlanEmpty(context.Background(), "kimi-k3", "") {
		t.Fatal("a non-empty candidate set must not be reported as an empty plan")
	}
}

// TestGatewayPlanEmptyAsksWithRequestAlignedParams pins the parameters the
// criterion asks with: the same model, the request's tenant (default — the
// probe's gateway round goes out with the system key) and the request's
// modality.
func TestGatewayPlanEmptyAsksWithRequestAlignedParams(t *testing.T) {
	var gotModel, gotTenant, gotModality string
	w := &NodeProbeWorker{
		gatewayPlanLookup: func(_ context.Context, model, tenantID, modality string) (int, error) {
			gotModel, gotTenant, gotModality = model, tenantID, modality
			return 1, nil
		},
	}
	w.gatewayPlanEmpty(context.Background(), "kimi-k3", "")
	if gotModel != "kimi-k3" {
		t.Fatalf("model = %q, want the probed model", gotModel)
	}
	if gatewayPlanTenant != "default" {
		t.Fatalf("gatewayPlanTenant = %q, want default (the probe's gateway round uses the system key)", gatewayPlanTenant)
	}
	if gotTenant != "default" {
		t.Fatalf("tenant = %q, want default", gotTenant)
	}
	if gotModality != "text" {
		t.Fatalf("modality = %q, want text", gotModality)
	}
}

// TestProbeServiceSkipsGatewayRoundWhenTheModelHasNoCandidates is the
// service-level wiring check: with the routing layer reporting zero
// candidates, the pinned gateway round must NOT be issued (it could only be a
// 503 no_candidate) and the audit row must carry the new skip reason.
func TestProbeServiceSkipsGatewayRoundWhenTheModelHasNoCandidates(t *testing.T) {
	gatewayCalled := false
	recorder := &probeOutcomeRecorder{}
	svc := &ProbeService{
		worker: &NodeProbeWorker{
			gatewayPlanLookup: func(context.Context, string, string, string) (int, error) { return 0, nil },
		},
		directRoundFn: func(context.Context, int, string) nodeProbeRoundResult {
			return nodeProbeRoundResult{errCode: "http_502", httpStatus: 502}
		},
		gatewayRoundFn: func(context.Context, int, string) gatewayProbeResult {
			gatewayCalled = true
			return gatewayProbeResult{round: nodeProbeRoundResult{ok: true, httpStatus: 200}, pinned: true}
		},
		applyOutcomeFn: recorder.apply,
	}
	task := probeServiceTask(1)
	task.TenantID = "tenant-a"
	if _, err := svc.Run(context.Background(), task); err != nil {
		t.Fatalf("probe service run: %v", err)
	}
	if gatewayCalled {
		t.Fatal("the pinned gateway round must not run when the routing layer has no candidate at all")
	}
}

// TestProbeServiceKeepsGatewayRoundWhenTheModelStillHasCandidates is the
// discriminating arm of the test above: the criterion must NOT turn into a
// blanket "always skip" — a model that still has candidates keeps its gateway
// round.
func TestProbeServiceKeepsGatewayRoundWhenTheModelStillHasCandidates(t *testing.T) {
	gatewayCalled := false
	recorder := &probeOutcomeRecorder{}
	svc := &ProbeService{
		worker: &NodeProbeWorker{
			gatewayPlanLookup: func(context.Context, string, string, string) (int, error) { return 2, nil },
		},
		directRoundFn: func(context.Context, int, string) nodeProbeRoundResult {
			return nodeProbeRoundResult{errCode: "http_502", httpStatus: 502}
		},
		gatewayRoundFn: func(context.Context, int, string) gatewayProbeResult {
			gatewayCalled = true
			return gatewayProbeResult{round: nodeProbeRoundResult{ok: true, httpStatus: 200}, pinned: true}
		},
		applyOutcomeFn: recorder.apply,
	}
	task := probeServiceTask(1)
	task.TenantID = "tenant-a"
	if _, err := svc.Run(context.Background(), task); err != nil {
		t.Fatalf("probe service run: %v", err)
	}
	if !gatewayCalled {
		t.Fatal("the gateway round must still run while the model has candidates")
	}
}

// TestProbeServiceDoesNotAskThePlanWhenTheDirectRoundSucceeded pins the
// "only ask after a failed direct round" rule at the service level: the
// gateway round's job after a successful direct round is to confirm that the
// routing layer sees the recovered state — asking the plan first would kill
// that job.
func TestProbeServiceDoesNotAskThePlanWhenTheDirectRoundSucceeded(t *testing.T) {
	planAsked := 0
	recorder := &probeOutcomeRecorder{}
	svc := &ProbeService{
		worker: &NodeProbeWorker{
			gatewayPlanLookup: func(context.Context, string, string, string) (int, error) {
				planAsked++
				return 0, nil
			},
		},
		directRoundFn: func(context.Context, int, string) nodeProbeRoundResult {
			return nodeProbeRoundResult{ok: true, httpStatus: 200}
		},
		gatewayRoundFn: func(context.Context, int, string) gatewayProbeResult {
			return gatewayProbeResult{round: nodeProbeRoundResult{ok: true, httpStatus: 200}, pinned: true}
		},
		applyOutcomeFn: recorder.apply,
	}
	task := probeServiceTask(1)
	task.TenantID = "tenant-a"
	if _, err := svc.Run(context.Background(), task); err != nil {
		t.Fatalf("probe service run: %v", err)
	}
	if planAsked != 0 {
		t.Fatalf("the plan lookup must not be consulted after a successful direct round (asked %d times)", planAsked)
	}
}
