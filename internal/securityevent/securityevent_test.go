package securityevent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/memory"
	memclient "github.com/kaixuan/llm-gateway-go/domains/memory/client"
	"github.com/kaixuan/llm-gateway-go/internal/anomaly"
	memv2 "github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/memora"
)

// ---- 契约 ----

func TestEventIDIsStableAndScopeBound(t *testing.T) {
	a := EventID("tenant-a", "security.anomaly_detected.v1", "k1")
	b := EventID("tenant-a", "security.anomaly_detected.v1", "k1")
	c := EventID("tenant-b", "security.anomaly_detected.v1", "k1")
	d := EventID("tenant-a", "security.anomaly_detected.v1", "k2")
	if a != b {
		t.Errorf("same input produced different ids: %q vs %q", a, b)
	}
	if a == c {
		t.Error("different tenants must not share an event id")
	}
	if a == d {
		t.Error("different business keys must not share an event id")
	}
	if !strings.HasPrefix(a, "evt-security-") {
		t.Errorf("event id should follow the outbox naming convention: %q", a)
	}
}

func TestValidateRejectsIncompleteEvents(t *testing.T) {
	cases := []struct {
		name  string
		event Event
		want  string
	}{
		{"no event id", Event{EventType: "t", TenantID: "x", Code: "c", Severity: SeverityInfo, Action: ActionObserved}, "event_id"},
		{"no tenant", Event{EventID: "e", EventType: "t", Code: "c", Severity: SeverityInfo, Action: ActionObserved}, "tenant_id"},
		{"no code", Event{EventID: "e", EventType: "t", TenantID: "x", Severity: SeverityInfo, Action: ActionObserved}, "code"},
		{"bad severity", Event{EventID: "e", EventType: "t", TenantID: "x", Code: "c", Severity: "weird", Action: ActionObserved}, "severity"},
		{"bad action", Event{EventID: "e", EventType: "t", TenantID: "x", Code: "c", Severity: SeverityInfo, Action: "maybe"}, "action"},
	}
	for _, tc := range cases {
		err := tc.event.Validate()
		if err == nil {
			t.Errorf("%s: expected an error", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error should mention %q: %v", tc.name, tc.want, err)
		}
	}
}

// ---- 严重度归一化 ----

func TestNormalizeSeverity(t *testing.T) {
	cases := []struct {
		raw   string
		want  Severity
		known bool
	}{
		{"low", SeverityLow, true},
		{"MEDIUM", SeverityMedium, true},
		{"high", SeverityHigh, true},
		{"critical", SeverityCritical, true},
		{"warn", SeverityMedium, true},
		{"block", SeverityHigh, true},
		{"safe", SeverityInfo, true},
		{"pass", SeverityInfo, true},
		{"P0", SeverityCritical, true},
		{"P1", SeverityHigh, true},
		{"P2", SeverityMedium, true},
		{"rewrite", SeverityMedium, true},
		{"need_approval", SeverityMedium, true},
		// 数字刻度**不走**字符串规则（量纲不同，见 severity.go 的说明）。
		{"2", SeverityInfo, false},
		{"3", SeverityInfo, false},
		{"", SeverityInfo, false},
		{"nonsense", SeverityInfo, false},
	}
	for _, tc := range cases {
		got, known := NormalizeSeverity(tc.raw)
		if got != tc.want || known != tc.known {
			t.Errorf("NormalizeSeverity(%q) = (%q, %v), want (%q, %v)", tc.raw, got, known, tc.want, tc.known)
		}
	}
}

func TestSeverityFromGovernanceLevel(t *testing.T) {
	cases := map[int]Severity{0: SeverityInfo, 1: SeverityMedium, 2: SeverityHigh, 3: SeverityCritical}
	for level, want := range cases {
		if got := SeverityFromGovernanceLevel(level); got != want {
			t.Errorf("SeverityFromGovernanceLevel(%d) = %q, want %q", level, got, want)
		}
	}
}

func TestSeverityFromScore(t *testing.T) {
	cases := []struct {
		score float64
		want  Severity
	}{
		{0, SeverityInfo},
		{1, SeverityLow},
		{3.9, SeverityLow},
		{4, SeverityMedium},
		{6.9, SeverityMedium},
		{7, SeverityHigh},
		{8.9, SeverityHigh},
		{9, SeverityCritical},
		{10, SeverityCritical},
		{-5, SeverityInfo},
		{99, SeverityCritical},
	}
	for _, tc := range cases {
		if got := SeverityFromScore(tc.score); got != tc.want {
			t.Errorf("SeverityFromScore(%v) = %q, want %q", tc.score, got, tc.want)
		}
	}
}

// ---- 适配器 ----

func TestFromAnomalyKeepsTheRawScale(t *testing.T) {
	result := anomaly.AnomalyResult{
		Code:     "attachment_replay_loop",
		Severity: "high",
		Message:  "同一附件被重复发送",
		Metrics:  map[string]interface{}{"count": 3},
	}
	scope := Scope{TenantID: "tenant-a", SessionID: "sess-1", RequestID: "req-1", APIKeyID: 7}
	event := FromAnomaly(result, scope, ActionBlocked, time.Unix(1700000000, 0))

	if event.EventType != EventTypeAnomalyDetected {
		t.Errorf("event_type = %q", event.EventType)
	}
	if event.Source != SourceAnomaly {
		t.Errorf("source = %q", event.Source)
	}
	if event.Code != "attachment_replay_loop" {
		t.Errorf("code = %q", event.Code)
	}
	if event.Severity != SeverityHigh {
		t.Errorf("severity = %q", event.Severity)
	}
	if event.Action != ActionBlocked {
		t.Errorf("action = %q", event.Action)
	}
	if event.TenantID != "tenant-a" || event.SessionID != "sess-1" || event.APIKeyID != 7 {
		t.Errorf("scope not carried: %+v", event)
	}
	if event.Metadata["raw_severity"] != "high" {
		t.Errorf("raw severity must be preserved: %v", event.Metadata["raw_severity"])
	}
	if event.Metadata["count"] != 3 {
		t.Errorf("detector metrics must be preserved: %v", event.Metadata)
	}
	if err := event.Validate(); err != nil {
		t.Errorf("the adapted event must be valid: %v", err)
	}
}

func TestFromAnomalyUnknownSeverityDoesNotBecomeInfo(t *testing.T) {
	result := anomaly.AnomalyResult{Code: "x", Severity: "???", Message: "m"}
	event := FromAnomaly(result, Scope{TenantID: "t"}, ActionFlagged, time.Now())
	if event.Severity == SeverityInfo {
		t.Error("an unknown severity must not be downgraded to info")
	}
	if event.Metadata["raw_severity"] != "???" {
		t.Errorf("the raw severity must stay visible: %v", event.Metadata["raw_severity"])
	}
}

// ---- sink ----

// fakeWriter 记录写出的事件，并可按需失败。
type fakeWriter struct {
	mu     sync.Mutex
	events []Event
	fail   bool
}

func (f *fakeWriter) WriteSecurityEvent(_ context.Context, event Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errFake
	}
	f.events = append(f.events, event)
	return nil
}

var errFake = errFakeType{}

type errFakeType struct{}

func (errFakeType) Error() string { return "fake write failure" }

func TestSinkWritesTheEvent(t *testing.T) {
	writer := &fakeWriter{}
	sink := NewSink(SinkConfig{Writer: writer, Workers: 1, QueueSize: 4})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink.Start(ctx)

	event := NewEvent(EventTypeAnomalyDetected, SourceAnomaly, Scope{TenantID: "t"}, "code-1", SeverityHigh, ActionBlocked, "boom", "k1", time.Now(), nil)
	if !sink.Publish(event) {
		t.Fatal("publish should be accepted")
	}
	waitFor(t, func() bool { return sink.Stats().Processed == 1 })

	writer.mu.Lock()
	defer writer.mu.Unlock()
	if len(writer.events) != 1 {
		t.Fatalf("written events = %d, want 1", len(writer.events))
	}
	if writer.events[0].Code != "code-1" {
		t.Errorf("unexpected event: %+v", writer.events[0])
	}
}

func TestSinkNeverBlocksWhenTheQueueIsFull(t *testing.T) {
	// writer 阻塞 ⇒ worker 卡在第一条 ⇒ 队列很快填满。
	blocked := make(chan struct{})
	writer := &blockingWriter{release: blocked}
	sink := NewSink(SinkConfig{Writer: writer, Workers: 1, QueueSize: 1})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink.Start(ctx)

	event := NewEvent(EventTypeAnomalyDetected, SourceAnomaly, Scope{TenantID: "t"}, "c", SeverityLow, ActionFlagged, "m", "k", time.Now(), nil)
	accepted := 0
	for i := 0; i < 20; i++ {
		if sink.Publish(event) {
			accepted++
		}
	}
	close(blocked)
	if accepted >= 20 {
		t.Error("a full queue must drop events instead of accepting them all")
	}
	if sink.Stats().Dropped == 0 {
		t.Error("dropped events must be counted")
	}
}

type blockingWriter struct {
	release chan struct{}
}

func (b *blockingWriter) WriteSecurityEvent(ctx context.Context, _ Event) error {
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestSinkRejectsInvalidEvents(t *testing.T) {
	writer := &fakeWriter{}
	sink := NewSink(SinkConfig{Writer: writer, Workers: 1})
	if sink.Publish(Event{EventID: "e", EventType: "t", Code: "c", Severity: SeverityInfo, Action: ActionObserved}) {
		t.Error("an event without a tenant must be rejected")
	}
	if sink.Stats().Rejected != 1 {
		t.Errorf("rejected counter = %d, want 1", sink.Stats().Rejected)
	}
}

func TestSinkCountsWriteFailures(t *testing.T) {
	writer := &fakeWriter{fail: true}
	sink := NewSink(SinkConfig{Writer: writer, Workers: 1, QueueSize: 2, ErrorBackoff: time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink.Start(ctx)

	event := NewEvent(EventTypeAnomalyDetected, SourceAnomaly, Scope{TenantID: "t"}, "c", SeverityHigh, ActionBlocked, "m", "k", time.Now(), nil)
	sink.Publish(event)
	waitFor(t, func() bool { return sink.Stats().Errored == 1 })
	if sink.Stats().Processed != 0 {
		t.Error("a failed write must not be counted as processed")
	}
}

func TestSinkWithoutWriterIsDisabled(t *testing.T) {
	sink := NewSink(SinkConfig{})
	if sink.Enabled() {
		t.Error("a sink without a writer must report itself disabled")
	}
	if sink.Publish(Event{EventID: "e"}) {
		t.Error("a disabled sink must not accept events")
	}
}

// ---- memora 落地面 ----

func TestMemoraLegacyWriterSendsTheScopedPayload(t *testing.T) {
	var got struct {
		body    map[string]any
		headers http.Header
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got.headers = r.Header.Clone()
		_ = json.Unmarshal(raw, &got.body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	client := memclient.NewClient(memclient.ClientConfig{
		BaseURL:   srv.URL,
		APIKey:    "key-1",
		ProjectID: "proj-default",
	})
	writer := NewMemoraLegacyWriter(client, "fallback-user")

	event := NewEvent(EventTypeAnomalyDetected, SourceAnomaly, Scope{
		TenantID:   "tenant-a",
		ProjectID:  "proj-a",
		SessionID:  "sess-1",
		RequestID:  "req-1",
		InstanceID: "inst-1",
		APIKeyID:   7,
	}, "attachment_replay_loop", SeverityHigh, ActionBlocked, "同一附件被重复发送", "k1", time.Now(), map[string]any{"count": 3})

	if err := writer.WriteSecurityEvent(context.Background(), event); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got.headers.Get("X-Project-ID") != "proj-a" {
		t.Errorf("X-Project-ID = %q, want proj-a (the event scope wins over the client default)", got.headers.Get("X-Project-ID"))
	}
	if got.headers.Get("X-API-Key") != "key-1" {
		t.Errorf("X-API-Key = %q", got.headers.Get("X-API-Key"))
	}
	messages, _ := got.body["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages = %v", got.body["messages"])
	}
	msg, _ := messages[0].(map[string]any)
	content, _ := msg["content"].(string)
	if !strings.Contains(content, "[Security][high][blocked] attachment_replay_loop") {
		t.Errorf("content = %q", content)
	}
	if !strings.Contains(content, "session=sess-1") {
		t.Errorf("content should carry the locator: %q", content)
	}
	info, _ := got.body["info"].(map[string]any)
	if info["code"] != "attachment_replay_loop" {
		t.Errorf("info.code = %v", info["code"])
	}
	if info["severity"] != "high" || info["action"] != "blocked" {
		t.Errorf("info severity/action = %v/%v", info["severity"], info["action"])
	}
	if info["tenant_id"] != "tenant-a" {
		t.Errorf("info.tenant_id = %v", info["tenant_id"])
	}
	if info["meta_count"] != float64(3) {
		t.Errorf("detector metrics must be preserved under meta_*: %v", info)
	}
	if got.body["user_id"] != "tenant-a" {
		t.Errorf("user_id = %v, want the tenant", got.body["user_id"])
	}
}

func TestMemoraLegacyWriterWithoutTenantUsesTheDefaultUser(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	client := memclient.NewClient(memclient.ClientConfig{BaseURL: srv.URL, APIKey: "k"})
	writer := NewMemoraLegacyWriter(client, "fallback-user")
	event := NewEvent(EventTypeAnomalyDetected, SourceAnomaly, Scope{ProjectID: "p"}, "c", SeverityLow, ActionObserved, "m", "k", time.Now(), nil)

	if err := writer.WriteSecurityEvent(context.Background(), event); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got["user_id"] != "fallback-user" {
		t.Errorf("user_id = %v, want fallback-user", got["user_id"])
	}
}

func TestMemoraLegacyWriterWithoutAnyUserFailsLoudly(t *testing.T) {
	client := memclient.NewClient(memclient.ClientConfig{BaseURL: "http://127.0.0.1:1", APIKey: "k"})
	writer := NewMemoraLegacyWriter(client, "")
	event := NewEvent(EventTypeAnomalyDetected, SourceAnomaly, Scope{ProjectID: "p"}, "c", SeverityLow, ActionObserved, "m", "k", time.Now(), nil)

	// memora 的 user_id 为空会**静默跳过**（AddMessage 直接 return nil）⇒
	// 那样会变成"写成功但什么都没写"。这里必须报错。
	err := writer.WriteSecurityEvent(context.Background(), event)
	if err == nil {
		t.Fatal("a write without any user id must fail loudly, not silently skip")
	}
}

func TestMemoraLegacyWriterWithoutClientFails(t *testing.T) {
	writer := NewMemoraLegacyWriter(nil, "u")
	event := NewEvent(EventTypeAnomalyDetected, SourceAnomaly, Scope{TenantID: "t"}, "c", SeverityLow, ActionObserved, "m", "k", time.Now(), nil)
	if err := writer.WriteSecurityEvent(context.Background(), event); err == nil {
		t.Fatal("a writer without a client must fail, not panic")
	}
}

func TestFormatEventIsReadable(t *testing.T) {
	event := NewEvent(EventTypeAnomalyDetected, SourceAnomaly, Scope{
		TenantID:  "t",
		SessionID: "s",
	}, "code-1", SeverityMedium, ActionFlagged, "出问题了", "k", time.Now(), nil)
	got := FormatEvent(event)
	want := "[Security][medium][flagged] code-1: 出问题了 (session=s)"
	if got != want {
		t.Errorf("FormatEvent = %q, want %q", got, want)
	}
}

// ---- 工具 ----

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within the deadline")
}

var _ = memory.Message{}

// ---- v2 typed ingest 落地面 ----

func TestMemoraV2WriterSendsTheTypedIngest(t *testing.T) {
	var got struct {
		body    map[string]any
		headers http.Header
		path    string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got.headers = r.Header.Clone()
		got.path = r.URL.Path
		_ = json.Unmarshal(raw, &got.body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"accepted":1,"duplicates":0,"failed":[]}}`))
	}))
	defer srv.Close()

	client, err := memv2.New(memv2.Config{
		BaseURL:   srv.URL,
		JWTSecret: strings.Repeat("s", 40),
		Issuer:    memv2.DefaultIssuer,
		Audience:  memv2.DefaultAudience,
		TenantID:  "default",
		ActorID:   "local-ops",
		ProjectID: "proj-a",
	})
	if err != nil {
		t.Fatalf("memv2.New: %v", err)
	}
	writer := NewMemoraV2Writer(client)

	event := NewEvent(EventTypeAnomalyDetected, SourceAnomaly, Scope{
		TenantID:   "tenant-a",
		SessionID:  "sess-1",
		RequestID:  "req-1",
		InstanceID: "inst-1",
	}, "attachment_replay_loop", SeverityHigh, ActionBlocked, "同一附件被重复发送", "k1", time.Now(), map[string]any{"count": 3})

	if err := writer.WriteSecurityEvent(context.Background(), event); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got.path != "/api/v2/memories/ingest" {
		t.Errorf("path = %q", got.path)
	}
	if got.headers.Get("Idempotency-Key") == "" {
		t.Error("Idempotency-Key header is missing")
	}
	if got.headers.Get("X-Correlation-ID") == "" {
		t.Error("X-Correlation-ID header is missing")
	}
	if auth := got.headers.Get("Authorization"); !strings.HasPrefix(auth, "Bearer ") {
		t.Errorf("Authorization = %q", auth)
	}
	if got.headers.Get("X-Project-ID") != "proj-a" {
		t.Errorf("X-Project-ID = %q, want proj-a", got.headers.Get("X-Project-ID"))
	}
	if got.body["source_system"] != SourceSystemGateway {
		t.Errorf("source_system = %v", got.body["source_system"])
	}
	items, _ := got.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v", got.body["items"])
	}
	item, _ := items[0].(map[string]any)
	if item["idempotency_key"] != event.EventID {
		t.Errorf("item idempotency_key = %v, want the event id %q", item["idempotency_key"], event.EventID)
	}
	if item["source_type"] != SourceTypeSessionSummary {
		t.Errorf("source_type = %v", item["source_type"])
	}
	scope, _ := item["scope_chain"].(map[string]any)
	if scope["project_id"] != "proj-a" {
		t.Errorf("scope_chain.project_id = %v", scope["project_id"])
	}
	preview, _ := item["content_preview"].(string)
	if !strings.Contains(preview, "attachment_replay_loop") {
		t.Errorf("content_preview = %q", preview)
	}
	metadata, _ := item["metadata"].(map[string]any)
	if metadata["tenant_id"] != "tenant-a" {
		t.Errorf("metadata.tenant_id = %v", metadata["tenant_id"])
	}
	if metadata["raw_count"] != float64(3) {
		t.Errorf("detector metrics must be preserved: %v", metadata)
	}
	tags, _ := item["tags"].([]any)
	joined := fmt.Sprint(tags)
	if !strings.Contains(joined, "severity:high") || !strings.Contains(joined, "tenant:tenant-a") {
		t.Errorf("tags = %v", tags)
	}
}

func TestMemoraV2WriterTreatsRejectionAsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"accepted":0,"duplicates":0,"failed":["project_forbidden"]}}`))
	}))
	defer srv.Close()

	client, err := memv2.New(memv2.Config{
		BaseURL:   srv.URL,
		JWTSecret: strings.Repeat("s", 40),
		ProjectID: "proj-a",
	})
	if err != nil {
		t.Fatalf("memv2.New: %v", err)
	}
	event := NewEvent(EventTypeAnomalyDetected, SourceAnomaly, Scope{TenantID: "t"}, "c", SeverityHigh, ActionBlocked, "m", "k", time.Now(), nil)

	err = NewMemoraV2Writer(client).WriteSecurityEvent(context.Background(), event)
	if err == nil {
		t.Fatal("a rejected ingest must not look like a success")
	}
	if !strings.Contains(err.Error(), "project_forbidden") {
		t.Errorf("error should carry the rejection reason: %v", err)
	}
}

func TestMemoraV2WriterWithoutClientFails(t *testing.T) {
	event := NewEvent(EventTypeAnomalyDetected, SourceAnomaly, Scope{TenantID: "t"}, "c", SeverityLow, ActionObserved, "m", "k", time.Now(), nil)
	if err := NewMemoraV2Writer(nil).WriteSecurityEvent(context.Background(), event); err == nil {
		t.Fatal("a writer without a client must fail, not panic")
	}
}
