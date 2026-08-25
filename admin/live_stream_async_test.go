package admin

// 2026-08-25 (fix-154, 实时流解耦) 定向测试: SSE Hub 的慢操作外移契约。
//
// 覆盖 live_stream_async.go / live_stream_sse.go 的四条关键行为:
//
//  1. TestSlowLabelQueryDoesNotBlockRequestBroadcast — action worker 里
//     CredentialLabelsFor 的 500ms 慢查询不得阻塞普通 request 广播
//     (旧版把整条 poll 链放在主循环里, DB 抖动会卡住所有广播)。
//  2. TestActionWorkerDeliversInOrder — 手动 triggerActionPoll 两次,
//     两次 poll 的动作批次按顺序投递 (worker 串行消费 + 帧内 oldest→newest)。
//  3. TestStopJoinsAsyncWorkers / TestStopWithoutRunDoesNotHang — Stop 的
//     有界 join 语义: Run 过的 hub Stop 返回时两个 worker 的 done chan 已
//     closed; 从未 Run 的 hub Stop 直接返回不悬挂。
//  4. TestPublishBroadcastNotBlockedByRedis — Redis 持续无响应 (黑洞 TCP)
//     时 Publish 只做本地 enqueue, 不再同步等待 store.Record。
//
// 另有 TestCredentialLabelsQuerySeamCachesAndNegativeCaches 覆盖查询缝的
// 缓存/负缓存语义。

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/internal/liveactions"
	"github.com/redis/go-redis/v9"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// recordingResponseWriter is a mutex-guarded SSE sink. Unlike
// httptest.ResponseRecorder it is safe to read from the test goroutine while
// the hub's Run loop writes frames concurrently (writeEvent serialises on the
// client's writeMu, but the recorder itself has no internal locking).
type recordingResponseWriter struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	hdr   http.Header
	wrote chan struct{} // one token per Write; never blocks the producer
}

func newRecordingLifecycleClient(tenantID string, isSuper bool) (*liveStreamClient, *recordingResponseWriter) {
	w := &recordingResponseWriter{hdr: make(http.Header), wrote: make(chan struct{}, 64)}
	return &liveStreamClient{fl: w, w: w, tenantID: tenantID, isSuper: isSuper}, w
}

func (w *recordingResponseWriter) Header() http.Header { return w.hdr }
func (w *recordingResponseWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, err := w.buf.Write(p)
	w.mu.Unlock()
	select {
	case w.wrote <- struct{}{}:
	default:
	}
	return n, err
}
func (w *recordingResponseWriter) WriteHeader(int) {}
func (w *recordingResponseWriter) Flush()          {}

func (w *recordingResponseWriter) body() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// waitUntil polls cond every 2ms until it holds or the timeout expires.
// Used only for observable side effects (frames/flags); everything ordered
// is synchronised through channels or happens-before edges instead.
func waitUntil(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", desc)
}

// newDeadAddrPool returns a *pgxpool.Pool that never connects anywhere.
// CredentialLabelsFor only needs h.db != nil to reach the injected
// credentialLabelsQuery seam; the pool itself is never queried by these
// tests. pgxpool.New parses the config and maintains MinConns (default 0)
// lazily, so construction neither dials nor blocks.
func newDeadAddrPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://tester:tester@127.0.0.1:1/unused?sslmode=disable")
	if err != nil {
		t.Skipf("cannot construct offline pgxpool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// newAsyncTestHub builds a miniredis-backed hub with a non-nil (but dead) DB
// pool so the credentialLabelsQuery seam is reachable, and with the action
// poll ticker silenced — tests trigger polls manually via triggerActionPoll
// for full determinism.
func newAsyncTestHub(t *testing.T) (*LiveStreamSSEHub, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	hub := NewLiveStreamSSEHub(newDeadAddrPool(t), LiveStreamConfig{
		RedisClient:        rdb,
		ActionPollInterval: time.Hour,
	})
	return hub, mr, rdb
}

func waitAsyncWorkersStarted(t *testing.T, hub *LiveStreamSSEHub) {
	t.Helper()
	waitUntil(t, 2*time.Second, "async workers to start", func() bool {
		hub.asyncMu.Lock()
		defer hub.asyncMu.Unlock()
		return hub.actionWorkerStarted && hub.recordDrainerStarted
	})
}

// waitForRequestFrame blocks until a "request" frame carrying requestID is
// visible in the recorded SSE body.
func waitForRequestFrame(t *testing.T, w *recordingResponseWriter, requestID string) {
	t.Helper()
	waitUntil(t, 3*time.Second, fmt.Sprintf("request frame for %q", requestID), func() bool {
		for _, f := range parseSSEDataFrames(t, w.body()) {
			if f["type"] != "request" {
				continue
			}
			if req, ok := f["request"].(map[string]any); ok && req["request_id"] == requestID {
				return true
			}
		}
		return false
	})
}

// waitForLifecycleFrame blocks until a "request_lifecycle" frame containing an
// action for requestID is visible and match accepts its action array.
func waitForLifecycleFrame(t *testing.T, w *recordingResponseWriter, requestID string, match func(actions []map[string]any) bool) {
	t.Helper()
	waitUntil(t, 3*time.Second, fmt.Sprintf("lifecycle frame for %q", requestID), func() bool {
		for _, f := range parseSSEDataFrames(t, w.body()) {
			if f["type"] != "request_lifecycle" {
				continue
			}
			actions := asActionArray(t, f["action"])
			seen := false
			for _, m := range actions {
				if m["request_id"] == requestID {
					seen = true
					break
				}
			}
			if seen && match(actions) {
				return true
			}
		}
		return false
	})
}

// ── 1. slow label query must not block request broadcast ────────────────────

// TestSlowLabelQueryDoesNotBlockRequestBroadcast pins the core fix-154
// property: while the action worker is stuck inside CredentialLabelsFor
// (cold-cache DB stall), the main loop still delivers a normal "request"
// broadcast almost immediately. The slow query sleeps 500ms; delivery must
// complete well under that (threshold 450ms leaves CI headroom — actual
// latency is a few ms).
func TestSlowLabelQueryDoesNotBlockRequestBroadcast(t *testing.T) {
	hub, _, rdb := newAsyncTestHub(t)

	queryStarted := make(chan struct{}, 1)
	hub.credentialLabelsQuery = func(ctx context.Context, ids []int) (map[int]string, error) {
		select {
		case queryStarted <- struct{}{}:
		default:
		}
		time.Sleep(500 * time.Millisecond) // simulated cold-cache DB stall
		out := make(map[int]string, len(ids))
		for _, id := range ids {
			out[id] = "slow-label"
		}
		return out, nil
	}

	client, rec := newRecordingLifecycleClient("", true)
	// Registered before go Run(): goroutine start gives the happens-before
	// edge for this unlocked map write.
	hub.clients[client] = struct{}{}
	go hub.Run()
	defer hub.Stop()
	waitAsyncWorkersStarted(t, hub)

	// Give the action worker one batch whose label lookup enters the 500ms
	// stall, then wait until the stall has actually begun.
	ts := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	rdb.LPush(context.Background(), liveactions.RedisKey, mustMarshalAction(t, liveactions.ActionEvent{
		RequestID: "slow-req", Seq: 1, Action: liveactions.ActionCredentialSelected, Ts: ts, CredentialID: 42,
	}))
	hub.triggerActionPoll()
	select {
	case <-queryStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("credential label query never started — action worker did not pick up the trigger")
	}

	// While the worker is stalled, a normal request broadcast must still be
	// delivered to the SSE client without waiting for the query.
	start := time.Now()
	hub.Publish(LiveRequest{
		RequestID: "fast-req", Ts: ts.Format(time.RFC3339), TenantID: "tenant-a",
		Model: "m", ModelCategory: "v", ProviderCode: "p", Status: "in_progress",
	})
	waitForRequestFrame(t, rec, "fast-req")
	if elapsed := time.Since(start); elapsed >= 450*time.Millisecond {
		t.Fatalf("request broadcast blocked by slow label query: delivered in %v (budget 450ms, query stalls 500ms)", elapsed)
	}
}

// ── 2. action worker delivers polls in order ────────────────────────────────

// TestActionWorkerDeliversInOrder triggers two manual polls and asserts:
//   - each poll's actions arrive as one aggregated frame, oldest → newest;
//   - the label batch queries run once per poll, in poll order
//     ([[101 102]] then [[201]]) — proof the worker consumes triggers
//     serially rather than interleaving them.
func TestActionWorkerDeliversInOrder(t *testing.T) {
	hub, _, rdb := newAsyncTestHub(t)

	var mu sync.Mutex
	var queryCalls [][]int
	hub.credentialLabelsQuery = func(ctx context.Context, ids []int) (map[int]string, error) {
		cp := append([]int(nil), ids...)
		mu.Lock()
		queryCalls = append(queryCalls, cp)
		mu.Unlock()
		out := make(map[int]string, len(ids))
		for _, id := range ids {
			out[id] = fmt.Sprintf("cred-%d", id)
		}
		return out, nil
	}

	client, rec := newRecordingLifecycleClient("", true)
	hub.clients[client] = struct{}{}
	go hub.Run()
	defer hub.Stop()
	waitAsyncWorkersStarted(t, hub)

	ctx := context.Background()
	ts := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

	// Batch 1: three events for r1. LPUSH newest-last ⇒ list head is the
	// oldest entry, matching the oldest → newest delivery contract.
	rdb.LPush(ctx, liveactions.RedisKey, mustMarshalAction(t, liveactions.ActionEvent{
		RequestID: "r1", Seq: 3, Action: liveactions.ActionReply, Ts: ts.Add(2 * time.Millisecond), CredentialID: 101,
	}))
	rdb.LPush(ctx, liveactions.RedisKey, mustMarshalAction(t, liveactions.ActionEvent{
		RequestID: "r1", Seq: 2, Action: liveactions.ActionFirstByte, Ts: ts.Add(time.Millisecond), CredentialID: 102,
	}))
	rdb.LPush(ctx, liveactions.RedisKey, mustMarshalAction(t, liveactions.ActionEvent{
		RequestID: "r1", Seq: 1, Action: liveactions.ActionCredentialSelected, Ts: ts, CredentialID: 101,
	}))

	hub.triggerActionPoll()
	waitForLifecycleFrame(t, rec, "r1", func(actions []map[string]any) bool {
		if len(actions) != 3 {
			return false
		}
		return actions[0]["request_id"] == "r1" && actions[0]["seq"] == float64(1) && actions[0]["credential_label"] == "cred-101" &&
			actions[1]["seq"] == float64(2) && actions[1]["credential_label"] == "cred-102" &&
			actions[2]["seq"] == float64(3) && actions[2]["credential_label"] == "cred-101"
	})

	// Batch 2 (only after batch 1 was fully delivered): a second poll must
	// deliver only the new action, after the first frame.
	rdb.LPush(ctx, liveactions.RedisKey, mustMarshalAction(t, liveactions.ActionEvent{
		RequestID: "r2", Seq: 1, Action: liveactions.ActionCredentialSelected, Ts: ts.Add(time.Second), CredentialID: 201,
	}))
	hub.triggerActionPoll()
	waitForLifecycleFrame(t, rec, "r2", func(actions []map[string]any) bool {
		return len(actions) == 1 && actions[0]["request_id"] == "r2" && actions[0]["credential_label"] == "cred-201"
	})

	// Frame order in the recorded stream: r1's frame strictly before r2's.
	firstR1, firstR2 := -1, -1
	for i, f := range parseSSEDataFrames(t, rec.body()) {
		if f["type"] != "request_lifecycle" {
			continue
		}
		for _, m := range asActionArray(t, f["action"]) {
			switch m["request_id"] {
			case "r1":
				if firstR1 < 0 {
					firstR1 = i
				}
			case "r2":
				if firstR2 < 0 {
					firstR2 = i
				}
			}
		}
	}
	if firstR1 < 0 || firstR2 < 0 || firstR1 >= firstR2 {
		t.Fatalf("lifecycle frames out of order: r1 at %d, r2 at %d", firstR1, firstR2)
	}

	// The label seam saw exactly one batched query per poll, in poll order.
	mu.Lock()
	defer mu.Unlock()
	if len(queryCalls) != 2 {
		t.Fatalf("expected exactly 2 label batch queries (one per poll), got %v", queryCalls)
	}
	if fmt.Sprint(queryCalls[0]) != "[101 102]" || fmt.Sprint(queryCalls[1]) != "[201]" {
		t.Fatalf("label query batches out of order or wrongly batched: %v", queryCalls)
	}
}

// ── 3. Stop joins the async workers ─────────────────────────────────────────

// TestStopJoinsAsyncWorkers: after Run() started both workers, Stop() must
// return only once each worker's done channel is closed (bounded join).
func TestStopJoinsAsyncWorkers(t *testing.T) {
	hub, _, _ := newAsyncTestHub(t)
	go hub.Run()
	waitAsyncWorkersStarted(t, hub)

	hub.Stop()

	for name, done := range map[string]chan struct{}{
		"action worker":  hub.actionWorkerDone,
		"record drainer": hub.recordDrainerDone,
	} {
		if done == nil {
			t.Fatalf("%s done channel is nil after Run()+Stop()", name)
		}
		waitUntil(t, 200*time.Millisecond, name+" done channel closed", func() bool {
			select {
			case <-done:
				return true
			default:
				return false
			}
		})
	}
}

// TestStopWithoutRunDoesNotHang: Stop() on a hub whose Run() was never called
// joins nothing (done channels are nil) and must return promptly.
func TestStopWithoutRunDoesNotHang(t *testing.T) {
	hub, _, _ := newAsyncTestHub(t)
	done := make(chan struct{})
	go func() {
		hub.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() on a never-Run hub must return promptly (nil done channels are skipped by the join)")
	}
}

// ── 4. Publish is not blocked by a stalled Redis ────────────────────────────

// blackholeListener accepts TCP connections and never answers a byte. Every
// Redis command sent to it blocks until its context deadline, simulating a
// stalled Redis far more faithfully than a closed port (which fails fast).
func blackholeListener(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("blackhole listen: %v", err)
	}
	var connsMu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		ln.Close()
		connsMu.Lock()
		for _, c := range conns {
			c.Close()
		}
		connsMu.Unlock()
	})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			connsMu.Lock()
			conns = append(conns, conn)
			connsMu.Unlock()
			// Deliberately never read/write: hold the connection open.
		}
	}()
	return ln.Addr().String()
}

// TestPublishBroadcastNotBlockedByRedis: with Redis accepting connections but
// never answering, every store.Record stalls for its full 200ms write budget.
// 30 synchronous Records would need ≥6s; the decoupled Publish only performs
// non-blocking channel sends. The trailing assertions pin that the records
// were genuinely queued (drainer still behind) rather than dropped.
func TestPublishBroadcastNotBlockedByRedis(t *testing.T) {
	addr := blackholeListener(t)
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { rdb.Close() })

	hub := NewLiveStreamSSEHub(nil, LiveStreamConfig{
		RedisClient:        rdb,
		ActionPollInterval: time.Hour,
	})
	go hub.Run()
	defer hub.Stop()
	waitAsyncWorkersStarted(t, hub)

	const publishes = 30
	ts := time.Now().UTC().Format(time.RFC3339)
	start := time.Now()
	for i := 0; i < publishes; i++ {
		hub.Publish(LiveRequest{
			RequestID: fmt.Sprintf("stalled-redis-%d", i), Ts: ts, TenantID: "tenant-a",
			Model: "m", ModelCategory: "v", ProviderCode: "p", Status: "in_progress",
		})
	}
	elapsed := time.Since(start)
	// Sync baseline is ≥ publishes×200ms = 6s; 3s is a 2x cushion below it
	// while leaving ample room for scheduler noise on the async path (µs).
	if elapsed >= 3*time.Second {
		t.Fatalf("Publish calls were slowed by stalled Redis writes: %d publishes took %v", publishes, elapsed)
	}

	if got := hub.recordDropsCount(); got != 0 {
		t.Fatalf("record drops = %d, want 0 (queue cap %d not exceeded)", got, recordQueueCapacity)
	}
	// The drainer processes one stalled Record at a time (200ms each), so at
	// this point most records must still be pending in the queue — proving
	// Publish took the async path AND the blackhole really stalls writes
	// (a fail-fast Redis would leave the queue empty and the timing proof void).
	if queued := len(hub.recordQueue); queued == 0 {
		t.Fatal("record queue empty: the stalled-Redis setup did not exercise the async drainer path")
	}

	// The local broadcast queue received every frame without waiting for any
	// Redis write (the main loop may itself be slow on its delta read against
	// the blackhole — that is out of scope; Publish's contract is the enqueue).
	if queued := len(hub.broadcast); queued == 0 {
		t.Fatal("broadcast queue empty: Publish did not enqueue the local frame")
	}
}

// ── 5. credentialLabelsQuery seam: cache + negative cache ───────────────────

// TestCredentialLabelsQuerySeamCachesAndNegativeCaches covers the cache
// semantics that live in CredentialLabelsFor around the injectable query:
//   - found ids are cached and served without a second query;
//   - ids missing from the query result are negative-cached;
//   - a total query failure negative-caches the whole batch.
func TestCredentialLabelsQuerySeamCachesAndNegativeCaches(t *testing.T) {
	hub := NewLiveStreamSSEHub(newDeadAddrPool(t), LiveStreamConfig{})
	ctx := context.Background()

	var calls int
	hub.credentialLabelsQuery = func(ctx context.Context, ids []int) (map[int]string, error) {
		calls++
		return map[int]string{7: "seven"}, nil
	}

	got := hub.CredentialLabelsFor(ctx, []int{7, 8})
	if got[7] != "seven" {
		t.Fatalf("id 7 must resolve via the seam, got %v", got)
	}
	if _, ok := got[8]; ok {
		t.Fatalf("id missing from the query result must be omitted from the map, got %v", got)
	}
	again := hub.CredentialLabelsFor(ctx, []int{7, 8})
	if again[7] != "seven" {
		t.Fatalf("cached id 7 must resolve on the second call, got %v", again)
	}
	if calls != 1 {
		t.Fatalf("second call must be served from cache (incl. negative cache), queries = %d", calls)
	}

	failing := NewLiveStreamSSEHub(newDeadAddrPool(t), LiveStreamConfig{})
	var failCalls int
	failing.credentialLabelsQuery = func(ctx context.Context, ids []int) (map[int]string, error) {
		failCalls++
		return nil, errors.New("db down")
	}
	if out := failing.CredentialLabelsFor(ctx, []int{9}); out != nil {
		t.Fatalf("total query failure must yield nil, got %v", out)
	}
	if out := failing.CredentialLabelsFor(ctx, []int{9}); out != nil {
		t.Fatalf("second call after failure must yield nil, got %v", out)
	}
	if failCalls != 1 {
		t.Fatalf("failed batch must be negative-cached, queries = %d", failCalls)
	}
}
