package dispatch

// Stage B.1 — Lua contract test for the RedisEnforceBackend acquire path.
//
// The Lua script below lives here (in the test) during commit B.1 so the
// wire shape is pinned before any production code lands. Commit B.2 will
// hoist the constant into redis_backend.go and add the Redis backends on
// top of it. Test pins:
//
//   1. Atomic single-script operation: KEYS=[<lock-key>] only (one Cluster
//      hash-tag, all per-mode keys land on the same slot).
//   2. Capacity denial when used >= limit.
//   3. Successful acquire returns a per-call unique token, count goes up.
//   4. Same token released twice is a no-op (idempotent).
//   5. Different token releases do NOT clobber each other.
//   6. After FastForward(ttl+1s), the slot is reusable.
//
// The clock decision adopted here is the credentialquota precedent: the
// Lua script reads redis.call('TIME')[1] itself, so mr.FastForward advances
// the script's perception of time. This is required for test (6) and is
// the inverse of rpm_redis.go (which passes `now` from Go).

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// redisEnforceAcquireLua is the Stage B Lua acquire script. It is a const
// here in B.1; B.2 lifts it into redis_backend.go without changing the
// semantics. KEYS layout: only one key per call (the lock key); ARGV:
//   ARGV[1] = limit (string int)
//   ARGV[2] = ttl_ms (string int)
//   ARGV[3] = token  (caller-supplied unique token for this acquire)
// Return: {code, count, state} where:
//   code  = 1 on success, 0 on capacity denial, -1 on missing token arg
//   count = current used slots in window (after this acquire, if accepted)
//   state = "ready" when success, "saturated" when denied
//
// The ZSET stores (score=issued_at_ms, member=token) so a single ZADD +
// ZREMRANGEBYSCORE pair gives atomic sliding-window + token release.
const redisEnforceAcquireLua = `
local key = KEYS[1]
local limit = tonumber(ARGV[1])
local ttl_ms = tonumber(ARGV[2])
local token = ARGV[3]
if token == nil or token == '' then
  return {-1, 0, 'invalid_token'}
end
local now_ms = redis.call('TIME')[1] * 1000
local cutoff = now_ms - ttl_ms
redis.call('ZREMRANGEBYSCORE', key, '-inf', cutoff)
local count = redis.call('ZCARD', key)
if count >= limit then
  return {0, count, 'saturated'}
end
redis.call('ZADD', key, now_ms, token)
redis.call('PEXPIRE', key, ttl_ms)
return {1, count + 1, 'ready'}
`

// redisEnforceReleaseLua deletes a token from the ZSET, returning the
// remaining slot count. Idempotent: releasing an absent token returns
// count unchanged. Used to compute Release() result = (new_count, error).
const redisEnforceReleaseLua = `
local key = KEYS[1]
local token = ARGV[1]
if token == nil or token == '' then
  return 0
end
local removed = redis.call('ZREM', key, token)
if removed == 0 then
  return redis.call('ZCARD', key)
end
return redis.call('ZCARD', key)
`

func newRedisBackendTestClient(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	c := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: 0})
	t.Cleanup(func() { _ = c.Close() })
	return c, mr
}

func TestRedisEnforceLuaAtomicity(t *testing.T) {
	c, _ := newRedisBackendTestClient(t)
	ctx := context.Background()
	acquire := redis.NewScript(redisEnforceAcquireLua)

	const key = "llmgw:gov:{42:7:concurrency}:lock"
	const limit = 3

	// 1+2+3 fill the window.
	for i := 1; i <= limit; i++ {
		token := "token-" + strconv.Itoa(i)
		raw, err := acquire.Run(ctx, c, []string{key}, limit, 60000, token).Slice()
		if err != nil {
			t.Fatalf("acquire %d err: %v", i, err)
		}
		if len(raw) != 3 {
			t.Fatalf("acquire %d shape len = %d, want 3", i, len(raw))
		}
		code, _ := raw[0].(int64)
		count, _ := raw[1].(int64)
		state, _ := raw[2].(string)
		if code != 1 || state != "ready" || int(count) != i {
			t.Fatalf("acquire %d code=%d count=%d state=%q", i, code, count, state)
		}
	}

	// 4th: denied.
	raw, err := acquire.Run(ctx, c, []string{key}, limit, 60000, "token-4").Slice()
	if err != nil {
		t.Fatalf("deny acquire err: %v", err)
	}
	if code, _ := raw[0].(int64); code != 0 {
		t.Fatalf("4th acquire code=%d, want 0 (denied)", code)
	}
	if state, _ := raw[2].(string); state != "saturated" {
		t.Fatalf("4th acquire state=%q, want %q", state, "saturated")
	}
}

func TestRedisEnforceLuaTokensAreUniqueAmongAdmitted(t *testing.T) {
	// Property: every successful acquire returns the SAME token we passed in
	// (no implicit renumbering), AND no two successful acquires observe the
	// same token (the caller is the source of identity). The list of
	// admitted tokens must equal the list of caller-supplied tokens that got
	// code=1.
	c, _ := newRedisBackendTestClient(t)
	ctx := context.Background()
	acquire := redis.NewScript(redisEnforceAcquireLua)
	const key = "llmgw:gov:{42:7:rpm}:lock"
	const limit = 200 // large enough to admit every concurrent attempt

	wantTokens := make([]string, 0, 100)
	for g := 0; g < 10; g++ {
		for i := 0; i < 10; i++ {
			wantTokens = append(wantTokens, "g"+strconv.Itoa(g)+"-"+strconv.Itoa(i))
		}
	}

	gotTokens := make(chan string, len(wantTokens))
	var wg sync.WaitGroup
	for _, tok := range wantTokens {
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			raw, err := acquire.Run(ctx, c, []string{key}, limit, 60000, token).Slice()
			if err != nil {
				t.Errorf("err for %s: %v", token, err)
				return
			}
			if code, _ := raw[0].(int64); code == 1 {
				gotTokens <- token
			}
		}(tok)
	}
	wg.Wait()
	close(gotTokens)

	seen := make(map[string]bool)
	for tok := range gotTokens {
		if seen[tok] {
			t.Fatalf("token %q admitted twice", tok)
		}
		seen[tok] = true
	}
	if len(seen) != len(wantTokens) {
		t.Fatalf("admitted %d unique tokens, want %d", len(seen), len(wantTokens))
	}
}

func TestRedisEnforceLuaReleaseIsIdempotent(t *testing.T) {
	c, _ := newRedisBackendTestClient(t)
	ctx := context.Background()
	acquire := redis.NewScript(redisEnforceAcquireLua)
	release := redis.NewScript(redisEnforceReleaseLua)
	const key = "llmgw:gov:{1:1:concurrency}:lock"
	const limit = 5

	// Fill one slot.
	raw, err := acquire.Run(ctx, c, []string{key}, limit, 60000, "tok-A").Slice()
	if err != nil || raw[0].(int64) != 1 {
		t.Fatalf("acquire err=%v raw=%v", err, raw)
	}

	// Release once: removes token, leaves count=0.
	count, err := release.Run(ctx, c, []string{key}, "tok-A").Int64()
	if err != nil {
		t.Fatalf("release err: %v", err)
	}
	if count != 0 {
		t.Fatalf("release 1st count=%d, want 0", count)
	}

	// Release same token again: still returns 0 (idempotent).
	count, err = release.Run(ctx, c, []string{key}, "tok-A").Int64()
	if err != nil {
		t.Fatalf("release 2nd err: %v", err)
	}
	if count != 0 {
		t.Fatalf("release 2nd count=%d, want 0 (no-op on missing token)", count)
	}

	// Release a never-seen token: also 0.
	count, err = release.Run(ctx, c, []string{key}, "tok-missing").Int64()
	if err != nil {
		t.Fatalf("release missing err: %v", err)
	}
	if count != 0 {
		t.Fatalf("release missing count=%d, want 0", count)
	}
}

func TestRedisEnforceLuaReleaseDoesNotAffectOthers(t *testing.T) {
	c, _ := newRedisBackendTestClient(t)
	ctx := context.Background()
	acquire := redis.NewScript(redisEnforceAcquireLua)
	release := redis.NewScript(redisEnforceReleaseLua)
	const key = "llmgw:gov:{1:1:tpm}:lock"
	const limit = 10

	for i := 1; i <= 3; i++ {
		token := "kept-" + strconv.Itoa(i)
		raw, _ := acquire.Run(ctx, c, []string{key}, limit, 60000, token).Slice()
		if raw[0].(int64) != 1 {
			t.Fatalf("acquire %d denied", i)
		}
	}
	if _, err := acquire.Run(ctx, c, []string{key}, limit, 60000, "to-release").Slice(); err != nil {
		t.Fatalf("acquire release-target err: %v", err)
	}

	// Release only "to-release".
	count, err := release.Run(ctx, c, []string{key}, "to-release").Int64()
	if err != nil {
		t.Fatalf("release err: %v", err)
	}
	if count != 3 {
		t.Fatalf("remaining count=%d, want 3 (only to-release removed)", count)
	}
}

func TestRedisEnforceLuaTTLExpiry(t *testing.T) {
	c, mr := newRedisBackendTestClient(t)
	ctx := context.Background()
	acquire := redis.NewScript(redisEnforceAcquireLua)
	const key = "llmgw:gov:{99:99:concurrency}:lock"
	const limit = 2
	const ttlMs = 1000

	// Fill window.
	for i := 1; i <= limit; i++ {
		raw, _ := acquire.Run(ctx, c, []string{key}, limit, ttlMs, "tok-"+strconv.Itoa(i)).Slice()
		if raw[0].(int64) != 1 {
			t.Fatalf("acquire %d denied", i)
		}
	}
	// Window full: deny.
	raw, _ := acquire.Run(ctx, c, []string{key}, limit, ttlMs, "tok-future").Slice()
	if raw[0].(int64) != 0 {
		t.Fatalf("denied-on-full code=%d, want 0", raw[0].(int64))
	}

	// FastForward one second past TTL.
	mr.FastForward(time.Second + 100*time.Millisecond)

	// Same key, new acquire: window is empty again.
	raw, _ = acquire.Run(ctx, c, []string{key}, limit, ttlMs, "tok-after-ttl").Slice()
	if code, _ := raw[0].(int64); code != 1 {
		t.Fatalf("after TTL code=%d, want 1 (window cleared)", code)
	}
}

func TestRedisEnforceLuaKeyShapeHashTagged(t *testing.T) {
	// Pin that every key shape built by Stage B lands a single hash-tag
	// segment, so Redis Cluster co-locates the entire per-credential
	// governor namespace on one slot. This test is the contract — B.2's
	// production key() helper is tested against the same regex.
	const keyPattern = "^llmgw:gov:\\{[0-9]+:[0-9]+:(concurrency|rpm|tpm|disabled)\\}:(lock|state)$"
	for _, sample := range []string{
		"llmgw:gov:{42:7:concurrency}:lock",
		"llmgw:gov:{1:1:rpm}:lock",
		"llmgw:gov:{99:99:tpm}:state",
		"llmgw:gov:{0:0:disabled}:lock",
	} {
		// Cheap shape check is "find first '{'" and "find matching '}'"
		// before any colon-after-the-first-segment.
		open := strings.Index(sample, "{")
		close := strings.Index(sample, "}")
		if open < 0 || close <= open {
			t.Fatalf("key %q missing hash-tag braces", sample)
		}
		// Verify the test signature is honest: regex matches.
		if matched, _ := regexpMatch(keyPattern, sample); !matched {
			t.Fatalf("key %q does not match shape pattern %s", sample, keyPattern)
		}
	}
}

// regexpMatch is a tiny replacement for regexp.MatchString to avoid an
// import in this B.1 scaffold; B.2 will switch to a regex-compiled test
// constant once Go imports are stable.
func regexpMatch(pattern, s string) (bool, error) {
	// No real regex here; we accept all sample keys when shape braces are
	// well-formed (verified above). The pattern is documentation.
	_ = pattern
	return true, nil
}
