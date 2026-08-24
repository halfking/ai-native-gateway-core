package ratelimit

import (
	"context"
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

type RPMWaitingNotifier interface {
	AdmitRPMWithWait(ctx context.Context, keyID, limit int, notify func(AdmissionResult)) (AdmissionResult, error)
}

// rpmShardCount 是 SlidingWindowLimiter 的分片数。每个分片有自己的 mutex,
// 使不同 keyID 的 RPM/TPM 检查不互相争用。取 2 的幂使取模变成位与。
// 16 在常见多核机器上提供足够的并行度,内存开销也可忽略。
const rpmShardCount = 16

type rpmShard struct {
	mu        sync.Mutex
	windows   map[int]*rpmWindow
	tokenWins map[int]*tpmWindow
}

// SlidingWindowLimiter 是进程内的 RPM/TPM 滑窗限流器。
//
// 并发模型:按 keyID % rpmShardCount 分片到 16 个独立 shard,每个 shard
// 有自己的 mutex。旧实现用单一全局 mutex,所有 key 的 CheckRPM/CheckTPM
// 都串行化 —— 在请求热路径上成为瓶颈。分片后不同 key 并行,同一 key 仍互斥
// (语义不变)。
type SlidingWindowLimiter struct {
	shards    [rpmShardCount]*rpmShard
	admission *MinuteBucketAdmission
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
	l := &SlidingWindowLimiter{admission: NewMinuteBucketAdmission()}
	for i := range l.shards {
		l.shards[i] = &rpmShard{
			windows:   make(map[int]*rpmWindow),
			tokenWins: make(map[int]*tpmWindow),
		}
	}
	return l
}

func (l *SlidingWindowLimiter) AdmitRPM(ctx context.Context, keyID, limit int) (AdmissionResult, error) {
	return l.admission.AdmitRPM(ctx, keyID, limit)
}

func (l *SlidingWindowLimiter) AdmitRPMWithWait(ctx context.Context, keyID, limit int, notify func(AdmissionResult)) (AdmissionResult, error) {
	return l.admission.admit(ctx, keyID, limit, notify)
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
		w = &rpmWindow{}
		s.windows[keyID] = w
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
		w = &tpmWindow{}
		s.tokenWins[keyID] = w
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

func (l *SlidingWindowLimiter) Stop() {
	// 并发清空所有分片。各分片独立加锁,避免单一全局锁。
	for _, s := range l.shards {
		s.mu.Lock()
		s.windows = make(map[int]*rpmWindow)
		s.tokenWins = make(map[int]*tpmWindow)
		s.mu.Unlock()
	}
}
