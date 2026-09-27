// Package admin — storage_degraded_test.go
//
// Subtask 4（handoff §6）降级路径单测。两层：
//  1. IsStorageUnavailable 的分类判据（连接层 vs 服务端 SQL 错误）；
//  2. 三个端点在池子为空 / 查询失败时确实回 503 + storage_status。
//
// 每条断言都有对应的变异测试记录（见 commit message 与 §22）。
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// ---------------------------------------------------------------------------
// 1. 分类器
// ---------------------------------------------------------------------------

// netTimeoutError 是一个 net.Error 且 Timeout() 为 true 的桩。
type netTimeoutError struct{}

func (netTimeoutError) Error() string   { return "i/o timeout" }
func (netTimeoutError) Timeout() bool   { return true }
func (netTimeoutError) Temporary() bool { return true }

var _ net.Error = netTimeoutError{}

func TestIsStorageUnavailable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		// ---- 判为降级（连接层故障） ----
		{"nil error is not degraded", nil, false},
		{
			"nil pool sentinel",
			ErrNilDatabasePool,
			true,
		},
		{
			"nil pool sentinel wrapped by withTx's caller",
			fmt.Errorf("begin database tx: %w", ErrNilDatabasePool),
			true,
		},
		{
			"network timeout",
			netTimeoutError{},
			true,
		},
		{
			"context deadline exceeded",
			context.DeadlineExceeded,
			true,
		},
		{
			"wrapped context deadline",
			fmt.Errorf("query turns: %w", context.DeadlineExceeded),
			true,
		},

		// ---- 不判为降级（请求/查询本身的问题，重试无意义） ----
		{
			"context canceled is the client leaving, not the store",
			context.Canceled,
			false,
		},
		{
			"pg error (syntax / permission / RLS) stays 500",
			&pgconn.PgError{Code: "42501", Message: "permission denied for table session_turns"},
			false,
		},
		{
			"pg undefined table stays 500",
			&pgconn.PgError{Code: "42P01", Message: `relation "nope" does not exist`},
			false,
		},
		{
			"plain error stays 500",
			errors.New("unexpected EOF"),
			false,
		},
		{
			"non-timeout net error stays 500",
			net.InvalidAddrError("no route"),
			false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsStorageUnavailable(tc.err); got != tc.want {
				t.Errorf("IsStorageUnavailable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// context.Canceled 必须在 DeadlineExceeded 之前独立判：若先判
// context.DeadlineExceeded 分支，client 主动取消会被误报成存储降级。
// TestIsStorageUnavailable_ConnectError 用一次真实的回环连接失败造出
// *pgconn.ConnectError。
//
// 为什么不直接构造字面量：pgconn.ConnectError 的内部 err 字段未导出，
// `&pgconn.ConnectError{Config: ...}` 的 Error() 会解引用 nil 而 panic
// （分类器正是被这条打到过）。127.0.0.1:1 是本机保留端口，connect 立即
// ECONNREFUSED，不产生任何外部网络流量。
func TestIsStorageUnavailable_ConnectError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := pgconn.Connect(ctx, "postgres://nobody@127.0.0.1:1/none")
	if err == nil {
		t.Skip("unexpected: connect to 127.0.0.1:1 succeeded")
	}
	var connectErr *pgconn.ConnectError
	if !errors.As(err, &connectErr) {
		t.Fatalf("expected *pgconn.ConnectError, got %T: %v", err, err)
	}
	if !IsStorageUnavailable(err) {
		t.Errorf("IsStorageUnavailable(*pgconn.ConnectError) = false, want true")
	}
	// withTx 的包裹形态也要判为降级。
	if !IsStorageUnavailable(fmt.Errorf("begin database tx: %w", err)) {
		t.Errorf("wrapped *pgconn.ConnectError must be classified as unavailable")
	}
}

// TestIsStorageUnavailable_CanceledWinsOverDeadline 是 Canceled 守卫唯一
// 真正起判别作用的输入：一个**同时**满足 errors.Is(Canceled) 与
// errors.Is(DeadlineExceeded) 的 joined 错误（Go 1.20+ 多重 %w；嵌套 ctx /
// errgroup 合并时真的会出现）。
//
// 2026-09-28 变异测试 S2 实测：原断言只喂 context.Canceled 时，删掉实现里的
// Canceled 守卫**测试照样全绿** —— 纯 Canceled 既不匹配 Deadline 分支，也不
// 匹配 ConnectError / net.Error，靠的是「都不命中」的兜底返回。那条断言等于
// 没覆盖守卫本身。joined 错误才真正需要它：有守卫 -> false（客户端走人，不是
// 存储的问题）；无守卫 -> 命中 Deadline 分支 -> true（把客户端取消误报成存储
// 降级，凭空造一次假的 503 告警）。
func TestIsStorageUnavailable_CanceledWinsOverDeadline(t *testing.T) {
	joined := fmt.Errorf("query: %w: %w", context.Canceled, context.DeadlineExceeded)
	if !errors.Is(joined, context.Canceled) || !errors.Is(joined, context.DeadlineExceeded) {
		t.Fatal("test fixture error: joined error must match both sentinels")
	}
	if IsStorageUnavailable(joined) {
		t.Error("joined Canceled+Deadline error must NOT be storage-unavailable " +
			"(client went away; reporting 503 here fabricates an outage)")
	}
	// 纯 Canceled 也要钉住（它靠兜底返回 false，同样是契约的一部分）。
	if IsStorageUnavailable(context.Canceled) {
		t.Error("context.Canceled must not be storage-unavailable")
	}
	if IsStorageUnavailable(fmt.Errorf("wrapped: %w", context.Canceled)) {
		t.Error("wrapped context.Canceled must not be storage-unavailable")
	}
}

// ---------------------------------------------------------------------------
// 2. 响应体
// ---------------------------------------------------------------------------

func TestWriteStorageDegradedBody(t *testing.T) {
	rec := httptest.NewRecorder()
	writeStorageDegradedBody(rec, "list")

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body is not JSON: %v (raw=%q)", err, rec.Body.String())
	}
	if got := body["storage_status"]; got != StorageStatusUnavailable {
		t.Errorf("storage_status = %v, want %q", got, StorageStatusUnavailable)
	}
	if got := body["component"]; got != "list" {
		t.Errorf("component = %v, want %q", got, "list")
	}
	if got := body["retryable"]; got != true {
		t.Errorf("retryable = %v, want true", got)
	}
	// 降级响应不得回显内部错误串（连接错误里带主机名/端口/DSN 片段）。
	raw := rec.Body.String()
	for _, leak := range []string{"db.internal", "postgres://", "password", "error\":"} {
		if strings.Contains(raw, leak) {
			t.Errorf("degraded response leaks %q: %s", leak, raw)
		}
	}
}

// ---------------------------------------------------------------------------
// 3. 端点接线：池子为空必须回 503 + storage_status
// ---------------------------------------------------------------------------

func assertDegraded(t *testing.T, rec *httptest.ResponseRecorder, component string) {
	t.Helper()
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body=%q)", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v (raw=%q)", err, rec.Body.String())
	}
	if body["storage_status"] != StorageStatusUnavailable {
		t.Errorf("storage_status = %v, want %q", body["storage_status"], StorageStatusUnavailable)
	}
	if body["component"] != component {
		t.Errorf("component = %v, want %q", body["component"], component)
	}
}

// TestSessionDetailV2_NilPoolDegraded 锁住 detail 端点的降级形态。
func TestSessionDetailV2_NilPoolDegraded(t *testing.T) {
	api := &SessionDetailV2API{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/sessions/detail?session_id=s1", nil)
	api.ServeHTTP(rec, req)
	assertDegraded(t, rec, "detail")
}

// TestSessionListV2_NilPoolDegraded 锁住 list v2 端点的降级形态。
func TestSessionListV2_NilPoolDegraded(t *testing.T) {
	api := &SessionListV2API{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/sessions/list", nil)
	api.ServeHTTP(rec, req)
	assertDegraded(t, rec, "list")
}

// TestSessionTurnsUnified_NilDBDegraded 锁住 turns 端点的降级形态。
// serveSessionTurnsUnifiedDB 是包内函数，可直接以 nil 库调用。
func TestSessionTurnsUnified_NilDBDegraded(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/sessions/s1/turns", nil)
	req = SetAuthContext(req, &AuthContext{TenantID: "t1", Role: "admin", IsJWT: true})
	serveSessionTurnsUnifiedDB(nil, "secret", rec, req, "s1")
	assertDegraded(t, rec, "turns")
}
