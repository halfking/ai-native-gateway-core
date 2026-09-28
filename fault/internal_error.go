package fault

// internal_error.go — fault 包 500 响应收口 helper（R74 审计轮）。
//
// 与 admin/internal_error.go 同源模式（R72 L4 / R73 二梯队铺开）的 echo
// 版本：fault 是独立包（labstack/echo），不引 admin；客户端只见固定 op
// 文案，真实错误带调用点锚点进 slog。守卫面（admin 包 guard 不覆盖本包）
// 由 R74 轮文档登记，后续轮次按需补静态扫描。

import (
	"log/slog"
	"net/http"
	"path"
	"runtime"
	"strconv"

	"github.com/labstack/echo/v4"
)

func slogCallerFault() string {
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
// {"error": op} wire shape the fault admin API already uses) and logs the
// real error server-side. op must be static text — never compose
// err.Error() into it.
func writeInternalErr(c echo.Context, op string, err error) error {
	slog.Error("fault handler internal error", "op", op, "caller", slogCallerFault(), "err", err)
	return c.JSON(http.StatusInternalServerError, map[string]string{"error": op})
}
