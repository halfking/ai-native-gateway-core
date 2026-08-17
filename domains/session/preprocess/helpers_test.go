package preprocess

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// testKey returns a fixed non-zero 32-byte AES key.
func testKey(seed byte) [32]byte {
	var k [32]byte
	for i := range k {
		k[i] = seed + byte(i)
	}
	return k
}

func newMiniredis(t *testing.T) (*miniredis.Miniredis, redis.UniversalClient) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return mr, client
}

// newTestStore builds a Redis store with test defaults.
func newTestStore(t *testing.T, mutate func(*StoreOptions)) (*RedisArtifactStore, *miniredis.Miniredis) {
	t.Helper()
	mr, client := newMiniredis(t)
	opts := StoreOptions{
		Client:   client,
		AESKey:   testKey(1),
		LeaseTTL: 10 * time.Second,
	}
	if mutate != nil {
		mutate(&opts)
	}
	store, err := NewRedisArtifactStore(opts)
	if err != nil {
		t.Fatalf("NewRedisArtifactStore: %v", err)
	}
	return store, mr
}

// fakeBuilder counts layer builds and returns deterministic payloads that
// embed the current suffixes (flip a suffix to emulate a version change that
// alters the layer output).
type fakeBuilder struct {
	mu         sync.Mutex
	rawBuilds  int
	sanBuilds  int
	cmpBuilds  int
	rawSuffix  string
	sanSuffix  string
	cmpSuffix  string
	sanDegrade bool
}

func (b *fakeBuilder) counts() (int, int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.rawBuilds, b.sanBuilds, b.cmpBuilds
}

func (b *fakeBuilder) BuildRaw(ctx context.Context, in *SessionPrepareInput, prev SessionRevision) (*BuiltArtifact, error) {
	b.mu.Lock()
	b.rawBuilds++
	b.mu.Unlock()
	payload := []byte("raw|" + b.rawSuffix + "|" + in.RequestID + "|" + string(in.Delta))
	return &BuiltArtifact{
		Payload: payload,
		Receipts: []TransformReceipt{{
			Name: "canonicalize", Version: 1,
			InputHash: HashBytes(in.Delta), OutputHash: HashBytes(payload),
			Status: TransformApplied,
		}},
		Tokens: int32(len(payload)),
	}, nil
}

func (b *fakeBuilder) BuildSanitized(ctx context.Context, in *SessionPrepareInput, raw *SessionArtifact) (*BuiltArtifact, error) {
	b.mu.Lock()
	b.sanBuilds++
	b.mu.Unlock()
	payload := []byte("san|" + b.sanSuffix + "|" + in.RequestID)
	status := uint8(TransformApplied)
	if b.sanDegrade {
		status = TransformDegraded
	}
	return &BuiltArtifact{
		Payload:  payload,
		Degraded: b.sanDegrade,
		Receipts: []TransformReceipt{{
			Name: "sanitize", Version: 2,
			InputHash: raw.ContentHash, OutputHash: HashBytes(payload),
			Status: status,
		}},
		Tokens: int32(len(payload)),
	}, nil
}

func (b *fakeBuilder) BuildCompressed(ctx context.Context, in *SessionPrepareInput, sanitized *SessionArtifact, variant string) (*BuiltArtifact, error) {
	b.mu.Lock()
	b.cmpBuilds++
	b.mu.Unlock()
	payload := []byte("cmp|" + b.cmpSuffix + "|" + variant + "|" + in.RequestID)
	return &BuiltArtifact{
		Payload: payload,
		Receipts: []TransformReceipt{{
			Name: "compress", Version: 3,
			InputHash: sanitized.ContentHash, OutputHash: HashBytes(payload),
			Status: TransformApplied,
		}},
		Tokens: int32(len(payload)),
	}, nil
}

// defaultDeps returns a fixed dependency version set (R11.5 inputs).
func defaultDeps() ArtifactDependencies {
	return ArtifactDependencies{
		Raw: RawDependencies{
			CanonicalizerVersion: "canon-v1",
			Mutation:             MutationAppendDelta,
		},
		Sanitized: SanitizedDependencies{
			SanitizerVersion:  "san-v1",
			PolicyHash:        "policy-abc",
			PlaceholderSchema: "ph-v1",
		},
		Compressed: CompressedDependencies{
			CompressorVersion:      "comp-v1",
			SchemaVersion:          "schema-v1",
			CompressionMode:        "mode-summary",
			SummaryPromptVersion:   "sp-v1",
			SummaryModelVersion:    "sm-v1",
			Protocol:               "openai",
			ContextWindowBucket:    "128k",
			ToolsHash:              "tools-xyz",
			PrefixHandlingVersion:  "prefix-v1",
			ToolHandlingVersion:    "tool-v1",
			ModelCapabilityProfile: "cap-a",
		},
	}
}

// prepareInput is the standard test input.
func prepareInput(tenant, session, request string, delta []byte) SessionPrepareInput {
	return SessionPrepareInput{
		TenantID:  tenant,
		SessionID: session,
		RequestID: request,
		Delta:     delta,
		Deps:      defaultDeps(),
	}
}

// newTestHook wires store + builder + capture sink; returns the hook, the
// builder and the capture sink.
func newTestHook(t *testing.T, store SessionArtifactStore, builder ArtifactBuilder, sink EventSink) SessionPreprocessHook {
	t.Helper()
	h, err := NewHook(HookConfig{
		Store:             store,
		Builder:           builder,
		Events:            sink,
		Now:               time.Now,
		LeaseWaitTimeout:  2 * time.Second,
		LeasePollInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewHook: %v", err)
	}
	return h
}

func mustPrepare(t *testing.T, h SessionPreprocessHook, in SessionPrepareInput) *PreparedSessionArtifacts {
	t.Helper()
	p, err := h.Prepare(context.Background(), in)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	return p
}
