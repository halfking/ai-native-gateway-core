package admin

import (
	"os"
	"strings"
	"testing"
)

func TestRoutingResolveUsesAuthenticatedTenantScope(t *testing.T) {
	source, err := os.ReadFile("routing.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	start := strings.Index(text, "func (h *Handler) handleRoutingResolve")
	end := strings.Index(text, "func (h *Handler) handleRoutingCandidateBindingUpdate")
	if start < 0 || end < 0 || end <= start {
		t.Fatal("handleRoutingResolve bounds not found")
	}
	resolve := text[start:end]
	for _, want := range []string{
		"v.tenant_id,",
		"($2 = '' OR v.tenant_id = $2)",
		"v.tenant_id = p.tenant_id",
		"v.tenant_id = c.tenant_id",
		"rawModels, EffectiveTenantIDAll(r)",
		"&c.CredentialID, &c.TenantID, &c.CredentialLabel",
		"TenantID:     c.TenantID",
	} {
		if !strings.Contains(resolve, want) {
			t.Errorf("resolve tenant contract missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"p.tenant_id = 'default'",
		"TenantID:     \"default\"",
	} {
		if strings.Contains(resolve, forbidden) {
			t.Errorf("resolve tenant contract contains forbidden default scope %q", forbidden)
		}
	}
}
