package bg

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type concurrentProbeCapabilityResult struct {
	ok      bool
	verdict *bool
	err     string
}

func TestF04_ConcurrentProbeVerdictsStayBoundToInvocation(t *testing.T) {
	fallbackStarted := make(chan struct{})
	releaseFallback := make(chan struct{})
	negativeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models", "/models":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		case "/v1/responses", "/responses":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(vapeurClaudeResponses400))
		case "/v1/chat/completions", "/chat/completions":
			close(fallbackStarted)
			select {
			case <-releaseFallback:
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
			case <-r.Context().Done():
			}
		default:
			t.Errorf("unexpected negative probe path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(negativeServer.Close)

	positiveServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models", "/models":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		case "/v1/responses", "/responses":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"resp_ok","object":"response","status":"completed","output":[]}`))
		default:
			t.Errorf("unexpected positive probe path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(positiveServer.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	probe := &CredentialProbeV2{}
	negativeSnapshot := v2Snapshot{
		ID:                1001,
		BaseURL:           negativeServer.URL,
		APIKey:            "negative-test-key",
		DefaultProbeModel: "gpt-5.6-terra",
		ProviderProtocol:  "openai-responses",
	}
	positiveSnapshot := v2Snapshot{
		ID:                1002,
		BaseURL:           positiveServer.URL,
		APIKey:            "positive-test-key",
		DefaultProbeModel: "gpt-5.6-terra",
		ProviderProtocol:  "openai-responses",
	}

	results := make(chan concurrentProbeCapabilityResult, 2)
	go func() {
		ok, errMsg := probe.probeCredential(ctx, negativeSnapshot)
		results <- concurrentProbeCapabilityResult{
			ok:      ok,
			verdict: probe.takeCapabilityVerdict(negativeSnapshot.ID, negativeSnapshot.DefaultProbeModel),
			err:     errMsg,
		}
	}()
	select {
	case <-fallbackStarted:
	case <-ctx.Done():
		t.Fatal("negative probe did not reach the blocked Chat fallback")
	}

	go func() {
		ok, errMsg := probe.probeCredential(ctx, positiveSnapshot)
		results <- concurrentProbeCapabilityResult{
			ok:      ok,
			verdict: probe.takeCapabilityVerdict(positiveSnapshot.ID, positiveSnapshot.DefaultProbeModel),
			err:     errMsg,
		}
	}()

	var positive concurrentProbeCapabilityResult
	select {
	case positive = <-results:
	case <-ctx.Done():
		t.Fatal("positive probe did not complete")
	}
	close(releaseFallback)

	var negative concurrentProbeCapabilityResult
	select {
	case negative = <-results:
	case <-ctx.Done():
		t.Fatal("negative probe did not complete")
	}
	if !positive.ok || positive.verdict == nil || !*positive.verdict {
		t.Fatalf("positive probe result = (ok=%t, verdict=%v, err=%q), want (true, true)", positive.ok, positive.verdict, positive.err)
	}
	if !negative.ok || negative.verdict == nil || *negative.verdict {
		t.Fatalf("negative probe result = (ok=%t, verdict=%v, err=%q), want (true, false)", negative.ok, negative.verdict, negative.err)
	}
}
