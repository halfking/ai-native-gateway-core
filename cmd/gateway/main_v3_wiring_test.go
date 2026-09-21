package main

import (
	"context"
	"errors"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
	"github.com/kaixuan/llm-gateway-go/domains/session"
	redissafe "github.com/kaixuan/llm-gateway-go/internal/redis"
)

// newRedisBackendForTest wires a SessionCacheBackend backed by miniredis so the
// adapter's SafeHGetAll behaviour (WRONGTYPE propagation + ErrKeyNotFound →
// empty map + nil) can be asserted without a live Redis instance.
func newRedisBackendForTest(t *testing.T) (compression.SessionCacheBackend, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rc.Close() })
	// session.RedisClient wraps a *redis.Client and exposes .Client() for the
	// adapter; NewRedisClientFromClient exists exactly for this injection.
	rcWrap := session.NewRedisClientFromClient(rc)
	return redisBackendFromClient(rcWrap), mr
}

// TestRedisBackendAdapter_HGetAll_KeyNotFoundCollapsesToEmptyMap locks in the
// go-redis HGETALL contract: the adapter must convert redissafe.ErrKeyNotFound
// to (empty map, nil) so downstream cache.loadFromRedis treats it as a miss
// instead of an error.
func TestRedisBackendAdapter_HGetAll_KeyNotFoundCollapsesToEmptyMap(t *testing.T) {
	backend, _ := newRedisBackendForTest(t)

	fields, err := backend.HGetAll(context.Background(), "session:does-not-exist")
	if err != nil {
		t.Fatalf("HGetAll on missing key must return nil error, got %v", err)
	}
	if fields == nil {
		t.Fatal("HGetAll on missing key must return empty (non-nil) map to preserve go-redis contract")
	}
	if len(fields) != 0 {
		t.Fatalf("expected empty map, got %v", fields)
	}
}

// TestRedisBackendAdapter_HGetAll_HappyPathReturnsFields confirms the adapter
// surfaces an existing hash's fields unchanged.
func TestRedisBackendAdapter_HGetAll_HappyPathReturnsFields(t *testing.T) {
	backend, mr := newRedisBackendForTest(t)

	mr.HSet("session:abc", "schema_version", "1", "summary", "hello")

	fields, err := backend.HGetAll(context.Background(), "session:abc")
	if err != nil {
		t.Fatalf("HGetAll on existing hash must succeed, got %v", err)
	}
	if fields["schema_version"] != "1" || fields["summary"] != "hello" {
		t.Fatalf("unexpected fields: %v", fields)
	}
}

// TestRedisBackendAdapter_HGetAll_WrongTypePropagates asserts that a string
// stored under the session key surfaces as a typed WRONGTYPE error. The cache
// layer drops the entry via decodeSessionStateFields fallback, so the typed
// error must NOT be silently swallowed by the adapter — losing it would
// reintroduce the bug fixed in P1-14 (2026-08-28).
func TestRedisBackendAdapter_HGetAll_WrongTypePropagates(t *testing.T) {
	backend, mr := newRedisBackendForTest(t)

	// Plant a string value where a session hash is expected. SafeHGetAll
	// must detect the type mismatch via TYPE pre-check.
	mr.Set("session:string-not-hash", "this is not a hash")

	fields, err := backend.HGetAll(context.Background(), "session:string-not-hash")
	if err == nil {
		t.Fatal("HGetAll on a non-hash key must return an error, got nil")
	}
	if !errors.Is(err, redissafe.ErrWrongType) {
		t.Fatalf("expected ErrWrongType in chain, got %v", err)
	}
	var typedErr *redissafe.TypedError
	if !errors.As(err, &typedErr) {
		t.Fatalf("expected *redissafe.TypedError, got %T", err)
	}
	if typedErr.Expected != "hash" || typedErr.Actual != "string" {
		t.Fatalf("typed error expected=hash/actual=string, got %+v", typedErr)
	}
	if fields != nil {
		t.Fatalf("expected nil data on WRONGTYPE, got %v", fields)
	}
}
