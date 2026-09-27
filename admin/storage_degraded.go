// Package admin — storage_degraded.go
//
// Subtask 4（handoff §6）：把「数据库读路径不可用」从 HTTP 500 里拆出来，
// 显式表达为 503 + storage_status。
//
// 为什么要拆：500 的语义是「这个请求本身让服务端出错了」，会触发与调用方
// 无关的告警与重试策略。数据库不可达其实是依赖降级 —— 请求本身完全合法，
// 换个时间点重试就可能成功。折叠成 500 会让值班把一次存储抖动误判成代码
// 缺陷，也让前端拿不到「可重试」的信号。
//
// 分类器刻意保守：只把「连不上 / 开不出事务 / 读超时」判成降级，SQL 语法错、
// 权限不足、RLS 拒绝等仍然是 500。判据过宽会把真 bug 伪装成可重试的降级，
// 那比现在的 500 更糟。
package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kaixuan/llm-gateway-go/internal/observability"
)

// StorageStatusUnavailable 是降级响应体里 storage_status 字段的取值。
// 与 domains/attachments 里已有的 "storage_unavailable" 错误码同字面量，
// 但语义更窄：这里特指「读路径拿不到持久化状态」，而非「对象存储写入失败」。
const StorageStatusUnavailable = "storage_unavailable"

// IsStorageUnavailable 报告一个读路径错误是否应被判定为「存储不可用」。
//
// 判据（保守，只覆盖连接层）：
//   - 连接建立失败（*pgconn.ConnectError）—— 数据库没起来 / 网络不通；
//   - 网络层超时（net.Error 且 Timeout()）—— 存储不可达或过载；
//   - 上下文超时（context.DeadlineExceeded）—— 10s 读超时预算耗尽。
//
// 判为降级的 PgError SQLSTATE 白名单（R73 审计 M-5，保守集合）：
//   - 08xxx 连接异常类（connection_exception：服务端连接层故障，含
//     08006 connection_failure）；
//   - 53xxx 资源不足类（insufficient_resources：53300 too_many_connections、
//     53100 磁盘满、53200 内存不足——共享 PG 实例的典型可用性故障）；
//   - 57P03 cannot_connect_now（服务端启动/恢复期拒连）。
//     这些是基础设施可用性故障，与「查询本身有缺陷」语义不同，客户端
//     稍后重试是有意义的（与 503 retryable 语义一致）。
//
// 刻意不判为降级的：
//   - context.Canceled —— 客户端自己走了，不是存储的问题；
//   - 其余 *pgconn.PgError —— 服务端明确回了 SQL 错误（语法 42601、
//     权限 42501、约束 23xxx、RLS、57014 query_canceled/语句超时——
//     后者可能是自身预算，语义两可，保持 500 不重新分类）；
//   - 其它一切 —— 保持既有 500 行为，不做无根据的重新分类。
func IsStorageUnavailable(err error) bool {
	if err == nil {
		return false
	}
	// 池子为空：走哨兵而不是匹配错误文案（withTx 已改为返回
	// ErrNilDatabasePool）。这里绝不对任意 err 调 Error() 做字符串比对——
	// 那既脆弱，某些 pgconn 错误类型的 Error() 在内部字段缺失时还会 panic。
	if errors.Is(err, ErrNilDatabasePool) {
		return true
	}
	// 客户端主动取消不是存储降级，必须排在 Deadline 之前独立判。
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var connectErr *pgconn.ConnectError
	if errors.As(err, &connectErr) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	// M-5：服务端回的连接/资源类 SQLSTATE 也是存储可用性故障。
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case strings.HasPrefix(pgErr.Code, "08"), // connection_exception
			strings.HasPrefix(pgErr.Code, "53"), // insufficient_resources
			pgErr.Code == "57P03":               // cannot_connect_now
			return true
		}
	}
	return false
}

// WriteStorageDegraded 写出 503 降级响应：固定文案 + storage_status 字段，
// 并按 component 打点。
//
// 刻意不回显 err.Error()：连接错误串里常带主机名、端口、甚至 DSN 片段
// （这与 Subtask 1 修掉的「500 分支回显含 tenant_id 的内部错误串」同型）。
// 排障需要的信息走服务端日志与指标，不走响应体。
//
// component 取 observability.StorageComponent* 常量之一。
func WriteStorageDegraded(w http.ResponseWriter, component string, err error) {
	observability.RecordStorageDegraded(component)
	defer observability.StorageDegradedDone()

	// 降级原因只进日志，不进响应体。
	logStorageDegraded(component, err)

	writeStorageDegradedBody(w, component)
}

// writeStorageDegradedBody 只负责写响应体，便于单测直接断言而不必关心
// 打点与日志副作用。
func writeStorageDegradedBody(w http.ResponseWriter, component string) {
	// 会话列表/详情/轮次三个端点历史上分别用 writeJSON / writeError /
	// writeExportJSONError 三种写出器，响应体外壳不一致。这里统一用
	// writeJSON + 固定字段，不再掺各端点自有的 error 包装。
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{
		"status":         "error",
		"storage_status": StorageStatusUnavailable,
		"component":      component,
		"message":        "session storage is temporarily unavailable, please retry",
		// 显式给出可重试语义，客户端不必靠猜状态码。
		"retryable": true,
	})
}

// logStorageDegraded 把降级原因写进服务端日志。抽成独立函数是为了让
// WriteStorageDegraded 的测试不需要捕获日志。
func logStorageDegraded(component string, err error) {
	if err == nil {
		return
	}
	slog.Default().Warn("admin session read degraded: storage unavailable",
		"component", component,
		"storage_status", StorageStatusUnavailable,
		"error", fmt.Sprintf("%T", err),
		"err", err.Error())
}
