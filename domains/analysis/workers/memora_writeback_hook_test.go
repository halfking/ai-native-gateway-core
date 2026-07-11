package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type memoraTestDB struct {
	row pgx.Row
}

func (d memoraTestDB) QueryRow(context.Context, string, ...any) pgx.Row { return d.row }
func (d memoraTestDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, nil
}
func (d memoraTestDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

type memoraTestRow struct {
	values []any
	err    error
}

func (r memoraTestRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for i, value := range r.values {
		switch target := dest[i].(type) {
		case *string:
			*target = value.(string)
		case *[]string:
			*target = value.([]string)
		case **string:
			if value == nil {
				*target = nil
			} else {
				v := value.(string)
				*target = &v
			}
		case **int:
			if value == nil {
				*target = nil
			} else {
				v := value.(int)
				*target = &v
			}
		case *int:
			*target = value.(int)
		case *int64:
			*target = value.(int64)
		case *time.Time:
			*target = value.(time.Time)
		default:
			return fmt.Errorf("unsupported scan target %T", target)
		}
	}
	return nil
}

func TestMemoraWritebackHookPostsOwnedSummary(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	row := memoraTestRow{values: []any{
		"session-1", "tenant-1", "user-1", "Title", "Summary",
		[]string{"topic"}, "code", []string{"tool_use"}, 8, "model-1",
		3, int64(120), now, now.Add(time.Minute),
	}}

	var got memoraSummaryPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/session/ingest-summary" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Header.Get("X-API-Key") != "secret" {
			t.Fatalf("missing service key")
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	hook := NewMemoraWritebackHook(memoraTestDB{row: row}, MemoraWritebackConfig{
		BaseURL: server.URL,
		APIKey:  "secret",
	}, nil)
	if err := hook.OnSessionClosed(context.Background(), "tenant-1", "session-1"); err != nil {
		t.Fatal(err)
	}
	if got.UserID != "user-1" || got.Summary != "Summary" || got.QualityScore == nil || *got.QualityScore != 8 {
		t.Fatalf("unexpected payload: %+v", got)
	}
}

func TestMemoraWritebackHookDisabledWithoutCredentials(t *testing.T) {
	hook := NewMemoraWritebackHook(memoraTestDB{}, MemoraWritebackConfig{}, nil)
	if err := hook.OnSessionClosed(context.Background(), "tenant-1", "session-1"); err != nil {
		t.Fatal(err)
	}
}
