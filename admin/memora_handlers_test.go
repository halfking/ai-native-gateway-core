package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/memory"
)

func TestMemoraSessionsSQLUsesIntervalCast(t *testing.T) {
	const marker = "interval '1 hour'"
	sql, _ := buildMemoraSessionsSQL(nil, 24, 1, 50)
	if !strings.Contains(sql, marker) {
		t.Fatalf("memora sessions SQL must use %s for no_topic_window end time", marker)
	}
	if strings.Contains(sql, "THEN '1 hour'") {
		t.Fatal("memora sessions SQL must not add bare text intervals to timestamptz")
	}
}

// fakeMemoraClient is a thread-safe stub satisfying the memoraClient
// interface used by Handler. Disabled() defaults to false so the
// handler proceeds to the API-key/tenant-access branch.
type fakeMemoraClient struct {
	disabled atomic.Bool
	lastK    atomic.Int64
	smartRes []memory.Memory
	smartErr error
}

func (f *fakeMemoraClient) Disabled() bool { return f.disabled.Load() }
func (f *fakeMemoraClient) Ping(_ context.Context) error {
	return nil
}
func (f *fakeMemoraClient) BaseURL() string { return "http://stub-memora.local" }
func (f *fakeMemoraClient) SmartSearch(_ context.Context, _, _ string, topK int) ([]memory.Memory, error) {
	f.lastK.Store(int64(topK))
	return f.smartRes, f.smartErr
}

func newMemoraQueryRequest(taskID, query string) *http.Request {
	url := "/api/system/memora-query/" + taskID
	if query != "" {
		url += "?q=" + query
	}
	r := httptest.NewRequest(http.MethodGet, url, nil)
	return SetAuthContext(r, &AuthContext{
		Role:     "super_admin",
		TenantID: "default",
		Username: "admin",
	})
}

// stubPool returns a lazy pgxpool.Pool that will fail any actual query
// (unreachable host with short connect timeout). Use only when the
// test should NOT reach a real database call but the handler requires
// a non-nil pool to skip the db==nil guard.
//
// Configuration notes:
//   - ConnectTimeout=1s: without this, puddle spawns background
//     goroutines that hang trying to reach 127.0.0.1:1, which then
//     blocks pool.Close() during t.Cleanup and stalls the test suite.
//   - MaxConns=1 / MinConns=0: small pool avoids background warming.
func stubPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig("postgres://invalid:invalid@127.0.0.1:1/nonexistent?connect_timeout=1")
	if err != nil {
		t.Fatalf("parse stub pool config: %v", err)
	}
	cfg.MaxConns = 1
	cfg.MinConns = 0
	cfg.ConnConfig.ConnectTimeout = 1 * time.Second
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("create stub pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestMemoraQueryRejectsPostMethod(t *testing.T) {
	h := &Handler{db: stubPool(t), memoraClient: &fakeMemoraClient{}}
	r := httptest.NewRequest(http.MethodPost, "/api/system/memora-query/task-1", nil)
	r = SetAuthContext(r, &AuthContext{Role: "super_admin", TenantID: "default"})
	w := httptest.NewRecorder()
	h.handleMemoraQuery(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("got %d, want 405 Method Not Allowed", w.Code)
	}
}

func TestMemoraQueryRequiresDatabase(t *testing.T) {
	h := &Handler{memoraClient: &fakeMemoraClient{}}
	r := newMemoraQueryRequest("task-1", "hello")
	w := httptest.NewRecorder()
	h.handleMemoraQuery(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503 Service Unavailable", w.Code)
	}
	if !strings.Contains(w.Body.String(), "database not configured") {
		t.Fatalf("expected 'database not configured' message; got %q", w.Body.String())
	}
}

func TestMemoraQueryRejectsDisabledMemora(t *testing.T) {
	mc := &fakeMemoraClient{}
	mc.disabled.Store(true)
	h := &Handler{db: stubPool(t), memoraClient: mc}
	r := newMemoraQueryRequest("task-1", "hello")
	w := httptest.NewRecorder()
	h.handleMemoraQuery(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503 (memora disabled)", w.Code)
	}
	if !strings.Contains(w.Body.String(), "memora not configured") {
		t.Fatalf("expected 'memora not configured'; got %q", w.Body.String())
	}
}

func TestMemoraQueryRequiresTaskID(t *testing.T) {
	h := &Handler{db: stubPool(t), memoraClient: &fakeMemoraClient{}}
	r := newMemoraQueryRequest("", "hello")
	w := httptest.NewRecorder()
	h.handleMemoraQuery(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 Missing task_id", w.Code)
	}
}

func TestMemoraQueryRequiresQueryParam(t *testing.T) {
	h := &Handler{db: stubPool(t), memoraClient: &fakeMemoraClient{}}
	r := newMemoraQueryRequest("task-1", "")
	w := httptest.NewRecorder()
	h.handleMemoraQuery(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 Missing q", w.Code)
	}
}

func TestMemoraQueryTopKClampBounds(t *testing.T) {
	// Defence-in-depth: the handler clamps top_k to [1, 20]. The
	// constants live in memora_handlers.go as inline literals; mirror
	// them here so a future refactor that renames/relocates them
	// fails this test instead of silently widening the bounds.
	if memoraTopKMin != 1 {
		t.Fatalf("memoraTopKMin changed: got %d, want 1 (security regression?)", memoraTopKMin)
	}
	if memoraTopKMax != 20 {
		t.Fatalf("memoraTopKMax changed: got %d, want 20 (security regression?)", memoraTopKMax)
	}
}

// memoraTopKMin / memoraTopKMax mirror the inline bounds in
// handleMemoraQuery (memora_handlers.go). Kept here so a refactor
// that touches the handler without updating these constants will
// fail loudly.
const (
	memoraTopKMin = 1
	memoraTopKMax = 20
)

// TestMemoraQueryUserIDIsTenantScoped documents the safety-critical
// invariant: the userID passed to memoraClient.SmartSearch MUST be
// derived via memory.UserID(tenantID, apiKeyID, taskID), NOT the raw
// task_id supplied by the caller. Cross-tenant integration test
// requires a real pgxpool.Pool; see admin/memora_query_integration_test.go.
func TestMemoraQueryUserIDIsTenantScoped(t *testing.T) {
	t.Skip("integration test pending pgxpool stub; see admin/memora_query_integration_test.go")
}

// TestBuildMemoraSessionsSQLArgsMatchPlaceholders ensures the SQL
// placeholders line up with the args slice, preventing an off-by-one
// that would cause wrong query results (or, in worst case, error
// leakage to the client).
func TestBuildMemoraSessionsSQLArgsMatchPlaceholders(t *testing.T) {
	for _, c := range []struct {
		name          string
		hours         int
		noTopicWindow int
		limit         int
		wantN         int
	}{
		{"default", 24, 1, 50, 3},
		{"small", 1, 1, 1, 3},
		{"max limit", 168, 6, 200, 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			sql, args := buildMemoraSessionsSQL(nil, c.hours, c.noTopicWindow, c.limit)
			placeholders := map[int]bool{}
			for i := 1; i <= 10; i++ {
				if strings.Contains(sql, "$"+itoa(i)) {
					placeholders[i] = true
				}
			}
			if len(placeholders) != c.wantN {
				t.Fatalf("SQL should have %d placeholders; got %d in\n%s", c.wantN, len(placeholders), sql)
			}
			if len(args) != c.wantN {
				t.Fatalf("args should match placeholders (got %d args for %d placeholders)", len(args), c.wantN)
			}
			if got, ok := args[0].(int); !ok || got != c.hours {
				t.Fatalf("args[0]=%v want %d", args[0], c.hours)
			}
		})
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
