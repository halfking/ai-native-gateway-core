package main

// auth_rejection_logging_test.go — 2026-10-16 事故回归门。
//
// 事故：zcode/ZCode 用 http://llm.kxpms.cn/v1 请求 minimax-m3，边缘 nginx
// 对 :80 一律 `301 https://$host$request_uri`；客户端按 fetch 规范跟随时
// Authorization 被跨源剥离、POST 降级成 GET，落到 AuthMiddleware 得到
// 401 `{"code":"missing_key"}`，zcode 报 `reason=auth_failed`。那把 key
// 当时是有效的（api_keys id=89，enabled/active）。
//
// 网关侧的真实缺陷不是鉴权判错，而是**这次拒绝在网关日志里查无此物**：
// AuthMiddleware 挂在 LoggingMiddleware 之外，被它拒掉的请求不产生
// http_request 记录；missing_key 分支本身又一条日志都不打。客户端拿到的
// X-Request-Id（19d7d64a…）在 154 两份实例日志里 grep 命中 0 次，
// 定位只能绕到 nginx access log。
//
// 本门两层（房规同 mobile_security_headers_test.go）：
//  1. 行为面——未带凭据的请求必须同时留下 http_request 记录与带 code 的
//     auth 拒绝日志，且两者都带 user_agent（唯一能把调用方指认出来的字段）。
//  2. 接线面——main.go 里 Logging 必须排在 Auth 之外、RequestID 之内；
//     挪位不会让任何行为测试变红，只有钉住调用点形状才能防回归。

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/middleware"
)

// authRejectChain 按 main.go 的相对次序复刻鉴权相关的三段：
// RequestID → Logging → Auth。行为面的意义就在这个次序。
func authRejectChain(t *testing.T, inner http.Handler) http.Handler {
	t.Helper()
	return middleware.NewBuilder().
		Add(middleware.NewRequestIDMiddleware()).
		Add(middleware.NewLoggingMiddleware()).
		Add(middleware.NewAuthMiddleware("test-global-key")).
		Build().
		Then(inner)
}

func captureAuthRejectLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	leveler := new(slog.LevelVar)
	leveler.Set(slog.LevelDebug)
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: leveler})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func recordsOf(t *testing.T, buf *bytes.Buffer, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("日志行不是合法 JSON: %v\nline=%s", err, line)
		}
		if rec["msg"] == msg {
			out = append(out, rec)
		}
	}
	return out
}

// 行为面：未带凭据的数据面请求必须 401 missing_key，且两条日志都在。
func TestAuthRejectionIsGreppableInGatewayLogs(t *testing.T) {
	buf := captureAuthRejectLogs(t)
	h := authRejectChain(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("未带凭据的请求不应触达内层 handler")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"minimax-m3"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ZCode/3.15.1 ai-sdk/provider-utils/4.0.27 runtime/node.js/24")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d，期望 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"missing_key"`) {
		t.Fatalf("响应体缺 missing_key：%s", rec.Body.String())
	}

	// (1) http_request 记录必须存在，且能按 X-Request-Id 串起来。
	httpRecs := recordsOf(t, buf, "http_request")
	if len(httpRecs) != 1 {
		t.Fatalf("http_request 记录数 = %d，期望 1 —— LoggingMiddleware 若被挪到 Auth 内侧，"+
			"被鉴权拒绝的请求将不留任何访问记录（2026-10-16 事故根因）", len(httpRecs))
	}
	if got := httpRecs[0]["status"]; got != float64(http.StatusUnauthorized) {
		t.Errorf("http_request.status = %v，期望 401", got)
	}
	if got := httpRecs[0]["request_id"]; got != rec.Header().Get("X-Request-Id") {
		t.Errorf("http_request.request_id = %v，与响应头 X-Request-Id=%q 不一致",
			got, rec.Header().Get("X-Request-Id"))
	}
	if got := httpRecs[0]["user_agent"]; got != req.Header.Get("User-Agent") {
		t.Errorf("http_request.user_agent = %v，期望 %q —— 没有它就无法把 401 指认回调用方",
			got, req.Header.Get("User-Agent"))
	}

	// (2) auth 拒绝日志必须带机器可读的 code 与调用方线索。
	authRecs := recordsOf(t, buf, "auth: missing API key")
	if len(authRecs) != 1 {
		t.Fatalf("auth 拒绝日志数 = %d，期望 1 —— missing_key 分支若无日志，"+
			"该类故障只能绕到 nginx 日志反推（2026-10-16 事故）", len(authRecs))
	}
	if got := authRecs[0]["code"]; got != "missing_key" {
		t.Errorf("auth 拒绝日志 code = %v，期望 missing_key", got)
	}
	if got := authRecs[0]["user_agent"]; got != req.Header.Get("User-Agent") {
		t.Errorf("auth 拒绝日志 user_agent = %v，期望 %q", got, req.Header.Get("User-Agent"))
	}
	if got := authRecs[0]["path"]; got != "/v1/responses" {
		t.Errorf("auth 拒绝日志 path = %v，期望 /v1/responses", got)
	}
}

// 行为面（对照）：带了一个非 sk- 的错钥匙，仍走 invalid_key 分支且同样留痕。
// 防止只补了 missing_key 一侧、把 invalid_key 的既有线索弄丢。
func TestAuthRejectionInvalidKeyStillLogsCode(t *testing.T) {
	buf := captureAuthRejectLogs(t)
	h := authRejectChain(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("错钥匙不应触达内层 handler")
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer wrong-key-value")
	req.Header.Set("User-Agent", "test-client/1.0")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d，期望 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"invalid_key"`) {
		t.Fatalf("响应体缺 invalid_key：%s", rec.Body.String())
	}
	authRecs := recordsOf(t, buf, "auth: invalid API key")
	if len(authRecs) != 1 {
		t.Fatalf("invalid_key 拒绝日志数 = %d，期望 1", len(authRecs))
	}
	if got := authRecs[0]["code"]; got != "invalid_key" {
		t.Errorf("invalid_key 日志 code = %v，期望 invalid_key", got)
	}
	if got := authRecs[0]["key_prefix"]; got != "wrong-ke****" {
		t.Errorf("invalid_key 日志 key_prefix = %v，期望 wrong-ke****", got)
	}
	if got := authRecs[0]["user_agent"]; got != "test-client/1.0" {
		t.Errorf("invalid_key 日志 user_agent = %v，期望 test-client/1.0", got)
	}
	if n := len(recordsOf(t, buf, "http_request")); n != 1 {
		t.Errorf("http_request 记录数 = %d，期望 1", n)
	}
}

// 接线面：Logging 必须在 Auth 之外（否则行为面第 (1) 条失守），
// 且必须在 RequestID 之内（RequestIDMiddleware 把生成的 id 写回
// r.Header["X-Request-Id"]，再往内排会让记录里的 request_id 退化）。
// 文本锚定的理由与 TestMobileMountSitsInsideMiddlewareChain 相同：
// 挪位不会让任何行为测试变红。
func TestLoggingMiddlewareSitsOutsideAuthAndInsideRequestID(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)

	const (
		markReqID = "Add(middleware.NewRequestIDMiddleware())"
		markLog   = "Add(middleware.NewLoggingMiddleware())"
		markAuth  = "Add(middleware.NewAuthMiddleware(cfg.APIKey))"
	)
	iReq, iLog, iAuth := strings.Index(s, markReqID), strings.Index(s, markLog), strings.Index(s, markAuth)
	if iReq < 0 || iLog < 0 || iAuth < 0 {
		t.Fatalf("main.go 未找到链序锚点（request_id=%d logging=%d auth=%d）", iReq, iLog, iAuth)
	}
	if iLog > iAuth {
		t.Fatal("main.go 里 LoggingMiddleware 又被挪到 AuthMiddleware 内侧 —— " +
			"鉴权拒绝将不产生 http_request 记录（2026-10-16 事故回归）")
	}
	if iLog < iReq {
		t.Fatal("main.go 里 LoggingMiddleware 被挪到 RequestIDMiddleware 外侧 —— " +
			"本记录的 request_id 会退化成客户端原值或空值，无法与响应头 X-Request-Id 对齐")
	}
	if n := strings.Count(s, markLog); n != 1 {
		t.Fatalf("main.go 里 NewLoggingMiddleware 出现 %d 次，期望 1 —— 链序断言会认错调用点", n)
	}
}
