package admin

import (
	"context"
	"reflect"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// Wave 1 A1 回归钉桩：/api/routing/resolve 的 plan_order 必须与真实选路
// 同源（Router.PlanCandidatesPinned），不得恒为空。

type fakeLiveResolver struct{ cands []provider.Candidate }

func (f *fakeLiveResolver) GetCandidates(ctx context.Context, model, profile, tenantID string) ([]provider.Candidate, *provider.Policy, error) {
	return f.cands, provider.DefaultPolicy(), nil
}

func livePlanOrderTestCandidate(credID int, tier, weight int) provider.Candidate {
	return provider.Candidate{
		CredentialID:     credID,
		ProviderID:       credID * 10,
		Routable:         true,
		LifecycleStatus:  "active",
		RawModel:         "glm-5.2",
		StandardizedName: "glm-5.2",
		Tier:             tier,
		Weight:           weight,
	}
}

func TestLivePlanOrder_UnavailableWhenNotInjected(t *testing.T) {
	h := &Handler{}
	entries, source := h.livePlanOrder(context.Background(), "glm-5.2", "")
	if source != "unavailable" {
		t.Errorf("source = %q, want unavailable", source)
	}
	if len(entries) != 0 {
		t.Errorf("entries = %v, want empty", entries)
	}
}

func TestLivePlanOrder_LiveRouterProducesPlanOrder(t *testing.T) {
	cands := []provider.Candidate{
		livePlanOrderTestCandidate(101, 2, 100),
		livePlanOrderTestCandidate(202, 1, 100),
	}
	h := &Handler{}
	h.SetLiveRoutingSource(&LiveRoutingSource{
		Router:   executors.NewRouter(nil, nil),
		Resolver: &fakeLiveResolver{cands: cands},
	})

	entries, source := h.livePlanOrder(context.Background(), "glm-5.2", "")
	if source != "live-router" {
		t.Fatalf("source = %q, want live-router", source)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2 (both candidates available)", len(entries))
	}

	// 同源自洽：顺序必须与直接调用同一 Router 的结果逐位一致。
	router := executors.NewRouter(nil, nil)
	expected := router.PlanCandidatesPinned(context.Background(), cands, nil, nil, provider.DefaultPolicy(), nil, "", "glm-5.2", "admin-resolve-test")
	if len(expected) != len(entries) {
		t.Fatalf("router planned %d, entries %d", len(expected), len(entries))
	}
	for i, e := range entries {
		if e["credential_id"] != expected[i].CredentialID {
			t.Errorf("entries[%d].credential_id = %v, want %d", i, e["credential_id"], expected[i].CredentialID)
		}
		if e["rank"] != i+1 {
			t.Errorf("entries[%d].rank = %v, want %d", i, e["rank"], i+1)
		}
		for _, key := range []string{"provider_id", "raw_model", "tier", "weight"} {
			if _, ok := e[key]; !ok {
				t.Errorf("entries[%d] missing key %q", i, key)
			}
		}
	}
}

func TestLivePlanOrder_ResolverErrorFallsBackUnavailable(t *testing.T) {
	h := &Handler{}
	h.SetLiveRoutingSource(&LiveRoutingSource{
		Router:   executors.NewRouter(nil, nil),
		Resolver: fakeErrResolver{},
	})
	_, source := h.livePlanOrder(context.Background(), "glm-5.2", "")
	if source != "unavailable" {
		t.Errorf("source = %q, want unavailable", source)
	}
}

type fakeErrResolver struct{}

func (fakeErrResolver) GetCandidates(ctx context.Context, model, profile, tenantID string) ([]provider.Candidate, *provider.Policy, error) {
	return nil, nil, context.DeadlineExceeded
}

func TestBuildOrderDebug_FirstMatchAndNote(t *testing.T) {
	cands := []resolveCandidate{{CredentialID: 7}, {CredentialID: 8}}

	match := buildOrderDebug(
		[]map[string]any{{"credential_id": 7}, {"credential_id": 8}},
		"live-router", cands)
	if match["first_match"] != true {
		t.Errorf("first_match = %v, want true", match["first_match"])
	}
	if _, has := match["note"]; has {
		t.Errorf("unexpected note on matching order: %v", match)
	}

	mismatch := buildOrderDebug(
		[]map[string]any{{"credential_id": 8}},
		"live-router", cands)
	if mismatch["first_match"] != false {
		t.Errorf("first_match = %v, want false", mismatch["first_match"])
	}
	if mismatch["note"] == "" {
		t.Errorf("expected disagreement note, got %v", mismatch)
	}

	unavail := buildOrderDebug(nil, "unavailable", cands)
	if !reflect.DeepEqual(unavail, map[string]any{"plan_order_source": "unavailable"}) {
		t.Errorf("unavailable debug = %v", unavail)
	}
}
