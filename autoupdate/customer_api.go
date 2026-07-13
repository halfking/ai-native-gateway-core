package autoupdate

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
)

// VersionProvider returns the running gateway's version triple.
// In production this is wired to cmd/gateway's Version / GitCommit / BuildNumber.
type VersionProvider func() (version string, buildSeq int)

// CustomerAPI exposes customer-facing upgrade endpoints under /api/system/upgrade/*.
//
// These endpoints are intentionally unauthenticated because the customer must be
// able to see available updates before any admin login. They are read-only —
// actual upgrade execution is initiated by the admin via /api/admin/releases/*.
type CustomerAPI struct {
	store   Store
	version VersionProvider
	channel Channel
}

// NewCustomerAPI wires the customer-facing upgrade handlers.
func NewCustomerAPI(store Store, version VersionProvider, channel Channel) *CustomerAPI {
	if channel == "" {
		channel = ChannelStable
	}
	return &CustomerAPI{
		store:   store,
		version: version,
		channel: channel,
	}
}

// RegisterRoutes mounts customer-facing handlers under the given Echo group.
// Caller is expected to pass the /api/system/upgrade group.
func (api *CustomerAPI) RegisterRoutes(g *echo.Group) {
	g.GET("/status", api.handleStatus)
	g.POST("/check", api.handleCheck)
}

type UpgradeStatusResponse struct {
	CurrentVersion  string  `json:"current_version"`
	CurrentBuildSeq int     `json:"current_build_seq"`
	Channel         Channel `json:"channel"`
	LatestVersion   string  `json:"latest_version,omitempty"`
	LatestBuildSeq  int     `json:"latest_build_seq,omitempty"`
	HasUpdate       bool    `json:"has_update"`
	UpdateMandatory bool    `json:"update_mandatory,omitempty"`
	ReleaseTitle    string  `json:"release_title,omitempty"`
	ReleaseNotes    string  `json:"release_notes,omitempty"`
	MinVersion      string  `json:"min_version,omitempty"`
	IsCompatible    bool    `json:"is_compatible"`
	PublishedAt     *string `json:"published_at,omitempty"`
}

type UpgradeCheckResponse struct {
	UpgradeStatusResponse
	CheckedAt string `json:"checked_at"`
}

func (api *CustomerAPI) handleStatus(c echo.Context) error {
	return c.JSON(http.StatusOK, api.buildStatus(c.Request().Context()))
}

func (api *CustomerAPI) handleCheck(c echo.Context) error {
	resp := api.buildStatus(c.Request().Context())
	return c.JSON(http.StatusOK, UpgradeCheckResponse{
		UpgradeStatusResponse: resp,
		CheckedAt:             nowString(),
	})
}

func (api *CustomerAPI) buildStatus(ctx context.Context) UpgradeStatusResponse {
	currentVer, currentSeq := api.currentVersion()

	resp := UpgradeStatusResponse{
		CurrentVersion:  currentVer,
		CurrentBuildSeq: currentSeq,
		Channel:         api.channel,
		IsCompatible:    true,
	}

	latest, err := api.store.GetLatestReleaseAfter(ctx, api.channel, currentSeq)
	if err != nil {
		slog.Warn("upgrade status: failed to fetch latest release", "error", err, "channel", api.channel)
		return resp
	}
	if latest == nil {
		// Fall back to the absolute latest in case build_seq doesn't match exactly.
		latest, err = api.store.GetLatestRelease(ctx, api.channel)
		if err != nil || latest == nil {
			return resp
		}
	}

	if latest.PublishedAt == nil {
		return resp
	}

	resp.LatestVersion = latest.Version
	resp.LatestBuildSeq = latest.BuildSeq
	resp.ReleaseTitle = latest.Title
	resp.ReleaseNotes = latest.Description
	resp.MinVersion = latest.MinVersion
	resp.UpdateMandatory = latest.Mandatory

	if latest.PublishedAt != nil {
		iso := latest.PublishedAt.UTC().Format("2006-01-02T15:04:05Z")
		resp.PublishedAt = &iso
	}

	if latest.BuildSeq > currentSeq {
		resp.HasUpdate = true
	}
	if !IsCompatible(currentVer, latest.MinVersion) {
		resp.IsCompatible = false
	}
	return resp
}

func (api *CustomerAPI) currentVersion() (string, int) {
	if api.version == nil {
		return "dev", 0
	}
	v, seq := api.version()
	if v == "" {
		v = "dev"
	}
	if seq <= 0 {
		if n, err := strconv.Atoi(strconv.Itoa(seq)); err == nil {
			seq = n
		}
	}
	return v, seq
}

func nowString() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}
