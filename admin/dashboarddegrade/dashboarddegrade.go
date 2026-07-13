// Package dashboarddegrade — "表缺失时优雅降级" 共享工具。
//
// 设计动机：admin(父) → admin/dashboardapi(子) 是合法的依赖，但反向会
// 形成 import cycle。把两边都需要的小工具放到这个独立的子包中，谁都可
// 以 import 它。
package dashboarddegrade

import (
	"errors"
	"log/slog"
	"regexp"

	"github.com/jackc/pgx/v5/pgconn"
)

// MissingRelationRe extracts the relation name from a Postgres 42P01
// "relation ... does not exist" message. PostgreSQL does not populate
// PgError.TableName for this class of error, so we fall back to parsing
// the message itself.
var MissingRelationRe = regexp.MustCompile(`relation "([^"]+)" does not exist`)

// IsMissingRelationError reports whether err is a Postgres SQLSTATE 42P01
// (undefined_table). Used by dashboard endpoints that depend on optional
// analytic tables (e.g. session_module_executions_hot) so they can
// degrade gracefully when the table has not been created on the target
// database yet, instead of returning a 500 to the browser.
func IsMissingRelationError(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "42P01"
}

// ExtractRelationName returns the relation name reported in a 42P01 error,
// preferring PgError.TableName and falling back to a regex over the
// message body. Returns empty string when not a 42P01.
func ExtractRelationName(err error) string {
	if !IsMissingRelationError(err) {
		return ""
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.TableName != "" {
			return pgErr.TableName
		}
		if m := MissingRelationRe.FindStringSubmatch(pgErr.Message); len(m) == 2 {
			return m[1]
		}
	}
	return ""
}

// Report logs the missing-table condition at WARN severity, returns the
// relation name callers can embed in their degraded response body.
func Report(logger *slog.Logger, op string, err error) string {
	if !IsMissingRelationError(err) {
		return ""
	}
	view := ExtractRelationName(err)
	if logger != nil {
		logger.Warn("dashboard query degraded: missing optional table",
			"op", op,
			"relation", view,
			"hint", "module_executions/session_module_executions_hot may not be migrated yet",
		)
	}
	return view
}
