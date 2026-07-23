// Package bg/systemmonitor — redis_queue.go
//
// 任务入队 / 抢占 / 完成 / 重新入队的 Redis 操作封装。
// 设计依据: docs/会话优化v2/32-系统监测模块设计.md §3.1 / §3.2
package systemmonitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisKey 命名空间常量（与 design §3.1 严格一致）。
//
// 任何在代码中拼写 Redis Key 的位置都必须使用这里定义的常量。
// 搜索模式: grep -rn "llmgw:monitor" --include="*.go"
const (
	RedisKeyQueue      = "llmgw:monitor:queue"
	RedisKeyRunning    = "llmgw:monitor:running"
	RedisKeyTasksCnt   = "llmgw:monitor:tasks:counter"
	RedisKeyWorkers    = "llmgw:monitor:workers"
	RedisKeyEventsPub  = "llmgw:monitor:events"
	RedisInflightTTL   = 30 * time.Second
	RedisRecentSuccTTL = 300 * time.Second
)

// Queue 是面向 SystemMonitor 的 Redis 队列封装。
//
// 线程安全：所有方法接收 ctx 后转发给 redis.Client（其内部已线程安全）。
// Fallback：当 rdb == nil 时所有写方法返回 nil + nil（fallback_queue.go
// 由 SystemMonitor 顶层接管内存降级路径）。
//
// scripts 字段以 atomic.Pointer 存放（2026-07-24 审计修复）：worker 在 Claim/Complete
// 中读，Submit 在 Lua 失败重载时写；裸指针存在数据竞争。
type Queue struct {
	rdb     *redis.Client
	scripts atomic.Pointer[LoadedScripts]
}

// NewQueue 构造队列包装。
func NewQueue(rdb *redis.Client, scripts *LoadedScripts) *Queue {
	q := &Queue{rdb: rdb}
	if scripts != nil {
		q.scripts.Store(scripts)
	}
	return q
}

// Enabled reports whether the queue has a live Redis backend.
func (q *Queue) Enabled() bool {
	return q != nil && q.rdb != nil
}

// setScripts 原子替换已加载的 Lua 脚本（仅 Submit 失败重载路径使用）。
func (q *Queue) setScripts(s *LoadedScripts) {
	if s == nil {
		return
	}
	q.scripts.Store(s)
}

// loadScripts 返回当前已加载的脚本；nil 表示尚未加载。
func (q *Queue) loadScripts() *LoadedScripts {
	return q.scripts.Load()
}

// Submit enqueues a task to the Redis FIFO queue and persists the full
// task definition under llmgw:monitor:tasks:{id}.
//
// Steps:
//  1. INCR llmgw:monitor:tasks:counter → task_id
//  2. Marshal task to JSON (claim.lua expects cjson-compatible encoding)
//  3. HSET llmgw:monitor:tasks:{id} task fields (for REST GET + SSE replay)
//  4. LPUSH llmgw:monitor:queue {json}
//
// The caller (SystemMonitor.Submit) is responsible for Validate() before
// Submit() — Submit itself does not call Validate to keep the hot path lean.
func (q *Queue) Submit(ctx context.Context, task *Task) error {
	if !q.Enabled() {
		return errors.New("queue disabled: redis client is nil")
	}
	if task == nil {
		return errors.New("task is nil")
	}

	// 1. 分配 task_id
	id, err := q.rdb.Incr(ctx, RedisKeyTasksCnt).Result()
	if err != nil {
		return fmt.Errorf("submit: incr counter: %w", err)
	}
	task.ID = id
	if task.EnqueuedAt.IsZero() {
		task.EnqueuedAt = time.Now().UTC()
	}
	if task.ScheduledAt.IsZero() {
		task.ScheduledAt = task.EnqueuedAt
	}
	if task.NextRunAt.IsZero() {
		task.NextRunAt = task.ScheduledAt
	}
	task.Status = TaskStatusReady
	task.Attempt = 0

	// 2. JSON 编码（Lua cjson 兼容：用 float64 表示 ms 时间戳）
	taskJSON, err := marshalTaskForLua(task)
	if err != nil {
		return fmt.Errorf("submit: marshal task: %w", err)
	}

	// 3. 写任务 hash（REST GET + SSE 重放）
	if err := q.writeTaskHash(ctx, task); err != nil {
		return fmt.Errorf("submit: write task hash: %w", err)
	}

	// 4. LPUSH 入队（FIFO = LPUSH + RPOPLPUSH + LPOP 的对称）
	if err := q.rdb.LPush(ctx, RedisKeyQueue, taskJSON).Err(); err != nil {
		return fmt.Errorf("submit: lpush queue: %w", err)
	}
	return nil
}

// Claim atomically pops the next eligible task off the queue.
//
// See lua/claim.lua for the full script semantics. Returns:
//
//	(nil, nil)   - 队列空 / 队头任务该跳过 / 损坏 JSON
//	(*Task, nil) - 抢占成功
//	(nil, err)   - Redis 故障 / 脚本执行失败
//
// 2026-07-24 审计修复：inflight key (30s dedup) 现已在 claim.lua 内部从
// task.credential_id / task.raw_model 构造；调用方在解码前无法（也不应）提供
// credID/rawModel。修复前 fetchTask 用 (0,"") 调用 → 守卫拒绝 → 队列永不消费。
func (q *Queue) Claim(ctx context.Context) (*Task, error) {
	if !q.Enabled() {
		return nil, errors.New("queue disabled: redis client is nil")
	}
	scripts := q.loadScripts()
	if scripts == nil {
		return nil, errors.New("claim: lua scripts not loaded")
	}

	res, err := runScript(ctx, q.rdb, scripts.claimSHA, claimLuaSrc,
		[]string{RedisKeyQueue},
		workerIDFromContext(ctx), int(RedisInflightTTL.Seconds()))
	if err != nil {
		return nil, fmt.Errorf("claim: eval: %w", err)
	}
	if res == nil {
		return nil, nil // 队列空 / 应跳过
	}
	raw, ok := res.(string)
	if !ok || raw == "" {
		return nil, nil
	}
	var task Task
	if err := json.Unmarshal([]byte(raw), &task); err != nil {
		return nil, fmt.Errorf("claim: unmarshal task: %w", err)
	}
	return &task, nil
}

// Complete marks the task as done in Redis (status update + running removal).
//
// The inflight token is left to expire naturally (30s) so other workers do
// not race onto the same node before the next scheduled_at. To release
// immediately, call CompleteWithInflightRelease (Phase 2).
func (q *Queue) Complete(ctx context.Context, task *Task, status TaskStatus, extras map[string]any) error {
	if !q.Enabled() {
		return errors.New("queue disabled: redis client is nil")
	}
	if task == nil {
		return errors.New("task is nil")
	}
	scripts := q.loadScripts()
	if scripts == nil {
		return errors.New("complete: lua scripts not loaded")
	}

	taskKey := task.HashKey()
	inflightKey := task.InflightKey()

	extrasJSON, err := json.Marshal(extras)
	if err != nil {
		return fmt.Errorf("complete: marshal extras: %w", err)
	}
	_, err = runScript(ctx, q.rdb, scripts.completeSHA, completeLuaSrc,
		[]string{taskKey, inflightKey}, string(status), string(extrasJSON))
	if err != nil {
		return fmt.Errorf("complete: eval: %w", err)
	}

	// 本地缓存同步更新
	task.Status = status
	now := time.Now().UTC()
	task.FinishedAt = &now
	task.Attempt++
	return nil
}

// Requeue pushes the task back to the queue with a new scheduled_at.
// Used by the backoff ladder after a failed attempt (design §4.5).
//
// IMPORTANT: the task's next_run_at must be in the future; claim.lua will
// rotate it back to the tail until then. The caller is responsible for
// choosing the right backoff step.
func (q *Queue) Requeue(ctx context.Context, task *Task, nextRunAt time.Time) error {
	if !q.Enabled() {
		return errors.New("queue disabled: redis client is nil")
	}
	if task == nil {
		return errors.New("task is nil")
	}
	task.NextRunAt = nextRunAt
	task.Attempt++
	task.ScheduledAt = nextRunAt
	task.Status = TaskStatusReady

	taskJSON, err := marshalTaskForLua(task)
	if err != nil {
		return fmt.Errorf("requeue: marshal task: %w", err)
	}
	if err := q.rdb.LPush(ctx, RedisKeyQueue, taskJSON).Err(); err != nil {
		return fmt.Errorf("requeue: lpush: %w", err)
	}
	if err := q.writeTaskHash(ctx, task); err != nil {
		return fmt.Errorf("requeue: write task hash: %w", err)
	}
	return nil
}

// QueueSize returns LLEN(llmgw:monitor:queue) for dashboards / monitoring.
func (q *Queue) QueueSize(ctx context.Context) (int64, error) {
	if !q.Enabled() {
		return 0, nil
	}
	return q.rdb.LLen(ctx, RedisKeyQueue).Result()
}

// RunningSize returns SCARD(llmgw:monitor:running) for dashboards.
func (q *Queue) RunningSize(ctx context.Context) (int64, error) {
	if !q.Enabled() {
		return 0, nil
	}
	return q.rdb.SCard(ctx, RedisKeyRunning).Result()
}

// writeTaskHash persists the task definition so REST GET + SSE replay can
// reconstruct the original Task without re-running probe results.
func (q *Queue) writeTaskHash(ctx context.Context, task *Task) error {
	fields := map[string]any{
		"id":                task.ID,
		"task_type":         string(task.TaskType),
		"automaticity":      string(task.Automaticity),
		"source":            string(task.Source),
		"credential_id":     task.CredentialID,
		"provider_id":       task.ProviderID,
		"raw_model":         task.RawModel,
		"enqueued_at":       task.EnqueuedAt.Unix(),
		"scheduled_at":      task.ScheduledAt.Unix(),
		"next_run_at":       task.NextRunAt.Unix(),
		"attempt":           task.Attempt,
		"max_attempts":      task.MaxAttempts,
		"status":            string(task.Status),
		"parent_request_id": task.ParentRequestID,
	}
	if task.WorkerID != "" {
		fields["worker_id"] = task.WorkerID
	}
	return q.rdb.HSet(ctx, task.HashKey(), fields).Err()
}

// marshalTaskForLua produces JSON that lua/cjson can decode directly.
//
// Critical difference from json.Marshal:
//
//	json.Marshal: time.Time -> RFC3339 string "2026-07-23T12:00:00Z" (Lua must re-parse)
//	cjson.decode:  only handles number / bool / string / null / table
//
// We convert time fields to milliseconds-since-epoch (number) so the Lua
// side can compare them directly against redis.call('TIME').
func marshalTaskForLua(t *Task) (string, error) {
	type luaTask struct {
		ID              int64  `json:"id"`
		TaskType        string `json:"task_type"`
		Automaticity    string `json:"automaticity"`
		Source          string `json:"source"`
		CredentialID    int64  `json:"credential_id"`
		ProviderID      int64  `json:"provider_id"`
		RawModel        string `json:"raw_model"`
		EnqueuedAtMs    int64  `json:"enqueued_at_ms"`
		ScheduledAtMs   int64  `json:"scheduled_at_ms"`
		NextRunAtMs     int64  `json:"next_run_at_ms"`
		Attempt         int    `json:"attempt"`
		MaxAttempts     int    `json:"max_attempts"`
		ParentRequestID string `json:"parent_request_id,omitempty"`
		Priority        int    `json:"priority"`
	}
	p := t.TaskType.Priority()
	out := luaTask{
		ID:              t.ID,
		TaskType:        string(t.TaskType),
		Automaticity:    string(t.Automaticity),
		Source:          string(t.Source),
		CredentialID:    t.CredentialID,
		ProviderID:      t.ProviderID,
		RawModel:        t.RawModel,
		EnqueuedAtMs:    unixMilli(t.EnqueuedAt),
		ScheduledAtMs:   unixMilli(t.ScheduledAt),
		NextRunAtMs:     unixMilli(t.NextRunAt),
		Attempt:         t.Attempt,
		MaxAttempts:     t.MaxAttempts,
		ParentRequestID: t.ParentRequestID,
		Priority:        int(p),
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func unixMilli(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano() / int64(time.Millisecond)
}

// workerIDFromContext returns a stable identifier for the current gateway
// instance — used by claim.lua to record which worker claimed a task.
//
// Defaults to "default" if not set in context. main.go sets this in the
// SystemMonitor construction with the gateway's hostname + pid.
type workerIDKey struct{}

func contextWithWorkerID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, workerIDKey{}, id)
}

func workerIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(workerIDKey{}).(string); ok && v != "" {
		return v
	}
	return "default"
}

// Int64FromString parses a Redis HGETALL field that may arrive as string.
func Int64FromString(s string) int64 {
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}
