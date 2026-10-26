package admin

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func tileOf(id, ts, status string) LiveStreamTile {
	return LiveStreamTile{RequestID: id, Timestamp: ts, Status: status, Model: "gpt-x"}
}

func laneOf(id, dim string, tiles ...LiveStreamTile) LiveStreamLane {
	return LiveStreamLane{ID: id, Name: id, Dimension: dim, Requests: tiles}
}

func snapshotOf(dims map[string][]LiveStreamLane) *LiveStreamSnapshot {
	return &LiveStreamSnapshot{
		Summary:          LiveStreamStats{Total: len(dims["model"])},
		Dimensions:       dims,
		DimensionLegends: map[string][]LiveStreamLegendItem{},
	}
}

// M1: old == nil → every lane becomes a full upsert patch (cold baseline).
func TestComputeTileDeltaColdBaselineFullUpserts(t *testing.T) {
	new := snapshotOf(map[string][]LiveStreamLane{
		"model": {laneOf("gpt-x", "model", tileOf("r1", "2026-10-27T00:00:01Z", "in_progress"))},
	})
	td := ComputeTileDelta(nil, new)
	if td == nil {
		t.Fatal("nil tile delta")
	}
	patch, ok := td.ChangedTiles["model"]["gpt-x"]
	if !ok {
		t.Fatalf("expected full-upsert patch, got %+v", td.ChangedTiles)
	}
	if len(patch.Upserts) != 1 || patch.Upserts[0].RequestID != "r1" {
		t.Fatalf("unexpected upserts %+v", patch.Upserts)
	}
	if len(patch.Removed) != 0 {
		t.Fatalf("unexpected removals %+v", patch.Removed)
	}
}

// M2: unchanged lanes are skipped entirely — the slim contract.
func TestComputeTileDeltaSkipsUnchangedLanes(t *testing.T) {
	base := snapshotOf(map[string][]LiveStreamLane{
		"model": {
			laneOf("gpt-x", "model", tileOf("r1", "2026-10-27T00:00:01Z", "success")),
			laneOf("gpt-y", "model", tileOf("r2", "2026-10-27T00:00:02Z", "success")),
		},
	})
	td := ComputeTileDelta(base, base)
	if len(td.ChangedTiles) != 0 || len(td.RemovedLanes) != 0 {
		t.Fatalf("identical snapshots must produce an empty tile delta, got %+v / %+v", td.ChangedTiles, td.RemovedLanes)
	}
}

// M3: a new tile appends as an upsert; a removed tile lands in Removed.
func TestComputeTileDeltaUpsertAndRemove(t *testing.T) {
	base := snapshotOf(map[string][]LiveStreamLane{
		"model": {laneOf("gpt-x", "model",
			tileOf("r1", "2026-10-27T00:00:01Z", "success"),
			tileOf("r2", "2026-10-27T00:00:02Z", "success"),
		)},
	})
	next := snapshotOf(map[string][]LiveStreamLane{
		"model": {laneOf("gpt-x", "model",
			tileOf("r1", "2026-10-27T00:00:01Z", "failure"),
			tileOf("r3", "2026-10-27T00:00:03Z", "in_progress"),
		)},
	})
	td := ComputeTileDelta(base, next)
	patch, ok := td.ChangedTiles["model"]["gpt-x"]
	if !ok {
		t.Fatalf("expected patch, got %+v", td.ChangedTiles)
	}
	if len(patch.Upserts) != 2 || patch.Upserts[0].Status != "failure" || patch.Upserts[1].RequestID != "r3" {
		t.Fatalf("unexpected upserts %+v", patch.Upserts)
	}
	if !reflect.DeepEqual(patch.Removed, []string{"r2"}) {
		t.Fatalf("unexpected removals %+v", patch.Removed)
	}
}

// M4: a lane that vanished lands in removed_lanes.
func TestComputeTileDeltaRemovedLanes(t *testing.T) {
	base := snapshotOf(map[string][]LiveStreamLane{
		"model": {
			laneOf("gpt-x", "model", tileOf("r1", "2026-10-27T00:00:01Z", "success")),
			laneOf("gpt-y", "model", tileOf("r2", "2026-10-27T00:00:02Z", "success")),
		},
	})
	next := snapshotOf(map[string][]LiveStreamLane{
		"model": {laneOf("gpt-x", "model", tileOf("r1", "2026-10-27T00:00:01Z", "success"))},
	})
	td := ComputeTileDelta(base, next)
	if !reflect.DeepEqual(td.RemovedLanes["model"], []string{"gpt-y"}) {
		t.Fatalf("unexpected removed lanes %+v", td.RemovedLanes)
	}
	if _, ok := td.ChangedTiles["model"]["gpt-y"]; ok {
		t.Fatal("a removed lane must not also carry a patch")
	}
}

// M5: legend emission follows lane patches, mirroring the legacy delta.
func TestComputeTileDeltaLegendsFollowChangedDims(t *testing.T) {
	legends := map[string][]LiveStreamLegendItem{
		"model":    {{Key: "gpt-x", Name: "gpt-x", Count: 1}},
		"provider": {{Key: "openai", Name: "openai", Count: 1}},
	}
	new := snapshotOf(map[string][]LiveStreamLane{
		"model":    {laneOf("gpt-x", "model", tileOf("r1", "2026-10-27T00:00:01Z", "success"))},
		"provider": {laneOf("openai", "provider", tileOf("r1", "2026-10-27T00:00:01Z", "success"))},
	})
	new.DimensionLegends = legends
	td := ComputeTileDelta(nil, new)
	if len(td.DimensionLegends["model"]) != 1 || len(td.DimensionLegends["provider"]) != 1 {
		t.Fatalf("expected legends for both changed dims, got %+v", td.DimensionLegends)
	}
}

// M6: dims filtering drops unsubscribed dimensions; nil keeps everything.
func TestFilterTileDeltaForDims(t *testing.T) {
	td := &LiveStreamTileDelta{
		ChangedTiles: map[string]map[string]liveStreamLanePatch{
			"model":    {"gpt-x": {Stats: LiveStreamStats{Total: 1}}},
			"provider": {"openai": {Stats: LiveStreamStats{Total: 1}}},
		},
		RemovedLanes:     map[string][]string{"vendor": {"v1"}},
		DimensionLegends: map[string][]LiveStreamLegendItem{"model": {{Key: "gpt-x"}}},
	}
	if got := filterTileDeltaForDims(td, nil); got != td {
		t.Fatal("nil dims must be the identity filter")
	}
	out := filterTileDeltaForDims(td, map[string]bool{"model": true})
	if _, ok := out.ChangedTiles["provider"]; ok {
		t.Fatal("provider must be filtered out")
	}
	if _, ok := out.ChangedTiles["model"]; !ok {
		t.Fatal("model must survive the filter")
	}
	if _, ok := out.DimensionLegends["model"]; !ok {
		t.Fatal("legends for subscribed dims must survive")
	}
	if len(out.RemovedLanes) != 0 {
		t.Fatalf("unsubscribed removals must drop, got %+v", out.RemovedLanes)
	}
}

// M7: snapshot dims filtering (initial_data / snapshot_refresh).
func TestFilterSnapshotForDims(t *testing.T) {
	sn := snapshotOf(map[string][]LiveStreamLane{
		"model": {
			laneOf("gpt-x", "model", tileOf("r1", "2026-10-27T00:00:01Z", "success")),
			laneOf("other", "model", tileOf("r9", "2026-10-27T00:00:09Z", "success")),
		},
		"vendor": {laneOf("openai", "vendor", tileOf("r1", "2026-10-27T00:00:01Z", "success"))},
	})
	if got := filterSnapshotForDims(sn, nil); got != sn {
		t.Fatal("nil dims must be the identity filter")
	}
	out := filterSnapshotForDims(sn, map[string]bool{"model": true})
	if _, ok := out.Dimensions["vendor"]; ok {
		t.Fatal("vendor must be filtered out of the snapshot")
	}
	if len(out.Dimensions["model"]) != 2 {
		t.Fatalf("subscribed dim lanes must survive, got %d", len(out.Dimensions["model"]))
	}}

// M8: ?dims= semantics — absent (all) / present-empty (none) / list.
func TestParseLiveStreamDims(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/admin/live-stream", nil)
	if dims, present := parseLiveStreamDims(r); present || dims != nil {
		t.Fatalf("absent param must read (nil, false), got (%v, %v)", dims, present)
	}
	r = httptest.NewRequest("GET", "/api/admin/live-stream?dims=", nil)
	dims, present := parseLiveStreamDims(r)
	if !present || len(dims) != 0 {
		t.Fatalf("empty param must read (empty set, true), got (%v, %v)", dims, present)
	}
	r = httptest.NewRequest("GET", "/api/admin/live-stream?dims=model,bogus,provider", nil)
	dims, _ = parseLiveStreamDims(r)
	if len(dims) != 2 || !dims["model"] || !dims["provider"] {
		t.Fatalf("unexpected dims %+v", dims)
	}
}

// M9: dims key distinctness — payload cache must not merge "all"/""/"a,b".
func TestLiveStreamDimsKey(t *testing.T) {
	cases := []struct {
		dims  map[string]bool
		value string
	}{
		{nil, "all"},
		{map[string]bool{}, ""},
		{map[string]bool{"model": true}, "model"},
		{map[string]bool{"provider": true, "model": true}, "model,provider"},
	}
	for _, tc := range cases {
		if got := liveStreamDimsKey(tc.dims); got != tc.value {
			t.Fatalf("dimsKey(%v) = %q, want %q", tc.dims, got, tc.value)
		}
	}
}

// M10 (wire compat): the legacy envelope variant never serialises tile_delta,
// and the slim variant never serialises delta — the two formats must not
// leak into each other's byte stream.
func TestEnvelopeFormatsDoNotLeak(t *testing.T) {
	dl := &LiveStreamDelta{Summary: LiveStreamStats{Total: 1}}
	td := &LiveStreamTileDelta{Summary: LiveStreamStats{Total: 1}}
	env := LiveStreamEnvelope{Type: "request", Timestamp: time.Now().UTC(), Delta: dl, TileDelta: td}

	legacy := env
	legacy.TileDelta = nil
	b, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if hasJSONKey(t, b, "tile_delta") {
		t.Fatal("legacy envelope must not carry tile_delta")
	}
	if !hasJSONKey(t, b, "delta") {
		t.Fatal("legacy envelope must carry delta")
	}

	slim := env
	slim.Delta = nil
	slim.TileDelta = td
	b, err = json.Marshal(slim)
	if err != nil {
		t.Fatal(err)
	}
	if hasJSONKey(t, b, "delta") {
		t.Fatal("slim envelope must not carry delta")
	}
	if !hasJSONKey(t, b, "tile_delta") {
		t.Fatal("slim envelope must carry tile_delta")
	}
}

// M11: slim JSON shape matches the frontend merge contract (changed_tiles →
// dim → laneId → {name, stats, upserts, removed}; removed_lanes → dim → ids).
func TestTileDeltaJSONShape(t *testing.T) {
	base := snapshotOf(map[string][]LiveStreamLane{
		"model": {laneOf("gpt-x", "model", tileOf("r1", "2026-10-27T00:00:01Z", "success"))},
	})
	next := snapshotOf(map[string][]LiveStreamLane{
		"model": {laneOf("gpt-x", "model",
			tileOf("r2", "2026-10-27T00:00:02Z", "in_progress"),
		)},
	})
	b, err := json.Marshal(ComputeTileDelta(base, next))
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	tiles, ok := wire["changed_tiles"].(map[string]any)
	if !ok {
		t.Fatalf("missing changed_tiles: %s", b)
	}
	modelMap, ok := tiles["model"].(map[string]any)
	if !ok {
		t.Fatalf("missing dim map: %s", b)
	}
	patch, ok := modelMap["gpt-x"].(map[string]any)
	if !ok {
		t.Fatalf("missing lane patch: %s", b)
	}
	if _, ok := patch["upserts"]; !ok {
		t.Fatalf("missing upserts: %s", b)
	}
	// The lane itself survives (r1 rotated out of its tile window), so the
	// removal must surface inside the patch, not as a removed lane.
	removedTiles, ok := patch["removed"].([]any)
	if !ok || len(removedTiles) != 1 || removedTiles[0] != "r1" {
		t.Fatalf("patch.removed must carry [\"r1\"], got %s", b)
	}
}

func hasJSONKey(t *testing.T, raw []byte, key string) bool {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	_, ok := m[key]
	return ok
}
