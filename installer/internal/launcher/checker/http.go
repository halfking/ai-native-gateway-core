package checker

import (
	"context"
	"fmt"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/installer/internal/upgrader"
)

// MasterHTTPSource queries Maintain's distribution version-check endpoint.
// Anonymous callers receive release metadata; a complete DeviceProof also
// enables the instance-specific P4.4 policy hint.
type MasterHTTPSource struct {
	MasterURL      string
	CurrentVersion func() string
	Channel        string
	Platform       string
	Arch           string
	Proof          upgrader.DeviceProof
	HTTPClient     *http.Client
}

func (m *MasterHTTPSource) Check(ctx context.Context) (*FoundRelease, error) {
	client := upgrader.NewClientWithHTTPClient(m.MasterURL, m.Proof, m.HTTPClient)
	curVer := ""
	if m.CurrentVersion != nil {
		curVer = m.CurrentVersion()
	}
	platform, arch := m.Platform, m.Arch
	if platform == "" {
		platform = "linux"
	}
	if arch == "" {
		arch = "amd64"
	}
	result, err := client.CheckDistribution(ctx, curVer, m.Channel, platform, arch)
	if err != nil {
		return nil, fmt.Errorf("maintain version check: %w", err)
	}
	if !result.HasUpdate || result.Release == nil {
		return nil, nil
	}
	return &FoundRelease{
		Version:                  result.Release.Version,
		DownloadURL:              result.Release.DownloadURL,
		SHA256:                   result.Release.SHA256,
		Changelog:                result.Release.Changelog,
		AutoUpgrade:              result.AutoUpgrade,
		UpgradePolicyID:          result.UpgradePolicyID,
		EstimatedDowntimeMinutes: result.EstimatedDowntimeMinutes,
	}, nil
}
