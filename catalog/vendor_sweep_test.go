package catalog

// THROWAWAY local verification (not committed): sweep every active canonical
// model through InferVendor and report families that still fail recognition.

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestVendorSweepLocal(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT mc.family, (ARRAY_AGG(mc.canonical_name ORDER BY mc.canonical_name))[1]
		FROM models_canonical mc
		WHERE mc.status = 'active'
		GROUP BY mc.family ORDER BY mc.family
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	unknown := 0
	for rows.Next() {
		var family, sample string
		if err := rows.Scan(&family, &sample); err != nil {
			t.Fatal(err)
		}
		if v := InferVendor(sample, family); v == "" {
			unknown++
			fmt.Printf("UNRECOGNIZED family=%-18s sample=%s\n", family, sample)
		}
	}
	fmt.Printf("sweep done: %d families unrecognized\n", unknown)
}
