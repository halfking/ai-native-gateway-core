package streaming

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// resolveCandidatesForRequest derives the request modality from the raw body
// and asks the provider resolver for candidates that can actually serve it.
func resolveCandidatesForRequest(ctx context.Context, resolver providerResolver, model, profile, tenantID string, body []byte) ([]provider.Candidate, *provider.Policy, string, error) {
	modality := detectRequestModality(body)
	candidates, policy, err := resolver.GetCandidatesByModality(ctx, model, profile, tenantID, modality)
	return candidates, policy, modality, err
}
