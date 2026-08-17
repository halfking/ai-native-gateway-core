package bootstrap

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestMapRowUsesConfiguredCoolSeconds(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	node := mapRow(probeRow{
		TenantID:            "tenant-a",
		CredentialID:        42,
		RawModel:            "model-a",
		ConsecutiveFailures: failStreakLimit,
	}, "ursm:v2:", 120, now)

	got, err := strconv.ParseInt(node.Fields["cool_until_ms"], 10, 64)
	if err != nil {
		t.Fatalf("cool_until_ms=%q: %v", node.Fields["cool_until_ms"], err)
	}
	want := now.Add(120 * time.Second).UnixMilli()
	if got != want {
		t.Fatalf("cool_until_ms=%d, want %d", got, want)
	}
}

func TestMapRowPreservesManualHold(t *testing.T) {
	node := mapRow(probeRow{
		TenantID:     "tenant-a",
		CredentialID: 42,
		RawModel:     "model-a",
		Paused:       true,
		LastDirectOK: pgtype.Bool{Bool: true, Valid: true},
	}, "ursm:v2:", 120, time.Now())
	if node.Fields["manual_hold"] != "1" || node.Fields["available"] != "0" {
		t.Fatalf("paused legacy row mapped to %v, want manual hold", node.Fields)
	}
	if !strings.HasPrefix(node.Key, "ursm:v2:node:tenant-a:42:model-a") {
		t.Fatalf("node key=%q, want tenant-aware key", node.Key)
	}
}

func TestMapRowHealthyAndFailedContracts(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	healthy := mapRow(probeRow{
		CredentialID:        7,
		RawModel:            "model-a",
		LastAttemptAt:       pgtype.Timestamptz{Time: now, Valid: true},
		LastDirectOK:        pgtype.Bool{Bool: true, Valid: true},
		ConsecutiveFailures: 0,
	}, "ursm:v2:", 120, now)
	if healthy.Fields["available"] != "1" || healthy.Fields["disabled"] != "0" || healthy.Fields["last_ok_ms"] == "" {
		t.Fatalf("healthy legacy row mapped to %v", healthy.Fields)
	}
	if healthy.Fields["generation"] != "1" || healthy.Fields["source_priority"] != "10" {
		t.Fatalf("bootstrap generation contract=%v", healthy.Fields)
	}
}
