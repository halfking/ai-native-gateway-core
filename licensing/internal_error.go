package licensing

// internal_error.go — licensing 包 500 响应收口 helper（R74 审计轮）。
//
// 与 admin/internal_error.go（R72 L4）/ fault/internal_error.go 同源模式：
// 客户端只见固定 op 文案，真实错误带调用点锚点进 slog。pgx/驱动内部错误
// 串（SQL/DSN/路径）不再到达客户端。

import (
	"log/slog"
	"net/http"
	"path"
	"runtime"
	"strconv"

	"github.com/labstack/echo/v4"
)

func slogCallerLicensing() string {
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

// writeInternalErr responds 500 with the fixed op copy (same flat
// {"error": op} wire shape the licensing admin API already uses) and logs
// the real error server-side. op must be static text — never compose
// err.Error() into it.
func writeInternalErr(c echo.Context, op string, err error) error {
	slog.Error("licensing handler internal error", "op", op, "caller", slogCallerLicensing(), "err", err)
	return c.JSON(http.StatusInternalServerError, map[string]string{"error": op})
}
