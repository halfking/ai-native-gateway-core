// Package admin — admin 聚合读面 rows 迭代错误分类（R35-N1）
//
// R35-N1 同族扫描发现：聚合读面大量 `for rows.Next()` 循环存在两类静默
// 吞错——①循环内 rows.Scan 失败 `continue` 跳行无任何服务端痕迹；②循环
// 结束后不检查 rows.Err()，迭代中断（连接断开/服务端错误）时静默返回
// 200 截断列表。R33/R35 已给 session-analytics 族装上查询侧 42P01 分类门
// （writeAnalyticsQueryErr / writeAnalyticsDetailErr），本文件把同样的
// 分类语义扩展到 rows 迭代侧，供聚合读面端点复用。
package admin

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
) // writeAggRowsErr 分类聚合 rows 迭代错误（rows.Err() 产物）：
// 42P01 → 503 + analytics_view_missing（缺视图是可修复的环境态，与
// writeAnalyticsQueryErr 同语义）；其余迭代中断（网络/服务端故障）→
// writeInternalErr 500。err == nil（正常收敛）返回 false，handler 继续
// 正常响应路径；返回 true 表示响应已写出，handler 必须 return。
func writeAggRowsErr(w http.ResponseWriter, op string, err error) bool {
	if err == nil {
		return false
	}
	if IsMissingRelationError(err) {
		ReportMissingRelation(slog.Default(), op, err)
		writeErrorWithCode(w, http.StatusServiceUnavailable, "analytics_view_missing",
			analyticsViewMigrationHint(ExtractMissingRelationName(err)))
		return true
	}
	writeInternalErr(w, op, err)
	return true
}

// aggRowsErrClassified 是 writeAggRowsErr 的无副作用变体，供需要自定义
// 响应形状的调用方先分类再自行写出：返回 (category, handled)。
// category: "ok" / "view_missing" / "internal"。
func aggRowsErrClassified(err error) (string, *pgconn.PgError) {
	if err == nil {
		return "ok", nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
		return "view_missing", pgErr
	}
	return "internal", nil
}

// warnRowSkip 给聚合循环内 rows.Scan 失败跳行留服务端痕迹（R35-N1）。
// 「跳行容错」语义保留——单行坏数据不应毒化整页聚合——但必须有日志可查，
// 否则 schema 演进引发的持续类型失配会让聚合数据静默缩水且无人知晓。
func warnRowSkip(op string, err error) {
	slog.Warn("admin aggregate row scan failed; row skipped", "op", op, "error", err)
}

// rowsIterErr 统一提取迭代终态错误：pgx.Rows 用 Err()。留给只有
// pgx.Rows 句柄的场景，避免调用方直接依赖 pgx 包细节。
func rowsIterErr(rows pgx.Rows) error { return rows.Err() }

// writeLookupErr 是单实体查询（QueryRow+Scan）的分类三门（R35-N1）：
// ErrNoRows → 404 notFoundMsg（真不存在，原语义）；42P01 → 503 +
// analytics_view_missing（缺表/缺视图是可修复环境态，吞成 404 会把
// 运维引向"数据被删了"的错误方向）；其余（连接断/超时）→ 500。
// 替换"一切 err 都 404"的旧写法（全族扫描 19 处）。
func writeLookupErr(w http.ResponseWriter, notFoundMsg string, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, notFoundMsg)
		return
	}
	if IsMissingRelationError(err) {
		ReportMissingRelation(slog.Default(), "admin lookup", err)
		writeErrorWithCode(w, http.StatusServiceUnavailable, "analytics_view_missing",
			analyticsViewMigrationHint(ExtractMissingRelationName(err)))
		return
	}
	writeInternalErr(w, "lookup failed", err)
}
