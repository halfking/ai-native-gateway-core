//go:build integration

package streaming

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/middleware"
)

// ─────────────────────────────────────────────────────────────────────────────
// 缺陷B 补齐：failAndMark 一类出口此前从不记录客户端实收状态，于是下游
// （sessionv2mirror.statusCode / lite sink）只能退回字面 500。线上实测
// （2.5.8.2671，20 个真实请求）：error_kind=transient / no_candidate 的失败请求
// 客户端实收 503，session_turns_hot.status_code 却是 500。
//
// 这些出口全部由 **真实 handler** 驱动（ChatHandler.ServeHTTP），失败流量用仓
// 库既有夹具在本地造：路由阶段用错误的模型名 / 无候选，执行阶段用注入的
// AttemptExecutor 返回一个确定的 *executors.ExecuteError。不发任何真实模型请求。
//
// ⚠ 量具纪律（承上一轮）：断言一律写 `ClientStatusCode == <具体值>`，禁止
// `!= 500` —— 假绿探针已证明后者没有牙。另有「前提仍在场」断言：先确认客户端
// 确实收到了预期状态码、且确实产出了带 error_kind 的终态 entry，否则下面关于
// status 的断言可能只是在测一个没失败的请求。
// ─────────────────────────────────────────────────────────────────────────────

// retireFailExecutor 返回一个确定的执行失败，让 handler 走到共享的
// post-execute 错误分支（content_filter / credential / unsupported_feature /
// model_deprecated / rate_limit / all-candidates-failed / provider_error）。
type retireFailExecutor struct {
	kind      errorsx.ErrorKind
	exhausted bool
	tried     int
}

func (e retireFailExecutor) Execute(*executors.ExecParams) (*executors.ExecuteResult, error) {
	return nil, &executors.ExecuteError{
		LastKind:  e.kind,
		Exhausted: e.exhausted,
		Tried:     e.tried,
	}
}

// retireStreamingBody builds a chat body with the requested stream flag.
func retireStreamingBody(stream bool) string {
	v := "false"
	if stream {
		v = "true"
	}
	return `{"model":"gpt-4o","stream":` + v + `,"messages":[{"role":"user","content":"hi"}]}`
}

// runRetireChat drives one real request through the handler and returns the
// client-observed status plus every emitted terminal entry.
func runRetireChat(t *testing.T, body string, resolver providerResolver, exec AttemptExecutor) (int, []*telemetry.RequestLogEntry) {
	t.Helper()
	var entries []*telemetry.RequestLogEntry
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	h.setRequestKeyVerifierForTest(retirePassVerifier{})
	h.executor = &executors.Executor{}
	h.provider = resolver
	h.SetRequestLogHook(func(e *telemetry.RequestLogEntry) { entries = append(entries, e) })
	if exec != nil {
		// Bound the survival window hard: the shipped default is a 240s in-connection
		// budget with up to 100 retries, which would make each executor-error case
		// take minutes. survivalAttemptExec is only reachable through the coordinator,
		// so the seam requires survival to be eligible.
		h.SetRequestSurvival(func(string) bool { return true }, SurvivalOptions{
			Deadline:          2 * time.Second,
			MaxRetries:        1,
			RetryBase:         10 * time.Millisecond,
			RetryMax:          10 * time.Millisecond,
			KeepaliveInterval: 10 * time.Millisecond,
		})
		h.survivalAttemptExec = exec
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-retire")
	w := httptest.NewRecorder()
	middleware.NewRequestIDMiddleware().Wrap(h).ServeHTTP(w, req)
	return w.Code, entries
}

// pickEntry returns the single emitted entry carrying wantKind.
func pickEntry(t *testing.T, entries []*telemetry.RequestLogEntry, wantKind string) *telemetry.RequestLogEntry {
	t.Helper()
	for _, e := range entries {
		if e.ErrorKind != nil && *e.ErrorKind == wantKind {
			return e
		}
	}
	t.Fatalf("no emitted entry carried error_kind=%q; entries=%d", wantKind, len(entries))
	return nil
}

// TestFailAndMarkExitsRecordClientReceivedStatus covers the failAndMark family.
//
// keepalive OFF → the explicit arm of each branch runs, so the client receives
// the explicit status written by writeErrorJSON*.
// keepalive ON (the shipped default) → preStreamPrepared is true, the prewarmed
// arm runs, and the client receives 200 with the error travelling as an
// in-stream SSE envelope. That asymmetry is the point of the last two cases:
// the persisted status must follow the client, not the branch's "usual" status.
func TestFailAndMarkExitsRecordClientReceivedStatus(t *testing.T) {
	for _, tc := range []struct {
		name       string
		body       string
		resolver   providerResolver
		exec       AttemptExecutor
		keepalive  string
		wantKind   string
		wantClient int
	}{
		{
			name:       "no_candidate_renders_503",
			body:       retireStreamingBody(false),
			resolver:   retireNoCandidateResolver{known: true},
			wantKind:   "no_candidate",
			wantClient: http.StatusServiceUnavailable,
		},
		{
			name:       "invalid_model_renders_400",
			body:       retireStreamingBody(false),
			resolver:   retireNoCandidateResolver{known: false},
			wantKind:   "invalid_model",
			wantClient: http.StatusBadRequest,
		},
		{
			// The survival terminal is the render-then-emit case: the coordinator
			// already put its protocol terminal on an already-committed stream, so
			// the client received 200. Kept in this matrix because it is the case a
			// naive "attach the upstream status" fix would get wrong.
			name:       "survival_terminal_renders_200",
			body:       retireStreamingBody(true),
			resolver:   durableEndpointResolver{},
			exec:       retireFailExecutor{kind: errorsx.KindUpstreamDown},
			wantKind:   "gateway_survival_fail_closed",
			wantClient: http.StatusOK,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.keepalive == "" {
				t.Setenv("LLM_GATEWAY_ENABLE_PRE_STREAM_KEEPALIVE", "true")
			} else {
				t.Setenv("LLM_GATEWAY_ENABLE_PRE_STREAM_KEEPALIVE", tc.keepalive)
			}
			code, entries := runRetireChat(t, tc.body, tc.resolver, tc.exec)

			// 前提 1：客户端确实收到了预期状态（否则 status 断言是空的）。
			if code != tc.wantClient {
				t.Fatalf("client received %d, want %d; the branch under test was not taken", code, tc.wantClient)
			}
			// 前提 2：确实走的是 failAndMark 出口并产生了终态行。
			e := pickEntry(t, entries, tc.wantKind)

			if e.ClientStatusCode == nil {
				t.Fatalf("entry for error_kind=%q carries no ClientStatusCode; the client received %d but "+
					"the status never entered the entry, so session_turns.status_code falls back to 500",
					tc.wantKind, code)
			}
			if *e.ClientStatusCode != tc.wantClient {
				t.Fatalf("ClientStatusCode = %d, want %d (client actually received %d)",
					*e.ClientStatusCode, tc.wantClient, code)
			}
		})
	}
}

// 成功对照：成功请求不得凭空发明 ClientStatusCode（nil 让下游走自己的
// 200 推导）。防的是「一律写死一个状态码」这种过度修正。
func TestSuccessPathStillRecordsNoClientStatus(t *testing.T) {
	var entries []*telemetry.RequestLogEntry
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	h.setRequestKeyVerifierForTest(retirePassVerifier{})
	h.SetRequestLogHook(func(e *telemetry.RequestLogEntry) { entries = append(entries, e) })
	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	w := httptest.NewRecorder()
	middleware.NewRequestIDMiddleware().Wrap(h).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET probe status = %d, want 200", w.Code)
	}
	for _, e := range entries {
		if e.ClientStatusCode != nil {
			t.Fatalf("success entry invented ClientStatusCode=%d", *e.ClientStatusCode)
		}
	}
}

// clientStatusForPrewarmed 是「预热已提交响应」这一形态的唯一判定点，7 个共享的
// post-execute 分支都经由它，所以它的 200-vs-显式状态契约在这里钉死：
//   - keepalive 已提交 → 客户端实收 **200**（writePrewarmedStreamError* 只写帧、
//     从不 WriteHeader），把显式状态透传上去就是凭空发明一个客户端没见过的码；
//   - 未提交 → 显式状态就是线上的状态，原样返回。
//
// 为什么需要单测它：端到端能驱动的只有路由阶段的分支；这 7 个要抵达必须有完整
// 假上游（survival 会先截走执行错误）。端到端判据覆盖可达分支，本表 + 上面那张
// 逐站点表覆盖不可达分支的契约，两段合起来不留空白。
func TestClientStatusForPrewarmed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		prepared bool
		explicit int
		want     int
		reason   string
	}{
		{"committed_stream_receives_200_not_the_explicit_status", true, http.StatusServiceUnavailable, http.StatusOK,
			"the keepalive already committed the response; the client saw 200"},
		{"committed_stream_receives_200_not_the_upstream_4xx", true, http.StatusTooManyRequests, http.StatusOK,
			"same, for a branch whose explicit status is a 4xx"},
		{"uncommitted_stream_keeps_the_explicit_status", false, http.StatusServiceUnavailable, http.StatusServiceUnavailable,
			"writeErrorJSON* is what puts this on the wire"},
		{"uncommitted_stream_keeps_gone", false, http.StatusGone, http.StatusGone, "410 is the wire status"},
		{"uncommitted_stream_keeps_computed_credential_status", false, http.StatusUnauthorized, http.StatusUnauthorized,
			"credHTTPStatus comes from classifyUpstreamCredentialFailure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := clientStatusForPrewarmed(tc.prepared, tc.explicit); got != tc.want {
				t.Fatalf("clientStatusForPrewarmed(%v, %d) = %d, want %d — %s",
					tc.prepared, tc.explicit, got, tc.want, tc.reason)
			}
		})
	}
}

// 每个 failAndMark 调用点都必须显式声明客户端状态，且声明的值必须与该分支
// 真正写给客户端的状态一致。这是一张随代码走的表：
//
//   - 少一个实参 → 编译不过，而不是静默退回 500；
//   - 实参值与下表不符 → 这里红，说明有人改了写客户端的状态却没改这里
//     （或反过来），二者必须一起动。
//
// 期望值全部取自各分支相邻的、真正决定客户端响应的代码路径：writeErrorJSON*
// 的实参、classifyRoutingError / classifyUpstreamCredentialFailure 的返回值、
// 以及 prewarmed / 已渲染 SSE 终态这一类「客户端实收 200」。
func TestEveryFailAndMarkCallSiteStatesTheClientReceivedStatus(t *testing.T) {
	for file, want := range map[string][]string{
		"handler.go": {
			// classifyRoutingError's own verdict, passed to writeErrorJSON below.
			"rc.httpStatus",
			// writeErrorJSONCtx(..., http.StatusBadRequest, ...) — invalid_model.
			"http.StatusBadRequest",
			// writeNoCandidateWithAlternatives renders 503 on every arm.
			"http.StatusServiceUnavailable",
			// Survival terminal already rendered on a committed stream → 200.
			"http.StatusOK",
			// The shared post-execute branches: prewarmed arm is 200 (the keepalive
			// committed the response and writePrewarmedStreamError* never sets a
			// status), else the explicit status writeErrorJSON* puts on the wire.
			"clientStatusForPrewarmed(preStreamPrepared, http.StatusBadRequest)",
			"clientStatusForPrewarmed(preStreamPrepared, credHTTPStatus)",
			"clientStatusForPrewarmed(preStreamPrepared, http.StatusBadRequest)",
			"clientStatusForPrewarmed(preStreamPrepared, http.StatusGone)",
			"clientStatusForPrewarmed(preStreamPrepared, http.StatusTooManyRequests)",
			"clientStatusForPrewarmed(preStreamPrepared, http.StatusServiceUnavailable)",
			"clientStatusForPrewarmed(preStreamPrepared, http.StatusBadGateway)",
		},
		// In-stream policy block: writeTrustedTerminal wrote the terminal frame
		// with no WriteHeader on the committed stream → 200.
		// Non-stream policy block: writeAnthropicError(..., http.StatusForbidden, ...).
		"messages.go":  {"http.StatusOK", "http.StatusForbidden"},
		"responses.go": {"http.StatusOK", "http.StatusForbidden"},
	} {
		got := failAndMarkCallSites(t, file)
		if len(got) != len(want) {
			t.Fatalf("%s: %d failAndMark call sites, want %d — a site was added or dropped "+
				"without revisiting the client-status contract: %v", file, len(got), len(want), got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s failAndMark site #%d records client status %q, want %q", file, i+1, got[i], want[i])
			}
		}
	}
	if !strings.Contains(readStreamingSource(t, "request_log_pipeline.go"),
		"clientStatusForPrewarmed(preStreamPrepared bool, explicitStatus int) int") {
		t.Fatal("clientStatusForPrewarmed is gone; the prewarmed 200 vs explicit status decision " +
			"has no single source of truth any more")
	}
}

// failAndMarkCallSites returns, per file, the rendered source of each
// `logCtx.failAndMark(...)` call's clientStatus argument. go/ast is used rather
// than a text scan so comments, nested calls and quoted parens cannot skew it.
func failAndMarkCallSites(t *testing.T, file string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "failAndMark" {
			return true
		}
		if recv, ok := sel.X.(*ast.Ident); !ok || recv.Name != "logCtx" {
			return true
		}
		if len(call.Args) < 5 {
			out = append(out, fmt.Sprintf("<MISSING: %d args>", len(call.Args)))
			return true
		}
		var b strings.Builder
		if err := printer.Fprint(&b, fset, call.Args[4]); err != nil {
			t.Fatal(err)
		}
		out = append(out, b.String())
		return true
	})
	return out
}

func readStreamingSource(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
