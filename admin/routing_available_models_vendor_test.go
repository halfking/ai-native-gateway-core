package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Integration test for migration 675: /api/routing/available-models must
// group family rows with a missing/empty model_families.vendor by inferred
// vendor instead of 「其他」 — qwen3.8-27b has family='qwen3.8' which had no
// model_families row, so the featured-model picker showed it under 其他 and
// Alibaba could not be found.
//
// Run with:
//
//	TEST_DATABASE_URL="postgres://..." go test ./admin/ -run TestRoutingAvailableModelsVendorInference -v
func TestRoutingAvailableModelsVendorInference(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// Only meaningful when the dataset contains the qwen3.8 family case.
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM models_canonical WHERE canonical_name = 'qwen3.8-27b' AND status = 'active'`,
	).Scan(&n); err != nil {
		t.Fatalf("probe models_canonical: %v", err)
	}
	if n == 0 {
		t.Skip("qwen3.8-27b not present in this database")
	}

	h := NewHandler(pool, "test-secret", nil)
	req := httptest.NewRequest(http.MethodGet, "/api/routing/available-models", nil)
	rec := httptest.NewRecorder()
	h.handleRoutingAvailableModels(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Families []availableFamilyEntry `json:"families"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	found := false
	for _, fam := range resp.Families {
		for _, v := range fam.Versions {
			if v.CanonicalName != "qwen3.8-27b" {
				continue
			}
			found = true
			if fam.Vendor != "Alibaba" {
				t.Errorf("qwen3.8-27b grouped under vendor %q (family %q), want Alibaba", fam.Vendor, fam.ID)
			}
		}
	}
	if !found {
		t.Fatal("qwen3.8-27b missing from available-models families")
	}
}
