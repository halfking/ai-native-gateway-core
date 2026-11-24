package streaming

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
)

func TestTrustedPlatformOriginRequiresAuthenticatedSystemOwner(t *testing.T) {
	ctx := context.Background()
	owner := "credential-selfcheck-worker"
	stage, actor, ok := trustedPlatformOrigin(ctx, &authentication.KeyInfo{OwnerUser: &owner, IsSystem: true})
	if !ok || stage != "self_check" || actor != owner {
		t.Fatalf("trusted system owner => (%q,%q,%v), want self_check and owner", stage, actor, ok)
	}
	if got := platformOriginForKey(ctx, &authentication.KeyInfo{OwnerUser: &owner, IsSystem: true}); got != "self_check" {
		t.Fatalf("platform origin = %q, want self_check", got)
	}
	// owner_user is mutable by tenant admins, so matching an allowlisted label
	// alone must not grant the billing exemption; is_system is DB provenance.
	if _, _, ok := trustedPlatformOrigin(ctx, &authentication.KeyInfo{OwnerUser: &owner, IsSystem: false}); ok {
		t.Fatal("ordinary key with a matching owner_user must not be platform classified")
	}

	spoofedOwner := "ordinary-tenant"
	if _, _, ok := trustedPlatformOrigin(ctx, &authentication.KeyInfo{OwnerUser: &spoofedOwner, IsSystem: true}); ok {
		t.Fatal("ordinary authenticated tenant must not be platform classified")
	}
	if _, _, ok := trustedPlatformOrigin(ctx, nil); ok {
		t.Fatal("missing authenticated identity must not be platform classified")
	}
	staticSentinel := "global-auth-passed"
	if _, _, ok := trustedPlatformOrigin(ctx, &authentication.KeyInfo{OwnerUser: &staticSentinel, IsSystem: true}); ok {
		t.Fatal("global auth sentinel must not grant platform billing exemption")
	}
}
