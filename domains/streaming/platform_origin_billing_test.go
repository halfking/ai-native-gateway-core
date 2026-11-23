package streaming

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
)

func TestTrustedPlatformOriginRequiresAuthenticatedSystemOwner(t *testing.T) {
	ctx := context.Background()
	owner := "credential-selfcheck-worker"
	stage, actor, ok := trustedPlatformOrigin(ctx, &authentication.KeyInfo{OwnerUser: &owner})
	if !ok || stage != "self_check" || actor != owner {
		t.Fatalf("trusted system owner => (%q,%q,%v), want self_check and owner", stage, actor, ok)
	}

	spoofedOwner := "ordinary-tenant"
	if _, _, ok := trustedPlatformOrigin(ctx, &authentication.KeyInfo{OwnerUser: &spoofedOwner}); ok {
		t.Fatal("ordinary authenticated tenant must not be platform classified")
	}
	if _, _, ok := trustedPlatformOrigin(ctx, nil); ok {
		t.Fatal("missing authenticated identity must not be platform classified")
	}
	staticSentinel := "global-auth-passed"
	if _, _, ok := trustedPlatformOrigin(ctx, &authentication.KeyInfo{OwnerUser: &staticSentinel}); ok {
		t.Fatal("global auth sentinel must not grant platform billing exemption")
	}
}
