package streaming

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestModelsHandler_ServesLastGoodOnDeadDB pins the 2026-09-04 availability
// gear: once a successful query has populated the last-good cache, DB
// failures (and even a lost pool) keep serving the cached list within
// modelsStaleGrace instead of 5xx-ing a pure read endpoint.
func TestModelsHandler_ServesLastGoodOnDeadDB(t *testing.T) {
	// Port 1 is unbound on every supported platform: dial fails with
	// "connection refused" immediately, simulating a crashed PostgreSQL.
	pool, err := pgxpool.New(context.Background(), "postgres://u:p@127.0.0.1:1/db?sslmode=disable")
	if err != nil {
		t.Fatalf("construct dead pool: %v", err)
	}
	defer pool.Close()

	h := NewModelsHandler()
	h.SetDB(pool)
	h.rememberGoodEntries(modelsScopeFeatured, []modelEntry{{ID: "gpt-x", Object: "model"}})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 serving last-good during outage; body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Object string       `json:"object"`
		Data   []modelEntry `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(payload.Data) != 1 || payload.Data[0].ID != "gpt-x" {
		t.Fatalf("data = %+v, want the cached gpt-x entry", payload.Data)
	}
}

// TestModelsHandler_NilPoolWithoutCacheStill503 pins that the gear only
// applies when this process actually served a list before — a DB-less boot
// with no cache keeps the explicit 503 (no hallucinated model lists).
func TestModelsHandler_NilPoolWithoutCacheStill503(t *testing.T) {
	h := NewModelsHandler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// TestModelsHandler_StaleGraceExpiry pins the bound: entries older than
// modelsStaleGrace must not be served.
func TestModelsHandler_StaleGraceExpiry(t *testing.T) {
	h := NewModelsHandler()
	h.rememberGoodEntries(modelsScopeFeatured, []modelEntry{{ID: "gpt-x", Object: "model"}})
	h.staleMu.Lock()
	h.stale[modelsScopeFeatured] = modelsStaleEntry{
		entries: []modelEntry{{ID: "gpt-x", Object: "model"}},
		at:      time.Now().Add(-(modelsStaleGrace + time.Minute)),
	}
	h.staleMu.Unlock()

	if _, ok := h.lastGoodEntries(modelsScopeFeatured); ok {
		t.Fatal("entry past modelsStaleGrace must not serve")
	}
}

// ★ TestModelsHandler_StaleCacheIsPerScope pins that the last-good cache is
// keyed BY SCOPE.
//
// 2026-10-29：精选过滤在 SQL 里，所以查询结果本身随口径变化。此前缓存只有一格
// ⇒ 一次「精选」请求填进去的快照，会在数据库故障期间被「全量」请求当成
// 「这就是全部」端出去（= 静默少报模型）。两个口径必须各存各的快照；某个口径
// 没有快照时，行为与「从未缓存过」一致（显式 500），而不是拿另一个口径的
// 快照冒充。
func TestModelsHandler_StaleCacheIsPerScope(t *testing.T) {
	h := NewModelsHandler()
	h.rememberGoodEntries(modelsScopeFeatured, []modelEntry{{ID: "featured-x", Object: "model"}})

	// The featured snapshot exists…
	if entries, ok := h.lastGoodEntries(modelsScopeFeatured); !ok || len(entries) != 1 {
		t.Fatalf("featured scope must have its own snapshot, got ok=%v entries=%d", ok, len(entries))
	}
	// …and the all scope must NOT inherit it.
	if entries, ok := h.lastGoodEntries(modelsScopeAll); ok {
		t.Fatalf("all scope must not serve the featured snapshot, got %d entries", len(entries))
	}
}

// ★ TestModelsHandler_StaleCacheAllScopeIsSeparate is the mirror arm: a
// snapshot taken under "all" must be reachable only under "all".
func TestModelsHandler_StaleCacheAllScopeIsSeparate(t *testing.T) {
	h := NewModelsHandler()
	h.rememberGoodEntries(modelsScopeAll, []modelEntry{{ID: "all-x", Object: "model"}})

	if entries, ok := h.lastGoodEntries(modelsScopeAll); !ok || len(entries) != 1 || entries[0].ID != "all-x" {
		t.Fatalf("all scope must serve its own snapshot, got ok=%v entries=%+v", ok, entries)
	}
	if _, ok := h.lastGoodEntries(modelsScopeFeatured); ok {
		t.Fatal("featured scope must not serve the all-scope snapshot")
	}
}
