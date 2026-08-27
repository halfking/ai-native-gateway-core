package executors_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/domains/identity"
	"github.com/kaixuan/llm-gateway-go/domains/streaming"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
)

func TestGLM52PrematureEOFFailsOverThroughRealStreamBridge(t *testing.T) {
	var (
		mu       sync.Mutex
		authSeen []string
		models   []string
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode upstream request: %v", err)
		}

		mu.Lock()
		authSeen = append(authSeen, r.Header.Get("Authorization"))
		models = append(models, body.Model)
		attempt := len(authSeen)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		if attempt <= 3 {
			_, _ = fmt.Fprint(w, "data: {\"id\":\"failed-node-metadata\",\"object\":\"chat.completion.chunk\",\"model\":\"z-ai/glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n")
			return
		}
		_, _ = fmt.Fprint(w, "data: {\"id\":\"ok\",\"object\":\"chat.completion.chunk\",\"model\":\"z-ai/glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"final-answer\"},\"finish_reason\":null}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	limiter := credential.NewLimiter()
	defer limiter.Stop()
	exec := executors.NewExecutor(
		executors.NewRouter(executors.NewStickyCache(), limiter),
		credential.NewManager(),
		limiter,
		pool.NewPoolManager(nil),
		nil,
		func(chunk []byte, _ bool) []byte { return chunk },
		// P1-2 fix (2026-08-28): Added ctx parameter to match new signature.
		func(ctx context.Context, w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, _ string, _ executors.NormalizerFunc, capture *audit.StreamCapture, toolsRequested bool) executors.StreamOutcome {
			out := streaming.StreamChatWithPendingCapture(ctx, w, resp, clientModel, outboundModel, streaming.NewNormalizer(), capture, toolsRequested, nil, nil)
			return executors.StreamOutcome{
				Interrupted: out.Interrupted,
				Reason:      out.Reason,
				Resumable:   out.Resumable,
				ChunkCount:  out.ChunkCount,
				Kind:        out.Kind,
			}
		},
		nil,
	)
	pipeline := exec.NewDispatchPipeline()
	pipeline.Start()
	defer pipeline.Stop()
	exec.SetDispatchPipeline(pipeline)

	candidate := func(providerID, credentialID int, apiKey string) provider.Candidate {
		return provider.Candidate{
			ProviderID:        providerID,
			CredentialID:      credentialID,
			BaseURL:           upstream.URL,
			Protocol:          "openai-completions",
			CatalogCode:       "openai",
			Tier:              1,
			Weight:            100,
			RawModel:          "z-ai/glm-5.2",
			OfferRawModel:     "z-ai/glm-5.2",
			APIKey:            apiKey,
			BillingMode:       "token_plan",
			Routable:          true,
			LifecycleStatus:   "active",
			AvailabilityState: "ready",
			QuotaState:        "ok",
			CircuitState:      "closed",
		}
	}
	candidates := []provider.Candidate{
		candidate(18, 209423, "key-1"),
		candidate(19, 209424, "key-2"),
	}

	recorder := httptest.NewRecorder()
	result, err := exec.Execute(&executors.ExecParams{
		W:                           recorder,
		R:                           httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes:                   []byte(`{"model":"glm-5.2","messages":[],"stream":true}`),
		Model:                       "glm-5.2",
		ClientModel:                 "glm-5.2",
		ClientProtocol:              "openai-completions",
		IsStream:                    true,
		ClientID:                    identity.ClientIdentity{IdentityHash: "glm-52-real-bridge-failover"},
		Candidates:                  candidates,
		Policy:                      &provider.Policy{TierFallbackMax: 4, RetryPerCredential: 0},
		DispatchAllowProviderChange: true,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result == nil {
		t.Fatal("expected successful failover result")
	}

	body := recorder.Body.String()
	if strings.Contains(body, "failed-node-metadata") {
		t.Fatalf("failed candidate metadata leaked to client: %q", body)
	}
	if strings.Contains(body, "eof_without_done") || strings.Contains(body, "upstream_error") {
		t.Fatalf("upstream failure leaked to client: %q", body)
	}
	if !strings.Contains(body, "final-answer") || !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("client did not receive the successful stream: %q", body)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(authSeen) != 4 || authSeen[0] != authSeen[1] || authSeen[1] != authSeen[2] || authSeen[2] == authSeen[3] {
		t.Fatalf("authorization attempts = %v, want A,A,A,B", authSeen)
	}
	if len(models) != 4 {
		t.Fatalf("upstream models = %v, want four attempts", models)
	}
	for _, model := range models {
		if model != "z-ai/glm-5.2" {
			t.Fatalf("upstream models = %v, want candidate model on every attempt", models)
		}
	}
}
