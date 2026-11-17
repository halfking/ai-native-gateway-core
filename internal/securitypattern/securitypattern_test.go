package securitypattern

// M2-3 跨实例威胁模式的单元测试。
//
// 覆盖：聚合口径 / 快照语义（过期、整份替换）/ 读面契约（标签过滤、降级
// 即失败、metadata 还原）/ 同步循环的失败语义（保留旧快照）。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	memv2 "github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/memora"
	"github.com/kaixuan/llm-gateway-go/internal/securityevent"
)

// ---- 聚合口径 ----

func TestBuildPatterns_GroupsBySignature(t *testing.T) {
	observations := []Observation{
		{EventType: "security.threat_detected.v1", Code: "pii_leak", Severity: securityevent.SeverityMedium, InstanceID: "gw-a", TenantID: "t1", DetectedAt: time.Now()},
		{EventType: "security.threat_detected.v1", Code: "pii_leak", Severity: securityevent.SeverityHigh, InstanceID: "gw-b", TenantID: "t2", DetectedAt: time.Now()},
		{EventType: "security.threat_detected.v1", Code: "prompt_injection", Severity: securityevent.SeverityLow, InstanceID: "gw-a", TenantID: "t1", DetectedAt: time.Now()},
	}
	patterns := BuildPatterns(observations)
	if len(patterns) != 2 {
		t.Fatalf("patterns = %d, want 2", len(patterns))
	}
	pii := patterns[Signature("security.threat_detected.v1", "pii_leak")]
	if pii.Observations != 2 {
		t.Errorf("pii_leak observations = %d, want 2", pii.Observations)
	}
	if pii.InstanceCount != 2 {
		t.Errorf("pii_leak instance_count = %d, want 2 (gw-a + gw-b)", pii.InstanceCount)
	}
	if len(pii.Tenants) != 2 {
		t.Errorf("pii_leak tenants = %v, want 2 tenants", pii.Tenants)
	}
	// 严重度取**最高**，不是最后一条。
	if pii.Severity != securityevent.SeverityHigh {
		t.Errorf("pii_leak severity = %q, want high (the max, not the last one)", pii.Severity)
	}
}

// TestBuildPatterns_EmptySignatureIsSkipped 钉住"没有形状的观察不进聚合"。
func TestBuildPatterns_EmptySignatureIsSkipped(t *testing.T) {
	patterns := BuildPatterns([]Observation{{EventType: "", Code: ""}})
	if len(patterns) != 0 {
		t.Fatalf("patterns = %d, want 0 (an observation without a signature has no shape to group by)", len(patterns))
	}
}

// TestSignature_IsStableAcrossSessions 钉住签名**不含**每次都变的字段。
//
// 签名若掺入 session/request id ⇒ 每个事件都是"新模式"，聚合失去意义。
func TestSignature_IsStableAcrossSessions(t *testing.T) {
	a := Signature("security.threat_detected.v1", "pii_leak")
	b := Signature("security.threat_detected.v1", "pii_leak")
	if a != b {
		t.Fatalf("signature is not stable: %q vs %q", a, b)
	}
	if a != "security.threat_detected.v1|pii_leak" {
		t.Errorf("signature = %q, want eventType|code", a)
	}
}

// ---- 缓存语义 ----

func newTestEvent() securityevent.Event {
	return securityevent.Event{
		EventID:   "evt-1",
		EventType: "security.threat_detected.v1",
		Code:      "pii_leak",
		Severity:  securityevent.SeverityHigh,
		Action:    securityevent.ActionBlocked,
	}
}

func TestCache_AnnotateAttachesFleetHint(t *testing.T) {
	cache := NewCache(time.Hour)
	cache.Replace(map[string]Pattern{
		Signature("security.threat_detected.v1", "pii_leak"): {
			Signature:     Signature("security.threat_detected.v1", "pii_leak"),
			Observations:  7,
			Instances:     []string{"gw-a", "gw-b"},
			InstanceCount: 2,
		},
	}, time.Now())

	event := newTestEvent()
	if !cache.Annotate(&event) {
		t.Fatal("Annotate = false, want true (the pattern is in the snapshot)")
	}
	if event.FleetHint == nil {
		t.Fatal("FleetHint is nil")
	}
	if event.FleetHint.Observations != 7 {
		t.Errorf("FleetHint.Observations = %d, want 7", event.FleetHint.Observations)
	}
	if event.FleetHint.InstanceCount != 2 {
		t.Errorf("FleetHint.InstanceCount = %d, want 2", event.FleetHint.InstanceCount)
	}
}

// TestCache_AnnotateDoesNotChangeTheVerdict 钉住"共享域不参与处置判定"。
//
// 这是本包最重要的一条不变量：共享项目里的内容可能被污染，让它影响
// "要不要拦"等于把外部系统的可信度接到请求路径上。
func TestCache_AnnotateDoesNotChangeTheVerdict(t *testing.T) {
	cache := NewCache(time.Hour)
	cache.Replace(map[string]Pattern{
		Signature("security.threat_detected.v1", "pii_leak"): {
			Signature:     Signature("security.threat_detected.v1", "pii_leak"),
			Observations:  99,
			InstanceCount: 5,
			Severity:      securityevent.SeverityCritical,
		},
	}, time.Now())

	event := newTestEvent()
	beforeSeverity, beforeAction := event.Severity, event.Action
	cache.Annotate(&event)
	if event.Severity != beforeSeverity || event.Action != beforeAction {
		t.Fatalf("annotate changed the verdict: severity %q→%q action %q→%q",
			beforeSeverity, event.Severity, beforeAction, event.Action)
	}
}

func TestCache_AnnotateMissLeavesNoHint(t *testing.T) {
	cache := NewCache(time.Hour)
	cache.Replace(map[string]Pattern{
		Signature("security.threat_detected.v1", "other"): {Signature: "x"},
	}, time.Now())

	event := newTestEvent()
	if cache.Annotate(&event) {
		t.Fatal("Annotate = true for an unknown pattern")
	}
	if event.FleetHint != nil {
		t.Fatal("FleetHint must stay nil when the pattern is unknown")
	}
}

// TestCache_StaleSnapshotIsNotUsed 钉住"陈旧的提示比没有提示更危险"。
func TestCache_StaleSnapshotIsNotUsed(t *testing.T) {
	cache := NewCache(time.Millisecond)
	cache.Replace(map[string]Pattern{
		Signature("security.threat_detected.v1", "pii_leak"): {Signature: "s", Observations: 3},
	}, time.Now().Add(-time.Hour))

	if !cache.Stale() {
		t.Fatal("Stale = false, want true (the snapshot is an hour old)")
	}
	event := newTestEvent()
	if cache.Annotate(&event) {
		t.Fatal("Annotate = true on a stale snapshot")
	}
	if event.FleetHint != nil {
		t.Fatal("FleetHint must stay nil on a stale snapshot")
	}
	stats := cache.Stats()
	if stats.StaleSkipped != 1 {
		t.Errorf("StaleSkipped = %d, want 1 (a stale skip must be countable)", stats.StaleSkipped)
	}
}

// TestCache_ReplaceDropsGhostPatterns 钉住"整份替换 ⇒ 不会留下幽灵模式"。
func TestCache_ReplaceDropsGhostPatterns(t *testing.T) {
	cache := NewCache(time.Hour)
	cache.Replace(map[string]Pattern{
		Signature("e", "ghost"): {Signature: "e|ghost"},
	}, time.Now())
	cache.Replace(map[string]Pattern{
		Signature("e", "live"): {Signature: "e|live"},
	}, time.Now())

	if _, ok := cache.Lookup(Signature("e", "ghost")); ok {
		t.Fatal("the replaced-away pattern is still in the cache")
	}
	if _, ok := cache.Lookup(Signature("e", "live")); !ok {
		t.Fatal("the new pattern is missing")
	}
}

// ---- 读面 ----

// fakeMemora 是一个只认 v2 检索契约的假 memora。
type fakeMemora struct {
	server      *httptest.Server
	lastBody    map[string]any
	lastHeaders http.Header
	respond     func() (int, string)
}

func newFakeMemora(t *testing.T) *fakeMemora {
	t.Helper()
	fake := &fakeMemora{}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		fake.lastBody = map[string]any{}
		_ = json.Unmarshal(raw, &fake.lastBody)
		fake.lastHeaders = r.Header.Clone()
		if fake.respond != nil {
			status, body := fake.respond()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"items":[],"degraded":false},"request_id":"req-1"}`)
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeMemora) client(t *testing.T) *memv2.Client {
	t.Helper()
	client, err := memv2.New(memv2.Config{
		BaseURL:   f.server.URL,
		JWTSecret: "test-secret",
		TenantID:  "tenant-x",
		ActorID:   "actor-x",
		ProjectID: "project-x",
	})
	if err != nil {
		t.Fatalf("memv2.New: %v", err)
	}
	return client
}

func TestReader_SendsSecurityTagFilterAndFailClosedPolicy(t *testing.T) {
	fake := newFakeMemora(t)
	reader := NewMemoraReader(fake.client(t), "", 10)
	if _, err := reader.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	filters, ok := fake.lastBody["filters"].(map[string]any)
	if !ok {
		t.Fatalf("request has no filters: %v", fake.lastBody)
	}
	tags, _ := filters["tags"].([]any)
	found := false
	for _, tag := range tags {
		if tag == securityevent.TagSecurity {
			found = true
		}
	}
	if !found {
		t.Errorf("filters.tags = %v, want it to contain %q", tags, securityevent.TagSecurity)
	}

	policy, _ := fake.lastBody["policy"].(map[string]any)
	if policy["on_degraded"] != "fail_closed" {
		t.Errorf("policy.on_degraded = %v, want fail_closed (a degraded result must not be taken as a snapshot)", policy["on_degraded"])
	}

	// 身份：请求头必须带 X-Project-ID 与 Bearer。
	if got := fake.lastHeaders.Get("X-Project-ID"); got != "project-x" {
		t.Errorf("X-Project-ID = %q, want project-x", got)
	}
	if !strings.HasPrefix(fake.lastHeaders.Get("Authorization"), "Bearer ") {
		t.Error("Authorization must carry the service JWT")
	}

	// scope_chain 必须与身份一致（服务端会判 project_mismatch / tenant_mismatch）。
	scope, _ := fake.lastBody["scope_chain"].(map[string]any)
	if scope["tenant_id"] != "tenant-x" || scope["project_id"] != "project-x" {
		t.Errorf("scope_chain = %v, want tenant-x/project-x", scope)
	}
}

func TestReader_ParsesMetadataIntoObservations(t *testing.T) {
	fake := newFakeMemora(t)
	fake.respond = func() (int, string) {
		return http.StatusOK, `{"data":{"items":[
			{"memory_id":"m-1","snippet":"[Security][high][blocked] pii_leak: leak",
			 "metadata":{"event_type":"security.threat_detected.v1","code":"pii_leak",
			 "severity":"high","action":"blocked","tenant_id":"t1","instance_id":"gw-a",
			 "detected_at":"2026-11-17T10:00:00Z"}},
			{"memory_id":"m-2","snippet":"no metadata","metadata":{}}
		],"degraded":false},"request_id":"req-1"}`
	}
	reader := NewMemoraReader(fake.client(t), "", 10)
	observations, err := reader.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(observations) != 1 {
		t.Fatalf("observations = %d, want 1 (the item without event_type/code must be skipped)", len(observations))
	}
	got := observations[0]
	if got.Code != "pii_leak" || got.EventType != "security.threat_detected.v1" {
		t.Errorf("observation = %+v, want the pii_leak threat event", got)
	}
	if got.Severity != securityevent.SeverityHigh {
		t.Errorf("severity = %q, want high", got.Severity)
	}
	if got.InstanceID != "gw-a" || got.TenantID != "t1" {
		t.Errorf("observation = %+v, want instance gw-a / tenant t1", got)
	}
	if got.DetectedAt.IsZero() {
		t.Error("detected_at was not parsed")
	}
}

// TestReader_DegradedIsAFailure 钉住"降级的结果不能当成快照"。
func TestReader_DegradedIsAFailure(t *testing.T) {
	fake := newFakeMemora(t)
	fake.respond = func() (int, string) {
		return http.StatusOK, `{"data":{"items":[],"degraded":true,"degraded_reasons":["memory_backend_unavailable"]},"request_id":"req-1"}`
	}
	reader := NewMemoraReader(fake.client(t), "", 10)
	if _, err := reader.Fetch(context.Background()); err == nil {
		t.Fatal("Fetch = nil error on a degraded response; a partial snapshot must not pass as a snapshot")
	}
}

// ---- 同步循环 ----

type stubReader struct {
	observations []Observation
	err          error
	calls        int
}

func (r *stubReader) Fetch(context.Context) ([]Observation, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	return r.observations, nil
}

func TestSyncer_SuccessReplacesSnapshot(t *testing.T) {
	reader := &stubReader{observations: []Observation{
		{EventType: "e", Code: "c", InstanceID: "gw-a"},
	}}
	cache := NewCache(time.Hour)
	syncer := NewSyncer(SyncerConfig{Reader: reader, Cache: cache})

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if _, ok := cache.Lookup(Signature("e", "c")); !ok {
		t.Fatal("the pattern is missing from the cache after a successful sync")
	}
	stats := syncer.Stats()
	if stats.Pulls != 1 || stats.Observations != 1 || stats.Patterns != 1 {
		t.Errorf("stats = %+v, want pulls=1 observations=1 patterns=1", stats)
	}
}

// TestSyncer_FailureKeepsPreviousSnapshot 钉住"读不到 ≠ 没有模式"。
func TestSyncer_FailureKeepsPreviousSnapshot(t *testing.T) {
	reader := &stubReader{observations: []Observation{{EventType: "e", Code: "c"}}}
	cache := NewCache(time.Hour)
	syncer := NewSyncer(SyncerConfig{Reader: reader, Cache: cache})

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("first SyncOnce: %v", err)
	}
	reader.err = errors.New("shared domain unreachable")
	if err := syncer.SyncOnce(context.Background()); err == nil {
		t.Fatal("SyncOnce = nil error, want the failure to surface")
	}
	if _, ok := cache.Lookup(Signature("e", "c")); !ok {
		t.Fatal("the previous snapshot was dropped by a failed sync")
	}
	stats := syncer.Stats()
	if stats.Errors != 1 {
		t.Errorf("errors = %d, want 1", stats.Errors)
	}
	if stats.LastError == "" {
		t.Error("last_error must be recorded (an invisible failure reads like 'no new patterns')")
	}
}

func TestSyncer_BackoffGrowsWithConsecutiveErrors(t *testing.T) {
	reader := &stubReader{err: errors.New("down")}
	syncer := NewSyncer(SyncerConfig{Reader: reader, Cache: NewCache(time.Hour), ErrorBackoff: time.Second})
	first := syncer.Backoff()
	_ = syncer.SyncOnce(context.Background())
	_ = syncer.SyncOnce(context.Background())
	if syncer.Backoff() <= first {
		t.Errorf("backoff = %v, want it to grow past %v after consecutive failures", syncer.Backoff(), first)
	}
}
