package retryowner

import (
	"context"
	"testing"
)

func TestOwnerDefaultsToLegacy(t *testing.T) {
	if got := OwnerFrom(context.Background()); got != Legacy {
		t.Fatalf("OwnerFrom(background) = %q, want %q", got, Legacy)
	}
}

func TestFreezeOwnerFirstWins(t *testing.T) {
	ctx := FreezeOwner(context.Background(), Survival)
	if got := OwnerFrom(ctx); got != Survival {
		t.Fatalf("OwnerFrom = %q, want %q", got, Survival)
	}
	// A disagreeing second freeze must not change the frozen owner.
	ctx2 := FreezeOwner(ctx, Legacy)
	if got := OwnerFrom(ctx2); got != Survival {
		t.Fatalf("re-freeze with different owner changed decision: %q, want %q", got, Survival)
	}
	// Agreeing re-freeze is a no-op but keeps the value.
	ctx3 := FreezeOwner(ctx, Survival)
	if got := OwnerFrom(ctx3); got != Survival {
		t.Fatalf("agreeing re-freeze lost owner: %q", got)
	}
}

func TestPathDefaultsToLegacyLoop(t *testing.T) {
	if got := PathFrom(context.Background()); got != PathLegacyLoop {
		t.Fatalf("PathFrom(background) = %q, want %q", got, PathLegacyLoop)
	}
}

func TestFreezePathFirstWins(t *testing.T) {
	ctx := FreezePath(context.Background(), PathDispatchV2)
	if got := PathFrom(ctx); got != PathDispatchV2 {
		t.Fatalf("PathFrom = %q, want %q", got, PathDispatchV2)
	}
	if got := PathFrom(FreezePath(ctx, PathLegacyLoop)); got != PathDispatchV2 {
		t.Fatalf("re-freeze changed path: %q, want %q", got, PathDispatchV2)
	}
}

// TestOwnerAndPathCoexist guards against key collisions between the two
// context values.
func TestOwnerAndPathCoexist(t *testing.T) {
	ctx := FreezeOwner(FreezePath(context.Background(), PathDispatchV2), Survival)
	if got := OwnerFrom(ctx); got != Survival {
		t.Fatalf("owner = %q, want survival", got)
	}
	if got := PathFrom(ctx); got != PathDispatchV2 {
		t.Fatalf("path = %q, want dispatch_v2", got)
	}
}
