// Package admin — probe_stream_sse.go
//
// 自检（probe）队列 SSE 流，镜像"实时请求流"的展示形态（大/小两种、新数据从
// 右侧加入、SSE + Redis 队列打通），但 envelope 为自检领域专用。
//
// 端点：GET /api/admin/probe/stream
//
// 为什么不复用 LiveStreamSSEHub：LiveStreamRedisStore.Record 结构性强绑定
// LiveRequest（vendor/provider/model 维度、liveRequestRedisPayload），共用
// keyspace 会让自检 tile 与业务请求 tile 混在同一泳道。本文件参照
// SystemMonitorSSEHub（admin/systemmonitor_stream_sse.go）的物理隔离先例：
// 独立 Redis Pub/Sub 通道（llmgw:probe:events）、独立 envelope、独立 keyspace。
//
// 大/小形态：
//   - initial_data / snapshot_refresh —— 全量快照（大全量形态）
//   - submitted / started / completed / failed / idle_marker —— 单条增量（小形态），
//     前端按 ts DESC 从右侧追加新 tile。
package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/redis/go-redis/v9"
)

// ProbeStreamTask is the unified probe-lifecycle event payload emitted by the
// probe workers (node_probe / integrity / selfcheck) and carried in SSE
// envelopes. One taskID maps to one (credential, model, run) lifecycle; status
// transitions update the same taskID in place so the dashboard can collapse
// pending → in-flight → ok/fail into a single tile.
//
// ID conventions (audit-enforced 2026-08-12):
//   - node_probe: buildNodeProbeTaskID(credID, model)  → "node_probe:<cred>:<model>"
//   - integrity:  task.DedupKey (preserved across retries)
//   - selfcheck:  "selfcheck:<credID>:<unixnano>"  (daily run, distinct per run)
//
// The high-cardinality telemetry request_id (request_logs row) is unrelated
// to this SSE key — it stays distinct and is recorded in auto_decision.
type ProbeStreamTask struct {
	ID           string  `json:"id"`        // stable lifecycle id (see conventions above)
	TaskType     string  `json:"task_type"` // node_probe | integrity_verify | selfcheck
	Source       string  `json:"source"`    // node_probe | integrity | selfcheck
	Status       string  `json:"status"`    // pending | in-flight | ok | fail
	CredentialID int64   `json:"credential_id"`
	ProviderID   int64   `json:"provider_id,omitempty"`
	ProviderCode string  `json:"provider_code,omitempty"`
	RawModel     string  `json:"raw_model,omitempty"`
	Attempt      int     `json:"attempt,omitempty"`
	LatencyMs    *int    `json:"latency_ms,omitempty"`
	HTTPStatus   *int    `json:"http_status,omitempty"`
	ErrCode      string  `json:"err_code,omitempty"`
	ErrDetail    string  `json:"err_detail,omitempty"`
	Scheduled    bool    `json:"scheduled,omitempty"` // 定时自检（不进待请求队列）
	Reason       string  `json:"reason,omitempty"`    // 触发原因（request_failure / no_candidates / periodic ...）
	Timestamp    int64   `json:"ts"`                  // unix milliseconds
	Flusher      func()  `json:"-"`
	_            [0]int8 // disallow unkeyed struct literals
}

// TsUnixMilli returns the event timestamp, defaulting to now.
func (t ProbeStreamTask) TsUnixMilli() int64 {
	if t.Timestamp > 0 {
		return t.Timestamp
	}
	return time.Now().UnixMilli()
}

// ProbeStreamEnvelope is the SSE wire type.
type ProbeStreamEnvelope struct {
	Type    string            `json:"type"` // initial_data|submitted|started|completed|failed|idle_marker|snapshot_refresh
	Ts      time.Time         `json:"ts"`
	Task    *ProbeStreamTask  `json:"task,omitempty"`
	Initial []ProbeStreamTask `json:"initial,omitempty"`  // initial_data full replay
	LaneIDs []string          `json:"lane_ids,omitempty"` // idle_marker lane hints
}

// eventTypeForStatus maps a task status to the small-form SSE event type.
func eventTypeForStatus(status string) string {
	switch status {
	case "pending":
		return "submitted"
	case "in-flight":
		return "started"
	case "ok":
		return "completed"
	case "fail":
		return "failed"
	default:
		return status
	}
}

// ProbeSSEHub fans out Redis Pub/Sub probe events to dashboard clients.
// Modelled on SystemMonitorSSEHub (simple fan-out, per-client channel,
// keepalive) — see admin/systemmonitor_stream_sse.go.
type ProbeSSEHub struct {
	rdb   *redis.Client
	store *ProbeRedisStore
	// instanceID tags every Publish() so the local Redis subscriber
	// (run loop) can drop events that originated from this same hub
	// instead of double-fanning-out to connected SSE clients.
	// 2026-08-12 audit fix: without this gate the hub delivered every
	// transition twice — once from Publish()'s local fanOut, once from
	// the Redis pub/sub round-trip back to its own subscriber.
	instanceID string

	mu        sync.Mutex
	clients   map[chan ProbeStreamEnvelope]struct{}
	broadcast chan ProbeStreamEnvelope

	stop     chan struct{}
	stopOnce sync.Once
}

// NewProbeSSEHub constructs the hub. rdb may be nil — the hub then runs in
// in-memory-only mode (Record is a no-op, no cross-instance fan-out), which is
// useful for tests and single-instance dev setups.
func NewProbeSSEHub(rdb *redis.Client) *ProbeSSEHub {
	store := NewProbeRedisStore(rdb)
	hub := &ProbeSSEHub{
		rdb:        rdb,
		store:      store,
		instanceID: newProbeInstanceID(),
		clients:    map[chan ProbeStreamEnvelope]struct{}{},
		broadcast:  make(chan ProbeStreamEnvelope, 256),
		stop:       make(chan struct{}),
	}
	if rdb != nil {
		go hub.run()
	}
	return hub
}

// newProbeInstanceID returns a per-process tag embedded in notify messages so
// the hub's own subscriber can skip them. Encoded as 8 hex chars from crypto
// rand to avoid collisions across restarts.
func newProbeInstanceID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("p-%d", os.Getpid())
	}
	return fmt.Sprintf("p-%s", hex.EncodeToString(b[:]))
}

// shouldSkipNotification reports whether a Pub/Sub payload originated from
// this hub and should therefore be dropped (the local fanOut already
// delivered it). Exposed for unit tests.
//
// 2026-08-12 audit: before this filter existed, every probe transition was
// delivered to SSE clients twice on every gateway instance (once from
// Publish's local fanOut, once from the Redis pub/sub round-trip echoing
// back to its own subscriber).
func (h *ProbeSSEHub) shouldSkipNotification(payload string) bool {
	if h == nil || h.instanceID == "" {
		return false
	}
	var n struct {
		TaskID     string `json:"task_id"`
		InstanceID string `json:"instance_id"`
	}
	if err := json.Unmarshal([]byte(payload), &n); err != nil || n.TaskID == "" {
		return false
	}
	return n.InstanceID != "" && n.InstanceID == h.instanceID
}

// Enabled reports whether the hub is backed by Redis (cross-instance mode).
// When false, Publish still fans out locally so a single-instance gateway
// still gets live updates.
func (h *ProbeSSEHub) Enabled() bool { return h != nil && h.rdb != nil }

// run is the dispatcher: subscribes to the Redis notify channel and heartbeats.
func (h *ProbeSSEHub) run() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pubsub := h.rdb.Subscribe(ctx, probeNotifyChannel)
	defer func() { _ = pubsub.Close() }()
	ch := pubsub.Channel()

	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-h.stop:
			return
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if h.shouldSkipNotification(msg.Payload) {
				continue
			}
			var n struct {
				TaskID     string `json:"task_id"`
				InstanceID string `json:"instance_id"`
			}
			if json.Unmarshal([]byte(msg.Payload), &n) != nil || n.TaskID == "" {
				continue
			}
			lctx, lcancel := context.WithTimeout(ctx, 200*time.Millisecond)
			task, err := h.store.LoadDetail(lctx, n.TaskID)
			lcancel()
			if err != nil || task == nil {
				continue
			}
			h.fanOut(ProbeStreamEnvelope{
				Type: eventTypeForStatus(task.Status),
				Ts:   time.UnixMilli(task.TsUnixMilli()).UTC(),
				Task: task,
			})
		case <-heartbeat.C:
			h.fanOut(ProbeStreamEnvelope{Type: "idle_marker", Ts: time.Now().UTC()})
		}
	}
}

// fanOut delivers an envelope to every connected client; a full client buffer
// drops that client (browser auto-reconnects), matching LiveStreamSSEHub /
// SystemMonitorSSEHub backpressure policy.
//
// Concurrency: the send loop runs UNDER h.mu. This is essential because
// removeClient and Stop close client channels, and a send on a closed channel
// panics unconditionally (the select's default branch does NOT save it — the
// runtime checks closed-state before arbitrating cases). ProbeSSEHub has TWO
// concurrent fanOut callers — run() (Redis/heartbeat) and Publish() (bg
// workers) — so the close-during-send window is reachable. Evicted channels
// are deleted from the map under the lock and closed only after releasing it,
// which is safe because they are then invisible to every other fanOut snapshot
// and removeClient's map-membership guard refuses to double-close.
func (h *ProbeSSEHub) fanOut(env ProbeStreamEnvelope) {
	h.mu.Lock()
	var dead []chan ProbeStreamEnvelope
	for c := range h.clients {
		select {
		case c <- env:
		default:
			// Buffer full: drop this client. Remove from the map under the
			// lock so no later fanOut re-snapshots it; close after unlock.
			delete(h.clients, c)
			dead = append(dead, c)
		}
	}
	h.mu.Unlock()
	for _, c := range dead {
		close(c)
	}
}

func (h *ProbeSSEHub) removeClient(c chan ProbeStreamEnvelope) {
	h.mu.Lock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c)
	}
	h.mu.Unlock()
}

// Publish records a probe task transition to Redis and fans it out locally.
// Producers (bg workers via the ActiveProbeEmitter hook or direct calls) use
// this as the single entry point for every lifecycle transition.
func (h *ProbeSSEHub) Publish(task ProbeStreamTask) {
	if h == nil {
		return
	}
	if task.ID == "" {
		return
	}
	if h.store.Enabled() {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		_ = h.store.RecordWithOrigin(ctx, task, h.instanceID)
		cancel()
	}
	// Local fan-out so same-instance clients see the update immediately even
	// before the Redis pub/sub round-trip (and when Redis is disabled).
	h.fanOut(ProbeStreamEnvelope{
		Type: eventTypeForStatus(task.Status),
		Ts:   time.UnixMilli(task.TsUnixMilli()).UTC(),
		Task: &task,
	})
}

// PublishProbeEvent adapts a bg.ProbeStreamEvent (the bg-side projection that
// avoids a bg→admin import) into a ProbeStreamTask and publishes it. This
// method makes *ProbeSSEHub satisfy bg.ProbeEventSink so the ActiveProbeEmitter
// can publish completed/failed transitions without depending on admin.
func (h *ProbeSSEHub) PublishProbeEvent(evt bg.ProbeStreamEvent) {
	if h == nil || evt.ID == "" {
		return
	}
	h.Publish(ProbeStreamTask{
		ID:           evt.ID,
		TaskType:     evt.TaskType,
		Source:       evt.Source,
		Status:       evt.Status,
		CredentialID: evt.CredentialID,
		ProviderID:   evt.ProviderID,
		ProviderCode: evt.ProviderCode,
		RawModel:     evt.RawModel,
		Attempt:      evt.Attempt,
		LatencyMs:    evt.LatencyMs,
		HTTPStatus:   evt.HTTPStatus,
		ErrCode:      evt.ErrCode,
		ErrDetail:    evt.ErrDetail,
		Scheduled:    evt.Scheduled,
		Reason:       evt.Reason,
		Timestamp:    evt.TimestampMs,
	})
}

// HandleStream is the http.HandlerFunc mounted at GET /api/admin/probe/stream.
// SSE handshake mirrors SystemMonitorSSEHub.HandleStream.
func (h *ProbeSSEHub) HandleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	clientCh := make(chan ProbeStreamEnvelope, 64)
	h.mu.Lock()
	h.clients[clientCh] = struct{}{}
	h.mu.Unlock()
	defer h.removeClient(clientCh)

	if _, err := w.Write([]byte("retry: 3000\n\n")); err != nil {
		return
	}
	flusher.Flush()

	// initial_data: newest tiles from Redis (large form). When Redis is
	// disabled, send an empty initial envelope so the frontend renders an
	// empty state instead of hanging.
	if h.store.Enabled() {
		ictx, icancel := context.WithTimeout(r.Context(), 1*time.Second)
		tiles, _ := h.store.SnapshotFromDimQueues(ictx, 100)
		icancel()
		initial := make([]ProbeStreamTask, 0, len(tiles))
		for _, tile := range tiles {
			dctx, dcancel := context.WithTimeout(r.Context(), 100*time.Millisecond)
			full, _ := h.store.LoadDetail(dctx, tile.ID)
			dcancel()
			if full != nil {
				initial = append(initial, *full)
			}
		}
		writeProbeEnvelope(w, flusher, ProbeStreamEnvelope{
			Type:    "initial_data",
			Ts:      time.Now().UTC(),
			Initial: initial,
		})
	} else {
		writeProbeEnvelope(w, flusher, ProbeStreamEnvelope{
			Type: "initial_data", Ts: time.Now().UTC(),
		})
	}

	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()

	var writeMu sync.Mutex
	for {
		select {
		case <-r.Context().Done():
			return
		case <-h.stop:
			return
		case env, ok := <-clientCh:
			if !ok {
				return
			}
			writeMu.Lock()
			writeProbeEnvelopeLocked(w, flusher, &writeMu, env)
		case <-keepalive.C:
			writeMu.Lock()
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				writeMu.Unlock()
				return
			}
			writeMu.Unlock()
			flusher.Flush()
		}
	}
}

// writeProbeEnvelope serialises + writes one SSE event frame.
func writeProbeEnvelope(w http.ResponseWriter, flusher http.Flusher, env ProbeStreamEnvelope) {
	payload, err := json.Marshal(env)
	if err != nil {
		return
	}
	if _, err := w.Write([]byte("event: " + env.Type + "\n")); err != nil {
		return
	}
	if _, err := w.Write(append(payload, '\n')); err != nil {
		return
	}
	if _, err := w.Write([]byte("\n")); err != nil {
		return
	}
	flusher.Flush()
}

// writeProbeEnvelopeLocked is the per-client-mutex variant used inside the
// read loop. It unlocks on write failure so the caller can return.
func writeProbeEnvelopeLocked(w http.ResponseWriter, flusher http.Flusher, mu *sync.Mutex, env ProbeStreamEnvelope) {
	defer mu.Unlock()
	payload, err := json.Marshal(env)
	if err != nil {
		return
	}
	if _, err := w.Write([]byte("event: " + env.Type + "\n")); err != nil {
		return
	}
	if _, err := w.Write(append(payload, '\n')); err != nil {
		return
	}
	if _, err := w.Write([]byte("\n")); err != nil {
		return
	}
	flusher.Flush()
}

// Stop shuts down the hub.
func (h *ProbeSSEHub) Stop() {
	if h == nil {
		return
	}
	h.stopOnce.Do(func() { close(h.stop) })
	h.mu.Lock()
	for c := range h.clients {
		close(c)
		delete(h.clients, c)
	}
	h.mu.Unlock()
}

// logProbePublish is a thin helper for producers that want structured logging
// without importing slog directly.
func logProbePublish(task ProbeStreamTask) {
	slog.Info("probe_stream publish",
		"task_id", task.ID,
		"source", task.Source,
		"status", task.Status,
		"credential_id", task.CredentialID,
		"raw_model", task.RawModel,
	)
}
