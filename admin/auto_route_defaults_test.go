package admin

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDefaultRoutingUpdateReqJSON(t *testing.T) {
	raw := `{
		"tier": "secondary",
		"priority": 80,
		"reason": "ops note",
		"profile": "smart",
		"canonical_model": "claude-sonnet-4.5",
		"tenant_id": "acme",
		"clear_tenant": false,
		"expires_at": "2030-01-02T03:04:05Z",
		"clear_expires": false
	}`
	var req DefaultRoutingUpdateReq
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.Tier == nil || *req.Tier != "secondary" {
		t.Fatalf("tier=%v", req.Tier)
	}
	if req.Priority == nil || *req.Priority != 80 {
		t.Fatalf("priority=%v", req.Priority)
	}
	if req.Profile == nil || *req.Profile != "smart" {
		t.Fatalf("profile=%v", req.Profile)
	}
	if req.CanonicalModel == nil || *req.CanonicalModel != "claude-sonnet-4.5" {
		t.Fatalf("model=%v", req.CanonicalModel)
	}
	if req.TenantID == nil || *req.TenantID != "acme" {
		t.Fatalf("tenant=%v", req.TenantID)
	}
	if req.ClearTenant || req.ClearExpires {
		t.Fatalf("clear flags should be false")
	}
	if req.ExpiresAt == nil || !req.ExpiresAt.Equal(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("expires=%v", req.ExpiresAt)
	}
}

func TestValidDefaultTiersAndProfiles(t *testing.T) {
	for _, tier := range []string{"primary", "secondary", "fallback"} {
		if !validDefaultTiers[tier] {
			t.Fatalf("expected tier %q valid", tier)
		}
	}
	if validDefaultTiers["other"] {
		t.Fatal("other tier should be invalid")
	}
	for _, p := range []string{"", "smart", "speed_first", "cost_first"} {
		if !validDefaultProfiles[p] {
			t.Fatalf("expected profile %q valid", p)
		}
	}
	if validDefaultProfiles["balanced"] {
		t.Fatal("balanced profile should be invalid")
	}
}
