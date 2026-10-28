package taskbus

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MemoryStore 是进程内 Store，供测试与本波示范链（无 DB 切表）。
// 同 idempotency_key 二次入队返回已有任务（at-least-once 去重）。
//
// ★ r1041 轮（2026-10-28）：**回收与索引**。
//
// ★ 起因：独立审计实测 ——
//
//	enqueued=200000 sent=200000 rows_still_retained=200000
//	单次 ClaimBatch 扫描 6.1 ms，而 dispatcher 每 100 ms 轮询一次
//	保留载荷 781 MiB
//
// ★ 两个独立缺陷：
//
//	① **回收缺失**：旧实现 `MarkSent` 只改 Status、不删条目 ⇒ byID/byKey/claimed
//	   三个 map 随流量**单调增长** ⇒ 生产挂载点 `cmd/gateway/main_pipeline.go`
//	   的 `cache_save` 任务载荷是**完整上游响应体** ⇒ 内存无界增长。
//	② **扫描成本**：旧实现 `ClaimBatch` 每轮**全表扫** byID ⇒ 已完成任务仍在表里
//	   ⇒ 每次轮询成本随累计流量线性劣化 ⇒ 100ms 轮询 = 每秒 10 次全扫。
//
// ★ 修法（三处，缺一不可）：
//
//	(a) **未完成索引** `pending map[int64]bool` —— ClaimBatch 只遍历它，
//	    不再全表扫 byID ⇒ 已完成任务**结构上**不在遍历集合里。
//	(b) **保留窗口** `Retention` —— 完成后仍保留一段时间（供排障/幂等去重），
//	    默认 10 分钟；窗口内可查、窗口后被回收。
//	(c) **回收器** `sweep(now)` —— 删掉超窗的已完成条目（含 byKey 与 claimed）。
//
// ★⇒ 「保留窗口」也是**幂等去重窗口**：at-least-once 语义要求同一
//　 idempotency_key 在窗口内重复入队返回同一任务 ⇒ ⇒ 窗口不能是 0。
// ★⇒ 窗口取值是**权衡**：太长 ⇒ 内存压不住；太短 ⇒ 幂等去重提前失效。
// ★⇒ 默认 10 分钟；生产可按 `URSM_` 之外的调用方需求调。
type MemoryStore struct {
	mu      sync.Mutex
	nextID  int64
	byID    map[int64]*Task
	byKey   map[string]int64
	claimed map[int64]bool

	// ★ pending 是「未完成」索引（★ 修 r1041 缺陷②：ClaimBatch 不再全表扫）。
	pending map[int64]bool
	// ★ lastScanCandidates 记录上一次 ClaimBatch **遍历到的候选数**。
	// ★ 它是「结构性」观测（★ 不是性能断言 —— 性能断言受机器影响）。
	lastScanCandidates int
	// ★ Retention 是完成后的保留窗口（★ 修 r1041 缺陷①）。
	Retention time.Duration
}

// DefaultMemoryStoreRetention 是完成后的默认保留窗口。
//
// ★ 10 分钟的来由：★ at-least-once 投递 + 幂等去重要求「同一 idempotency_key
// ★ 在窗口内重复入队返回同一任务」⇒ ⇒ 窗口至少要覆盖一个投递重试周期
// ★（dispatcher 的 MaxAttempts=5 / 退避上限通常在分钟级）⇒ 10 分钟够用。
const DefaultMemoryStoreRetention = 10 * time.Minute

// NewMemoryStore 创建空内存库。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		byID:      make(map[int64]*Task),
		byKey:     make(map[string]int64),
		claimed:   make(map[int64]bool),
		pending:   make(map[int64]bool),
		Retention: DefaultMemoryStoreRetention,
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
	s.pending[t.ID] = true
	return *t, nil
}

// ClaimBatch 取出可投递任务（pending/failed 且 next_retry 到期）。
//
// ★ r1041：★ 只遍历 `pending` 索引 —— 已完成任务**结构上**不在候选集合里
//　 （⇒ 旧实现的「全表扫 byID ⇒ 每 100ms 一次 ⇒ 成本随累计流量线性劣化」被消除）。
func (s *MemoryStore) ClaimBatch(_ context.Context, limit int, now time.Time) ([]Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 32
	}
	out := make([]Task, 0, limit)
	candidates := 0
	for id := range s.pending {
		candidates++
		t, ok := s.byID[id]
		if !ok {
			// ★ 索引与实体不一致（理论上不该发生）⇒ 顺手清掉，避免索引膨胀。
			delete(s.pending, id)
			continue
		}
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
	s.lastScanCandidates = candidates
	return out, nil
}

// MarkSent 标记成功。
//
// ★ r1041：★ 完成后【移出 pending 索引】（⇒ ClaimBatch 不再遍历它）
//　 但**条目本身保留到保留窗口结束**（⇒ 幂等去重窗口内重复入队仍返回同一任务）。
func (s *MemoryStore) MarkSent(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.byID[id]
	if !ok {
		return fmt.Errorf("taskbus.MemoryStore.MarkSent: id %d not found", id)
	}
	t.Status = StatusSent
	t.CompletedAt = time.Now().UTC()
	delete(s.claimed, id)
	delete(s.pending, id)
	return nil
}

// MarkFailed 标记失败并安排退避。
//
// ★ r1041：★ 失败的任务**留在 pending 索引里**（它还要重投）⇒ 语义正确。
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
	s.pending[id] = true
	return nil
}

// MarkDLQ 进入死信。
//
// ★ r1041：★ 死信是**终态** ⇒ 移出 pending 索引（否则每轮都白遍历它）。
func (s *MemoryStore) MarkDLQ(_ context.Context, id int64, lastErr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.byID[id]
	if !ok {
		return fmt.Errorf("taskbus.MemoryStore.MarkDLQ: id %d not found", id)
	}
	t.Status = StatusDLQ
	t.LastError = lastErr
	t.CompletedAt = time.Now().UTC()
	delete(s.claimed, id)
	delete(s.pending, id)
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

// Sweep 回收超保留窗口的**已完成**任务（sent / dlq）。
//
// ★ pending / failed 的任务**永不回收**（它们还要投递）。
//
// ★ r1041（2026-10-28）：★ 这是修「内存无界增长」的那一刀 ——
// ★ 旧实现里完成的条目**永不删除** ⇒ byID/byKey 随流量单调增长
// ★ （实测 20 万条 = 781 MiB，载荷是完整上游响应体）。
func (s *MemoryStore) Sweep(now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sweepLocked(now)
}

func (s *MemoryStore) sweepLocked(now time.Time) int {
	retention := s.Retention
	if retention <= 0 {
		retention = DefaultMemoryStoreRetention
	}
	removed := 0
	for id, t := range s.byID {
		if t.Status != StatusSent && t.Status != StatusDLQ {
			continue
		}
		// ★ 用「完成时刻」做基准（★ 不是 CreatedAt —— ★ 一个跑了很久才完成
		// ★ 的任务，用 CreatedAt 判龄会被**立刻**判为超窗 ⇒ 幂等窗口失效）。
		finished := t.CompletedAt
		if finished.IsZero() {
			finished = t.CreatedAt
		}
		if now.Sub(finished) < retention {
			continue
		}
		delete(s.byID, id)
		delete(s.claimed, id)
		delete(s.pending, id)
		if cur, ok := s.byKey[t.IdempotencyKey]; ok && cur == id {
			delete(s.byKey, t.IdempotencyKey)
		}
		removed++
	}
	return removed
}

// ── 观测辅助（★ 供门断言「结构性」性质，★ 不是性能断言）──────────────────

// sweep 是 Sweep 的测试别名（★ 同包内可直接调用未导出版本）。
func (s *MemoryStore) sweep(now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sweepLocked(now)
}

// retainedCount 返回当前仍保留的条目数（★ 含未完成与窗口内的已完成）。
func (s *MemoryStore) retainedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byID)
}

// pendingCount 返回未完成条目数。
func (s *MemoryStore) pendingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending)
}

// candidateScanCount 返回上一次 ClaimBatch 遍历到的候选数。
//
// ★ 这是【结构性】观测：★ 它证明「ClaimBatch 只遍历未完成索引」，
// ★ 而不是「扫得更快」（★ 后者受机器与负载影响，不是可靠判据）。
func (s *MemoryStore) candidateScanCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastScanCandidates
}

// hasIdempotencyKey 判断幂等键是否仍被占用。
func (s *MemoryStore) hasIdempotencyKey(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.byKey[key]
	return ok
}
