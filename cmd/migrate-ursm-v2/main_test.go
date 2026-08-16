package main

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

// TestMapRow_PausedBecomesManualHold pins the migration contract
// that legacy paused=true → URSM v2 manual_hold=1. If we lose this,
// a paused credential becomes "available" after cutover and the
// gateway will route to a credential the operator intentionally
// took out of rotation.
func TestMapRow_PausedBecomesManualHold(t *testing.T) {
	r := probeRow{
		TenantID:             "tenant-42",
		CredentialID:         42,
		RawModel:             "gpt-4",
		ConsecutiveFailures:  99,
		ConsecutiveSuccesses: 0,
		Paused:               true,
	}
	got := mapRow(r, "ursm:v2:")
	if got.Fields["manual_hold"] != "1" {
		t.Fatalf("manual_hold=%q, want 1", got.Fields["manual_hold"])
	}
	if got.Fields["available"] != "0" {
		t.Fatalf("available=%q, want 0", got.Fields["available"])
	}
	if got.Fields["disabled"] != "1" {
		t.Fatalf("disabled=%q, want 1", got.Fields["disabled"])
	}
	if !strings.HasPrefix(got.NodeKey, "ursm:v2:node:tenant-42:42:gpt-4") {
		t.Fatalf("node key=%q, want prefix ursm:v2:node:tenant-42:42:gpt-4", got.NodeKey)
	}
}

// TestMapRow_HealthyAvailable pins the migration contract that a
// legacy row with no failures → URSM v2 available=1.
func TestMapRow_HealthyAvailable(t *testing.T) {
	now := time.Now()
	r := probeRow{
		CredentialID:        7,
		RawModel:            "claude-3-opus",
		ConsecutiveFailures: 0,
		LastAttemptAt:       sql.NullTime{Time: now, Valid: true},
		LastDirectOK:        sql.NullBool{Bool: true, Valid: true},
		Paused:              false,
	}
	got := mapRow(r, "test:")
	if got.Fields["available"] != "1" {
		t.Fatalf("available=%q, want 1", got.Fields["available"])
	}
	if got.Fields["disabled"] != "0" {
		t.Fatalf("disabled=%q, want 0", got.Fields["disabled"])
	}
	if got.Fields["manual_hold"] != "" {
		t.Fatalf("manual_hold=%q, want unset (healthy must NOT auto-hold)", got.Fields["manual_hold"])
	}
	if got.Fields["last_ok_ms"] == "" {
		t.Fatal("last_ok_ms must be set when LastAttemptAt is valid")
	}
}

// TestMapRow_FailStreakCool pins the migration contract that a legacy
// row with consecutive_failures >= FailStreakLimit → URSM v2
// disabled=1 with a cool_until_ms in the future.
func TestMapRow_FailStreakCool(t *testing.T) {
	r := probeRow{
		CredentialID:        100,
		RawModel:            "minimax-m3",
		ConsecutiveFailures: 5,
		Paused:              false,
		LastErrCode:         sql.NullString{String: "rate_limit", Valid: true},
	}
	got := mapRow(r, "ursm:v2:")
	if got.Fields["available"] != "0" {
		t.Fatalf("available=%q, want 0", got.Fields["available"])
	}
	if got.Fields["disabled"] != "1" {
		t.Fatalf("disabled=%q, want 1", got.Fields["disabled"])
	}
	coolMs := got.Fields["cool_until_ms"]
	if coolMs == "" {
		t.Fatal("cool_until_ms must be set when consecutive_failures >= limit")
	}
	// Verify cool_until is in the future.
	if got, want := coolMs, "0"; got == want {
		t.Fatalf("cool_until_ms=%q, want >0", coolMs)
	}
	if got.Fields["last_err"] != "rate_limit" {
		t.Fatalf("last_err=%q, want rate_limit", got.Fields["last_err"])
	}
}

// TestMapRow_GenerationMonotonic pins that the migrated generation=1 +
// source_priority=10 contract matches what URSM v2 expects so live
// RecordRequest writes (which always carry higher gen via
// NodeMirror.ApplyFromAPI) can safely overwrite migrated entries.
func TestMapRow_GenerationMonotonic(t *testing.T) {
	r := probeRow{CredentialID: 1, RawModel: "m", ConsecutiveFailures: 0}
	got := mapRow(r, "ursm:v2:")
	if got.Fields["generation"] != "1" {
		t.Fatalf("generation=%q, want 1 (baseline for migration)", got.Fields["generation"])
	}
	if got.Fields["source_priority"] != "10" {
		t.Fatalf("source_priority=%q, want 10 (Request priority; lower than Admin=40)", got.Fields["source_priority"])
	}
}

// TestSummaryClassification (B3 audit fix) pins the dry-run / apply
// summary counter logic. The previous implementation string-concatenated
// "available|manual_hold|disabled" and matched against hard-coded tuples,
// but the healthy path produces ("1", "", "0") → "1||0" — none of the
// cases matched, so every healthy node fell through to 0. This test
// would have caught the bug.
func TestSummaryClassification(t *testing.T) {
	nodes := []mappedNode{
		// Healthy: available=1, manual_hold=unset, disabled=0
		{NodeKey: "k1", Fields: map[string]string{"available": "1", "disabled": "0"}},
		// In-cool: available=0, manual_hold=unset, disabled=1, cool_until_ms set
		{NodeKey: "k2", Fields: map[string]string{"available": "0", "disabled": "1", "cool_until_ms": "999"}},
		// Manual hold: available=0, manual_hold=1, disabled=1
		{NodeKey: "k3", Fields: map[string]string{"available": "0", "disabled": "1", "manual_hold": "1"}},
		// Edge: available=1 + manual_hold=1 (admin override, but not zero) — count as manual_hold
		{NodeKey: "k4", Fields: map[string]string{"available": "1", "disabled": "1", "manual_hold": "1"}},
	}
	available, cooled, manualHold := classifyNodes(nodes)
	if available != 1 {
		t.Fatalf("available=%d, want 1", available)
	}
	if cooled != 1 {
		t.Fatalf("cooled=%d, want 1", cooled)
	}
	if manualHold != 2 {
		t.Fatalf("manualHold=%d, want 2", manualHold)
	}
}

// TestEnvOr pins the env-or-default helper so a future refactor
// doesn't accidentally flip the precedence.
func TestEnvOr(t *testing.T) {
	t.Setenv("MIGRATE_TEST_KEY", "from-env")
	if got := envOr("MIGRATE_TEST_KEY", "fallback"); got != "from-env" {
		t.Fatalf("envOr with set key=%q, want from-env", got)
	}
	if got := envOr("MIGRATE_TEST_KEY_UNSET", "fallback"); got != "fallback" {
		t.Fatalf("envOr with unset key=%q, want fallback", got)
	}
	if got := envOr("MIGRATE_TEST_KEY_EMPTY", "fallback"); got != "fallback" {
		t.Fatalf("envOr with empty-string key=%q, want fallback", got)
	}
	t.Setenv("MIGRATE_TEST_KEY_EMPTY", "")
	if got := envOr("MIGRATE_TEST_KEY_EMPTY", "fallback"); got != "fallback" {
		t.Fatalf("envOr with empty-after-set key=%q, want fallback", got)
	}
}

func TestRedisURLFromEnv(t *testing.T) {
	t.Setenv("REDIS_URL", "")
	t.Setenv("LLM_GATEWAY_REDIS_ADDR", "redis.example:6380")
	password := "p" + "@ss/word"
	t.Setenv("LLM_GATEWAY_REDIS_PASSWORD", password)
	t.Setenv("LLM_GATEWAY_REDIS_DB", "7")
	got := redisURLFromEnv()
	if !strings.Contains(got, "redis.example:6380/7") || !strings.Contains(got, "p%40ss%2Fword") {
		t.Fatalf("redisURLFromEnv() = %q, want encoded password and db=7", got)
	}

	t.Setenv("REDIS_URL", "redis://explicit:6379/4")
	if got, want := redisURLFromEnv(), "redis://explicit:6379/4"; got != want {
		t.Fatalf("explicit REDIS_URL = %q, want %q", got, want)
	}

	t.Setenv("REDIS_URL", "")
	t.Setenv("LLM_GATEWAY_REDIS_ADDR", "")
	t.Setenv("LLM_GATEWAY_REDIS_PASSWORD", "")
	t.Setenv("LLM_GATEWAY_REDIS_DB", "")
	if got, want := redisURLFromEnv(), defaultRedisURL; got != want {
		t.Fatalf("default Redis URL = %q, want %q", got, want)
	}
}

// TestMaskDSN ensures the connection-string echo on startup doesn't
// leak credentials into operator-visible logs.
func TestMaskDSN(t *testing.T) {
	cases := []struct{ in, want string }{
		{"postgres://user:pass@host:5432/db", "postgres://***@host:5432/db"},
		{"redis://localhost:6379/0", "redis://localhost:6379/0"},
		{"redis://:" + "supersecret" + "@10.0.0.1:6379/0", "redis://***@10.0.0.1:6379/0"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := maskDSN(tc.in); got != tc.want {
			t.Fatalf("maskDSN(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
