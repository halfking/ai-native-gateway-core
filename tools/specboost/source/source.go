// Package source enriches SpecBoost with real usage data (C1-3).
//
// The v1 PoC (tools/specboost/) enhances a tool spec using only its static
// description + a mock LLM. C1-3 adds a DATA SOURCE abstraction that feeds
// the enhancer concrete usage signals:
//
//   - How often is the tool called?
//   - What arguments are passed in practice? (parameter coverage)
//   - What's the success / failure rate?
//   - What error messages does the LLM see? (helps the enhancer write
//     descriptions that pre-empt common mistakes)
//   - What call patterns emerge? (e.g., the tool is always called in a
//     chain after another tool)
//
// The Source interface decouples the enhancer from the data layer so tests
// inject a deterministic fake. The production Source reads from the
// registry (tools) and audit (events) packages — both already exist.
package source

import (
	"errors"
	"time"
)

// ToolID uniquely identifies a tool within a tenant.
type ToolID string

// ParameterName is a parameter on a tool (e.g. "query", "limit").
type ParameterName string

// ArgUsage describes how a single parameter is used in practice.
type ArgUsage struct {
	Name         ParameterName
	TotalCalls   int64 // times this parameter appeared
	DistinctVals int   // rough cardinality estimate (cap'd at 256)
	SampleVals   []any // ≤16 representative values (for prompt context)
	MissingCount int64 // calls where this param was absent
}

// ToolUsage is the aggregated usage profile of one tool over a time window.
type ToolUsage struct {
	ToolID       ToolID
	TenantID     string
	WindowStart  time.Time
	WindowEnd    time.Time
	TotalCalls   int64
	SuccessCount int64
	ErrorCount   int64
	AvgLatencyMs int
	Parameters   []ArgUsage // sorted by TotalCalls DESC
	SampleErrors []string   // ≤8 truncated error messages
}

// Source is the data access layer the SpecBoost enhancer reads.
type Source interface {
	// FetchUsage returns the usage profile for a single tool.
	FetchUsage(tenantID string, toolID ToolID, since time.Time) (ToolUsage, error)

	// ListTools returns the registry-known tool IDs in a tenant. SpecBoost
	// loops over these when running batch enhancement (Q4 C1-4).
	ListTools(tenantID string) ([]ToolID, error)
}

// ErrNoData is returned when the time window has no observations.
var ErrNoData = errors.New("source: no usage data in window")

// ErrUnknownTool is returned for tools not in the registry.
var ErrUnknownTool = errors.New("source: unknown tool id")
