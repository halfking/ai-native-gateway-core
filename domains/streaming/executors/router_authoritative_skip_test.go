package executors

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

func TestAuthoritativeSkipsCredentialStateManager(t *testing.T) {
	r := NewRouter(nil, nil)
	r.StateManager = nil // 兼容
	_ = r.PlanCandidates([]provider.Candidate{{CredentialID: 1, ProviderID: 1, RawModel: "m", Tier: 1}}, nil, &provider.Policy{}, nil)
}
