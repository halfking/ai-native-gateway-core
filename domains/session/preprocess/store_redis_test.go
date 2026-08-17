package preprocess

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// redisClient opens a raw client for peeking at stored bytes in tests.
func redisClient(t *testing.T, addr string) *redis.Client {
	t.Helper()
	c := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func sampleArtifact(tenant, session string, kind ArtifactKind, variant string, payload []byte) *SessionArtifact {
	return &SessionArtifact{
		TenantID:       tenant,
		SessionID:      session,
		Kind:           kind,
		Variant:        variant,
		SourceHash:     HashBytes([]byte("source-" + string(kind))),
		DependencyHash: HashBytes([]byte("dep-" + string(kind))),
		Payload:        payload,
		SchemaVersion:  7,
		TransformVer:   3,
		Tokens:         42,
	}
}

func TestStorePutGetRoundTripAllKinds(t *testing.T) {
	store, _ := newTestStore(t, nil)
	ctx := context.Background()
	tenant, session := "tenant-a", "sess-1"

	for _, kind := range []ArtifactKind{ArtifactRaw, ArtifactSanitized, ArtifactCompressed} {
		variant := "default"
		if kind == ArtifactCompressed {
			variant = "gpt4o"
		}
		art := sampleArtifact(tenant, session, kind, variant, []byte("payload-"+string(kind)))
		ok, err := store.PutCAS(ctx, art, SessionRevision{})
		require.NoError(t, err)
		require.True(t, ok, "create PutCAS for %s must succeed", kind)

		got, found, err := store.Get(ctx, tenant, session, kind, variant)
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, art.Payload, got.Payload)
		assert.Equal(t, art.ContentHash, got.ContentHash)
		assert.Equal(t, art.DependencyHash, got.DependencyHash)
		assert.Equal(t, art.SchemaVersion, got.SchemaVersion)
		assert.True(t, got.VerifyContentHash())
	}
}

// UT-SA-10: PutCAS succeeds only when the expected revision matches.
func TestStorePutCASCreateAndConflict(t *testing.T) {
	store, _ := newTestStore(t, nil)
	ctx := context.Background()

	// create against empty manifest with zero expectation
	ok, err := store.PutCAS(ctx, sampleArtifact("t", "s", ArtifactRaw, "", []byte("p1")), SessionRevision{})
	require.NoError(t, err)
	require.True(t, ok)

	// advance the manifest revision via a raw append
	mut := SessionMutation{TenantID: "t", SessionID: "s", RequestID: "r1", TurnNo: 1, Mutation: MutationAppendDelta, DeltaHash: h("d1"), DependencyHash: h("dep")}
	rev1, err := store.AppendRawTurn(ctx, mut, SessionRevision{})
	require.NoError(t, err)

	// stale expectation (zero) must fail and must not overwrite the meta
	stale := sampleArtifact("t", "s", ArtifactSanitized, "", []byte("p2"))
	stale.DependencyHash = h("stale-dep")
	ok, err = store.PutCAS(ctx, stale, SessionRevision{})
	require.NoError(t, err)
	assert.False(t, ok, "PutCAS with stale expected revision must return false")

	m, err := store.GetManifest(ctx, "t", "s")
	require.NoError(t, err)
	assert.Equal(t, StatusStale, m.Sanitized.Status, "conflicting PutCAS must not touch the manifest meta")
	assert.True(t, isZeroHash(m.Sanitized.DependencyHash), "conflicting PutCAS must not write its dependency hash")
	assert.Equal(t, rev1, m.Revision)

	// correct expectation succeeds
	fresh := sampleArtifact("t", "s", ArtifactSanitized, "", []byte("p3"))
	ok, err = store.PutCAS(ctx, fresh, rev1)
	require.NoError(t, err)
	require.True(t, ok)
	m, err = store.GetManifest(ctx, "t", "s")
	require.NoError(t, err)
	assert.Equal(t, StatusReady, m.Sanitized.Status)
	assert.True(t, m.Flags.Has(FlagSanitizedReady))
}

// UT-SA-11: raw payloads are AES-GCM envelope encrypted at rest; plaintext
// never appears in Redis; wrong key / tampered ciphertext fails decryption.
func TestStoreRawEncryptedAtRest(t *testing.T) {
	store, mr := newTestStore(t, nil)
	ctx := context.Background()
	payload := []byte("PII: user phone +86 138 0000 0000")

	ok, err := store.PutCAS(ctx, sampleArtifact("t", "s", ArtifactRaw, "", payload), SessionRevision{})
	require.NoError(t, err)
	require.True(t, ok)

	atRest := mr.HGet(store.artifactKey("t", "s", ArtifactRaw, "default"), "payload")
	assert.NotContains(t, atRest, "PII", "raw payload must not be plaintext at rest")
	assert.NotEqual(t, string(payload), atRest)

	enc := mr.HGet(store.artifactKey("t", "s", ArtifactRaw, "default"), "encoding")
	assert.Equal(t, "aesgcm-v1", enc)

	got, found, err := store.Get(ctx, "t", "s", ArtifactRaw, "")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, payload, got.Payload)
}

func TestStoreRawWrongKeyFails(t *testing.T) {
	mr, client := newMiniredis(t)
	ctx := context.Background()
	key1 := testKey(1)
	key2 := testKey(9) // different key

	store1, err := NewRedisArtifactStore(StoreOptions{Client: client, AESKey: key1})
	require.NoError(t, err)
	store2, err := NewRedisArtifactStore(StoreOptions{Client: client, AESKey: key2})
	require.NoError(t, err)

	ok, err := store1.PutCAS(ctx, sampleArtifact("t", "s", ArtifactRaw, "", []byte("secret")), SessionRevision{})
	require.NoError(t, err)
	require.True(t, ok)

	_, _, err = store2.Get(ctx, "t", "s", ArtifactRaw, "")
	require.Error(t, err, "decryption with the wrong key must fail")
	assert.True(t, errors.Is(err, ErrArtifactCorrupted))
	_ = mr
}

func TestStoreRawTamperedCiphertextFails(t *testing.T) {
	store, mr := newTestStore(t, nil)
	ctx := context.Background()

	ok, err := store.PutCAS(ctx, sampleArtifact("t", "s", ArtifactRaw, "", []byte("secret")), SessionRevision{})
	require.NoError(t, err)
	require.True(t, ok)

	key := store.artifactKey("t", "s", ArtifactRaw, "default")
	sealed := mr.HGet(key, "payload")
	tampered := []byte(sealed)
	tampered[len(tampered)/2] ^= 0xFF // flip one byte inside ciphertext/tag
	mr.HSet(key, "payload", string(tampered))

	_, _, err = store.Get(ctx, "t", "s", ArtifactRaw, "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrArtifactCorrupted))
}

// AAD binding: raw ciphertext swapped between sessions must not decrypt.
func TestStoreRawAADMismatchFails(t *testing.T) {
	store, mr := newTestStore(t, nil)
	ctx := context.Background()

	ok, err := store.PutCAS(ctx, sampleArtifact("t", "s1", ArtifactRaw, "", []byte("secret-s1")), SessionRevision{})
	require.NoError(t, err)
	require.True(t, ok)

	src := store.artifactKey("t", "s1", ArtifactRaw, "default")
	dst := store.artifactKey("t", "s2", ArtifactRaw, "default")
	sealed := mr.HGet(src, "payload")
	mr.HSet(dst, "payload", sealed)
	// minimal meta so Get proceeds to decode
	mr.HSet(dst, "encoding", "aesgcm-v1")
	ch := HashBytes([]byte("secret-s1"))
	mr.HSet(dst, "content_hash", hex32(ch))

	_, _, err = store.Get(ctx, "t", "s2", ArtifactRaw, "")
	require.Error(t, err, "AAD binds tenant+session+kind: cross-session swap must fail")
	assert.True(t, errors.Is(err, ErrArtifactCorrupted))
}

func TestStoreGzipOptionRoundTrip(t *testing.T) {
	store, mr := newTestStore(t, func(o *StoreOptions) {
		o.CompressSanitized = true
		o.CompressCompressed = true
	})
	ctx := context.Background()
	payload := []byte("sanitized payload sanitized payload sanitized payload")

	ok, err := store.PutCAS(ctx, sampleArtifact("t", "s", ArtifactSanitized, "", payload), SessionRevision{})
	require.NoError(t, err)
	require.True(t, ok)
	enc := mr.HGet(store.artifactKey("t", "s", ArtifactSanitized, "default"), "encoding")
	assert.Equal(t, "gzip", enc)

	got, found, err := store.Get(ctx, "t", "s", ArtifactSanitized, "")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, payload, got.Payload)
}

// UT-SA-11: TTLs are per-layer and independently configurable.
func TestStorePerLayerTTL(t *testing.T) {
	store, mr := newTestStore(t, func(o *StoreOptions) {
		o.RawTTL = 1 * time.Hour
		o.SanitizedTTL = 3 * time.Hour
		o.CompressedTTL = 3 * time.Hour
	})
	ctx := context.Background()

	for _, kind := range []ArtifactKind{ArtifactRaw, ArtifactSanitized, ArtifactCompressed} {
		ok, err := store.PutCAS(ctx, sampleArtifact("t", "s", kind, "", []byte("p-"+string(kind))), SessionRevision{})
		require.NoError(t, err)
		require.True(t, ok)
	}

	// advance 90 minutes: raw (1h TTL) expires, others (3h) survive
	mr.FastForward(90 * time.Minute)

	_, found, err := store.Get(ctx, "t", "s", ArtifactRaw, "")
	require.NoError(t, err)
	assert.False(t, found, "raw must expire after its own TTL")

	_, found, err = store.Get(ctx, "t", "s", ArtifactSanitized, "")
	require.NoError(t, err)
	assert.True(t, found, "sanitized must survive with its longer TTL")

	_, found, err = store.Get(ctx, "t", "s", ArtifactCompressed, "")
	require.NoError(t, err)
	assert.True(t, found, "compressed must survive with its longer TTL")
}

func TestStoreDefaultTTLIsTwoHours(t *testing.T) {
	store, mr := newTestStore(t, nil)
	ctx := context.Background()

	ok, err := store.PutCAS(ctx, sampleArtifact("t", "s", ArtifactRaw, "", []byte("p")), SessionRevision{})
	require.NoError(t, err)
	require.True(t, ok)

	mr.FastForward(2*time.Hour - time.Minute)
	_, found, _ := store.Get(ctx, "t", "s", ArtifactRaw, "")
	assert.True(t, found, "artifact must survive just under the default 2h TTL")

	mr.FastForward(2 * time.Minute)
	_, found, _ = store.Get(ctx, "t", "s", ArtifactRaw, "")
	assert.False(t, found, "artifact must expire after the default 2h TTL")
}

// UT-SA-03 (store level): raw append marks sanitized/compressed stale without
// deleting payloads; raw stays ready.
func TestStoreAppendRawTurnMarksDownstreamStale(t *testing.T) {
	store, _ := newTestStore(t, nil)
	ctx := context.Background()

	for _, kind := range []ArtifactKind{ArtifactRaw, ArtifactSanitized, ArtifactCompressed} {
		ok, err := store.PutCAS(ctx, sampleArtifact("t", "s", kind, "", []byte("p-"+string(kind))), SessionRevision{})
		require.NoError(t, err)
		require.True(t, ok)
	}

	mut := SessionMutation{TenantID: "t", SessionID: "s", RequestID: "r1", TurnNo: 1, Mutation: MutationAppendDelta, DeltaHash: h("d1"), DependencyHash: h("dep1")}
	rev, err := store.AppendRawTurn(ctx, mut, SessionRevision{})
	require.NoError(t, err)
	assert.EqualValues(t, 1, rev.TurnNo)
	assert.Equal(t, "r1", rev.HeadRequestID)

	m, err := store.GetManifest(ctx, "t", "s")
	require.NoError(t, err)
	assert.True(t, m.Flags.Has(FlagRawReady), "raw stays ready after append")
	assert.True(t, m.Flags.Has(FlagSanitizedStale), "sanitized must be stale")
	assert.True(t, m.Flags.Has(FlagCompressedStale), "compressed must be stale")
	assert.Equal(t, StatusStale, m.Sanitized.Status)
	assert.Equal(t, StatusStale, m.CompressedVariants["default"].Status)

	// payloads are NOT deleted (R11.7: 只改 manifest，不删正文)
	_, found, err := store.Get(ctx, "t", "s", ArtifactSanitized, "")
	require.NoError(t, err)
	assert.True(t, found, "sanitized payload must still be readable")
	_, found, err = store.Get(ctx, "t", "s", ArtifactCompressed, "")
	require.NoError(t, err)
	assert.True(t, found, "compressed payload must still be readable")
}

// UT-SA-10: InvalidateFrom follows the dependency graph.
func TestStoreInvalidateFromDependencyGraph(t *testing.T) {
	store, _ := newTestStore(t, nil)
	ctx := context.Background()

	// advance revision to 1 with raw ready
	mut := SessionMutation{TenantID: "t", SessionID: "s", RequestID: "r1", TurnNo: 1, Mutation: MutationAppendDelta, DeltaHash: h("d1"), DependencyHash: h("dep1")}
	rev, err := store.AppendRawTurn(ctx, mut, SessionRevision{})
	require.NoError(t, err)

	for _, kind := range []ArtifactKind{ArtifactSanitized, ArtifactCompressed} {
		ok, err := store.PutCAS(ctx, sampleArtifact("t", "s", kind, "", []byte("p")), rev)
		require.NoError(t, err)
		require.True(t, ok)
	}

	require.NoError(t, store.InvalidateFrom(ctx, "t", "s", ArtifactSanitized))
	m, err := store.GetManifest(ctx, "t", "s")
	require.NoError(t, err)
	assert.True(t, m.Flags.Has(FlagRawReady), "InvalidateFrom(Sanitized) must not touch raw")
	assert.True(t, m.Flags.Has(FlagSanitizedStale))
	assert.True(t, m.Flags.Has(FlagCompressedStale))

	// InvalidateFrom(Raw) reaches all three layers
	require.NoError(t, store.InvalidateFrom(ctx, "t", "s", ArtifactRaw))
	m, err = store.GetManifest(ctx, "t", "s")
	require.NoError(t, err)
	assert.True(t, m.Flags.Has(FlagRawStale))
	assert.True(t, m.Flags.Has(FlagSanitizedStale))
	assert.True(t, m.Flags.Has(FlagCompressedStale))
	assert.Equal(t, StatusStale, m.Raw.Status)

	// InvalidateFrom(Compressed) stops at compressed
	require.NoError(t, store.InvalidateFrom(ctx, "t", "s", ArtifactCompressed))
	m, err = store.GetManifest(ctx, "t", "s")
	require.NoError(t, err)
	assert.True(t, m.Flags.Has(FlagCompressedStale))
	assert.True(t, m.Flags.Has(FlagRawStale), "raw untouched by compressed invalidation")
}

// UT-SA-10: optional fields are cleaned with HDEL — no empty-string residue.
func TestStoreNoEmptyFieldResidue(t *testing.T) {
	store, mr := newTestStore(t, nil)
	ctx := context.Background()

	ok, err := store.PutCAS(ctx, sampleArtifact("t", "s", ArtifactRaw, "", []byte("p")), SessionRevision{})
	require.NoError(t, err)
	require.True(t, ok)

	mut := SessionMutation{TenantID: "t", SessionID: "s", RequestID: "r1", TurnNo: 1, Mutation: MutationAppendDelta, DeltaHash: h("d1"), DependencyHash: h("dep1")}
	_, err = store.AppendRawTurn(ctx, mut, SessionRevision{})
	require.NoError(t, err)

	peek := redisClient(t, mr.Addr())
	fields, err := peek.HGetAll(context.Background(), store.manifestKey("t", "s")).Result()
	require.NoError(t, err)
	require.NotEmpty(t, fields)
	for k, v := range fields {
		assert.NotEqual(t, "", v, "field %q must never hold an empty string (HDEL, not HSET-空值)", k)
	}
	_, hasFailure := fields["raw_failure"]
	assert.False(t, hasFailure, "zero failure code must be HDELed, not stored as 0/empty")
}

// UT-SA-11 + task: cross-tenant isolation — tenant A's keys are invisible to
// tenant B even with the same session id.
func TestStoreTenantIsolation(t *testing.T) {
	store, _ := newTestStore(t, nil)
	ctx := context.Background()
	secret := []byte("tenant-a-secret")

	ok, err := store.PutCAS(ctx, sampleArtifact("tenant-a", "sess-x", ArtifactRaw, "", secret), SessionRevision{})
	require.NoError(t, err)
	require.True(t, ok)

	// tenant B cannot read tenant A's artifact
	_, found, err := store.Get(ctx, "tenant-b", "sess-x", ArtifactRaw, "")
	require.NoError(t, err)
	assert.False(t, found, "tenant B must not see tenant A's artifact")

	mB, err := store.GetManifest(ctx, "tenant-b", "sess-x")
	require.NoError(t, err)
	assert.True(t, mB.Revision.IsZero(), "tenant B manifest must be empty")
	assert.Equal(t, StatusMissing, mB.Raw.Status)

	// tenant B writing the same session id must not clash with tenant A
	ok, err = store.PutCAS(ctx, sampleArtifact("tenant-b", "sess-x", ArtifactRaw, "", []byte("tenant-b-data")), SessionRevision{})
	require.NoError(t, err)
	require.True(t, ok)

	gotA, found, err := store.Get(ctx, "tenant-a", "sess-x", ArtifactRaw, "")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, secret, gotA.Payload, "tenant A's payload must be untouched")

	// key derivation uses hashed tenant/session ids
	assert.NotEqual(t,
		store.artifactKey("tenant-a", "sess-x", ArtifactRaw, ""),
		store.artifactKey("tenant-b", "sess-x", ArtifactRaw, ""))
}

// UT-SA-13: L1 deep copies — external mutation of the returned payload or of
// the stored artifact cannot affect cached bytes; byte budget evicts.
func TestStoreL1DeepCopyAndBudget(t *testing.T) {
	store, _ := newTestStore(t, func(o *StoreOptions) { o.L1BudgetBytes = 64 })
	ctx := context.Background()

	art := sampleArtifact("t", "s", ArtifactSanitized, "", []byte("payload-aaaa"))
	ok, err := store.PutCAS(ctx, art, SessionRevision{})
	require.NoError(t, err)
	require.True(t, ok)

	// mutate the artifact we passed in — the cache must be unaffected
	art.Payload[0] = 'X'
	got1, found, err := store.Get(ctx, "t", "s", ArtifactSanitized, "")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "payload-aaaa", string(got1.Payload), "mutating the Put input must not corrupt the cache")

	// mutate the returned payload — the next Get must be unaffected
	got1.Payload[0] = 'Y'
	got2, found, err := store.Get(ctx, "t", "s", ArtifactSanitized, "")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "payload-aaaa", string(got2.Payload), "mutating the Get result must not corrupt the cache")

	// byte budget: inserting a bigger artifact evicts the previous one
	big := sampleArtifact("t", "s", ArtifactRaw, "", make([]byte, 64))
	ok, err = store.PutCAS(ctx, big, SessionRevision{})
	require.NoError(t, err)
	require.True(t, ok)
	assert.LessOrEqual(t, store.l1.TotalBytes(), int64(64), "L1 must respect the byte budget after every update")
}

func TestStoreL1DisabledWhenBudgetZero(t *testing.T) {
	store, _ := newTestStore(t, func(o *StoreOptions) { o.L1BudgetBytes = 0 })
	assert.Nil(t, store.l1)

	ctx := context.Background()
	ok, err := store.PutCAS(ctx, sampleArtifact("t", "s", ArtifactRaw, "", []byte("p")), SessionRevision{})
	require.NoError(t, err)
	require.True(t, ok)
	got, found, err := store.Get(ctx, "t", "s", ArtifactRaw, "")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "p", string(got.Payload))
}

func TestStoreRequiresAESKey(t *testing.T) {
	_, client := newMiniredis(t)
	_, err := NewRedisArtifactStore(StoreOptions{Client: client})
	assert.ErrorIs(t, err, ErrRawKeyRequired)
	_, err = NewRedisArtifactStore(StoreOptions{AESKey: testKey(1)})
	assert.Error(t, err, "client is required")
}

// Regression for the manifest flat-field round trip (variants included).
func TestStoreManifestRoundTripWithVariants(t *testing.T) {
	store, _ := newTestStore(t, nil)
	ctx := context.Background()

	rev := SessionRevision{}
	for i, kind := range []ArtifactKind{ArtifactRaw, ArtifactSanitized, ArtifactCompressed} {
		variant := ""
		if kind == ArtifactCompressed {
			variant = "gpt4o-mini"
		}
		art := sampleArtifact("t", "s", kind, variant, []byte("p"))
		art.SchemaVersion = uint16(i + 1)
		ok, err := store.PutCAS(ctx, art, rev)
		require.NoError(t, err)
		require.True(t, ok)
	}

	m, err := store.GetManifest(ctx, "t", "s")
	require.NoError(t, err)
	assert.Equal(t, StatusReady, m.Raw.Status)
	assert.Equal(t, StatusReady, m.Sanitized.Status)
	assert.Equal(t, uint16(1), m.Raw.SchemaVersion)
	assert.Equal(t, uint16(3), m.CompressedVariants["gpt4o-mini"].SchemaVersion)
	assert.True(t, m.Flags.Has(FlagRawReady|FlagSanitizedReady|FlagCompressedReady))
	assert.False(t, m.Flags.Has(FlagRawStale|FlagSanitizedStale|FlagCompressedStale))
}
