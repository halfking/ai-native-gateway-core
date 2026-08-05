package admin

import (
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/kaixuan/llm-gateway-go/internal/summarystore"
)

// TestSplitCorpusIntoChunks (2026-08-06) — guards the map-reduce splitter
// that decides when a session is "long enough" to need chunking. Pure
// function, no DB or HTTP dependencies.
func TestSplitCorpusIntoChunks(t *testing.T) {
	tests := []struct {
		name      string
		corpus    string
		chunkSize int
		want      int
	}{
		{name: "short corpus → 1 chunk", corpus: "hello world", chunkSize: 100, want: 1},
		{name: "exactly chunk size → 1 chunk", corpus: strings.Repeat("a", 100), chunkSize: 100, want: 1},
		{name: "1.5x chunk size → 2 chunks", corpus: strings.Repeat("a", 150), chunkSize: 100, want: 2},
		{name: "5x chunk size → 5 chunks", corpus: strings.Repeat("a", 500), chunkSize: 100, want: 5},
		{name: "with newlines prefers newline break", corpus: strings.Repeat("a", 50) + "\n" + strings.Repeat("b", 50) + "\n" + strings.Repeat("c", 50), chunkSize: 100, want: 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := splitCorpusIntoChunks(tc.corpus, tc.chunkSize)
			if len(got) != tc.want {
				t.Fatalf("got %d chunks, want %d", len(got), tc.want)
			}
			// Round-trip: joining chunks must equal the original (modulo a
			// single dropped newline at each break boundary — acceptable).
			joined := strings.Join(got, "")
			originalNoNewlines := strings.ReplaceAll(tc.corpus, "\n", "")
			joinedNoNewlines := strings.ReplaceAll(joined, "\n", "")
			if joinedNoNewlines != originalNoNewlines {
				t.Errorf("round-trip mismatch (len got=%d want=%d)", len(joinedNoNewlines), len(originalNoNewlines))
			}
		})
	}
}

// TestParseSummaryJSON (2026-08-06) — guards the JSON parser that
// tolerates both strict JSON and prose-wrapped responses.
func TestParseSummaryJSON(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantSummary string
		wantTitle   string
		wantTopics  int
	}{
		{
			name:        "strict JSON",
			input:       `{"summary":"用户询问订单状态","key_points":["a","b","c"],"user_intent":"check_order","title":"订单查询"}`,
			wantSummary: "用户询问订单状态",
			wantTitle:   "订单查询",
			wantTopics:  3,
		},
		{
			name:        "prose-wrapped JSON",
			input:       `好的,以下是总结:\n{"summary":"用户询问天气","key_points":["北京","晴"],"user_intent":"weather"}\n完成。`,
			wantSummary: "用户询问天气",
			wantTitle:   "用户询问天气",
			wantTopics:  2,
		},
		{
			name:        "no JSON → treat as prose summary",
			input:       "用户问了几个问题,我回答了。",
			wantSummary: "用户问了几个问题,我回答了。",
			wantTitle:   "用户问了几个问题,我回答了。",
			wantTopics:  0,
		},
		{
			name:        "empty → empty summary",
			input:       "",
			wantSummary: "",
			wantTitle:   "",
			wantTopics:  0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			title, summary, topics, _, _ := parseSummaryJSON(tc.input, "minimax-m2.7")
			if summary != tc.wantSummary {
				t.Errorf("summary = %q, want %q", summary, tc.wantSummary)
			}
			if title != tc.wantTitle {
				t.Errorf("title = %q, want %q", title, tc.wantTitle)
			}
			if len(topics) != tc.wantTopics {
				t.Errorf("topics = %d, want %d", len(topics), tc.wantTopics)
			}
		})
	}
}

// TestAllowTenant_RateLimited (2026-08-06) — verifies the per-tenant rate
// limiter rejects after the configured per-minute budget is consumed. We
// set a tiny budget (2/min) for the test to keep it fast.
func TestAllowTenant_RateLimited(t *testing.T) {
	g := &AutoSummaryGenerator{
		enabled:     true,
		rateByTnt:   make(map[string]*rate.Limiter),
		ratePerMin:  2,
		workerSlots: make(chan struct{}, 4),
		rng:         nil,
	}
	if !g.allowTenant("t1") {
		t.Fatal("first call should be allowed")
	}
	if !g.allowTenant("t1") {
		t.Fatal("second call should be allowed (burst up to ratePerMin)")
	}
	if g.allowTenant("t1") {
		t.Fatal("third call should be rate-limited")
	}
	// A different tenant has its own bucket — must be allowed even though
	// t1's bucket is empty.
	if !g.allowTenant("t2") {
		t.Fatal("different tenant must have its own bucket")
	}
}

// TestWorkerSlots_BoundedConcurrency (2026-08-06) — verifies the slot
// semaphore prevents more than N concurrent goroutines from entering the
// critical section.
func TestWorkerSlots_BoundedConcurrency(t *testing.T) {
	g := &AutoSummaryGenerator{
		enabled:     true,
		rateByTnt:   make(map[string]*rate.Limiter),
		ratePerMin:  1000,
		workerSlots: make(chan struct{}, 2),
		rng:         nil,
	}
	var inFlight atomic.Int32
	var maxObserved atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case g.workerSlots <- struct{}{}:
				defer func() { <-g.workerSlots }()
			default:
				return
			}
			now := inFlight.Add(1)
			for {
				m := maxObserved.Load()
				if now <= m || maxObserved.CompareAndSwap(m, now) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			inFlight.Add(-1)
		}()
	}
	wg.Wait()
	if got := maxObserved.Load(); got > 2 {
		t.Fatalf("max in-flight = %d, want ≤ 2 (slot capacity)", got)
	}
}

// TestDoCallSummaryOnce_EmitsBranchSessionAndParentHeaders (2026-08-06)
// — verifies the loopback request includes the gs_ branch session id
// (X-Gw-Session-Id: gs:<parent>), the parent request correlation header,
// and the source-actor header. Mirrors the title-generator test in
// auto_title_generator_test.go.
func TestDoCallSummaryOnce_EmitsBranchSessionAndParentHeaders(t *testing.T) {
	var (
		gotHeaders http.Header
		gotPath    string
		mu         sync.Mutex
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotHeaders = r.Header.Clone()
		gotPath = r.URL.Path
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"model":"minimax-m2.7","choices":[{"message":{"content":"{\"summary\":\"x\",\"key_points\":[\"a\"]}"}}]}`))
	}))
	defer srv.Close()
	t.Setenv("LLM_GATEWAY_ENDPOINT", srv.URL)

	g := newTestSummaryGen()
	task := adminLLMTaskConfig{
		DefaultProfile: "cost_first",
		TaskHint:       "creative",
		Key:            "session_summary",
		DeviceSeed:     "admin-session-summary",
		Temperature:    0.2,
	}
	res, err, status, bodyExcerpt := g.doCallSummaryOnce(
		t.Context(), srv.URL+"/v1/chat/completions",
		[]byte(`{"model":"minimax-m2.7","messages":[]}`),
		"sk-fake",
		"gw_abc123",          // user main session
		"parent-req-id-xyz",  // user request id that triggered this
		task,
		"minimax-m2.7",
		"summary",
	)
	if err != nil {
		t.Fatalf("doCallSummaryOnce error: %v (status=%d, body=%q)", err, status, bodyExcerpt)
	}
	if res.Content == "" {
		t.Fatal("expected non-empty content")
	}

	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q, want /v1/chat/completions", gotPath)
	}
	if v := gotHeaders.Get("X-Gw-Session-Id"); v != "gs:gw_abc123" {
		t.Errorf("X-Gw-Session-Id = %q, want gs:gw_abc123", v)
	}
	if v := gotHeaders.Get("X-Gw-Parent-Request-Id"); v != "parent-req-id-xyz" {
		t.Errorf("X-Gw-Parent-Request-Id = %q, want parent-req-id-xyz", v)
	}
	if v := gotHeaders.Get("X-Gw-Source-Actor"); v != "auto-summary-generator" {
		t.Errorf("X-Gw-Source-Actor = %q, want auto-summary-generator", v)
	}
	if v := gotHeaders.Get("X-Gw-Is-Auto"); v != "true" {
		t.Errorf("X-Gw-Is-Auto = %q, want true", v)
	}
	if v := gotHeaders.Get("X-Gw-Task-Id"); v != "auto-summary:gw_abc123:summary" {
		t.Errorf("X-Gw-Task-Id = %q, want auto-summary:gw_abc123:summary", v)
	}
}

// newTestSummaryGen returns an AutoSummaryGenerator with a freshly-seeded
// jitter source so retry-path tests don't NPE on a nil *rand.Rand.
func newTestSummaryGen() *AutoSummaryGenerator {
	return &AutoSummaryGenerator{
		enabled:     true,
		rateByTnt:   make(map[string]*rate.Limiter),
		ratePerMin:  1000,
		workerSlots: make(chan struct{}, 4),
		rng:         rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// TestCallSummaryOnceWithMode_RetriesOn503 (2026-08-06) — verifies the
// shared isTransientAutoTitleErr classifier (reused by both generators)
// gates the retry correctly.
func TestCallSummaryOnceWithMode_RetriesOn503(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream busy"}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"model":"minimax-m2.7","choices":[{"message":{"content":"{\"summary\":\"x\",\"key_points\":[\"a\"]}"}}]}`))
	}))
	defer srv.Close()
	t.Setenv("LLM_GATEWAY_ENDPOINT", srv.URL)

	g := newTestSummaryGen()
	g.handler = &Handler{}
	_, _, _, _, _, err := g.callSummaryOnceWithMode(
		t.Context(),
		"sk-fake",
		"gw_abc",
		"parent-req",
		"corpus",
		1,
		"summary",
	)
	if err != nil {
		t.Fatalf("expected retry to recover; got: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("calls = %d, want 2 (1 + 1 retry)", got)
	}
	_ = summarystore.NewStore(nil) // ensure summarystore stays imported (used by handler.go wire)
}

// TestIsPgxNoRows (2026-08-06) — guards the string-sniff "no rows"
// detector used by shouldTriggerSummary. Avoids a hard pgx import so the
// admin package can stay pgx-free at the type level.
func TestIsPgxNoRows(t *testing.T) {
	if isPgxNoRows(nil) {
		t.Fatal("nil must not be a no-rows error")
	}
	if !isPgxNoRows(errorString("ERROR: no rows in result set")) {
		t.Fatal("\"no rows in result set\" should match")
	}
	if !isPgxNoRows(errorString("pgx scan error (SQLSTATE P0002)")) {
		t.Fatal("\"P0002\" should match")
	}
	if isPgxNoRows(errorString("some other error")) {
		t.Fatal("\"some other error\" should not match")
	}
}

// errorString is a tiny helper to avoid importing errors just for this test.
type errorString string

func (e errorString) Error() string { return string(e) }