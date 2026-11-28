package executors

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/redis/go-redis/v9"
)

// 2026-11-29（上线后真机验证抓到的回归）：指纹槽修复让「内部流量不占槽」之后，
// 内部请求的 fpLease 恒为 nil；而 chat / anthropic 执行器的并发过载与流中断分支
// 无判空解引用 fpLease.Holder ⇒ 211 副节点 16:48Z 连续 3 次
// `dispatch forward panic recovered: nil pointer dereference`（内部
// deepseek-flash 请求，上游 429）。recover 兜住了进程，但那次尝试被改写成
// `dispatch forward panic`（错误分类与状态写入全部走偏）。
//
// 无槽运行此前就存在（池满后的降级路径），只是少得多——潜伏缺陷被新行为放大。
// 本文件钉住：拿不到 lease 的请求在上游报并发过载/限流时走正常错误路径。

// nilLeaseUpstream answers every request with the given status and an overload body.
func nilLeaseUpstream(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"message":"The engine is currently overloaded, please try again later."}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func nilLeaseExecutor(t *testing.T, fpMgr *credentialfpslot.Manager) *Executor {
	t.Helper()
	limiter := newLimiterForTest()
	t.Cleanup(limiter.Stop)
	return &Executor{
		Circuit:         newCircuitManagerForTest(),
		Limiter:         limiter,
		FpSlots:         fpMgr,
		UpstreamTimeout: 5 * time.Second,
		StreamTimeout:   10 * time.Second,
	}
}

func nilLeaseParams(internal bool) *ExecParams {
	return &ExecParams{
		W:                httptest.NewRecorder(),
		R:                httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes:        []byte(`{"model":"deepseek-flash","messages":[{"role":"user","content":"hi"}]}`),
		ClientModel:      "deepseek-flash",
		Model:            "deepseek-flash",
		RequestID:        "nil-lease-overload",
		TenantID:         "default",
		InternalRequest:  internal,
		UpstreamAttempts: NewUpstreamAttemptBudget(DefaultUpstreamAttemptLimit),
	}
}

func nilLeaseCandidate(baseURL string, fpLimit int) provider.Candidate {
	return provider.Candidate{
		ProviderID: 24, CredentialID: 2, BaseURL: baseURL,
		Protocol: "openai-completions", RawModel: "deepseek-flash", APIKey: "test-key",
		FpSlotLimit: &fpLimit, BillingMode: "token",
	}
}

// TestNilFpLease_OverloadTakesNormalErrorPath covers both ways a dispatch
// forward ends up without a lease: internal traffic (never acquires) and a
// slotless run (pool saturated by an active holder). The third case proves the
// holder path still works when a lease exists.
func TestNilFpLease_OverloadTakesNormalErrorPath(t *testing.T) {
	fpLimit := 1
	cases := []struct {
		name     string
		internal bool
		saturate bool
	}{
		{name: "internal_request_never_holds_a_slot", internal: true},
		{name: "slotless_run_pool_saturated", saturate: true},
		{name: "with_lease_control", internal: false, saturate: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mr := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			t.Cleanup(func() { _ = client.Close() })
			fpMgr := credentialfpslot.New(credentialfpslot.Config{DefaultLimit: 1, Enabled: true}, client)
			if tc.saturate {
				if lease, ok := fpMgr.Acquire(t.Context(), 2, &fpLimit, "busy-user", "default"); !ok || lease == nil {
					t.Fatal("failed to saturate the fingerprint slot")
				}
			}
			exec := nilLeaseExecutor(t, fpMgr)
			for _, status := range []int{http.StatusServiceUnavailable, http.StatusTooManyRequests} {
				cand := nilLeaseCandidate(nilLeaseUpstream(t, status).URL, fpLimit)
				params := nilLeaseParams(tc.internal)
				dctx := &dispatchCtx{params: params, candidates: []provider.Candidate{cand}, holder: "internal-caller", retryPerCred: 0, tTotal: time.Now()}
				out := exec.forwardForDispatch(dctx, cand, "nil-lease-attempt", func() {}, nil, t.Context())
				err, kind := out.Err, out.ErrorKind
				if err == nil {
					t.Fatalf("status %d: an overloaded upstream must fail the attempt", status)
				}
				if strings.Contains(err.Error(), "dispatch forward panic") {
					t.Fatalf("status %d: nil fingerprint lease was dereferenced: %v", status, err)
				}
				if status == http.StatusServiceUnavailable && kind != string(errorsx.KindConcurrent) {
					t.Fatalf("status %d: error kind = %q, want %q", status, kind, errorsx.KindConcurrent)
				}
				if status == http.StatusTooManyRequests && kind != string(errorsx.KindRateLimit) && kind != string(errorsx.KindConcurrent) {
					t.Fatalf("status %d: error kind = %q, want a rate-limit family kind", status, kind)
				}
			}
		})
	}
}

// TestNoUnguardedFpLeaseHolderDeref is the source gate: inside this package a
// fingerprint lease's Holder may only be read through
// forceUnpinFpLeaseOnFatalKind, which tolerates a nil lease. A direct
// `fpLease.Holder` (or forceUnpinOnFatalKind fed from fpLease) reintroduces the
// panic the moment a request runs without a slot.
func TestNoUnguardedFpLeaseHolderDeref(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			code := line
			if idx := strings.Index(code, "//"); idx >= 0 {
				code = code[:idx]
			}
			if strings.Contains(code, "fpLease.Holder") || (strings.Contains(code, "forceUnpinOnFatalKind(") && strings.Contains(code, "fpLease.")) {
				t.Errorf("%s:%d reads the fingerprint lease without a nil guard — route it through forceUnpinFpLeaseOnFatalKind:\n\t%s", file, i+1, strings.TrimSpace(line))
			}
		}
	}

	raw, err := os.ReadFile("executor.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	start := strings.Index(src, "func (e *Executor) forceUnpinFpLeaseOnFatalKind(")
	if start < 0 {
		t.Fatal("forceUnpinFpLeaseOnFatalKind is gone — the nil-lease guard must live somewhere")
	}
	body := src[start:]
	if end := strings.Index(body, "\n}\n"); end >= 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "if lease == nil {") || !strings.Contains(body, "return") {
		t.Fatalf("forceUnpinFpLeaseOnFatalKind lost its nil-lease early return:\n%s", body)
	}
}
