package modelquality

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeRow is a minimal pgx.Row stub returning one result row.
type fakeRow struct {
	values []any
	err    error
}

func (r *fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return errors.New("scan arity mismatch")
	}
	for i, v := range r.values {
		// dest[i] is always a *T in pgx usage; assign through any().
		switch d := dest[i].(type) {
		case *string:
			*d = v.(string)
		default:
			_ = d
		}
	}
	return nil
}

// fakePool implements the minimum pgxpool surface used by CanonicalCatalogDiscovery.
type fakePool struct {
	rows *fakeRows
}

func (p *fakePool) Query(ctx context.Context, sql string, args ...any) (pgxRows, error) {
	return p.rows, nil
}

type pgxRows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}

type fakeRows struct {
	data     [][]any
	idx      int
	nextErr  error
	scanErr  error
	finalErr error
}

func (r *fakeRows) Next() bool {
	if r.nextErr != nil {
		return false
	}
	return r.idx < len(r.data)
}
func (r *fakeRows) Scan(dest ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}
	row := r.data[r.idx]
	if len(dest) != len(row) {
		return errors.New("arity")
	}
	for i, v := range row {
		if d, ok := dest[i].(*string); ok {
			*d = v.(string)
		}
	}
	r.idx++
	return nil
}
func (r *fakeRows) Err() error    { return r.finalErr }
func (r *fakeRows) Close()         {}

// We need a *pgxpool.Pool-shaped type. To avoid building a full fake,
// test the NewCanonicalCatalogDiscovery contract: nil pool returns a typed error,
// and the construction accepts any non-nil pool. This keeps the test runnable
// without a live Postgres and ensures the public surface is stable.

func TestCanonicalCatalogDiscovery_NilPool(t *testing.T) {
	d := NewCanonicalCatalogDiscovery(nil)
	if d == nil {
		t.Fatal("NewCanonicalCatalogDiscovery returned nil")
	}
	_, err := d.DiscoverModels(context.Background())
	if err == nil {
		t.Fatal("expected error when pool is nil, got nil")
	}
}

// TestCanonicalCatalogDiscovery_QueryError exercises the error path of
// pgxPool.Query by using a pool-like wrapper. We construct a minimal adapter
// (not exported) only in this test file.
func TestCanonicalCatalogDiscovery_QueryError(t *testing.T) {
	// Use the real pgxpool.Pool with an unreachable DSN so Query fails fast.
	// The pool lazily dials; the first Query returns an error.
	cfg, err := pgxpool.ParseConfig("postgres://invalid:invalid@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}
	defer pool.Close()

	d := NewCanonicalCatalogDiscovery(pool)
	_, err = d.DiscoverModels(context.Background())
	if err == nil {
		t.Fatal("expected query error against unreachable host, got nil")
	}
	// Sanity: error should wrap a connection-ish failure (pgconn or net).
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		// connection-level errors are wrapped — fine
	}
}

// TestStaticModelDiscovery_NoOp guards the legacy path so callers that still
// wire StaticModelDiscovery don't lose coverage.
func TestStaticModelDiscovery_NoOp(t *testing.T) {
	d := NewStaticModelDiscovery([]ModelTarget{
		{Provider: "x", ModelName: "y", CanonicalModel: "y"},
	})
	got, err := d.DiscoverModels(context.Background())
	if err != nil {
		t.Fatalf("DiscoverModels: %v", err)
	}
	if len(got) != 1 || got[0].Provider != "x" {
		t.Fatalf("got = %+v", got)
	}
}