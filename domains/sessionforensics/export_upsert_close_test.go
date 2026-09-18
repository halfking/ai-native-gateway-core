package sessionforensics_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/sessionforensics"
)

// countingIter wraps a RowIterator and records whether Close was called.
// R43 (2026-09-18): UpsertSummary used to discard the iterator returned by
// Store.Query — with the pgx-backed store that leaks one pool connection per
// call (rows hold their connection until Close). This pin fails if a future
// edit drops the iterator again.
type countingIter struct {
	sessionforensics.RowIterator
	closed    bool
	closeErr  error
	nextCount *int
}

func (c *countingIter) Close() error {
	c.closed = true
	return c.closeErr
}

func (c *countingIter) Next() bool {
	if c.nextCount != nil {
		*c.nextCount++
	}
	return false
}

// queryCaptureStore returns the iterator it is handed and records the SQL.
type queryCaptureStore struct {
	sessionforensics.Store
	it  sessionforensics.RowIterator
	sql []string
}

func (s *queryCaptureStore) Query(ctx context.Context, query string, args ...any) (sessionforensics.RowIterator, error) {
	s.sql = append(s.sql, query)
	return s.it, nil
}

// TestUpsertSummaryClosesQueryIterator pins the R43 connection-leak fix:
// every Store.Query issued by UpsertSummary must be drained and Closed.
func TestUpsertSummaryClosesQueryIterator(t *testing.T) {
	it := &countingIter{RowIterator: &mockRows{}}
	store := &queryCaptureStore{Store: newMockStore(), it: it}
	e := sessionforensics.NewExporterWithStore(store)

	err := e.UpsertSummary(context.Background(), "sess-1", "default",
		sessionforensics.SummaryResult{Title: "t", Summary: "s"})
	if err != nil {
		t.Fatalf("UpsertSummary returned error: %v", err)
	}
	if !it.closed {
		t.Fatal("UpsertSummary did not Close the RowIterator returned by Store.Query (pool connection leak)")
	}
	if len(store.sql) != 1 || !strings.Contains(strings.ToUpper(store.sql[0]), "INSERT INTO SESSION_SUMMARIES") {
		t.Fatalf("expected exactly one session_summaries INSERT, got %q", store.sql)
	}
}

// TestUpsertSummaryPropagatesIteratorErr pins that a draining error from the
// iterator surfaces instead of being silently swallowed after the R43 fix.
func TestUpsertSummaryPropagatesIteratorErr(t *testing.T) {
	want := errors.New("drain failed")
	it := &countingIter{RowIterator: &mockRows{}, closeErr: nil}
	iter := &errIter{RowIterator: it, err: want}
	store := &queryCaptureStore{Store: newMockStore(), it: iter}
	e := sessionforensics.NewExporterWithStore(store)

	err := e.UpsertSummary(context.Background(), "sess-1", "default",
		sessionforensics.SummaryResult{Title: "t", Summary: "s"})
	if !errors.Is(err, want) {
		t.Fatalf("UpsertSummary err = %v, want %v", err, want)
	}
}

type errIter struct {
	sessionforensics.RowIterator
	err error
}

func (e *errIter) Err() error { return e.err }
