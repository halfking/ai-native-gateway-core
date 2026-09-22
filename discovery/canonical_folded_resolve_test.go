// canonical_folded_resolve_test.go — 2026-09-23 252 审计轮：迁移 735 折叠名
// 预解析的三分支钉（reroute / 无命中 / fail-open）。
package discovery

import (
	"context"
	"errors"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
)

func TestAdoptFoldedExisting_ReroutesToFoldingEqualActiveRow(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery(`(?s)SELECT canonical_name FROM models_canonical.*regexp_replace`).
		WithArgs("doubao-1.5-ui-tars").
		WillReturnRows(pgxmock.NewRows([]string{"canonical_name"}).AddRow("doubao-1-5-ui-tars"))

	name := "doubao-1.5-ui-tars"
	family := InferFamily(name)
	adoptFoldedExisting(context.Background(), mock, &name, &family)

	if name != "doubao-1-5-ui-tars" {
		t.Fatalf("canonicalName = %q, want rerouted %q", name, "doubao-1-5-ui-tars")
	}
	if want := InferFamily("doubao-1-5-ui-tars"); family != want {
		t.Fatalf("family = %q, want %q", family, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptFoldedExisting_NoRowsKeepsIncoming(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery(`(?s)SELECT canonical_name FROM models_canonical.*regexp_replace`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"canonical_name"}))

	name := "brand-new-model-v2"
	family := InferFamily(name)
	adoptFoldedExisting(context.Background(), mock, &name, &family)

	if name != "brand-new-model-v2" {
		t.Fatalf("canonicalName = %q, want unchanged", name)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptFoldedExisting_QueryErrorFailsOpen(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	// 预解析查询失败必须 fail-open（保持原值走普通 upsert），不得阻塞
	// 发现流水线——最坏情况回落到既有 ON CONFLICT / 23505 行为。
	mock.ExpectQuery(`(?s)SELECT canonical_name FROM models_canonical.*regexp_replace`).
		WillReturnError(errors.New("boom: lookup failed"))

	name := "some-model-v3"
	family := InferFamily(name)
	adoptFoldedExisting(context.Background(), mock, &name, &family)

	if name != "some-model-v3" {
		t.Fatalf("canonicalName = %q, want unchanged on lookup error", name)
	}
}
