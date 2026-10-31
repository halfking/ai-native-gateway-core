package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/internal/testdb"
)

// r1039_jwt_middleware_revocation_realdb_test.go — 2026-10-28 批判性审计轮补的
// **真库端到端**判据。
//
// ## ★ 为什么必须补这条（本轮审出的第一处「声名」）
//
// r1039 后续轮的 6 条判据（r1039_jwt_middleware_revocation_test.go）**全部 stub 了
// 探针**（换的是 package main 的 `jwtRevocationProbe`）⇒ 它们证明的是
// 「中间件会调用探针」，**不是**「探针真的会吊销」。
//
// ⇒ 生产路径 `admin.RevokedByPasswordChange` → `probeAuthRevocation` →
//   **`settings_kv` 真表** ⇒ 在上一轮里**一次都没被执行过**。
// ⇒ 若那张表的读法、纪元格式、或导出外壳坏了 ⇒ **上一轮的判据照样全绿**。
//
// ⇒ 本条把**整条生产链**在**真库**上跑一遍：
//
//	真实 JWT（签发时间在纪元之前） → 真实 Echo 中间件 → 真实导出外壳 →
//	真实探针 → 真实 settings_kv 行 ⇒ 401 + 「session revoked: password changed」
//
// ## 判据纪律
//
//   - **真库腿不可用时必须显式说明「本次没跑」**（不是安静地跳过）——仓内既有约定。
//   - 用**一次性库**（退出即 DROP）⇒ 不碰 `llm_gateway`。
//   - 必须有**反向对照**（同一个用户删掉纪元行 ⇒ 放行）——否则「一律 401」也能让
//     本条判据全绿。
//   - 必须有**两侧口径对拍**：同一串凭据，ServeMux 侧（`admin.AdminMiddleware`）
//     与 Echo 侧（`newJWTMiddleware`）**必须给出同一个判决** ⇒ 证明「对齐」是真的，
//     而不是「两边各自有个看起来对的实现」。

const r1039RealDBRevokedUser = 424242
const r1039RealDBCleanUser = 424243

// r1039RealDBDSN 取真库 DSN（仓内既有键名）。
func r1039RealDBDSN(t *testing.T) string {
	t.Helper()
	for _, key := range []string{"TEST_DATABASE_URL", "TEST_DB_URL", "TEST_PG_DSN"} {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	t.Skip("TEST_DATABASE_URL / TEST_DB_URL / TEST_PG_DSN 未设置 —— 本次**没有**跑真库端到端验证")
	return ""
}

// r1039CheckCreateDatabase 预检当前角色有没有 CREATEDB。
//
// 为什么预检（与 admin/scratch_db_test.go 同款）：internal/testdb.Create 在建库
// 失败时是 t.Fatalf ⇒ 一个**权限受限**的 CI 角色会把「跑不了」变成「红门」
// ⇒ 那是假红。但也**不能**因此 fallback 到共享库 ⇒ 只能明说没跑。
func r1039CheckCreateDatabase(baseDSN string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, baseDSN)
	if err != nil {
		return errors.New("连接 base DSN 失败: " + err.Error())
	}
	defer pool.Close()
	var can bool
	if err := pool.QueryRow(ctx,
		`SELECT rolcreatedb FROM pg_roles WHERE rolname = current_user`).Scan(&can); err != nil {
		return errors.New("查询 current_user 的 CREATEDB 失败: " + err.Error())
	}
	if !can {
		return errors.New("当前角色没有 CREATEDB —— 一次性库建不出来")
	}
	return nil
}

// r1039RealDBPool 建一次性库 + 只建判据需要的 settings_kv 表，并写入改密纪元。
//
// ★★ **2026-10-28 批判式审计轮·第二次修订**：本函数原先**自己** CREATE / DROP
//   DATABASE，且**没有信号接管** ⇒ 进程被 SIGTERM 时 Go 运行时**不执行**
//   t.Cleanup ⇒ 一次性库**泄漏**（★ 这正是 internal/testdb 存在的原因）。
//   ⇒ 改用**共享 helper** internal/testdb.Create：它带进程级信号注册表
//   （SIGINT/SIGTERM ⇒ 回收全部已登记库 ⇒ 重发信号 ⇒ 仍以「被信号杀死」退出）、
//   `WITH (FORCE)` 回收、120s 清理超时，以及 `mustLandInScratch`
//   （★ 断言 DSN **真的**落在新库上 ⇒ 杜绝「静默跑在共享库上」）。
// ⇒ CREATEDB 预检：权限受限的角色 ⇒ **明说没跑**（SKIP），不让 testdb.Fatalf
//   把「跑不了」变成「红门」。
func r1039RealDBPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	baseDSN := r1039RealDBDSN(t)
	if err := r1039CheckCreateDatabase(baseDSN); err != nil {
		t.Skip(err.Error() + " —— 本次**没有**跑真库端到端验证（刻意：宁可明说没跑，" +
			"也不退回共享库去破坏它）")
	}
	dsn := testdb.Create(t, baseDSN)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("连接一次性库失败: %v", err)
	}
	t.Cleanup(pool.Close)

	// 只建判据需要的表（列集与 sql/schema/01-schema.sql 的 settings_kv 对齐）。
	if _, err := pool.Exec(ctx, `
		CREATE TABLE public.settings_kv (
			key character varying(128) NOT NULL PRIMARY KEY,
			value jsonb NOT NULL,
			value_type character varying(32) NOT NULL,
			scope character varying(16) DEFAULT 'platform'::character varying NOT NULL,
			category character varying(32) DEFAULT 'general'::character varying NOT NULL,
			updated_at timestamp with time zone DEFAULT now() NOT NULL,
			updated_by character varying(64)
		)`); err != nil {
		t.Fatalf("create settings_kv: %v", err)
	}
	// 纪元行：**只给 424242 写**，424243 故意没有 ⇒ 反向对照用。
	if _, err := pool.Exec(ctx, `
		INSERT INTO settings_kv (key, value, value_type, scope, category, updated_at, updated_by)
		VALUES ('admin.auth_revocations',
		        jsonb_build_object($1::text, to_jsonb(now())),
		        'string', 'platform', 'security', now(), 'test:r1039')`,
		fmt.Sprintf("%d", r1039RealDBRevokedUser)); err != nil {
		t.Fatalf("insert revocation epoch: %v", err)
	}
	return pool
}

// TestR1039EchoMiddlewareRevokesAgainstRealDatabase 是本轮补的**真库端到端**判据：
// 真实 JWT → 真实 Echo 中间件 → 真实导出外壳 → 真实探针 → 真实 settings_kv 行。
func TestR1039EchoMiddlewareRevokesAgainstRealDatabase(t *testing.T) {
	t.Setenv("LLM_GATEWAY_JWT_SECRET", r1039JWTSecret)
	pool := r1039RealDBPool(t)

	// 纪元 = 写库那一刻；token 的签发时间在它**之前** ⇒ 必须被吊销。
	issuedAt := time.Now().Add(-time.Hour)
	token := r1039TokenAt(t, r1039RealDBRevokedUser, "super_admin", issuedAt)

	// ① Echo 侧：必须 401 + 逐字同一句提示。
	reached := false
	e := r1039RevocationEcho(t, pool, &reached)
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("真库纪元命中时 Echo 侧必须 401，实际 HTTP %d：%s", rec.Code, rec.Body.String())
	}
	if !contains(rec.Body.String(), "session revoked: password changed") {
		t.Fatalf("401 正文必须逐字含 %q，实际：%s", "session revoked: password changed", rec.Body.String())
	}
	if reached {
		t.Fatal("被吊销的会话不得到达 handler")
	}

	// ② 反向对照：**没有纪元行**的同一形状会话必须放行
	//    （否则「一律 401」也能让本条判据全绿）。
	cleanReached := false
	e2 := r1039RevocationEcho(t, pool, &cleanReached)
	cleanReq := httptest.NewRequest(http.MethodGet, "/probe", nil)
	cleanReq.Header.Set("Authorization", "Bearer "+r1039TokenAt(t, r1039RealDBCleanUser, "super_admin", issuedAt))
	cleanRec := httptest.NewRecorder()
	e2.ServeHTTP(cleanRec, cleanReq)
	if cleanRec.Code != http.StatusOK {
		t.Fatalf("没有纪元行的会话必须放行，实际 HTTP %d：%s", cleanRec.Code, cleanRec.Body.String())
	}
	if !cleanReached {
		t.Fatal("没有纪元行的会话必须到达 handler")
	}
}

// TestR1039EchoAndServeMuxRevocationAgreeOnRealDatabase 是**两侧口径对拍**：
// 同一串凭据、同一张真库，ServeMux 侧（`admin.AdminMiddleware`）与 Echo 侧
// （`newJWTMiddleware`）**必须给出同一个判决**。
//
// ⇒ 这条证明「对齐」是真的：不是「两边各自有个看起来对的实现」。
func TestR1039EchoAndServeMuxRevocationAgreeOnRealDatabase(t *testing.T) {
	t.Setenv("LLM_GATEWAY_JWT_SECRET", r1039JWTSecret)
	pool := r1039RealDBPool(t)
	issuedAt := time.Now().Add(-time.Hour)
	token := r1039TokenAt(t, r1039RealDBRevokedUser, "super_admin", issuedAt)

	// ServeMux 侧（既有实现，本轮没动它）。
	mw := admin.AdminMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), pool, r1039JWTSecret)
	srvReq := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	srvReq.Header.Set("Authorization", "Bearer "+token)
	srvRec := httptest.NewRecorder()
	mw(srvRec, srvReq)

	// Echo 侧（本轮接上的那条）。
	reached := false
	e := r1039RevocationEcho(t, pool, &reached)
	echoReq := httptest.NewRequest(http.MethodGet, "/probe", nil)
	echoReq.Header.Set("Authorization", "Bearer "+token)
	echoRec := httptest.NewRecorder()
	e.ServeHTTP(echoRec, echoReq)

	if srvRec.Code != http.StatusUnauthorized {
		t.Fatalf("ServeMux 侧在真库纪元下必须 401，实际 HTTP %d：%s", srvRec.Code, srvRec.Body.String())
	}
	if echoRec.Code != srvRec.Code {
		t.Fatalf("两侧口径必须一致：ServeMux=%d Echo=%d", srvRec.Code, echoRec.Code)
	}
}

// TestR1039EchoMiddlewareRevocationFailsOpenOnDatabaseOutage 钉住**读失败放行**的
// 真库版本：池不可用（连接被掐断）时，管理面**不得**被锁死。
//
// ★★ **量具坑（第一版红在这里，★ 而红得有道理）**：改密纪元表是**进程内 5s TTL
//   缓存**（`admin/auth_revocation.go` 的 `authRevocationEpoch`）⇒ 前面的用例已经把
//   缓存**焐热**了 ⇒ 此时的探针**根本不碰数据库** ⇒ 「把池关掉」这个前提**不成立**
//   ⇒ 第一版断言「必须放行」拿到的是 401（判据量的是**缓存**，不是**宕机**）。
// ⇒ 正解：**先等缓存过期**（> TTL）再掐断连接 ⇒ 探针真的去读库 ⇒ 读失败 ⇒ 放行。
//   （这与仓内既有纪律同源：**判据红了先怀疑量具**。）
func TestR1039EchoMiddlewareRevocationFailsOpenOnDatabaseOutage(t *testing.T) {
	t.Setenv("LLM_GATEWAY_JWT_SECRET", r1039JWTSecret)
	pool := r1039RealDBPool(t)

	// 等纪元缓存过期（TTL 5s；这里等 6s 留余量）⇒ 随后的探针必须**真的去读库**。
	// ⚠ 缓存是**进程级**的（`admin` 包内），本测试二进制里无法主动失效它
	//   （`invalidateAuthRevocationCache` 未导出）⇒ 只能等它自然过期。
	t.Log("waiting out the 5s revocation-epoch cache TTL so the probe really hits the database")
	time.Sleep(6 * time.Second)

	pool.Close() // 现在关掉连接 ⇒ 探针的读**必然失败**

	reached := false
	e := r1039RevocationEcho(t, pool, &reached)
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer "+r1039TokenAt(t, r1039RealDBRevokedUser, "super_admin", time.Now().Add(-time.Hour)))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("数据库不可用时必须放行（fail-open），实际 HTTP %d：%s", rec.Code, rec.Body.String())
	}
	if !reached {
		t.Fatal("fail-open 路径必须到达 handler")
	}
}
