package db

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// R39 (2026-09-17) pins: the 087 ensure's not-provisioned verdict must be
// recorded process-wide and consumed by the gateway wiring, and catalog
// errors must be classifiable as schema mismatch so boot retries stop
// burning the budget on them (the 245 deploy-blocker shape).

func TestIsSchemaMismatchErrorClassifiesCatalogErrors(t *testing.T) {
	cases := []struct {
		code string
		want bool
	}{
		{"42P01", true},  // undefined_table
		{"42703", true},  // undefined_column
		{"42883", true},  // undefined_function
		{"42809", true},  // wrong_object_type
		{"0A000", true},  // feature_not_supported (view-dep rewrites)
		{"42P07", false}, // duplicate_table → NOT a mismatch
		{"23505", false}, // unique_violation
		{"", false},
	}
	for _, tc := range cases {
		err := fmt.Errorf("ensure wrap: %w", &pgconn.PgError{Code: tc.code, Message: "x"})
		if got := IsSchemaMismatchError(err); got != tc.want {
			t.Errorf("IsSchemaMismatchError(code=%q) = %v, want %v", tc.code, got, tc.want)
		}
	}
	if IsSchemaMismatchError(errors.New("plain connect error")) {
		t.Errorf("non-PgError must not classify as schema mismatch")
	}
	if IsSchemaMismatchError(nil) {
		t.Errorf("nil must not classify as schema mismatch")
	}
}

func TestProviderTemplatesProvisionedSignalStoredByEnsure(t *testing.T) {
	src, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatalf("read db.go: %v", err)
	}
	const fn = "func (d *DB) ensureFreediscoveryTemplateHealth("
	start := strings.Index(string(src), fn)
	if start < 0 {
		t.Fatalf("ensureFreediscoveryTemplateHealth not found")
	}
	body := string(src)[start:]
	end := strings.Index(body, "\n// providerTemplatesProvisioned records")
	if end < 0 {
		t.Fatalf("cannot find end of ensureFreediscoveryTemplateHealth")
	}
	body = body[:end]

	if !strings.Contains(body, "providerTemplatesProvisioned.Store(false)") {
		t.Errorf("absent-table skip branch must record ProviderTemplatesProvisioned=false")
	}
	if !strings.Contains(body, "providerTemplatesProvisioned.Store(true)") {
		t.Errorf("present-table path must record ProviderTemplatesProvisioned=true")
	}
	// Presence check must pin relkind so a same-named VIEW cannot pass the
	// gate and then blow up the ALTER with 42809.
	if !strings.Contains(body, "relkind IN ('r', 'p')") {
		t.Errorf("presence check must match ordinary/partitioned tables only, not views")
	}
}

func TestGatewayWiringConsumesProvisionedSignal(t *testing.T) {
	src, err := os.ReadFile("../cmd/gateway/main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if !strings.Contains(string(src), "db.ProviderTemplatesProvisioned()") {
		t.Errorf("gateway wiring must gate free-discovery on the provisioned signal")
	}
}
