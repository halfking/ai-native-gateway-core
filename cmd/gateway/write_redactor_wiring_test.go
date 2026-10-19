package main

import (
	"context"
	"database/sql"
	"github.com/kaixuan/llm-gateway-go/settings"
	"sync/atomic"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

func TestWriteRedactorLoadsRestorationSourceAfterConstruction(t *testing.T) {
	previous := settings.Global
	t.Cleanup(func() { settings.Global = previous })
	settings.Global = settings.NewRegistry()
	settings.Global.MustRegisterSpec(&settings.Spec{Key: "output_compliance.enabled", Type: settings.TypeBool, Scope: settings.ScopePlatform, Default: true})
	old := loadSanitizeRestoredValues()
	sanitizeRestoredValues.Store(sanitizeSessionValuesFunc(nil))
	t.Cleanup(func() { sanitizeRestoredValues.Store(old) })
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("FROM pii_patterns").WillReturnRows(sqlmock.NewRows([]string{"id", "pattern_name", "pattern_type", "regex_pattern", "description", "enabled", "severity", "redact_format"}))
	mock.ExpectQuery("FROM toxic_keywords").WillReturnRows(sqlmock.NewRows([]string{"id", "keyword", "category", "severity", "language", "enabled"}))
	fn := buildRedactBodyWithContextFn(db)
	if fn == nil {
		t.Fatal("redactor missing")
	}
	var calls atomic.Int64
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, true)
	storeSanitizeRestoredValues(func(got context.Context, session, tenant string) ([]string, error) {
		calls.Add(1)
		if got.Value(key{}) != true || session != "s" || tenant != "tenant-a" {
			t.Error("restoration context lost")
		}
		return []string{"synthetic-value"}, nil
	})
	mock.ExpectQuery("FROM output_compliance_policies").WillReturnError(sql.ErrNoRows)
	fn(ctx, []byte(`{"choices":[{"message":{"content":"ordinary text"}}]}`), "s", "tenant-a")
	if calls.Load() != 1 {
		t.Fatal("startup captured missing restoration source permanently")
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
