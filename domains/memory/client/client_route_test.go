package client

// client_route_test.go —— 路由契约门（2026-10-20，审计 D1）。
//
// 缺陷：网关的 memora 客户端把 add/search 都打在 `/product/add` 与
// `/product/search` —— 那是 **MemOS** 的路径。memora 全仓没有任何
// /product/* 路由（真实路径见 client.go 顶部的路由清单与
// memora/internal/memory/handler.go:73-108）⇒ 人工抽取端点推 memora 必然
// 404，整条「会话数据 → 记忆」链从未走通。
//
// 本文件钉的是**实际发出的请求路径**（用 httptest 记录 method + path），
// 而不是「代码里有没有某个字符串」—— 后者钉不住「默认解析到哪个路径」。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/memory"
)

// recordedRequest is one request observed by the fake memora server.
type recordedRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   string
}

// fakeMemora records every request and answers with the given body.
type fakeMemora struct {
	mu       sync.Mutex
	requests []recordedRequest
	body     string
}

func (f *fakeMemora) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		f.mu.Lock()
		f.requests = append(f.requests, recordedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Header: r.Header.Clone(),
			Body:   string(buf[:n]),
		})
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		//nolint:errcheck // test server write
		w.Write([]byte(f.body))
	}
}

func (f *fakeMemora) last() recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return recordedRequest{}
	}
	return f.requests[len(f.requests)-1]
}

// TestClient_DefaultRoutesAreMemoraReal pins the default route set.
//
// The expected values are memora's real routes (memora/internal/memory/
// handler.go:83,86,87,88). If someone flips the default back to the MemOS
// paths, this gate goes red — which is the point: the broken baseline must
// not be able to come back silently.
func TestClient_DefaultRoutesAreMemoraReal(t *testing.T) {
	c := NewClient(ClientConfig{BaseURL: "http://127.0.0.1:1"})

	if got := c.AddPath(); got != "/api/memory/add" {
		t.Fatalf("默认 add 路径 = %q，必须是 /api/memory/add（memora/internal/memory/handler.go:87）", got)
	}
	if got := c.SearchPath(); got != "/api/search" {
		t.Fatalf("默认 search 路径 = %q，必须是 /api/search（memora/internal/memory/handler.go:83）", got)
	}
}

// TestClient_AddPostsToMemoraAddRoute pins the add request path end-to-end.
func TestClient_AddPostsToMemoraAddRoute(t *testing.T) {
	fake := &fakeMemora{body: `{"ok":true,"id":"m1"}`}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := NewClient(ClientConfig{BaseURL: srv.URL})
	if err := c.AddMessage(context.Background(), "u1",
		[]memory.Message{{Role: "user", Content: "hello"}},
		map[string]any{"source": "test"}); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	req := fake.last()
	if req.Method != http.MethodPost {
		t.Fatalf("add 必须是 POST，实得 %s", req.Method)
	}
	if req.Path != "/api/memory/add" {
		t.Fatalf("add 路径 = %q，必须是 /api/memory/add（memora/internal/memory/handler.go:87）", req.Path)
	}
}

// TestClient_SearchPostsToMemoraSearchRoute pins the search request path and
// the payload shape (unchanged: user_id / query / top_k).
func TestClient_SearchPostsToMemoraSearchRoute(t *testing.T) {
	fake := &fakeMemora{body: `{"query":"q","results":[],"count":0}`}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := NewClient(ClientConfig{BaseURL: srv.URL})
	if _, err := c.Search(context.Background(), "u1", "q", 8); err != nil {
		t.Fatalf("Search: %v", err)
	}
	req := fake.last()
	if req.Method != http.MethodPost {
		t.Fatalf("search 必须是 POST，实得 %s", req.Method)
	}
	if req.Path != "/api/search" {
		t.Fatalf("search 路径 = %q，必须是 /api/search（memora/internal/memory/handler.go:83）", req.Path)
	}
	for _, want := range []string{`"user_id":"u1"`, `"query":"q"`, `"top_k":8`} {
		if !strings.Contains(req.Body, want) {
			t.Fatalf("search payload 缺 %s：%s", want, req.Body)
		}
	}
}

// TestClient_MemoraResponseShapeIsParsed guards the "path fixed but response
// shape differs" half of D1: memora answers
// {"query":…,"results":[{id,content,tags,score}],"count":n}
// (memora/internal/memory/handler.go:231) while the client historically only
// understood MemOS's data.text_mem[].memories[]. Decoding only the old shape
// would make the fixed route return **zero results** — a silent no-op.
func TestClient_MemoraResponseShapeIsParsed(t *testing.T) {
	fake := &fakeMemora{body: `{"query":"q","count":1,"results":[{"id":"m1","content":"remembered","tags":["t1"],"score":0.75,"mem_cube_id":"cube-9"}]}`}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := NewClient(ClientConfig{BaseURL: srv.URL})
	mems, err := c.Search(context.Background(), "u1", "q", 8)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(mems) != 1 {
		t.Fatalf("memora 响应形态必须解析出 1 条，实得 %d 条：%+v", len(mems), mems)
	}
	if mems[0].Text != "remembered" || mems[0].ID != "m1" || mems[0].Score != 0.75 {
		t.Fatalf("memora 结果字段映射不对：%+v", mems[0])
	}
	if len(mems[0].Tags) != 1 || mems[0].Tags[0] != "t1" {
		t.Fatalf("memora tags 未映射：%+v", mems[0])
	}
	if mems[0].CubeID != "cube-9" {
		t.Fatalf("memora mem_cube_id 未映射：%+v", mems[0])
	}
}

// TestClient_MemOSResponseShapeStillParsed keeps the legacy opt-in path
// working: the MemOS envelope must still decode.
func TestClient_MemOSResponseShapeStillParsed(t *testing.T) {
	fake := &fakeMemora{body: `{"code":0,"data":{"text_mem":[{"cube_id":"kb","memories":[{"id":"b1","memory":"legacy","relativity":0.5}]}]}}`}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := NewClient(ClientConfig{BaseURL: srv.URL})
	mems, err := c.Search(context.Background(), "u1", "q", 8)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(mems) != 1 || mems[0].Text != "legacy" {
		t.Fatalf("MemOS 响应形态必须仍能解析：%+v", mems)
	}
}

// TestClient_LegacyMemOSPathsAreExplicitOptIn pins that the MemOS routes are
// reachable **only** when explicitly asked for — by config or by env.
func TestClient_LegacyMemOSPathsAreExplicitOptIn(t *testing.T) {
	t.Run("config", func(t *testing.T) {
		fake := &fakeMemora{body: `{"ok":true}`}
		srv := httptest.NewServer(fake.handler())
		defer srv.Close()

		c := NewClient(ClientConfig{BaseURL: srv.URL, LegacyMemOSPaths: true})
		if err := c.AddMessage(context.Background(), "u1",
			[]memory.Message{{Role: "user", Content: "hello"}}, nil); err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
		if got := fake.last().Path; got != "/product/add" {
			t.Fatalf("opt-in 后 add 路径 = %q，必须是 /product/add", got)
		}
		if _, err := c.Search(context.Background(), "u1", "q", 8); err != nil {
			t.Fatalf("Search: %v", err)
		}
		if got := fake.last().Path; got != "/product/search" {
			t.Fatalf("opt-in 后 search 路径 = %q，必须是 /product/search", got)
		}
	})

	t.Run("env", func(t *testing.T) {
		t.Setenv(LegacyMemOSPathsEnv, "1")
		fake := &fakeMemora{body: `{"ok":true}`}
		srv := httptest.NewServer(fake.handler())
		defer srv.Close()

		c := NewClient(ClientConfig{BaseURL: srv.URL})
		if err := c.AddMessage(context.Background(), "u1",
			[]memory.Message{{Role: "user", Content: "hello"}}, nil); err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
		if got := fake.last().Path; got != "/product/add" {
			t.Fatalf("env opt-in 后 add 路径 = %q，必须是 /product/add", got)
		}
	})

	t.Run("env off keeps memora paths", func(t *testing.T) {
		t.Setenv(LegacyMemOSPathsEnv, "")
		c := NewClient(ClientConfig{BaseURL: "http://127.0.0.1:1"})
		if got := c.AddPath(); got != "/api/memory/add" {
			t.Fatalf("未 opt-in 时 add 路径 = %q，必须是 /api/memory/add", got)
		}
	})
}

// TestClient_ExplicitPathsOverrideDefaults pins the third knob: an explicit
// path wins over both the default and the legacy opt-in.
func TestClient_ExplicitPathsOverrideDefaults(t *testing.T) {
	c := NewClient(ClientConfig{
		BaseURL:          "http://127.0.0.1:1",
		LegacyMemOSPaths: true,
		AddPath:          "/api/add",
		SearchPath:       "/api/memory/search",
	})
	if got := c.AddPath(); got != "/api/add" {
		t.Fatalf("显式 add 路径 = %q，必须是 /api/add", got)
	}
	if got := c.SearchPath(); got != "/api/memory/search" {
		t.Fatalf("显式 search 路径 = %q，必须是 /api/memory/search", got)
	}
}

// TestClient_ProjectScopeHeaderIsSent pins memora's fail-closed project
// scope: without X-Project-ID, /api/add and /api/search answer
// 400 "project_id is required (000035 contract)"
// (memora/internal/memory/handler.go:208-211, 335-338).
func TestClient_ProjectScopeHeaderIsSent(t *testing.T) {
	t.Run("from info", func(t *testing.T) {
		fake := &fakeMemora{body: `{"ok":true}`}
		srv := httptest.NewServer(fake.handler())
		defer srv.Close()

		c := NewClient(ClientConfig{BaseURL: srv.URL})
		if err := c.AddMessage(context.Background(), "u1",
			[]memory.Message{{Role: "user", Content: "hello"}},
			map[string]any{"project_id": "proj-7"}); err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
		if got := fake.last().Header.Get("X-Project-ID"); got != "proj-7" {
			t.Fatalf("X-Project-ID = %q，必须取自 info 的 project_id（proj-7）", got)
		}
	})

	t.Run("from config", func(t *testing.T) {
		fake := &fakeMemora{body: `{"ok":true}`}
		srv := httptest.NewServer(fake.handler())
		defer srv.Close()

		c := NewClient(ClientConfig{BaseURL: srv.URL, ProjectID: "proj-default"})
		if err := c.AddMessage(context.Background(), "u1",
			[]memory.Message{{Role: "user", Content: "hello"}}, nil); err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
		if got := fake.last().Header.Get("X-Project-ID"); got != "proj-default" {
			t.Fatalf("X-Project-ID = %q，必须是 client 级默认 proj-default", got)
		}
	})
}
