package ratelimit

import (
	"container/list"
	"context"
	"errors"
	"sync"
	"time"
)

// RPMLimiter is the minimal interface used by relay/handler.go.
// Both SlidingWindowLimiter and RedisLimiter satisfy it.
type RPMLimiter interface {
	CheckRPM(keyID int, limit int) bool
	RPMStatus(keyID int, limit int) (used int, remaining int)
}

type RPMAdmission interface {
	AdmitRPM(ctx context.Context, keyID, limit int) (AdmissionResult, error)
}

// TPMChecker 是 token 预算预检的可选能力（SlidingWindowLimiter /
// RedisLimiter 均已实现）。2026-11-15（限流统一按 key 轮）前零调用方——
// api_keys.rate_limit_tpm 是死字段；现由 streaming.checkGatewayRateLimit
// 的 TPM 第二道闸消费。
type TPMChecker interface {
	CheckTPM(keyID int, estimatedTokens int, limit int) bool
}

type RPMWaitingNotifier interface {
	AdmitRPMWithWait(ctx context.Context, keyID, limit int, notify func(AdmissionResult)) (AdmissionResult, error)
}

// ErrQueueBudgetExceeded is returned by AdmitRPMWithBudget when the estimated
// queue wait exceeds the caller's remaining request budget. The request is
// reject-fast (handler maps it to 429 + Retry-After) instead of burning the
// whole upstream timeout in the queue and dying as a "context canceled" 502
// with no log row (2026-08-26 kimi-k3 incident on 245).
var ErrQueueBudgetExceeded = errors.New("rate limit queue wait exceeds request budget")

// RPMBudgetedAdmission extends the waiting admission with a hard wait cap:
// maxWait
//
//	<= 0   → unlimited (legacy behaviour)
//	> 0    → reject fast (ErrQueueBudgetExceeded) when the estimated queue
//	         wait exceeds maxWait. The AdmissionResult still carries
//	         Position/Limit and EstimatedWaitSec for the Retry-After header.
type RPMBudgetedAdmission interface {
	AdmitRPMWithBudget(ctx context.Context, keyID, limit int, maxWait time.Duration, notify func(AdmissionResult)) (AdmissionResult, error)
}

// EstimatedWaitSec is the handler-visible estimate of how long a queued
// request would wait before admission (seconds). Populated on the
// ErrQueueBudgetExceeded path so callers can set Retry-After accurately.

// rpmShardCount 是 SlidingWindowLimiter 的分片数。每个分片有自己的 mutex,
// 使不同 keyID 的 RPM/TPM 检查不互相争用。取 2 的幂使取模变成位与。
// 16 在常见多核机器上提供足够的并行度,内存开销也可忽略。
const rpmShardCount = 16

type rpmShard struct {
	mu        sync.Mutex
	windows   map[int]*rpmWindow
	tokenWins map[int]*tpmWindow
	
	// P3-10: LRU tracking for keyID bucket eviction
	// When total entries exceed maxEntries, least-recently-used buckets are removed
	lruList    *list.List      // list.Element.Value is keyID (int)
	lruKeyMap  map[int]*list.Element  // keyID -> list.Element
	maxEntries int      // max total entries in this shard (0 = unlimited)
}

// SlidingWindowLimiter 是进程内的 RPM/TPM 滑窗限流器。
//
// 并发模型:按 keyID % rpmShardCount 分片到 16 个独立 shard,每个 shard
// 有自己的 mutex。旧实现用单一全局 mutex,所有 key 的 CheckRPM/CheckTPM
// 都串行化 —— 在请求热路径上成为瓶颈。分片后不同 key 并行,同一 key 仍互斥
// (语义不变)。
//
// P3-10: LRU eviction added to prevent unbounded memory growth.
// Buckets older than 60 seconds are automatically purged; when total
// entries exceed maxEntriesPerShard, least-recently-used buckets are evicted.
type SlidingWindowLimiter struct {
	shards        [rpmShardCount]*rpmShard
	admission     *MinuteBucketAdmission
	maxEntriesPerShard   int  // max entries per shard (0 = unlimited, default recommended: 10000)
}

type rpmWindow struct {
	timestamps []float64
}

type tpmWindow struct {
	entries []tokenEntry
}

type tokenEntry struct {
	ts     float64
	tokens int
}

func NewSlidingWindowLimiter() *SlidingWindowLimiter {
	l := &SlidingWindowLimiter{
		admission:   NewMinuteBucketAdmission(),
		maxEntriesPerShard: 10000,  // P3-10: default max entries per shard
	}
	for i := range l.shards {
		l.shards[i] = &rpmShard{
			windows:    make(map[int]*rpmWindow),
			tokenWins:  make(map[int]*tpmWindow),
			lruList:    list.New(),
			lruKeyMap:  make(map[int]*list.Element),
			maxEntries: l.maxEntriesPerShard,
		}
	}
	return l
}

// NewSlidingWindowLimiterWithMaxEntries creates a limiter with custom max entries per shard (P3-10).
// maxEntries <= 0 disables LRU eviction (unlimited memory).
// Recommended: 10000 for production (allows ~160k API keys per 16 shards with some headroom).
func NewSlidingWindowLimiterWithMaxEntries(maxEntriesPerShard int) *SlidingWindowLimiter {
	l := &SlidingWindowLimiter{
		admission:   NewMinuteBucketAdmission(),
		maxEntriesPerShard: maxEntriesPerShard,
	}
	for i := range l.shards {
		l.shards[i] = &rpmShard{
			windows:    make(map[int]*rpmWindow),
			tokenWins:  make(map[int]*tpmWindow),
			lruList:    list.New(),
			lruKeyMap:  make(map[int]*list.Element),
			maxEntries: maxEntriesPerShard,
		}
	}
	return l
}

func (l *SlidingWindowLimiter) AdmitRPM(ctx context.Context, keyID, limit int) (AdmissionResult, error) {
	return l.admission.AdmitRPM(ctx, keyID, limit)
}

func (l *SlidingWindowLimiter) AdmitRPMWithWait(ctx context.Context, keyID, limit int, notify func(AdmissionResult)) (AdmissionResult, error) {
	return l.admission.AdmitRPMWithBudget(ctx, keyID, limit, 0, notify)
}

// AdmitRPMWithBudget passes the queue-wait cap through to the underlying
// minute-bucket admission (see RPMBudgetedAdmission).
func (l *SlidingWindowLimiter) AdmitRPMWithBudget(ctx context.Context, keyID, limit int, maxWait time.Duration, notify func(AdmissionResult)) (AdmissionResult, error) {
	return l.admission.AdmitRPMWithBudget(ctx, keyID, limit, maxWait, notify)
}

// shard 返回 keyID 对应的分片。keyID 可能为负 (hash),用位与取非负低 bits。
func (l *SlidingWindowLimiter) shard(keyID int) *rpmShard {
	// int -> uintptr 再位与,避免负数取模的分支。
	return l.shards[uint(uintptr(keyID))&(rpmShardCount-1)]
}

func (l *SlidingWindowLimiter) CheckRPM(keyID int, limit int) bool {
	if limit <= 0 {
		return true
	}
	s := l.shard(keyID)
	s.mu.Lock()
	defer s.mu.Unlock()

	now := float64(time.Now().UnixMilli()) / 1000.0
	cutoff := now - 60.0

	w, ok := s.windows[keyID]
	if !ok {
		// P3-10: Create new window and add to LRU
		w = &rpmWindow{}
		s.windows[keyID] = w
		s.updateLRU(keyID)
	} else {
		// P3-10: Update LRU on access
		s.updateLRU(keyID)
	}

	if len(w.timestamps) > 0 {
		filtered := w.timestamps[:0]
		for _, t := range w.timestamps {
			if t > cutoff {
				filtered = append(filtered, t)
			}
		}
		w.timestamps = filtered
	}

	if len(w.timestamps) >= limit {
		return false
	}

	w.timestamps = append(w.timestamps, now)
	return true
}

func (l *SlidingWindowLimiter) CheckTPM(keyID int, estimatedTokens int, limit int) bool {
	if limit <= 0 {
		return true
	}
	s := l.shard(keyID)
	s.mu.Lock()
	defer s.mu.Unlock()

	now := float64(time.Now().UnixMilli()) / 1000.0
	cutoff := now - 60.0

	w, ok := s.tokenWins[keyID]
	if !ok {
		// P3-10: Create new window and add to LRU
		w = &tpmWindow{}
		s.tokenWins[keyID] = w
		s.updateLRU(keyID)
	} else {
		// P3-10: Update LRU on access
		s.updateLRU(keyID)
	}

	if len(w.entries) > 0 {
		filtered := w.entries[:0]
		for _, e := range w.entries {
			if e.ts > cutoff {
				filtered = append(filtered, e)
			}
		}
		w.entries = filtered
	}

	currentTotal := 0
	for _, e := range w.entries {
		currentTotal += e.tokens
	}

	if currentTotal+estimatedTokens > limit {
		return false
	}

	w.entries = append(w.entries, tokenEntry{ts: now, tokens: estimatedTokens})
	return true
}

func (l *SlidingWindowLimiter) RPMStatus(keyID int, limit int) (used int, remaining int) {
	if limit <= 0 {
		return 0, -1
	}
	s := l.shard(keyID)
	s.mu.Lock()
	defer s.mu.Unlock()

	now := float64(time.Now().UnixMilli()) / 1000.0
	cutoff := now - 60.0

	w, ok := s.windows[keyID]
	if !ok {
		return 0, limit
	}

	count := 0
	for _, t := range w.timestamps {
		if t > cutoff {
			count++
		}
	}

	rem := limit - count
	if rem < 0 {
		rem = 0
	}
	return count, rem
}

// updateLRU updates the LRU position of a keyID and evicts LRU buckets if needed (P3-10).
// Must be called under shard lock.
func (s *rpmShard) updateLRU(keyID int) {
	// Move to end (most recently used)
	if elem, exists := s.lruKeyMap[keyID]; exists {
		s.lruList.MoveToBack(elem)
	} else {
		// Add new entry
		elem := s.lruList.PushBack(keyID)
		s.lruKeyMap[keyID] = elem
	}

	// Evict LRU entries if over limit
	if s.maxEntries > 0 {
		totalEntries := len(s.windows) + len(s.tokenWins)
		for totalEntries > s.maxEntries && s.lruList.Len() > 0 {
			// Remove least recently used (front of list)
			elem := s.lruList.Front()
			lruKeyID := elem.Value.(int)
			s.lruList.Remove(elem)
			delete(s.lruKeyMap, lruKeyID)

			// Remove both windows for this keyID
			delete(s.windows, lruKeyID)
			delete(s.tokenWins, lruKeyID)

			totalEntries = len(s.windows) + len(s.tokenWins)
		}
	}
}

func (l *SlidingWindowLimiter) Stop() {
	// 并发清空所有分片。各分片独立加锁,避免单一全局锁。
	for _, s := range l.shards {
		s.mu.Lock()
		s.windows = make(map[int]*rpmWindow)
		s.tokenWins = make(map[int]*tpmWindow)
		s.lruList = list.New()
		s.lruKeyMap = make(map[int]*list.Element)
		s.mu.Unlock()
	}
}
