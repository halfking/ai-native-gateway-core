package executors

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	ursmv2 "github.com/kaixuan/llm-gateway-go/domains/ursm/v2"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/provider"
)

func TestStrictCanaryDoesNotFallbackRejectedScopedCandidate(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := ursmv2.DefaultConfig()
	cfg.Mode = api.ModeCanary
	cfg.StrictCanary = true
	cfg.CanaryTenants = []string{"canary"}
	cfg.CanaryCredentials = []int{1}
	cfg.CanaryModels = []string{"glm-5.2"}
	cfg.RedisKeyPrefix = "ursm:v2:canary:"
	mgr := ursmv2.New(ursmv2.Dependencies{Redis: rdb, Config: cfg})
	if err := mgr.SetReady(context.Background(), true); err != nil {
		t.Fatal(err)
	}

	router := NewRouter(nil, nil)
	router.URSMv2 = mgr
	candidates := []provider.Candidate{
		{CredentialID: 1, ProviderID: 1, RawModel: "glm-5.2", Tier: 1, Routable: true, LifecycleStatus: "active"},
		{CredentialID: 2, ProviderID: 1, RawModel: "glm-5.2", Tier: 1, Routable: true, LifecycleStatus: "active"},
	}
	got := router.PlanCandidatesWithContext(context.Background(), candidates, nil, &provider.Policy{}, nil, "canary", "glm-5.2", "scope-fallback")
	if len(got) != 1 || got[0].CredentialID != 2 {
		t.Fatalf("strict canary route=%+v, want only out-of-scope legacy credential 2", got)
	}
}
