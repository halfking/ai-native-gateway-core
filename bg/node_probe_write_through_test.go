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
	} {
		if !strings.Contains(sql, marker) {
			t.Fatalf("healthy credential SQL missing %q", marker)
		}
	}
	if strings.Contains(sql, "permanently_exhausted") || strings.Contains(sql, "balance_exhausted") {
		t.Fatal("healthy credential SQL must not refuse hard-quota rows")
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
