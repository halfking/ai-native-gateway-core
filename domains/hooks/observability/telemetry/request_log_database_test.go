package telemetry

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRequestLogDatabaseNilPoolIsNilInterface(t *testing.T) {
	client := NewClient()
	if got := client.requestLogDatabase(); got != nil {
		t.Fatalf("new client returned typed nil database: %T", got)
	}
	client.SetDB((*pgxpool.Pool)(nil))
	if got := client.requestLogDatabase(); got != nil {
		t.Fatalf("SetDB(nil) returned typed nil database: %T", got)
	}
}
