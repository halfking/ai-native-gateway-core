//go:build !integration

package collector

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubPref struct{ enabled bool }

func (s stubPref) Enabled(context.Context) (bool, error) { return s.enabled, nil }

type stubTraffic struct{}

func (stubTraffic) Snapshot(context.Context) (TrafficSnapshot, error) {
	return TrafficSnapshot{
		CurrentConcurrency: 2,
		Last5MinTPS:        1.5,
		Last5MinP50Ms:      40,
		Last5MinP99Ms:      120,
		Last5MinSuccessPct: 99.1,
		ModelUsage:         map[string]int64{"gpt-4": 10},
		TenantCount:        1,
	}, nil
}

type recordingReporter struct{ payloads [][]byte }

func (r *recordingReporter) Report(_ context.Context, payload []byte) error {
	r.payloads = append(r.payloads, append([]byte(nil), payload...))
	return nil
}

func TestCollector_SkipsWhenOptOut(t *testing.T) {
	reporter := &recordingReporter{}
	c := NewCollector(Config{
		InstanceID: "inst-1",
		Version:    "v1",
		StartTime:  time.Now().Add(-time.Minute),
	}, stubPref{enabled: false}, stubTraffic{}, nil, nil, reporter)

	require.NoError(t, c.collectOnce(context.Background()))
	assert.Empty(t, reporter.payloads)
}

func TestCollector_ReportsAllowlistedPayload(t *testing.T) {
	reporter := &recordingReporter{}
	c := NewCollector(Config{
		InstanceID:     "inst-1",
		Version:        "v1.2.3",
		LicenseKeyHash: "abcd1234abcd1234",
		StartTime:      time.Now().Add(-2 * time.Minute),
	}, stubPref{enabled: true}, stubTraffic{}, nil, nil, reporter)

	require.NoError(t, c.collectOnce(context.Background()))
	require.Len(t, reporter.payloads, 1)

	var decoded RuntimeMetrics
	require.NoError(t, json.Unmarshal(reporter.payloads[0], &decoded))
	assert.Equal(t, "inst-1", decoded.InstanceID)
	assert.Equal(t, "v1.2.3", decoded.Version)
	assert.Equal(t, 2, decoded.CurrentConcurrency)
	assert.Equal(t, int64(10), decoded.ModelUsage["gpt-4"])
	assert.NoError(t, ValidatePayload(reporter.payloads[0]))
}
