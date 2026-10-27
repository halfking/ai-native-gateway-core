package taskbus

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MemoryStore 是进程内 Store，供测试与本波示范链（无 DB 切表）。
// 同 idempotency_key 二次入队返回已有任务（at-least-once 去重）。
type MemoryStore struct {
	mu      sync.Mutex
	nextID  int64
	byID    map[int64]*Task
	byKey   map[string]int64
	claimed map[int64]bool
}

// NewMemoryStore 创建空内存库。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		byID:    make(map[int64]*Task),
		byKey:   make(map[string]int64),
		claimed: make(map[int64]bool),
	}
}

// Enqueue 实现 Store。
func (s *MemoryStore) Enqueue(_ context.Context, taskType, idempotencyKey string, schemaVersion int, payload []byte) (Task, error) {
	if taskType == "" || idempotencyKey == "" {
		return Task{}, fmt.Errorf("taskbus.MemoryStore.Enqueue: taskType and idempotencyKey required")
	}
	if schemaVersion <= 0 {
		return Task{}, fmt.Errorf("taskbus.MemoryStore.Enqueue: schemaVersion must be > 0")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.byKey[idempotencyKey]; ok {
		return *s.byID[id], nil
	}
	s.nextID++
	t := &Task{
		ID:             s.nextID,
		TaskType:       taskType,
		Payload:        append([]byte(nil), payload...),
		PayloadVersion: schemaVersion,
		IdempotencyKey: idempotencyKey,
		Status:         StatusPending,
		CreatedAt:      time.Now().UTC(),
	}
	s.byID[t.ID] = t
	s.byKey[idempotencyKey] = t.ID
	return *t, nil
}

// ClaimBatch 取出可投递任务（pending/failed 且 next_retry 到期）。
func (s *MemoryStore) ClaimBatch(_ context.Context, limit int, now time.Time) ([]Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 32
	}
	out := make([]Task, 0, limit)
	for _, t := range s.byID {
		if s.claimed[t.ID] {
			continue
		}
		if t.Status != StatusPending && t.Status != StatusFailed {
			continue
		}
		if !t.NextRetryAt.IsZero() && t.NextRetryAt.After(now) {
			continue
		}
		s.claimed[t.ID] = true
		cp := *t
		cp.Payload = append([]byte(nil), t.Payload...)
		out = append(out, cp)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// MarkSent 标记成功。
func (s *MemoryStore) MarkSent(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.byID[id]
	if !ok {
		return fmt.Errorf("taskbus.MemoryStore.MarkSent: id %d not found", id)
	}
	t.Status = StatusSent
	delete(s.claimed, id)
	return nil
}

// MarkFailed 标记失败并安排退避。
func (s *MemoryStore) MarkFailed(_ context.Context, id int64, attempts int, nextRetry time.Time, lastErr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.byID[id]
	if !ok {
		return fmt.Errorf("taskbus.MemoryStore.MarkFailed: id %d not found", id)
	}
	t.Status = StatusFailed
	t.Attempts = attempts
	t.NextRetryAt = nextRetry
	t.LastError = lastErr
	delete(s.claimed, id)
	return nil
}

// MarkDLQ 进入死信。
func (s *MemoryStore) MarkDLQ(_ context.Context, id int64, lastErr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.byID[id]
	if !ok {
		return fmt.Errorf("taskbus.MemoryStore.MarkDLQ: id %d not found", id)
	}
	t.Status = StatusDLQ
	t.LastError = lastErr
	delete(s.claimed, id)
	return nil
}

// CountByStatus 计数（taskType 空 = 全类型）。
func (s *MemoryStore) CountByStatus(_ context.Context, taskType, status string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, t := range s.byID {
		if taskType != "" && t.TaskType != taskType {
			continue
		}
		if t.Status == status {
			n++
		}
	}
	return n, nil
}
