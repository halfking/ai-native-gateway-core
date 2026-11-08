package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// bytesReader 是 bytes.NewReader 的短别名（测试里读请求体用）。
func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

// containsSubstring 是 strings.Contains 的短别名。
func containsSubstring(haystack, needle string) bool { return strings.Contains(haystack, needle) }

// stringsReader 是 strings.NewReader 的短别名。
func stringsReader(s string) *strings.Reader { return strings.NewReader(s) }

// fakeUpstream 是一个最小可用的上游 MCP 服务（JSON-RPC over HTTP）。
type fakeUpstream struct {
	server *httptest.Server
	// tools 是该上游暴露的工具清单。
	tools []map[string]any
	// callResult 是 tools/call 的返回体。
	callResult map[string]any
	// lastRequest 记录最后一次调用（断言元数据头注入用）。
	lastRequest http.Header
	// lastBody 记录最后一次请求体。
	lastBody map[string]any
	// status 非 0 时返回该状态码。
	status int
}

func newFakeUpstream(t *testing.T, tools []map[string]any) *fakeUpstream {
	t.Helper()
	f := &fakeUpstream{tools: tools}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.lastRequest = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.lastBody = body
		if f.status != 0 {
			w.WriteHeader(f.status)
			_, _ = w.Write([]byte(`{"error":"upstream failure"}`))
			return
		}
		method, _ := body["method"].(string)
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{"jsonrpc": "2.0", "id": body["id"]}
		switch method {
		case "tools/list":
			resp["result"] = map[string]any{"tools": f.tools}
		case "tools/call":
			if f.callResult != nil {
				resp["result"] = f.callResult
			} else {
				resp["result"] = map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}
			}
		default:
			resp["result"] = map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeUpstream) url() string { return f.server.URL }

// newTestGateway 构造一个接好账本与审计的测试网关。
func newTestGateway(t *testing.T, upstream *fakeUpstream, opts ...func(*Config)) (*Gateway, *httptest.Server) {
	t.Helper()
	store := NewMemoryStore()
	audit, err := NewAuditLog(200, "")
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	cfg := Config{
		Store:           store,
		Audit:           audit,
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		AdminToken:      "admin-token",
		DefaultTenantID: "default",
		ToolTimeout:     5 * time.Second,
		Now:             time.Now,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	g, err := NewGateway(cfg)
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	if upstream != nil {
		adapter := Adapter{
			Name:        "memora",
			Endpoint:    upstream.url(),
			Enabled:     true,
			Capability:  "memory",
			Sensitivity: SensitivityRead,
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}
		if err := store.PutAdapter(context.Background(), adapter); err != nil {
			t.Fatalf("put adapter: %v", err)
		}
	}
	mount := NewMount(g)
	srv := httptest.NewServer(mount.Handler())
	t.Cleanup(srv.Close)
	return g, srv
}

// rpcCall 发一次 JSON-RPC 请求并返回 (status, body)。
func rpcCall(t *testing.T, srv *httptest.Server, path, token string, payload map[string]any) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("decode %q: %v", string(body), err)
		}
	}
	return resp.StatusCode, out
}

// rpcErrorCode 从 JSON-RPC 错误体里取错误码。
func rpcErrorCode(body map[string]any) int {
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		return 0
	}
	code, ok := errObj["code"].(float64)
	if !ok {
		return 0
	}
	return int(code)
}

// rpcResult 从 JSON-RPC 成功体里取 result。
func rpcResult(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	res, ok := body["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in body: %v", body)
	}
	return res
}
