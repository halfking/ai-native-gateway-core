package admin

// internal_error.go — 统一的 admin 500 响应收口 helper（R72 审计轮 L4）。
//
// 背景：admin 包 ~400 处 500 响应直接把 err.Error() 拼进响应体
// （writeError(w, 500, "query failed: "+err.Error()) / http.Error(w,
// err.Error(), 500)），pgx/驱动内部错误串原样到达客户端——R71 F7 先在
// session detail/list v2 两个端点收口（固定文案 + slog），本轮把同一
// 机制批量铺开。
//
// 约定：
//   - 客户端只见到 op（固定的操作描述，如 "query failed"），绝不携带
//     err.Error()；
//   - 真实错误带调用点 file:line 锚点进 slog，服务端可排查；
//   - 调用方不需要再手写一行 slog。
//
// 守卫：internal_error_guard_test.go 静态扫描 admin 包源码，禁止任何
// 500 响应路径重新引入 err.Error()（白名单机制与 sqlreadguard 同款）。

import (
	"log/slog"
	"net/http"
	"path"
	"runtime"
	"strconv"
)

// slogCaller returns "file:line" of the first caller outside this file,
// so every log line carries the handler site that produced the error.
func slogCaller() string {
	for skip := 2; skip < 8; skip++ {
		_, file, line, ok := runtime.Caller(skip)
		if !ok {
			break
		}
		if path.Base(file) != "internal_error.go" {
			return path.Base(file) + ":" + strconv.Itoa(line)
		}
	}
	return "unknown"
}

// writeInternalErr responds 500 with the fixed op copy (JSON, same shape as
// writeError) and logs the real error server-side. op must be static text —
// never compose err.Error() into it.
func writeInternalErr(w http.ResponseWriter, op string, err error) {
	slog.Error("admin handler internal error", "op", op, "caller", slogCaller(), "err", err)
	writeError(w, http.StatusInternalServerError, op)
}

// writeInternalTextErr is the text/plain twin for handlers that answer via
// http.Error (the response shape is part of those endpoints' contract).
func writeInternalTextErr(w http.ResponseWriter, op string, err error) {
	slog.Error("admin handler internal error", "op", op, "caller", slogCaller(), "err", err)
	http.Error(w, op, http.StatusInternalServerError)
}

// writeInternalErrStr is the string-shape twin: endpoints whose error body
// is the flat `{"error":"…"}` (writeJSON + map[string]string) keep that wire
// shape, only the leaked err.Error() suffix is replaced by the fixed op copy.
func writeInternalErrStr(w http.ResponseWriter, op string, err error) {
	slog.Error("admin handler internal error", "op", op, "caller", slogCaller(), "err", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": op})
}
