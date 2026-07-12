package admin

import (
	"errors"
	"log/slog"
	"regexp"

	"github.com/jackc/pgx/v5/pgconn"
)

// missingRelationRe extracts the relation name from a Postgres 42P01
// "relation ... does not exist" message. PostgreSQL does not populate
// PgError.TableName for this class of error, so we fall back to parsing
// the message itself.
var missingRelationRe = regexp.MustCompile(`relation "([^"]+)" does not exist`)

// isMissingRelationError reports whether err is a Postgres SQLSTATE 42P01
// (undefined_table). It exists so dashboard endpoints that depend on
// optional analytic views (e.g. usage_ledger_with_current_month) can
// degrade gracefully when the view has not been created on the target
// database yet, instead of returning a 500 to the browser.
func isMissingRelationError(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "42P01"
}

// extractMissingRelationName returns the relation name reported in the
// 42P01 error, preferring PgError.TableName and falling back to a regex
// over the message body.
func extractMissingRelationName(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.TableName != "" {
			return pgErr.TableName
		}
		if m := missingRelationRe.FindStringSubmatch(pgErr.Message); len(m) == 2 {
			return m[1]
		}
	}
	return ""
}

// missingRelationHint builds the operator-facing hint string embedded in
// the degraded payload. It is shared across endpoints so the wording
// stays consistent.
func missingRelationHint(view string) string {
	if view == "" {
		return "数据视图尚未初始化，请先执行数据聚合迁移"
	}
	return "数据视图 " + view + " 尚未初始化，请先执行数据聚合迁移"
}

// reportMissingRelation logs the missing-view condition once per call and
// returns the relation name that callers can embed in their degraded
// response body. Callers compose the HTTP response themselves; this
// helper exists so the log format stays consistent across all five
// dashboard endpoints that depend on optional analytic views.
func reportMissingRelation(logger *slog.Logger, op string, err error) string {
	if !isMissingRelationError(err) {
		return ""
	}
	view := extractMissingRelationName(err)
	if logger != nil {
		logger.Warn("dashboard query degraded: missing optional view",
			"op", op,
			"relation", view,
			"hint", "data_aggregations view not migrated yet",
		)
	}
	return view
}
