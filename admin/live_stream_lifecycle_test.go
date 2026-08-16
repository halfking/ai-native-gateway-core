package admin

// OBS-BE2 (V3.3-OBS, 2026-08-15) contract tests: request_lifecycle /
// child_request envelopes, llmgw:live:actions replay/poll semantics and
// tenant isolation for action delivery (docs/会话优化v3/24号 §3/§4).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/internal/liveactions"
	"github.com/redis/go-redis/v9"
)

// newLifecycleTestHub builds a hub backed by miniredis plus one registered
// SSE client per requested audience.
func newLifecycleTestHub(t *testing.T) (*LiveStreamSSEHub, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	hub := NewLiveStreamSSEHub(nil, LiveStreamConfig{RedisClient: rdb})
	return hub, mr, rdb
}

func newLifecycleClient(tenantID string, isSuper bool) (*liveStreamClient, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	return &liveStreamClient{
		fl:       rec,
		w:        rec,
		tenantID: tenantID,
		isSuper:  isSuper,
	}, rec
}

// parseSSEDataFrames extracts the JSON payloads of all "data:" frames.
func parseSSEDataFrames(t *testing.T, body string) []map[string]any {
	t.Helper()
	var frames []map[string]any
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &m); err != nil {
			t.Fatalf("invalid SSE data frame %q: %v", line, err)
		}
		frames = append(frames, m)
	}
	return frames
}

func mustMarshalAction(t *testing.T, ev liveactions.ActionEvent) string {
	t.Helper()
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal action: %v", err)
	}
	return string(b)
}

// asActionArray normalises the envelope's "action" payload: one object for
// a single event, an array for an aggregated batch (both are on-contract).
func asActionArray(t *testing.T, v any) []map[string]any {
	t.Helper()
	switch payload := v.(type) {
	case []any:
		out := make([]map[string]any, 0, len(payload))
		for _, item := range payload {
			out = append(out, item.(map[string]any))
		}
		return out
	case map[string]any:
		return []map[string]any{payload}
	default:
		t.Fatalf("unexpected action payload shape %T (%#v)", v, v)
		return nil
	}
}

// ── wire contract ───────────────────────────────────────────────────────────

func TestRequestLifecycleEnvelope_SingleFlattensDetail(t *testing.T) {
	ts := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	ev := liveactions.ActionEvent{
		RequestID:    "req-1",
		Seq:          4,
		Action:       liveactions.ActionModelEnqueued,
		Ts:           ts,
		Model:        "glm-5.2",
		CredentialID: 7,
		Detail:       map[string]string{"queue_depth": "3"},
	}
	env := LiveStreamEnvelope{
		Type:      "request_lifecycle",
		Timestamp: ts,
		Action:    actionWirePayload([]liveactions.ActionEvent{ev}),
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	if !strings.Contains(s, `"type":"request_lifecycle"`) {
		t.Fatalf("missing type, payload %s", s)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	action, ok := m["action"].(map[string]any)
	if !ok {
		t.Fatalf("single event must serialise action as an object, got %T (%s)", m["action"], s)
	}
	for _, want := range []string{"request_id", "seq", "action", "ts", "model", "credential_id"} {
		if _, ok := action[want]; !ok {
			t.Fatalf("action missing %q, payload %s", want, s)
		}
	}
	// 24号 §2 detail 字段摊平到顶层，且不得残留 detail 包装。
	if v := action["queue_depth"]; v != "3" {
		t.Fatalf("queue_depth must be flattened to top level as \"3\", got %v (%s)", v, s)
	}
	if _, still := action["detail"]; still {
		t.Fatalf("detail wrapper must not remain on the wire, payload %s", s)
	}
}

func TestRequestLifecycleEnvelope_BatchUsesArray(t *testing.T) {
	ts := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	batch := []liveactions.ActionEvent{
		{RequestID: "req-1", Seq: 1, Action: liveactions.ActionArrive, Ts: ts},
		{RequestID: "req-1", Seq: 2, Action: liveactions.ActionRouteResolved, Ts: ts.Add(time.Millisecond)},
	}
	env := LiveStreamEnvelope{
		Type:      "request_lifecycle",
		Timestamp: ts,
		Action:    actionWirePayload(batch),
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	arr, ok := m["action"].([]any)
	if !ok || len(arr) != 2 {
		t.Fatalf("batch frame must carry a 2-element action array, got %#v", m["action"])
	}
}

func TestLiveStreamEnvelope_LegacyFramesOmitLifecycleKeys(t *testing.T) {
	latency := 120
	env := LiveStreamEnvelope{
		Type:      "request",
		Timestamp: time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC),
		Request:   &LiveRequest{RequestID: "req-1", Ts: "2026-08-15T12:00:00Z", TenantID: "tenant-a", Model: "m", Status: "success", LatencyMs: &latency},
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	for _, banned := range []string{`"action"`, `"parent_request_id"`} {
		if strings.Contains(s, banned) {
			t.Fatalf("request envelope must omit %s, payload %s", banned, s)
		}
	}
	if !strings.Contains(s, `"request_id":"req-1"`) {
		t.Fatalf("legacy snake_case keys must remain, payload %s", s)
	}
}

func TestLiveRequest_CamelCaseLifecycleAliases(t *testing.T) {
	// 无关联字段的请求：不得出现任何 alias 键（零值不冒充）。
	plain := LiveRequest{RequestID: "r", Ts: "2026-08-15T12:00:00Z", TenantID: "tenant-a", Model: "m"}
	b, err := json.Marshal(plain)
	if err != nil {
		t.Fatalf("marshal plain: %v", err)
	}
	for _, banned := range []string{`"parentRequestId"`, `"requestType"`, `"parent_request_id"`, `"request_type"`} {
		if strings.Contains(string(b), banned) {
			t.Fatalf("plain request must omit %s, payload %s", banned, b)
		}
	}

	// 带主从关联：snake_case 原键保留，camelCase alias 追加（24号 §3）。
	child := LiveRequest{
		RequestID:       "child-1",
		Ts:              "2026-08-15T12:00:00Z",
		TenantID:        "tenant-a",
		Model:           "m",
		ParentRequestID: "parent-1",
		RequestType:     "title",
	}
	b, err = json.Marshal(child)
	if err != nil {
		t.Fatalf("marshal child: %v", err)
	}
	s := string(b)
	for _, want := range []string{`"parent_request_id":"parent-1"`, `"request_type":"title"`, `"parentRequestId":"parent-1"`, `"requestType":"title"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("child request missing %s, payload %s", want, s)
		}
	}
}

func TestLiveStreamConfig_ActionPollIntervalWithinLatencyBudget(t *testing.T) {
	cfg := LiveStreamConfig{}
	cfg.defaults()
	// 27号 §3: SSE 聚合批量推送 ≤500ms。poll tick + 一个聚合窗 ≤ 2×interval。
	if cfg.ActionPollInterval <= 0 || 2*cfg.ActionPollInterval > 500*time.Millisecond {
		t.Fatalf("ActionPollInterval default %v violates the ≤500ms budget", cfg.ActionPollInterval)
	}
	if cfg.ActionReplayLimit != 200 {
		t.Fatalf("ActionReplayLimit default = %d, want 200 (24号 §4)", cfg.ActionReplayLimit)
	}
}

// ── initial replay ──────────────────────────────────────────────────────────

func TestReplayLifecycleActions_OrderCapAndTenantFilter(t *testing.T) {
	hub, _, rdb := newLifecycleTestHub(t)
	ctx := context.Background()

	base := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	// 250 个有效事件（两租户）+ 1 个 state_change（节点维度）+ 畸形条目。
	// LPUSH 从旧到新压入 → 列表头是最新。
	for i := 0; i < 120; i++ {
		ev := liveactions.ActionEvent{RequestID: fmt.Sprintf("a-%03d", i), Seq: 1, Action: liveactions.ActionArrive, Ts: base.Add(time.Duration(i) * time.Millisecond)}
		if i%2 == 0 {
			hub.rememberActionTenant(ev.RequestID, "tenant-a")
		}
		rdb.LPush(ctx, liveactions.RedisKey, mustMarshalAction(t, ev))
	}
	for i := 0; i < 130; i++ {
		ev := liveactions.ActionEvent{RequestID: fmt.Sprintf("b-%03d", i), Seq: 1, Action: liveactions.ActionReply, Ts: base.Add(time.Duration(1000+i) * time.Millisecond)}
		if i%3 == 0 {
			hub.rememberActionTenant(ev.RequestID, "tenant-b")
		}
		rdb.LPush(ctx, liveactions.RedisKey, mustMarshalAction(t, ev))
	}
	rdb.LPush(ctx, liveactions.RedisKey, mustMarshalAction(t, liveactions.ActionEvent{Action: liveactions.ActionStateChange, Ts: base.Add(2 * time.Second)}))
	rdb.LPush(ctx, liveactions.RedisKey, "{not-json")

	superClient, superRec := newLifecycleClient("", true)
	tenantClient, tenantRec := newLifecycleClient("tenant-a", false)

	hub.replayLifecycleActions(ctx, superClient)
	hub.replayLifecycleActions(ctx, tenantClient)

	superFrames := parseSSEDataFrames(t, superRec.Body.String())
	if len(superFrames) != 1 {
		t.Fatalf("super client expected exactly one replay frame, got %d", len(superFrames))
	}
	superActions := asActionArray(t, superFrames[0]["action"])
	// 252 entries pushed; LRANGE 0 199 的窗口内最新是畸形条目（解码跳过）、
	// 其次是 state_change（节点维度，按 24号 §2 排除）→ 198 条请求维度事件。
	if len(superActions) != 198 {
		t.Fatalf("super replay must cap at 200 decoded request-scoped entries, got %d", len(superActions))
	}
	// 排序校验：ts 升序（跨请求 seq 无全序，故先比 ts）。
	prev := time.Time{}
	for i, m := range superActions {
		ts, err := time.Parse(time.RFC3339Nano, m["ts"].(string))
		if err != nil {
			t.Fatalf("action %d ts: %v", i, err)
		}
		if prev.After(ts) {
			t.Fatalf("replay must be ordered ascending by ts, position %d went backwards", i)
		}
		prev = ts
	}

	tenantFrames := parseSSEDataFrames(t, tenantRec.Body.String())
	if len(tenantFrames) != 1 {
		t.Fatalf("tenant client expected exactly one replay frame, got %d", len(tenantFrames))
	}
	tenantActions := asActionArray(t, tenantFrames[0]["action"])
	for _, m := range tenantActions {
		rid, _ := m["request_id"].(string)
		if rid == "" {
			t.Fatalf("node-dimension state_change must be excluded from lifecycle replay, got %#v", m)
		}
		if owner, ok := hub.actionTenant(rid); !ok || owner != "tenant-a" {
			t.Fatalf("tenant-a replay leaked request %q owned by %q (known=%v)", rid, owner, ok)
		}
	}
	// LRANGE 0 199 窗口 = 畸形 + state_change + 130 条 b-* + 68 条最新 a-052..a-119。
	// 其中偶数编号（已登记 tenant-a）34 条；state_change 已被通道排除。
	if len(tenantActions) != 34 {
		t.Fatalf("tenant-a replay expected 34 visible actions, got %d", len(tenantActions))
	}
}

func TestReplayLifecycleActions_DropsUnattributedForTenants(t *testing.T) {
	hub, _, rdb := newLifecycleTestHub(t)
	ctx := context.Background()
	ts := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	// 未登记归属且 Redis 无 detail → 仅超管可见。
	rdb.LPush(ctx, liveactions.RedisKey, mustMarshalAction(t, liveactions.ActionEvent{RequestID: "ghost", Seq: 1, Action: liveactions.ActionArrive, Ts: ts}))

	superClient, superRec := newLifecycleClient("", true)
	tenantClient, tenantRec := newLifecycleClient("tenant-a", false)
	hub.replayLifecycleActions(ctx, superClient)
	hub.replayLifecycleActions(ctx, tenantClient)

	if len(parseSSEDataFrames(t, superRec.Body.String())) != 1 {
		t.Fatalf("super client must receive the unattributed action")
	}
	if tenantRec.Body.Len() != 0 {
		t.Fatalf("tenant client must not receive unattributed actions, got %q", tenantRec.Body.String())
	}
}

func TestReplayLifecycleActions_ResolvesOwnershipFromRedisDetail(t *testing.T) {
	hub, _, rdb := newLifecycleTestHub(t)
	ctx := context.Background()
	ts := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)

	// 先写请求 detail（全局键），再写动作：回放时应能解析归属 tenant-b。
	req := LiveRequest{RequestID: "late-1", Ts: ts.Format(time.RFC3339), TenantID: "tenant-b", Model: "m", ModelCategory: "v", ProviderCode: "p", Status: "success"}
	if err := hub.store.Record(ctx, req, ""); err != nil {
		t.Fatalf("record: %v", err)
	}
	rdb.LPush(ctx, liveactions.RedisKey, mustMarshalAction(t, liveactions.ActionEvent{RequestID: "late-1", Seq: 1, Action: liveactions.ActionReply, Ts: ts}))

	tenantClient, tenantRec := newLifecycleClient("tenant-b", false)
	otherClient, otherRec := newLifecycleClient("tenant-a", false)
	hub.replayLifecycleActions(ctx, tenantClient)
	hub.replayLifecycleActions(ctx, otherClient)

	if len(parseSSEDataFrames(t, tenantRec.Body.String())) != 1 {
		t.Fatalf("tenant-b must receive its action after Redis ownership resolution, body %q", tenantRec.Body.String())
	}
	if otherRec.Body.Len() != 0 {
		t.Fatalf("tenant-a must not receive tenant-b's action, got %q", otherRec.Body.String())
	}
}

// ── live polling ────────────────────────────────────────────────────────────

func TestPollLiveActions_FirstPollDeliversBacklogThenNewOnly(t *testing.T) {
	hub, _, rdb := newLifecycleTestHub(t)
	ctx := context.Background()
	ts := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)

	superClient, superRec := newLifecycleClient("", true)
	hub.clients[superClient] = struct{}{}
	t.Cleanup(func() { delete(hub.clients, superClient) })

	// 启动前已有积压：第一次 poll 以 at-least-once 口径投递积压
	//（与连接回放重复，前端按 (request_id, seq) 折叠），cursor 落到最新。
	for i := 0; i < 3; i++ {
		rdb.LPush(ctx, liveactions.RedisKey, mustMarshalAction(t, liveactions.ActionEvent{
			RequestID: fmt.Sprintf("old-%d", i), Seq: 1, Action: liveactions.ActionArrive, Ts: ts.Add(time.Duration(i) * time.Millisecond),
		}))
	}
	hub.pollLiveActions()
	backlog := parseSSEDataFrames(t, superRec.Body.String())
	if len(backlog) != 1 || len(asActionArray(t, backlog[0]["action"])) != 3 {
		t.Fatalf("first poll must deliver the scanned backlog at-least-once, frames %#v", backlog)
	}
	superRec.Body.Reset()

	// 新增 2 条（含 detail 摊平）→ 一次聚合帧（数组）推送。
	ev1 := liveactions.ActionEvent{RequestID: "new-1", Seq: 1, Action: liveactions.ActionFirstByte, Ts: ts.Add(time.Second), Detail: map[string]string{"ttfb_ms": "87"}}
	ev2 := liveactions.ActionEvent{RequestID: "new-1", Seq: 2, Action: liveactions.ActionReply, Ts: ts.Add(2 * time.Second), Retry: true, RetrySeq: 1}
	rdb.LPush(ctx, liveactions.RedisKey, mustMarshalAction(t, ev2))
	rdb.LPush(ctx, liveactions.RedisKey, mustMarshalAction(t, ev1))
	hub.pollLiveActions()

	frames := parseSSEDataFrames(t, superRec.Body.String())
	if len(frames) != 1 {
		t.Fatalf("expected exactly one aggregated frame, got %d", len(frames))
	}
	arr, ok := frames[0]["action"].([]any)
	if !ok || len(arr) != 2 {
		t.Fatalf("aggregated frame must carry both new actions, got %#v", frames[0]["action"])
	}
	first := arr[0].(map[string]any)
	if first["request_id"] != "new-1" || first["seq"].(float64) != 1 {
		t.Fatalf("frame must be ordered oldest → newest, got %#v", first)
	}
	if first["ttfb_ms"] != "87" {
		t.Fatalf("ttfb_ms must be flattened, got %#v", first)
	}
	second := arr[1].(map[string]any)
	if second["retry"] != true || second["retry_seq"].(float64) != 1 {
		t.Fatalf("retry fields must survive the wire, got %#v", second)
	}

	// 无新增 → 不推送。
	superRec.Body.Reset()
	hub.pollLiveActions()
	if superRec.Body.Len() != 0 {
		t.Fatalf("poll without new actions must not push, got %q", superRec.Body.String())
	}
}

func TestPollLiveActions_TenantIsolation(t *testing.T) {
	hub, _, rdb := newLifecycleTestHub(t)
	ctx := context.Background()
	ts := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)

	aClient, aRec := newLifecycleClient("tenant-a", false)
	bClient, bRec := newLifecycleClient("tenant-b", false)
	superClient, superRec := newLifecycleClient("", true)
	for _, c := range []*liveStreamClient{aClient, bClient, superClient} {
		hub.clients[c] = struct{}{}
	}
	t.Cleanup(func() {
		delete(hub.clients, aClient)
		delete(hub.clients, bClient)
		delete(hub.clients, superClient)
	})

	hub.rememberActionTenant("own-a", "tenant-a")
	// 首次 poll 投递 seed（at-least-once），cursor 落到最新；清空录制后再进入断言段。
	rdb.LPush(ctx, liveactions.RedisKey, mustMarshalAction(t, liveactions.ActionEvent{RequestID: "seed", Seq: 1, Action: liveactions.ActionArrive, Ts: ts}))
	hub.pollLiveActions()
	aRec.Body.Reset()
	bRec.Body.Reset()
	superRec.Body.Reset()

	rdb.LPush(ctx, liveactions.RedisKey, mustMarshalAction(t, liveactions.ActionEvent{RequestID: "own-a", Seq: 2, Action: liveactions.ActionReply, Ts: ts.Add(time.Second)}))
	rdb.LPush(ctx, liveactions.RedisKey, mustMarshalAction(t, liveactions.ActionEvent{RequestID: "unknown-owner", Seq: 1, Action: liveactions.ActionReply, Ts: ts.Add(2 * time.Second)}))
	hub.pollLiveActions()

	aActions := parseSSEDataFrames(t, aRec.Body.String())
	if len(aActions) != 1 {
		t.Fatalf("tenant-a expected one frame, got %d", len(aActions))
	}
	for _, m := range asActionArray(t, aActions[0]["action"]) {
		if m["request_id"] != "own-a" {
			t.Fatalf("tenant-a leaked a foreign action: %#v", m)
		}
	}
	if bRec.Body.Len() != 0 {
		t.Fatalf("tenant-b must receive nothing, got %q", bRec.Body.String())
	}
	super := parseSSEDataFrames(t, superRec.Body.String())
	if len(super) != 1 || len(asActionArray(t, super[0]["action"])) != 2 {
		t.Fatalf("super admin must receive both actions, frames %#v", super)
	}
}

func TestPollLiveActions_MalformedEntriesDoNotKillStream(t *testing.T) {
	hub, _, rdb := newLifecycleTestHub(t)
	ctx := context.Background()
	ts := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)

	superClient, superRec := newLifecycleClient("", true)
	hub.clients[superClient] = struct{}{}
	t.Cleanup(func() { delete(hub.clients, superClient) })

	rdb.LPush(ctx, liveactions.RedisKey, mustMarshalAction(t, liveactions.ActionEvent{RequestID: "seed", Seq: 1, Action: liveactions.ActionArrive, Ts: ts}))
	hub.pollLiveActions()
	superRec.Body.Reset()

	rdb.LPush(ctx, liveactions.RedisKey, "garbage")
	rdb.LPush(ctx, liveactions.RedisKey, mustMarshalAction(t, liveactions.ActionEvent{RequestID: "good", Seq: 1, Action: liveactions.ActionReply, Ts: ts.Add(time.Second)}))
	hub.pollLiveActions()

	frames := parseSSEDataFrames(t, superRec.Body.String())
	if len(frames) != 1 || len(asActionArray(t, frames[0]["action"])) != 1 {
		t.Fatalf("malformed entries must be skipped, frames %#v", frames)
	}
}

// TestPollLiveActions_BootAgainstEmptyListDeliversFirstEvent guards the
// cursor semantics: a hub whose poll sees an EMPTY list must deliver the
// first event appended afterwards — an empty cursor means "everything
// scanned is new", never a swallow window (2026-08-15 DV2 实测回归：
// hub 启动时列表为空，首个 arrive 曾被吞)。
func TestPollLiveActions_BootAgainstEmptyListDeliversFirstEvent(t *testing.T) {
	hub, _, rdb := newLifecycleTestHub(t)
	ctx := context.Background()
	ts := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)

	superClient, superRec := newLifecycleClient("", true)
	hub.clients[superClient] = struct{}{}
	t.Cleanup(func() { delete(hub.clients, superClient) })

	// 第一次 poll 命中空列表：布防完成，cursor 保持空。
	hub.pollLiveActions()
	if superRec.Body.Len() != 0 {
		t.Fatalf("poll on empty list must not deliver, got %q", superRec.Body.String())
	}

	// 布防后的第一个事件必须实时送达（不得被吞）。
	rdb.LPush(ctx, liveactions.RedisKey, mustMarshalAction(t, liveactions.ActionEvent{RequestID: "first-1", Seq: 1, Action: liveactions.ActionArrive, Ts: ts}))
	hub.pollLiveActions()

	frames := parseSSEDataFrames(t, superRec.Body.String())
	if len(frames) != 1 {
		t.Fatalf("first post-boot event must be delivered, frames %d", len(frames))
	}
	actions := asActionArray(t, frames[0]["action"])
	if len(actions) != 1 || actions[0]["request_id"] != "first-1" {
		t.Fatalf("expected first-1 arrive, got %#v", actions)
	}

	// 后续无新增 → 不推送。
	superRec.Body.Reset()
	hub.pollLiveActions()
	if superRec.Body.Len() != 0 {
		t.Fatalf("poll without new actions must not push, got %q", superRec.Body.String())
	}
}

func TestPollLiveActions_WithoutRedisIsNoop(t *testing.T) {
	hub := NewLiveStreamSSEHub(nil, LiveStreamConfig{})
	hub.pollLiveActions() // must not panic (RedisClient nil)
	hub.replayLifecycleActions(context.Background(), &liveStreamClient{})
}

// ── child_request ───────────────────────────────────────────────────────────

func TestFanOutChildRequest_ScopedDelivery(t *testing.T) {
	hub, _, _ := newLifecycleTestHub(t)
	ownerClient, ownerRec := newLifecycleClient("tenant-a", false)
	otherClient, otherRec := newLifecycleClient("tenant-b", false)
	superClient, superRec := newLifecycleClient("", true)
	for _, c := range []*liveStreamClient{ownerClient, otherClient, superClient} {
		hub.clients[c] = struct{}{}
	}
	t.Cleanup(func() {
		delete(hub.clients, ownerClient)
		delete(hub.clients, otherClient)
		delete(hub.clients, superClient)
	})

	hub.fanOutChildRequest(LiveRequest{
		RequestID:       "child-9",
		Ts:              "2026-08-15T12:00:00Z",
		TenantID:        "tenant-a",
		Model:           "glm-5.2",
		Status:          "success",
		ParentRequestID: "parent-9",
		RequestType:     "title",
	})

	for name, rec := range map[string]*httptest.ResponseRecorder{"owner": ownerRec, "super": superRec} {
		frames := parseSSEDataFrames(t, rec.Body.String())
		if len(frames) != 1 {
			t.Fatalf("%s expected one child_request frame, got %d (body %q)", name, len(frames), rec.Body.String())
		}
		if frames[0]["type"] != "child_request" {
			t.Fatalf("%s frame type = %#v", name, frames[0]["type"])
		}
		if frames[0]["parent_request_id"] != "parent-9" {
			t.Fatalf("%s parent_request_id missing, frame %#v", name, frames[0])
		}
		req := frames[0]["request"].(map[string]any)
		if req["requestType"] != "title" || req["parentRequestId"] != "parent-9" {
			t.Fatalf("%s child request aliases missing, got %#v", name, req)
		}
	}
	if otherRec.Body.Len() != 0 {
		t.Fatalf("tenant-b must not receive tenant-a's child_request, got %q", otherRec.Body.String())
	}
}

// ── Redis payload round trip / remote subscriber ────────────────────────────

func TestRedisPayload_ParentRequestMetadataRoundTrip(t *testing.T) {
	req := LiveRequest{
		RequestID:       "c-1",
		Ts:              "2026-08-15T12:00:00Z",
		TenantID:        "tenant-a",
		Model:           "m",
		ModelCategory:   "v",
		ProviderCode:    "p",
		Status:          "success",
		ParentRequestID: "p-1",
		RequestType:     "summary",
	}
	data, err := marshalLiveRequestRedisPayload(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := unmarshalLiveRequestRedisPayload(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.ParentRequestID != "p-1" || back.RequestType != "summary" {
		t.Fatalf("parent/type metadata lost in round trip: %#v", back)
	}
}

func TestRedisNotify_ReconstructsChildMetadata(t *testing.T) {
	hub, _, _ := newLifecycleTestHub(t)
	ctx := context.Background()
	child := LiveRequest{
		RequestID:       "child-sub",
		Ts:              "2026-08-15T12:00:00Z",
		TenantID:        "tenant-a",
		Model:           "m",
		ModelCategory:   "v",
		ProviderCode:    "p",
		Status:          "in_progress",
		ParentRequestID: "parent-sub",
		RequestType:     "title",
	}
	if err := hub.store.Record(ctx, child, ""); err != nil {
		t.Fatalf("record: %v", err)
	}
	payload, err := json.Marshal(liveStreamNotifyPayload{RequestID: child.RequestID, TenantID: child.TenantID})
	if err != nil {
		t.Fatalf("marshal notify: %v", err)
	}
	hub.handleRedisNotify(string(payload))

	select {
	case got := <-hub.broadcast:
		if got.ParentRequestID != "parent-sub" || got.RequestType != "title" {
			t.Fatalf("remote hub lost child metadata: %#v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for redis notify broadcast")
	}
}

// TestReplayLifecycleActions_ExcludesNodeDimensionEvents anchors the
// channel assignment: state_change rows stored in llmgw:live:actions are
// node-dimension (24号 §2) and must NOT ride request_lifecycle frames —
// neither for super admins nor in the live poll path.
func TestReplayLifecycleActions_ExcludesNodeDimensionEvents(t *testing.T) {
	hub, _, rdb := newLifecycleTestHub(t)
	ctx := context.Background()
	ts := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	rdb.LPush(ctx, liveactions.RedisKey, mustMarshalAction(t, liveactions.ActionEvent{
		Action: liveactions.ActionStateChange, CredentialID: 9, Ts: ts,
		Detail: map[string]string{"field": "circuit_state", "old": "closed", "new": "open"},
	}))
	rdb.LPush(ctx, liveactions.RedisKey, mustMarshalAction(t, liveactions.ActionEvent{RequestID: "r-1", Seq: 1, Action: liveactions.ActionArrive, Ts: ts.Add(time.Second)}))

	superClient, superRec := newLifecycleClient("", true)
	hub.replayLifecycleActions(ctx, superClient)

	frames := parseSSEDataFrames(t, superRec.Body.String())
	if len(frames) != 1 {
		t.Fatalf("expected one frame, got %d", len(frames))
	}
	for _, m := range asActionArray(t, frames[0]["action"]) {
		if m["request_id"] != "r-1" {
			t.Fatalf("state_change must be excluded from request_lifecycle, got %#v", m)
		}
	}
}

func TestReplayLifecycleActionsFor_OnlyReplaysVisibleSnapshotRequests(t *testing.T) {
	hub, _, rdb := newLifecycleTestHub(t)
	ctx := context.Background()
	ts := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	for _, ev := range []liveactions.ActionEvent{
		{RequestID: "visible", Seq: 1, Action: liveactions.ActionArrive, Ts: ts},
		{RequestID: "unrelated", Seq: 1, Action: liveactions.ActionArrive, Ts: ts.Add(time.Second)},
	} {
		if err := rdb.LPush(ctx, liveactions.RedisKey, mustMarshalAction(t, ev)).Err(); err != nil {
			t.Fatalf("push action: %v", err)
		}
	}

	client, rec := newLifecycleClient("", true)
	hub.replayLifecycleActionsFor(ctx, client, map[string]struct{}{"visible": {}})
	frames := parseSSEDataFrames(t, rec.Body.String())
	if len(frames) != 1 {
		t.Fatalf("expected one replay frame, got %d", len(frames))
	}
	actions := asActionArray(t, frames[0]["action"])
	if len(actions) != 1 || actions[0]["request_id"] != "visible" {
		t.Fatalf("replay must contain only visible snapshot actions, got %#v", actions)
	}
}

func TestSnapshotRequestIDs_PrefersDetailDimensionsAndFallsBack(t *testing.T) {
	detail := &LiveStreamSnapshot{
		DetailDimensions: map[string][]LiveStreamLane{
			"vendor": {{Requests: []LiveStreamTile{{RequestID: "detail-1"}, {RequestID: "detail-1"}}}},
		},
		Dimensions: map[string][]LiveStreamLane{
			"vendor": {{Requests: []LiveStreamTile{{RequestID: "fallback-ignored"}}}},
		},
	}
	ids := snapshotRequestIDs(detail)
	if len(ids) != 1 {
		t.Fatalf("detail IDs = %#v, want only deduplicated detail request", ids)
	}
	if _, ok := ids["detail-1"]; !ok {
		t.Fatalf("detail request missing: %#v", ids)
	}

	fallback := &LiveStreamSnapshot{Dimensions: map[string][]LiveStreamLane{
		"vendor": {{Requests: []LiveStreamTile{{RequestID: "fallback-1"}}}},
	}}
	ids = snapshotRequestIDs(fallback)
	if len(ids) != 1 {
		t.Fatalf("fallback IDs = %#v, want one request", ids)
	}
	if _, ok := ids["fallback-1"]; !ok {
		t.Fatalf("fallback request missing: %#v", ids)
	}
}

// TestLiveStreamEnvelope_LifecycleFieldSnapshot freezes the OBS-BE2 frame
// key sets: the exact top-level keys each new envelope type may carry.
// Adding a key requires a deliberate contract update (DV1).
func TestLiveStreamEnvelope_LifecycleFieldSnapshot(t *testing.T) {
	ts := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)

	lifecycle := LiveStreamEnvelope{
		Type:      "request_lifecycle",
		Timestamp: ts,
		Action:    actionWirePayload([]liveactions.ActionEvent{{RequestID: "r", Seq: 1, Action: liveactions.ActionArrive, Ts: ts}}),
	}
	b, err := json.Marshal(lifecycle)
	if err != nil {
		t.Fatalf("marshal lifecycle: %v", err)
	}
	assertJSONKeySet(t, b, "request_lifecycle", []string{"type", "ts", "action"})

	child := LiveStreamEnvelope{
		Type:            "child_request",
		Timestamp:       ts,
		Request:         &LiveRequest{RequestID: "c", Ts: ts.Format(time.RFC3339), TenantID: "t", Model: "m", Status: "success", ParentRequestID: "p", RequestType: "title"},
		ParentRequestID: "p",
	}
	b, err = json.Marshal(child)
	if err != nil {
		t.Fatalf("marshal child: %v", err)
	}
	assertJSONKeySet(t, b, "child_request", []string{"type", "ts", "request", "parent_request_id"})
}

func assertJSONKeySet(t *testing.T, data []byte, label string, want []string) {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("%s: unmarshal %s: %v", label, data, err)
	}
	if len(m) != len(want) {
		t.Fatalf("%s key set drift: got %d keys (%v), want %d (%v)", label, len(m), m, len(want), want)
	}
	for _, k := range want {
		if _, ok := m[k]; !ok {
			t.Fatalf("%s missing key %q, payload %s", label, k, data)
		}
	}
}

// TestNormalizeLiveRequestType anchors the persisted request_type → frozen
// wire enum mapping (24号 §3): chat|title|summary|sensitive_word|probe|unknown.
func TestNormalizeLiveRequestType(t *testing.T) {
	cases := map[string]string{
		"":                "",
		"main":            "",
		"title_gen":       "title",
		"title":           "title",
		"summary":         "summary",
		"sensitive_check": "sensitive_word",
		"sensitive_word":  "sensitive_word",
		"probe":           "probe",
		"compression":     "unknown",
		"mystery":         "unknown",
	}
	for in, want := range cases {
		if got := normalizeLiveRequestType(in); got != want {
			t.Errorf("normalizeLiveRequestType(%q) = %q, want %q", in, got, want)
		}
	}
}
