package kv

import (
	"os"
	"strings"
	"testing"
)

// design_intent_test.go encodes the NON-NEGOTIABLE invariants of the kv
// package. These guard against a future edit that silently breaks cache hit
// rates, leaks prompt content into telemetry, or duplicates logic that
// belongs to peer packages.
//
// Pattern follows security/armor/design_intent_test.go and
// cache/prefix/design_intent_test.go.

// TestKey_IsPureFunction_Documented: Key() MUST be a pure function of
// (body, opts). No I/O, no global state, no time-dependence.
// This guarantees tests are deterministic and cache lookups reproducible.
func TestKey_IsPureFunction_Documented(t *testing.T) {
	src := mustRead(t, "key.go")
	if !strings.Contains(src, "pure function of (body, opts)") {
		t.Error("key.go must document that Key is a pure function of (body, opts)")
	}
}

// TestTailExcluded_Documented: the most important behavioral guarantee —
// the most recent turn(s) are excluded from the hash. Without this the
// cache hit rate collapses to zero on any multi-turn conversation.
func TestTailExcluded_Documented(t *testing.T) {
	src := mustRead(t, "key.go")
	if !strings.Contains(src, "TailTurns > 0 messages were excluded") &&
		!strings.Contains(src, "TailTurns is the number of MOST RECENT conversation turns to EXCLUDE") {
		t.Error("key.go must document the tail-exclusion invariant")
	}
}

// TestStabilizationDelegation_Documented: kv/ delegates message ordering
// to cache/prefix/. It MUST NOT reimplement stabilization — otherwise two
// callers would compute different keys for the same conversation.
func TestStabilizationDelegation_Documented(t *testing.T) {
	src := mustRead(t, "key.go")
	if !strings.Contains(src, "cache/prefix") {
		t.Error("key.go must reference cache/prefix package as the stabilization authority")
	}
	if !strings.Contains(src, "DELEGATES") && !strings.Contains(src, "delegates") {
		t.Error("key.go must explicitly say it delegates stabilization to cache/prefix")
	}
}

// TestDomainBoundary_Documented: kv/ OWNS key+store; does NOT own
// stabilization, cache_control injection, or semantic matching. The
// boundary must be explicit so we don't duplicate logic.
func TestDomainBoundary_Documented(t *testing.T) {
	src := mustRead(t, "key.go")
	if !strings.Contains(src, "kv/ OWNS") {
		t.Error("key.go must document the OWN list (kv/ OWNS)")
	}
	if !strings.Contains(src, "kv/ does NOT own") {
		t.Error("key.go must document the does-NOT-own list")
	}
	if !strings.Contains(src, "cache_control") {
		t.Error("key.go must mention cache_control as NOT-our-job")
	}
	if !strings.Contains(src, "cache/semantic") {
		t.Error("key.go must mention cache/semantic as the semantic authority")
	}
}

// TestPrefixBytes_NotForLogging: PrefixBytes is exposed for downstream
// consumers (session.CacheInjector needs the raw bytes to insert markers),
// but MUST NEVER be logged — it contains prompt content.
func TestPrefixBytes_NotForLogging(t *testing.T) {
	src := mustRead(t, "key.go")
	if !strings.Contains(src, "NEVER") || !strings.Contains(src, "PrefixBytes") {
		t.Error("key.go must warn NEVER to log PrefixBytes (contains prompt content)")
	}
}

// TestTolerantInput_Documented: Key() never errors on bad input. The cache
// layer is an optimization — breaking a request because of a malformed body
// would be a worse failure than the cache miss itself.
func TestTolerantInput_Documented(t *testing.T) {
	src := mustRead(t, "key.go")
	if !strings.Contains(src, "tolerant") && !strings.Contains(src, "never errors") {
		t.Error("key.go must document that Key never errors on bad input")
	}
}

// TestStore_ConcurrencySafe_Documented: the Store contract guarantees
// concurrent safety. Without it, the relay would crash under load.
func TestStore_ConcurrencySafe_Documented(t *testing.T) {
	src := mustRead(t, "memory.go")
	if !strings.Contains(src, "safe for concurrent use") {
		t.Error("memory.go must document concurrency safety")
	}
	src = mustRead(t, "store.go")
	if !strings.Contains(src, "safe for concurrent use") && !strings.Contains(src, "concurrency-safe") {
		t.Error("store.go must document concurrency safety in the interface contract")
	}
}

// TestDefensiveCopy_Documented: payload MUST be defensively copied on both
// Store and Lookup — otherwise a caller mutation can corrupt the cache.
func TestDefensiveCopy_Documented(t *testing.T) {
	src := mustRead(t, "memory.go")
	count := strings.Count(src, "Defensive copy")
	if count < 2 {
		t.Errorf("memory.go must document defensive copy on both Store and Lookup, found %d", count)
	}
}

// TestEmptyKeyRejected_Documented: empty key MUST be rejected by all Store
// methods — otherwise "unkeyed" calls would collapse to a single global
// bucket and silently cross-pollinate sessions.
func TestEmptyKeyRejected_Documented(t *testing.T) {
	src := mustRead(t, "store.go")
	if !strings.Contains(src, "ErrEmptyKey") {
		t.Error("store.go must define ErrEmptyKey")
	}
	src = mustRead(t, "memory.go")
	if strings.Count(src, "ErrEmptyKey") < 3 {
		t.Error("memory.go must check ErrEmptyKey on Store, Lookup, AND Invalidate")
	}
}

// TestTTL_SensibleDefault_Documented: DefaultTTL exists and is non-trivial
// (not "0" or "1ns"). A 0 default would mean "everything expires
// immediately" and silently kill hit rates.
func TestTTL_SensibleDefault_Documented(t *testing.T) {
	src := mustRead(t, "store.go")
	if !strings.Contains(src, "DefaultTTL") {
		t.Error("store.go must define DefaultTTL constant")
	}
	if !strings.Contains(src, "5 * time.Minute") {
		t.Error("DefaultTTL should be 5min (Anthropic upstream cache window)")
	}
}

func mustRead(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("cannot read %s: %v", name, err)
	}
	return string(b)
}
