// Package liveactions implements the request-lifecycle action-event emitter
// (V3.3-OBS OBS-B1, docs/会话优化v3/24 §2/§4).
//
// Every visible step of a client request (arrive → route_resolved →
// model_enqueued → ... → reply) is emitted as an ActionEvent keyed by
// request_id with a per-request monotonically increasing seq. Events are
// appended to the bounded Redis LIST llmgw:live:actions (LPUSH+LTRIM, 5000
// entries) so the SSE initial_data replay can rebuild the action timeline
// after a page refresh.
//
// Hard rules (23 号 §2 架构原则 5/6):
//   - 发射旁路异步：Emit NEVER blocks the caller. Events go through a buffered
//     channel; when full the event is dropped and live_actions_dropped_total
//     is incremented (queue-full-means-drop, inherited from the Stats
//     non-blocking principle).
//   - Redis 不可用时静默降级：write errors are counted
//     (live_actions_redis_failures_total) and logged at debug level only.
//   - 安全红线：正文、API key、系统 prompt 不得进入任何可观测通道。
//     ActionEvent carries only ids, model names, error KINDS and small
//     string details — callers must never put body content or secrets into
//     Detail.
//
// nil-safety: every Emit site calls (*Emitter).Emit on a possibly-nil
// pointer; the method is a no-op for a nil receiver, so instrumented packages
// have zero behavioural dependency on wiring.
package liveactions

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
)

// Action is the closed action-event vocabulary (24 号 §2, 13 kinds). It is
// aligned with the internal/trace stage naming so the timeline and the trace
// view share one vocabulary.
type Action string

const (
	// ActionArrive: 客户端请求到达网关 (S1). Detail: client_protocol, model(原始).
	ActionArrive Action = "arrive"
	// ActionRouteResolved: 模型解析完成，auto 决策/别名/直通 (S3).
	ActionRouteResolved Action = "route_resolved"
	// ActionModelEnqueued: 落入模型队列 (S4). Detail: queue_depth.
	ActionModelEnqueued Action = "model_enqueued"
	// ActionCredentialSelected: 路由选定凭据 (S5). Detail: weight, tier.
	ActionCredentialSelected Action = "credential_selected"
	// ActionNodeEnqueued: 落入节点（凭据）队列 (S6). Detail: queue_depth.
	ActionNodeEnqueued Action = "node_enqueued"
	// ActionNodeSelected: 最终选定节点 (S7 前). Detail: sticky.
	ActionNodeSelected Action = "node_selected"
	// ActionUpstreamRequest: 开始转发 (S7). Detail: attempt.
	ActionUpstreamRequest Action = "upstream_request"
	// ActionFirstByte: 收到首字节 (S8). Detail: ttfb_ms.
	ActionFirstByte Action = "first_byte"
	// ActionReply: 收到回复/完成 (S9, 含错误). Detail: status, latency_ms.
	ActionReply Action = "reply"
	// ActionNodeSwitch: 切换节点/重试. Detail: from_credential_id,
	// to_credential_id, reason; Retry=true 时前端打特别标 (24 号 §1).
	ActionNodeSwitch Action = "node_switch"
	// ActionModelSwitch: 切换模型. Detail: from_model, to_model, reason.
	ActionModelSwitch Action = "model_switch"
	// ActionStateChange: 节点状态变更（旁路投影）。节点维度事件，不挂请求
	// (走 node_update/state_transition 通道)；本包仅保证枚举与类型支持。
	ActionStateChange Action = "state_change"
	// ActionNoRoute: 无可用路由. Detail: blocked_reasons.
	ActionNoRoute Action = "no_route"
)

// Actions is the full closed enum, in doc-24 §2 order. Test/枚举完整性用.
var Actions = []Action{
	ActionArrive,
	ActionRouteResolved,
	ActionModelEnqueued,
	ActionCredentialSelected,
	ActionNodeEnqueued,
	ActionNodeSelected,
	ActionUpstreamRequest,
	ActionFirstByte,
	ActionReply,
	ActionNodeSwitch,
	ActionModelSwitch,
	ActionStateChange,
	ActionNoRoute,
}

// ActionEvent is one request-scoped action (24 号 §2). All fields except
// RequestID/Seq/Action/Ts are optional + backward compatible (13 号契约
// 原则). Detail values must never contain body content, API keys or system
// prompts (安全红线).
type ActionEvent struct {
	RequestID     string            `json:"request_id"`
	Seq           int64             `json:"seq"`
	Action        Action            `json:"action"`
	Ts            time.Time         `json:"ts"`
	Model         string            `json:"model,omitempty"`
	CredentialID  int               `json:"credential_id,omitempty"`
	ErrorKind     string            `json:"error_kind,omitempty"`
	RetrySeq      int               `json:"retry_seq,omitempty"`
	Retry         bool              `json:"retry,omitempty"`
	Stage         string            `json:"stage,omitempty"`
	StageCategory string            `json:"stage_category,omitempty"`
	Detail        map[string]string `json:"detail,omitempty"`
}

func stageForAction(action Action) requestjourney.JourneyStage {
	switch action {
	case ActionArrive:
		return requestjourney.StageReceived
	case ActionRouteResolved:
		return requestjourney.StageRouting
	case ActionModelEnqueued:
		return requestjourney.StageModelQueue
	case ActionCredentialSelected, ActionNodeSelected:
		return requestjourney.StageNodeSelection
	case ActionNodeEnqueued:
		return requestjourney.StageCredentialQueue
	case ActionUpstreamRequest:
		return requestjourney.StageUpstream
	case ActionFirstByte:
		return requestjourney.StageStreaming
	case ActionNodeSwitch, ActionModelSwitch:
		return requestjourney.StageRetrying
	case ActionReply, ActionNoRoute:
		return requestjourney.StageTerminal
	default:
		return ""
	}
}

func normalizeStage(ev *ActionEvent) {
	if ev == nil {
		return
	}
	stage := requestjourney.JourneyStage(ev.Stage)
	if stage == "" {
		stage = stageForAction(ev.Action)
		if stage != "" {
			ev.Stage = string(stage)
		} else {
			// No caller-supplied Stage and no mapping for this Action.
			// Surface as a metric so future Action additions missing from
			// stageForAction are caught instead of silently emitting events
			// with empty stage / stage_category.
			stageNormFailureTot.Add(1)
			return
		}
	}
	if ev.StageCategory == "" && stage != "" {
		ev.StageCategory = string(stage.Category())
	}
}

// Redis contract (24 号 §4).
const (
	// RedisKey is the bounded action replay queue behind GET /api/admin/live-stream
	// initial_data. LPUSH (newest at head) + LTRIM keeps it bounded.
	RedisKey = "llmgw:live:actions"
	// RedisMaxLen is the queue bound (24 号 §4: 有界 5000 条).
	RedisMaxLen = 5000
	// DefaultBufferSize is the in-process emit buffer. When full, events are
	// dropped + counted (never block the hot path).
	DefaultBufferSize = 4096
	// redisWriteTimeout bounds one worker-side LPUSH+LTRIM pipeline.
	redisWriteTimeout = 2 * time.Second
)

// redisMaxLen is the runtime LTRIM bound (defaults to RedisMaxLen; a var so
// tests can shrink it instead of pushing 5000 events through race mode).
var redisMaxLen = int64(RedisMaxLen)

// Client is the minimal Redis surface the emitter needs. *redis.Client
// satisfies it; tests inject a miniredis-backed client. Mirrors the client
// injection style of admin.LiveStreamRedisStore.
type Client interface {
	Pipeline() redis.Pipeliner
}

// ── per-request seq allocation ─────────────────────────────────────────────
//
// 24 号 §2: 事件 seq 在 request_id 内单调递增；前端按 seq 排序。跨进程不要求
// 严格全局序（进程内 sync.Map 计数足够）。Counters are pruned on terminal
// actions (reply / no_route) so the map tracks in-flight requests only.
var seqCounters sync.Map // requestID -> *atomic.Int64

// NextSeq returns the next per-request sequence number (1-based). Empty
// request ids share a single "" counter; state_change (node-dimension) events
// may pass "" and get seq 0 (they are not request-scoped).
func NextSeq(requestID string) int64 {
	if requestID == "" {
		return 0
	}
	v, _ := seqCounters.LoadOrStore(requestID, &atomic.Int64{})
	return v.(*atomic.Int64).Add(1)
}

// pruneSeq drops the per-request counter after a terminal action so the
// sync.Map stays bounded by in-flight requests.
func pruneSeq(requestID string) {
	if requestID == "" {
		return
	}
	seqCounters.Delete(requestID)
}

// ResetSeqForTest clears the per-request seq table (tests only).
func ResetSeqForTest() {
	seqCounters.Range(func(k, _ any) bool {
		seqCounters.Delete(k)
		return true
	})
}

// ResetMetricsForTest zeros the three package-level metric atomics so a test
// can assert the value introduced by its own action (droppedTotal /
// redisFailureTot / stageNormFailureTot). Tests only — never call from
// production code.
func ResetMetricsForTest() {
	droppedTotal.Store(0)
	redisFailureTot.Store(0)
	stageNormFailureTot.Store(0)
}

// ── metrics ────────────────────────────────────────────────────────────────
//
// Flat package-level atomics surfaced through one custom Prometheus collector
// (matches the per-package metrics style; using atomics keeps the values
// readable from unit tests without the prometheus testutil dependency, which
// is not vendored in this repo).
var (
	droppedTotal        atomic.Uint64
	redisFailureTot     atomic.Uint64
	stageNormFailureTot atomic.Uint64
	metricsRegistered   sync.Once
)

// DroppedTotal returns how many events this process dropped because the emit
// buffer was full (channel 满即丢 + 计数, 24 号 §4).
func (e *Emitter) DroppedTotal() uint64 {
	if e == nil {
		return 0
	}
	return e.dropped.Load()
}

// RedisFailuresTotal returns how many Redis writes failed since process start
// (silent degradation: counted, never surfaced to the request path).
func (e *Emitter) RedisFailuresTotal() uint64 {
	if e == nil {
		return 0
	}
	return e.redisFailures.Load()
}

// StageNormalizationFailuresTotal returns how many ActionEvents fell out of
// the stageForAction mapping (catch-all for any future Action added without
// updating the switch). Should stay at zero in production.
func StageNormalizationFailuresTotal() uint64 {
	return stageNormFailureTot.Load()
}

type liveActionsCollector struct{}

func (liveActionsCollector) Describe(ch chan<- *prometheus.Desc) {
	prometheus.DescribeByCollect(liveActionsCollector{}, ch)
}

func (liveActionsCollector) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc("live_actions_dropped_total", "Action events dropped because the emit buffer was full.", nil, nil),
		prometheus.CounterValue, float64(droppedTotal.Load()))
	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc("live_actions_redis_failures_total", "Action-event Redis writes that failed (silent degradation).", nil, nil),
		prometheus.CounterValue, float64(redisFailureTot.Load()))
	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc("live_actions_stage_normalization_failures_total", "ActionEvents whose stageForAction mapping returned empty (should stay at zero; surfaces a missing switch arm).", nil, nil),
		prometheus.CounterValue, float64(stageNormFailureTot.Load()))
}

// ── emitter ────────────────────────────────────────────────────────────────

// Emitter is the async action-event emitter. Construct once at startup and
// inject (handler / executor / dispatch pipeline); a nil *Emitter is a valid
// no-op so un-wired deployments and existing tests keep working unchanged.
type Emitter struct {
	client Client
	ch     chan ActionEvent
	wg     sync.WaitGroup

	dropped       atomic.Uint64
	redisFailures atomic.Uint64
	closed        atomic.Bool
	closeOnce     sync.Once
	sendMu        sync.Mutex
}

// NewEmitter builds an emitter writing to the bounded Redis action queue.
// rdb may be nil (events are still emitted to the buffer but the worker
// no-ops the write — keeps emit sites identical across environments).
// bufSize <= 0 falls back to DefaultBufferSize.
func NewEmitter(rdb Client, bufSize int) *Emitter {
	if bufSize <= 0 {
		bufSize = DefaultBufferSize
	}
	e := &Emitter{
		client: rdb,
		ch:     make(chan ActionEvent, bufSize),
	}
	metricsRegistered.Do(func() {
		prometheus.MustRegister(liveActionsCollector{})
	})
	e.wg.Add(1)
	go e.run()
	return e
}

// Emit enqueues one action event. Nil-receiver safe (no-op), non-blocking
// (select/default drop + count). seq is allocated per request_id at emit
// time; Ts defaults to now(UTC).
func (e *Emitter) Emit(_ context.Context, ev ActionEvent) {
	if e == nil || ev.Action == "" {
		return
	}
	// state_change 是节点维度事件（24 号 §2），允许无 request_id；其余动作
	// 没有主键就没有时间线归属，直接丢弃。
	if ev.RequestID == "" && ev.Action != ActionStateChange {
		return
	}
	if ev.Seq == 0 {
		ev.Seq = NextSeq(ev.RequestID)
	}
	if ev.Ts.IsZero() {
		ev.Ts = time.Now().UTC()
	}
	normalizeStage(&ev)
	e.sendMu.Lock()
	defer e.sendMu.Unlock()
	if e.closed.Load() {
		// Post-Close emit (shutdown race): count as dropped instead of
		// panicking on the closed channel.
		e.dropped.Add(1)
		droppedTotal.Add(1)
		return
	}
	select {
	case e.ch <- ev:
	default:
		// 满即丢 + 计数（23 号 §2 原则 5：旁路异步，队列满即丢）。
		e.dropped.Add(1)
		droppedTotal.Add(1)
	}
	// Terminal actions: release the per-request seq counter so the map stays
	// bounded by in-flight requests. reply/no_route 之后同一 request_id 不会再
	// 有动作事件。
	if ev.Action == ActionReply || ev.Action == ActionNoRoute {
		pruneSeq(ev.RequestID)
	}
}

// Close stops the worker after draining the buffer. Idempotent.
func (e *Emitter) Close() {
	if e == nil {
		return
	}
	e.closeOnce.Do(func() {
		e.sendMu.Lock()
		e.closed.Store(true)
		close(e.ch)
		e.sendMu.Unlock()
	})
	e.wg.Wait()
}

// run is the single worker goroutine draining the buffer into Redis.
// Detached context: the request ctx may already be cancelled by the time the
// event is written; observability side-channels must not die with the
// request.
func (e *Emitter) run() {
	defer e.wg.Done()
	for ev := range e.ch {
		e.write(ev)
	}
}

func (e *Emitter) write(ev ActionEvent) {
	if e.client == nil {
		return
	}
	data, err := json.Marshal(ev)
	if err != nil {
		e.redisFailures.Add(1)
		redisFailureTot.Add(1)
		slog.Debug("liveactions: marshal failed", "action", ev.Action, "request_id", ev.RequestID, "err", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), redisWriteTimeout)
	defer cancel()
	pipe := e.client.Pipeline()
	pipe.LPush(ctx, RedisKey, string(data))
	// Keep the newest RedisMaxLen entries at the head (0..redisMaxLen-1).
	pipe.LTrim(ctx, RedisKey, 0, redisMaxLen-1)
	if _, err := pipe.Exec(ctx); err != nil {
		// 静默降级：计数 + debug 日志，不阻塞、不上抛（Redis 不可用时请求路径零感知）。
		e.redisFailures.Add(1)
		redisFailureTot.Add(1)
		slog.Debug("liveactions: redis write failed", "action", ev.Action, "request_id", ev.RequestID, "err", err.Error())
	}
}
