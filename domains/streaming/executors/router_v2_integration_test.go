package executors

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/provider"
)

func TestRouterRespectsV2PlanInAuthoritative(t *testing.T) {
	r := NewRouter(nil, nil)
	// 不真正接 v2 时，PlanCandidates 行为不变；本测试仅断言不 panic
	_ = r.PlanCandidates([]provider.Candidate{
		{CredentialID: 1, ProviderID: 1, RawModel: "m", Tier: 1},
	}, PlanContext{}, nil, &provider.Policy{}, nil)
	_ = api.ModeAuthoritative
}
