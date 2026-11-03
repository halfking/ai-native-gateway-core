package main

import (
	"context"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

func TestOwnerLookupReadsOnlySessionOwner(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(`SELECT owner_user\s+FROM session_dim`).
		WithArgs("gw-session", "tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{"owner_user"}).AddRow("alice"))
	if got := makeOwnerLookup(db)(context.Background(), "gw-session", "tenant-a"); got != "alice" {
		t.Fatalf("data owner = %q, want alice", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerLookupRequiresTenantAndHonorsCancelledContext(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	lookup := makeOwnerLookup(db)
	if got := lookup(context.Background(), "gw-session", ""); got != "" {
		t.Fatalf("missing tenant returned owner %q", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := lookup(ctx, "gw-session", "tenant-a"); got != "" {
		t.Fatalf("cancelled lookup returned owner %q", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestLookupOwnersSubqueryCarriesTenantPredicate（N3，2026-11-04 审计）：
// session_id 不是全局唯一（跨租户可撞名，生产成立过一次）⇒ 内层
// session_turn_details(_hot) 子查询只按 session_id 取「最近一轮」时，
// api_key_owner_user 可能归属错租户。判据三层：
//
//	行为层：sqlmock 的 ExpectQuery 正则**要求** hot 臂带租户谓词 ——
//	回退成无约束子查询时正则不命中 ⇒ lookupOwners 返回空值 ⇒ 红；
//	文本层：内层两臂各带一条与外层同形的租户谓词（$2 空串不过滤、
//	非空等值，与外层 sd.tenant_id 谓词同形）；
//	语义层：tenantID 为空串（未知租户）时同一 SQL 照常执行 —— $2 空串
//	不过滤的既有语义在子查询内复刻，不是收紧成必须非空。
func TestLookupOwnersSubqueryCarriesTenantPredicate(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// 行为层判据：hot 臂必须紧跟租户谓词（回退即不命中此正则）。
	mock.ExpectQuery(`session_turn_details_hot\s+WHERE session_id = \$1\s+AND \(\$2 = '' OR tenant_id = \$2\)`).
		WithArgs("gw-session", "tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{"owner_user", "api_key_owner_user"}).AddRow("alice", "alice"))
	caller, data := lookupOwners(context.Background(), db, "gw-session", "tenant-a")
	if caller != "alice" || data != "alice" {
		t.Fatalf("lookupOwners = (%q, %q), want (alice, alice)", caller, data)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("executed SQL does not match tenant-predicated shape: %v", err)
	}

	// 文本层判据：两臂各一条租户谓词 + 外层谓词仍在。
	innerStart := strings.Index(lookupOwnersSQL, "(SELECT x.api_key_owner_user")
	innerEnd := strings.Index(lookupOwnersSQL, ") x")
	if innerStart < 0 || innerEnd < 0 || innerEnd <= innerStart {
		t.Fatalf("lookupOwnersSQL inner subquery not found")
	}
	inner := lookupOwnersSQL[innerStart:innerEnd]
	if got := strings.Count(inner, "AND ($2 = '' OR tenant_id = $2)"); got != 2 {
		t.Fatalf("inner subquery tenant predicates = %d, want 2 (hot + parent arms)", got)
	}
	for _, arm := range []string{"public.session_turn_details_hot", "public.session_turn_details\n"} {
		if !strings.Contains(inner, arm) {
			t.Fatalf("inner subquery missing arm %q", arm)
		}
	}
	if !strings.Contains(lookupOwnersSQL, "($2 = '' OR sd.tenant_id = $2)") {
		t.Fatalf("outer session_dim tenant predicate missing")
	}

	// 语义层判据：未知租户（$2=''）走同一条 SQL、不过滤。
	mock.ExpectQuery(`session_turn_details_hot`).
		WithArgs("gw-session", "").
		WillReturnRows(sqlmock.NewRows([]string{"owner_user", "api_key_owner_user"}).AddRow(nil, nil))
	caller, data = lookupOwners(context.Background(), db, "gw-session", "")
	if caller != "" || data != "" {
		t.Fatalf("unknown-tenant lookup = (%q, %q), want empty", caller, data)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unknown-tenant lookup must run the same SQL: %v", err)
	}
}
