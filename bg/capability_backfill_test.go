// capability_backfill_test.go — 能力位回填任务的判定逻辑（2026-10-02）。
//
// 这组测试对着**真实上游帧**跑**真实探测器**：httptest 返回的是实测录下的
//
//	vapEUR / codex 响应，分类走的是 probe_http.go 里同一个 singleResponsesPing。
//
// 只有两个 DB 触点（scan / persist）被注入——换掉它们之后仍然测的是
// 「真实帧 → 真实判定 → 写不写、写什么」这条链。
//
// 判据自检（对本轮要求的回应）：每条「不该写」的用例都配一条正样本，
// 见 TestBackfillProbePreconditionSelfCheck —— 先喂已知失败样本确认能判红，
// 再喂正常样本确认不误报。
package bg

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/admin/distlock"
	"github.com/kaixuan/llm-gateway-go/internal/providercap"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// 实测上游帧（2026-09-28 / 2026-10-02 直连 api.vapeur.ai 录制）。
//
// 该供应商不支持 Responses API —— claude 三兄弟的实际拒绝形状。
const realVapeurResponsesUnsupportedBody = `{"error":{"message":"该供应商不支持 Responses API","type":"invalid_request_error","code":"unsupported_operation"}}`

// gpt-5.3-codex 非流式 /v1/responses 200，录自实测。
const realCodexResponsesOKBody = `{
  "id": "resp_0c7d53a46ec052b6006abf2cebb26c8193b9191a8c45d79b35",
  "object": "response",
  "created_at": 1790913771,
  "status": "completed",
  "model": "gpt-5.3-codex",
  "output": [
    {
      "id": "msg_0c7d53a46ec052b6006abf2cec55fc819396019c39134b0cef",
      "type": "message",
      "status": "completed",
      "content": [{"type": "output_text", "annotations": [], "logprobs": [], "text": "Hi!"}],
      "phase": "final_answer",
      "role": "assistant"
    }
  ],
  "usage": {"input_tokens": 8, "output_tokens": 8, "total_tokens": 16},
  "incomplete_details": null
}`

type backfillWrite struct {
	BindingID int64
	RawModel  string
	Supported bool
	Evidence  []byte
}

// recordedSink 实现 ResponsesCapabilitySink，记录镜像写。
type recordedSink struct {
	mu   sync.Mutex
	hits []bool
}

func (r *recordedSink) SetSupportsResponses(_ context.Context, _ int, _ string, supported bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hits = append(r.hits, supported)
	return nil
}

// admitAll 起一条**所有闸门都通过**的绑定，再交给 mutate 改需要的字段。
//
// 显式写出每一个闸门字段（而不是靠零值），这样「某条闸门被误设成 false」
// 会立刻变成一条红，而不是让整组用例静默地什么都不做。
func admitAll(mutate func(*dueBinding)) dueBinding {
	d := dueBinding{
		CredentialStatus:   "active",
		LifecycleStatus:    "active",
		CredentialDisabled: false,
		ProviderEnabled:    true,
		ProviderDisabled:   false,
		BindingAvailable:   true,
	}
	if mutate != nil {
		mutate(&d)
	}
	return d
}

type backfillHarness struct {
	backfill   *CapabilityBackfill
	writes     *[]backfillWrite
	sink       *recordedSink
	ciphertext []byte
	baseURL    string
}

func newBackfillHarness(t *testing.T, handler http.HandlerFunc, protocol string) *backfillHarness {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	// 真实凭据信封：解密路径也走真的（secret.EncryptAESGCM → DecryptAny），
	// 不给 decrypt 开后门。
	var key [32]byte
	copy(key[:], "capability-backfill-test-32byte")
	kr, err := secret.NewKeyring(map[string][32]byte{"k1": key}, "k1")
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	envelope, err := secret.EncryptAESGCM([]byte("sk-real-test-key"), kr)
	if err != nil {
		t.Fatalf("EncryptAESGCM: %v", err)
	}

	writes := &[]backfillWrite{}
	sink := &recordedSink{}
	real := NewCapabilityBackfill(nil, nil, kr, sink)
	b := &CapabilityBackfill{
		encKey:  nil,
		keyring: kr,
		sink:    sink,
		// 与生产同参：batchLimit=0 会让探测循环第一行就 break（R33 审计
		// 教训——补丁后 -run 模式换成 TestBackfill* 才暴露这组假绿）。
		batchLimit: capabilityBackfillBatchLimit,
		staleAfter: capabilityBackfillStaleAfter,
		// 真实探测器：只换 DB 两个触点。
		probe: real.probe,
		scan: func(context.Context) ([]dueBinding, error) {
			return []dueBinding{admitAll(func(d *dueBinding) {
				d.BindingID = 4242
				d.CredentialID = 126
				d.Ciphertext = []byte(envelope)
				d.OutboundModel = "gpt-5.3-codex"
				d.RawModel = "gpt-5.3-codex"
				d.BaseURL = srv.URL
				d.Protocol = protocol
				d.CatalogCode = "vapeur"
			})}, nil
		},
		persist: func(_ context.Context, row dueBinding, supported bool, evidence []byte) error {
			*writes = append(*writes, backfillWrite{
				BindingID: row.BindingID, RawModel: row.RawModel,
				Supported: supported, Evidence: evidence,
			})
			return nil
		},
	}
	return &backfillHarness{backfill: b, writes: writes, sink: sink, ciphertext: []byte(envelope), baseURL: srv.URL}
}

func (h *backfillHarness) run(t *testing.T) int {
	t.Helper()
	n, err := h.backfill.BackfillOnce(context.Background())
	if err != nil {
		t.Fatalf("BackfillOnce: %v", err)
	}
	return n
}

// 每条闸门一条用例。
//
// 这组是本轮审计补上的覆盖漏洞：原先这些条件**只写在 SQL 里**，一条都测不到。
// 2026-10-02 审计靠人工比对才发现 `c.status IN ('active','cooling','degraded')`
// 被漏抄——而当时没有任何一条用例会红。删掉任意一条闸门，本组立刻红。
func TestCapabilityBackfillAdmissionGates(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(*dueBinding)
		wantAdmit bool
		wantWhy   string
	}{
		{name: "healthy binding is admitted", mutate: func(d *dueBinding) { d.Protocol = "openai-responses" }, wantAdmit: true},
		{name: "soft-deleted credential must never be probed", mutate: func(d *dueBinding) { d.CredentialStatus = "deleted" }, wantWhy: "credential_status_deleted"},
		{name: "quarantined credential is not probed", mutate: func(d *dueBinding) { d.CredentialStatus = "quarantine" }, wantWhy: "credential_status_quarantine"},
		{name: "quota-expired credential is not probed", mutate: func(d *dueBinding) { d.CredentialStatus = "quota_expired" }, wantWhy: "credential_status_quota_expired"},
		{name: "disabled credential is not probed", mutate: func(d *dueBinding) { d.CredentialStatus = "disabled" }, wantWhy: "credential_status_disabled"},
		{name: "cooling is still probed (node_probe treats it as serving)", mutate: func(d *dueBinding) { d.CredentialStatus = "cooling"; d.Protocol = "openai-responses" }, wantAdmit: true},
		{name: "degraded is still probed (node_probe treats it as serving)", mutate: func(d *dueBinding) { d.CredentialStatus = "degraded"; d.Protocol = "openai-responses" }, wantAdmit: true},
		{name: "retired lifecycle is not probed", mutate: func(d *dueBinding) { d.LifecycleStatus = "retired" }, wantWhy: "lifecycle_retired"},
		{name: "manually disabled credential is not probed", mutate: func(d *dueBinding) { d.CredentialDisabled = true }, wantWhy: "credential_manual_disabled"},
		{name: "manually disabled provider is not probed", mutate: func(d *dueBinding) { d.ProviderDisabled = true }, wantWhy: "provider_manual_disabled"},
		{name: "disabled provider is not probed", mutate: func(d *dueBinding) { d.ProviderEnabled = false }, wantWhy: "provider_disabled"},
		{name: "unavailable binding is not probed", mutate: func(d *dueBinding) { d.BindingAvailable = false }, wantWhy: "binding_unavailable"},
		{name: "non-responses protocol is not probed", mutate: func(d *dueBinding) { d.Protocol = "openai-completions" }, wantWhy: "protocol_openai-completions"},
		{name: "anthropic protocol is not probed", mutate: func(d *dueBinding) { d.Protocol = "anthropic-messages" }, wantWhy: "protocol_anthropic-messages"},
		{name: "unparseable protocol is not probed", mutate: func(d *dueBinding) { d.Protocol = "totally-not-a-protocol" }, wantWhy: "protocol_unparseable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mutate := tc.mutate
			if mutate == nil {
				mutate = func(*dueBinding) {}
			}
			admit, why := capabilityBackfillAdmit(admitAll(mutate))
			if admit != tc.wantAdmit {
				t.Fatalf("capabilityBackfillAdmit() admit = %v, want %v (reason=%q)", admit, tc.wantAdmit, why)
			}
			if tc.wantWhy != "" && why != tc.wantWhy {
				t.Fatalf("skip reason = %q, want %q", why, tc.wantWhy)
			}
		})
	}
}

// 端到端：软删除的凭据在**解密与出网之前**就被挡住。
//
// 变异验证：把 capabilityBackfillAdmit 短路为恒 true → 本用例红，且红的形态是
// 「persist 被调用」而不是「返回了错误」——闸门失效长那样。
func TestBackfillDoesNotProbeSoftDeletedCredential(t *testing.T) {
	var probed bool
	h := newBackfillHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		probed = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(realCodexResponsesOKBody))
	}, "openai-responses")
	h.backfill.scan = func(context.Context) ([]dueBinding, error) {
		return []dueBinding{admitAll(func(d *dueBinding) {
			d.BindingID = 9999
			d.CredentialID = 126
			d.Ciphertext = h.ciphertext
			d.RawModel = "gpt-5.3-codex"
			d.OutboundModel = "gpt-5.3-codex"
			d.BaseURL = h.baseURL
			d.Protocol = "openai-responses"
			d.CatalogCode = "vapeur"
			d.CredentialStatus = "deleted"
		})}, nil
	}

	if n := h.run(t); n != 0 {
		t.Fatalf("written = %d, want 0 for a soft-deleted credential", n)
	}
	if probed {
		t.Fatal("a soft-deleted credential was probed upstream")
	}
	if len(*h.writes) != 0 {
		t.Fatalf("persist called for a soft-deleted credential: %+v", *h.writes)
	}
}

// 真实 vapEUR 200 帧 ⇒ 写 supported=true，证据里带真实响应原文。
func TestBackfillWritesPositiveFromRealResponsesFrame(t *testing.T) {
	h := newBackfillHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(realCodexResponsesOKBody))
	}, "openai-responses")

	if n := h.run(t); n != 1 {
		t.Fatalf("written = %d, want 1", n)
	}
	if len(*h.writes) != 1 {
		t.Fatalf("writes = %d, want 1", len(*h.writes))
	}
	w := (*h.writes)[0]
	if !w.Supported {
		t.Fatalf("supported = false, want true (real 200 responses frame is positive evidence)")
	}
	var ev map[string]any
	if err := json.Unmarshal(w.Evidence, &ev); err != nil {
		t.Fatalf("evidence is not JSON: %v (%s)", err, w.Evidence)
	}
	if got := ev["probe_mode"]; got != "responses_nonstream" {
		t.Fatalf("evidence probe_mode = %v, want responses_nonstream", got)
	}
	if got, _ := ev["http_status"].(float64); got != 200 {
		t.Fatalf("evidence http_status = %v, want 200", ev["http_status"])
	}
	// 证据必须是探测器拿到的真实上游帧，不是本任务拼的形状。
	sample, _ := ev["body_sample"].(string)
	if !strings.Contains(sample, "resp_0c7d53a46ec052b") {
		t.Fatalf("evidence body_sample does not carry the real upstream frame: %q", sample)
	}
	// Redis 镜像必须与 SQL 结论同向。
	if len(h.sink.hits) != 1 || !h.sink.hits[0] {
		t.Fatalf("redis mirror = %v, want [true]", h.sink.hits)
	}
}

// 真实 vapEUR 400「该供应商不支持 Responses API」⇒ 写 supported=false。
func TestBackfillWritesNegativeFromRealUnsupportedFrame(t *testing.T) {
	h := newBackfillHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(realVapeurResponsesUnsupportedBody))
	}, "openai-responses")

	if n := h.run(t); n != 1 {
		t.Fatalf("written = %d, want 1", n)
	}
	w := (*h.writes)[0]
	if w.Supported {
		t.Fatalf("supported = true, want false (real 400 unsupported frame is negative evidence)")
	}
	if len(h.sink.hits) != 1 || h.sink.hits[0] {
		t.Fatalf("redis mirror = %v, want [false]", h.sink.hits)
	}
}

// 无证据一律不写。把「没探到」写成 supported=false 就等于用默认值关掉一个
// 可能完全正常的绑定——那正是这张表最初腐烂的形状。
//
// 变异验证：删掉 result.supportsResponses == nil 那个 return，本用例红。
func TestBackfillLeavesRowUntouchedWithoutEvidence(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "upstream 5xx",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"message":"upstream_down"}}`))
			},
		},
		{
			name: "auth failure is not a capability verdict",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
			},
		},
		{
			name: "400 that is a request-shape rejection, not a capability gap",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"Unsupported parameter: 'messages' on responses api"}}`))
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newBackfillHarness(t, tc.handler, "openai-responses")
			if n := h.run(t); n != 0 {
				t.Fatalf("written = %d, want 0", n)
			}
			if len(*h.writes) != 0 {
				t.Fatalf("persist called %d times without evidence: %+v", len(*h.writes), *h.writes)
			}
			if len(h.sink.hits) != 0 {
				t.Fatalf("redis mirror written without evidence: %v", h.sink.hits)
			}
		})
	}
}

// 只有 openai-responses 协议的绑定才该被探。别的协议没有 /v1/responses。
//
// 变异验证：删掉协议闸门，本用例红（chat 绑定会被写一条 supported=false）。
func TestBackfillSkipsNonResponsesProtocols(t *testing.T) {
	var probed bool
	h := newBackfillHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		probed = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(realCodexResponsesOKBody))
	}, "openai-completions")
	h.backfill.probe = func(context.Context, probeTarget, providercap.Descriptor) httpProbeResult {
		probed = true
		return httpProbeResult{status: "ok", httpStatus: 200, supportsResponses: boolEvidence(true)}
	}

	if n := h.run(t); n != 0 {
		t.Fatalf("written = %d, want 0 for a chat-protocol binding", n)
	}
	if probed {
		t.Fatal("a non-responses binding was probed; the protocol gate was bypassed")
	}
	if len(*h.writes) != 0 {
		t.Fatalf("persist called for a non-responses binding: %+v", *h.writes)
	}
}

// 2026-09-23 vapeur 事故里的确切错拼「openai-response」（单数）必须被归一后
// 仍然走 responses 路径。
func TestBackfillNormalizesSingularResponsesAlias(t *testing.T) {
	h := newBackfillHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(realCodexResponsesOKBody))
	}, "openai-response")

	if n := h.run(t); n != 1 {
		t.Fatalf("written = %d, want 1 (the singular alias is the exact mis-spelling from the 2026-09-23 incident)", n)
	}
	if len(*h.writes) != 1 || !(*h.writes)[0].Supported {
		t.Fatalf("writes = %+v, want one positive write", *h.writes)
	}
}

// 判据自检：把已知判错的样本先喂进来，确认探测器真的会判红（不会一律返回
// nil 或一律返回 true），再确认正常样本不误报。
//
// 这条是本轮要求的「判据自检」在探测器层的落点：如果 singleResponsesPing
// 对所有输入都返回 nil，上面的「无证据不写」三条会全绿而毫无意义；如果它对
// 所有输入都返回 true，「写 negative」会全绿而毫无意义。
func TestBackfillProbePreconditionSelfCheck(t *testing.T) {
	// 已知失败样本：真实 400 不支持帧 ⇒ 必须判出 false，不能是 nil。
	hNeg := newBackfillHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(realVapeurResponsesUnsupportedBody))
	}, "openai-responses")
	hNeg.run(t)
	if len(*hNeg.writes) != 1 || (*hNeg.writes)[0].Supported {
		t.Fatalf("判据自检失败：已知失败样本没有判红，writes=%+v", *hNeg.writes)
	}
	// 正常样本：真实 200 帧 ⇒ 必须判出 true，不能是 nil。
	hOK := newBackfillHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(realCodexResponsesOKBody))
	}, "openai-responses")
	hOK.run(t)
	if len(*hOK.writes) != 1 || !(*hOK.writes)[0].Supported {
		t.Fatalf("判据自检失败：正常样本误报，writes=%+v", *hOK.writes)
	}
}

// 探针的 max_output_tokens 是 32（providercap.ResponsesProbeMaxOutputTokens）。
// 推理型模型（glm-5.2 等）会先出 reasoning_content，32 个 token 常常被吃光，
// 表现为 **HTTP 200 + finish_reason:"length" + 空 output**。
//
// 第三轮审计已经记过这件事：任何用小 token 上限做**内容**断言的探测/回归会
// 产生假失败。本任务问的是另一个问题——「这个供应商的 /v1/responses 通不通」，
// 判据是状态码而不是有没有出字。所以 length 必须**仍然判正**。
//
// 变异验证：若有人给判定加上「必须有 output_text」之类的内容门控，本用例红，
// 且红的形态正是第三轮那个假失败。
func TestBackfillReasoningModelLengthTruncationStillCountsAsSupported(t *testing.T) {
	h := newBackfillHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// glm-5.2 形状：token 预算被 reasoning 吃光，200 但没有 output。
		_, _ = w.Write([]byte(`{
  "id": "resp_reasoning_budget_exhausted",
  "object": "response",
  "status": "incomplete",
  "model": "glm-5.2",
  "output": [],
  "usage": {"input_tokens": 8, "output_tokens": 32, "total_tokens": 40},
  "incomplete_details": {"reason": "max_output_tokens"}
}`))
	}, "openai-responses")

	if n := h.run(t); n != 1 {
		t.Fatalf("written = %d, want 1: a 200 with finish_reason=length is still a working /v1/responses endpoint", n)
	}
	if len(*h.writes) != 1 || !(*h.writes)[0].Supported {
		t.Fatalf("writes = %+v, want one positive write — a token-truncated 200 must not read as a capability gap", *h.writes)
	}
	// 真实帧仍要进证据：这条正是将来排查"为什么这条能力位是这么写的"时唯一的东西。
	var ev map[string]any
	if err := json.Unmarshal((*h.writes)[0].Evidence, &ev); err != nil {
		t.Fatalf("evidence is not JSON: %v", err)
	}
	sample, _ := ev["body_sample"].(string)
	if !strings.Contains(sample, "resp_reasoning_budget_exhausted") {
		t.Fatalf("evidence body_sample lost the real frame: %q", sample)
	}
}

// 流式能力键不在本任务的写入面内。探针只发一次非流式请求，对 SSE 没有证据。
//
// 判据钉在**字符串字面量**上，不钉在源码子串上：本文件的注释里就写着
// 「native_responses_stream 刻意不在此处出现」，子串门会被这句话自己喂饱
// 然后报一个假的红。ast.Inspect 走的是解析后的字面量，注释不在其中。
//
// 变异验证：若有人把 CapabilityNonstream 改成流式键、或在 persist 里多写一行
// native_responses_stream，本用例红。
func TestBackfillNeverWritesStreamCapability(t *testing.T) {
	if CapabilityNonstream != "native_responses_nonstream" {
		t.Fatalf("CapabilityNonstream = %q, want native_responses_nonstream", CapabilityNonstream)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "capability_backfill.go", nil, 0)
	if err != nil {
		t.Fatalf("parse capability_backfill.go: %v", err)
	}
	var offenders []string
	//nolint:staticcheck // 走文件内相对路径即包内源码，bg 包测试惯例
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if strings.Contains(lit.Value, "native_responses_stream") {
			offenders = append(offenders, lit.Value)
		}
		return true
	})
	if len(offenders) > 0 {
		t.Fatalf("capability_backfill.go has string literals naming the stream capability key %v; "+
			"the probe has no streaming evidence and must not write that leg", offenders)
	}
}

// kill switch 必须真的能关掉任务：这是一条会持续花上游 token 的后台任务，
// 运营方不能在不发版的前提下停不掉它。
//
// 变异验证：把 capabilityBackfillEnabled 改成恒 true → 本用例红。
func TestCapabilityBackfillKillSwitch(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"", true}, {"1", true}, {"true", true}, {"on", true}, {"yes", true},
		{"0", false}, {"false", false}, {"off", false}, {"no", false}, {" OFF ", false},
	}
	for _, tc := range cases {
		t.Setenv(CapabilityBackfillEnvKillSwitch, tc.val)
		if got := capabilityBackfillEnabled(); got != tc.want {
			t.Fatalf("capabilityBackfillEnabled() with %q = %v, want %v", tc.val, got, tc.want)
		}
	}
}

// ─── R33 审计钉桩（2026-10-03）：attempt 退避 / 反饥饿 / 蓝绿单跑 ───
//
// 三条不可让步约束之外的两条运行经济性闸门：无证据绑定在进程内退避期内不再
// 出网（48 探/天 → 至多 24 探/天，且不再把预算吃满饿死健康行）；蓝绿双实例
// 经 distlock 单跑（Redis 未接线时退化为既有双跑行为）。

// backfillLedgerHarness 与 newBackfillHarness 同形但扫多条绑定、probe 可编程
// （按 binding 记调用次数），供退避/反饥饿两用例共用。
type backfillLedgerHarness struct {
	backfill *CapabilityBackfill
	probeMu  sync.Mutex
	probes   map[int64]int
}

func newLedgerHarness(t *testing.T, n int, result httpProbeResult) *backfillLedgerHarness {
	t.Helper()
	h := &backfillLedgerHarness{probes: make(map[int64]int)}
	// 真实信封：recordAttempt 在 decrypt 之后、probe 之前，行必须能走通
	// 解密路径（与 newBackfillHarness 同一纪律——不给 decrypt 开后门）。
	var key [32]byte
	copy(key[:], "capability-backfill-test-32byte")
	kr, err := secret.NewKeyring(map[string][32]byte{"k1": key}, "k1")
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	envelope, err := secret.EncryptAESGCM([]byte("sk-real-test-key"), kr)
	if err != nil {
		t.Fatalf("EncryptAESGCM: %v", err)
	}
	b := &CapabilityBackfill{
		keyring:    kr,
		batchLimit: 10,
		staleAfter: capabilityBackfillStaleAfter,
		probe: func(_ context.Context, target probeTarget, _ providercap.Descriptor) httpProbeResult {
			h.probeMu.Lock()
			h.probes[int64(target.CredentialID)]++
			h.probeMu.Unlock()
			return result
		},
		scan: func(context.Context) ([]dueBinding, error) {
			rows := make([]dueBinding, 0, n)
			for i := 0; i < n; i++ {
				rows = append(rows, admitAll(func(d *dueBinding) {
					d.BindingID = int64(1000 + i)
					// CredentialID 借作 binding 序号，probe 侧按它计数。
					d.CredentialID = 1000 + i
					d.Ciphertext = []byte(envelope)
					d.Protocol = "openai-responses"
					// 非空即可：probe 已被换掉，endpoint 解析只需通过空值守门。
					d.BaseURL = "http://ledger-test.invalid"
				}))
			}
			return rows, nil
		},
		persist: func(context.Context, dueBinding, bool, []byte) error { return nil },
	}
	h.backfill = b
	return h
}

// 用例 1：无证据绑定退避——第二轮不再对同一批出网（此前每 30min 都探一遍，
// 48 探/天 vs 健康行 4 探/天的成本反转）。
//
// 变异验证：删掉 recordAttempt 调用 → 第二轮 probes 仍增长 → 本用例红。
func TestBackfillAttemptBackoffSkipsRecentProbes(t *testing.T) {
	h := newLedgerHarness(t, 5, httpProbeResult{status: "network", category: probeCategoryProviderError})
	ctx := context.Background()
	if _, err := h.backfill.BackfillOnce(ctx); err != nil {
		t.Fatalf("cycle 1: %v", err)
	}
	first := h.totalProbes()
	if first != 5 {
		t.Fatalf("cycle 1 probes = %d, want 5", first)
	}
	if _, err := h.backfill.BackfillOnce(ctx); err != nil {
		t.Fatalf("cycle 2: %v", err)
	}
	if got := h.totalProbes(); got != first {
		t.Fatalf("cycle 2 added %d probes within backoff window, want 0", got-first)
	}
}

// 用例 2：反饥饿——第一批「永远无证据」的绑定在预算内被探过后，第二轮的
// 预算必须让位给同批后面从未试过的绑定（此前 NULLS FIRST + 窗口=预算使
// >50 条无证据行永久霸占整批）。
//
// 窗口倍数本身由 TestBackfillScanLimitWidensWindow 直钉（本用例的 scan 是
// 注入接缝，看不见 SQL LIMIT）。
func TestBackfillLedgerYieldsBudgetToUntriedBindings(t *testing.T) {
	const total = 30 // 扫描窗口 batchLimit×4=40 ≥ 30，全部可被扫到
	h := newLedgerHarness(t, total, httpProbeResult{status: "network", category: probeCategoryProviderError})
	ctx := context.Background()
	if _, err := h.backfill.BackfillOnce(ctx); err != nil {
		t.Fatalf("cycle 1: %v", err)
	}
	firstRound := h.probedBindings()
	if len(firstRound) != 10 {
		t.Fatalf("cycle 1 probed %d bindings, want budget 10", len(firstRound))
	}
	before := h.probedBindings()
	if _, err := h.backfill.BackfillOnce(ctx); err != nil {
		t.Fatalf("cycle 2: %v", err)
	}
	// probedBindings 是跨轮累计，第二轮取差分。
	secondRound := map[int64]int{}
	for id, c := range h.probedBindings() {
		if d := c - before[id]; d > 0 {
			secondRound[id] = d
		}
	}
	if len(secondRound) != 10 {
		t.Fatalf("cycle 2 probed %d bindings, want 10", len(secondRound))
	}
	for id := range firstRound {
		if secondRound[id] > 0 {
			t.Fatalf("cycle 2 re-probed binding %d within backoff window (starvation)", id)
		}
	}
	if len(h.probes) != 20 {
		t.Fatalf("distinct bindings probed = %d, want 20 (10+10 disjoint)", len(h.probes))
	}
}

func (h *backfillLedgerHarness) totalProbes() int {
	h.probeMu.Lock()
	defer h.probeMu.Unlock()
	n := 0
	for _, c := range h.probes {
		n += c
	}
	return n
}

func (h *backfillLedgerHarness) probedBindings() map[int64]int {
	h.probeMu.Lock()
	defer h.probeMu.Unlock()
	out := make(map[int64]int, len(h.probes))
	for id, c := range h.probes {
		out[int64(id)] = c
	}
	return out
}

// 用例 3：蓝绿 follower 跳过——distlock 已被另一实例持有时，本实例整轮跳过
// （零出网）。
//
// 变异验证：删掉 BackfillOnce 的 acquireSweepDistLock 块 → follower 也会探
// 测 → 本用例红。
func TestBackfillFollowerSkipsCycle(t *testing.T) {
	mgr := distlock.NewLocalManager()
	// 「另一实例」先持有锁。
	leader, err := mgr.Acquire(context.Background(), distlock.AcquireOpts{
		Key:  distlock.BuildKey(autoRouteDistLockNamespace, "capability_backfill"),
		TTL:  capabilityBackfillDistLockTTL,
		Mode: distlock.ModeWaitFollower, Scope: "capability_backfill",
	})
	if err != nil {
		t.Fatalf("leader acquire: %v", err)
	}
	defer leader.Release(context.Background())

	h := newLedgerHarness(t, 3, httpProbeResult{status: "network", category: probeCategoryProviderError})
	h.backfill.SetDistLock(mgr)
	n, err := h.backfill.BackfillOnce(context.Background())
	if err != nil {
		t.Fatalf("follower cycle: %v", err)
	}
	if n != 0 || h.totalProbes() != 0 {
		t.Fatalf("follower instance probed (%d written, %d probes), want zero outbound", n, h.totalProbes())
	}
}

// 用例 4：扫描窗口形状——SQL 窗口必须是预算 × scanFactor。探测接缝测试
// （用例 2）的 scan 是注入的、看不见 SQL LIMIT，所以窗口倍数在这里直钉。
//
// 变异验证：capabilityBackfillScanFactor 改 1 → 本用例红。
func TestBackfillScanLimitWidensWindow(t *testing.T) {
	b := &CapabilityBackfill{batchLimit: 50}
	// 期望值硬编码：引用 capabilityBackfillScanFactor 自身会把变异喂饱
	//（§9 自证陷阱，首轮变异 factor=1 时确实没红）。
	if got := b.scanLimit(); got != 200 {
		t.Fatalf("scanLimit = %d, want 50×4 = 200", got)
	}
}
