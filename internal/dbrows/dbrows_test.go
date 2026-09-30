package dbrows

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type fakeRows struct {
	err error
}

func (f fakeRows) Close()                                        {}
func (f fakeRows) Err() error                                    { return f.err }
func (f fakeRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (f fakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (f fakeRows) Next() bool                                    { return false }
func (f fakeRows) Scan(dest ...any) error                       { return nil }
func (f fakeRows) Values() ([]any, error)                       { return nil, nil }
func (f fakeRows) RawValues() [][]byte                          { return nil }
func (f fakeRows) Conn() *pgx.Conn                               { return nil }

func TestErr_PassthroughAndAbort(t *testing.T) {
	if err := Err(fakeRows{}); err != nil {
		t.Fatalf("clean iteration must return nil, got %v", err)
	}
	boom := errors.New("conn closed")
	if err := Err(fakeRows{err: boom}); !errors.Is(err, boom) {
		t.Fatalf("iteration abort must propagate, got %v", err)
	}
	if err := Err(nil); err != nil {
		t.Fatalf("nil rows must return nil, got %v", err)
	}
}

func TestSkipOrFail(t *testing.T) {
	if SkipOrFail("op", nil) {
		t.Fatalf("nil err must not be treated as skip")
	}
	if !SkipOrFail("op", errors.New("bad row")) {
		t.Fatalf("non-nil err must signal skip")
	}
}
