// Package admin — systemmonitor_stream_sse.go
//
// 系统监测模块 SSE 流（与 live-stream 物理隔离）。
// 设计依据: docs/会话优化v2/32-系统监测模块设计.md §5.1 / §5.2 / §5.3
//
// 端点：GET /api/admin/system-monitor/stream
//
// 数据源：Redis Pub/Sub llmgw:monitor:events
//
// 为什么 SSE 而非 WebSocket（沿用 design §1.2 #1 的判断）：
//   - SSE 共享 HTTP / cookie JWT 认证（rule 20 §6.1）
//   - EventSource 内建断线重连 + Last-Event-ID resume
//   - 仪表盘系统监测是只读视图，无需双向
package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// SystemMonitorSSEHub fans out Redis Pub/Sub events to dashboard clients.
//
// 与 LiveStreamSSEHub (admin/live_stream_sse.go) 不同：
//   - 不直接收 request_logs（那是 live-stream 的职责）
//   - 订阅 llmgw:monitor:events（系统监测自有通道）
//   - envelope 字段集更精简（详见 systemMonitorEvent）
type SystemMonitorSSEHub struct {
	rdb     *redis.Client
	workers int
	logger  *slog.Logger

	// client fan-out: per-client bounded broadcast + mutex-protected writes
	mu        sync.Mutex
	clients   map[chan systemMonitorEvent]struct{}
	broadcast chan systemMonitorEvent

	stop     chan struct{}
	stopOnce sync.Once
}

// NewSystemMonitorSSEHub constructs the hub and starts the dispatcher.
//
// rdb may be nil — the hub then refuses connections (503) so the frontend
// can detect the disabled state without reconnecting forever.
//
// workers = number of goroutines that pull from Redis. Default 2.
func NewSystemMonitorSSEHub(rdb *redis.Client) *SystemMonitorSSEHub {
	if rdb == nil {
		return &SystemMonitorSSEHub{
			clients:   map[chan systemMonitorEvent]struct{}{},
			broadcast: make(chan systemMonitorEvent, 256),
			stop:      make(chan struct{}),
		}
	}
	hub := &SystemMonitorSSEHub{
		rdb:       rdb,
		workers:   2,
		logger:    slog.Default(),
		clients:   map[chan systemMonitorEvent]struct{}{},
		broadcast: make(chan systemMonitorEvent, 256),
		stop:      make(chan struct{}),
	}
	go hub.run()
	return hub
}

// Enabled reports whether the hub has a live Redis backend.
//
// Used by handler.RegisterSystemMonitorRoutes to decide whether to mount
// the SSE endpoint.
func (h *SystemMonitorSSEHub) Enabled() bool {
	return h != nil && h.rdb != nil
}

// systemMonitorEvent is the SSE envelope schema (design §5.2).
type systemMonitorEvent struct {
	Type      string         `json:"type"` // submitted/claimed/started/completed/skipped/failed/queue_full/heartbeat
	Timestamp time.Time      `json:"ts"`
	Task      *smTaskSummary `json:"task,omitempty"`
	Stats     *smStats       `json:"stats,omitempty"`
	// 跳过场景专用
	SkipReason      string `json:"skip_reason,omitempty"`
	RecentRequestID string `json:"recent_request_id,omitempty"`
	// 事件额外信息（如心跳 host/worker_id）
	Host string `json:"host,omitempty"`
}

// smTaskSummary is the slim task projection emitted in SSE envelopes.
// Full state lives in Redis Hash task.HashKey() and PG system_probe_runs.
type smTaskSummary struct {
	ID           int64  `json:"id"`
	TaskType     string `json:"task_type"`
	Automaticity string `json:"automaticity"`
	Status       string `json:"status"`
	Attempt      int    `json:"attempt"`
	MaxAttempts  int    `json:"max_attempts"`
	CredentialID int64  `json:"credential_id"`
	ProviderID   int64  `json:"provider_id"`
	RawModel     string `json:"raw_model"`
	Source       string `json:"source"`
	WorkerID     string `json:"worker_id,omitempty"`
	HTTPStatus   *int   `json:"http_status,omitempty"`
	LatencyMs    *int   `json:"latency_ms,omitempty"`
	ErrCode      string `json:"err_code,omitempty"`
}

type smStats struct {
	QueueSize        int `json:"queue_size"`
	RunningSize      int `json:"running_size"`
	Concurrency      int `json:"monitor_concurrency"`
	SkippedTotal1h   int `json:"skipped_total_1h"`
	CompletedTotal1h int `json:"completed_total_1h"`
	FailedTotal1h    int `json:"failed_total_1h"`
}

// run starts the dispatcher goroutines. Called by NewSystemMonitorSSEHub
// when rdb != nil.
func (h *SystemMonitorSSEHub) run() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 启动 Redis 订阅
	pubsub := h.rdb.Subscribe(ctx, "llmgw:monitor:events")
	defer func() { _ = pubsub.Close() }()
	ch := pubsub.Channel()

	// 心跳 ticker：每 30s 发一个心跳给所有 client
	heartbeatTicker := time.NewTicker(30 * time.Second)
	defer heartbeatTicker.Stop()

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
			var evt systemMonitorEvent
			if err := json.Unmarshal([]byte(msg.Payload), &evt); err != nil {
				h.logger.Warn("system_monitor sse: bad event payload",
					"error", err, "payload", msg.Payload)
				continue
			}
			h.fanOut(evt)
		case <-heartbeatTicker.C:
			h.fanOut(systemMonitorEvent{
				Type:      "heartbeat",
				Timestamp: time.Now().UTC(),
				Stats:     &smStats{}, // Phase 2: fill from counters
			})
		}
	}
}

// fanOut delivers an event to every connected client without blocking.
//
// A slow client whose broadcast channel is full gets dropped (and reconnected
// via EventSource auto-reconnect) — matches design §5.1 backpressure.
func (h *SystemMonitorSSEHub) fanOut(evt systemMonitorEvent) {
	h.mu.Lock()
	clients := make([]chan systemMonitorEvent, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.Unlock()
	for _, c := range clients {
		select {
		case c <- evt:
		default:
			// Client buffer full; drop and disconnect (browser auto-reconnect).
			h.removeClient(c)
		}
	}
}

// removeClient deletes c from the registry and closes it.
func (h *SystemMonitorSSEHub) removeClient(c chan systemMonitorEvent) {
	h.mu.Lock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c)
	}
	h.mu.Unlock()
}

// HandleStream is the http.HandlerFunc mounted at GET /api/admin/system-monitor/stream.
//
// SSE handshake:
//   - Sets headers: Content-Type: text/event-stream, Cache-Control: no-cache, Connection: keep-alive
//   - Sends retry: 3000 (Last-Event-ID resume 3s window)
//   - Per-client mutex protects writes (matches LiveStreamSSEHub)
//
// Authentication is handled by the adminWrap middleware applied at route
// registration; this handler does NOT verify the session itself.
func (h *SystemMonitorSSEHub) HandleStream(w http.ResponseWriter, r *http.Request) {
	if !h.Enabled() {
		http.Error(w, "system monitor SSE not enabled (redis not wired)", http.StatusServiceUnavailable)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// client 信道：capacity = 64 (与 LiveStreamSSEHub 一致)
	clientCh := make(chan systemMonitorEvent, 64)
	h.mu.Lock()
	h.clients[clientCh] = struct{}{}
	h.mu.Unlock()

	defer h.removeClient(clientCh)

	// Initial retry hint (Last-Event-ID resume window)
	if _, err := w.Write([]byte("retry: 3000\n\n")); err != nil {
		return
	}
	flusher.Flush()

	// 心跳独立 loop（即使队列空也保持连接活跃）
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()

	var writeMu sync.Mutex // per-client write guard (design backpressure)
	for {
		select {
		case <-r.Context().Done():
			return
		case <-h.stop:
			return
		case evt, ok := <-clientCh:
			if !ok {
				return
			}
			payload, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			writeMu.Lock()
			if _, err := w.Write([]byte("event: " + evt.Type + "\n")); err != nil {
				writeMu.Unlock()
				return
			}
			if _, err := w.Write(append(payload, '\n')); err != nil {
				writeMu.Unlock()
				return
			}
			if _, err := w.Write([]byte("\n")); err != nil {
				writeMu.Unlock()
				return
			}
			writeMu.Unlock()
			flusher.Flush()
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

// Stop shuts down the hub. After Stop() the HandleStream endpoint must
// not be hit (main.go should unmount the route first).
func (h *SystemMonitorSSEHub) Stop() {
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

// Publish enqueues an event for fan-out. Used by SystemMonitor workers
// or REST handlers that need to push a state change without going through
// Redis Pub/Sub (e.g. tests, in-process triggers).
func (h *SystemMonitorSSEHub) Publish(evt systemMonitorEvent) {
	if !h.Enabled() {
		return
	}
	h.fanOut(evt)
}
