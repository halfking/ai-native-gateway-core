package admin

import (
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// ★ D13（2026-11-19 审计轮）apply 错误路径分流钉测。
//
// 旧实现：`http.Error(w, err.Error(), 422)` 原样回显 piApplyActions 的
// 返回值，而后者用 `%w` 包了驱动错误 ⇒ pgx 错误串（表名/列名/约束名）
// 到达客户端。admin 包的守卫 `internal_error_guard_test.go` **只扫 500
// 状态行** ⇒ 422 路径漏网。
//
// 现分流：业务性失败 ⇒ 422 + 可读文案；驱动失败 ⇒ 500 + 固定文案。

const (
	d13QueryResultSQL = `SELECT rule_adjustment, confidence_score, is_false_positive, auto_applied FROM error_analysis_results WHERE queue_id = $1`
	d13QueryRuleSQL   = `SELECT enabled, severity, pattern FROM prompt_injection_rules WHERE rule_name = $1 FOR UPDATE`
)

// d13NewHandler 造一个只走到 apply 错误路径的 handler + mock。
func d13NewHandler(t *testing.T, ruleErr error) (*PIAnalysisHandler, sqlmock.Sqlmock, *sql.DB) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(d13QueryResultSQL)).
		WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"rule_adjustment", "confidence_score", "is_false_positive", "auto_applied"}).
			AddRow([]byte(`{"action":"disable_rule","rule_name":"rule_a","reason":"llm suggestion"}`), 0.95, false, false))
	// 1b. 类型闸（2026-11-20 PI 接入轮新增）：apply 前先读队列项的
	// error_type（output_guard_blocked 恒 view-only）。钉测路径给一个
	// 非 view-only 的类型让流程走到规则查询。
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT error_type FROM error_analysis_queue WHERE id = $1`)).
		WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"error_type"}).AddRow("rule_false_positive"))
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(d13QueryRuleSQL)).
		WithArgs("rule_a").
		WillReturnError(ruleErr)

	h := &PIAnalysisHandler{db: db}
	return h, mock, db
}

// TestD13ApplyBusinessFailureReturns422 可读文案：规则不存在是**业务性**
// 失败 ⇒ 422，客户端能展示原因、能自愈。
func TestD13ApplyBusinessFailureReturns422(t *testing.T) {
	h, _, db := d13NewHandler(t, sql.ErrNoRows)
	defer db.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/pi-analysis/results/42/apply", nil)
	h.ApplyFix(rec, req, 42)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("业务性失败应返回 422，got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "does not exist") {
		t.Fatalf("业务性失败应给出可读原因，got %q", rec.Body.String())
	}
}

// TestD13ApplyDriverFailureMustNotEchoInternalError 驱动失败（pgx 错误）：
// 500 + 固定文案，**不得**回显表名/列名/约束名。
func TestD13ApplyDriverFailureMustNotEchoInternalError(t *testing.T) {
	internal := `ERROR: relation "prompt_injection_rules" does not exist (SQLSTATE 42P01)`
	h, _, db := d13NewHandler(t, errors.New(internal))
	defer db.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/pi-analysis/results/42/apply", nil)
	h.ApplyFix(rec, req, 42)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("驱动失败应返回 500，got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, leak := range []string{"prompt_injection_rules", "42P01", "does not exist", "SQLSTATE"} {
		if strings.Contains(body, leak) {
			t.Fatalf("500 响应回显了内部错误串 %q：%s", leak, body)
		}
	}
}
