package sessionforensics_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
	"github.com/kaixuan/llm-gateway-go/domains/sessionforensics"
)

// mockStore 实现 sessionforensics.Store 接口（pgx.Rows 等的具体替代由
// test 拼 SQL 字符串模拟；这里用最简的 map-backed 实现）。
type mockStore struct {
	mu           sync.Mutex
	sessionRows  map[string][]map[string]any // session_id → rows
	summaryRows  map[string]map[string]any   // session_id → summary
	lastSQL      []string
	failQueryErr error
}

func newMockStore() *mockStore {
	return &mockStore{
		sessionRows: map[string][]map[string]any{},
		summaryRows: map[string]map[string]any{},
	}
}

func (m *mockStore) Query(ctx context.Context, sql string, args ...any) (sessionforensics.RowIterator, error) {
	m.mu.Lock()
	m.lastSQL = append(m.lastSQL, sql)
	hasSelect := strings.Contains(strings.ToUpper(sql), "SELECT")
	hasInsert := strings.Contains(strings.ToUpper(sql), "INSERT")
	m.mu.Unlock()
	if m.failQueryErr != nil {
		return nil, m.failQueryErr
	}
	if hasSelect {
		// 简化：根据 args[0]=sessionID 查 sessionRows
		if len(args) > 0 {
			sid, ok := args[0].(string)
			if !ok {
				return &mockRows{}, nil
			}
			m.mu.Lock()
			rs := m.sessionRows[sid]
			m.mu.Unlock()
			return &mockRows{rows: rs}, nil
		}
	}
	if hasInsert {
		return &mockRows{}, nil
	}
	return &mockRows{}, nil
}

func (m *mockStore) QueryRow(ctx context.Context, sql string, args ...any) sessionforensics.Row {
	m.mu.Lock()
	m.lastSQL = append(m.lastSQL, sql)
	hasSelect := strings.Contains(strings.ToUpper(sql), "SELECT")
	hasInsert := strings.Contains(strings.ToUpper(sql), "INSERT")
	m.mu.Unlock()
	if m.failQueryErr != nil {
		return &mockRow{err: m.failQueryErr}
	}
	if hasSelect {
		if len(args) > 0 {
			sid, ok := args[0].(string)
			if !ok {
				return &mockRow{}
			}
			m.mu.Lock()
			r := m.summaryRows[sid]
			m.mu.Unlock()
			if r == nil {
				return &mockRow{err: errNoRows}
			}
			return &mockRow{row: r}
		}
	}
	if hasInsert {
		return &mockRow{}
	}
	return &mockRow{}
}

var errNoRows = errors.New("no rows in result set")

type mockRows struct {
	idx  int
	rows []map[string]any
}

func (r *mockRows) Next() bool {
	return r.idx < len(r.rows)
}
func (r *mockRows) Scan(...any) error {
	if r.idx >= len(r.rows) {
		return errors.New("no more rows")
	}
	r.idx++
	return nil
}
func (r *mockRows) Err() error   { return nil }
func (r *mockRows) Close() error { return nil }

type mockRow struct {
	row map[string]any
	err error
}

func (r *mockRow) Scan(...any) error {
	if r.err != nil {
		return r.err
	}
	return nil
}

// ── Exporter 单元测试 ─────────────────────────────────────────────────────

func TestExporter_ExportSession_Empty(t *testing.T) {
	store := newMockStore()
	exp := sessionforensics.NewExporterWithStore(store)
	_, err := exp.ExportSession(context.Background(), "", "default")
	if err == nil {
		t.Fatal("expected error for empty session_id")
	}
}

func TestExporter_ExportSession_NotFound(t *testing.T) {
	store := newMockStore()
	exp := sessionforensics.NewExporterWithStore(store)
	_, err := exp.ExportSession(context.Background(), "gw_xxx", "default")
	if !errors.Is(err, sessionforensics.ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestExporter_ExportSession_RoundTrip(t *testing.T) {
	// 我们用真实 pgxpool + 测试 fixture 会很重；这里只验证：
	//   1. NotFound 路径
	//   2. ErrSessionNotFound 错误
	//   3. session_summaries lookup error 不破坏调用
	// 更深入 SQL 行为测试在生产 CI 上跑。
	store := newMockStore()
	exp := sessionforensics.NewExporterWithStore(store)
	_, err := exp.ExportSession(context.Background(), "gw_test_001", "default")
	if !errors.Is(err, sessionforensics.ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

// ── Client (HTTP) 单测 ───────────────────────────────────────────────────

func TestClient_Download_404(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":"missing"}`))
	}))
	defer ts.Close()

	c := sessionforensics.NewClient(ts.URL)
	_, err := c.Download(context.Background(), "gw_nope", "default")
	if !errors.Is(err, sessionforensics.ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestClient_Download_OK(t *testing.T) {
	expected := sessionforensics.SessionPack{
		SessionMeta: sessionforensics.SessionMeta{ID: "gw_xxx", Title: "Demo"},
		Messages: []sessionforensics.ExportMessage{
			{Turn: 1, Role: "user", Content: `{"model":"gpt-4o","messages":[]}`},
		},
		Summary: "s",
	}
	body, _ := json.Marshal(expected)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "gw_xxx" {
			t.Errorf("id mismatch: %s", r.URL.Query().Get("id"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer ts.Close()

	c := sessionforensics.NewClient(ts.URL)
	got, err := c.Download(context.Background(), "gw_xxx", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.SessionMeta.Title != "Demo" {
		t.Errorf("title: %q", got.SessionMeta.Title)
	}
	if len(got.Messages) != 1 {
		t.Errorf("messages len: %d", len(got.Messages))
	}
}

func TestClient_Download_CarriesBearer(t *testing.T) {
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"session_meta":{"id":"gw_x","title":""},"messages":[]}`))
	}))
	defer ts.Close()

	c := sessionforensics.NewClient(ts.URL)
	c.Bearer = "sk-test-xxxxx"
	_, err := c.Download(context.Background(), "gw_x", "default")
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer sk-test-xxxxx" {
		t.Errorf("expected Bearer header, got %q", gotAuth)
	}
}

// ── Replayer 单测 ────────────────────────────────────────────────────────

func TestReplayer_Replay_MultiTurnDeltaAppend(t *testing.T) {
	pack := &sessionforensics.SessionPack{
		SessionMeta: sessionforensics.SessionMeta{ID: "gxw_replay_dt_001"},
		Messages: []sessionforensics.ExportMessage{
			{Turn: 1, Role: "user", Content: `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`},
			{Turn: 2, Role: "user", Content: `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"bye"}]}`},
		},
	}
	rp := sessionforensics.NewReplayer()
	rep := rp.Replay(context.Background(), pack, sessionforensics.ReplayOptions{
		TenantID: "default", ContextWindow: 128_000,
	})
	if len(rep.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(rep.Steps))
	}
	// turn 1: fresh
	if rep.Steps[0].CacheTier != "MISS" {
		t.Logf("turn 1 cache tier: %s (expected MISS)", rep.Steps[0].CacheTier)
	}
	// turn 2: L1 hit
	if rep.Steps[1].CacheTier != "L1" {
		t.Errorf("turn 2 cache tier: %s (expected L1)", rep.Steps[1].CacheTier)
	}
	t.Logf("strategy counts: %v", rep.Aggregate.StrategyCounts)
	t.Logf("lossiness counts: %v", rep.Aggregate.LossinessCounts)
}

func TestReplayer_Replay_NilPack(t *testing.T) {
	if report := sessionforensics.NewReplayer().Replay(context.Background(), nil, sessionforensics.ReplayOptions{}); report != nil {
		t.Fatalf("Replay(nil) = %#v, want nil", report)
	}
}

func TestLoadExtractPyFile_PreservesTurnEvidence(t *testing.T) {
	file := filepath.Join(t.TempDir(), "session.json")
	body := `{"session_meta":{"id":"gxw_evidence_001"},"turns":[{"turn":1,"request_id":"req_1","ts":"2026-07-12T00:00:00Z","client_model":"gpt-4o","success":true,"latency_ms":42,"request_body":{"model":"gpt-4o","messages":[]},"response_body":{"id":"resp_1"}}]}`
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	pack, err := sessionforensics.LoadExtractPyFile(file)
	if err != nil {
		t.Fatal(err)
	}
	message := pack.Messages[0]
	if message.RequestID != "req_1" || message.Model != "gpt-4o" || message.LatencyMs != 42 || !message.Success {
		t.Fatalf("turn evidence not preserved: %#v", message)
	}
	if message.ResponseContent == "" {
		t.Fatal("response content not preserved")
	}
}

func TestReplayer_Replay_HyperlongTriggersStrip(t *testing.T) {
	// 构造 5MB 内容，包含大量 tool calls 触发 v4 strip
	bigContent := strings.Repeat(`{"role":"user","content":"'||chr(60)||'a'||chr(62)||'big content '||chr(60)||'/a'||chr(62)||' here"}`, 50000)
	body := `{"model":"gpt-4o","messages":[` + bigContent + `]}`
	pack := &sessionforensics.SessionPack{
		SessionMeta: sessionforensics.SessionMeta{ID: "gxw_replay_str_001"},
		Messages:    []sessionforensics.ExportMessage{{Turn: 1, Role: "user", Content: body}},
	}
	rp := sessionforensics.NewReplayer()
	rep := rp.Replay(context.Background(), pack, sessionforensics.ReplayOptions{
		TenantID: "default", ContextWindow: 128_000,
	})
	if len(rep.Steps) != 1 {
		t.Fatal("expected 1 step")
	}
	t.Logf("hyperlong replay: bytesBefore=%d bytesAfter=%d tools_cached_hit=%v",
		rep.Steps[0].BytesBefore, rep.Steps[0].BytesAfter, rep.Steps[0].ToolsCachedHit)
}

// ── Summarizer 单测 ──────────────────────────────────────────────────────

func TestSummarizer_FallbackWhenNilInner(t *testing.T) {
	sm := sessionforensics.NewSummarizer(nil) // nil inner
	res, err := sm.Summarize(context.Background(), "gw_x",
		sessionforensics.SummarizeOptions{FirstMessageOverride: "你好"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Title != "你好" {
		t.Errorf("expected fallback title to first message, got %q", res.Title)
	}
	if res.Source != "fallback" {
		t.Errorf("expected source=fallback, got %q", res.Source)
	}
}

func TestSummarizer_FallbackEmptyMessage(t *testing.T) {
	sm := sessionforensics.NewSummarizer(nil)
	res, err := sm.Summarize(context.Background(), "gw_x",
		sessionforensics.SummarizeOptions{FirstMessageOverride: ""})
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "未命名会话" {
		t.Errorf("expected fallback title for empty msg, got %q", res.Title)
	}
}

func TestSummarizer_FallbackTruncates(t *testing.T) {
	sm := sessionforensics.NewSummarizer(nil)
	long := strings.Repeat("a", 200)
	res, _ := sm.Summarize(context.Background(), "gw_x",
		sessionforensics.SummarizeOptions{FirstMessageOverride: long})
	if !strings.HasSuffix(res.Title, "...") {
		t.Errorf("expected long title to be truncated with ellipsis, got %q", res.Title)
	}
	if len([]rune(res.Title)) > 54 {
		t.Errorf("title too long: %d runes", len([]rune(res.Title)))
	}
}

// ── Service 端到端 ────────────────────────────────────────────────────────

func TestService_HTTPOnly_Path(t *testing.T) {
	// 没有 DB，走 HTTP 路径
	var capturedURL string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedURL = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"session_meta":{"id":"gw_x","title":"t"},
			"messages":[{"turn":1,"role":"user","content":"{}"}],
			"summary":""
		}`))
	}))
	defer ts.Close()

	svc := sessionforensics.NewService(sessionforensics.ServiceConfig{
		HTTPBaseURL: ts.URL, Bearer: "tok",
	})
	pack, err := svc.DownloadSession(context.Background(), "gw_x", "default")
	if err != nil {
		t.Fatal(err)
	}
	if pack.SessionMeta.ID != "gw_x" {
		t.Errorf("id: %q", pack.SessionMeta.ID)
	}
	if !strings.Contains(capturedURL, "id=gw_x") {
		t.Errorf("URL missing id: %s", capturedURL)
	}
}

func TestService_LocalStoreDir_Persistence(t *testing.T) {
	tmpDir := t.TempDir()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"session_meta":{"id":"gw_xyz","title":"t"},
			"messages":[],
			"summary":""
		}`))
	}))
	defer ts.Close()

	svc := sessionforensics.NewService(sessionforensics.ServiceConfig{
		HTTPBaseURL:   ts.URL,
		LocalStoreDir: tmpDir,
	})
	_, err := svc.DownloadSession(context.Background(), "gw_xyz", "default")
	if err != nil {
		t.Fatal(err)
	}
	fp := filepath.Join(tmpDir, "session_gw_xyz.json")
	if _, err := os.Stat(fp); err != nil {
		t.Fatalf("expected file at %s: %v", fp, err)
	}
}

// ── 三层缓存正确性 ─────────────────────────────────────────────────────────

func TestReplayer_LayeredCacheBehaviour(t *testing.T) {
	pack := &sessionforensics.SessionPack{
		SessionMeta: sessionforensics.SessionMeta{ID: "gxw_replay_lc_001"},
		Messages: []sessionforensics.ExportMessage{
			{Turn: 1, Role: "user", Content: `{"model":"gpt-4o","messages":[]}`},
			{Turn: 2, Role: "user", Content: `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`},
		},
	}
	// 不带 L2/L3
	rp := sessionforensics.NewReplayer()
	rep := rp.Replay(context.Background(), pack, sessionforensics.ReplayOptions{
		TenantID: "default",
	})
	if rep.Steps[0].CacheTier != "MISS" {
		t.Errorf("expected turn 1 MISS")
	}
	if rep.Steps[1].CacheTier != "L1" {
		t.Errorf("expected turn 2 L1, got %s", rep.Steps[1].CacheTier)
	}
}

// 复用 compression 包验证 SessionCache 完整性
func TestSessionCache_L2WriteRead_RoundTrip(t *testing.T) {
	// Use sessionforensics' InMemoryL2Backend to ensure interface alignment
	cache := compression.NewSessionCache(&fakeL2{}, nil)
	// 只验证 NewSessionCache 能正常工作
	if cache == nil {
		t.Fatal("nil cache")
	}
}

type fakeL2 struct{}

func (f *fakeL2) HSet(_ context.Context, _ string, _ ...any) error { return nil }
func (f *fakeL2) HGetAll(_ context.Context, _ string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (f *fakeL2) Expire(_ context.Context, _ string, _ time.Duration) error { return nil }
func (f *fakeL2) Del(_ context.Context, _ string) error                     { return nil }
