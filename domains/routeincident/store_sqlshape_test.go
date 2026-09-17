// store_sqlshape_test.go — 无库单元测试，钉死 store 两条关键 SQL 的
// 形状。真库行为由 store_integration_test.go（TEST_PG_URL 门控）验证；
// 这里保证即使没有数据库，2026-09-17 252 生产事故的两个成因也不允许
// 回归：
//
//	S-1  lockOpenSQL 的状态集必须与部分唯一索引
//	     uq_route_incidents_active_route 的 WHERE 谓词逐字一致
//	     （715 引入 pending 后 lockActive 漏配导致每条路由的第二次
//	     失败必然 23505）；
//	S-2  insertNewSQL 必须携带指向该索引的 ON CONFLICT DO NOTHING
//	     （并发首败的竞态不得泄漏 23505）。
package routeincident

import (
	"regexp"
	"strings"
	"testing"
)

// liveStatePredicate is the canonical predicate text shared by the
// partial unique index (migration 715) and the store's SQL.
const liveStatePredicate = "state IN ('pending', 'active', 'recovering')"

func normalizeSQLWhitespace(s string) string {
	return regexp.MustCompile(`\s+`).ReplaceAllString(strings.TrimSpace(s), " ")
}

func TestSQLShape_LockOpenCoversLiveStates(t *testing.T) {
	shape := normalizeSQLWhitespace(lockOpenSQL)
	for _, want := range []string{
		"FROM route_incidents",
		"COALESCE(provider_id, 0) = COALESCE($4, 0)",
		"COALESCE(credential_id, 0) = COALESCE($5, 0)",
		"FOR UPDATE",
	} {
		if !strings.Contains(shape, want) {
			t.Fatalf("lockOpenSQL lost required fragment %q; shape:\n%s", want, shape)
		}
	}
	if !strings.Contains(shape, normalizeSQLWhitespace(liveStatePredicate)) {
		t.Fatalf("lockOpenSQL state set drifted from the partial unique index predicate %q; shape:\n%s", liveStatePredicate, shape)
	}
}

func TestSQLShape_InsertNewConflictsOnPartialIndex(t *testing.T) {
	shape := normalizeSQLWhitespace(insertNewSQL)
	for _, want := range []string{
		"INSERT INTO route_incidents",
		"ON CONFLICT ( tenant_id, endpoint_protocol, model, COALESCE(provider_id, 0), COALESCE(credential_id, 0) )",
		normalizeSQLWhitespace(liveStatePredicate),
		"DO NOTHING",
		"RETURNING id",
	} {
		if !strings.Contains(shape, want) {
			t.Fatalf("insertNewSQL lost required fragment %q; shape:\n%s", want, shape)
		}
	}
}
