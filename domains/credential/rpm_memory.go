package credential

import (
	"context"
	"fmt"
	"hash/fnv"
	"sync"
	"time"
)

const rpmWindowSeconds = 60.0

// rpmMemShardCount 是 MemoryRPMLimiter 的分片数 (2 的幂)。每个分片独立 mutex,
// 使不同 credential 的 RPM 预约不互相争用 —— 旧实现用单一全局 mutex,
// 在 AcquireAll 热路径上串行化所有 credential。
const rpmMemShardCount = 16

// RPMLimiter reserves a request slot in a per-credential RPM window.
type RPMLimiter interface {
	CheckAndReserve(ctx context.Context, providerID, credentialID int, limit int) (bool, int, error)
}

type rpmMemShard struct {
	mu       sync.Mutex
	credsRPM map[string]*rpmWindow
}

// MemoryRPMLimiter is the process-local fallback RPM implementation.
type MemoryRPMLimiter struct {
	shards [rpmMemShardCount]*rpmMemShard
}

// NewMemoryRPMLimiter creates a process-local RPM limiter.
func NewMemoryRPMLimiter() *MemoryRPMLimiter {
	m := &MemoryRPMLimiter{}
	for i := range m.shards {
		m.shards[i] = &rpmMemShard{credsRPM: make(map[string]*rpmWindow)}
	}
	return m
}

// shard 返回 key 对应的分片。
func (m *MemoryRPMLimiter) shard(key string) *rpmMemShard {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return m.shards[h.Sum32()&(rpmMemShardCount-1)]
}

// CheckAndReserve checks and records one request in the sliding window.
func (m *MemoryRPMLimiter) CheckAndReserve(ctx context.Context, providerID, credentialID int, limit int) (bool, int, error) {
	if err := ctx.Err(); err != nil {
		return false, 0, nil
	}
	if limit <= 0 {
		return true, 0, nil
	}

	key := fmt.Sprintf("%d/%d", providerID, credentialID)
	now := float64(time.Now().UnixMilli()) / 1000.0
	cutoff := now - rpmWindowSeconds

	s := m.shard(key)
	s.mu.Lock()
	defer s.mu.Unlock()

	w, ok := s.credsRPM[key]
	if !ok {
		w = &rpmWindow{}
		s.credsRPM[key] = w
	}
	filtered := w.timestamps[:0]
	for _, timestamp := range w.timestamps {
		if timestamp > cutoff {
			filtered = append(filtered, timestamp)
		}
	}
	w.timestamps = filtered
	if len(w.timestamps) >= limit {
		return false, len(w.timestamps), nil
	}
	w.timestamps = append(w.timestamps, now)
	return true, len(w.timestamps), nil
}
