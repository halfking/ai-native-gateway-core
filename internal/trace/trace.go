// Package trace 提供请求链路追踪能力。
//
// 设计目标 (refs: docs/design/request-trace-system.md):
//   - 请求进入 → 中间件 → handler → executor → upstream → 流响应 全链路事件
//   - 每个事件包含: 序号 / 阶段 / 模块 / 耗时 / 状态 / 详情 / 错误
//   - 请求进行中: Redis 暂存 (key=request:trace:{request_id}, TTL 600s)
//   - 请求完成: 一次性 UPDATE 到 request_logs.trace_events (JSONB)
//   - 失败时: 保存上下文快照(路由状态/节点状态/并发槽位/凭据缓存等)
//
// 优雅降级:
//   - Redis 不可用 → NoopRecorder(零开销)
//   - DB 不可用 → FlushToPG 静默失败(trace_events IS NULL, 上游侧不阻塞)
//
// 性能:
//   - 每次 Append 走 Redis Pipeline/Lua, P99 < 5ms
//   - 主流程中任何 Recorder 调用都用 best-effort, 失败仅 slog.Warn 不抛错
package trace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// ─── 领域模型 ─────────────────────────────────────────────────────────────────

// Status 事件状态。与 design doc §2.2.1 对齐。
type Status string

const (
	StatusSuccess Status = "success"
	StatusFailed  Status = "failed"
	StatusTimeout Status = "timeout"
	StatusSkipped Status = "skipped"
)

// FinalStatus 整次请求的最终状态。
type FinalStatus string

const (
	FinalSuccess FinalStatus = "success"
	FinalFailed  FinalStatus = "failed"
	FinalTimeout FinalStatus = "timeout"
)

// Stage 事件阶段。与 design doc §2.2.3 一一对应。
//
// 新增阶段时:
//  1. 在下方 Stage 常量追加
//  2. 同时在 admin/request_trace.go 的 stageDisplayName 追加中文/英文映射
//  3. 前端 web/src/locales/*/trace.ts 追加 i18n key
type Stage string

const (
	StageReceiveRequest  Stage = "receive_request"  // 中间件
	StageAuthenticate    Stage = "authenticate"     // 鉴权
	StageRateLimit       Stage = "rate_limit_check" // RPM/TPM 限流
	StageBodyParse       Stage = "body_parse"       // body 读取/解析
	StageSessionLookup   Stage = "session_lookup"   // 会话查找/创建
	StageRouteResolve    Stage = "route_resolve"    // 路由解析 (候选列表)
	StageRouteCredential Stage = "route_credential" // 选中具体凭据
	StageUpstreamRequest Stage = "upstream_request" // 向上游发出 HTTP
	StageStreamStart     Stage = "stream_start"     // 首字节到达 (TTFB)
	StageStreamChunk     Stage = "stream_chunk"     // 流式 chunk 汇总
	StageStreamComplete  Stage = "stream_complete"  // 上游 [DONE] 到达
	StageRequestComplete Stage = "request_complete" // 整个请求退出(成功/失败)
)

// Module 涉及的子模块,便于按模块聚合统计。
type Module string

const (
	ModuleMiddleware Module = "middleware"
	ModuleAuth       Module = "auth"
	ModuleRatelimit  Module = "ratelimit"
	ModuleHandler    Module = "handler"
	ModuleExecutor   Module = "executor"
	ModuleUpstream   Module = "upstream"
	ModuleSession    Module = "session"
)

// TraceEvent 单个步骤事件。
//
// JSON 中的字段顺序与设计文档 §2.2.1 保持一致;前端依赖 Seq 排序。
type TraceEvent struct {
	Seq        int            `json:"seq"`
	Stage      Stage          `json:"stage"`
	StageName  string         `json:"stage_name"` // 中文显示名(由调用方传入,避免 i18n 漂移)
	Module     Module         `json:"module"`
	Timestamp  time.Time      `json:"timestamp"`
	DurationMs int            `json:"duration_ms"`
	Status     Status         `json:"status"`
	Details    map[string]any `json:"details,omitempty"`
	Error      string         `json:"error,omitempty"`
	// Snapshot 仅失败事件包含。序列化时单独字段,前端可折叠/展开。
	Snapshot *Snapshot `json:"snapshot,omitempty"`
}

// Snapshot 失败时的上下文快照。便于事后定位失败根因。
//
// 各字段都是 best-effort: 调用方未提供时为空。
// 序列化时 fields map 按 key 排序,确保 Redis 与 PG 中字段顺序一致。
type Snapshot struct {
	CapturedAt      time.Time            `json:"captured_at"`          // 快照时刻
	Candidates      []any                `json:"candidates,omitempty"` // 当前候选列表(provider_id/credential_id/状态)
	RoutingState    string               `json:"routing_state,omitempty"`
	CredentialMode  string               `json:"credential_mode,omitempty"`
	NodeProbeState  map[string]any       `json:"node_probe_state,omitempty"` // 当前模型/凭据 探测状态
	ConcurrencySlot *ConcurrencySnapshot `json:"concurrency_slot,omitempty"`
	CircuitState    string               `json:"circuit_state,omitempty"`
	FailureHint     string               `json:"failure_hint,omitempty"` // 人工可读的归类提示
	Extra           map[string]any       `json:"extra,omitempty"`
}

// ConcurrencySnapshot 凭据并发槽位快照。
type ConcurrencySnapshot struct {
	CredentialID int    `json:"credential_id"`
	InUse        int    `json:"in_use"`    // 已被占用的槽位
	MaxSlots     int    `json:"max_slots"` // 总槽位
	Blocked      bool   `json:"blocked"`   // 触发限流
	Reason       string `json:"reason,omitempty"`
}

// RequestTrace 整次请求的链路快照(供 API 直接序列化)。
//
// 与内部存储的 JSONB 列结构一致。
type RequestTrace struct {
	RequestID       string       `json:"request_id"`
	Events          []TraceEvent `json:"events"`
	FinalStatus     FinalStatus  `json:"final_status"`
	FailedAtStage   Stage        `json:"failed_at_stage,omitempty"`
	TotalDurationMs int          `json:"total_duration_ms"`
	NextSeq         int          `json:"-"`
	StartedAt       time.Time    `json:"started_at,omitempty"`
	UpdatedAt       time.Time    `json:"updated_at,omitempty"`
}

// ─── Recorder 接口 ────────────────────────────────────────────────────────────

// Recorder 是 trace 包的对外接口。所有方法都是 best-effort,
// 主流程调用不应关心其返回错误(失败仅 slog.Warn)。
type Recorder interface {
	// Append 追加单个事件。Append 内部分配 Seq, 调用方无需设置。
	// 返回 nil 表示事件已持久化(或优雅忽略,如 Noop)。
	Append(ctx context.Context, requestID string, ev TraceEvent) error

	// Finalize 标记整次请求的最终状态。失败时 finalStage 必须非空。
	Finalize(ctx context.Context, requestID string, status FinalStatus, failedStage Stage) error

	// Load 读取完整 trace。优先从 Redis 读,自动 fallback 到 PG。
	Load(ctx context.Context, requestID string) (*RequestTrace, bool, error)

	// FlushToPG 把 Redis 中的 trace 写入 request_logs.trace_events。
	// 一般在请求结束的 defer 中调用。失败仅日志,不抛错。
	FlushToPG(ctx context.Context, db *pgxpool.Pool, requestID string) error

	// Purge 主动删除 Redis key(可选,主要供测试)。
	Purge(ctx context.Context, requestID string) error
}

// ─── NoopRecorder ────────────────────────────────────────────────────────────

// NoopRecorder 当 Redis 不可用或生产环境禁用 trace 时使用。
// 所有方法都是 O(1),零开销,主流程零阻塞。
type NoopRecorder struct{}

// Append implements Recorder.
func (NoopRecorder) Append(_ context.Context, _ string, _ TraceEvent) error { return nil }

// Finalize implements Recorder.
func (NoopRecorder) Finalize(_ context.Context, _ string, _ FinalStatus, _ Stage) error {
	return nil
}

// Load implements Recorder.
func (NoopRecorder) Load(_ context.Context, _ string) (*RequestTrace, bool, error) {
	return nil, false, nil
}

// FlushToPG implements Recorder.
func (NoopRecorder) FlushToPG(_ context.Context, _ *pgxpool.Pool, _ string) error { return nil }

// Purge implements Recorder.
func (NoopRecorder) Purge(_ context.Context, _ string) error { return nil }

// ─── RedisRecorder ───────────────────────────────────────────────────────────

const (
	// redisKeyPrefix 所有 trace key 的命名空间,跟随项目惯例 session:* / pending_response*
	redisKeyPrefix = "request:trace:"

	// defaultRedisTTL 默认 10 分钟。覆盖设计文档 §2.2.2 的 600s。
	// 在 streaming/长上下文场景下更宽容,避免 5 分钟就过期看不到进度。
	defaultRedisTTL = 10 * time.Minute

	// maxEventsPerRequest 单请求最大事件数,防止异常/无限循环撑爆 Redis。
	// 设计文档 §6 提到的安全限制。
	maxEventsPerRequest = 200
)

// Lua 脚本:原子完成"读旧 trace → 追加新事件 → 截断超出 maxEvents 的旧事件 → 重设 TTL"。
//
// KEYS[1] = request:trace:{id}
// ARGV[1] = 新事件的 JSON 字符串
// ARGV[2] = 当前时间 RFC3339Nano
// ARGV[3] = maxEventsPerRequest
// ARGV[4] = TTL (秒)
// ARGV[5] = request_id
// ARGV[6] = 当前时间 Unix 毫秒
//
// 返回: trace 的最终 JSON 字符串(或错误时为 "ERR:...")。
const luaAppendEvent = `
local key = KEYS[1]
local newEvJson = ARGV[1]
local nowTs = ARGV[2]
local maxEvents = tonumber(ARGV[3])
local ttlSec = tonumber(ARGV[4])

local existing = redis.call('GET', key)
local trace
if not existing then
    trace = { request_id = ARGV[5], events = {}, final_status = '', failed_at_stage = '', total_duration_ms = 0, started_at = nowTs, started_at_unix_ms = tonumber(ARGV[6]) }
else
    trace = cjson.decode(existing)
    if trace.request_id == nil or trace.request_id == '' then
        trace.request_id = ARGV[5]
    end
end

local events = trace.events
if type(events) == 'string' then
    events = cjson.decode(events)
end
local nextSeq = tonumber(trace.next_seq) or (#events + 1)
-- 新事件的 Seq: 由 envelope 维护的单调计数分配,截断后不重复
local newEv = cjson.decode(newEvJson)
newEv.seq = nextSeq
nextSeq = nextSeq + 1
table.insert(events, newEv)

-- 截断:保留最后 maxEvents 个
if #events > maxEvents then
    local keepFrom = #events - maxEvents + 1
    local trimmed = {}
    for i = keepFrom, #events do
        table.insert(trimmed, events[i])
    end
    events = trimmed
end

trace.events = events
trace.next_seq = nextSeq
trace.updated_at = nowTs
local out = cjson.encode(trace)
redis.call('SET', key, out, 'EX', ttlSec)
return out
`

// RedisRecorder 通过 Redis 暂存 trace。FlushToPG 由 defer 调用。
//
// 线程安全: 所有方法都依赖 Redis 自身的原子性,不维护本地状态。
type RedisRecorder struct {
	rdb        *redis.Client
	ttl        time.Duration
	maxEvents  int
	appendSha  atomic.Pointer[string] // 缓存 EVALSHA 的 SHA
	appendOnce sync.Once
}

// NewRedisRecorder 构造一个 RedisRecorder。rdb=nil 时返回 NoopRecorder。
func NewRedisRecorder(rdb *redis.Client) Recorder {
	if rdb == nil {
		return NoopRecorder{}
	}
	return &RedisRecorder{
		rdb:       rdb,
		ttl:       defaultRedisTTL,
		maxEvents: maxEventsPerRequest,
	}
}

// keyFor 构造 Redis key。
func keyFor(requestID string) string { return redisKeyPrefix + requestID }

// cachedScript 首次调用时 SCRIPT LOAD,之后用 EVALSHA。
func (r *RedisRecorder) cachedScript(ctx context.Context) (string, error) {
	if v := r.appendSha.Load(); v != nil {
		return *v, nil
	}
	sha, err := r.rdb.ScriptLoad(ctx, luaAppendEvent).Result()
	if err != nil {
		return "", err
	}
	r.appendSha.Store(&sha)
	return sha, nil
}

// Append implements Recorder.
//
// 失败仅 slog.Warn,不返回 error 给主流程(签名仍保留 error 以便测试断言)。
func (r *RedisRecorder) Append(ctx context.Context, requestID string, ev TraceEvent) error {
	if requestID == "" || r == nil || r.rdb == nil {
		return nil
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now()
	}
	ev.Seq = 0 // 由 Lua 脚本内部分配

	payload, err := json.Marshal(ev)
	if err != nil {
		slog.Warn("trace.Append: marshal event failed",
			"request_id", requestID, "stage", ev.Stage, "err", err)
		return err
	}

	runCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()

	nowTs := time.Now().UTC().Format(time.RFC3339Nano)
	sha, err := r.cachedScript(runCtx)
	if err != nil {
		slog.Warn("trace.Append: SCRIPT LOAD failed",
			"request_id", requestID, "err", err)
		// Fallback: 用 Eval 直接传 script,SCRIPT LOAD 失败不会让整体失败。
		eventTimeMs := ev.Timestamp.UnixMilli()
		_, err = r.rdb.Eval(runCtx, luaAppendEvent, []string{keyFor(requestID)},
			string(payload), nowTs, r.maxEvents, int(r.ttl.Seconds()), requestID, eventTimeMs).Result()
	} else {
		eventTimeMs := ev.Timestamp.UnixMilli()
		_, err = r.rdb.EvalSha(runCtx, sha, []string{keyFor(requestID)},
			string(payload), nowTs, r.maxEvents, int(r.ttl.Seconds()), requestID, eventTimeMs).Result()
	}
	if err != nil {
		slog.Warn("trace.Append: EVAL failed",
			"request_id", requestID, "stage", ev.Stage, "err", err)
	}
	return err
}

// Finalize implements Recorder.
func (r *RedisRecorder) Finalize(ctx context.Context, requestID string, status FinalStatus, failedStage Stage) error {
	if requestID == "" || r == nil || r.rdb == nil {
		return nil
	}
	key := keyFor(requestID)
	runCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()

	// 通过 Lua 同时更新终态、耗时和完成事件,避免两阶段竞态。
	const luaFinalize = `
local key = KEYS[1]
local status = ARGV[1]
local failedStage = ARGV[2]
local nowTs = ARGV[3]
local ttlSec = tonumber(ARGV[4])
local nowMs = tonumber(ARGV[5])

local existing = redis.call('GET', key)
if not existing then
    return 0
end
local trace = cjson.decode(existing)
local events = trace.events
if type(events) == 'string' then
    events = cjson.decode(events)
end
local hasComplete = false
for _, ev in ipairs(events) do
    if ev.stage == 'request_complete' then
        hasComplete = true
        break
    end
end
if not hasComplete then
    local nextSeq = tonumber(trace.next_seq) or (#events + 1)
    table.insert(events, {
        seq = nextSeq,
        stage = 'request_complete',
        stage_name = 'trace.stage.request_complete',
        module = 'handler',
        timestamp = nowTs,
        duration_ms = 0,
        status = status,
        details = { failed_stage = failedStage }
    })
    trace.next_seq = nextSeq + 1
end
trace.events = events
trace.final_status = status
trace.failed_at_stage = failedStage
local startedMs = tonumber(trace.started_at_unix_ms)
if startedMs and nowMs and nowMs >= startedMs then
    trace.total_duration_ms = nowMs - startedMs
else
    trace.total_duration_ms = tonumber(trace.total_duration_ms) or 0
end
trace.updated_at = nowTs
redis.call('SET', key, cjson.encode(trace), 'EX', ttlSec)
return 1
`
	result, err := r.rdb.Eval(runCtx, luaFinalize, []string{key},
		string(status), string(failedStage),
		time.Now().UTC().Format(time.RFC3339Nano),
		int(r.ttl.Seconds()), time.Now().UnixMilli()).Result()
	if err != nil {
		slog.Warn("trace.Finalize failed",
			"request_id", requestID, "status", status, "err", err)
		return err
	}
	if n, ok := result.(int64); ok && n == 0 {
		return fmt.Errorf("trace: request %q not found during finalize", requestID)
	}
	return nil
}

// Load implements Recorder.
//
// 读取顺序: Redis → PG。
// fromRedis=true 表示来自 Redis,fromRedis=false 表示 PG 兜底。
func (r *RedisRecorder) Load(ctx context.Context, requestID string) (*RequestTrace, bool, error) {
	if requestID == "" {
		return nil, false, ErrEmptyRequestID
	}
	if r == nil || r.rdb == nil {
		return nil, false, nil
	}
	key := keyFor(requestID)

	runCtx, cancel := context.WithTimeout(ctx, 1000*time.Millisecond)
	defer cancel()

	raw, err := r.rdb.Get(runCtx, key).Result()
	if err == nil {
		trace, err := unmarshalTrace(raw, requestID)
		if err != nil {
			return nil, false, err
		}
		return trace, true, nil
	}
	if !errors.Is(err, redis.Nil) {
		slog.Warn("trace.Load: Redis GET failed",
			"request_id", requestID, "err", err)
	}
	return nil, false, nil
}

// FlushToPG implements Recorder.
//
// 把 Redis 中的 trace 一次性写入 request_logs.trace_events (JSONB)。
// 删除 Redis key 在写入成功(或写入失败但已重试)后执行,避免并发请求看到"trace 已 flush 但 DB 还未写"的中间态。
func (r *RedisRecorder) FlushToPG(ctx context.Context, db *pgxpool.Pool, requestID string) error {
	if requestID == "" || r == nil || r.rdb == nil || db == nil {
		return nil
	}
	key := keyFor(requestID)

	runCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	raw, err := r.rdb.Get(runCtx, key).Result()
	if errors.Is(err, redis.Nil) {
		return nil // 没有 trace 数据,无需 flush
	}
	if err != nil {
		slog.Warn("trace.FlushToPG: Redis GET failed",
			"request_id", requestID, "err", err)
		// 即使 Redis 读失败,也不阻塞主流程
		return err
	}

	result, err := db.Exec(runCtx, `
		UPDATE request_logs
		SET trace_events = $1::jsonb
		WHERE request_id = $2
	`, raw, requestID)
	if err != nil {
		slog.Warn("trace.FlushToPG: UPDATE failed",
			"request_id", requestID, "err", err)
		return err
	}
	if !shouldDeleteTraceAfterFlush(result.RowsAffected()) {
		slog.Warn("trace.FlushToPG: request log row not found, retaining Redis trace",
			"request_id", requestID)
		return ErrTraceParentNotFound
	}

	// 2026-07-19: 解析 trace_events 并写入 request_stage_events 规范化表（migration 434）
	if err := r.writeStageEvents(runCtx, db, requestID, raw); err != nil {
		// 非阻塞失败，记录警告但不返回错误（保证主流程不受影响）
		slog.Warn("trace.FlushToPG: writeStageEvents failed",
			"request_id", requestID, "err", err)
	}

	// Flush 成功后删除 Redis key (用 DEL 而非 UNLINK,确保后续读取立刻拿到 PG 版)。
	if delErr := r.rdb.Del(runCtx, key).Err(); delErr != nil {
		slog.Warn("trace.FlushToPG: Redis DEL failed",
			"request_id", requestID, "err", delErr)
	}
	return nil
}

// Purge implements Recorder. 仅测试/手动管理使用。
func (r *RedisRecorder) Purge(ctx context.Context, requestID string) error {
	if r == nil || r.rdb == nil {
		return nil
	}
	return r.rdb.Del(ctx, keyFor(requestID)).Err()
}

// ─── 辅助 ────────────────────────────────────────────────────────────────────

func shouldDeleteTraceAfterFlush(rowsAffected int64) bool {
	return rowsAffected > 0
}

// ErrTraceParentNotFound 表示 trace 对应的 request_logs 行尚未落库或已不存在。
var ErrTraceParentNotFound = errors.New("trace: request log row not found")

// ErrEmptyRequestID 当 requestID 为空时返回。
var ErrEmptyRequestID = errors.New("trace: empty request_id")

// unmarshalTrace 把 Redis 中的 JSON 解到 RequestTrace。
// 双向兼容: 既支持新格式({request_id, events, ...}) 也支持纯 events 数组的旧数据。
func unmarshalTrace(raw, fallbackID string) (*RequestTrace, error) {
	var env struct {
		RequestID       string          `json:"request_id"`
		Events          json.RawMessage `json:"events"`
		FinalStatus     FinalStatus     `json:"final_status"`
		FailedAtStage   Stage           `json:"failed_at_stage"`
		TotalDurationMs int             `json:"total_duration_ms"`
		NextSeq         int             `json:"next_seq"`
		StartedAt       time.Time       `json:"started_at"`
		UpdatedAt       time.Time       `json:"updated_at"`
	}

	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return nil, fmt.Errorf("trace: unmarshal envelope: %w", err)
	}
	var events []TraceEvent
	if len(env.Events) > 0 && string(env.Events) != "null" {
		if err := json.Unmarshal(env.Events, &events); err != nil {
			var legacy string
			if string(env.Events)[0] != '"' || json.Unmarshal(env.Events, &legacy) != nil || json.Unmarshal([]byte(legacy), &events) != nil {
				return nil, fmt.Errorf("trace: unmarshal events: %w", err)
			}
		}
	}
	if env.RequestID == "" {
		env.RequestID = fallbackID
	}
	return &RequestTrace{
		RequestID:       env.RequestID,
		Events:          events,
		FinalStatus:     env.FinalStatus,
		FailedAtStage:   env.FailedAtStage,
		TotalDurationMs: env.TotalDurationMs,
		NextSeq:         env.NextSeq,
		StartedAt:       env.StartedAt,
		UpdatedAt:       env.UpdatedAt,
	}, nil
}

// LoadFromPG 是 PG 兜底读取。供 admin/request_trace.go 在 Redis miss 时调用。
//
// 独立函数(非 RedisRecorder 方法),便于测试。
//
// 2026-07-17: request_id 不存在 → 返回 (nil, nil),而不是把 pgx.ErrNoRows 抛给上层。
// "没有 trace 数据" 是预期路径 (例如尚未 flush 的进行中请求、或极老的 history 行),
// 不应该作为 HTTP 500 报给前端。原实现会让前端展示 "no rows in result set" 红色错误,
// 实际只是该行尚无 trace_events JSONB;此处把 ErrNoRows 当成"未找到"返回。
func LoadFromPG(ctx context.Context, db *pgxpool.Pool, requestID string) (*RequestTrace, error) {
	if requestID == "" {
		return nil, ErrEmptyRequestID
	}
	if db == nil {
		return nil, nil
	}
	var raw []byte
	err := db.QueryRow(ctx,
		`SELECT trace_events FROM request_logs WHERE request_id = $1 LIMIT 1`,
		requestID).Scan(&raw)
	if err != nil {
		// 2026-07-17: 没有 trace_events JSONB 或 request_id 不存在都属于"没数据",
		// 不算 load 失败。让上层进入 not-found / probe-synthesize 路径即可。
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	return unmarshalTrace(string(raw), requestID)
}
