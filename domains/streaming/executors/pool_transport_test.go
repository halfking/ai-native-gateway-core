package executors

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/identity" //nolint:depguard // executor transport integration test
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

type executorPoolRoundTripper func(*http.Request) (*http.Response, error)

func (f executorPoolRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestExecuteOpenAIUsesIdentityBoundPoolTransport(t *testing.T) {
	pm := pool.NewPoolManager(nil)
	t.Cleanup(func() {
		pm.CloseAll()
		pm.Stop()
	})

	key := pool.PoolKey{IdentityHash: "pool-transport", ProviderID: 41, CredentialID: 42}
	p := pm.GetOrCreate(key, "")
	if p == nil {
		t.Fatal("pool manager returned nil")
	}
	var poolCalls atomic.Int32
	p.Client().Transport = executorPoolRoundTripper(func(req *http.Request) (*http.Response, error) {
		poolCalls.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"chatcmpl_pool","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]} `)),
			Request:    req,
		}, nil
	})

	exec := &Executor{
		Circuit:         newCircuitManagerForTest(),
		Limiter:         newLimiterForTest(),
		Pools:           pm,
		Upstream:        upstreampkg.NewWithRetries(0),
		UpstreamTimeout: time.Second,
		StreamTimeout:   time.Second,
	}
	t.Cleanup(func() {
		exec.Limiter.Stop()
		exec.Upstream.Stop()
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	result, err := exec.executeOpenAI(&ExecParams{
		W:              httptest.NewRecorder(),
		R:              req,
		BodyBytes:      []byte(`{"model":"pool-model","messages":[]}`),
		ClientProtocol: "openai-completions",
		ClientModel:    "pool-model",
		ClientID:       identity.ClientIdentity{IdentityHash: key.IdentityHash},
	}, provider.Candidate{
		ProviderID:   key.ProviderID,
		CredentialID: key.CredentialID,
		BaseURL:      "https://must-not-dial.invalid",
		Protocol:     "openai-completions",
		CatalogCode:  "openai",
		RawModel:     "pool-model",
		APIKey:       "sk-pool",
	}, 0, time.Now(), nil)
	if err != nil {
		t.Fatalf("executeOpenAI: %v", err)
	}
	if result == nil {
		t.Fatal("executeOpenAI returned nil result")
	}
	if got := poolCalls.Load(); got != 1 {
		t.Fatalf("pool transport calls = %d, want 1; request may have only acquired the semaphore", got)
	}
	if p.State() != pool.PoolActive {
		t.Fatalf("pool state after success = %s, want active", p.State())
	}
}

func TestExecuteOpenAIClientCancelUsesPoolTransportAndReleasesSlot(t *testing.T) {
	pm := pool.NewPoolManager(nil)
	t.Cleanup(func() {
		pm.CloseAll()
		pm.Stop()
	})

	key := pool.PoolKey{IdentityHash: "pool-cancel", ProviderID: 51, CredentialID: 52}
	p := pm.GetOrCreate(key, "")
	if p == nil {
		t.Fatal("pool manager returned nil")
	}
	started := make(chan struct{})
	var poolCalls atomic.Int32
	p.Client().Transport = executorPoolRoundTripper(func(req *http.Request) (*http.Response, error) {
		poolCalls.Add(1)
		close(started)
		<-req.Context().Done()
		return nil, req.Context().Err()
	})

	exec := &Executor{
		Circuit:         newCircuitManagerForTest(),
		Limiter:         newLimiterForTest(),
		Pools:           pm,
		Upstream:        upstreampkg.NewWithRetries(0),
		UpstreamTimeout: time.Minute,
		StreamTimeout:   time.Minute,
	}
	t.Cleanup(func() {
		exec.Limiter.Stop()
		exec.Upstream.Stop()
	})

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
	result := make(chan error, 1)
	go func() {
		_, err := exec.executeOpenAI(&ExecParams{
			W:              httptest.NewRecorder(),
			R:              req,
			BodyBytes:      []byte(`{"model":"pool-model","messages":[]}`),
			ClientProtocol: "openai-completions",
			ClientModel:    "pool-model",
			ClientID:       identity.ClientIdentity{IdentityHash: key.IdentityHash},
		}, provider.Candidate{
			ProviderID:   key.ProviderID,
			CredentialID: key.CredentialID,
			BaseURL:      "https://must-not-dial.invalid",
			Protocol:     "openai-completions",
			CatalogCode:  "openai",
			RawModel:     "pool-model",
			APIKey:       "sk-pool",
		}, 0, time.Now(), nil)
		result <- err
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("pool transport was not invoked")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("executeOpenAI error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled executor request did not return")
	}
	if got := poolCalls.Load(); got != 1 {
		t.Fatalf("pool transport calls = %d, want 1", got)
	}

	for i := 0; i < 32; i++ {
		acquireCtx, acquireCancel := context.WithTimeout(context.Background(), time.Second)
		err := p.Acquire(acquireCtx)
		acquireCancel()
		if err != nil {
			t.Fatalf("Acquire %d after canceled request: %v (executor slot leaked)", i, err)
		}
	}
	for i := 0; i < 32; i++ {
		p.Release()
	}
}
