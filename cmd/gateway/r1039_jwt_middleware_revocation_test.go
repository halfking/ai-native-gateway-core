package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/kaixuan/llm-gateway-go/admin"
)

// r1039_jwt_middleware_revocation_test.go — 审计档
// docs/audit/2026-10-20-r1039-脱敏阻断与设置界面审计轮.md §8 第 9 条的判据。
//
// ## 遗留项本体（既有差异，不是本轮新引入的缺陷）
//
// 「改密即吊销」这条策略**只有一半存在**：
//
//   - ServeMux 侧（admin/auth.go）：AdminMiddleware / SuperAdminMiddleware /
//     tenantAdminOrSuperAdmin / provider_access **全都**带「改密吊销探测」
//     ⇒ 改密之后，用旧 token 的会话**当场 401**；
//   - Echo 侧（cmd/gateway/main_jwt_middleware.go）：**没有**这个探测
//     ⇒ 改密之后，同一串旧 cookie/token 在 Echo 注册的 `/api/admin/*` 端点上
//     **继续全权可用**（实测影响面：/api/admin/tiers、/api/admin/licenses、
//     /api/admin/vibecoding/*、/api/admin/faults、/api/admin/releases、
//     /api/admin/autoupdate、/api/admin/center/*、/api/admin/routing-opt/ml）。
//
// ⇒ 同一套账号体系下，**换个 HTTP 框架就绕过了改密吊销**。
//
// ## 本轮做了什么（只做「对齐」，不新增策略）
//
// Echo 侧接上**同一份**探测实现（admin.RevokedByPasswordChange → 同一个
// authRevocationProbe 变量、同一份 5s TTL 缓存、同一条「读失败一律放行」的尺子）。
// ⇒ 不新增任何撤销条件，也不放宽任何既有条件。
//
// ## 判据纪律
//
//   - 判据打**中间件本身**（Echo + 真实 JWT + 真实 cookie），不是打 helper：
//     断言「helper 返回 true」若只在 helper 层断言，中间件没调用它就照样全绿。
//   - 每条放行用例都配**对照**：同一串凭据在「已改密」与「未改密」两种纪元下
//     必须给出**相反**的结果（否则「一律 401」也会让拒绝类判据全绿）。
//   - 401 的**正文**必须逐字含 "session revoked: password changed"
//     —— 与 ServeMux 侧同一句提示（文案是契约，客户端据此提示「请重新登录」）。

// r1039RevocationStub 换掉中间件看到的吊销探针，并返回还原函数。
func r1039RevocationStub(t *testing.T, fn func(ctx context.Context, db *pgxpool.Pool, userID int, issuedAt time.Time) bool) {
	t.Helper()
	old := jwtRevocationProbe
	jwtRevocationProbe = fn
	t.Cleanup(func() { jwtRevocationProbe = old })
}

// r1039TokenAt 签一串**签发时间可控**的 JWT（admin.SignToken 永远盖 now）。
func r1039TokenAt(t *testing.T, userID int, role string, issuedAt time.Time) string {
	t.Helper()
	claims := admin.JWTClaims{
		UserID:   userID,
		TenantID: "acme",
		Username: "tester",
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(issuedAt.Add(24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(issuedAt),
			Issuer:    "llm-gateway",
			Audience:  jwt.ClaimStrings{admin.AudienceName},
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(r1039JWTSecret))
	if err != nil {
		t.Fatalf("签 token 失败: %v", err)
	}
	return signed
}

// r1039RevocationEcho 造一个只挂本中间件 + 一个记录是否到达 handler 的 Echo。
//
// ★ db 是**显式参数**：上一轮这个助手把池写死成 nil ⇒ 探针恒 fail-open ⇒
//   「真库端到端」那条判据会**恒不吊销**（编译器在第一次跑时抓到了它）。
//   ⇒ 池必须由调用方给，且**真库腿必须传真池**。
func r1039RevocationEcho(t *testing.T, db *pgxpool.Pool, reached *bool) *echo.Echo {
	t.Helper()
	e := echo.New()
	e.Use(newJWTMiddleware(r1039JWTSecret, db))
	e.GET("/probe", func(c echo.Context) error {
		*reached = true
		role, _ := c.Get("role").(string)
		return c.JSON(http.StatusOK, map[string]any{"role": role})
	})
	return e
}

// TestR1039EchoMiddlewareRevokesBearerAfterPasswordChange 钉住**核心修复**：
// 改密纪元之前签发的 Bearer token 必须被拒（401），且**不得**到达 handler。
//
// ⚠ 这是本条遗留项在 Echo 侧的**唯一**可观测差异：把中间件里的探测调用删掉
// （回到修复前形态），本用例必须变红。
func TestR1039EchoMiddlewareRevokesBearerAfterPasswordChange(t *testing.T) {
	t.Setenv("LLM_GATEWAY_JWT_SECRET", r1039JWTSecret)
	epoch := time.Now().Add(-time.Minute)
	r1039RevocationStub(t, func(_ context.Context, _ *pgxpool.Pool, _ int, issuedAt time.Time) bool {
		return issuedAt.Before(epoch)
	})

	reached := false
	e := r1039RevocationEcho(t, nil, &reached)
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer "+r1039TokenAt(t, 42, "super_admin", time.Now().Add(-time.Hour)))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("改密前签发的 token 必须 401，实际 HTTP %d：%s", rec.Code, rec.Body.String())
	}
	if !contains(rec.Body.String(), "session revoked: password changed") {
		t.Fatalf("401 正文必须逐字含 %q（与 ServeMux 侧同一句提示），实际：%s",
			"session revoked: password changed", rec.Body.String())
	}
	if reached {
		t.Fatal("被吊销的会话不得到达 handler")
	}
}

// TestR1039EchoMiddlewareRevokesCookieSessionAfterPasswordChange 钉住**浏览器那条路**：
// 前端登录后靠 HttpOnly cookie 鉴权（刻意不发 Authorization 头）⇒
// 改密之后，**cookie 会话**必须同样被拒。
//
// ⇒ 这一条是上一条的**必要对照**：只钉 Bearer 的话，「把 cookie 路径漏掉」照样全绿。
func TestR1039EchoMiddlewareRevokesCookieSessionAfterPasswordChange(t *testing.T) {
	t.Setenv("LLM_GATEWAY_JWT_SECRET", r1039JWTSecret)
	epoch := time.Now().Add(-time.Minute)
	r1039RevocationStub(t, func(_ context.Context, _ *pgxpool.Pool, _ int, issuedAt time.Time) bool {
		return issuedAt.Before(epoch)
	})

	reached := false
	e := r1039RevocationEcho(t, nil, &reached)
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.AddCookie(&http.Cookie{Name: admin.CookieName, Value: r1039TokenAt(t, 42, "super_admin", time.Now().Add(-time.Hour))})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("改密前的 cookie 会话必须 401，实际 HTTP %d：%s", rec.Code, rec.Body.String())
	}
	if !contains(rec.Body.String(), "session revoked: password changed") {
		t.Fatalf("401 正文必须逐字含 %q，实际：%s",
			"session revoked: password changed", rec.Body.String())
	}
	if reached {
		t.Fatal("被吊销的会话不得到达 handler")
	}
}

// TestR1039EchoMiddlewareKeepsSessionIssuedAfterPasswordChange 钉住**反向对照**：
// 改密**之后**签发的会话必须照旧可用（否则这条策略会把正常登录也打死）。
//
// ⇒ 没有这一条，「一律 401」也能让上一条判据全绿。
func TestR1039EchoMiddlewareKeepsSessionIssuedAfterPasswordChange(t *testing.T) {
	t.Setenv("LLM_GATEWAY_JWT_SECRET", r1039JWTSecret)
	epoch := time.Now().Add(-time.Minute)
	r1039RevocationStub(t, func(_ context.Context, _ *pgxpool.Pool, _ int, issuedAt time.Time) bool {
		return issuedAt.Before(epoch)
	})

	reached := false
	e := r1039RevocationEcho(t, nil, &reached)
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer "+r1039TokenAt(t, 42, "super_admin", time.Now().Add(time.Minute)))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("改密后签发的 token 必须照旧可用，实际 HTTP %d：%s", rec.Code, rec.Body.String())
	}
	if !reached {
		t.Fatal("未被吊销的会话必须到达 handler")
	}
}

// TestR1039EchoMiddlewareRevocationProbeSeesTokenClaims 钉住**探针拿到的到底是什么**：
// user_id 与**签发时间**必须来自 token 的 claims。
//
// ⇒ 这一条杀掉两种「看起来对」的接线错误：
//   - 传 `time.Now()` 而不是 claims.IssuedAt ⇒ 吊销判定永远为假（**漏放行**）；
//   - 传零值 ⇒ 零值早于任何纪元 ⇒ **一切会话都被吊销**。
func TestR1039EchoMiddlewareRevocationProbeSeesTokenClaims(t *testing.T) {
	t.Setenv("LLM_GATEWAY_JWT_SECRET", r1039JWTSecret)

	type call struct {
		userID   int
		issuedAt time.Time
		db       *pgxpool.Pool
	}
	var calls []call
	r1039RevocationStub(t, func(_ context.Context, db *pgxpool.Pool, userID int, issuedAt time.Time) bool {
		calls = append(calls, call{userID: userID, issuedAt: issuedAt, db: db})
		return false
	})

	issuedAt := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	reached := false
	e := r1039RevocationEcho(t, nil, &reached)
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer "+r1039TokenAt(t, 77, "super_admin", issuedAt))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("探针返回未吊销时必须放行，实际 HTTP %d：%s", rec.Code, rec.Body.String())
	}
	if len(calls) != 1 {
		t.Fatalf("探针必须恰好被调用一次（每次请求一次），实际 %d 次", len(calls))
	}
	got := calls[0]
	if got.userID != 77 {
		t.Fatalf("探针拿到的 user_id 必须是 token 里的 77，实际 %d", got.userID)
	}
	if diff := got.issuedAt.Sub(issuedAt); diff > time.Second || diff < -time.Second {
		t.Fatalf("探针拿到的签发时间必须是 token 的 iat（%v），实际 %v", issuedAt, got.issuedAt)
	}
}

// TestR1039EchoMiddlewareRevocationFailsOpenWithoutDatabase 钉住**读失败放行**：
// 没有数据库（db == nil）时，吊销探测必须**放行**而不是把整个管理面锁死在 401。
//
// ⇒ 与 ServeMux 侧同一语义（settings_kv 不可用不能锁死管理面）。
func TestR1039EchoMiddlewareRevocationFailsOpenWithoutDatabase(t *testing.T) {
	t.Setenv("LLM_GATEWAY_JWT_SECRET", r1039JWTSecret)

	// 真实探针（不 stub）：nil 库 ⇒ fail-open。
	if admin.RevokedByPasswordChange(context.Background(), nil, 42, time.Now()) {
		t.Fatal("nil 库时吊销探测必须 fail-open（放行）")
	}

	reached := false
	e := r1039RevocationEcho(t, nil, &reached)
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer "+r1039TokenAt(t, 42, "super_admin", time.Now().Add(-time.Hour)))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("没有数据库时必须放行（fail-open），实际 HTTP %d：%s", rec.Code, rec.Body.String())
	}
	if !reached {
		t.Fatal("fail-open 路径必须到达 handler")
	}
}

// TestR1039EchoMiddlewareRevocationDoesNotShadowCredentialErrors 钉住**拒绝侧不被串味**：
// 凭据本身坏掉时，401 正文必须是「invalid or expired token」，
// 不能被改密吊销那句覆盖掉（否则运维分不清是「会话过期」还是「改密踢出」）。
func TestR1039EchoMiddlewareRevocationDoesNotShadowCredentialErrors(t *testing.T) {
	t.Setenv("LLM_GATEWAY_JWT_SECRET", r1039JWTSecret)
	r1039RevocationStub(t, func(context.Context, *pgxpool.Pool, int, time.Time) bool { return true })

	reached := false
	e := r1039RevocationEcho(t, nil, &reached)
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer not-a-jwt")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("坏凭据必须 401，实际 HTTP %d：%s", rec.Code, rec.Body.String())
	}
	if contains(rec.Body.String(), "session revoked: password changed") {
		t.Fatalf("坏凭据的 401 正文不得被改密吊销那句覆盖：%s", rec.Body.String())
	}
	if !contains(rec.Body.String(), "invalid or expired token") {
		t.Fatalf("坏凭据的 401 正文必须是 %q，实际：%s", "invalid or expired token", rec.Body.String())
	}
}
