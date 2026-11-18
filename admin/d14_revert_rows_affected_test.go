package admin

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// ★ D14（2026-11-19 审计轮）revert 行数语义守卫钉测。
//
// 缺陷本体：
//
//	revert 时 `RowsAffected` **未检查** ⇒ 规则已被删除时静默无效（受影响
//	0 行），但响应仍声称「已回滚」；且 `applied_actions` 旧快照残留，与
//	`auto_applied=FALSE` 并存 ⇒ 数据语义不一致。
//
// 现行为：
//
//	· 受影响 0 行 ⇒ 记入未回滚清单 ⇒ **整体 422 并回滚事务**（不做半截
//	  回滚），响应体列出未回滚的规则名；
//	· 回滚成功 ⇒ 一并 `applied_actions = NULL`。

const (
	d14SelectAppliedSQL = `SELECT auto_applied, applied_actions FROM error_analysis_results WHERE queue_id = \$1`
	d14UpdateRuleSQL    = `UPDATE prompt_injection_rules`
	d14ClearAppliedSQL  = `UPDATE error_analysis_results`
)

// d14AppliedActions 是「已应用过的动作」旧值快照：一条 disable_rule。
const d14AppliedActions = `[{"action":"disable_rule","rule_name":"rule_a","old":{"enabled":false},"new":{"enabled":true}}]`

// d14NewRevertHandler 造一个走到 revert 的 handler + mock。
// rowsAffected 是「规则 UPDATE」实际影响的行数。
func d14NewRevertHandler(t *testing.T, rowsAffected int64) (*PIAnalysisHandler, sqlmock.Sqlmock, *sql.DB) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	mock.ExpectQuery(d14SelectAppliedSQL).
		WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"auto_applied", "applied_actions"}).
			AddRow(true, []byte(d14AppliedActions)))
	mock.ExpectBegin()
	mock.ExpectExec(d14UpdateRuleSQL).
		WillReturnResult(sqlmock.NewResult(0, rowsAffected))
	return &PIAnalysisHandler{db: db}, mock, db
}

// TestD14RevertAbortsWhenRuleMissing 钉住：规则已删除（RowsAffected = 0）
// 时必须 422，且**必须回滚事务**（不得提交、不得清 applied_actions），
// 响应体要列出未能回滚的规则名。
func TestD14RevertAbortsWhenRuleMissing(t *testing.T) {
	h, mock, db := d14NewRevertHandler(t, 0)
	defer db.Close()
	mock.ExpectRollback()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/pi-analysis/results/42/revert", nil)
	h.RevertFix(rec, req, 42)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("规则已删除时必须返回 422，got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "rule_a") {
		t.Fatalf("422 响应必须列出未能回滚的规则名，body=%s", rec.Body.String())
	}
	// 不得清 applied_actions ⇒ 不应出现第二次 UPDATE。
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("DB 交互与预期不符（未回滚路径上不应提交、也不应清 applied_actions）：%v", err)
	}
}

// TestD14RevertSuccessClearsAppliedSnapshot 钉住正常路径：回滚成功 ⇒
// 提交事务，且 `applied_actions` 一并清空。
func TestD14RevertSuccessClearsAppliedSnapshot(t *testing.T) {
	h, mock, db := d14NewRevertHandler(t, 1)
	defer db.Close()
	mock.ExpectExec(d14ClearAppliedSQL).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/pi-analysis/results/42/revert", nil)
	h.RevertFix(rec, req, 42)

	if rec.Code != http.StatusOK {
		t.Fatalf("回滚成功应返回 200，got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"reverted":true`) {
		t.Fatalf("回滚成功响应体应含 reverted=true，body=%s", rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("DB 交互与预期不符：%v", err)
	}
}

// TestD14RevertNotAppliedIsConflict 钉住未应用过的队列不得被 revert。
func TestD14RevertNotAppliedIsConflict(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery(d14SelectAppliedSQL).
		WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"auto_applied", "applied_actions"}).
			AddRow(false, nil))

	h := &PIAnalysisHandler{db: db}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/pi-analysis/results/42/revert", nil)
	h.RevertFix(rec, req, 42)

	if rec.Code != http.StatusConflict {
		t.Fatalf("未应用过的队列应返回 409，got %d body=%s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("DB 交互与预期不符：%v", err)
	}
}

// 编译期断言：确保本文件用到的 sqlmock 特性都可用（避免 vendor 漂移时
// 才在编译期炸）。
var _ = sqlmock.NewResult
var _ = sql.ErrNoRows
