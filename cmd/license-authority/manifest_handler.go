package main

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/autoupdate"
	"github.com/labstack/echo/v4"
)

// ManifestHandler handles manifest requests
type ManifestHandler struct {
	store autoupdate.Store
}

// NewManifestHandler creates a new manifest handler
func NewManifestHandler(store autoupdate.Store) *ManifestHandler {
	return &ManifestHandler{
		store: store,
	}
}

// ManifestResponse represents the manifest.json structure
type ManifestResponse struct {
	Version      string          `json:"version"`
	BuildSeq     int             `json:"build_seq"`
	BinaryURL    string          `json:"binary_url"`
	BinarySHA256 string          `json:"binary_sha256"`
	Images       []ImageManifest `json:"images"`
	Changelog    string          `json:"changelog,omitempty"`
}

// ImageManifest represents a Docker image in the manifest
type ImageManifest struct {
	Name   string `json:"name"`
	Tag    string `json:"tag"`
	Digest string `json:"digest"`
	URL    string `json:"url,omitempty"`
}

// HandleManifest handles GET /api/v1/updates/manifest?version=v1.14.0
func (h *ManifestHandler) HandleManifest(c echo.Context) error {
	version := c.QueryParam("version")
	if version == "" {
		// Support alternative query param name
		version = c.QueryParam("v")
	}
	if version == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "version parameter is required")
	}

	// Query release by version
	release, err := h.store.GetRelease(c.Request().Context(), version)
	if err != nil {
		slog.Warn("release not found", "version", version, "error", err)
		return echo.NewHTTPError(http.StatusNotFound, fmt.Sprintf("version %s not found", version))
	}

	// Build manifest response
	manifest := &ManifestResponse{
		Version:      release.Version,
		BuildSeq:     release.BuildSeq,
		BinaryURL:    fmt.Sprintf("https://llm.kxpms.cn/releases/%s/llm-gateway-go", release.Version),
		BinarySHA256: release.ImageDigest,
		Changelog:    release.Changelog,
		Images:       buildImageList(release),
	}

	return c.JSON(http.StatusOK, manifest)
}

// buildImageList constructs the image list from release metadata
func buildImageList(release *autoupdate.Release) []ImageManifest {
	images := []ImageManifest{
		{
			Name:   "kx-llm-gateway-go",
			Tag:    release.ImageTag,
			Digest: release.ImageDigest,
			URL:    fmt.Sprintf("registry.kxpms.cn/kx-llm-gateway-go:%s", release.ImageTag),
		},
	}

	// TODO: Parse additional images from release metadata if needed
	// For now, return the primary gateway image
	return images
}

// RegisterRoutes registers manifest routes
func (h *ManifestHandler) RegisterRoutes(g *echo.Group) {
	g.GET("/manifest", h.HandleManifest)
}
