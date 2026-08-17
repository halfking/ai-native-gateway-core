package preprocess

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLeaseMutualExclusionAndRelease(t *testing.T) {
	store, _ := newTestStore(t, nil)
	ctx := context.Background()
	dep := h("dep")

	lease1, ok, err := store.AcquireBuildLease(ctx, "t", "s", ArtifactRaw, dep)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, ArtifactRaw, lease1.Kind())
	assert.NotEmpty(t, lease1.Token())

	// second acquisition of the same (session, kind, dep) fails
	_, ok, err = store.AcquireBuildLease(ctx, "t", "s", ArtifactRaw, dep)
	require.NoError(t, err)
	assert.False(t, ok, "lease must be mutually exclusive")

	// release enables re-acquisition
	require.NoError(t, lease1.Release(ctx))
	lease2, ok, err := store.AcquireBuildLease(ctx, "t", "s", ArtifactRaw, dep)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, lease2.Release(ctx))
}

// A lease is scoped to the dependency hash: a config change lets a new
// builder proceed immediately instead of waiting on a stale build.
func TestLeaseScopedToDependencyHash(t *testing.T) {
	store, _ := newTestStore(t, nil)
	ctx := context.Background()

	_, ok, err := store.AcquireBuildLease(ctx, "t", "s", ArtifactRaw, h("dep-v1"))
	require.NoError(t, err)
	require.True(t, ok)

	_, ok, err = store.AcquireBuildLease(ctx, "t", "s", ArtifactRaw, h("dep-v2"))
	require.NoError(t, err)
	assert.True(t, ok, "different dependency hash must get its own lease")
}

// Lease TTL expiry frees the lease even if the holder dies (fake clock via
// miniredis FastForward).
func TestLeaseTTLExpiry(t *testing.T) {
	store, mr := newTestStore(t, func(o *StoreOptions) { o.LeaseTTL = 100 * time.Millisecond })
	ctx := context.Background()

	lease, ok, err := store.AcquireBuildLease(ctx, "t", "s", ArtifactRaw, h("dep"))
	require.NoError(t, err)
	require.True(t, ok)

	mr.FastForward(150 * time.Millisecond)

	// expired lease is acquirable again; stale release is a no-op
	_, ok, err = store.AcquireBuildLease(ctx, "t", "s", ArtifactRaw, h("dep"))
	require.NoError(t, err)
	require.True(t, ok, "expired lease must be acquirable")
	assert.NoError(t, lease.Release(ctx), "releasing an expired/overtaken lease must not error")
}

func TestLeaseReleaseByWrongTokenIsNoop(t *testing.T) {
	store, mr := newTestStore(t, nil)
	ctx := context.Background()
	dep := h("dep")

	lease, ok, err := store.AcquireBuildLease(ctx, "t", "s", ArtifactRaw, dep)
	require.NoError(t, err)
	require.True(t, ok)

	// steal the lease server-side (simulates an admin/op overwrite)
	require.NoError(t, mr.Set(store.leaseKey("t", "s", ArtifactRaw, dep), "someone-else"))

	require.NoError(t, lease.Release(ctx))
	val, err := mr.Get(store.leaseKey("t", "s", ArtifactRaw, dep))
	require.NoError(t, err)
	assert.Equal(t, "someone-else", val, "release with a stale token must not delete the new holder's lease")
}
