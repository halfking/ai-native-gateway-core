package externalhost

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/externalhost/contract"
)

// newTestHost 用 httptest 替换 unix socket 传输，聚焦宿主语义而非传输细节。
func newTestHost(t *testing.T, handler http.HandlerFunc) *Host {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(Config{
		SocketPath: "/dev/null",
		BaseURL:    srv.URL,
		Timeout:    2 * time.Second,
		HTTPClient: srv.Client(),
	})
}

func localCtx() *contract.Context {
	return &contract.Context{
		Protocol:  "anthropic",
		Model:     "claude-sonnet",
		RequestID: "req-1",
		TenantID:  "t1",
		Origin:    contract.OriginLocal,
	}
}

func TestOnRequestContinue(t *testing.T) {
	host := newTestHost(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != contract.HookPointOnRequest.Path() {
			t.Errorf("path = %q, want %q", r.URL.Path, contract.HookPointOnRequest.Path())
		}
		if got := r.Header.Get("X-External-Hook-Contract"); got != contract.ContractVersion {
			t.Errorf("contract header = %q", got)
		}
		_, _ = w.Write([]byte(`{"contract_version":"gateway-external-hook-v1","decision":"continue"}`))
	})
	out := host.OnRequest(context.Background(), localCtx(), &contract.RequestPayload{Method: "POST"})
	if out.Failed() {
		t.Fatalf("unexpected failure: %v", out.Err)
	}
	if out.Decision != contract.DecisionContinue {
		t.Errorf("decision = %q", out.Decision)
	}
}

func TestOnRequestRewriteApplies(t *testing.T) {
	host := newTestHost(t, func(w http.ResponseWriter, r *http.Request) {
		// 补丁语义：只给 path，model/stream/tools 缺省即不改写。
		_, _ = w.Write([]byte(`{"contract_version":"gateway-external-hook-v1","decision":"rewrite",
			"request":{"path":"/v1/messages"}}`))
	})
	out := host.OnRequest(context.Background(), localCtx(), &contract.RequestPayload{
		Method: "POST", Path: "/v1/chat", Model: "claude-sonnet", Stream: true,
	})
	if out.Failed() {
		t.Fatalf("unexpected failure: %v", out.Err)
	}
	if out.Decision != contract.DecisionRewrite {
		t.Fatalf("decision = %q", out.Decision)
	}
	if out.Request == nil || out.Request.Path != "/v1/messages" {
		t.Fatalf("rewritten request = %+v", out.Request)
	}
	// 缺省字段必须保持原值（补丁语义）。
	if out.Request.Model != "claude-sonnet" || !out.Request.Stream {
		t.Errorf("omitted fields must stay unchanged, got model=%q stream=%v", out.Request.Model, out.Request.Stream)
	}
}

func TestOnRequestRewriteProtectedFieldRollsBack(t *testing.T) {
	// 钩子显式改写 model —— 受保护字段，整体回滚并记失败。
	host := newTestHost(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"contract_version":"gateway-external-hook-v1","decision":"rewrite",
			"request":{"model":"other-model"}}`))
	})
	out := host.OnRequest(context.Background(), localCtx(), &contract.RequestPayload{Method: "POST", Model: "claude-sonnet"})
	if out.Failure != contract.FailureProtectedField {
		t.Fatalf("failure = %q, want %q (err=%v)", out.Failure, contract.FailureProtectedField, out.Err)
	}
	if !errors.Is(out.Err, contract.ErrProtectedField) {
		t.Errorf("err = %v, want ErrProtectedField", out.Err)
	}
	// 回滚语义：必须原样直通，不得带改写载荷。
	if out.Decision != contract.DecisionContinue || out.Request != nil {
		t.Errorf("protected-field rewrite must pass through unchanged, got %+v", out)
	}
}

func TestOnRequestRewriteStreamProtectedRollsBack(t *testing.T) {
	host := newTestHost(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"contract_version":"gateway-external-hook-v1","decision":"rewrite",
			"request":{"stream":false}}`))
	})
	out := host.OnRequest(context.Background(), localCtx(), &contract.RequestPayload{Method: "POST", Stream: true})
	if out.Failure != contract.FailureProtectedField {
		t.Fatalf("failure = %q, want %q (err=%v)", out.Failure, contract.FailureProtectedField, out.Err)
	}
}

func TestOnRequestTimeoutPassesThrough(t *testing.T) {
	host := newTestHost(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{"contract_version":"gateway-external-hook-v1","decision":"continue"}`))
	})
	host.cfg.Timeout = 80 * time.Millisecond
	host.client.Timeout = 80 * time.Millisecond

	out := host.OnRequest(context.Background(), localCtx(), &contract.RequestPayload{Method: "POST"})
	if out.Failure != contract.FailureTimeout {
		t.Fatalf("failure = %q, want %q (err=%v)", out.Failure, contract.FailureTimeout, out.Err)
	}
	if out.Decision != contract.DecisionContinue || out.Request != nil {
		t.Errorf("timeout must pass through unchanged, got %+v", out)
	}
}

func TestOnRequestCrashPassesThrough(t *testing.T) {
	host := newTestHost(t, func(w http.ResponseWriter, r *http.Request) {
		// 模拟进程崩溃：连接直接断开（hijack 后关闭）。
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, err := hj.Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	out := host.OnRequest(context.Background(), localCtx(), &contract.RequestPayload{Method: "POST"})
	if out.Failure != contract.FailureCrash {
		t.Fatalf("failure = %q, want %q (err=%v)", out.Failure, contract.FailureCrash, out.Err)
	}
	if out.Decision != contract.DecisionContinue {
		t.Errorf("crash must pass through unchanged, got %+v", out)
	}
}

func TestRemoteOriginSkipsBodyCapture(t *testing.T) {
	var sawBody bool
	host := newTestHost(t, func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 8192)
		n, _ := r.Body.Read(buf)
		// 正文在 JSON 中是 base64 编码，必须按 base64 片段判定。
		if strings.Contains(string(buf[:n]), "c2VjcmV0LWJvZHk=") {
			sawBody = true
		}
		_, _ = w.Write([]byte(`{"contract_version":"gateway-external-hook-v1","decision":"continue"}`))
	})
	ctx := localCtx()
	ctx.Origin = contract.OriginRemote
	out := host.OnRequest(context.Background(), ctx, &contract.RequestPayload{
		Method: "POST",
		Body:   contract.Payload{Body: []byte("secret-body")},
	})
	if out.Failed() {
		t.Fatalf("unexpected failure: %v", out.Err)
	}
	if sawBody {
		t.Error("remote origin body must not reach the hook (W3.4 privacy)")
	}
}

func TestRemoteBodyCaptureAllowedWhenConfigured(t *testing.T) {
	var sawBody bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 8192)
		n, _ := r.Body.Read(buf)
		if strings.Contains(string(buf[:n]), "c2VjcmV0LWJvZHk=") {
			sawBody = true
		}
		_, _ = w.Write([]byte(`{"contract_version":"gateway-external-hook-v1","decision":"continue"}`))
	}))
	defer srv.Close()
	host := New(Config{SocketPath: "/dev/null", BaseURL: srv.URL, Timeout: time.Second, AllowRemoteBodyCapture: true, HTTPClient: srv.Client()})
	ctx := localCtx()
	ctx.Origin = contract.OriginRemote
	out := host.OnRequest(context.Background(), ctx, &contract.RequestPayload{
		Method: "POST", Body: contract.Payload{Body: []byte("secret-body")},
	})
	if out.Failed() {
		t.Fatalf("unexpected failure: %v", out.Err)
	}
	if !sawBody {
		t.Error("explicit opt-in must allow remote body capture")
	}
}

func TestOnResponseBlockCarriesReason(t *testing.T) {
	host := newTestHost(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"contract_version":"gateway-external-hook-v1","decision":"block","reason":"compliance"}`))
	})
	out := host.OnResponse(context.Background(), localCtx(), &contract.ResponsePayload{Status: 200})
	if out.Failed() {
		t.Fatalf("block is an explicit intent, not a failure: %v", out.Err)
	}
	if out.Decision != contract.DecisionBlock || out.Reason != "compliance" {
		t.Errorf("outcome = %+v", out)
	}
}

func TestOnResponseVersionMismatchIsProtocolFailure(t *testing.T) {
	host := newTestHost(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"contract_version":"gateway-external-hook-v0","decision":"continue"}`))
	})
	out := host.OnResponse(context.Background(), localCtx(), &contract.ResponsePayload{Status: 200})
	if out.Failure != contract.FailureProtocol {
		t.Fatalf("failure = %q, want %q (err=%v)", out.Failure, contract.FailureProtocol, out.Err)
	}
	if out.Decision != contract.DecisionContinue {
		t.Errorf("protocol failure must pass through unchanged, got %+v", out)
	}
}

func TestOnResponseHookErrorIsHookFailure(t *testing.T) {
	host := newTestHost(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"contract_version":"gateway-external-hook-v1","decision":"continue","error":"boom"}`))
	})
	out := host.OnResponse(context.Background(), localCtx(), &contract.ResponsePayload{Status: 200})
	if out.Failure != contract.FailureHook {
		t.Fatalf("failure = %q, want %q (err=%v)", out.Failure, contract.FailureHook, out.Err)
	}
}

func TestOnResponseHTTPErrorStatusIsProtocolFailure(t *testing.T) {
	host := newTestHost(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	out := host.OnResponse(context.Background(), localCtx(), &contract.ResponsePayload{Status: 200})
	if out.Failure != contract.FailureProtocol {
		t.Fatalf("failure = %q, want %q", out.Failure, contract.FailureProtocol)
	}
}

func TestNilArgumentsRejected(t *testing.T) {
	host := newTestHost(t, func(w http.ResponseWriter, r *http.Request) {})
	if out := host.OnRequest(context.Background(), nil, &contract.RequestPayload{}); out.Failure != contract.FailureProtocol {
		t.Errorf("nil context must be protocol failure, got %+v", out)
	}
	if out := host.OnRequest(context.Background(), localCtx(), nil); out.Failure != contract.FailureProtocol {
		t.Errorf("nil payload must be protocol failure, got %+v", out)
	}
	if out := host.OnResponse(context.Background(), nil, &contract.ResponsePayload{}); out.Failure != contract.FailureProtocol {
		t.Errorf("nil context must be protocol failure, got %+v", out)
	}
}

func TestHostTimeoutIsClamped(t *testing.T) {
	h := New(Config{SocketPath: "/x", Timeout: 30 * time.Second})
	if h.Timeout() != contract.MaxTimeout {
		t.Errorf("timeout = %v, want %v", h.Timeout(), contract.MaxTimeout)
	}
	h2 := New(Config{SocketPath: "/x", Timeout: 0})
	if h2.Timeout() != contract.DefaultTimeout {
		t.Errorf("timeout = %v, want %v", h2.Timeout(), contract.DefaultTimeout)
	}
}
