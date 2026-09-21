package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFetchACCProjects_NoBaseURL(t *testing.T) {
	t.Setenv("LLM_GATEWAY_ACC_BASE_URL", "")
	t.Setenv("ACC_BASE_URL", "")
	t.Setenv("ACC_URL", "")
	_, _, err := fetchACCProjects(context.Background(), loadACCSyncConfig(), "")
	if err == nil {
		t.Fatal("expected error when BaseURL is empty")
	}
}

func TestFetchACCProjects_NoToken(t *testing.T) {
	t.Setenv("LLM_GATEWAY_ACC_BASE_URL", "http://example.test")
	t.Setenv("LLM_GATEWAY_ACC_SERVICE_TOKEN", "")
	_, _, err := fetchACCProjects(context.Background(), loadACCSyncConfig(), "")
	if err == nil {
		t.Fatal("expected error when token is empty")
	}
}

// roundTripperFunc 适配 http.RoundTripper 接口，便于 httptest 不启 server。
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// fetchWithStub 把 fetchACCProjects 内部的 http.Client 替换为可控的
// RoundTripper。这是关键的可测试性切入点——避免起真实 HTTP server，
// 同时仍然走完 fetchACCProjects 的所有分支（鉴权、超时、状态码映射、
// 分页、complete 判断）。
func fetchWithStub(t *testing.T, handler http.HandlerFunc, cfg accSyncConfig, tenant string) ([]accProjectPayload, bool, error) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	// cfg.BaseURL 写到 stub server。
	cfg.BaseURL = srv.URL
	if cfg.ServiceToken == "" {
		cfg.ServiceToken = "stub-token"
	}
	return fetchACCProjects(context.Background(), cfg, tenant)
}

func TestFetchACCProjects_AuthHeader(t *testing.T) {
	var gotAuth string
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(accProjectsResponse{OK: true, Items: []accProjectPayload{}})
	})
	_, _, err := fetchWithStub(t, stub, accSyncConfig{ServiceToken: "real-token"}, "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if gotAuth != "Bearer real-token" {
		t.Errorf("Authorization = %q, want Bearer real-token", gotAuth)
	}
}

func TestFetchACCProjects_TenantAndCursorParams(t *testing.T) {
	var gotQuery string
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(accProjectsResponse{OK: true, Items: []accProjectPayload{}})
	})
	_, _, err := fetchWithStub(t, stub, accSyncConfig{}, "tenant-a")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(gotQuery, "tenant=tenant-a") {
		t.Errorf("query = %q, want tenant=tenant-a", gotQuery)
	}
}

func TestFetchACCProjects_StatusMappings(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   bool // finished should be false
	}{
		{"401", http.StatusUnauthorized, false},
		{"403", http.StatusForbidden, false},
		{"404", http.StatusNotFound, false},
		{"500", http.StatusInternalServerError, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "boom", tc.status)
			})
			_, finished, err := fetchWithStub(t, stub, accSyncConfig{}, "")
			if err == nil {
				t.Fatal("expected error")
			}
			if finished {
				t.Error("non-2xx must mark finished=false")
			}
		})
	}
}

func TestFetchACCProjects_BodyLimitReturnsError(t *testing.T) {
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 故意返回一个超大 body，验证 io.LimitReader 不让 fetch hang
		// 也不让 server 触发客户端 OOM。但因为 fetchACCProjects 对错误
		// 状态码判断发生在 body 读取之后，所以这里检查的是 client.Do
		// 完成后能正确读到截断的 body 并返回 5xx 错误。
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, strings.Repeat("x", accProjectsBodyLimit*2))
	})
	_, finished, err := fetchWithStub(t, stub, accSyncConfig{}, "")
	if err == nil {
		t.Fatal("expected error from 5xx response")
	}
	if finished {
		t.Error("finished must be false on error")
	}
}

func TestFetchACCProjects_Pagination_StopsOnEmptyCursor(t *testing.T) {
	var calls atomic.Int32
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("cursor") {
		case "":
			_ = json.NewEncoder(w).Encode(accProjectsResponse{
				OK: true, Items: []accProjectPayload{{Ref: "p1"}}, NextCursor: "p1",
			})
		case "p1":
			_ = json.NewEncoder(w).Encode(accProjectsResponse{
				OK: true, Items: []accProjectPayload{{Ref: "p2"}}, NextCursor: "",
			})
		}
	})
	items, finished, err := fetchWithStub(t, stub, accSyncConfig{}, "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !finished {
		t.Error("finished must be true after empty NextCursor")
	}
	if got := int(calls.Load()); got != 2 {
		t.Errorf("calls = %d, want 2", got)
	}
	if len(items) != 2 {
		t.Errorf("items len = %d, want 2", len(items))
	}
}

func TestFetchACCProjects_Pagination_BailsOnTooManyPages(t *testing.T) {
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 永远返回非空 cursor，触发 100 页上限
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(accProjectsResponse{
			OK: true, Items: []accProjectPayload{{Ref: "p"}}, NextCursor: "p",
		})
	})
	_, finished, err := fetchWithStub(t, stub, accSyncConfig{}, "")
	if err == nil {
		t.Fatal("expected pagination cap error")
	}
	if finished {
		t.Error("finished must be false when cap exceeded")
	}
}

func TestFetchACCProjects_OkFalseIsError(t *testing.T) {
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "source": "broken"})
	})
	_, _, err := fetchWithStub(t, stub, accSyncConfig{}, "")
	if err == nil {
		t.Fatal("ok=false must be an error")
	}
}

func TestNilIfEmpty(t *testing.T) {
	if nilIfEmpty("") != nil {
		t.Error("empty should be nil")
	}
	if got := nilIfEmpty("x"); got != "x" {
		t.Errorf("non-empty = %v", got)
	}
}

func TestQueryEscape(t *testing.T) {
	cases := map[string]string{
		"":          "",
		"plain":     "plain",
		"a b":       "a%20b",
		"a&b":       "a%26b",
		"a?b":       "a%3Fb",
		"a#b":       "a%23b",
	}
	for in, want := range cases {
		if got := queryEscape(in); got != want {
			t.Errorf("queryEscape(%q) = %q, want %q", in, got, want)
		}
	}
}
