package autoupdate

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strings"

	"github.com/labstack/echo/v4"
)

// AuthMiddleware P1 修复：autoupdate Admin API 认证中间件
// 支持 Bearer token 认证，token 从环境变量 AUTOUPDATE_ADMIN_TOKEN 读取
func AuthMiddleware() echo.MiddlewareFunc {
	expectedToken := os.Getenv("AUTOUPDATE_ADMIN_TOKEN")
	if expectedToken == "" {
		// 未配置 token 时，默认使用固定值（仅开发环境，生产必须配置）
		expectedToken = "dev-autoupdate-admin-token-change-me"
	}

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// 从 Authorization header 提取 Bearer token
			auth := c.Request().Header.Get("Authorization")
			if auth == "" {
				return c.JSON(http.StatusUnauthorized, map[string]string{
					"error": "missing Authorization header",
					"code":  "missing_auth",
				})
			}

			// 验证 Bearer token 格式
			parts := strings.SplitN(auth, " ", 2)
			if len(parts) != 2 || parts[0] != "Bearer" {
				return c.JSON(http.StatusUnauthorized, map[string]string{
					"error": "invalid Authorization header format, expected Bearer <token>",
					"code":  "invalid_auth_format",
				})
			}

			providedToken := parts[1]

			// 使用 constant-time 比较防止时序攻击
			if subtle.ConstantTimeCompare([]byte(expectedToken), []byte(providedToken)) != 1 {
				return c.JSON(http.StatusUnauthorized, map[string]string{
					"error": "invalid token",
					"code":  "invalid_token",
				})
			}

			return next(c)
		}
	}
}
