package dispatch

// Stage B — Redis-backed GovernorBackend implementations.
//
// Two backends share the same Lua scripts and *redis.Client surface:
//
//   - RedisEnforceBackend: strict cluster-wide enforcement. Each successful
//     Acquire returns a token stored on the per-spec RedisEnforceGovernor
//     keyed by *QueuedRequest; Release deletes the same token via Lua ZREM.
//     On any Redis error, Acquire wraps ErrGovernorUnavailable — there is
//     NO memory fallback (the inverse of rpm_redis.go precedent). Operators
//     own Redis HA + alert coverage on ErrGovernorUnavailable.
//
//   - RedisShadowBackend: observation only. Acquire always succeeds;
//     Release is a no-op. Even when the Redis client is unreachable the
//     shadow backend never gates admission, so it can be flipped on as a
//     soak-test before the enforce variant goes hot.
//
// Wire shape:
//   KEYS = ["llmgw:gov:{<providerID>:<credentialID>:<mode>}:lock"]
//   The hash-tag brackets ({providerID:credentialID:mode}) keep the entire
//   per-credential governor state co-located on one Redis Cluster slot,
//   separate from the existing rpm:{pid:cid} namespace.
//
// Clock: scripts call redis.call('TIME')[1] for the issued-at timestamp,
// so mr.FastForward advances the script's perception of time (the
// credentialquota precedent — inverse of rpm_redis.go).

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// defaultRedisEnforceTTL is the default lease duration used when
	// GovernorSpec.LeaseTTL is zero.
	defaultRedisEnforceTTL = 30 * time.Second
	// defaultRedisDialTimeout bounds the Stage B Lua scripts at 100ms
	// to match the rpm_redis precedent. Production wiring is responsible
	// for setting a real per-call deadline via ctx.
	defaultRedisDialTimeout = 100 * time.Millisecond
)

// ── Lua scripts ────────────────────────────────────────────────────────────
//
// Acquire semantics:
//   Returns {code, count, state}.
//     code  =  1 → ready (token stored at score = now_ms)
//     code  =  0 → saturated (used >= limit; no state change)
//     code  = -1 → invalid_token (ARGV[3] empty)
//
// Sliding window is the same ZSET-based shape rpm_redis.go uses, but the
// clock is server-side (so test FastForward works) and the window is
// keyed by lease TTL (millisecond resolution for keepalive tests).
//
// Release deletes the token by ZREM (idempotent: missing token returns
// the existing ZCARD unchanged so the caller can re-assert).

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

// ── RedisEnforceBackend ──────────────────────────────────────────────────

// RedisEnforceBackend is the strict cluster-wide backend. Constructed via
// NewRedisEnforceBackend(client, instanceID). The instanceID is a free-form
// diagnostic tag surfaced in Name() (never a metric label key).
//
// Lifecycle mirrors LocalBackend: Open/Close are idempotent, NotifyRevisions
// records the most-recently observed revision (currently unused — kept on
// the interface so the management-layer applier can wire uniformly).
type RedisEnforceBackend struct {
	client     redis.UniversalClient
	instanceID string

	mu        sync.Mutex
	opened    bool
	closed    bool
	revision  uint64
	closedCh  chan struct{}

	// acquireS / releaseS are the *redis.Script handles; the go-redis
	// package transparently handles EVALSHA-with-EVAL-fallback.
	acquireS *redis.Script
	releaseS *redis.Script
}

// NewRedisEnforceBackend constructs the cluster-wide enforcement backend
// against the given client. client must be non-nil; nil clients panic so
// the configuration error surfaces at startup rather than at admission
// time (the strict-fail-closed contract forbids silent fallback).
func NewRedisEnforceBackend(client redis.UniversalClient, instanceID string) *RedisEnforceBackend {
	if client == nil {
		panic("dispatch: NewRedisEnforceBackend requires non-nil redis client")
	}
	if instanceID == "" {
		instanceID = "redis-enforce"
	}
	return &RedisEnforceBackend{
		client:     client,
		instanceID: instanceID,
		closedCh:   make(chan struct{}),
		acquireS:   redis.NewScript(redisEnforceAcquireLua),
		releaseS:   redis.NewScript(redisEnforceReleaseLua),
	}
}

// Kind is BackendRedisEnforce — the closed-enum identifier Stage C
// metrics will surface.
func (b *RedisEnforceBackend) Kind() GovernorBackendKind { return BackendRedisEnforce }

// Name is the free-form diagnostic identifier.
func (b *RedisEnforceBackend) Name() string { return b.instanceID }

// Open is idempotent.
func (b *RedisEnforceBackend) Open(_ context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		// Reopen path — keep the same channel identity if not yet closed.
		select {
		case <-b.closedCh:
			b.closedCh = make(chan struct{})
		default:
		}
		b.closed = false
	}
	b.opened = true
	return nil
}

// Close is idempotent and safe on unopened backends.
func (b *RedisEnforceBackend) Close(_ context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.opened {
		return nil
	}
	b.opened = false
	b.closed = true
	select {
	case <-b.closedCh:
		// already closed
	default:
		close(b.closedCh)
	}
	return nil
}

// NotifyRevisions records the latest policy revision observed. Stage B's
// enforcement path does not consult this; it exists on the interface so
// Stage E can wire ApplyPolicySnapshot uniformly across backends.
func (b *RedisEnforceBackend) NotifyRevisions(_ context.Context, rev uint64) error {
	b.mu.Lock()
	b.revision = rev
	b.mu.Unlock()
	return nil
}

// New constructs a per-spec RedisEnforceGovernor. The returned Governor
// shares the underlying *redis.Script handles via closure, but owns its
// own lease map (keyed by *QueuedRequest pointer) and redisKey.
func (b *RedisEnforceBackend) New(ctx context.Context, spec GovernorSpec) (Governor, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrGovernorUnavailable, err)
	}
	if spec.Limit <= 0 {
		// Zero / negative limit is a configuration error — refusing to
		// admit anything would surprise the operator, so we degrade to
		// noop rather than silent-overadmission. Wrap with
		// ErrGovernorUnavailable so callers can classify it.
		return nil, fmt.Errorf("%w: non-positive limit for spec %d/%d (mode=%s)",
			ErrGovernorUnavailable, spec.ProviderID, spec.CredentialID, spec.Mode)
	}
	if b.specBackendMismatch(spec) {
		return nil, fmt.Errorf("%w: spec backend=%s, this=%s",
			ErrGovernorUnavailable, spec.Backend, b.Kind())
	}
	ttl := spec.LeaseTTL
	if ttl <= 0 {
		ttl = defaultRedisEnforceTTL
	}
	mode := spec.Mode
	if mode == "" {
		mode = ModeConcurrency
	}
	return &redisEnforceGovernor{
		backend:      b,
		spec:         spec,
		key:          redisGovernorKey(spec.ProviderID, spec.CredentialID, mode),
		limit:        spec.Limit,
		ttl:          ttl,
		mode:         mode,
		leases:       map[*QueuedRequest]string{},
		tokenCounter: new(uint64),
	}, nil
}

func (b *RedisEnforceBackend) specBackendMismatch(spec GovernorSpec) bool {
	if spec.Backend == "" {
		return false // empty is "no preference" — backwards-compatible
	}
	return spec.Backend != BackendRedisEnforce
}

// ── RedisShadowBackend ───────────────────────────────────────────────────

// RedisShadowBackend observes cluster Redis state without gating
// admission. It is intended for soak-testing the enforcement path's
// Lua scripts and key shapes before operators flip
// LLM_GATEWAY_DISPATCH_GOVERNOR_BACKEND to redis_enforce.
//
// Even when the client is unreachable, Acquire returns a synthetic lease
// (Backend = BackendRedisShadow, zero times, empty token) so the
// forwarder's Release path stays a no-op without surfacing
// ErrGovernorUnavailable.
type RedisShadowBackend struct {
	client     redis.UniversalClient
	instanceID string

	mu       sync.Mutex
	opened   bool
	closed   bool
	revision uint64

	// acquireS is the only script the shadow variant uses — to record
	// what would have happened. The probe errors are swallowed.
	acquireS *redis.Script
}

// NewRedisShadowBackend constructs the shadow observer. nil clients panic
// for symmetry with the enforce variant so the same startup-time check
// catches configuration mistakes regardless of which kind is being wired.
func NewRedisShadowBackend(client redis.UniversalClient, instanceID string) *RedisShadowBackend {
	if client == nil {
		panic("dispatch: NewRedisShadowBackend requires non-nil redis client")
	}
	if instanceID == "" {
		instanceID = "redis-shadow"
	}
	return &RedisShadowBackend{
		client:     client,
		instanceID: instanceID,
		acquireS:   redis.NewScript(redisEnforceAcquireLua),
	}
}

// Kind is BackendRedisShadow — never gates admission.
func (b *RedisShadowBackend) Kind() GovernorBackendKind { return BackendRedisShadow }

// Name is the free-form diagnostic identifier.
func (b *RedisShadowBackend) Name() string { return b.instanceID }

// Open is idempotent.
func (b *RedisShadowBackend) Open(_ context.Context) error {
	b.mu.Lock()
	b.opened = true
	b.mu.Unlock()
	return nil
}

// Close is idempotent. Safe on unopened backends.
func (b *RedisShadowBackend) Close(_ context.Context) error {
	b.mu.Lock()
	b.opened = false
	b.closed = true
	b.mu.Unlock()
	return nil
}

// NotifyRevisions is a no-op for the same reason the enforce variant
// tracks but doesn't yet consume it.
func (b *RedisShadowBackend) NotifyRevisions(_ context.Context, rev uint64) error {
	b.mu.Lock()
	b.revision = rev
	b.mu.Unlock()
	return nil
}

// New constructs a per-spec RedisShadowGovernor. Errors from the
// management layer's New call are still wrapped — so even shadow
// configurations with garbage specs fail closed at construction — but
// once constructed, every Acquire returns immediately without touching
// Redis.
func (b *RedisShadowBackend) New(_ context.Context, spec GovernorSpec) (Governor, error) {
	if b.specBackendMismatch(spec) {
		return nil, fmt.Errorf("%w: spec backend=%s, this=%s",
			ErrGovernorUnavailable, spec.Backend, b.Kind())
	}
	mode := spec.Mode
	if mode == "" {
		mode = ModeConcurrency
	}
	return &redisShadowGovernor{
		backend: b,
		spec:    spec,
		key:     redisGovernorKey(spec.ProviderID, spec.CredentialID, mode),
		mode:    mode,
	}, nil
}

func (b *RedisShadowBackend) specBackendMismatch(spec GovernorSpec) bool {
	if spec.Backend == "" {
		return false
	}
	return spec.Backend != BackendRedisShadow
}

// ── Lua-key helper ────────────────────────────────────────────────────────

// redisGovernorKey returns the Cluster-safe hash-tagged key. The single
// hash-tag segment {<providerID>:<credentialID>:<mode>} pins all of a
// credential's mode namespaces to the same slot. Tested by
// TestRedisEnforceLuaKeyShapeHashTagged in B.1.
func redisGovernorKey(providerID, credentialID int, mode string) string {
	return "llmgw:gov:{" + strconv.Itoa(providerID) + ":" +
		strconv.Itoa(credentialID) + ":" + mode + "}:lock"
}

// ── redisEnforceGovernor ─────────────────────────────────────────────────

// redisEnforceGovernor is the per-spec Governor returned by
// RedisEnforceBackend.New. It enforces the Lua-acquire / Lua-release
// contract from B.1's tests, maintains an in-memory lease table (keyed
// by *QueuedRequest) so Release can pass the right token back, and is
// the boundary that translates Redis errors into wrapped
// ErrGovernorUnavailable errors.
type redisEnforceGovernor struct {
	backend *RedisEnforceBackend
	spec    GovernorSpec
	key     string
	limit   int
	ttl     time.Duration
	mode    string

	leasesMu sync.Mutex
	leases   map[*QueuedRequest]string

	tokenCounter *uint64
}

func (g *redisEnforceGovernor) Mode() string { return g.mode }

// Acquire blocks (capped at giveUp + ctx.Done) until the Lua acquire
// script returns ready, then stashes the per-call token in the leases map
// keyed by qr so the matching Release can pass it back into Lua.
//
// On Redis error (any) wraps ErrGovernorUnavailable with the original
// cause — strictly fail-closed, no memory fallback.
func (g *redisEnforceGovernor) Acquire(ctx context.Context, qr *QueuedRequest, giveUp time.Time) error {
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: %w", ErrGovernorUnavailable, err)
		}
		if !time.Now().Before(giveUp) {
			return errPaceTimeout
		}
		token := g.nextToken()
		raw, err := g.backend.acquireS.Run(ctx, g.backend.client,
			[]string{g.key},
			g.limit, g.ttl.Milliseconds(), token).Slice()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				// Unexpected empty result without script error → treat as saturated.
				return fmt.Errorf("%w: empty lua result", ErrGovernorUnavailable)
			}
			return fmt.Errorf("%w: %w", ErrGovernorUnavailable, err)
		}
		if len(raw) < 3 {
			return fmt.Errorf("%w: unexpected lua result length=%d", ErrGovernorUnavailable, len(raw))
		}
		code, _ := raw[0].(int64)
		state, _ := raw[2].(string)
		switch code {
		case 1:
			g.leasesMu.Lock()
			g.leases[qr] = token
			g.leasesMu.Unlock()
			return nil
		case 0:
			// Saturated. Sleep briefly and retry, capped by giveUp.
			g.leasesMu.Lock()
			delete(g.leases, qr)
			g.leasesMu.Unlock()
			g.waitOrGiveUp(ctx, giveUp)
			continue
		default:
			return fmt.Errorf("%w: lua state=%q", ErrGovernorUnavailable, state)
		}
	}
}

// Release removes the per-call token previously stored by Acquire. If no
// token is recorded (e.g. due to denials that cleared the slot), this is
// a no-op. The Lua script is itself idempotent on the wire, so duplicate
// Releases from the forwarder's defer-once paths stay safe.
func (g *redisEnforceGovernor) Release(qr *QueuedRequest) {
	g.leasesMu.Lock()
	token := g.leases[qr]
	if token == "" {
		g.leasesMu.Unlock()
		return
	}
	delete(g.leases, qr)
	g.leasesMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), defaultRedisDialTimeout)
	defer cancel()
	// Errors here are intentionally silent: Release is best-effort and
	// runs on the defer-path. The lease will expire by TTL anyway, and
	// the unavailability path is observable from Acquire.
	_, _ = g.backend.releaseS.Run(ctx, g.backend.client,
		[]string{g.key}, token).Int64()
}

// nextToken returns a per-call unique token. Format is opaque to callers
// and carries no secret material.
func (g *redisEnforceGovernor) nextToken() string {
	t := time.Now().UnixNano()
	n := atomic.AddUint64(g.tokenCounter, 1)
	return strconv.FormatInt(t, 36) + ":" + strconv.FormatUint(n, 36) +
		":" + strconv.FormatUint(g.spec.Revision, 36)
}

// waitOrGiveUp is the pacing sleep between saturation retries. Mirrors
// the cadence of concurrencyGovernor (2ms) so the behavior under heavy
// load is comparable between Local and Redis-enforce backends.
func (g *redisEnforceGovernor) waitOrGiveUp(ctx context.Context, giveUp time.Time) {
	if !time.Now().Before(giveUp) {
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Millisecond):
	}
}

// ── redisShadowGovernor ─────────────────────────────────────────────────

// redisShadowGovernor is the per-spec Governor returned by
// RedisShadowBackend.New. Acquire always returns immediately (with a
// synthetic, non-tokenized lease) and Release is a no-op. Even when the
// underlying Redis client is unreachable the shadow variant never gates
// admission — that is the soak-test contract.
type redisShadowGovernor struct {
	backend *RedisShadowBackend
	spec    GovernorSpec
	key     string
	mode    string
}

func (g *redisShadowGovernor) Mode() string { return g.mode }

// Acquire returns nil immediately. The synthetic lease (zero times,
// empty token) is observable on the returned *Lease shape via the
// per-spec LeaseStem, but the existing Governor interface cannot
// surface it — Stage D's hook into credForwarder will read it via the
// per-call sidecar that B.2 lays the groundwork for.
func (g *redisShadowGovernor) Acquire(_ context.Context, _ *QueuedRequest, _ time.Time) error {
	return nil
}

// Release is a no-op for the shadow variant. Equivalence with
// LocalBackend.Release in the disabled mode is the intended steady state.
func (g *redisShadowGovernor) Release(_ *QueuedRequest) {}

// ── Compile-time conformance ──────────────────────────────────────────────

var (
	_ GovernorBackend = (*RedisEnforceBackend)(nil)
	_ GovernorBackend = (*RedisShadowBackend)(nil)
	_ Governor        = (*redisEnforceGovernor)(nil)
	_ Governor        = (*redisShadowGovernor)(nil)
)
