package admin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHeavyMeasurementAllowedTable pins the host policy. Every case is a DSN
// someone could plausibly export into TEST_PG_URL, including the two that must
// NOT be treated as local.
func TestHeavyMeasurementAllowedTable(t *testing.T) {
	cases := []struct {
		name    string
		dsn     string
		optIn   string
		allowed bool
	}{
		{"ipv4 loopback", "postgres://u:p@127.0.0.1:5432/llm_gateway", "", true},
		{"ipv6 loopback", "postgres://u:p@[::1]:5432/llm_gateway", "", true},
		{"localhost name", "postgres://u:p@localhost:5432/llm_gateway", "", true},
		{"unix socket / no host", "postgres:///llm_gateway?host=/var/run/postgresql", "", true},
		{"remote host denied", "postgres://u:p@8.136.114.245:5432/llm_gateway", "", false},
		{"remote host public dns", "postgres://u:p@pg.example.com:5432/llm_gateway", "", false},
		{"remote host opted in", "postgres://u:p@8.136.114.245:5432/llm_gateway", "1", true},
		{"opt-in accepts yes/true/on", "postgres://u:p@db.internal:5432/x", "yes", true},
		{"opt-in rejects noise", "postgres://u:p@db.internal:5432/x", "maybe", false},
		{"keyvalue dsn host parsed", "host=10.0.0.5 port=5432 dbname=llm_gateway", "", false},
		{"keyvalue dsn loopback", "host=127.0.0.1 port=5432 dbname=llm_gateway", "", true},
		{"unparseable fails closed", "://:::not a url", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := heavyMeasurementAllowed(tc.dsn, tc.optIn)
			if got != tc.allowed {
				t.Fatalf("allowed=%v want %v (reason=%q)", got, tc.allowed, reason)
			}
			if !got && reason == "" {
				t.Error("a denial must carry a reason; a silent skip reads like a pass")
			}
			if strings.Contains(reason, "u:p@") {
				t.Errorf("the reason leaks the DSN credentials: %q", reason)
			}
		})
	}
}

// TestHeavyMeasurementGuardIsActuallyWired stops the policy from being a helper
// nobody calls. Go does not error on unused functions, so a guard that is
// defined, documented and unit-tested — but never invoked by the gate it was
// written for — compiles and passes forever. That is the "looks like it exists,
// never actually fires" failure mode.
func TestHeavyMeasurementGuardIsActuallyWired(t *testing.T) {
	const gate = "session_summary_v2_fallback_integration_test.go"
	src, err := os.ReadFile(filepath.Join(".", gate))
	if err != nil {
		t.Fatalf("read %s: %v", gate, err)
	}
	text := string(src)
	if !strings.Contains(text, "heavyMeasurementAllowed(") {
		t.Fatalf("%s never calls heavyMeasurementAllowed — the measurement-grade "+
			"EXPLAIN ANALYZE in this file can still be pointed at a shared or "+
			"production host by exporting TEST_PG_URL", gate)
	}
	// It must run *before* the statement, not after it: a guard placed past the
	// EXPLAIN protects nothing.
	guardAt := strings.Index(text, "heavyMeasurementAllowed(")
	execAt := strings.Index(text, "EXPLAIN (ANALYZE")
	if execAt < 0 {
		t.Fatalf("%s no longer contains the measurement-grade EXPLAIN ANALYZE; "+
			"this guard was written for that statement and should be revisited "+
			"(not silently deleted) if the statement was replaced", gate)
	}
	if guardAt > execAt {
		t.Errorf("the host guard runs at offset %d, after the EXPLAIN ANALYZE at %d — "+
			"it protects nothing in that order", guardAt, execAt)
	}
}
