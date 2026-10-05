package admin

import (
	"errors"
	"log/slog"
	"regexp"
	"strings"

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

// DegradedListResponse 包裹「本身是一个数组」的列表载荷。
//
// ## 为什么需要它
//
// 降级契约要求每个降级载荷都自报家门，但 JSON 数组**没有位置**放字段：
// 42P01 时这三个端点原本返回 `[]`，页面上与「这段时间真的一个请求都没有」
// 完全同形。DashboardView 的抽屉、看板供应商卡、供应商用量探索页都把空数组
// 当成真值渲染 —— 用户看到的是「没有热点 key」，而真相是「聚合视图没迁移」。
//
// 改载荷形状（数组 → 对象）是破坏性变更，但「诚实但破坏」优于
// 「兼容但说谎」：这三条前端调用点都只在本仓 web/ 内，已同步改造。
//
// ## 为什么是恒发而不是 omitempty
//
// 与 PeriodCompareResponse.Degraded 同理：字段缺失与 false 在 API 语义上
// 无法区分，omitempty 会把这个标记自己废掉一半。
type DegradedListResponse struct {
	Items          []any  `json:"items"`
	Degraded       bool   `json:"degraded"`
	DegradedReason string `json:"degraded_reason,omitempty"`
	MissingView    string `json:"missing_view,omitempty"`
	Hint           string `json:"hint,omitempty"`
}

// degradedList 构造降级列表响应；view 为缺失的视图名（可为空）。
func degradedList(view string) DegradedListResponse {
	return DegradedListResponse{
		Items:          []any{},
		Degraded:       true,
		DegradedReason: view,
		MissingView:    view,
		Hint:           missingRelationHint(view),
	}
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

// boardMissingViewName 从错误里取出缺失的视图名；非 42P01 返回空串。
func boardMissingViewName(err error) string {
	if err == nil || !IsMissingRelationError(err) {
		return ""
	}
	return ExtractMissingRelationName(err)
}

// applyBoardDegradation 把看板载荷的降级事实写进 payload。
//
// ## 为什么看板需要单独一套而不是复用 degradedList
//
// 饼图/趋势不是裸数组端点：`pies` 是七个维度的 map，`trends` 是折线序列。
// 它们的降级是**部分**的 —— 六个维度算得出来、第七个算不出来，
// 此时把整个 `degraded` 置 true 会让页面把好的那六个也一起标成不可信。
// 所以这里按维度记账：`degraded_pies` 只列真正算不出来的维度名。
//
// ## 为什么必须恒发
//
// 与 DegradedListResponse.Degraded 同理：`degraded_pies` 缺失与「空 map」
// 在前端 `?? []` 之后无法区分。前端要判断「这一格是 0 还是没算出来」，
// 就必须能区分「字段不存在」和「字段存在但为空」，所以这里恒发（无 omitempty）。
//
// 无降级时 degraded=false + degraded_pies={}，与降级时的 true + 非空 map 分得开。
func applyBoardDegradation(payload map[string]any, pies boardPieDegradation, trendsErr error) {
	// 恒发这两项：见上方「为什么必须恒发」。
	payload["degraded"] = false
	payload["degraded_pies"] = map[string]any{}
	payload["degraded_trends"] = false

	var reasons []string

	if pies.Any() {
		payload["degraded"] = true
		payload["degraded_pies"] = map[string]any{
			"dimensions":   pies.Keys,
			"reason":       boardDegradedReason(pies.MissingView, "board pie dimension"),
			"missing_view": pies.MissingView,
			"hint":         missingRelationHint(pies.MissingView),
		}
		reasons = append(reasons, "pies:"+strings.Join(pies.Keys, ","))
	}

	if trendsErr != nil {
		payload["degraded"] = true
		payload["degraded_trends"] = true
		view := boardMissingViewName(trendsErr)
		payload["degraded_trends_reason"] = boardDegradedReason(view, "board trend series")
		payload["trends_missing_view"] = view
		payload["trends_hint"] = missingRelationHint(view)
		reasons = append(reasons, "trends")
	}

	if len(reasons) > 0 {
		// 直接 slog 而不是走 ReportMissingRelation：那个 helper 只在 err 真的是
		// 42P01 时才落日志，而我这里传的是降级摘要，不是原始错误 ——
		// 借它转发会得到「一条都不记」的假象（这正是本轮修的那类缺陷的镜像）。
		slog.Default().Warn("dashboard board degraded: some board sections are unavailable, not zero",
			"degraded", reasons,
			"pies_missing_view", pies.MissingView,
			"trends_missing_view", boardMissingViewName(trendsErr),
			"hint", "empty board sections here mean the query failed, not that there was no traffic",
		)
		payload["degraded_reason"] = strings.Join(reasons, "; ")
	}
}

// boardDegradedReason 生成人类可读的降级原因。非 42P01 时说明是查询失败
// 而不是缺视图 —— 两者给运维的排查方向完全不同。
func boardDegradedReason(view, what string) string {
	if view != "" {
		return what + " unavailable: missing view " + view
	}
	return what + " unavailable: query failed"
}

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
