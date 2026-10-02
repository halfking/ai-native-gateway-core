package sanitize

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

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
	"github.com/stretchr/testify/require"
)

func TestInputSanitizerProtocolTextAndOpaqueMedia(t *testing.T) {
	cases := []struct {
		name, path, body string
		wantRefs         int
		wantPlaceholders int
		// 每个载体放唯一敏感值并逐值断言：messageRef 对未变更 item 也计数，
		// refs/占位符计数钳不住"单类型退回 default 直通"的回归（第二十八轮
		// 变异验证实测），逐值 NotContains 才是承重断言。
		notWant []string
	}{
		{
			name: "chat content blocks", path: "/v1/chat/completions",
			body:     `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"call 13800138000"},{"type":"image_url","image_url":{"url":"data:image/png;base64,13800138000"}}]},{"role":"assistant","content":"prior 13800138000"}]}`,
			wantRefs: 2, wantPlaceholders: 1,
		},
		{
			name: "anthropic system and user blocks", path: "/v1/messages",
			body:     `{"model":"m","max_tokens":16,"system":[{"type":"text","text":"mail a@b.com"}],"messages":[{"role":"user","content":[{"type":"text","text":"call 13800138000"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"13800138000"}}]}]}`,
			wantRefs: 1, wantPlaceholders: 2,
		},
		{
			name: "responses instructions and tool output", path: "/v1/responses",
			body:     `{"model":"m","instructions":"mail a@b.com","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"call 13800138000"},{"type":"input_image","image_url":"data:image/png;base64,13800138000"}]},{"type":"function_call_output","call_id":"c1","output":"phone 13912345678"}]}`,
			wantRefs: 2, wantPlaceholders: 3,
		},
		{
			// 第二十七轮 N1 钉测：reasoning item 的 summary[].text 是客户端
			// 回传的明文，必须入洗；encrypted_content 是供应商密文，原样透传。
			name: "responses reasoning summary", path: "/v1/responses",
			body:     `{"model":"m","input":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"user phone 13812345678"}],"encrypted_content":"enc-13800138000-opaque"}]}`,
			wantRefs: 1, wantPlaceholders: 1,
		},
		{
			// 第二十八轮 A1 钉测：known 输出承载型 item 全族必须入洗——
			// custom_tool_call_output.output / mcp_call.arguments+output /
			// web_search_call.action.query / file_search_call.queries+results[].text。
			// 每个载体唯一敏感值 + 逐值 NotContains（见 notWant 注释）。
			name: "responses output-bearing item families", path: "/v1/responses",
			body: `{"model":"m","input":[` +
				`{"type":"custom_tool_call_output","call_id":"c1","output":"phone 13800138001"},` +
				`{"type":"mcp_call","id":"mcp_1","status":"completed","arguments":"{\"q\":\"call 13800138002\"}","output":"mail a@b.com"},` +
				`{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"call 13800138003"}},` +
				`{"type":"file_search_call","id":"fs_1","status":"completed","queries":["call 13800138004"],"results":[{"file_id":"f1","text":"phone 13912345678"}]}]}`,
			wantRefs:         4,
			wantPlaceholders: 6,
			notWant: []string{
				"13800138001", // custom_tool_call_output.output
				"13800138002", // mcp_call.arguments
				"a@b.com",     // mcp_call.output
				"13800138003", // web_search_call.action.query
				"13800138004", // file_search_call.queries
				"13912345678", // file_search_call.results[].text
			},
		},
		{
			name: "completions prompt array", path: "/v1/completions",
			body:     `{"model":"m","prompt":["call 13800138000","mail a@b.com"]}`,
			wantRefs: 2, wantPlaceholders: 2,
		},
		{
			// 第二十九轮钉测：Codex/工具族输出承载型 item 补漏——
			// local_shell_call_output.output / apply_patch_call_output.output /
			// shell_call_output.outputs[].text（无 type 容器）/
			// code_interpreter_call.code+outputs[].logs / apply_patch_call.action.content。
			name: "responses codex output-bearing item families", path: "/v1/responses",
			body: `{"model":"m","input":[` +
				`{"type":"local_shell_call_output","call_id":"c1","output":"{\"output\":\"phone 13800138011\"}"},` +
				`{"type":"apply_patch_call_output","call_id":"c2","output":"patched ok call 13800138012"},` +
				`{"type":"shell_call_output","call_id":"c3","outputs":[{"text":"call 13800138013"}]},` +
				`{"type":"code_interpreter_call","call_id":"c4","container_id":"k1","code":"print(\"call 13800138014\")","outputs":[{"type":"logs","logs":"phone 13800138015"},{"type":"image","image_url":"data:image/png;base64,opaque"}]},` +
				`{"type":"apply_patch_call","call_id":"c5","action":{"type":"create","path":"a.txt","content":"call 13800138016"}}]}`,
			wantRefs:         5,
			wantPlaceholders: 6,
			notWant: []string{
				"13800138011", // local_shell_call_output.output
				"13800138012", // apply_patch_call_output.output
				"13800138013", // shell_call_output.outputs[].text
				"13800138014", // code_interpreter_call.code
				"13800138015", // code_interpreter_call.outputs[].logs
				"13800138016", // apply_patch_call.action.content
			},
		},
		{
			// 第三十轮钉测：call 侧配对项（*_output 必有对应 call，回放成对）——
			// shell_call / local_shell_call / computer_call 的 action 子树
			// （command[]/env 值/type 动作 text）逐叶子入洗；apply_patch_call
			// 显式 null action 与缺失同义直通（与 web_search_call 同口径）。
			name: "responses codex call-side action families", path: "/v1/responses",
			body: `{"model":"m","input":[` +
				`{"type":"shell_call","call_id":"s1","status":"completed","action":{"type":"exec","command":["echo 13800138021"]}},` +
				`{"type":"local_shell_call","call_id":"s2","status":"completed","action":{"type":"exec","command":["cat 13800138022"],"env":{"TOKEN":"phone 13800138023"},"working_directory":"/tmp"}},` +
				`{"type":"computer_call","call_id":"s3","status":"completed","action":{"type":"type","text":"call 13800138024"}},` +
				`{"type":"apply_patch_call","call_id":"s4","status":"completed","action":null}]}`,
			wantRefs:         4,
			wantPlaceholders: 4,
			notWant: []string{
				"13800138021", // shell_call.action.command[]
				"13800138022", // local_shell_call.action.command[]
				"13800138023", // local_shell_call.action.env.TOKEN
				"13800138024", // computer_call.action.text
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rdb := setupSaniGuardRedis(t)
			s, err := NewSanitizer(NewPatternDetector())
			require.NoError(t, err)
			mw, err := NewSanitizeInputMiddleware(s, rdb, time.Minute)
			require.NoError(t, err)
			var forwarded []byte
			var info compression.SanitizeInfo
			var hasInfo bool
			handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				forwarded, _ = io.ReadAll(r.Body)
				info, hasInfo = compression.SanitizeInfoFromContext(r.Context())
			}))
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("X-Gw-Session-Id", "session-protocols")
			req = req.WithContext(WithAuthenticatedTenant(req.Context(), "tenant-protocols"))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.True(t, hasInfo)
			require.Len(t, info.MessageRefs, tc.wantRefs)
			require.Equal(t, tc.wantPlaceholders, info.Stats.PlaceholderCount)
			for _, secret := range tc.notWant {
				require.NotContains(t, string(forwarded), secret)
			}
			require.NotContains(t, string(forwarded), `"text":"call 13800138000"`)
			if tc.path == "/v1/completions" {
				require.NotContains(t, string(forwarded), "13800138000")
				require.NotContains(t, string(forwarded), "a@b.com")
			}
			require.Contains(t, string(forwarded), "{SENSITIVE:")
			var decoded any
			require.NoError(t, json.Unmarshal(forwarded, &decoded))
			// Opaque media bytes remain intact; replayed assistant text is sanitized.
			if strings.Contains(tc.body, "data:image/png;base64,13800138000") {
				require.Contains(t, string(forwarded), "data:image/png;base64,13800138000")
			}
			if strings.Contains(tc.body, `"data":"13800138000"`) {
				require.Contains(t, string(forwarded), `"data":"13800138000"`)
			}
			if strings.Contains(tc.body, "encrypted_content") {
				// 供应商密文原样透传；summary 明文必须已换成占位符。
				require.Contains(t, string(forwarded), "enc-13800138000-opaque")
				require.NotContains(t, string(forwarded), "13812345678")
			}
			if tc.path == "/v1/chat/completions" {
				require.Contains(t, string(forwarded), "prior {SENSITIVE:phone:1}")
			}
			vals, err := rdb.HGetAll(context.Background(), sanitizeMapKey("tenant-protocols", "session-protocols")).Result()
			require.NoError(t, err)
			require.Len(t, vals, tc.wantPlaceholders)
		})
	}
}

type unavailableInputDetector struct{}

func (unavailableInputDetector) Name() string { return "unavailable" }
func (unavailableInputDetector) Detect(context.Context, string) ([]SensitiveFragment, error) {
	return nil, errors.New("detector unavailable")
}

func TestInputSanitizerDetectorFailureStopsDispatch(t *testing.T) {
	s, err := NewSanitizer(unavailableInputDetector{})
	require.NoError(t, err)
	mw, err := NewSanitizeInputMiddleware(s, nil, time.Minute)
	require.NoError(t, err)
	called := false
	handler := mw.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"messages":[{"role":"user","content":"call 13800138000"}]}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.False(t, called)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.NotContains(t, rec.Body.String(), "13800138000")
	require.Contains(t, rec.Body.String(), `"type":"error"`)
}

func TestInputSanitizerMappingFailureStopsDispatch(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	require.NoError(t, rdb.Close())
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	mw, err := NewSanitizeInputMiddleware(s, rdb, time.Minute)
	require.NoError(t, err)
	called := false
	handler := mw.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"input":"call 13800138000"}`))
	req.Header.Set("X-Gw-Session-Id", "session-fail")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.False(t, called)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.NotContains(t, rec.Body.String(), "13800138000")
}

func TestInputSanitizerOversizedBodyStopsDispatch(t *testing.T) {
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	mw, err := NewSanitizeInputMiddleware(s, nil, time.Minute)
	require.NoError(t, err)
	called := false
	handler := mw.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"messages":[]}`))
	req.ContentLength = maxSanitizeBodySize + 1
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.False(t, called)
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	require.Contains(t, rec.Body.String(), "Request body too large")

	// Unknown/chunked length must still stop after a one-byte overread.
	unknown := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("123456789"))
	unknown.ContentLength = -1
	_, err = readBodyLimit(unknown, 8)
	require.ErrorIs(t, err, errSanitizeBodyTooLarge)
	within := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("12345678"))
	within.ContentLength = -1
	body, err := readBodyLimit(within, 8)
	require.NoError(t, err)
	require.Equal(t, "12345678", string(body))
}

// 第三十轮：action 形态语义口径统一——显式 null 与缺失同义直通，
// 非对象形态才拒单（web_search_call R29 先例，apply_patch_call/shell 族对齐）。
func TestInputSanitizerActionShapeSemantics(t *testing.T) {
	cases := []struct {
		name, item string
		wantStatus int
	}{
		{
			name:       "shell_call non-object action rejected",
			item:       `{"type":"shell_call","call_id":"s1","action":"junk"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "shell_call missing action passes",
			item:       `{"type":"shell_call","call_id":"s2","status":"completed"}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "shell_call null action passes",
			item:       `{"type":"shell_call","call_id":"s3","action":null}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "local_shell_call null action passes",
			item:       `{"type":"local_shell_call","call_id":"s4","action":null}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "computer_call null action passes",
			item:       `{"type":"computer_call","call_id":"s5","action":null}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "apply_patch_call null action passes",
			item:       `{"type":"apply_patch_call","call_id":"s6","action":null}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "apply_patch_call non-object action rejected",
			item:       `{"type":"apply_patch_call","call_id":"s7","action":42}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "web_search_call null action passes (R29 pin)",
			item:       `{"type":"web_search_call","call_id":"s8","action":null}`,
			wantStatus: http.StatusOK,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rdb := setupSaniGuardRedis(t)
			s, err := NewSanitizer(NewPatternDetector())
			require.NoError(t, err)
			mw, err := NewSanitizeInputMiddleware(s, rdb, time.Minute)
			require.NoError(t, err)
			handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body) }))
			body := `{"model":"m","input":[` + tc.item + `]}`
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
			req.Header.Set("X-Gw-Session-Id", "session-action-shape")
			req = req.WithContext(WithAuthenticatedTenant(req.Context(), "tenant-action-shape"))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			require.Equal(t, tc.wantStatus, rec.Code, rec.Body.String())
		})
	}
}
