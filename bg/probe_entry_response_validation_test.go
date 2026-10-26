package bg

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/credentialstate"
	"github.com/kaixuan/llm-gateway-go/internal/providercap"
)

type auditProbeObserver struct {
	credentialstate.StateObserver
	calls []*credentialstate.State
}

func (o *auditProbeObserver) UpdateFromProbe(_ context.Context, state *credentialstate.State) {
	o.calls = append(o.calls, state)
}

func TestProbeOtherEntryPointsRequireOutput(t *testing.T) {
	for _, body := range []string{"", "<html>login</html>", `{}`, `{"error":{"message":"overloaded"}}`, `{"choices":[{"message":{"reasoning_content":"thinking"}}]}`} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }))
			defer srv.Close()
			srv.Client().Timeout = 5 * time.Second
			e := &ActiveProbeExecutor{httpClient: srv.Client()}
			target := &ProbeTarget{CredentialID: 7, RawModel: "minimax-m3", BaseURL: srv.URL, Protocol: "openai-completions"}
			if r := e.Run(t.Context(), target); r.Status == ProbeStatusSuccess {
				t.Errorf("active direct recovered without output: %+v", r)
			}
			e.SetGateway(srv.URL, "test-key", srv.Client())
			if r := e.RunGateway(t.Context(), target); r.Status == ProbeStatusSuccess {
				t.Errorf("active gateway recovered without output: %+v", r)
			}
			if r := e.runModelsList(t.Context(), target); r.Status == ProbeStatusSuccess {
				t.Errorf("active models recovered without model evidence: %+v", r)
			}
			oldChat, oldHTTP := probeChatClient, probeHTTPClient
			probeChatClient, probeHTTPClient = srv.Client(), srv.Client()
			defer func() { probeChatClient, probeHTTPClient = oldChat, oldHTTP }()
			if r := singleChatPing(t.Context(), srv.URL, "test-key", target.RawModel, providercap.Descriptor{}, 7, target.RawModel); r.category == probeCategoryOK {
				t.Errorf("model chat probe accepted empty output: %+v", r)
			}
			if r := singleResponsesPing(t.Context(), srv.URL, "test-key", target.RawModel, providercap.Descriptor{}, 7, target.RawModel); r.category == probeCategoryOK || r.supportsResponses != nil {
				t.Errorf("model Responses probe learned health/capability without output: %+v", r)
			}
			if r := singleGet(t.Context(), srv.URL, "test-key", providercap.Descriptor{}, 7, target.RawModel); r.category == probeCategoryOK {
				t.Errorf("model list probe accepted missing models: %+v", r)
			}
		})
	}
}

func TestProbeOtherEntryPointsReadCompleteOutput(t *testing.T) {
	chat := `{"choices":[{"message":{"content":"` + strings.Repeat("x", 6000) + `"}}]}`
	responses := `{"object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"pong"}]}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := chat
		if strings.HasSuffix(r.URL.Path, "responses") {
			body = responses
		}
		if strings.HasSuffix(r.URL.Path, "models") {
			body = `{"data":[{"id":"minimax-m3"}]}`
		}
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	srv.Client().Timeout = 5 * time.Second
	e := &ActiveProbeExecutor{httpClient: srv.Client()}
	target := &ProbeTarget{CredentialID: 7, RawModel: "minimax-m3", BaseURL: srv.URL, Protocol: "openai-completions"}
	if r := e.Run(t.Context(), target); r.Status != ProbeStatusSuccess {
		t.Fatalf("active positive control: %+v", r)
	}
	e.SetGateway(srv.URL, "test-key", srv.Client())
	if r := e.RunGateway(t.Context(), target); r.Status != ProbeStatusSuccess {
		t.Fatalf("gateway positive control: %+v", r)
	}
	if r := e.runModelsList(t.Context(), target); r.Status != ProbeStatusSuccess {
		t.Fatalf("models positive control: %+v", r)
	}
	old := probeChatClient
	probeChatClient = srv.Client()
	defer func() { probeChatClient = old }()
	if r := singleChatPing(t.Context(), srv.URL, "test-key", target.RawModel, providercap.Descriptor{}, 7, target.RawModel); r.category != probeCategoryOK {
		t.Fatalf("chat positive control: %+v", r)
	}
	if r := singleResponsesPing(t.Context(), srv.URL+"/responses", "test-key", target.RawModel, providercap.Descriptor{}, 7, target.RawModel); r.category != probeCategoryOK || r.supportsResponses == nil || !*r.supportsResponses {
		t.Fatalf("Responses positive control: %+v", r)
	}
}

type auditProbeRoundTripper func(*http.Request) (*http.Response, error)

func (f auditProbeRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type auditProbeReadError struct{}

func (auditProbeReadError) Read([]byte) (int, error) { return 0, errors.New("upstream read failed") }

func TestProbeOtherEntryPointsReadFailureCannotRecover(t *testing.T) {
	for _, status := range []int{200, 401} {
		client := &http.Client{Timeout: 5 * time.Second, Transport: auditProbeRoundTripper(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(io.MultiReader(strings.NewReader(`{"choices":[{"message":{"content":"pong"}}]}`), auditProbeReadError{}))}, nil
		})}
		e := &ActiveProbeExecutor{httpClient: client}
		r := e.Run(t.Context(), &ProbeTarget{BaseURL: "http://probe.invalid", RawModel: "minimax-m3", Protocol: "openai-completions"})
		if r.Status == ProbeStatusSuccess {
			t.Errorf("read error recovered: %+v", r)
		}
		if status == 401 && r.Status != ProbeStatusAuth {
			t.Errorf("lost received authentication evidence: %+v", r)
		}
	}
}

func TestActiveProbeObservationPreservesAuthority(t *testing.T) {
	for _, status := range []ProbeStatus{ProbeStatusSkipped, ProbeStatusSuccess, ProbeStatusAuth} {
		observer := &auditProbeObserver{}
		w := NewActiveProbeWorker(ActiveProbeWorkerConfig{Enabled: true, StateManager: observer, MaxAttempts: 1})
		key := probeKey(7, "minimax-m3")
		w.running[key] = &probeState{CredentialID: 7, Model: "minimax-m3", Attempt: 1}
		w.applyProbeResult(t.Context(), probeTask{CredID: 7, Model: "minimax-m3"}, 1, &ProbeResult{Status: status, ObservationOnly: true})
		if len(observer.calls) != 0 || w.RunningCount() != 0 || len(w.queue) != 0 {
			t.Errorf("observation wrote authority or queued failure: writes=%d running=%d queued=%d", len(observer.calls), w.RunningCount(), len(w.queue))
		}
	}
	observer := &auditProbeObserver{}
	w := NewActiveProbeWorker(ActiveProbeWorkerConfig{Enabled: true, StateManager: observer, MaxAttempts: 1})
	w.applyProbeResult(t.Context(), probeTask{CredID: 7, Model: "minimax-m3"}, 1, &ProbeResult{Status: ProbeStatusSuccess})
	if len(observer.calls) != 1 || !observer.calls[0].Available {
		t.Fatalf("valid output did not recover: %+v", observer.calls)
	}
	for _, status := range []ProbeStatus{ProbeStatusAuth, ProbeStatusHTTP4xx} {
		observer.calls = nil
		w.applyProbeResult(t.Context(), probeTask{CredID: 7, Model: "minimax-m3"}, 1, &ProbeResult{Status: status})
		if len(observer.calls) != 1 || observer.calls[0].Available {
			t.Fatalf("definitive failure evidence was suppressed: %+v", observer.calls)
		}
	}
}

func TestModelProbeObservationPreservesConsensus(t *testing.T) {
	r := classifyProbeHTTPResponse(200, []byte(`{"choices":[{"message":{"reasoning_content":"thinking"}}]}`), nil, 1, "chat")
	change, applied, succ, fail, state := (&ModelProbeRunner{}).computeConsensus(r.status, r.category, "broken_confirmed", r.errCode, 2, 3)
	if applied || change != "unchanged" || succ != 2 || fail != 3 || state != "broken_confirmed" {
		t.Fatalf("inconclusive probe changed model consensus: applied=%v change=%s succ=%d fail=%d state=%s", applied, change, succ, fail, state)
	}
}

func TestModelProbeObservationDoesNotApplyState(t *testing.T) {
	config, err := pgxpool.ParseConfig("postgres://test@127.0.0.1:1/test?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	var databaseAttempts atomic.Int32
	config.ConnConfig.DialFunc = func(context.Context, string, string) (net.Conn, error) {
		databaseAttempts.Add(1)
		return nil, errors.New("test forbids database connection")
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	runner := &ModelProbeRunner{db: pool}
	for _, state := range []string{"healthy_confirmed", "broken_confirmed"} {
		ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
		runner.applyResult(ctx, probeTarget{CredentialID: 7, RawModel: "minimax-m3"}, "inconclusive", nil, "probe_response_inconclusive", "", 0, "unchanged", false, "test", 2, 3, state)
		cancel()
	}
	for _, status := range []string{"network", "http_5xx", "rate_limit", "auth"} {
		change, applied, succ, fail, state := runner.computeConsensus(status, probeCategoryProviderError, "broken_confirmed", status, 2, 3)
		ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
		runner.applyResult(ctx, probeTarget{CredentialID: 7, RawModel: "minimax-m3"}, status, nil, status, "", 0, change, applied, "test", succ, fail, state)
		cancel()
	}
	if got := databaseAttempts.Load(); got != 0 {
		t.Fatalf("observation attempted %d database connections", got)
	}
	// The pool spy must actually see writes when a conclusive result is applied.
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	runner.applyResult(ctx, probeTarget{CredentialID: 7, RawModel: "minimax-m3"}, "ok", nil, "", "", 0, "recovered", true, "test", 3, 0, "healthy_confirmed")
	if databaseAttempts.Load() == 0 {
		t.Fatal("database spy never observed the positive control")
	}
}

func TestActiveProbeNonAuthoritativeFailureRetainsState(t *testing.T) {
	for _, status := range []ProbeStatus{ProbeStatusHTTP5xx, ProbeStatusNetwork, ProbeStatusTimeout, ProbeStatusRate, ProbeStatusCanceled, ProbeStatusSkipped, ProbeStatusFailed} {
		observer := &auditProbeObserver{}
		w := NewActiveProbeWorker(ActiveProbeWorkerConfig{Enabled: true, StateManager: observer, MaxAttempts: 1})
		w.applyProbeResult(t.Context(), probeTask{CredID: 7, Model: "minimax-m3"}, 1, &ProbeResult{Status: status})
		if len(observer.calls) != 0 {
			t.Errorf("status %s falsely restored authority: %+v", status, observer.calls)
		}
	}
}

func TestActiveProbeDiagnosticsRedactCredentialEcho(t *testing.T) {
	key := "sk-proj1234567890abcdefghij"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"pong `+key+`"}}]}`)
	}))
	defer srv.Close()
	srv.Client().Timeout = 5 * time.Second
	executor := &ActiveProbeExecutor{httpClient: srv.Client()}
	target := &ProbeTarget{BaseURL: srv.URL, RawModel: "minimax-m3", Protocol: "openai-completions"}
	executor.SetGateway(srv.URL, "test-key", srv.Client())
	for _, result := range []*ProbeResult{executor.Run(t.Context(), target), executor.RunGateway(t.Context(), target)} {
		if result.Status != ProbeStatusSuccess {
			t.Fatalf("valid output lost: %+v", result)
		}
		if strings.Contains(result.RespPreview+result.ResponseBody+result.ErrMsg, key) {
			t.Fatal("credential echo reached persistent probe diagnostics")
		}
	}
}

func TestProbeObservationDiagnosticsAreNotGatewayFailure(t *testing.T) {
	r := &ProbeResult{Status: ProbeStatusSkipped, ObservationOnly: true}
	if got := classifyProbeErrorKind(r); got != "probe_direct_inconclusive" {
		t.Fatalf("kind = %s", got)
	}
	if got := classifyProbeFailureStage(r); got != "upstream" {
		t.Fatalf("stage = %s", got)
	}
}

func TestProbeNativeCapabilityDoesNotRecoverHealth(t *testing.T) {
	body := `{"id":"resp_budget","object":"response","status":"incomplete","output":[],"incomplete_details":{"reason":"max_output_tokens"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }))
	defer srv.Close()
	srv.Client().Timeout = 5 * time.Second
	target := &ProbeTarget{RawModel: "glm-5.2", BaseURL: srv.URL, Protocol: "openai-responses"}
	active := (&ActiveProbeExecutor{httpClient: srv.Client()}).Run(t.Context(), target)
	if !active.ObservationOnly || active.Status == ProbeStatusSuccess || active.SupportsResponses == nil || !*active.SupportsResponses {
		t.Fatalf("protocol evidence was confused with health: %+v", active)
	}
	for _, invalid := range []string{`{}`, `{"error":{"message":"overloaded"}}`, strings.Replace(body, `"incomplete"`, `"failed"`, 1), strings.Replace(body, `"max_output_tokens"`, `"unknown"`, 1), strings.Replace(body, `"output":[]`, `"output":null`, 1)} {
		if responsesProbeProtocolEvidence([]byte(invalid)) {
			t.Errorf("invalid native capability evidence: %s", invalid)
		}
	}
}

func TestProbeFallbackFailureKeepsActualScope(t *testing.T) {
	oldClient, oldBackoff := probeChatClient, probeBackoff
	defer func() { probeChatClient, probeBackoff = oldClient, oldBackoff }()
	probeBackoff = []time.Duration{0}
	for _, variant := range []struct {
		code   int
		prefix string
	}{
		{0, ""}, {401, ""}, {429, ""}, {503, ""},
		{0, strings.Repeat("diagnostic ", 80)}, {401, strings.Repeat("diagnostic ", 80)}, {429, strings.Repeat("diagnostic ", 80)}, {503, strings.Repeat("diagnostic ", 80)},
	} {
		code := variant.code
		nativeBody := `{"error":{"message":"` + variant.prefix + `This provider does not support the Responses API"}}`
		if variant.prefix != "" && strings.Contains(truncatePreview(nativeBody, 500), "does not support") {
			t.Fatal("fixture did not place native rejection beyond the preview")
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/responses") {
				w.WriteHeader(400)
				_, _ = io.WriteString(w, nativeBody)
				return
			}
			w.WriteHeader(code)
			_, _ = io.WriteString(w, `{"error":{"message":"provider unavailable"}}`)
		}))
		transport := srv.Client().Transport
		client := &http.Client{Timeout: 5 * time.Second, Transport: auditProbeRoundTripper(func(r *http.Request) (*http.Response, error) {
			if code == 0 && !strings.HasSuffix(r.URL.Path, "/responses") {
				return nil, errors.New("fallback transport failed")
			}
			return transport.RoundTrip(r)
		})}
		target := &ProbeTarget{RawModel: "minimax-m3", BaseURL: srv.URL, Protocol: "openai-responses"}
		active := (&ActiveProbeExecutor{httpClient: client}).Run(t.Context(), target)
		want := map[int]ProbeStatus{0: ProbeStatusNetwork, 401: ProbeStatusAuth, 429: ProbeStatusRate, 503: ProbeStatusHTTP5xx}[code]
		if active.Status != want || active.HTTPStatus != code || active.SupportsResponses == nil || *active.SupportsResponses {
			t.Errorf("fallback HTTP %d attributed to native protocol failure: %+v", code, active)
		}
		probeChatClient = client
		model := probeWithRetry(t.Context(), probeDescriptorFor(target.Protocol), probeTarget{RawModel: target.RawModel, BaseURL: srv.URL}, ProbeModeResponses)
		srv.Close()
		if model.category != probeCategoryProviderError || model.httpStatus != code || model.supportsResponses == nil || *model.supportsResponses {
			t.Errorf("fallback HTTP %d changed model scope: %+v", code, model)
		}
	}
}

func TestProbeFallbackCancellationRetainsNativeEvidence(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/responses") {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":{"message":"This provider does not support the Responses API"}}`)
			return
		}
		cancel()
		w.WriteHeader(503)
	}))
	defer srv.Close()
	oldClient, oldBackoff := probeChatClient, probeBackoff
	defer func() { probeChatClient, probeBackoff = oldClient, oldBackoff }()
	probeChatClient, probeBackoff = srv.Client(), []time.Duration{0, time.Hour}
	r := probeWithRetry(ctx, probeDescriptorFor("openai-responses"), probeTarget{RawModel: "minimax-m3", BaseURL: srv.URL}, ProbeModeResponses)
	if r.supportsResponses == nil || *r.supportsResponses || r.category != probeCategoryProviderError {
		t.Fatalf("cancellation erased native negative evidence: %+v", r)
	}
}

func TestProbeRetryInheritedCapabilityIsNotNewRejection(t *testing.T) {
	var nativeCalls, chatCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/responses") {
			if nativeCalls.Add(1) == 1 {
				w.WriteHeader(400)
				_, _ = io.WriteString(w, `{"error":{"message":"This provider does not support the Responses API"}}`)
				return
			}
		} else {
			chatCalls.Add(1)
		}
		w.WriteHeader(503)
		_, _ = io.WriteString(w, `{"error":{"message":"provider unavailable"}}`)
	}))
	defer srv.Close()
	oldClient, oldBackoff := probeChatClient, probeBackoff
	defer func() { probeChatClient, probeBackoff = oldClient, oldBackoff }()
	probeChatClient, probeBackoff = srv.Client(), []time.Duration{0, 0}
	r := probeWithRetry(t.Context(), probeDescriptorFor("openai-responses"), probeTarget{RawModel: "minimax-m3", BaseURL: srv.URL}, ProbeModeResponses)
	if nativeCalls.Load() != 2 || chatCalls.Load() != 1 || r.supportsResponses == nil || *r.supportsResponses || r.category != probeCategoryProviderError {
		t.Fatalf("old protocol evidence triggered a new fallback: native=%d chat=%d outcome=%+v", nativeCalls.Load(), chatCalls.Load(), r)
	}
}

func TestProbeFallbackConsumersRetainInconclusiveState(t *testing.T) {
	for _, valid := range []bool{false, true} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/responses") {
				w.WriteHeader(400)
				_, _ = io.WriteString(w, `{"error":{"message":"This provider does not support the Responses API"}}`)
				return
			}
			if valid {
				_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"pong"}}]}`)
			} else {
				_, _ = io.WriteString(w, `{"choices":[{"message":{"reasoning_content":"thinking"}}]}`)
			}
		}))
		srv.Client().Timeout = 5 * time.Second
		target := &ProbeTarget{CredentialID: 7, RawModel: "minimax-m3", Protocol: "openai-responses", BaseURL: srv.URL}
		active := (&ActiveProbeExecutor{httpClient: srv.Client()}).Run(t.Context(), target)
		if (active.Status == ProbeStatusSuccess) != valid || active.ObservationOnly == valid || active.SupportsResponses == nil || *active.SupportsResponses {
			t.Errorf("active fallback valid=%v: %+v", valid, active)
		}
		old := probeChatClient
		probeChatClient = srv.Client()
		model := probeWithRetry(t.Context(), probeDescriptorFor(target.Protocol), probeTarget{CredentialID: 7, RawModel: target.RawModel, BaseURL: srv.URL}, ProbeModeResponses)
		probeChatClient = old
		srv.Close()
		if (model.category == probeCategoryOK) != valid || (!valid && model.category != probeCategorySkipped) || model.supportsResponses == nil || *model.supportsResponses {
			t.Errorf("model fallback valid=%v: %+v", valid, model)
		}
	}
}
