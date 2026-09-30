package admin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// R35-N1 回归：admin 聚合读面错误分类三门。此前同族位置把一切查询错误
// 吞成 404（缺视图/连接断全被误报"资源不存在"），或 rows 迭代中断静默
// 返回 200 截断列表。writeAggRowsErr / writeLookupErr 是仿
// writeAnalyticsDetailErr 的分类双门/三门。

func mkViewMissing() error {
	return &pgconn.PgError{
		Code:      "42P01",
		Message:   `relation "request_stats_dim_minute" does not exist`,
		TableName: "request_stats_dim_minute",
	}
}

func TestWriteAggRowsErr_Classification(t *testing.T) {
	// nil（正常收敛）→ 不写响应
	rec := httptest.NewRecorder()
	if writeAggRowsErr(rec, "op", nil) {
		t.Fatalf("nil err must return false and write nothing, got code=%d", rec.Code)
	}

	// 42P01 → 503 + analytics_view_missing + 视图名引导
	rec = httptest.NewRecorder()
	if !writeAggRowsErr(rec, "op", mkViewMissing()) {
		t.Fatalf("42P01 must return true (response written)")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("42P01 should be 503, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "analytics_view_missing") ||
		!strings.Contains(rec.Body.String(), "request_stats_dim_minute") {
		t.Fatalf("missing guidance payload: %s", rec.Body.String())
	}

	// 迭代中断（网络/服务端）→ 500，不误导为数据不存在
	rec = httptest.NewRecorder()
	if !writeAggRowsErr(rec, "op", errors.New("conn closed")) {
		t.Fatalf("non-nil err must return true")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("iteration abort should be 500, got %d", rec.Code)
	}
}

func TestWriteLookupErr_Classification(t *testing.T) {
	// ErrNoRows → 404 原文案（真不存在）
	rec := httptest.NewRecorder()
	writeLookupErr(rec, "credential not found", pgx.ErrNoRows)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("ErrNoRows should stay 404, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "credential not found") {
		t.Fatalf("404 original message must be preserved: %s", rec.Body.String())
	}

	// 42P01 → 503 引导（缺表被吞成 404 是本轮根修的目标行为）
	rec = httptest.NewRecorder()
	writeLookupErr(rec, "credential not found", mkViewMissing())
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("42P01 should be 503, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "analytics_view_missing") {
		t.Fatalf("missing guidance payload: %s", rec.Body.String())
	}

	// 其它 DB 故障 → 500，且不外泄 err 细节
	rec = httptest.NewRecorder()
	writeLookupErr(rec, "user not found", errors.New("pq: internal secret detail"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("other errors should be 500, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "internal secret detail") {
		t.Fatalf("500 must not leak error detail: %s", rec.Body.String())
	}
}

func TestAggRowsErrClassified_Categories(t *testing.T) {
	if c, pg := aggRowsErrClassified(nil); c != "ok" || pg != nil {
		t.Fatalf("nil → ok, got %s", c)
	}
	if c, _ := aggRowsErrClassified(mkViewMissing()); c != "view_missing" {
		t.Fatalf("42P01 → view_missing, got %s", c)
	}
	if c, _ := aggRowsErrClassified(errors.New("boom")); c != "internal" {
		t.Fatalf("other → internal, got %s", c)
	}
}

// TestAggReadGuard_MigratedCallersWired：静态接线守卫——核心聚合读面
// 迁移点必须仍引用 writeAggRowsErr / warnRowSkip / writeLookupErr，
// 防止后续重构静默退回吞错形态。
func TestAggReadGuard_MigratedCallersWired(t *testing.T) {
	requireRefs := map[string][]string{
		"analytics.go":                  {"writeAggRowsErr", "warnRowSkip"},
		"dashboard_board_queries.go":    {"warnRowSkip"},
		"dashboard_session_stats.go":    {"warnRowSkip"},
		"usage_provider_models.go":      {"writeAggRowsErr", "warnRowSkip"},
		"session_list.go":               {"warnRowSkip"},
		"credential_models.go":          {"writeLookupErr"},
		"keys.go":                       {"writeLookupErr"},
		"usage.go":                      {"writeLookupErr"},
		"users.go":                      {"writeLookupErr"},
		"model_name_mapping.go":         {"writeLookupErr"},
		"providers.go":                  {"writeLookupErr"},
		"data_lifecycle_attachments.go": {"writeLookupErr"},
		"ip_blocklist.go":               {"writeLookupErr"},
		"maas_handlers.go":              {"writeLookupErr"},
	}
	for file, refs := range requireRefs {
		src, err := os.ReadFile(filepath.Join("admin", file))
		if err != nil {
			// go test 的工作目录是包目录本身
			src, err = os.ReadFile(file)
			if err != nil {
				t.Fatalf("read %s: %v", file, err)
			}
		}
		for _, ref := range refs {
			if !strings.Contains(string(src), ref) {
				t.Errorf("%s lost guard wiring: %s no longer referenced", file, ref)
			}
		}
	}
}
