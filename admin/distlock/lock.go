package distlock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// Mode controls the leader-follower behavior.
type Mode int

const (
	ModeWaitFollower Mode = iota
)

const (
	releaseChannelSuffix = ":release"
	defaultTTL           = 60 * time.Second
	handshakeTimeout     = 3 * time.Second
)

var (
	ErrNotEnabled      = errors.New("distlock: redis client not configured")
	ErrInvalidKey      = errors.New("distlock: key is required")
	ErrUnsupportedMode = errors.New("distlock: unsupported mode")
	ErrTTLExpired      = errors.New("distlock: leader TTL expired")
	ErrPubSubClosed    = errors.New("distlock: pubsub channel closed unexpectedly")
	ErrHandleReleased  = errors.New("distlock: handle released")
	ErrLockReplaced    = errors.New("distlock: lock ownership replaced")
	ErrPTTLInvalid     = errors.New("distlock: lock key has no expiry")
	ErrLeaseLost       = errors.New("distlock: lease ownership lost")
)

// BuildKey creates a Redis Cluster-safe key. The release channel is derived
// inside the release script, so only this key participates in EVAL routing.
func BuildKey(namespace, logicalKey string) string {
	return "llmgw:distlock:{" + strings.TrimSpace(namespace) + ":" + strings.TrimSpace(logicalKey) + "}:lock"
}

// AcquireOpts parameterizes Acquire. Scope is a bounded observability hint.
type AcquireOpts struct {
	Key   string
	TTL   time.Duration
	Mode  Mode
	Scope string
}

type Manager interface {
	Acquire(ctx context.Context, opts AcquireOpts) (*Handle, error)
	Enabled() bool
}

const releaseScript = `if redis.call('GET', KEYS[1]) == ARGV[1] then
	redis.call('PUBLISH', KEYS[1] .. ':release', ARGV[1])
	return redis.call('DEL', KEYS[1])
end
return 0`

const renewScript = `if redis.call('GET', KEYS[1]) == ARGV[1] then
	return redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return 0`

type RedisManager struct{ rdb *redis.Client }

func NewRedisManager(rdb *redis.Client) *RedisManager { return &RedisManager{rdb: rdb} }
func (m *RedisManager) Enabled() bool                 { return m != nil && m.rdb != nil }

func normalizeTTL(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return defaultTTL
	}
	return ttl
}

func validMode(mode Mode) bool { return mode == ModeWaitFollower }

func handshakeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return context.WithCancel(ctx)
		}
		if remaining < handshakeTimeout {
			return context.WithTimeout(ctx, remaining)
		}
	}
	return context.WithTimeout(ctx, handshakeTimeout)
}

func (m *RedisManager) Acquire(ctx context.Context, opts AcquireOpts) (h *Handle, err error) {
	scope := normalizeLockScope(opts.Scope)
	result := "error"
	defer func() { distlockAcquireTotal.WithLabelValues(scope, result).Inc() }()

	if !m.Enabled() {
		result = "disabled"
		return nil, ErrNotEnabled
	}
	if strings.TrimSpace(opts.Key) == "" {
		result = "invalid"
		return nil, ErrInvalidKey
	}
	if !validMode(opts.Mode) {
		result = "invalid"
		return nil, ErrUnsupportedMode
	}

	ttl := normalizeTTL(opts.TTL)
	token, err := newToken()
	if err != nil {
		return nil, fmt.Errorf("distlock: token gen: %w", err)
	}
	ok, err := m.rdb.SetNX(ctx, opts.Key, token, ttl).Result()
	if err != nil {
		return nil, fmt.Errorf("distlock: SetNX %q: %w", opts.Key, err)
	}
	if ok {
		backend := &redisBackend{rdb: m.rdb, key: opts.Key, token: token, ttl: ttl, scope: scope}
		h = newLeaderHandle(opts.Key, ttl, scope, backend)
		backend.startRenewal(h)
		result = "leader"
		return h, nil
	}

	handshakeCtx, cancel := handshakeContext(ctx)
	defer cancel()
	pubsub := m.rdb.Subscribe(handshakeCtx, opts.Key+releaseChannelSuffix)
	if _, err := pubsub.Receive(handshakeCtx); err != nil {
		_ = pubsub.Close()
		return nil, fmt.Errorf("distlock: follower subscribe %q: %w", opts.Key, err)
	}
	ownerToken, err := m.rdb.Get(handshakeCtx, opts.Key).Result()
	if errors.Is(err, redis.Nil) {
		_ = pubsub.Close()
		result = "follower"
		return newTerminalFollower(opts.Key, scope, ErrTTLExpired), nil
	}
	if err != nil {
		_ = pubsub.Close()
		return nil, fmt.Errorf("distlock: follower recheck %q: %w", opts.Key, err)
	}
	remaining, err := m.rdb.PTTL(handshakeCtx, opts.Key).Result()
	if err != nil {
		_ = pubsub.Close()
		return nil, fmt.Errorf("distlock: follower PTTL %q: %w", opts.Key, err)
	}
	if remaining == -1*time.Millisecond {
		_ = pubsub.Close()
		return nil, ErrPTTLInvalid
	}
	if remaining == -2*time.Millisecond || remaining <= 0 {
		_ = pubsub.Close()
		result = "follower"
		return newTerminalFollower(opts.Key, scope, ErrTTLExpired), nil
	}

	h = newFollowerHandle(opts.Key, remaining, scope, ownerToken, pubsub)
	h.startRedisWatcher(m.rdb)
	result = "follower"
	return h, nil
}

type redisBackend struct {
	rdb   *redis.Client
	key   string
	token string
	ttl   time.Duration
	scope string
	local handleBackend

	stopRenew chan struct{}
	renewDone chan struct{}
	lost      atomic.Bool
}

func (b *redisBackend) startRenewal(h *Handle) {
	if b == nil || b.local != nil || b.rdb == nil || b.token == "" || b.ttl <= 0 {
		return
	}
	interval := b.ttl / 3
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	b.stopRenew = make(chan struct{})
	b.renewDone = make(chan struct{})
	go func() {
		defer close(b.renewDone)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-b.stopRenew:
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
				result, err := b.rdb.Eval(ctx, renewScript, []string{b.key}, b.token, b.ttl.Milliseconds()).Int()
				cancel()
				if err != nil {
					distlockRenewTotal.WithLabelValues(b.scope, "error").Inc()
					continue
				}
				if result == 0 {
					b.lost.Store(true)
					h.markLeaseLost()
					distlockRenewTotal.WithLabelValues(b.scope, "lost").Inc()
					return
				}
				distlockRenewTotal.WithLabelValues(b.scope, "success").Inc()
			}
		}
	}()
}

func (b *redisBackend) stopRenewal() {
	if b == nil || b.stopRenew == nil {
		return
	}
	close(b.stopRenew)
	<-b.renewDone
	b.stopRenew = nil
}

func (b *redisBackend) leaderRelease(ctx context.Context) error {
	if b != nil && b.local != nil {
		return b.local.leaderRelease(ctx)
	}
	if b == nil || b.rdb == nil || b.token == "" {
		return nil
	}
	b.stopRenewal()
	ctxUse, cancel := context.WithTimeout(context.WithoutCancel(ctx), handshakeTimeout)
	defer cancel()
	deleted, err := b.rdb.Eval(ctxUse, releaseScript, []string{b.key}, b.token).Int()
	if err != nil {
		distlockReleaseTotal.WithLabelValues(b.scope, "error").Inc()
		slog.Warn("distlock: leader release EVAL failed", "key", b.key, "error", err)
		return err
	}
	if deleted == 0 {
		distlockReleaseTotal.WithLabelValues(b.scope, "not_owner").Inc()
		return ErrLeaseLost
	}
	distlockReleaseTotal.WithLabelValues(b.scope, "success").Inc()
	return nil
}

func (b *redisBackend) check(ctx context.Context) error {
	if b != nil && b.local != nil {
		return b.local.check(ctx)
	}
	if b == nil || b.rdb == nil || b.token == "" || b.lost.Load() {
		return ErrLeaseLost
	}
	value, err := b.rdb.Get(ctx, b.key).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			b.lost.Store(true)
			return ErrLeaseLost
		}
		return fmt.Errorf("distlock: check %q: %w", b.key, err)
	}
	if value != b.token {
		b.lost.Store(true)
		return ErrLeaseLost
	}
	return nil
}

type Handle struct {
	leader  bool
	key     string
	ttl     time.Duration
	scope   string
	backend handleBackend

	expectedToken string
	channel       <-chan *redis.Message
	localWait     <-chan struct{}
	pubsub        *redis.PubSub

	terminalDone chan struct{}
	terminalOnce sync.Once
	terminalErr  error
	stateMu      sync.RWMutex
	waitMetric   sync.Once
	releaseOnce  sync.Once
	leaseLost    atomic.Bool
}

type handleBackend interface {
	leaderRelease(ctx context.Context) error
	check(ctx context.Context) error
}

func newLeaderHandle(key string, ttl time.Duration, scope string, backend handleBackend) *Handle {
	return &Handle{leader: true, key: key, ttl: ttl, scope: scope, backend: backend}
}

func newFollowerHandle(key string, ttl time.Duration, scope, expectedToken string, pubsub *redis.PubSub) *Handle {
	return &Handle{key: key, ttl: ttl, scope: scope, expectedToken: expectedToken, channel: pubsub.Channel(), pubsub: pubsub, terminalDone: make(chan struct{})}
}

func newLocalFollowerHandle(key, scope string, wait <-chan struct{}, backend handleBackend) *Handle {
	h := &Handle{key: key, scope: scope, localWait: wait, terminalDone: make(chan struct{}), backend: backend}
	go func() {
		select {
		case <-wait:
			h.finish(nil)
		case <-h.terminalDone:
		}
	}()
	return h
}

func newTerminalFollower(key, scope string, err error) *Handle {
	h := &Handle{key: key, scope: scope, terminalDone: make(chan struct{})}
	h.finish(err)
	return h
}

func (h *Handle) startRedisWatcher(rdb *redis.Client) {
	go func() {
		timer := time.NewTimer(h.ttl)
		defer timer.Stop()
		for {
			select {
			case <-h.terminalDone:
				return
			case <-timer.C:
				h.finish(ErrTTLExpired)
				return
			case msg, ok := <-h.channel:
				if !ok {
					h.finish(ErrPubSubClosed)
					return
				}
				if msg == nil || msg.Payload != h.expectedToken {
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
				value, err := rdb.Get(ctx, h.key).Result()
				cancel()
				if errors.Is(err, redis.Nil) {
					h.finish(nil)
					return
				}
				if err != nil {
					h.finish(fmt.Errorf("distlock: follower release recheck %q: %w", h.key, err))
					return
				}
				if value != h.expectedToken {
					h.finish(ErrLockReplaced)
					return
				}
			}
		}
	}()
}

func (h *Handle) finish(err error) {
	if h == nil || h.terminalDone == nil {
		return
	}
	h.terminalOnce.Do(func() {
		h.stateMu.Lock()
		h.terminalErr = err
		h.stateMu.Unlock()
		close(h.terminalDone)
	})
}

func (h *Handle) terminalResult() error {
	h.stateMu.RLock()
	defer h.stateMu.RUnlock()
	return h.terminalErr
}

func (h *Handle) markLeaseLost() {
	if h != nil {
		h.leaseLost.Store(true)
	}
}

func (h *Handle) IsLeader() bool { return h != nil && h.leader }

// Check verifies that a leader still owns its lease before a durable side effect.
func (h *Handle) Check(ctx context.Context) error {
	if h == nil || !h.leader || h.leaseLost.Load() {
		return ErrLeaseLost
	}
	if h.backend == nil {
		return nil
	}
	if err := h.backend.check(ctx); err != nil {
		h.markLeaseLost()
		return err
	}
	return nil
}

func (h *Handle) Wait(ctx context.Context) error {
	if h == nil || h.leader {
		return nil
	}
	start := time.Now()
	defer h.waitMetric.Do(func() {
		distlockWaitSeconds.WithLabelValues(normalizeLockScope(h.scope)).Observe(time.Since(start).Seconds())
	})
	if h.terminalDone == nil {
		return ErrHandleReleased
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-h.terminalDone:
		return h.terminalResult()
	}
}

func (h *Handle) Release(ctx context.Context) {
	if h == nil {
		return
	}
	h.releaseOnce.Do(func() {
		if h.leader {
			if h.backend != nil {
				if err := h.backend.leaderRelease(ctx); err != nil {
					h.markLeaseLost()
				}
			}
			return
		}
		h.finish(ErrHandleReleased)
		pubsub := h.pubsub
		h.pubsub = nil
		if pubsub != nil {
			_ = pubsub.Close()
		}
	})
}

func newToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
