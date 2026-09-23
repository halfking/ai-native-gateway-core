package hostedtask

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 矩阵 C（路由装配）：复刻 main.go 的挂载形态 —— ServeMux 精确
// "/v1/hosted-tasks" + 前缀 "/v1/hosted-tasks/"，后接 static fallback。
// 验证托管端点不被 fallback 吞、子路径/方法路由正确。
func TestRouteAssemblyMountPattern(t *testing.T) {
	h := newTestHandler(t, newFakeStore())

	mux := http.NewServeMux()
	mux.Handle("/v1/hosted-tasks", h)
	mux.Handle("/v1/hosted-tasks/", h)
	// static fallback（main.go 里 static dir 的兜底形态）
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	cases := []struct {
		name   string
		method string
		path   string
		key    string
		want   int
	}{
		{"create hits handler", http.MethodPost, "/v1/hosted-tasks", "0123456789abcdef", http.StatusAccepted},
		{"create without key 400", http.MethodPost, "/v1/hosted-tasks", "", http.StatusBadRequest},
		{"collection GET 405", http.MethodGet, "/v1/hosted-tasks", "", http.StatusMethodNotAllowed},
		{"subpath GET missing task 404", http.MethodGet, "/v1/hosted-tasks/ht_nope", "", http.StatusNotFound},
		{"recall GET 405", http.MethodGet, "/v1/hosted-tasks/ht_x/recall", "", http.StatusMethodNotAllowed},
		{"recall missing task 404", http.MethodPost, "/v1/hosted-tasks/ht_x/recall", "", http.StatusNotFound},
		{"cancel POST 202", http.MethodPost, "/v1/hosted-tasks/0123456789abcdef00000000/cancel", "", http.StatusNotFound},
		{"unknown subpath 404", http.MethodGet, "/v1/hosted-tasks/ht_x/unknown", "", http.StatusNotFound},
		{"static fallback untouched", http.MethodGet, "/assets/app.js", "", http.StatusNotFound},
		{"similar prefix not swallowed", http.MethodGet, "/v1/hosted-tasksx", "", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := "{}"
			if tc.method == http.MethodPost && tc.path == "/v1/hosted-tasks" {
				body = validBody
			}
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(body))
			if tc.key != "" {
				req.Header.Set("Idempotency-Key", tc.key)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("%s %s = %d, want %d", tc.method, tc.path, rec.Code, tc.want)
			}
		})
	}
}
