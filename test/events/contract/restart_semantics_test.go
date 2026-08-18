package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type restartFixture struct {
	SchemaVersion  int                  `json:"schema_version"`
	UnknownVersion string               `json:"unknown_version"`
	Cases          []restartFixtureCase `json:"cases"`
}

type restartFixtureCase struct {
	Lane                      string `json:"lane"`
	ExecutionRecovery         bool   `json:"execution_recovery"`
	ResultReplay              bool   `json:"result_replay"`
	LeaseRequired             bool   `json:"lease_required"`
	FencingRequired           bool   `json:"fencing_required"`
	MetadataRebuild           bool   `json:"metadata_rebuild"`
	LeaseLiveUpstreamCalls    int    `json:"lease_live_upstream_calls"`
	LeaseExpiredUpstreamCalls int    `json:"lease_expired_upstream_calls"`
	TakeoverRequiresFencing   bool   `json:"takeover_requires_fencing"`
	ExpectedProjection        string `json:"expected_projection"`
}

func TestRestartSemanticsV1Fixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "fixtures", "restart_semantics_v1_valid.json"))
	if err != nil {
		t.Fatalf("read restart fixture: %v", err)
	}
	var fixture restartFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode restart fixture: %v", err)
	}
	if fixture.SchemaVersion != 1 || fixture.UnknownVersion != "reject" {
		t.Fatalf("invalid restart fixture header: %+v", fixture)
	}
	want := map[string]restartFixtureCase{
		"ordinary_dispatch": {ExpectedProjection: "dropped"},
		"pending":           {ResultReplay: true, ExpectedProjection: "replayed_result"},
		"durable":           {ExecutionRecovery: true, ResultReplay: true, LeaseRequired: true, FencingRequired: true, LeaseExpiredUpstreamCalls: 1, TakeoverRequiresFencing: true, ExpectedProjection: "recovered_terminal"},
		"queue_mirror":      {MetadataRebuild: true, ExpectedProjection: "metadata_only"},
	}
	if len(fixture.Cases) != len(want) {
		t.Fatalf("restart cases = %d, want %d", len(fixture.Cases), len(want))
	}
	seen := make(map[string]bool, len(fixture.Cases))
	for _, got := range fixture.Cases {
		if seen[got.Lane] {
			t.Fatalf("duplicate restart lane %q", got.Lane)
		}
		seen[got.Lane] = true
		expected, ok := want[got.Lane]
		if !ok {
			t.Fatalf("unexpected restart lane %q", got.Lane)
		}
		if got.ExecutionRecovery != expected.ExecutionRecovery || got.ResultReplay != expected.ResultReplay || got.LeaseRequired != expected.LeaseRequired || got.FencingRequired != expected.FencingRequired || got.MetadataRebuild != expected.MetadataRebuild || got.LeaseLiveUpstreamCalls != expected.LeaseLiveUpstreamCalls || got.LeaseExpiredUpstreamCalls != expected.LeaseExpiredUpstreamCalls || got.TakeoverRequiresFencing != expected.TakeoverRequiresFencing || got.ExpectedProjection != expected.ExpectedProjection {
			t.Fatalf("restart semantics for %q = %+v, want %+v", got.Lane, got, expected)
		}
		if got.Lane != "durable" && (got.LeaseLiveUpstreamCalls != 0 || got.LeaseExpiredUpstreamCalls != 0) {
			t.Fatalf("%s must not issue upstream work after restart", got.Lane)
		}
	}
	for lane := range want {
		if !seen[lane] {
			t.Fatalf("missing restart lane %q", lane)
		}
	}
}
