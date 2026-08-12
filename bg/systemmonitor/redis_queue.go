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

// ErrLeaseLost is returned by Complete when the caller's lease_token no longer
// matches the task's current lease — meaning the task was reclaimed (the
// original worker stalled/crashed past RedisLeaseTTL) and re-dispatched to
// another worker that already completed it. Callers should treat this as a
// benign "skip terminal side-effects" signal, not a hard failure.
var ErrLeaseLost = errors.New("systemmonitor: task lease lost (reclaimed by another worker)")

// RedisKey 命名空间常量（与 design §3.1 严格一致）。
//
// 任何在代码中拼写 Redis Key 的位置都必须使用这里定义的常量。
// 搜索模式: grep -rn "llmgw:monitor" --include="*.go"
const (
	RedisKeyQueue      = "llmgw:monitor:queue"
	RedisKeyProcessing = "llmgw:monitor:processing" // claimed-but-incomplete recovery lane
	RedisKeyRunning    = "llmgw:monitor:running"
	RedisKeyTasksCnt   = "llmgw:monitor:tasks:counter"
	RedisKeyWorkers    = "llmgw:monitor:workers"
	RedisKeyEventsPub  = "llmgw:monitor:events"
	RedisInflightTTL   = 30 * time.Second
	RedisRecentSuccTTL = 300 * time.Second
	// RedisLeaseTTL is how long a claimed task is owned by a worker before
	// reclaim can re-enqueue it. Must exceed RedisInflightTTL so a live worker
	// keeps ownership across the dedup window; recovery latency for a crashed
	// worker is ~RedisLeaseTTL.
	RedisLeaseTTL = 90 * time.Second
	// RedisReclaimBatch caps how many stale tasks one reclaim sweep restores,
	// bounding Lua runtime if processing ever grows abnormally large.
	RedisReclaimBatch = 256
	// RedisKeyTasksPrefix is the HASH key prefix used by the Lua scripts.
	RedisKeyTasksPrefix = "llmgw:monitor:tasks:"
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

// Submit enqueues a task atomically: ID allocation + task hash + ready queue
// happen in a single Lua EVAL, eliminating the orphan-hash / orphan-queue
// window the previous 3-round-trip implementation had on crash.
//
// See lua/submit.lua. The Lua script INCRs the counter, fills the real id into
// the task JSON, HSETs the task hash and LPUSHes the ready queue in one atomic
// step. task.ID is updated in place from the returned id.
func (q *Queue) Submit(ctx context.Context, task *Task) error {
	if !q.Enabled() {
		return errors.New("queue disabled: redis client is nil")
	}
	if task == nil {
		return errors.New("task is nil")
	}
	scripts := q.loadScripts()
	if scripts == nil {
		return errors.New("submit: lua scripts not loaded")
	}

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

	// Task JSON carries id=0; submit.lua fills the real id after INCR.
	taskJSON, err := marshalTaskForLua(task)
	if err != nil {
		return fmt.Errorf("submit: marshal task: %w", err)
	}
	fieldsJSON := q.taskHashFieldsJSON(task)

	res, err := runScript(ctx, q.rdb, scripts.submitSHA, submitLuaSrc,
		[]string{RedisKeyTasksCnt, RedisKeyTasksPrefix, RedisKeyQueue},
		taskJSON, fieldsJSON)
	if err != nil {
		return fmt.Errorf("submit: eval: %w", err)
	}
	// submit.lua returns {id_str, task_json_str}.
	arr, ok := res.([]any)
	if !ok || len(arr) < 1 {
		return fmt.Errorf("submit: unexpected script result: %v", res)
	}
	idStr, _ := arr[0].(string)
	id, parseErr := strconv.ParseInt(idStr, 10, 64)
	if parseErr != nil {
		return fmt.Errorf("submit: parse task id %q: %w", idStr, parseErr)
	}
	task.ID = id
	return nil
}

// taskHashFieldsJSON mirrors the fields written by writeTaskHash so submit.lua
// can HSET them atomically in the same EVAL. Returns a JSON object.
func (q *Queue) taskHashFieldsJSON(task *Task) string {
	fields := map[string]any{
		"task_type":         string(task.TaskType),
		"automaticity":      string(task.Automaticity),
		"source":            string(task.Source),
		"credential_id":     strconv.FormatInt(task.CredentialID, 10),
		"provider_id":       strconv.FormatInt(task.ProviderID, 10),
		"raw_model":         task.RawModel,
		"enqueued_at":       strconv.FormatInt(task.EnqueuedAt.Unix(), 10),
		"scheduled_at":      strconv.FormatInt(task.ScheduledAt.Unix(), 10),
		"next_run_at":       strconv.FormatInt(task.NextRunAt.Unix(), 10),
		"attempt":           strconv.Itoa(task.Attempt),
		"max_attempts":      strconv.Itoa(task.MaxAttempts),
		"status":            string(task.Status),
		"parent_request_id": task.ParentRequestID,
	}
	b, _ := json.Marshal(fields)
	return string(b)
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
		[]string{RedisKeyQueue, RedisKeyProcessing, RedisKeyRunning, RedisKeyTasksPrefix},
		workerIDFromContext(ctx), int(RedisInflightTTL.Seconds()), int(RedisLeaseTTL.Seconds()))
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
	// claim.lua 在 JSON 里签发了 lease_token; 保留它供 Complete fencing 用。
	if task.LeaseToken != "" {
		task.leaseToken = task.LeaseToken
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

	extrasJSON, err := json.Marshal(extras)
	if err != nil {
		return fmt.Errorf("complete: marshal extras: %w", err)
	}
	res, err := runScript(ctx, q.rdb, scripts.completeSHA, completeLuaSrc,
		[]string{taskKey, RedisKeyProcessing, RedisKeyRunning},
		string(status), string(extrasJSON), strconv.FormatInt(task.ID, 10), task.leaseToken)
	if err != nil {
		return fmt.Errorf("complete: eval: %w", err)
	}
	// complete.lua returns 0 when the caller's lease_token no longer matches
	// (task was reclaimed and re-dispatched to another worker). That is the
	// expected "lease lost" outcome, not a hard error: surface it so the caller
	// can skip terminal side-effects (audit/SSE already published by the new
	// owner) without polluting logs as a failure.
	if n, _ := res.(int64); n == 0 {
		return ErrLeaseLost
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
	// Requeue produces a fresh queue entry: clear any lease state carried over
	// from the prior claim so complete.lua fencing does not reject the next
	// owner with a stale token.
	task.leaseToken = ""

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
	// A requeued task must not also linger in the processing lane from its
	// previous claim; drop any recovery copy keyed by this task id.
	q.removeProcessingByID(ctx, task.ID)
	return nil
}

// removeProcessingByID is a best-effort cleanup that removes any processing
// member whose decoded id matches. Used by Requeue so a re-enqueued task is
// not later restored a second time by the reclaim sweep.
func (q *Queue) removeProcessingByID(ctx context.Context, taskID int64) {
	if !q.Enabled() {
		return
	}
	items, err := q.rdb.LRange(ctx, RedisKeyProcessing, 0, -1).Result()
	if err != nil {
		return
	}
	for _, raw := range items {
		var probe struct {
			ID int64 `json:"id"`
		}
		if json.Unmarshal([]byte(raw), &probe) == nil && probe.ID == taskID {
			_ = q.rdb.LRem(ctx, RedisKeyProcessing, 1, raw).Err()
			break
		}
	}
}

// Reclaim re-enqueues tasks in the processing lane whose lease has expired
// (the owning worker crashed or stalled past RedisLeaseTTL). Safe to call on
// every worker tick; it is a no-op when nothing is stale. Returns the number
// of tasks restored to the ready queue.
//
// See lua/reclaim.lua. This is the crash-recovery mechanism that makes Claim
// safe: a task popped off the ready queue is no longer lost when the worker
// dies — it lives in processing until complete removes it or reclaim restores it.
func (q *Queue) Reclaim(ctx context.Context) (int, error) {
	if !q.Enabled() {
		return 0, nil
	}
	scripts := q.loadScripts()
	if scripts == nil {
		return 0, errors.New("reclaim: lua scripts not loaded")
	}
	// reclaim.lua uses the lease TTL key (llmgw:monitor:lease:{id}) as the
	// expiry authority rather than TIME arithmetic, so miniredis FastForward
	// (which advances TTL but not TIME) exercises the recovery path correctly.
	res, err := runScript(ctx, q.rdb, scripts.reclaimSHA, reclaimLuaSrc,
		[]string{RedisKeyProcessing, RedisKeyQueue, RedisKeyTasksPrefix},
		RedisReclaimBatch)
	if err != nil {
		return 0, fmt.Errorf("reclaim: eval: %w", err)
	}
	switch v := res.(type) {
	case string:
		n, _ := strconv.Atoi(v)
		return n, nil
	case int64:
		return int(v), nil
	case int:
		return v, nil
	}
	return 0, nil
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
