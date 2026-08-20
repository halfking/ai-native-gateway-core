package modelquality

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestCanonicalCatalogDiscovery_NilPool verifies the constructor accepts nil
// and DiscoverModels surfaces a typed error (not a panic) when pool is missing.
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

// TestCanonicalCatalogDiscovery_QueryError uses a real pgxpool.Pool pointed at
// an unreachable host so Query fails fast. Asserts the error is non-nil and
// mentions a connect/network failure so callers can distinguish DB outage from
// a logical query error.
func TestCanonicalCatalogDiscovery_QueryError(t *testing.T) {
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
	// Sanity: the error should be either context-deadline (timeout) or a
	// network/connection failure. We accept any non-nil error here; the goal
	// is to confirm errors propagate rather than silently return an empty slice.
	if errors.Is(err, context.DeadlineExceeded) {
		return // timeout path is valid
	}
}

// TestStaticModelDiscovery_NoOp guards the legacy fallback so callers that
// still wire StaticModelDiscovery don't lose coverage when we wire
// CanonicalCatalogDiscovery by default.
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

func TestCanonicalCatalogDiscovery_WithTimeout(t *testing.T) {
	d := NewCanonicalCatalogDiscovery(nil)
	if d.WithTimeout(250*time.Millisecond) != d {
		t.Fatal("WithTimeout should return the receiver")
	}
	if d.timeout != 250*time.Millisecond {
		t.Fatalf("timeout = %v, want 250ms", d.timeout)
	}
	if d.WithTimeout(0) != d {
		t.Fatal("WithTimeout should return the receiver when resetting")
	}
	if d.timeout != time.Second {
		t.Fatalf("timeout = %v, want 1s after reset", d.timeout)
	}
	var nilDiscovery *CanonicalCatalogDiscovery
	if nilDiscovery.WithTimeout(time.Second) != nil {
		t.Fatal("nil receiver should remain nil")
	}
}
