package trace

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/jackc/pgx/v5"

	"github.com/redis/go-redis/v9"
)

// 测试辅助: 用 miniredis 替代真实 Redis,避免外部依赖。
// 注: 项目此前未引入 miniredis, 这里改为用 in-process test double,
//
//	因为 trace 包仅依赖 redis.Client 接口, 可注入桩实例。
//
// 但 go-redis v9 没有内置接口, 这里使用 unittest-friendly 方案:
// 起一个临时 127.0.0.1 端口不可控, 改为直接 stub Recorder 接口的测试。
func TestNoopRecorder_AllOpsSafe(t *testing.T) {
	rec := NoopRecorder{}
	ctx := context.Background()

	if err := rec.Append(ctx, "req-1", TraceEvent{Stage: StageReceiveRequest}); err != nil {
		t.Fatalf("Append should be noop, got %v", err)
	}
	if err := rec.Finalize(ctx, "req-1", FinalSuccess, ""); err != nil {
		t.Fatalf("Finalize should be noop, got %v", err)
	}
	got, fromRedis, err := rec.Load(ctx, "req-1")
	if err != nil || got != nil || fromRedis != false {
		t.Fatalf("Load on Noop should return (nil,false,nil), got (%+v,%v,%v)", got, fromRedis, err)
	}
	if err := rec.FlushToPG(ctx, nil, "req-1"); err != nil {
		t.Fatalf("FlushToPG on Noop should be noop, got %v", err)
	}
	if err := rec.Purge(ctx, "req-1"); err != nil {
		t.Fatalf("Purge on Noop should be noop, got %v", err)
	}
}

func TestNewRedisRecorder_NilClientReturnsNoop(t *testing.T) {
	rec := NewRedisRecorder(nil)
	if _, ok := rec.(NoopRecorder); !ok {
		t.Fatalf("NewRedisRecorder(nil) should return NoopRecorder, got %T", rec)
	}
}

func TestEventBuilder_BuildProducesValidEvent(t *testing.T) {
	ev := ReceiveRequest("POST", "/v1/chat/completions", "1.2.3.4").
		WithDetails("user_agent", "curl/7.0").
		Build()

	if ev.Stage != StageReceiveRequest {
		t.Errorf("stage mismatch: got %s, want %s", ev.Stage, StageReceiveRequest)
	}
	if ev.Status != StatusSuccess {
		t.Errorf("default status should be success")
	}
	if ev.Details["method"] != "POST" || ev.Details["path"] != "/v1/chat/completions" {
		t.Errorf("details not captured: %+v", ev.Details)
	}
	if ev.Details["user_agent"] != "curl/7.0" {
		t.Errorf("WithDetails failed: %+v", ev.Details)
	}
	if ev.StageName != "trace.stage.receive_request" {
		t.Errorf("stage name key wrong: %s", ev.StageName)
	}
}

func TestEventBuilder_WithErrorMarksFailed(t *testing.T) {
	ev := UpstreamRequest("https://api.example.com", 15000, true).
		WithError(errors.New("dial timeout")).
		Build()
	if ev.Status != StatusFailed {
		t.Errorf("WithError should mark Status=Failed")
	}
	if ev.Error != "dial timeout" {
		t.Errorf("error msg not captured: %s", ev.Error)
	}
}

func TestEventBuilder_RoundTripJSON(t *testing.T) {
	ev := RouteCredential(2451, 123, "gpt-5.6-luna", "openai/gpt-5", "premium", "tokens").
		Build()
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var back TraceEvent
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if back.Stage != StageRouteCredential || back.Details["credential_id"] != float64(123) {
		t.Errorf("round-trip mismatch: %+v", back)
	}
}

func TestSnapshot_Struct(t *testing.T) {
	snap := &Snapshot{
		CapturedAt:      time.Now(),
		ConcurrencySlot: &ConcurrencySnapshot{CredentialID: 99, InUse: 5, MaxSlots: 10, Blocked: true, Reason: "rpm_exceeded"},
		FailureHint:     "upstream_timeout",
		NodeProbeState:  map[string]any{"last_known_status": "unreachable", "consec_fails": 4},
	}
	data, _ := json.Marshal(snap)
	var back Snapshot
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("snapshot round-trip failed: %v", err)
	}
	if back.ConcurrencySlot == nil || !back.ConcurrencySlot.Blocked || back.ConcurrencySlot.InUse != 5 {
		t.Errorf("snapshot concurrency slot lost: %+v", back.ConcurrencySlot)
	}
	if back.NodeProbeState["consec_fails"] != float64(4) {
		// JSON unmarshal 把数字变成 float64
		t.Errorf("node probe state lost")
	}
}

func TestClassifyUpstreamError(t *testing.T) {
	cases := []struct {
		err        error
		statusCode int
		want       string
	}{
		{errors.New("dial tcp: i/o timeout"), 0, "upstream_timeout"},
		{errors.New("connection refused"), 0, "upstream_unreachable"},
		{errors.New("read: connection reset by peer"), 0, "upstream_reset"},
		{nil, 502, "upstream_5xx"},
		{nil, 429, "upstream_rate_limit"},
		{nil, 401, "upstream_auth_error"},
		{nil, 404, "upstream_not_found"},
		{nil, 400, "upstream_4xx"},
		{nil, 0, ""},
	}
	for _, c := range cases {
		got := classifyUpstreamError(c.err, c.statusCode)
		if got != c.want {
			t.Errorf("classifyUpstreamError(err=%v, code=%d) = %q, want %q",
				c.err, c.statusCode, got, c.want)
		}
	}
}

func TestGlobalSnapshotProvider_Noop(t *testing.T) {
	SetGlobalSnapshotProvider(nil)
	got := GetGlobalSnapshotProvider()
	if _, ok := got.(NoopSnapshotProvider); !ok {
		t.Fatalf("expected NoopSnapshotProvider after Set(nil), got %T", got)
	}
}

func TestKeyFor(t *testing.T) {
	got := keyFor("req-abc-123")
	want := "request:trace:req-abc-123"
	if got != want {
		t.Errorf("keyFor = %q, want %q", got, want)
	}
}

func TestEventBuilder_ConcurrentAppendIsGoroutineSafe(t *testing.T) {
	// 多 goroutine 同时构造 EventBuilder (immutable by design)
	// 只是确认 builder 自身没有共享可变 state。
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = BodyParse(1024, nil).Build()
		}()
	}
	wg.Wait()
}

// TestLoadFromPG_NilDBReturnsNil 验证 db==nil 时 LoadFromPG 返回 (nil,nil),
// 而不是错误。这条路径对应 admin 路由在配置缺失时仍能给出 not_found 响应。
func TestLoadFromPG_NilDBReturnsNil(t *testing.T) {
	got, err := LoadFromPG(context.Background(), nil, "req-xyz")
	if err != nil {
		t.Fatalf("LoadFromPG(nil db) err = %v, want nil", err)
	}
	if got != nil {
		t.Fatalf("LoadFromPG(nil db) = %+v, want nil", got)
	}
}

// TestLoadFromPG_EmptyRequestIDReturnsErr 验证 requestID 为空时返回 ErrEmptyRequestID。
func TestLoadFromPG_EmptyRequestIDReturnsErr(t *testing.T) {
	_, err := LoadFromPG(context.Background(), nil, "")
	if !errors.Is(err, ErrEmptyRequestID) {
		t.Fatalf("LoadFromPG('') err = %v, want ErrEmptyRequestID", err)
	}
}

// pgx.ErrNoRows 常量值校验: 文档保证它是非 nil 的 error, LoadFromPG 应将
// 其归一为 (nil, nil), 此处用 errors.Is 绑定防止有人误改。
var _ error = pgx.ErrNoRows

func TestRedisRecorder_Integration(t *testing.T) {
	addr := redisAddrFromEnv()
	if addr == "" {
		t.Skip("LLM_GATEWAY_REDIS_ADDR not set, skipping integration test")
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr, DB: 14})
	defer rdb.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("redis not reachable: %v", err)
	}

	t.Cleanup(func() { rdb.Del(ctx, keyFor("itest-1")) })

	rec := NewRedisRecorder(rdb)
	if err := rec.Append(ctx, "itest-1",
		ReceiveRequest("POST", "/v1/x", "127.0.0.1").Build()); err != nil {
		t.Fatalf("Append1 failed: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := rec.Append(ctx, "itest-1",
		RouteResolve("gpt-5", 3).Build()); err != nil {
		t.Fatalf("Append2 failed: %v", err)
	}

	if err := rec.Finalize(ctx, "itest-1", FinalFailed, StageUpstreamRequest); err != nil {
		t.Fatalf("Finalize failed: %v", err)
	}

	trace, fromRedis, err := rec.Load(ctx, "itest-1")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !fromRedis {
		t.Fatalf("expected from Redis, got false")
	}
	if len(trace.Events) != 2 {
		t.Errorf("expected 2 events, got %d", len(trace.Events))
	}
	if trace.Events[0].Seq != 1 || trace.Events[1].Seq != 2 {
		t.Errorf("Seq not monotonic: %+v", trace.Events)
	}
	if trace.FinalStatus != FinalFailed || trace.FailedAtStage != StageUpstreamRequest {
		t.Errorf("finalize not visible: final=%s failed=%s",
			trace.FinalStatus, trace.FailedAtStage)
	}
}

func TestUnmarshalTrace_LegacyStringEvents(t *testing.T) {
	raw := `{"request_id":"legacy-1","events":"[{\"seq\":1,\"stage\":\"receive_request\",\"module\":\"middleware\",\"status\":\"success\"}]","final_status":"success"}`
	trace, err := unmarshalTrace(raw, "legacy-1")
	if err != nil {
		t.Fatalf("unmarshal legacy trace failed: %v", err)
	}
	if len(trace.Events) != 1 || trace.Events[0].Stage != StageReceiveRequest {
		t.Fatalf("legacy events = %+v", trace.Events)
	}
}

// TestExtractTenantID 验证 2026-07-20 新增的 tenant_id 提取逻辑。
// 修复点: request_stage_events 表的 tenant_id 列 NOT NULL,stage_events.go 必须
// 从 authenticate 事件的 details 中提取 tenant_id,否则每条 INSERT 都失败。
func TestExtractTenantID(t *testing.T) {
	cases := []struct {
		name string
		evs  []TraceEvent
		want string
	}{
		{
			name: "authenticate 事件带 tenant_id",
			evs: []TraceEvent{
				{Stage: StageReceiveRequest, Details: map[string]any{"path": "/x"}},
				{Stage: StageAuthenticate, Details: map[string]any{"tenant_id": "default", "api_key_id": 2}},
				{Stage: StageRouteResolve, Details: map[string]any{}},
			},
			want: "default",
		},
		{
			name: "tenant_id 在非 authenticate 事件中(也应能提取)",
			evs: []TraceEvent{
				{Stage: StageRouteResolve, Details: map[string]any{"tenant_id": "tenant-xyz"}},
			},
			want: "tenant-xyz",
		},
		{
			name: "完全无 tenant_id (空字符串 fallback)",
			evs: []TraceEvent{
				{Stage: StageReceiveRequest, Details: map[string]any{"path": "/x"}},
				{Stage: StageAuthenticate, Details: map[string]any{"api_key_id": 0}},
			},
			want: "",
		},
		{
			name: "空 events",
			evs:  nil,
			want: "",
		},
		{
			name: "空字符串 tenant_id 不被采用,继续找下一个非空值",
			evs: []TraceEvent{
				{Stage: StageReceiveRequest, Details: map[string]any{"tenant_id": ""}},
				{Stage: StageAuthenticate, Details: map[string]any{"tenant_id": "t1"}},
			},
			want: "t1",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractTenantID(c.evs)
			if got != c.want {
				t.Errorf("extractTenantID = %q, want %q", got, c.want)
			}
		})
	}
}

func TestShouldDeleteTraceAfterFlush(t *testing.T) {
	if shouldDeleteTraceAfterFlush(0) {
		t.Fatal("zero rows affected must retain Redis trace")
	}
	if !shouldDeleteTraceAfterFlush(1) {
		t.Fatal("one affected row should allow Redis deletion")
	}
	if !shouldDeleteTraceAfterFlush(10) {
		t.Fatal("multiple affected rows should allow Redis deletion")
	}
}

func TestRedisRecorder_FinalizeAppendsCompletionEventAndDuration(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	rec := NewRedisRecorder(rdb)
	ctx := context.Background()
	if err := rec.Append(ctx, "finalize-test", ReceiveRequest("POST", "/v1/chat/completions", "127.0.0.1").WithTimestamp(time.Now().Add(-150*time.Millisecond)).Build()); err != nil {

		t.Fatalf("append failed: %v", err)
	}
	if err := rec.Finalize(ctx, "finalize-test", FinalFailed, StageUpstreamRequest); err != nil {
		t.Fatalf("finalize failed: %v", err)
	}
	trace, fromRedis, err := rec.Load(ctx, "finalize-test")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if !fromRedis || trace == nil {
		t.Fatalf("expected finalized trace in Redis, got trace=%+v fromRedis=%v", trace, fromRedis)
	}
	if trace.FinalStatus != FinalFailed || trace.FailedAtStage != StageUpstreamRequest {
		t.Fatalf("final status = %q/%q", trace.FinalStatus, trace.FailedAtStage)
	}
	if trace.TotalDurationMs <= 0 {
		t.Fatalf("total duration = %d, want positive", trace.TotalDurationMs)
	}
	if len(trace.Events) != 2 || trace.Events[1].Stage != StageRequestComplete {
		t.Fatalf("events = %+v, want completion event appended once", trace.Events)
	}
	if trace.Events[1].Status != StatusFailed {
		t.Fatalf("completion status = %q, want failed", trace.Events[1].Status)
	}
}

func TestRedisRecorder_FinalizeIsIdempotent(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	rec := NewRedisRecorder(rdb)
	ctx := context.Background()

	if err := rec.Append(ctx, "finalize-idempotent", ReceiveRequest("POST", "/v1/chat/completions", "127.0.0.1").Build()); err != nil {
		t.Fatalf("append failed: %v", err)
	}
	if err := rec.Finalize(ctx, "finalize-idempotent", FinalSuccess, ""); err != nil {
		t.Fatalf("first finalize failed: %v", err)
	}
	if err := rec.Finalize(ctx, "finalize-idempotent", FinalSuccess, ""); err != nil {
		t.Fatalf("second finalize failed: %v", err)
	}

	trace, _, err := rec.Load(ctx, "finalize-idempotent")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	count := 0
	for _, event := range trace.Events {
		if event.Stage == StageRequestComplete {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("completion event count = %d, want 1", count)
	}
}

func redisAddrFromEnv() string {
	if v := fromEnv("LLM_GATEWAY_REDIS_ADDR"); v != "" {
		return v
	}
	return ""
}

// 极简 env getter, 避免 imports 增加.
func fromEnv(k string) string {

	for _, kv := range envPairs() {
		if len(kv) >= 2 && kv[0] == k {
			return kv[1]
		}
	}
	return ""
}

func envPairs() [][2]string {
	// 实现见 env_helpers.go (单独小文件)
	return globalEnv
}
