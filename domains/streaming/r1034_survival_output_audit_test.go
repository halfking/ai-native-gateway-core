package streaming

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
)

func TestR1034SurvivalOutputBlockKeepsPolicyFailure(t *testing.T) {
	const sse = `data: {"id":"x","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"blocked provider text"}}]}` + "\n\n" + "data: [DONE]\n\n"
	attempt := &nativePolicyBlockAttemptExecutor{protocol: "openai-chat"}
	hook := &nativeNonStreamInterceptor{blocked: true}
	interceptor := response.NewInterceptorChain(hook)
	rec, entries := runChatOutputCommitWith(t, interceptor, nil, `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
		func(_ *executors.Executor, h *ChatHandler, _ *http.Request) {
			h.survivalAttemptExec = attempt
			h.SetRequestSurvival(func(string) bool { return true }, SurvivalOptions{Deadline: 5 * time.Second, RetryBase: time.Millisecond, RetryInterval: time.Millisecond, MaxRetries: 1})
		}, chatUpstreamFixture{body: sse, contentType: "text/event-stream"})
	if hook.seenCalls == 0 {
		t.Fatal("test never reached the output interceptor")
	}
	if strings.Count(rec.Body.String(), "Response blocked by output policy") != 1 {
		t.Fatalf("duplicate or missing terminal: %q", rec.Body.String())
	}
	failures := 0
	for _, e := range entries {
		if e.Success {
			t.Error("policy block recorded as success")
		}
		if e.ErrorKind != nil && *e.ErrorKind == "output_policy_blocked" {
			failures++
		}
		if e.ErrorKind != nil && *e.ErrorKind == "gateway_survival_fail_closed" {
			t.Error("policy block downgraded to survival failure")
		}
	}
	if failures != 1 {
		t.Fatalf("policy failure rows=%d", failures)
	}
}
