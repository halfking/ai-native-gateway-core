package relay

import (
	"context"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/audit"
	"github.com/kaixuan/llm-gateway-go/circuit"
	"github.com/kaixuan/llm-gateway-go/limiter"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/routing"
)

type staticProviderResolver struct {
	candidate provider.Candidate
	policy    *provider.Policy
}

func (s *staticProviderResolver) Enabled() bool { return true }

func (s *staticProviderResolver) GetCandidates(context.Context, string, string) ([]provider.Candidate, *provider.Policy, error) {
	return []provider.Candidate{s.candidate}, s.policy, nil
}

func attachTestExecutor(h *ChatHandler, cm *circuit.Manager, lim *limiter.Limiter, baseURL string) {
	sticky := routing.NewStickyCache()
	candidate := provider.Candidate{
		CredentialID:      1,
		ProviderID:        1,
		BaseURL:           baseURL,
		Protocol:          "openai-completions",
		CatalogCode:       "test",
		RawModel:          "test-model",
		Tier:              1,
		Weight:            100,
		BillingMode:       "token_plan",
		Routable:          true,
		LifecycleStatus:   "active",
		AvailabilityState: "ready",
		QuotaState:        "ok",
		CircuitState:      "closed",
		APIKey:            "sk-test",
	}
	exec := routing.NewExecutor(
		routing.NewRouter(sticky, lim),
		cm,
		lim,
		pool.NewPoolManager(nil),
		nil,
		func(chunk []byte, isStream bool) []byte { return chunk },
		func(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel string, norm routing.NormalizerFunc, capture *audit.StreamCapture, toolsRequested bool) routing.StreamOutcome {
			return StreamChatWithCaptureAndToolFallback(w, resp, clientModel, outboundModel, NewNormalizer(), capture, toolsRequested, StripMinimaxFieldsBody)
		},
		nil,
	)
	exec.StripMinimaxFields = StripMinimaxFieldsBody
	h.SetExecutor(exec, &staticProviderResolver{candidate: candidate, policy: provider.DefaultPolicy()}, sticky)
}
