package taskbus

import (
	"context"
	"time"
)

// 任务状态机（与 outbox_events 对齐，便于日后平迁）。
const (
	StatusPending = "pending"
	StatusFailed  = "failed"
	StatusDLQ     = "dlq"
	StatusSent    = "sent"
)

// TaskTypeASMEventV1 是 ASM 事件租户的任务类型（第一个存量租户登记位）。
const TaskTypeASMEventV1 = "asm.event.v1"

// TaskTypeCacheSaveV1 是 cache_save 示范迁的任务类型。
const TaskTypeCacheSaveV1 = "cache.save.v1"

// Task 是总线上的一条可投递任务。
type Task struct {
	ID             int64
	TaskType       string
	Payload        []byte
	PayloadVersion int
	IdempotencyKey string
	Status         string
	Attempts       int
	NextRetryAt    time.Time
	LastError      string
	CreatedAt      time.Time
	// ★ r1041 轮（2026-10-28）：CompletedAt = 进入终态（sent / dlq）的时刻。
	// ★ 它是**回收窗口的基准** —— ★ 没有它就只能用 CreatedAt 判龄
	// ★ ⇒ 一个跑了很久才完成的任务会被**立刻**判为「超窗」而回收
	// ★ ⇒ 幂等去重窗口（at-least-once 要求）就失效了。
	CompletedAt time.Time
}

// Handler 消费端合同：必须按 IdempotencyKey 幂等。
type Handler interface {
	Handle(ctx context.Context, task Task) error
}

// HandlerFunc 适配函数为 Handler。
type HandlerFunc func(ctx context.Context, task Task) error

// Handle 实现 Handler。
func (f HandlerFunc) Handle(ctx context.Context, task Task) error { return f(ctx, task) }

// Enqueuer 是 Hook 侧入队接口（domains/hooks 只依赖此窄接口）。
type Enqueuer interface {
	Enqueue(ctx context.Context, taskType string, idempotencyKey string, schemaVersion int, payload []byte) error
}

// Store 持久化抽象（本波 Memory；SQL 实现留待切表波）。
type Store interface {
	Enqueue(ctx context.Context, taskType string, idempotencyKey string, schemaVersion int, payload []byte) (Task, error)
	ClaimBatch(ctx context.Context, limit int, now time.Time) ([]Task, error)
	MarkSent(ctx context.Context, id int64) error
	MarkFailed(ctx context.Context, id int64, attempts int, nextRetry time.Time, lastErr string) error
	MarkDLQ(ctx context.Context, id int64, lastErr string) error
	CountByStatus(ctx context.Context, taskType, status string) (int, error)
}
