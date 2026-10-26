package redis

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// countingCmdable counts *round trips* for the per-key path. On a real
// redis.Client each Type()/HGetAll() call is one network round trip, so the
// method-call count is the round-trip count.
type countingCmdable struct {
	*redis.Client
	calls int
}

func (c *countingCmdable) Type(ctx context.Context, key string) *redis.StatusCmd {
	c.calls++
	return c.Client.Type(ctx, key)
}

func (c *countingCmdable) HGetAll(ctx context.Context, key string) *redis.MapStringStringCmd {
	c.calls++
	return c.Client.HGetAll(ctx, key)
}

// countingPipe counts Exec calls — one Exec is one pipeline round trip,
// regardless of how many commands it carries.
type countingPipe struct {
	redis.Pipeliner
	execs *int
}

func (c countingPipe) Exec(ctx context.Context) ([]redis.Cmder, error) {
	*c.execs++
	return c.Pipeliner.Exec(ctx)
}

type countingPipeliner struct {
	*redis.Client
	execs int
	pipes int
}

func (c *countingPipeliner) Type(ctx context.Context, key string) *redis.StatusCmd {
	return c.Client.Type(ctx, key)
}

func (c *countingPipeliner) HGetAll(ctx context.Context, key string) *redis.MapStringStringCmd {
	return c.Client.HGetAll(ctx, key)
}

func (c *countingPipeliner) Pipeline() redis.Pipeliner {
	c.pipes++
	return countingPipe{Pipeliner: c.Client.Pipeline(), execs: &c.execs}
}

func seedHashes(t *testing.T, mr *miniredis.Miniredis, rdb *redis.Client, n int) []string {
	t.Helper()
	keys := make([]string, 0, n)
	for i := 0; i < n; i++ {
		k := "ursm:v2:node:tenant-a:" + itoa(i) + ":model-x"
		if err := rdb.HSet(context.Background(), k, map[string]any{
			"available": "1", "credential_id": itoa(i), "fail_streak": "2",
		}).Err(); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
		keys = append(keys, k)
	}
	return keys
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// ⭐⭐ The whole point of §10.110.55: round trips must collapse.
//
// Per-key SafeHGetAll costs TWO round trips per key (TYPE + HGETALL).
// At ~1,800 keys that is ~3,600 round trips against a 30s budget — a 7.9ms
// per-round-trip cliff that measured latency (8.8~26.1ms) is already past.
func TestBatchCollapsesRoundTrips(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	keys := seedHashes(t, mr, rdb, 600)

	perKey := &countingCmdable{Client: rdb}
	for _, k := range keys {
		if _, err := SafeHGetAll(context.Background(), perKey, k); err != nil {
			t.Fatalf("per-key %s: %v", k, err)
		}
	}

	batched := &countingPipeliner{Client: rdb}
	got, err := SafeHGetAllBatch(context.Background(), batched, keys, 256)
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if len(got) != len(keys) {
		t.Fatalf("batch returned %d keys, want %d", len(got), len(keys))
	}

	t.Logf("per-key round trips=%d   batched round trips=%d", perKey.calls, batched.execs)
	if perKey.calls != 2*len(keys) {
		t.Fatalf("per-key path made %d calls, want 2 per key (%d)", perKey.calls, 2*len(keys))
	}
	// 600 keys at batch 256 => 3 Exec. Require at least a 10x collapse so a
	// degenerate "batch" that secretly loops per key cannot pass.
	if batched.execs*10 >= perKey.calls {
		t.Fatalf("batched path made %d round trips vs per-key %d: no meaningful collapse",
			batched.execs, perKey.calls)
	}
	if batched.execs == 0 {
		t.Fatal("no pipeline Exec recorded; the instrument, not the code, is broken")
	}
}

func TestBatchPreservesPerKeySemantics(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	ctx := context.Background()

	if err := rdb.HSet(ctx, "h:full", map[string]string{"a": "1", "b": "2"}).Err(); err != nil {
		t.Fatal(err)
	}
	// NOTE: an "existing but empty" hash is NOT reachable — deleting the last
	// field removes the key itself (miniredis rejects HSET with zero fields for
	// the same reason). SafeHGetAll's doc mentions that shape; it is defensive
	// only. The case that does occur is TYPE=none, asserted below.
	if err := rdb.Set(ctx, "s:notahash", "v", 0).Err(); err != nil {
		t.Fatal(err)
	}

	cp := &countingPipeliner{Client: rdb}
	_, err := SafeHGetAllBatch(ctx, cp, []string{"h:full", "s:notahash"}, 256)
	var typed *TypedError
	if !errors.As(err, &typed) {
		t.Fatalf("want *TypedError for the string key, got %v", err)
	}
	if typed.Key != "s:notahash" || typed.Actual != "string" {
		t.Fatalf("TypedError misreports the offending key: %+v", typed)
	}

	// Same call without the bad key: missing keys are skipped, an existing
	// but empty hash yields an empty (non-nil) map.
	cp2 := &countingPipeliner{Client: rdb}
	ok, err := SafeHGetAllBatch(ctx, cp2, []string{"h:full", "h:absent"}, 256)
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if _, present := ok["h:absent"]; present {
		t.Fatal("a missing key must be omitted, mirroring SafeHGetAll's ErrKeyNotFound path")
	}
	if m, present := ok["h:full"]; !present || m["a"] != "1" || m["b"] != "2" {
		t.Fatalf("h:full misread: %+v", m)
	}
}

// Chunk boundaries are where batch bugs hide: a key landing on the seam must
// still be read.
func TestBatchAcrossChunkBoundaries(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	keys := seedHashes(t, mr, rdb, 33)

	cp := &countingPipeliner{Client: rdb}
	got, err := SafeHGetAllBatch(context.Background(), cp, keys, 10)
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if len(got) != len(keys) {
		t.Fatalf("got %d keys across chunk boundaries, want %d", len(got), len(keys))
	}
	// Chunk count is counted by Pipeline() calls, not Exec: each chunk runs
	// TWO Execs (one TYPE pipeline, one HGETALL pipeline).
	if cp.pipes != 4 { // ceil(33/10)
		t.Fatalf("expected 4 chunks for 33 keys at batch 10, got %d", cp.pipes)
	}
	if cp.execs != 8 {
		t.Fatalf("expected 2 Exec per chunk (TYPE then HGETALL) => 8, got %d", cp.execs)
	}
	for i, k := range keys {
		if got[k]["credential_id"] != itoa(i) {
			t.Fatalf("key %s carries credential_id %q, want %d", k, got[k]["credential_id"], i)
		}
	}
}

func TestBatchFallsBackToDefaultSize(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	keys := seedHashes(t, mr, rdb, 5)

	cp := &countingPipeliner{Client: rdb}
	// Guarded on purpose: if the size normalisation is ever lost, the loop
	// advances by 0 and hangs. A hang is a useless gate result — this makes the
	// failure mode a red assertion instead of a stalled run.
	type result struct {
		got map[string]map[string]string
		err error
	}
	done := make(chan result, 1)
	go func() {
		g, e := SafeHGetAllBatch(context.Background(), cp, keys, 0)
		done <- result{g, e}
	}()

	var res result
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("SafeHGetAllBatch did not terminate for maxBatchSize=0")
	}
	if res.err != nil {
		t.Fatalf("batch size 0 must fall back to the default: %v", res.err)
	}
	got := res.got
	if len(got) != len(keys) {
		t.Fatalf("got %d keys, want %d", len(got), len(keys))
	}
	if cp.pipes != 1 {
		t.Fatalf("expected a single chunk under the default batch size, got %d", cp.pipes)
	}
	if cp.execs != 2 {
		t.Fatalf("expected 2 Execs in one chunk (TYPE then HGETALL), got %d", cp.execs)
	}
}

func TestBatchOnEmptyInput(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	cp := &countingPipeliner{Client: rdb}
	got, err := SafeHGetAllBatch(context.Background(), cp, nil, 128)
	if err != nil {
		t.Fatalf("empty input: %v", err)
	}
	if len(got) != 0 || cp.pipes != 0 || cp.execs != 0 {
		t.Fatalf("empty input must touch Redis zero times, got %d entries / %d pipelines / %d Exec",
			len(got), cp.pipes, cp.execs)
	}
}

// ⭐ The batch-size default is a *measured* operating point (§57). Pin it so a
// well-meaning tweak has to argue with the measurement, not slip in silently.
//
// The gate is verified by mutation: changing the constant to 256 makes this
// red. Anything looser (e.g. "> 0") would let the regression through.
func TestDefaultHashBatchSizeIsTheMeasuredOperatingPoint(t *testing.T) {
	if DefaultHashBatchSize != 512 {
		t.Fatalf("DefaultHashBatchSize=%d; §57 measured 512 on both gateways (medians 315ms/331ms vs 391ms/385ms at 256)",
			DefaultHashBatchSize)
	}
}
