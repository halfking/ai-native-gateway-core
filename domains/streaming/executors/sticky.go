package executors

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/ratelimit"
)

// calculateSessionStickyTTL 根据模型类型计算动态 TTL（Phase 1）
func calculateSessionStickyTTL(model string) time.Duration {
	modelLower := strings.ToLower(model)

	// embedding 模型：短期任务
	if strings.Contains(modelLower, "embedding") || strings.Contains(modelLower, "embed") {
		return 30 * time.Second
	}

	// completion 模型：长文本生成（需要在 chat 之前检查，因为有些模型名包含两者）
	if strings.Contains(modelLower, "completion") || strings.Contains(modelLower, "instruct") ||
		strings.Contains(modelLower, "davinci") {
		return 30 * time.Minute
	}

	// chat 模型：对话上下文
	if strings.Contains(modelLower, "chat") || strings.Contains(modelLower, "gpt") ||
		strings.Contains(modelLower, "claude") || strings.Contains(modelLower, "gemini") {
		return 10 * time.Minute
	}

	// 默认：15分钟
	return 15 * time.Minute
}

// StickyLevel defines the priority hierarchy for sticky session streaming.
//
// 2026-06-25: Multi-level sticky strategy to handle the following scenarios:
//   - Same client, same session, same model → L1 (session-model sticky)
//   - Same client, different session, same model → L2 (client-model sticky)
//   - Same client, new model → L3 fallback (client baseline)
//
// This prevents cross-session and cross-model sticky pollution while
// maintaining stability within a single conversation context.
type StickyLevel int

const (
	// StickyLevelSession: highest priority, session + model scoped.
	// Format: {tenant}:{app}:{key}:{profile}:{session_id}:{model}
	// TTL: 1 hour (conversation lifetime)
	StickyLevelSession StickyLevel = 1

	// StickyLevelClientModel: medium priority, client + model scoped.
	// Format: {tenant}:{app}:{key}:{profile}:{model}
	// TTL: 60 seconds (short contamination window — see stickyL2TTL below)
	StickyLevelClientModel StickyLevel = 2

	// StickyLevelClient: lowest priority, client-only scoped.
	// Format: {tenant}:{app}:{key}:{profile}
	// TTL: 24 hours (baseline fallback)
	StickyLevelClient StickyLevel = 3
)

// 2026-08-26 fix: align TTLs across memory, DB, and Redis.
//
// Semantics (operator-confirmed):
//   - L1 (session+model) sticky: same session_id, multiple requests → reuse credential
//   - L2 (client+model) sticky: NEW sessions must NOT inherit a previous session's
//     credential; L2's TTL is the contamination window for that risk.
//     Reduce from 2h/24h → 60s so a stale L2 entry expires before it can lock
//     many new sessions to one credential.
//   - L3 (client baseline) sticky: 24h, only used as a fallback when no model info.
const (
	stickyL1DefaultTTL = 15 * time.Minute // calculateSessionStickyTTL default
	stickyL2TTL        = 60 * time.Second
	stickyL3TTL        = 24 * time.Hour
)

// StickyLookupResult holds the result of a multi-level sticky lookup.
type StickyLookupResult struct {
	CredentialID int
	Level        StickyLevel
	Found        bool
}

// StickyRedisStore 是 domains/ursm/v2/cache.StickyStore 的最小接口,
// 避免 executors → ursm/v2/cache 的硬依赖(本包只依赖接口)。
// 使用显式 level(1/2/3)避免 colon 段数启发式误判。
type StickyRedisStore interface {
	SetLevel(ctx context.Context, level int, credID int, rawKey string, ttl time.Duration) error
	GetLevel(ctx context.Context, level int, rawKey string) (int, bool)
	DeleteLevelIfCredential(ctx context.Context, level int, rawKey string, credID int) error
	// ClearForCredential scans the entire sticky namespace and deletes every
	// entry whose value equals credID. Used for hot-reload when admin changes
	// a credential's priority/weight/concurrency — clear stale pins so new
	// sessions can re-enter load balancing.
	ClearForCredential(ctx context.Context, credID int) (int, error)
}

const stickyRedisWriteTimeout = 50 * time.Millisecond

type StickyCache struct {
	mu         sync.RWMutex
	items      map[string]stickyEntry
	dbPool     *pgxpool.Pool
	redisStore StickyRedisStore
	// 2026-08-06 FIX (P2-4): Add goroutine lifecycle management
	stopSweep chan struct{}
	sweepDone sync.WaitGroup
}

type stickyEntry struct {
	credentialID int
	failures     int // legacy field; preserved for backward compatibility
	// AUDIT-1: 连续失败追踪。
	// consecutiveFailures 在 RecordFailure 中递增；
	// 当两次失败间隔 > 10s 时重置为 1。
	// 达到阈值（默认 2）→ 删除 entry，下次请求重新走路由选择。
	consecutiveFailures int
	lastFailureAt       time.Time
	expiresAt           time.Time
}

func NewStickyCache() *StickyCache {
	c := &StickyCache{
		items:     make(map[string]stickyEntry),
		stopSweep: make(chan struct{}),
	}
	// Clear all bindings when the rate-limit gate transitions to disabled.
	// This avoids stale sticky entries from before the gate-off interval
	// affecting routing once the gate is re-enabled.
	ratelimit.RegisterTransitionHandler(func(enabled bool) {
		if !enabled {
			c.Clear()
		}
	})
	// Background sweeper: prevent unbounded memory growth from lazy-only TTL
	// expiry. Expired entries that are never Get'd again accumulate in items
	// indefinitely without this periodic cleanup.
	// 2026-08-06 FIX (P2-4): Track goroutine lifecycle for graceful shutdown.
	c.sweepDone.Add(1)
	go c.sweepLoop()
	return c
}

// sweepLoop periodically removes expired entries from the in-memory map.
// 2026-08-06 FIX (P2-4): Now supports graceful shutdown via stopSweep channel.
func (s *StickyCache) sweepLoop() {
	defer s.sweepDone.Done()
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopSweep:
			return
		case <-ticker.C:
			s.sweepExpired()
		}
	}
}

// sweepExpired removes all entries whose expiresAt has passed.
func (s *StickyCache) sweepExpired() {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.items {
		if now.After(v.expiresAt) {
			delete(s.items, k)
		}
	}
}

// Clear removes every sticky binding from the cache.
func (s *StickyCache) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.items {
		delete(s.items, k)
	}
}

// ClearForCredential removes every sticky binding that points at credID,
// across the in-memory map and (if present) the Redis store. Returns the
// total number of bindings cleared.
//
// 2026-08-26 hot-reload hook: when admin changes a credential's priority /
// weight / concurrency, we clear the stale sticky pins so new sessions
// don't inherit the previous credential while in-flight sessions complete
// naturally. Currently-running sessions keep their L1 entry until they
// record success again (which writes the same level keys but with refreshed
// TTL).
func (s *StickyCache) ClearForCredential(credID int) (int, error) {
	if credID <= 0 {
		return 0, nil
	}
	cleared := 0

	s.mu.Lock()
	for k, v := range s.items {
		if v.credentialID == credID {
			delete(s.items, k)
			cleared++
		}
	}
	store := s.redisStore
	s.mu.Unlock()

	if store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		n, err := store.ClearForCredential(ctx, credID)
		if err != nil {
			return cleared, err
		}
		cleared += n
	}
	if cleared > 0 {
		slog.Info("sticky hot-reload: cleared bindings for credential",
			"credential_id", credID,
			"cleared", cleared)
	}
	return cleared, nil
}

// Close gracefully shuts down the StickyCache background goroutines.
// 2026-08-06 FIX (P2-4): Prevent goroutine leak in tests and shutdown scenarios.
// Safe to call multiple times (stopSweep close is idempotent via sync.Once pattern).
func (s *StickyCache) Close() {
	if s == nil {
		return
	}
	select {
	case <-s.stopSweep:
		// Already closed
		return
	default:
		close(s.stopSweep)
		s.sweepDone.Wait()
	}
}

// SetDB installs the pgx pool used for sticky persistence.
//
// 并发修复 2026-07-27：必须在 s.mu 写锁下赋值。main.go 在启动阶段调用
// SetDB，而请求路径上的 RecordSuccess / RecordSuccessMultiLevel /
// RestoreFromDB 会并发读 s.dbPool；无锁写 + 无锁读是 data race，
// 且没有 happens-before 保证读方能看到已初始化的 pool。
func (s *StickyCache) SetDB(pool *pgxpool.Pool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dbPool = pool
}

// SetRedisStore 注入 StickyRedisStore(URSM v2 过渡), nil 时退化为纯内存。
//
// 2026-07-27 并发修复：与 SetDB 同因——RecordSuccessMultiLevel /
// RestoreFromDB 在请求路径上读 s.redisStore，无锁写是数据竞争。
func (s *StickyCache) SetRedisStore(store StickyRedisStore) {
	s.mu.Lock()
	s.redisStore = store
	s.mu.Unlock()
}

// db returns the current pool under the read lock. Callers copy the
// pointer out and then do their I/O on the local — the lock is never
// held across a DB round trip.
func (s *StickyCache) db() *pgxpool.Pool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dbPool
}

// redisStoreSnapshot returns the current redis store under the read lock.
func (s *StickyCache) redisStoreSnapshot() StickyRedisStore {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.redisStore
}

func (s *StickyCache) Get(key string) (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.items[key]
	if !ok || time.Now().After(e.expiresAt) {
		return 0, false
	}
	return e.credentialID, true
}

func (s *StickyCache) GetEntry(key string) (int, int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.items[key]
	if !ok || time.Now().After(e.expiresAt) {
		return 0, 0, false
	}
	return e.credentialID, e.failures, true
}

// 2026-08-26 fix: L2 (client+model) sticky removed from cascade.
//
// Sticky design contract (operator-confirmed):
//   - Same session_id, multiple requests → MUST pin to L1 (session+model) credential
//   - NEW session_id with same (client, model) → MUST go through normal load
//     balancing. If L2 is in the cascade, each successful request re-locks the
//     (client, model) pair to whatever the previous session picked, so a
//     sustained QPS keeps every new session glued to ONE credential forever,
//     killing load balancing.
//
// L3 (client baseline) is kept as a fallback only when both sessionID and
// model are missing (no L1 key derivable).
//
// We still record the L2 entry on RecordSuccess (memory-only, very short
// TTL). It is NOT consulted during lookup; the persisted field is available
// for legacy callers that Stats(metric) want to know "what the current
// (client,model) voted for". Operators may want to re-introduce L2 lookup
// behind a feature gate later — the on-disk shape stays compatible.
func (s *StickyCache) GetMultiLevel(
	tenantID string,
	appID, apiKeyID *int,
	clientProfile string,
	sessionID string,
	model string,
) StickyLookupResult {
	l1, _ /* l2 intentionally unused */, l3 := buildStickyKeys(tenantID, appID, apiKeyID, clientProfile, sessionID, model)

	now := time.Now()

	// 内存级联查找(L1 → L3), 持读锁。
	s.mu.RLock()
	// Try L1: session + model (highest priority)
	if l1 != "" {
		if e, ok := s.items[l1]; ok && now.Before(e.expiresAt) {
			cred := e.credentialID
			s.mu.RUnlock()
			slog.Debug("sticky L1 hit", "key", l1, "credentialID", cred)
			return StickyLookupResult{
				CredentialID: cred,
				Level:        StickyLevelSession,
				Found:        true,
			}
		}
	}

	// Skip L2 per contract above.

	// Try L3: client baseline (lowest priority, fallback only).
	if l3 != "" {
		if e, ok := s.items[l3]; ok && now.Before(e.expiresAt) {
			cred := e.credentialID
			s.mu.RUnlock()
			slog.Debug("sticky L3 hit", "key", l3, "credentialID", cred)
			return StickyLookupResult{
				CredentialID: cred,
				Level:        StickyLevelClient,
				Found:        true,
			}
		}
	}
	s.mu.RUnlock()

	// Redis fallback(URSM v2 过渡): 内存 miss 后回源 Redis, 用显式 level。
	// 2026-08-26 fix: also skip L2 in the Redis cascade — same contract reason.
	store := s.redisStoreSnapshot()
	if store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		type lv struct {
			key string
			lvl int
			ttl time.Duration
		}
		l1TTL := stickyL1DefaultTTL
		if sessionID != "" && model != "" {
			l1TTL = calculateSessionStickyTTL(model)
		}
		// L1 then L3 only — L2 deliberately excluded so new sessions cannot
		// inherit a previous session's pinned credential.
		levels := []lv{{l1, 1, l1TTL}, {l3, 3, stickyL3TTL}}
		for _, k := range levels {
			if k.key == "" {
				continue
			}
			if credID, ok := store.GetLevel(ctx, k.lvl, k.key); ok {
				cancel()
				// 回填内存
				s.Set(k.key, credID, k.ttl)
				// 按实际命中的 level 映射 StickyLevel, 不能硬编码 StickyLevelClient
				var lvl StickyLevel
				switch k.lvl {
				case 1:
					lvl = StickyLevelSession
				default:
					lvl = StickyLevelClient
				}
				return StickyLookupResult{CredentialID: credID, Level: lvl, Found: true}
			}
		}
		cancel()
	}

	slog.Debug("sticky miss", "tenant", tenantID, "session", sessionID, "model", model)
	return StickyLookupResult{Found: false}
}

func (s *StickyCache) Set(key string, credentialID int, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[key] = stickyEntry{
		credentialID: credentialID,
		failures:     0,
		// AUDIT-1: 成功（或新设置）清零连续失败计数器。
		consecutiveFailures: 0,
		lastFailureAt:       time.Time{},
		expiresAt:           time.Now().Add(ttl),
	}
}

// AUDIT-1 (2026-07-12): 重新实现 RecordFailure 的"2 次连续失败 + 10s
// 窗口"重置逻辑。
//
// 用户要求：使用当前的路由节点请求出错，10 秒后再次请求还出错（相当于
// 连续请求出错了 2 次）→ 应该重走路由选择的过程。
//
// 语义：
//  1. 每次失败都检查"上一次失败时间"，若在 10s 窗口内 → 算连续失败；
//     若超过 10s → 重置为"第 1 次失败"。
//  2. 连续失败计数达到 2 → 立即删除 sticky entry，下次请求重新路由。
//  3. 成功调用（RecordSuccess / Set）会自动清零连续计数。
//  4. 进入失败状态时保留 credentialID 信息便于 trace，
//     但已删除 entry 后任何后续访问都视为 miss。
func (s *StickyCache) RecordFailure(key string, threshold int) bool {
	const consecutiveWindow = 10 * time.Second
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.items[key]
	now := time.Now()
	if !ok || now.After(e.expiresAt) {
		delete(s.items, key)
		return true
	}
	// 检查 10s 窗口：上一次失败距今 > 10s 视为不连续，重置为 1
	if e.lastFailureAt.IsZero() || now.Sub(e.lastFailureAt) > consecutiveWindow {
		e.consecutiveFailures = 1
	} else {
		e.consecutiveFailures++
	}
	e.lastFailureAt = now
	if threshold <= 0 {
		threshold = 2 // AUDIT-1: 默认 2 次（用户语义）
	}
	if e.consecutiveFailures >= threshold {
		delete(s.items, key)
		return true
	}
	s.items[key] = e
	return false
}

// RecordFailureMultiLevel records a failure for every sticky level that
// currently points at credentialID. Once any matching level reaches the
// threshold, all matching levels are removed together so a later lookup
// cannot fall back to the same failed credential through L2 or L3.
func (s *StickyCache) RecordFailureMultiLevel(
	tenantID string,
	appID, apiKeyID *int,
	clientProfile string,
	sessionID string,
	model string,
	credentialID int,
	threshold int,
) bool {
	l1, l2, l3 := buildStickyKeys(tenantID, appID, apiKeyID, clientProfile, sessionID, model)
	keys := []string{l1, l2, l3}
	if threshold <= 0 {
		threshold = 2
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	matched := make([]string, 0, len(keys))
	reached := false
	for _, key := range keys {
		if key == "" {
			continue
		}
		e, ok := s.items[key]
		if !ok || now.After(e.expiresAt) || e.credentialID != credentialID {
			continue
		}
		matched = append(matched, key)
		if e.lastFailureAt.IsZero() || now.Sub(e.lastFailureAt) > 10*time.Second {
			e.consecutiveFailures = 1
		} else {
			e.consecutiveFailures++
		}
		e.lastFailureAt = now
		if e.consecutiveFailures >= threshold {
			reached = true
		}
		// AUDIT-2 (2026-07-12): Always write back the updated entry, even
		// when threshold is not reached. The previous code only wrote back
		// entries that didn't reach threshold, but this was inside the loop,
		// so the last matched entry would be written multiple times.
		s.items[key] = e
	}
	if reached {
		for _, key := range matched {
			delete(s.items, key)
		}
	}
	return reached
}

func (s *StickyCache) RecordSuccess(key string, credentialID int, ttl time.Duration) {
	s.Set(key, credentialID, ttl)
	// 并发修复 2026-07-27：通过 s.db() 在读锁下取出 pool 指针，再在锁外
	// 做 I/O。
	if pool := s.db(); pool != nil {
		go s.dbSet(pool, key, credentialID, ttl)
	}
}

// RecordSuccessMultiLevel records success for the levels we want to keep
// sticky. 2026-08-26 fix: L2 (client+model) is intentionally NOT recorded
// anymore — see GetMultiLevel for the rationale. We only record:
//   - L1: session+model (per-session sticky)
//   - L3: client baseline (when no session/model, very coarse fallback)
// Recording L2 caused new sessions of the same (client, model) to inherit
// the previous session's credential because RecordSuccessMultiLevel fires
// on every successful request and refreshes TTL — turning L2 into a
// "forever lock" under sustained traffic.
func (s *StickyCache) RecordSuccessMultiLevel(
	tenantID string,
	appID, apiKeyID *int,
	clientProfile string,
	sessionID string,
	model string,
	credentialID int,
) {
	// 2026-08-26: skip L2 from buildStickyKeys — passed but unused.
	l1, _ /* l2 intentionally unused */, l3 := buildStickyKeys(tenantID, appID, apiKeyID, clientProfile, sessionID, model)

	s.mu.Lock()
	now := time.Now()

	// L1: session + model (动态 TTL，基于模型类型)
	if l1 != "" {
		ttl := calculateSessionStickyTTL(model)
		s.items[l1] = stickyEntry{
			credentialID: credentialID,
			failures:     0,
			// AUDIT-1: 清零连续失败计数。
			consecutiveFailures: 0,
			lastFailureAt:       time.Time{},
			expiresAt:           now.Add(ttl),
		}
		slog.Debug("sticky L1 recorded",
			"key", l1,
			"ttl", ttl,
			"credential_id", credentialID,
		)
	}

	// L2 (client+model) is intentionally NOT written. Per the contract in
	// RecordSuccessMultiLevel above, L2 is excluded so a previous session's
	// successful credential cannot pin a *new* session of the same
	// (client, model).

	// L3: client baseline (24h).
	if l3 != "" {
		s.items[l3] = stickyEntry{
			credentialID:        credentialID,
			failures:            0,
			consecutiveFailures: 0,
			lastFailureAt:       time.Time{},
			expiresAt:           now.Add(stickyL3TTL),
		}
	}
	// 并发修复 2026-07-27：在释放写锁之前把 pool 与 redisStore 指针拷到
	// 局部变量，这样与 SetDB/SetRedisStore 之间有明确的 happens-before；
	// DB/Redis I/O 仍在锁外进行。
	s.mu.Unlock()
	pool := s.db()
	store := s.redisStoreSnapshot()

	// Redis 双写(URSM v2 过渡): 用显式 level, 避免 levelOf 启发式
	// 2026-08-26 fix: only L1 and L3 are dual-written. L2 is excluded.
	if store != nil {
		levels := []struct {
			key string
			lvl int
			ttl time.Duration
		}{
			{l1, 1, calculateSessionStickyTTL(model)},
			{l3, 3, stickyL3TTL},
		}
		for _, lv := range levels {
			if lv.key == "" {
				continue
			}
			// Each level gets its own bounded budget. Sharing one context would
			// let a slow L1 write consume the deadline and silently skip L2/L3.
			ctx, cancel := context.WithTimeout(context.Background(), stickyRedisWriteTimeout)
			if err := store.SetLevel(ctx, lv.lvl, credentialID, lv.key, lv.ttl); err != nil {
				slog.Debug("sticky redis double-write failed", "key", lv.key, "error", err)
			}
			cancel()
		}
	}

	// Async DB write for the retained levels (L1 + L3 only; L2 intentionally
	// excluded — see RecordSuccessMultiLevel).
	if pool != nil {
		go s.dbSetMultiLevel(pool, l1, "", l3, model, credentialID, now)
	}

	slog.Debug("sticky multi-level recorded",
		"credentialID", credentialID,
		"l1", l1,
		"l3", l3,
	)
}

// dbSet takes the pool as a parameter (rather than re-reading s.dbPool)
// so the goroutine uses the exact pool the caller observed under the
// lock. 并发修复 2026-07-27.
func (s *StickyCache) dbSet(pool *pgxpool.Pool, key string, credentialID int, ttl time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	expiresAt := time.Now().UTC().Add(ttl)
	_, err := pool.Exec(ctx, `
		INSERT INTO sticky_sessions (sticky_key, credential_id, set_at, expires_at)
		VALUES ($1, $2, now(), $3)
		ON CONFLICT (sticky_key) DO UPDATE SET
			credential_id = EXCLUDED.credential_id,
			set_at = EXCLUDED.set_at,
			expires_at = EXCLUDED.expires_at
	`, key, credentialID, expiresAt)
	if err != nil {
		slog.Debug("sticky DB write failed", "key", key, "error", err)
	}
}

// dbSetMultiLevel takes the pool as a parameter for the same reason as
// dbSet. 并发修复 2026-07-27.
//
// 2026-08-26 fix: only L1 and L3 are persisted to sticky_sessions. L2 is
// excluded per the RecordSuccessMultiLevel contract.
func (s *StickyCache) dbSetMultiLevel(pool *pgxpool.Pool, l1, l2, l3, model string, credentialID int, baseTime time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 2026-08-26: `_ = l2` — L2 intentionally unused from the persistence
	// path. The argument is kept in the signature for backwards-compat with
	// previous shape; dropping the parameter would force callers in the
	// legacy V2 routing package to be re-touched.
	_ = l2

	l1TTL := stickyL1DefaultTTL
	if model != "" {
		l1TTL = calculateSessionStickyTTL(model)
	}
	keys := []struct {
		key string
		ttl time.Duration
	}{
		{l1, l1TTL},
		// L2 skipped — see header comment.
		{l3, stickyL3TTL},
	}

	for _, k := range keys {
		if k.key == "" {
			continue
		}
		expiresAt := baseTime.UTC().Add(k.ttl)
		_, err := pool.Exec(ctx, `
			INSERT INTO sticky_sessions (sticky_key, credential_id, set_at, expires_at)
			VALUES ($1, $2, now(), $3)
			ON CONFLICT (sticky_key) DO UPDATE SET
				credential_id = EXCLUDED.credential_id,
				set_at = EXCLUDED.set_at,
				expires_at = EXCLUDED.expires_at
		`, k.key, credentialID, expiresAt)
		if err != nil {
			slog.Debug("sticky multi-level DB write failed", "key", k.key, "error", err)
		}
	}
}

func (s *StickyCache) RestoreFromDB(ctx context.Context) error {
	// 并发修复 2026-07-27：读锁下取出 pool，再在锁外查询（下面才重新
	// 拿写锁填充 items）。
	pool := s.db()
	if pool == nil {
		return nil
	}
	rows, err := pool.Query(ctx, `
		SELECT sticky_key, credential_id, expires_at
		FROM sticky_sessions
		WHERE expires_at > now()
	`)
	if err != nil {
		return err
	}
	defer rows.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	loaded := 0
	for rows.Next() {
		var key string
		var credID int
		var expiresAt time.Time
		if err := rows.Scan(&key, &credID, &expiresAt); err != nil {
			continue
		}
		s.items[key] = stickyEntry{
			credentialID: credID,
			failures:     0,
			expiresAt:    expiresAt.Local(),
		}
		loaded++
	}
	if loaded > 0 {
		slog.Info("sticky cache restored from DB", "entries", loaded)
	}
	return rows.Err()
}

func (s *StickyCache) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, key)
}

// DeleteMultiLevel removes all sticky levels for a given session context.
// Used when a credential becomes permanently unavailable (e.g., auth failure).
// AUDIT-2 (2026-07-12): added to support credential-fatal error cleanup.
func (s *StickyCache) DeleteMultiLevel(
	tenantID string,
	appID, apiKeyID *int,
	clientProfile string,
	sessionID string,
	model string,
	credentialID int,
) {
	l1, l2, l3 := buildStickyKeys(tenantID, appID, apiKeyID, clientProfile, sessionID, model)
	keys := []struct {
		key   string
		level int
	}{
		{l1, 1}, {l2, 2}, {l3, 3},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	for _, item := range keys {
		if item.key == "" {
			continue
		}
		s.mu.Lock()
		entry, ok := s.items[item.key]
		matched := ok && entry.credentialID == credentialID
		if matched {
			delete(s.items, item.key)
		}
		s.mu.Unlock()
		store := s.redisStoreSnapshot()
		if store != nil {
			if err := store.DeleteLevelIfCredential(ctx, item.level, item.key, credentialID); err != nil {
				slog.Debug("sticky redis delete failed", "key", item.key, "error", err)
			}
		}
	}
}

func (s *StickyCache) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.items)
}

// buildStickyKeys builds all three levels of sticky keys.
// Returns (L1, L2, L3). L1 and L2 may be empty if session_id or model are not provided.
//
// 2026-06-25: Internal helper for multi-level sticky streaming.
func buildStickyKeys(
	tenantID string,
	appID, apiKeyID *int,
	clientProfile string,
	sessionID string,
	model string,
) (l1, l2, l3 string) {
	profile := strings.TrimSpace(strings.ToLower(clientProfile))
	if profile == "" {
		profile = "default"
	}
	model = strings.TrimSpace(strings.ToLower(model))
	sessionID = strings.TrimSpace(sessionID)

	var app, key int
	if appID != nil {
		app = *appID
	}
	if apiKeyID != nil {
		key = *apiKeyID
	}

	// L3: client baseline (always present)
	l3 = fmt.Sprintf("%s:%d:%d:%s", tenantID, app, key, profile)

	// L2: client + model (only if model is specified)
	if model != "" {
		l2 = fmt.Sprintf("%s:%d:%d:%s:%s", tenantID, app, key, profile, model)
	}

	// L1: session + model (only if both session and model are specified)
	if sessionID != "" && model != "" {
		l1 = fmt.Sprintf("%s:%d:%d:%s:%s:%s", tenantID, app, key, profile, sessionID, model)
	}

	return l1, l2, l3
}

// BuildClientStickyKey builds a stable client-scoped sticky key (L3 baseline).
//
// 2026-06-25: This is now the L3 (lowest priority) key. For routing decisions,
// use GetMultiLevel instead, which cascades through L1 (session+model) →
// L2 (client+model) → L3 (client).
//
// Format: {tenant}:{app}:{key}:{profile}
//
// The key describes the CLIENT (not "client + model"). Model-specific
// routing happens via GetMultiLevel's L1/L2 levels; this L3 baseline
// is only used as a fallback when no model-specific binding exists.
func BuildClientStickyKey(tenantID string, appID, apiKeyID *int, clientProfile string) string {
	_, _, l3 := buildStickyKeys(tenantID, appID, apiKeyID, clientProfile, "", "")
	return l3
}
