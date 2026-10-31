package executors

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/ratelimit"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// stickyTTLConfig is the wired source of TTL truth (P1-B-①, audit
// 2026-10-28 §4): settings.StickyTTLConfig existed with zero consumers while
// this file hardcoded byte-identical values — the LiteLLM #28427 drift shape
// (config says one thing, code does another; future edits to the settings
// file silently no-op). Now every TTL site reads the config; the values
// stay identical to the previous hardcodes so behavior is unchanged.
//
// Cached once (the config has no live-reload path today); tests may swap it
// via setStickyTTLConfigForTest.
var stickyTTLConfig = settings.DefaultStickyTTLConfig()

// setStickyTTLConfigForTest swaps the sticky TTL config (test seam).
func setStickyTTLConfigForTest(cfg settings.StickyTTLConfig) { stickyTTLConfig = cfg }

// calculateSessionStickyTTL 根据模型类型计算动态 TTL（Phase 1）
// P1-B-①：数值来源从硬编码改为 settings.StickyTTLConfig（逐字节同值接线）。
func calculateSessionStickyTTL(model string) time.Duration {
	modelLower := strings.ToLower(model)

	// embedding 模型：短期任务
	if strings.Contains(modelLower, "embedding") || strings.Contains(modelLower, "embed") {
		return stickyTTLConfig.EmbeddingTTL
	}

	// completion 模型：长文本生成（需要在 chat 之前检查，因为有些模型名包含两者）
	if strings.Contains(modelLower, "completion") || strings.Contains(modelLower, "instruct") ||
		strings.Contains(modelLower, "davinci") {
		return stickyTTLConfig.CompletionTTL
	}

	// chat 模型：对话上下文
	if strings.Contains(modelLower, "chat") || strings.Contains(modelLower, "gpt") ||
		strings.Contains(modelLower, "claude") || strings.Contains(modelLower, "gemini") {
		return stickyTTLConfig.ChatTTL
	}

	// 默认
	return stickyTTLConfig.DefaultL1TTL
}

// stickyL3TTL is the client-baseline (L3) TTL — P1-B-① wiring of
// ClientBaselineTTL (was the hardcoded 24h at four call sites).
func stickyL3TTL() time.Duration { return stickyTTLConfig.ClientBaselineTTL }

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
	// TTL: 24 hours (long-term model preference)
	StickyLevelClientModel StickyLevel = 2

	// StickyLevelClient: lowest priority, client-only scoped.
	// Format: {tenant}:{app}:{key}:{profile}
	// TTL: 7 days (baseline fallback)
	StickyLevelClient StickyLevel = 3
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

// ClearForCredential removes every sticky binding associated with the
// given credential. Returns the number of cleared in-memory bindings.
//
// 2026-08-27: restored from deployed build 298201fe0. d2cbaf88b dropped
// this method, breaking admin.SetHotReloadDeps (PATCH binding/credential
// handlers relied on it to invalidate stale sticky pins without waiting
// for the 30s candCache TTL).
//
// Adapted to HEAD's StickyRedisStore interface, which has no per-credential
// scan API: the in-memory map is the authoritative read path, the Redis
// double-write is best-effort backup. Clearing the in-memory map is
// sufficient to drop the stale pin from the next routing cycle; the Redis
// records will fall out naturally via their per-entry TTL on the
// candCache sweep. Returning the in-memory count matches the
// admin.StickyCacheClearer interface contract (cleared, err).
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
	s.mu.Unlock()

	// P1-B-② (audit 2026-10-28 §4): the in-memory + (best-effort) Redis clear
	// above left the durable rows behind — RestoreFromDB on the next process
	// restart resurrected exactly the bindings this call removed (the L3 24h
	// window made a fatal credential routable again via sticky). Delete the
	// DB face too; one keyed statement, same best-effort contract.
	if pool := s.db(); pool != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if _, err := pool.Exec(ctx, `DELETE FROM sticky_sessions WHERE credential_id = $1`, credID); err != nil {
			slog.Debug("sticky db clear-for-credential failed", "credential_id", credID, "error", err)
		}
		cancel()
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
//
// Typed-nil 归一化（2026-09-17，252 无 Redis 部署实抓的每请求 500 crash）：
// 调用方持 `var stickyStore *ursmcache.StickyStore` 且 Redis 不可用时传入
// typed-nil 指针，`store != nil` 接口判空失效，GetLevel 在 nil receiver 上
// panic。这里归一化为非 typed nil，快照守卫恢复判空语义。
func (s *StickyCache) SetRedisStore(store StickyRedisStore) {
	if store != nil {
		if v := reflect.ValueOf(store); v.Kind() == reflect.Pointer && v.IsNil() {
			store = nil
		}
	}
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

// GetL1BindingAge (2026-10-31 C1 sticky 缓存经济学) 返回 L1 绑定的
// (credentialID, 已粘时长, found)。年龄从 expiresAt 反推：
// entry 写入时 expiresAt = now + calculateSessionStickyTTL(model)，
// 故 elapsed = ttl(model) − (expiresAt − now)。entry 属别的凭据 / 不在
// L1 / 已过期 ⇒ found=false（调用方 fail-open）。只读内存面——
// Redis 镜像没有 setAt（SetLevel 只存 credID），跨实例下年龄不可得，
// 与 C1 的 fail-open 矩阵一致（查不到 ⇒ 续期）。
func (s *StickyCache) GetL1BindingAge(l1Key, model string, credID int) (time.Duration, bool) {
	if s == nil || l1Key == "" {
		return 0, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.items[l1Key]
	if !ok || time.Now().After(e.expiresAt) || e.credentialID != credID {
		return 0, false
	}
	elapsed := calculateSessionStickyTTL(model) - time.Until(e.expiresAt)
	if elapsed < 0 {
		elapsed = 0
	}
	return elapsed, true
}

// GetMultiLevel looks up a sticky credential for the request identity.
//
// A complete session identity (sessionID + model) is intentionally scoped to
// L1 only. Falling through to L2 or L3 in that case makes a new session inherit
// the credential selected by an older session and bypasses load balancing.
// L3 remains a compatibility fallback only when the request cannot construct
// a complete session/model key.
func (s *StickyCache) GetMultiLevel(
	tenantID string,
	appID, apiKeyID *int,
	clientProfile string,
	sessionID string,
	model string,
) StickyLookupResult {
	l1, _, l3 := buildStickyKeys(tenantID, appID, apiKeyID, clientProfile, sessionID, model)
	hasSessionModel := strings.TrimSpace(sessionID) != "" && strings.TrimSpace(model) != ""

	now := time.Now()

	s.mu.RLock()
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

	if !hasSessionModel && l3 != "" {
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

	store := s.redisStoreSnapshot()
	if store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		type lv struct {
			key string
			lvl int
			ttl time.Duration
		}
		levels := []lv{{l1, int(StickyLevelSession), calculateSessionStickyTTL(model)}}
		if !hasSessionModel {
			levels = append(levels, lv{l3, int(StickyLevelClient), stickyL3TTL()})
		}
		for _, k := range levels {
			if k.key == "" {
				continue
			}
			if credID, ok := store.GetLevel(ctx, k.lvl, k.key); ok {
				cancel()
				s.Set(k.key, credID, k.ttl)
				lvl := StickyLevelClient
				if k.lvl == int(StickyLevelSession) {
					lvl = StickyLevelSession
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
		// AUDIT-1: 默认 2 次（用户语义）；Wave 2 集中化为 settings 热更键。
		threshold = settings.StickyFailureThreshold()
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
		threshold = settings.StickyFailureThreshold()
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

// RecordSuccessMultiLevel records the sticky levels allowed by the request
// identity. A complete session/model identity persists only L1, so a later
// session with the same client and model re-enters normal load balancing.
// L3 is retained only for requests that cannot construct an L1 key.
func (s *StickyCache) RecordSuccessMultiLevel(
	tenantID string,
	appID, apiKeyID *int,
	clientProfile string,
	sessionID string,
	model string,
	credentialID int,
) {
	l1, _, l3 := buildStickyKeys(tenantID, appID, apiKeyID, clientProfile, sessionID, model)
	hasSessionModel := strings.TrimSpace(sessionID) != "" && strings.TrimSpace(model) != ""
	if hasSessionModel {
		l3 = ""
	}

	s.mu.Lock()
	now := time.Now()

	if l1 != "" {
		ttl := calculateSessionStickyTTL(model)
		s.items[l1] = stickyEntry{
			credentialID:        credentialID,
			failures:            0,
			consecutiveFailures: 0,
			lastFailureAt:       time.Time{},
			expiresAt:           now.Add(ttl),
		}
		slog.Debug("sticky L1 recorded", "key", l1, "ttl", ttl, "credential_id", credentialID)
	}

	if l3 != "" {
		s.items[l3] = stickyEntry{
			credentialID:        credentialID,
			failures:            0,
			consecutiveFailures: 0,
			lastFailureAt:       time.Time{},
			expiresAt:           now.Add(stickyL3TTL()),
		}
	}
	s.mu.Unlock()

	pool := s.db()
	store := s.redisStoreSnapshot()

	if store != nil {
		levels := []struct {
			key string
			lvl int
			ttl time.Duration
		}{
			{l1, int(StickyLevelSession), calculateSessionStickyTTL(model)},
		}
		if l3 != "" {
			levels = append(levels, struct {
				key string
				lvl int
				ttl time.Duration
			}{l3, int(StickyLevelClient), stickyL3TTL()})
		}
		for _, lv := range levels {
			if lv.key == "" {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), stickyRedisWriteTimeout)
			if err := store.SetLevel(ctx, lv.lvl, credentialID, lv.key, lv.ttl); err != nil {
				slog.Debug("sticky redis double-write failed", "key", lv.key, "error", err)
			}
			cancel()
		}
	}

	if pool != nil {
		go s.dbSetMultiLevel(pool, l1, l3, model, credentialID, now)
	}

	slog.Debug("sticky multi-level recorded", "credentialID", credentialID, "l1", l1, "l3", l3)
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
func (s *StickyCache) dbSetMultiLevel(pool *pgxpool.Pool, l1, l3, model string, credentialID int, baseTime time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	l1TTL := stickyTTLConfig.DefaultL1TTL
	if model != "" {
		l1TTL = calculateSessionStickyTTL(model)
	}
	keys := []struct {
		key string
		ttl time.Duration
	}{
		{l1, l1TTL},
		{l3, stickyL3TTL()},
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
	// P1-B-② (audit 2026-10-28 §4): delete must reach the DB too — a
	// memory+Redis-only delete is undone by RestoreFromDB on the next process
	// restart (a fatal credential's binding resurrects within the remaining
	// L3 24h window). Same-owner guard as the in-memory branch: only rows
	// still pointing at this credential are removed, so a binding that has
	// already moved to a healthier credential is never deleted here.
	s.dbDeleteIfCredential(ctx, credentialID, l1, l2, l3)
}

// dbDeleteIfCredential deletes sticky_sessions rows owned by credentialID
// for the given keys (P1-B-②). Best-effort like every other sticky DB write
// (slog.Debug) — the durable row's remaining lifetime is bounded by its own
// expires_at, so a failed delete degrades to the pre-fix behavior, never to
// data loss.
func (s *StickyCache) dbDeleteIfCredential(ctx context.Context, credentialID int, keys ...string) {
	pool := s.db()
	if pool == nil {
		return
	}
	for _, key := range keys {
		if key == "" {
			continue
		}
		if _, err := pool.Exec(ctx, `
			DELETE FROM sticky_sessions
			WHERE sticky_key = $1 AND credential_id = $2
		`, key, credentialID); err != nil {
			slog.Debug("sticky db delete failed", "key", key, "error", err)
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
