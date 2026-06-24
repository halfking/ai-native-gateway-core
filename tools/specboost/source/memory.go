// Package source — in-memory implementation (C1-3 v1).
//
// Real implementation reads from registry.Tool + audit.Event streams; this
// in-memory implementation lets C1-3 ship as a pure interface + tests now,
// while the real data plumbing (C1-4 batch benchmark) lands separately.
package source

import (
	"sort"
	"sync"
	"time"
)

// Memory is a Source backed by a fixed map of ToolUsage records. Tests
// construct one and inject it; production code (when it lands) will wrap
// a query layer over registry + audit.
type Memory struct {
	mu       sync.RWMutex
	byTenant map[string]map[ToolID]ToolUsage
}

// NewMemory returns an empty in-memory source.
func NewMemory() *Memory {
	return &Memory{byTenant: make(map[string]map[ToolID]ToolUsage)}
}

// Seed inserts a usage record. The (tenantID, toolID) key replaces any
// previous entry — tests should Seed distinct tool IDs.
func (m *Memory) Seed(tenantID string, u ToolUsage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.byTenant[tenantID] == nil {
		m.byTenant[tenantID] = make(map[ToolID]ToolUsage)
	}
	u.TenantID = tenantID
	m.byTenant[tenantID][u.ToolID] = u
}

// FetchUsage returns the seeded record for (tenant, toolID). The `since`
// parameter is honored: if WindowStart is before `since`, we return ErrNoData
// (the caller asked for recent data and we don't have it).
func (m *Memory) FetchUsage(tenantID string, toolID ToolID, since time.Time) (ToolUsage, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	tools, ok := m.byTenant[tenantID]
	if !ok {
		return ToolUsage{}, ErrUnknownTool
	}
	u, ok := tools[toolID]
	if !ok {
		return ToolUsage{}, ErrUnknownTool
	}
	if !u.WindowStart.IsZero() && u.WindowStart.Before(since) {
		return ToolUsage{}, ErrNoData
	}
	return u, nil
}

// ListTools returns all seeded tool IDs for a tenant, sorted.
func (m *Memory) ListTools(tenantID string) ([]ToolID, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	tools, ok := m.byTenant[tenantID]
	if !ok {
		return nil, nil
	}
	out := make([]ToolID, 0, len(tools))
	for id := range tools {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// Aggregate derives a ToolUsage from a sequence of individual call records.
// This is the helper the real (DB-backed) implementation will use to roll
// up audit events into a profile; exposing it here lets the production
// impl share the exact aggregation logic with tests.
//
// records may be empty; the returned ToolUsage's TotalCalls is then 0.
func Aggregate(toolID ToolID, tenantID string, since, until time.Time, records []CallRecord) ToolUsage {
	out := ToolUsage{
		ToolID:      toolID,
		TenantID:    tenantID,
		WindowStart: since,
		WindowEnd:   until,
	}
	if len(records) == 0 {
		return out
	}
	// paramDist[name] = []any sample (capped)
	paramDist := map[ParameterName][]any{}
	paramTotals := map[ParameterName]int64{}
	paramMissing := map[ParameterName]int64{}
	paramDistinct := map[ParameterName]map[any]struct{}{}
	var totalLatency int64
	for _, r := range records {
		out.TotalCalls++
		if r.Success {
			out.SuccessCount++
		} else {
			out.ErrorCount++
			if len(out.SampleErrors) < 8 && r.ErrorMsg != "" {
				out.SampleErrors = append(out.SampleErrors, truncate(r.ErrorMsg, 200))
			}
		}
		totalLatency += r.LatencyMs
		for name, val := range r.Args {
			paramTotals[name]++
			if paramDistinct[name] == nil {
				paramDistinct[name] = make(map[any]struct{})
			}
			if len(paramDistinct[name]) < 256 {
				paramDistinct[name][val] = struct{}{}
			}
			if len(paramDist[name]) < 16 {
				paramDist[name] = append(paramDist[name], val)
			}
		}
		// Detect missing params by checking the tool's full schema vs r.Args
		// (we don't know the schema here; production impl fills this in by
		// joining against registry.Tool). For now, leave MissingCount=0.
		_ = paramMissing
	}
	if out.TotalCalls > 0 {
		out.AvgLatencyMs = int(totalLatency / out.TotalCalls)
	}
	for name, total := range paramTotals {
		out.Parameters = append(out.Parameters, ArgUsage{
			Name:         name,
			TotalCalls:   total,
			DistinctVals: len(paramDistinct[name]),
			SampleVals:   paramDist[name],
			MissingCount: paramMissing[name],
		})
	}
	sort.Slice(out.Parameters, func(i, j int) bool {
		return out.Parameters[i].TotalCalls > out.Parameters[j].TotalCalls
	})
	return out
}

// CallRecord is the raw shape of one tool-call event. The DB-backed
// implementation constructs these from audit.Event rows.
type CallRecord struct {
	ToolID    ToolID
	TenantID  string
	Args      map[ParameterName]any
	Success   bool
	ErrorMsg  string
	LatencyMs int64
	Timestamp time.Time
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
