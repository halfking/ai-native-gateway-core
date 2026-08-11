// Package admin — freepool_stream_sse.go
//
// Free-pool quota SSE stream. Pushes real-time credential quota events
// (rate_limited / quota_exhausted / quota_permanent / recovered) to the
// FreePoolView admin page so the operator sees state changes instantly
// instead of waiting for the 15s poll.
//
// 端点：GET /api/free-pool/stream
//
// 为什么不复用 ProbeSSEHub / LiveStreamSSEHub：参照 probe_stream_sse.go 的
// 物理隔离先例——独立 envelope、独立 keyspace、独立 Redis Pub/Sub 通道，
// 避免免费池事件与自检/业务请求 tile 混在同一泳道。
//
// 设计：镜像 ProbeSSEHub 的简单 fan-out 模型（per-client channel + keepalive），
// 但不持久化事件（free-pool 事件是 fire-and-forget，重连时靠 /status 全量
// 重载即可，无需 Redis Store 回放）。Redis 仅用于跨实例 Pub/Sub。
package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/freeresource"
	"github.com/redis/go-redis/v9"
)

// freePoolNotifyChannel is the Redis Pub/Sub channel for cross-instance
// fan-out of free-pool quota events.
const freePoolNotifyChannel = "llmgw:freepool:events"

// FreePoolEnvelope is the SSE wire type. The frontend dispatches off Type
// (named SSE events) and triggers a lightweight /api/free-pool/status refresh.
type FreePoolEnvelope struct {
	Type         string     `json:"type"` // rate_limited|quota_exhausted|quota_permanent|recovered
	CredentialID int64      `json:"credential_id"`
	ProviderCode string     `json:"provider_code,omitempty"`
	ModelID      string     `json:"model_id,omitempty"`
	AutoResetAt  *time.Time `json:"auto_reset_at,omitempty"`
	Ts           time.Time  `json:"ts"`
}

// FreePoolSSEHub fans out free-pool quota events to dashboard clients.
// Modelled on ProbeSSEHub (simple fan-out, per-client channel, keepalive).
type FreePoolSSEHub struct {
	rdb *redis.Client
	// instanceID tags every Publish() so the local Redis subscriber (run loop)
	// can drop events that originated from this same hub instead of
	// double-fanning-out to connected SSE clients. Mirrors ProbeSSEHub —
	// without this gate Publish's local fanOut + the Redis pub/sub round-trip
	// echoing back to this hub's own subscriber would deliver every event
	// twice to same-instance clients.
	instanceID string

	mu        sync.Mutex
	clients   map[chan FreePoolEnvelope]struct{}
	broadcast chan FreePoolEnvelope

	stop     chan struct{}
	stopOnce sync.Once
}

// NewFreePoolSSEHub constructs the hub. rdb may be nil — the hub then runs in
// in-memory-only mode (no cross-instance fan-out), which is useful for tests
// and single-instance dev setups. Same-instance clients still get live updates
// via the local fanOut.
func NewFreePoolSSEHub(rdb *redis.Client) *FreePoolSSEHub {
	hub := &FreePoolSSEHub{
		rdb:        rdb,
		instanceID: newFreePoolInstanceID(),
		clients:    map[chan FreePoolEnvelope]struct{}{},
		broadcast:  make(chan FreePoolEnvelope, 256),
		stop:       make(chan struct{}),
	}
	if rdb != nil {
		go hub.run()
	}
	return hub
}

// newFreePoolInstanceID returns a per-process tag embedded in Redis notify
// payloads so the hub's own subscriber can skip them. Encoded as 8 hex chars
// from crypto rand to avoid collisions across restarts. Mirrors
// newProbeInstanceID.
func newFreePoolInstanceID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("f-%d", os.Getpid())
	}
	return fmt.Sprintf("f-%s", hex.EncodeToString(b[:]))
}

// shouldSkipNotification reports whether a Redis Pub/Sub payload originated
// from this hub and should therefore be dropped (the local fanOut in Publish
// already delivered it). Without this, same-instance clients see every event
// twice.
func (h *FreePoolSSEHub) shouldSkipNotification(payload string) bool {
	if h == nil || h.instanceID == "" {
		return false
	}
	var n struct {
		InstanceID string `json:"instance_id"`
	}
	if err := json.Unmarshal([]byte(payload), &n); err != nil || n.InstanceID == "" {
		return false
	}
	return n.InstanceID == h.instanceID
}

// Enabled reports whether the hub is backed by Redis (cross-instance mode).
func (h *FreePoolSSEHub) Enabled() bool { return h != nil && h.rdb != nil }

// run is the dispatcher: subscribes to the Redis notify channel and heartbeats.
func (h *FreePoolSSEHub) run() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pubsub := h.rdb.Subscribe(ctx, freePoolNotifyChannel)
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
			// Drop self-originated notifications — Publish() already fanned
			// them out locally. Without this gate same-instance clients get
			// each event twice.
			if h.shouldSkipNotification(msg.Payload) {
				continue
			}
			var env FreePoolEnvelope
			if json.Unmarshal([]byte(msg.Payload), &env) != nil || env.Type == "" {
				continue
			}
			h.fanOut(env)
		case <-heartbeat.C:
			h.fanOut(FreePoolEnvelope{Type: "heartbeat", Ts: time.Now().UTC()})
		}
	}
}

// fanOut delivers an envelope to every connected client; a full client buffer
// drops that client (browser auto-reconnects). Concurrency model mirrors
// ProbeSSEHub.fanOut — send under h.mu, close dead channels after unlock.
func (h *FreePoolSSEHub) fanOut(env FreePoolEnvelope) {
	h.mu.Lock()
	var dead []chan FreePoolEnvelope
	for c := range h.clients {
		select {
		case c <- env:
		default:
			delete(h.clients, c)
			dead = append(dead, c)
		}
	}
	h.mu.Unlock()
	for _, c := range dead {
		close(c)
	}
}

func (h *FreePoolSSEHub) removeClient(c chan FreePoolEnvelope) {
	h.mu.Lock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c)
	}
	h.mu.Unlock()
}

// Publish broadcasts a free-pool event to all connected clients (local fan-out
// + Redis pub/sub for cross-instance delivery).
func (h *FreePoolSSEHub) Publish(env FreePoolEnvelope) {
	if h == nil {
		return
	}
	if env.Ts.IsZero() {
		env.Ts = time.Now().UTC()
	}
	// Local fan-out first so same-instance clients see it immediately.
	h.fanOut(env)
	// Cross-instance fan-out via Redis (best-effort, 200ms timeout). The
	// payload is wrapped with this hub's instanceID so this hub's own
	// subscriber can drop the round-trip (local clients were already served).
	// Other instances' subscribers ignore the tag and fan out normally.
	if h.rdb != nil {
		notify := struct {
			FreePoolEnvelope
			InstanceID string `json:"instance_id"`
		}{env, h.instanceID}
		if payload, err := json.Marshal(notify); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			_ = h.rdb.Publish(ctx, freePoolNotifyChannel, payload).Err()
			cancel()
		}
	}
}

// PublishQuotaEvent adapts a freeresource.QuotaEvent (the freeresource-side
// projection that avoids a freeresource→admin import) into a FreePoolEnvelope
// and publishes it. This method makes *FreePoolSSEHub satisfy
// freeresource.QuotaEventSink so QuotaTracker can publish without depending
// on admin.
func (h *FreePoolSSEHub) PublishQuotaEvent(evt freeresource.QuotaEvent) {
	if h == nil || evt.Type == "" {
		return
	}
	h.Publish(FreePoolEnvelope{
		Type:         evt.Type,
		CredentialID: evt.CredentialID,
		ProviderCode: evt.ProviderCode,
		ModelID:      evt.ModelID,
		AutoResetAt:  evt.AutoResetAt,
		Ts:           evt.Ts,
	})
}

// HandleStream is the http.HandlerFunc mounted at GET /api/free-pool/stream.
// SSE handshake mirrors ProbeSSEHub.HandleStream.
func (h *FreePoolSSEHub) HandleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	clientCh := make(chan FreePoolEnvelope, 64)
	h.mu.Lock()
	h.clients[clientCh] = struct{}{}
	h.mu.Unlock()
	defer h.removeClient(clientCh)

	if _, err := w.Write([]byte("retry: 3000\n\n")); err != nil {
		return
	}
	flusher.Flush()

	// initial hello so the frontend knows the stream is alive; a full status
	// snapshot is fetched separately via /api/free-pool/status.
	writeFreePoolEnvelope(w, flusher, FreePoolEnvelope{
		Type: "initial_data",
		Ts:   time.Now().UTC(),
	})

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
			writeFreePoolEnvelopeLocked(w, flusher, &writeMu, env)
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

// writeFreePoolEnvelope serialises + writes one SSE event frame.
func writeFreePoolEnvelope(w http.ResponseWriter, flusher http.Flusher, env FreePoolEnvelope) {
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

// writeFreePoolEnvelopeLocked is the per-client-mutex variant used inside the
// read loop. It unlocks on write failure so the caller can return.
func writeFreePoolEnvelopeLocked(w http.ResponseWriter, flusher http.Flusher, mu *sync.Mutex, env FreePoolEnvelope) {
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
func (h *FreePoolSSEHub) Stop() {
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
