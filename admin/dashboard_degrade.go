package admin

import (
	"errors"
	"log/slog"
	"net/http"
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

// missingRelationPayload is the body shape used when a dashboard endpoint
// cannot run its aggregation because a backend view is missing. It keeps
// the JSON contract stable so the frontend can render zeroed metrics
// alongside a non-blocking hint, instead of showing a destructive error
// banner that hides the rest of the dashboard layout.
type missingRelationPayload struct {
	Degraded     bool   `json:"degraded"`
	MissingView  string `json:"missing_view,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
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
// response body.
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

// writeMissingRelationPayload writes the degraded payload with HTTP 200 so
// the dashboard layout stays intact. Callers should check
// isMissingRelationError before invoking this helper.
func writeMissingRelationPayload(w http.ResponseWriter, op string, err error, payload any) {
	reportMissingRelation(slog.Default(), op, err)
	writeJSON(w, http.StatusOK, payload)
}

// writeMissingRelationOrError writes either the degraded payload (when err
// is a SQLSTATE 42P01) or a 500 with the underlying error message. This
// keeps the response shape consistent for callers that hold a typed
// zero-value response and just want to fall back gracefully.
func writeMissingRelationOrError(w http.ResponseWriter, op string, err error, degradedPayload any) bool {
	if !isMissingRelationError(err) {
		return false
	}
	view := reportMissingRelation(slog.Default(), op, err)
	payload := degradedPayload
	// Merge in the degradation marker if the payload is a map. We use a
	// type-switch so callers can pass either a map[string]any or a struct.
	if m, ok := payload.(map[string]any); ok {
		m["degraded"] = true
		m["missing_view"] = view
		m["error_code"] = "VIEW_MISSING"
		m["hint"] = missingRelationHint(view)
		writeJSON(w, http.StatusOK, m)
		return true
	}
	// For typed payloads we just write them as-is. Callers that want the
	// marker should pass a map. The typed path is reserved for endpoints
	// that have already merged the marker inline.
	writeJSON(w, http.StatusOK, payload)
	return true
}
