package ratelimit

import (
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestRedisLimiter(t *testing.T) (*RedisLimiter, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return NewRedisLimiter(client), server
}

// TestRedisLimiter_TPMCountDoesNotLoseReservations is the regression for the
// tpmLua member-collision bug (AUDIT_CROSSCUTTING_CONCURRENCY_20260813.md §5-RL1).
//
// Before the fix the ZSET member was "<ms>:<tokens>". Two reservations made in
// the same millisecond with the same token estimate (the common case, since
// defaultTokenEstimate is used when the count is unknown) produced an identical
// member, so the second ZADD overwrote the first instead of adding a new entry
// — the sliding-window SUM under-counted and over-admitted.
//
// The fix inserts a microsecond uniqueness component ("ms:usec:tokens") while
// keeping tokens as the trailing field so the sum parser still reads it. This
// test fires many same-token reservations against a tight limit and asserts the
// limiter actually denies once the cumulative usage crosses the cap. With the
// old member scheme the limiter would (incorrectly) admit more than the limit.
func TestRedisLimiter_TPMCountDoesNotLoseReservations(t *testing.T) {
	l, _ := newTestRedisLimiter(t)

	// limit = 1000 tokens, each reservation charges 100. Exactly 10 should be
	// admitted; the 11th must be denied. miniredis advances its clock lazily,
	// so a burst of 20 calls lands within the same ms window — the worst case
	// for the old collision-prone "ms:tokens" member.
	const limit = 1000
	const tokens = 100
	const allowed = limit / tokens // 10

	admitted := 0
	for i := 0; i < allowed*2; i++ {
		if l.CheckTPM(1, tokens, limit) {
			admitted++
		}
	}

	if admitted != allowed {
		t.Fatalf("TPM over-admitted: got %d allowed, want exactly %d (limit=%d, tokens/reservation=%d) — member collision regression?",
			admitted, allowed, limit, tokens)
	}
}

// TestRedisLimiter_RPMCountDoesNotOverAdmit mirrors the above for the RPM path
// (which already used a monotonic count suffix and was safe) to lock the
// invariant end-to-end through the Redis Lua script.
func TestRedisLimiter_RPMCountDoesNotOverAdmit(t *testing.T) {
	l, _ := newTestRedisLimiter(t)

	const limit = 5
	admitted := 0
	for i := 0; i < limit*3; i++ {
		if l.CheckRPM(1, limit) {
			admitted++
		}
	}
	if admitted != limit {
		t.Fatalf("RPM over-admitted: got %d, want %d", admitted, limit)
	}
}
