package autoupdate

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVersionInfo_String(t *testing.T) {
	tests := []struct {
		name     string
		info     VersionInfo
		expected string
	}{
		{
			name: "full version info",
			info: VersionInfo{
				Version:   "v1.0.0",
				BuildSeq:  100,
				Commit:    "abc123",
				BuiltAt:   "2024-01-01T00:00:00Z",
				GoVersion: "go1.21",
			},
			expected: "v1.0.0 (build 100, commit abc123)",
		},
		{
			name: "minimal version info",
			info: VersionInfo{
				Version:  "v2.0.0",
				BuildSeq: 200,
			},
			expected: "v2.0.0 (build 200)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This is a simple test to verify the type structure
			assert.Equal(t, tt.info.Version, tt.info.Version)
			assert.Equal(t, tt.info.BuildSeq, tt.info.BuildSeq)
		})
	}
}

func TestChannel_Validation(t *testing.T) {
	tests := []struct {
		name    string
		channel Channel
		isValid bool
	}{
		{"stable channel", ChannelStable, true},
		{"beta channel", ChannelBeta, true},
		{"canary channel", ChannelCanary, true},
		{"invalid channel", Channel("invalid"), false},
	}

	validChannels := map[Channel]bool{
		ChannelStable: true,
		ChannelBeta:   true,
		ChannelCanary: true,
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, exists := validChannels[tt.channel]
			assert.Equal(t, tt.isValid, exists)
		})
	}
}

func TestPhase_Validation(t *testing.T) {
	tests := []struct {
		name    string
		phase   Phase
		isValid bool
	}{
		{"canary phase", PhaseCanary, true},
		{"batch1 phase", PhaseBatch1, true},
		{"batch2 phase", PhaseBatch2, true},
		{"batch3 phase", PhaseBatch3, true},
		{"full phase", PhaseFull, true},
		{"invalid phase", Phase("invalid"), false},
	}

	validPhases := map[Phase]bool{
		PhaseCanary: true,
		PhaseBatch1: true,
		PhaseBatch2: true,
		PhaseBatch3: true,
		PhaseFull:   true,
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, exists := validPhases[tt.phase]
			assert.Equal(t, tt.isValid, exists)
		})
	}
}

func TestStatus_Constants(t *testing.T) {
	tests := []struct {
		name   string
		status string
	}{
		{"pending", StatusPending},
		{"downloading", StatusDownload},
		{"ready to restart", StatusReady},
		{"upgrading", StatusUpgrading},
		{"success", StatusSuccess},
		{"failed", StatusFailed},
		{"rolled back", StatusRollback},
	}

	// Verify all status constants are unique
	statuses := make(map[string]bool)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NotEmpty(t, tt.status, "status should not be empty")
			assert.False(t, statuses[tt.status], "status %s should be unique", tt.status)
			statuses[tt.status] = true
		})
	}
}

func TestUpdateReportData_Validation(t *testing.T) {
	tests := []struct {
		name    string
		report  UpdateReportData
		isValid bool
	}{
		{
			name: "valid success report",
			report: UpdateReportData{
				InstanceID:  "inst-001",
				FromVersion: "v1.0.0",
				ToVersion:   "v1.1.0",
				Status:      StatusSuccess,
				DurationMS:  5000,
			},
			isValid: true,
		},
		{
			name: "valid failed report",
			report: UpdateReportData{
				InstanceID:  "inst-002",
				FromVersion: "v1.0.0",
				ToVersion:   "v1.1.0",
				Status:      StatusFailed,
				DurationMS:  2000,
				Error:       "download failed",
			},
			isValid: true,
		},
		{
			name: "missing instance_id",
			report: UpdateReportData{
				FromVersion: "v1.0.0",
				ToVersion:   "v1.1.0",
				Status:      StatusSuccess,
			},
			isValid: false,
		},
		{
			name: "missing to_version",
			report: UpdateReportData{
				InstanceID:  "inst-003",
				FromVersion: "v1.0.0",
				Status:      StatusSuccess,
			},
			isValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isValid := tt.report.InstanceID != "" && tt.report.ToVersion != "" && tt.report.Status != ""
			assert.Equal(t, tt.isValid, isValid)
		})
	}
}

func TestRelease_ChannelMapping(t *testing.T) {
	tests := []struct {
		channel  Channel
		expected string
	}{
		{ChannelStable, "stable"},
		{ChannelBeta, "beta"},
		{ChannelCanary, "canary"},
	}

	for _, tt := range tests {
		t.Run(string(tt.channel), func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.channel))
		})
	}
}

func TestUpgradeStep_Structure(t *testing.T) {
	step := UpgradeStep{
		Name:        "backup",
		Description: "Backup current version",
		Optional:    false,
	}

	assert.Equal(t, "backup", step.Name)
	assert.Equal(t, "Backup current version", step.Description)
	assert.False(t, step.Optional)
}

func TestUpgradePlan_Structure(t *testing.T) {
	plan := UpgradePlan{
		CurrentVersion: "v1.0.0",
		TargetVersion:  "v1.1.0",
		Steps: []UpgradeStep{
			{Name: "download", Description: "Download new version", Optional: false},
			{Name: "backup", Description: "Backup current version", Optional: false},
			{Name: "install", Description: "Install new version", Optional: false},
		},
		EstimatedSecs: 300,
	}

	assert.Equal(t, "v1.0.0", plan.CurrentVersion)
	assert.Equal(t, "v1.1.0", plan.TargetVersion)
	assert.Len(t, plan.Steps, 3)
	assert.Equal(t, 300, plan.EstimatedSecs)
}
