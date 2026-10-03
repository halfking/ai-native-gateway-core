package admin

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
// (undefined_table). It exists so dashboard endpoints that depend on
// optional analytic views (e.g. usage_ledger_with_current_month) can
// degrade gracefully when the view has not been created on the target
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

// IsMissingColumnError reports whether err is a Postgres SQLSTATE 42703
// (undefined_column).
//
// 与 IsMissingRelationError 同源的一类降级：部署库的 schema 比代码旧。
// 区别只在症状——42P01 是「表/视图整个不存在」，42703 是「对象在，但少一列」。
// 后者更隐蔽：`usage_ledger_with_current_month` 视图存在，查询却引用了
// usage_ledger 从来没有的列（compression_strategy / gw_session_id，
// b9a8baba4 引入），于是每个命中该查询的页面都拿到 500。
//
// 只覆盖 42703。不要顺手把 42703 之外的错误也降级：那会把真实的 SQL 错误
// （写错列名、类型不匹配）伪装成「数据为空」，比 500 更难查。
func IsMissingColumnError(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "42703"
}

// IsSchemaBehindError reports whether err means "this deployment's schema is
// older than the code" — either the relation or one of its columns is absent.
// Callers that can render an empty-but-valid payload should degrade on this
// instead of on either code alone.
func IsSchemaBehindError(err error) bool {
	return IsMissingRelationError(err) || IsMissingColumnError(err)
}

// ExtractMissingRelationName returns the relation name reported in the
// 42P01 error, preferring PgError.TableName and falling back to a regex
// over the message body.
func ExtractMissingRelationName(err error) string {
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

// missingRelationHint builds the operator-facing hint string embedded in
// the degraded payload. It is shared across endpoints so the wording
// stays consistent.
func missingRelationHint(view string) string {
	if view == "" {
		return "数据视图尚未初始化，请先执行数据聚合迁移"
	}
	return "数据视图 " + view + " 尚未初始化，请先执行数据聚合迁移"
}

// ReportMissingRelation logs the missing-view condition once per call and
// returns the relation name that callers can embed in their degraded
// response body.
func ReportMissingRelation(logger *slog.Logger, op string, err error) string {
	if !IsMissingRelationError(err) {
		return ""
	}
	view := ExtractMissingRelationName(err)
	if logger != nil {
		logger.Warn("dashboard query degraded: missing optional view",
			"op", op,
			"relation", view,
			"hint", "data_aggregations view not migrated yet",
		)
	}
	return view
}

// ReportSchemaBehind logs any schema-behind condition (missing relation **or**
// missing column) and returns a short operator-facing description.
//
// 必须留日志：降级把 500 变成了 200 + 空数据，页面看上去正常。不记这一笔，
// 「代码查了库里没有的列」就会被伪装成「这段时间没有数据」——比 500 难查得多。
func ReportSchemaBehind(logger *slog.Logger, op string, err error) string {
	if err == nil {
		return ""
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return ""
	}
	switch pgErr.Code {
	case "42P01":
		ReportMissingRelation(logger, op, err)
		return "missing relation: " + ExtractMissingRelationName(err)
	case "42703":
		what := "column"
		if logger != nil {
			logger.Warn("dashboard query degraded: missing column",
				"op", op,
				"column", pgErr.ColumnName,
				"table", pgErr.TableName,
				"err", pgErr.Message,
				"hint", "query references a column this deployment's schema does not have; "+
					"the endpoint returns empty data until the schema catches up",
			)
		}
		if pgErr.TableName != "" {
			what += " " + pgErr.TableName + "." + pgErr.ColumnName
		} else if pgErr.ColumnName != "" {
			what += " " + pgErr.ColumnName
		}
		return what
	}
	return ""
}
