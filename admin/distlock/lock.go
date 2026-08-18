package distlock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Mode controls the leader-follower behavior. Currently only WaitFollower
// is supported; the field is here for future flexibility (e.g. a "skip"
// mode where followers immediately return without waiting).
type Mode int

const (
	// ModeWaitFollower makes followers block on Wait until the leader
	// releases or the lock TTL elapses. This is the only mode used by
	// the title pipeline; the caller re-checks the persisted state
	// after Wait returns to decide whether to skip or to retry as a
	// new leader.
	ModeWaitFollower Mode = iota
)

// AcquireOpts parameterizes Acquire. Key is required; TTL defaults to 60s
// when zero. Mode defaults to ModeWaitFollower.
type AcquireOpts struct {
	Key  string
	TTL  time.Duration
	Mode Mode
}

// Manager is the entry-point interface implemented by both RedisManager
// and LocalManager. Acquire returns a Handle whose IsLeader / Wait /
// Release methods implement the leader-follower protocol.
//
// Enabled reports whether the underlying transport is usable. Callers
// MAY short-circuit Acquire entirely when Enabled is false (the title
// pipeline does exactly this so the in-process LocalManager is the only
// path in no-Redis deployments).
type Manager interface {
	Acquire(ctx context.Context, opts AcquireOpts) (*Handle, error)
	Enabled() bool
}

// releaseChannelSuffix is appended to the lock key to derive the pub/sub
// channel that the leader publishes on Release. Keep in sync with the
// Lua compare-and-delete in releaseScript.
const releaseChannelSuffix = ":release"

// releaseScript: only delete the key if we still own the token, and
// publish on the release channel before deleting so concurrent followers
// receive the wake-up event. Without the token check a leader whose TTL
// expired and whose Release was delayed would delete a fresh lock
// acquired by a follower (or a subsequent leader).
const releaseScript = `if redis.call('GET', KEYS[1]) == ARGV[1] then
	redis.call('PUBLISH', KEYS[2], ARGV[1])
	return redis.call('DEL', KEYS[1])
end
return 0`

// RedisManager is the production implementation backed by go-redis.
// All goroutines that share a *redis.Client should share one *RedisManager.
type RedisManager struct {
	rdb *redis.Client
}

// NewRedisManager constructs a manager. rdb may be nil; in that case
// Enabled returns false and Acquire returns ErrNotEnabled. The caller is
// expected to gate Acquire behind Enabled.
func NewRedisManager(rdb *redis.Client) *RedisManager {
	return &RedisManager{rdb: rdb}
}

// Enabled reports whether the Redis transport is wired. Callers should
// short-circuit Acquire when this is false.
func (m *RedisManager) Enabled() bool {
	return m != nil && m.rdb != nil
}

// ErrNotEnabled is returned by Acquire when the manager has no Redis
// client. Callers should treat this as "skip the lock" rather than a
// hard error.
var ErrNotEnabled = errors.New("distlock: redis client not configured")

// Acquire takes the SETNX lock at opts.Key, or subscribes to the release
// channel as a follower if the lock is already held. The returned Handle
// distinguishes leader vs follower via IsLeader; the caller MUST call
// Release exactly once.
//
// Returned errors:
//
//   - ErrNotEnabled: rdb is nil. Caller should fall back to LocalManager
//     or skip the lock entirely.
//   - ctx.Err(): the caller's context was cancelled while doing the
//     initial SetNX / Subscribe handshake.
//   - any other error: Redis is temporarily unavailable. Caller should
//     log + proceed without the lock (DB ON CONFLICT is the final guard).
func (m *RedisManager) Acquire(ctx context.Context, opts AcquireOpts) (*Handle, error) {
	if !m.Enabled() {
		return nil, ErrNotEnabled
	}
	if opts.Key == "" {
		return nil, errors.New("distlock: opts.Key is required")
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	token, err := newToken()
	if err != nil {
		return nil, fmt.Errorf("distlock: token gen: %w", err)
	}

	ok, setErr := m.rdb.SetNX(ctx, opts.Key, token, ttl).Result()
	if setErr != nil {
		return nil, fmt.Errorf("distlock: SetNX %q: %w", opts.Key, setErr)
	}
	if ok {
		// Leader path. The backend closure does the Lua compare-and-delete
		// (which also publishes the release event).
		backend := &redisBackend{
			rdb:   m.rdb,
			key:   opts.Key,
			token: token,
		}
		return &Handle{
			leader:  true,
			key:     opts.Key,
			ttl:     ttl,
			backend: backend,
		}, nil
	}

	// Follower path: subscribe to the release channel. The subscription
	// must outlive the caller's request context (a 45s timeout upstream
	// can expire before the leader releases), so we use context.Background
	// for the SUBSCRIBE handshake and surface caller's ctx cancellation
	// via Handle.Wait.
	subCtx := context.Background()
	pubsub := m.rdb.Subscribe(subCtx, opts.Key+releaseChannelSuffix)
	if _, err := pubsub.Receive(subCtx); err != nil {
		_ = pubsub.Close()
		return nil, fmt.Errorf("distlock: follower subscribe %q: %w", opts.Key, err)
	}
	ch := pubsub.Channel()
	// Re-check after the subscription handshake. A leader can release after
	// SetNX reports contention but before Redis registers this subscriber.
	if _, err := m.rdb.Get(subCtx, opts.Key).Result(); errors.Is(err, redis.Nil) {
		_ = pubsub.Close()
		return &Handle{leader: false, key: opts.Key, ttl: 0}, nil
	} else if err != nil {
		_ = pubsub.Close()
		return nil, fmt.Errorf("distlock: follower recheck %q: %w", opts.Key, err)
	}

	// Read the actual remaining TTL from Redis so the follower's wait
	// timer matches the real lock expiry. If the key is already gone
	// (PTTL returns -2) or has no expiry (-1), treat the lock as already
	// released and return a follower that will wake immediately.
	var followerTTL time.Duration
	if rem, err := m.rdb.PTTL(subCtx, opts.Key).Result(); err == nil {
		if rem > 0 {
			followerTTL = rem
		} else {
			followerTTL = 0 // key already gone, will wake immediately
		}
	} else {
		// PTTL failed; fall back to the original TTL. This is conservative
		// (may wait a bit longer than the real TTL) but safe.
		followerTTL = ttl
	}
	return &Handle{
		leader:  false,
		key:     opts.Key,
		ttl:     followerTTL,
		channel: ch,
		pubsub:  pubsub,
		backend: &redisBackend{rdb: m.rdb, key: opts.Key},
	}, nil
}

// redisBackend implements the Handle.backend interface for the Redis
// manager. The leader path needs the token to do the compare-and-delete;
// the follower path does not need it.
type redisBackend struct {
	rdb   *redis.Client
	key   string
	token string // empty for follower handles
}

// leaderRelease does the Lua compare-and-delete (which also publishes the
// release event for followers). Called from Handle.Release.
func (b *redisBackend) leaderRelease(ctx context.Context) {
	if b == nil || b.rdb == nil || b.token == "" {
		return
	}
	// Use a fresh, short context so a request context that already
	// expired (45s timeout upstream) does not abort the DEL itself.
	ctxUse, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if _, err := b.rdb.Eval(ctxUse, releaseScript,
		[]string{b.key, b.key + releaseChannelSuffix},
		b.token).Result(); err != nil {
		slog.Debug("distlock: leader release EVAL failed",
			"key", b.key, "error", err.Error())
	}
}

// followerClose closes the pub/sub subscription. Called from
// Handle.Release for followers.
func (b *redisBackend) followerClose() {
	if b == nil {
		return
	}
	// pubsub is owned by the Handle, not the backend; see Handle.Release.
}

// Handle is what Acquire returns. It is safe to call IsLeader/Wait/Release
// from any goroutine. The Handle is single-use: after Release is called
// the Handle is invalid.
type Handle struct {
	leader  bool
	key     string
	ttl     time.Duration
	backend handleBackend

	// Follower-only transport state. Exactly one of {channel, localWait}
	// is non-nil depending on which manager issued the handle.
	channel   <-chan *redis.Message // Redis pub/sub channel (RedisManager follower)
	localWait <-chan struct{}       // leader's release chan (LocalManager follower)
	pubsub    *redis.PubSub         // Redis pub/sub handle (RedisManager follower)

	mu          sync.Mutex
	releaseOnce sync.Once
}

// handleBackend is the small surface Handle.Release / Wait use to
// dispatch to the right transport. Each implementation lives in the
// file that owns the Manager.
type handleBackend interface {
	leaderRelease(ctx context.Context)
	followerClose()
}

// IsLeader reports whether this handle is the leader (i.e. the only
// goroutine that should run the protected operation). Always returns
// false on a nil Handle so defer h.Release(...) never panics.
func (h *Handle) IsLeader() bool {
	return h != nil && h.leader
}

// Wait blocks until the leader releases, the lock TTL expires, or ctx is
// cancelled. Returns:
//
//   - nil: the leader released normally. The follower's caller should
//     re-check the persisted state to decide whether to skip or to
//     retry as the new leader.
//   - ctx.Err(): the waiter's context expired first.
//   - ErrTTLExpired: the lock TTL elapsed without a release event.
//   - ErrPubSubClosed: the pub/sub channel closed unexpectedly (Redis
//     disconnect, server restart).
//
// No-op on a leader handle (returns nil immediately).
func (h *Handle) Wait(ctx context.Context) error {
	if h == nil || h.leader {
		return nil
	}
	h.mu.Lock()
	channel := h.channel
	localWait := h.localWait
	ttl := h.ttl
	h.mu.Unlock()
	ttlTimer := time.NewTimer(ttl)
	defer ttlTimer.Stop()
	switch {
	case channel != nil:
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ttlTimer.C:
				return ErrTTLExpired
			case msg, ok := <-channel:
				if !ok {
					return ErrPubSubClosed
				}
				if msg != nil {
					_ = msg // payload is the leader's token; we don't need it
				}
				return nil
			}
		}
	case localWait != nil:
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ttlTimer.C:
			return ErrTTLExpired
		case <-localWait:
			return nil
		}
	default:
		return nil
	}
}

// ErrTTLExpired is returned by Handle.Wait when the lock TTL elapses
// without a leader release. The follower's caller is expected to
// re-check persisted state.
var ErrTTLExpired = errors.New("distlock: leader TTL expired")

// ErrPubSubClosed is returned by Handle.Wait when the pub/sub channel
// closes without a release message (Redis disconnect, server restart).
var ErrPubSubClosed = errors.New("distlock: pubsub channel closed unexpectedly")

// Release releases the lock. Safe to call on a nil Handle (no-op). For
// leaders this issues the Lua compare-and-delete that wakes followers +
// removes the key. For followers this closes the pub/sub subscription
// (RedisManager) or is a no-op (LocalManager — the channel is owned by
// the leader and is closed by the leader's Release).
//
// Idempotent: subsequent calls are no-ops.
func (h *Handle) Release(ctx context.Context) {
	if h == nil {
		return
	}
	h.releaseOnce.Do(func() {
		h.mu.Lock()
		leader := h.leader
		backend := h.backend
		pubsub := h.pubsub
		h.backend = nil
		h.pubsub = nil
		h.channel = nil
		h.localWait = nil
		h.mu.Unlock()

		if leader {
			if backend != nil {
				backend.leaderRelease(ctx)
			}
			return
		}
		if pubsub != nil {
			_ = pubsub.Close()
		}
		if backend != nil {
			backend.followerClose()
		}
	})
}

// newToken returns a 128-bit random hex string used to make the leader's
// Release safe against accidental key ownership transfer.
func newToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
