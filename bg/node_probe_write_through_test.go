package bg

import (
	"os"
	"strings"
	"testing"
)

func TestHealthyCredentialSQLClearsHardQuota(t *testing.T) {
	sql := healthyCredentialSQL()
	for _, marker := range []string{
		"health_status = 'healthy'",
		"availability_state = 'ready'",
		"quota_state = 'ok'",
		"quota_recover_at = NULL",
	} {
		if !strings.Contains(sql, marker) {
			t.Fatalf("healthy credential SQL missing %q", marker)
		}
	}
	if strings.Contains(sql, "permanently_exhausted") || strings.Contains(sql, "balance_exhausted") {
		t.Fatal("healthy credential SQL must not refuse hard-quota rows")
	}
}

// The executor fires MarkNodeProbeHealthy on EVERY successful business
// request. The write-through must therefore be a no-op (0 rows) when the
// surfaces are already healthy, otherwise each success rewrites the hot
// credentials row.
func TestHealthyWriteThroughSQLIsNoOpWhenAlreadyHealthy(t *testing.T) {
	cred := healthyCredentialSQL()
	for _, guard := range []string{
		"health_status IS DISTINCT FROM 'healthy'",
		"availability_state IS DISTINCT FROM 'ready'",
		"COALESCE(quota_state, 'ok') <> 'ok'",
	} {
		if !strings.Contains(cred, guard) {
			t.Fatalf("healthy credential SQL must skip already-healthy rows, missing %q", guard)
		}
	}
	binding := healthyBindingSQL()
	if !strings.Contains(binding, "COALESCE(cmb.available, FALSE) = FALSE") {
		t.Fatal("healthy binding SQL must only touch unavailable bindings")
	}
	if !strings.Contains(binding, "NOT LIKE 'manual%'") {
		t.Fatal("healthy binding SQL must leave manual bindings alone")
	}
}

func TestMarkNodeProbeHealthyWriteThrough(t *testing.T) {
	src, err := os.ReadFile("node_probe.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "syncHealthyNodeSurfaces(ctx, db, credentialID, rawModel)") {
		t.Fatal("MarkNodeProbeHealthy must write binding/credential surfaces")
	}
}

func TestUpdateCredentialHealthUsesWriteThroughSQL(t *testing.T) {
	src, err := os.ReadFile("node_probe.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "healthyCredentialSQL()") {
		t.Fatal("updateCredentialHealth must reuse healthyCredentialSQL")
	}
}
