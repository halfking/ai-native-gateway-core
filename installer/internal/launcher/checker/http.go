package checker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// MasterHTTPSource queries master for the latest Gateway release.
// Matches upgrader/client.go:46 endpoint shape:
// GET <master>/api/v1/updates/latest?channel=<ch>&current_version=<ver>
type MasterHTTPSource struct {
	MasterURL      string
	CurrentVersion string
	Channel        string
	HTTPClient     *http.Client
}

func (m *MasterHTTPSource) Check(ctx context.Context) (*FoundRelease, error) {
	if m.HTTPClient == nil {
		m.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	endpoint := fmt.Sprintf("%s/api/v1/updates/latest?channel=%s&current_version=%s",
		m.MasterURL,
		url.QueryEscape(m.Channel),
		url.QueryEscape(m.CurrentVersion),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("master check status %d", resp.StatusCode)
	}
	var body struct {
		HasUpdate bool `json:"has_update"`
		Release   struct {
			Version     string `json:"version"`
			DownloadURL string `json:"download_url"`
			SHA256      string `json:"sha256"`
			Changelog   string `json:"changelog"`
			Image       string `json:"image"`
		} `json:"release"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if !body.HasUpdate {
		return nil, nil
	}
	return &FoundRelease{
		Version:     body.Release.Version,
		DownloadURL: body.Release.DownloadURL,
		SHA256:      body.Release.SHA256,
		Changelog:   body.Release.Changelog,
		Image:       body.Release.Image,
	}, nil
}