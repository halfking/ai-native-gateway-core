package source

import (
	"testing"
	"time"
)

// TestAggregate_BasicCounts verifies the core aggregation: calls, success/fail,
// avg latency, parameters sorted by frequency.
func TestAggregate_BasicCounts(t *testing.T) {
	now := time.Now()
	since := now.Add(-24 * time.Hour)
	records := []CallRecord{
		{ToolID: "web_search", Args: map[ParameterName]any{"query": "go test", "limit": 10}, Success: true, LatencyMs: 200},
		{ToolID: "web_search", Args: map[ParameterName]any{"query": "k8s", "limit": 5}, Success: true, LatencyMs: 250},
		{ToolID: "web_search", Args: map[ParameterName]any{"query": "ab"}, Success: false, LatencyMs: 100, ErrorMsg: "query too short"},
	}
	u := Aggregate("web_search", "t1", since, now, records)

	if u.TotalCalls != 3 {
		t.Errorf("TotalCalls = %d, want 3", u.TotalCalls)
	}
	if u.SuccessCount != 2 {
		t.Errorf("SuccessCount = %d, want 2", u.SuccessCount)
	}
	if u.ErrorCount != 1 {
		t.Errorf("ErrorCount = %d, want 1", u.ErrorCount)
	}
	if u.AvgLatencyMs != (200+250+100)/3 {
		t.Errorf("AvgLatencyMs = %d, want %d", u.AvgLatencyMs, (200+250+100)/3)
	}
	// Parameters sorted by frequency: query(3), limit(2).
	if len(u.Parameters) != 2 {
		t.Fatalf("Parameters = %d, want 2", len(u.Parameters))
	}
	if u.Parameters[0].Name != "query" || u.Parameters[0].TotalCalls != 3 {
		t.Errorf("param[0] = %+v, want query/3", u.Parameters[0])
	}
	if u.Parameters[1].Name != "limit" || u.Parameters[1].TotalCalls != 2 {
		t.Errorf("param[1] = %+v, want limit/2", u.Parameters[1])
	}
}

// TestAggregate_SampleErrors_CappedAt8 verifies the error sample cap.
func TestAggregate_SampleErrors_CappedAt8(t *testing.T) {
	now := time.Now()
	since := now.Add(-time.Hour)
	records := make([]CallRecord, 20)
	for i := range records {
		records[i] = CallRecord{
			ToolID:    "x",
			Success:   false,
			ErrorMsg:  "err " + string(rune('A'+i)),
			LatencyMs: 10,
		}
	}
	u := Aggregate("x", "t1", since, now, records)
	if len(u.SampleErrors) != 8 {
		t.Errorf("SampleErrors = %d, want 8 (capped)", len(u.SampleErrors))
	}
}

// TestAggregate_DistinctCount verifies distinct value counting.
func TestAggregate_DistinctCount(t *testing.T) {
	now := time.Now()
	records := []CallRecord{
		{Args: map[ParameterName]any{"region": "us-east"}},
		{Args: map[ParameterName]any{"region": "us-west"}},
		{Args: map[ParameterName]any{"region": "us-east"}}, // dup
		{Args: map[ParameterName]any{"region": "eu"}},
	}
	u := Aggregate("x", "t1", now, now, records)
	var region ArgUsage
	for _, p := range u.Parameters {
		if p.Name == "region" {
			region = p
		}
	}
	if region.DistinctVals != 3 {
		t.Errorf("DistinctVals = %d, want 3 (us-east/us-west/eu)", region.DistinctVals)
	}
	if region.TotalCalls != 4 {
		t.Errorf("TotalCalls = %d, want 4", region.TotalCalls)
	}
}

// TestMemory_FetchUsage_Hit verifies Seed → Fetch round-trip.
func TestMemory_FetchUsage_Hit(t *testing.T) {
	m := NewMemory()
	now := time.Now()
	since := now.Add(-24 * time.Hour)
	u := ToolUsage{
		ToolID: "x", TenantID: "t1",
		WindowStart: since, WindowEnd: now,
		TotalCalls: 5, SuccessCount: 4,
	}
	m.Seed("t1", u)

	got, err := m.FetchUsage("t1", "x", since)
	if err != nil {
		t.Fatalf("FetchUsage: %v", err)
	}
	if got.TotalCalls != 5 {
		t.Errorf("TotalCalls = %d, want 5", got.TotalCalls)
	}
}

// TestMemory_FetchUsage_UnknownTool returns ErrUnknownTool.
func TestMemory_FetchUsage_UnknownTool(t *testing.T) {
	m := NewMemory()
	_, err := m.FetchUsage("t1", "ghost", time.Time{})
	if err != ErrUnknownTool {
		t.Errorf("err = %v, want ErrUnknownTool", err)
	}
}

// TestMemory_FetchUsage_OldWindow returns ErrNoData when seeded data is older
// than the caller's `since`.
func TestMemory_FetchUsage_OldWindow(t *testing.T) {
	m := NewMemory()
	old := ToolUsage{ToolID: "x", WindowStart: time.Now().Add(-7 * 24 * time.Hour)}
	m.Seed("t1", old)

	_, err := m.FetchUsage("t1", "x", time.Now().Add(-time.Hour))
	if err != ErrNoData {
		t.Errorf("err = %v, want ErrNoData", err)
	}
}

// TestMemory_ListTools_Sorted verifies the list contract.
func TestMemory_ListTools_Sorted(t *testing.T) {
	m := NewMemory()
	m.Seed("t1", ToolUsage{ToolID: "zeta"})
	m.Seed("t1", ToolUsage{ToolID: "alpha"})
	m.Seed("t1", ToolUsage{ToolID: "mu"})

	got, err := m.ListTools("t1")
	if err != nil {
		t.Fatal(err)
	}
	want := []ToolID{"alpha", "mu", "zeta"}
	if len(got) != 3 {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

// TestMemory_ListTools_EmptyTenant returns empty slice (not nil error).
func TestMemory_ListTools_EmptyTenant(t *testing.T) {
	m := NewMemory()
	got, err := m.ListTools("ghost-tenant")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}
