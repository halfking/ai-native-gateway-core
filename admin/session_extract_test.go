package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTruncateSamples 验证切片截断的边界.
func TestTruncateSamples(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		n    int
		want []string
	}{
		{"empty slice", []string{}, 5, []string{}},
		{"n=0 returns empty", []string{"a", "b", "c"}, 0, []string{}},
		{"n larger than slice returns whole", []string{"a", "b"}, 5, []string{"a", "b"}},
		{"n equals len returns whole", []string{"a", "b", "c"}, 3, []string{"a", "b", "c"}},
		{"n smaller truncates", []string{"a", "b", "c", "d", "e"}, 2, []string{"a", "b"}},
		{"n=1 returns first element", []string{"x", "y", "z"}, 1, []string{"x"}},
		// Known behaviour (audit pin): truncateSamples 在 n<0 时 panic.
		// `len(slice) <= n` 永远为 false 当 n<0 (例如 len=1, n=-1 -> 1<=-1 false),
		// 进入 slice[:n] 即 slice[:-1] panic. 与 session_compare.truncateStr
		// 是同一类 bug. 上游 n 来自常量 (e.g. preview sample count), 实际不会
		// 触发负值. 此测试不覆盖负值 (会 panic), 在 doc comment 里 pin.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateSamples(tt.in, tt.n)
			if !slicesEqual(got, tt.want) {
				t.Errorf("truncateSamples(%v, %d) = %v, want %v", tt.in, tt.n, got, tt.want)
			}
		})
	}
}

// slicesEqual compares two string slices.
func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestExtractToMemoraRequest_JSONRoundTrip 验证请求类型序列化.
func TestExtractToMemoraRequest_JSONRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		req  extractToMemoraRequest
	}{
		{"all defaults", extractToMemoraRequest{}},
		{"dry_run true", extractToMemoraRequest{DryRun: true}},
		{"include_responses true", extractToMemoraRequest{IncludeResponses: true}},
		{"both true", extractToMemoraRequest{DryRun: true, IncludeResponses: true}},
		{"both false explicit", extractToMemoraRequest{DryRun: false, IncludeResponses: false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.req)
			if err != nil {
				t.Fatalf("Marshal failed: %v", err)
			}
			var decoded extractToMemoraRequest
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("Unmarshal failed: %v", err)
			}
			if decoded != tt.req {
				t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, tt.req)
			}
		})
	}
}

// TestExtractToMemoraRequest_JSONTagsPinned pins the wire format.
func TestExtractToMemoraRequest_JSONTagsPinned(t *testing.T) {
	req := extractToMemoraRequest{DryRun: true, IncludeResponses: true}
	data, _ := json.Marshal(req)
	want := `{"dry_run":true,"include_responses":true}`
	if string(data) != want {
		t.Errorf("JSON tag drift: got %s, want %s", string(data), want)
	}
}

// TestHandleSessionContextRoutes_TaskIDRequired 验证 task_id 缺失/空/unknown 的边界.
//
// 这些是路由分发错误, 走 writeError 不依赖 db/registry, 所以可以安全测试.
func TestHandleSessionContextRoutes_TaskIDRequired(t *testing.T) {
	h := &Handler{} // db=nil, memoraClient=nil

	tests := []struct {
		name string
		path string
		want int
	}{
		{"empty path (root)", "/api/system/session-context/", http.StatusNotFound},
		{"trailing slash only", "/api/system/session-context", http.StatusNotFound},
		// Known behaviour (audit pin): empty task_id ("//sub-route")
		// gets 404 "unknown session-context route" instead of 400
		// "task_id required". path "/api/system/session-context//extract-to-memora"
		// splits into ["", "extract-to-memora"], len==2, skips the taskID
		// empty check and enters switch, falls to default 404.
		{"empty task_id (404 not 400)", "/api/system/session-context//extract-to-memora", http.StatusNotFound},
		{"whitespace task_id (gets 400 after TrimSpace)", "/api/system/session-context/%20/extract-to-memora", http.StatusBadRequest},
		{"unknown sub-route", "/api/system/session-context/abc/unknown-route", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader("{}"))
			w := httptest.NewRecorder()
			h.handleSessionContextRoutes(w, r)
			if w.Code != tt.want {
				t.Errorf("path=%s: got %d, want %d body=%s",
					tt.path, w.Code, tt.want, w.Body.String())
			}
		})
	}
}

// TestHandleSessionContextRoutes_MethodNotAllowed 验证 method 校验.
func TestHandleSessionContextRoutes_MethodNotAllowed(t *testing.T) {
	h := &Handler{}
	// task_id valid, route exists, but wrong method
	tests := []struct {
		method string
		path   string
	}{
		{"GET", "/api/system/session-context/abc/extract-to-memora"}, // extract requires POST
		{"POST", "/api/system/session-context/abc/extraction-status"},  // status requires GET
		{"PUT", "/api/system/session-context/abc/summarize-title"},   // whatever the default
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, tt.path, strings.NewReader("{}"))
			w := httptest.NewRecorder()
			h.handleSessionContextRoutes(w, r)
			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s: got %d, want 405 body=%s",
					tt.method, tt.path, w.Code, w.Body.String())
			}
		})
	}
}

// TestHandleSessionContextRoutes_ForbiddenForNonAdmin 验证 super_admin 校验.
//
// Known behaviour (audit pin): 任何角色不是 super_admin / admin_key 都
// 返回 403, 即使 db=nil. 授权检查在 db 检查之前.
func TestHandleSessionContextRoutes_ForbiddenForNonAdmin(t *testing.T) {
	h := &Handler{}
	r := httptest.NewRequest(http.MethodPost, "/api/system/session-context/abc/extract-to-memora", strings.NewReader("{}"))
	w := httptest.NewRecorder()

	h.handleSessionContextRoutes(w, r)

	// No auth set -> IsSuperAdminOrLegacy returns false -> 403
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for non-admin, got %d body=%s", w.Code, w.Body.String())
	}
}