package distribution

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// Default platforms when release_artifacts table is empty.
var defaultPlatforms = []CatalogItem{
	{Platform: "linux", Arch: "amd64", Label: "Linux x86_64 (amd64)", SizeLabel: "~3.2 GB"},
	{Platform: "linux", Arch: "arm64", Label: "Linux ARM64", SizeLabel: "~3.0 GB"},
	{Platform: "darwin", Arch: "amd64", Label: "macOS Intel (amd64)", SizeLabel: "~3.2 GB"},
	{Platform: "darwin", Arch: "arm64", Label: "macOS Apple Silicon (arm64)", SizeLabel: "~3.0 GB"},
	{Platform: "windows", Arch: "amd64", Label: "Windows x86_64", SizeLabel: "~3.3 GB"},
	{Platform: "linux", Arch: "loong64", Label: "龙芯 LoongArch", SizeLabel: "~3.2 GB"},
}

type CatalogService struct {
	store    Store
	releases CatalogProvider
	baseURL  string
	fallback VersionFallback
}

type VersionFallback struct {
	Version  string
	BuildSeq int
}

func NewCatalogService(store Store, releases CatalogProvider, baseURL string, fallback VersionFallback) *CatalogService {
	if baseURL == "" {
		baseURL = "https://download.kxpms.cn/llm-gateway-go"
	}
	return &CatalogService{store: store, releases: releases, baseURL: strings.TrimRight(baseURL, "/"), fallback: fallback}
}

func LoadVersionFallback() VersionFallback {
	v := os.Getenv("GATEWAY_RELEASE_VERSION")
	if v == "" {
		v = "v2.4.5"
	}
	return VersionFallback{Version: v, BuildSeq: 1018}
}

func (s *CatalogService) BuildCatalog(ctx context.Context) (*CatalogResponse, error) {
	version := s.fallback.Version
	buildSeq := s.fallback.BuildSeq
	var releaseDate string

	if s.releases != nil {
		if v, bs, pub, err := s.releases.LatestPublishedVersion(ctx); err == nil && v != "" {
			version = v
			buildSeq = bs
			if pub != nil {
				releaseDate = pub.Format(time.RFC3339)
			}
		}
	}

	artifacts, _ := s.store.ListArtifacts(ctx, version)
	items := make([]CatalogItem, 0)
	if len(artifacts) > 0 {
		for _, a := range artifacts {
			items = append(items, CatalogItem{
				Platform:     a.Platform,
				Arch:         a.Arch,
				Label:        platformLabel(a.Platform, a.Arch),
				ArtifactName: a.ArtifactName,
				SHA256:       a.SHA256,
				SizeBytes:    a.SizeBytes,
				SizeLabel:    formatSize(a.SizeBytes),
			})
		}
	} else {
		for _, p := range defaultPlatforms {
			name := artifactFileName(version, p.Platform, p.Arch)
			items = append(items, CatalogItem{
				Platform:     p.Platform,
				Arch:         p.Arch,
				Label:        p.Label,
				ArtifactName: name,
				SizeLabel:    p.SizeLabel,
			})
		}
	}

	supporters, _ := s.store.CountSupporters(ctx)

	gitRepo := os.Getenv("GIT_REPO_URL")
	if gitRepo == "" {
		gitRepo = "https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go"
	}
	gitBranch := os.Getenv("GIT_REPO_BRANCH")
	if gitBranch == "" {
		gitBranch = "main"
	}
	docsURL := os.Getenv("DOCS_URL")
	if docsURL == "" {
		docsURL = "https://llmgo.kxpms.cn/docs"
	}

	return &CatalogResponse{
		Version:     version,
		BuildSeq:    buildSeq,
		Channel:     "stable",
		ReleaseDate: releaseDate,
		Items:       items,
		Supporters:  supporters,
		GitRepoURL:  gitRepo,
		GitBranch:   gitBranch,
		DocsURL:     docsURL,
	}, nil
}

func (s *CatalogService) ArtifactURL(version, platform, arch string) (fileName, url string) {
	fileName = artifactFileName(version, platform, arch)
	url = fmt.Sprintf("%s/%s/%s", s.baseURL, version, fileName)
	return fileName, url
}

func artifactFileName(version, platform, arch string) string {
	short := strings.TrimPrefix(version, "v")
	if platform == "windows" {
		return fmt.Sprintf("llm-gateway-go-%s-windows-%s-offline.zip", short, arch)
	}
	return fmt.Sprintf("llm-gateway-go-%s-%s-%s-offline.tar.gz", short, platform, arch)
}

func platformLabel(platform, arch string) string {
	for _, p := range defaultPlatforms {
		if p.Platform == platform && p.Arch == arch {
			return p.Label
		}
	}
	return fmt.Sprintf("%s / %s", platform, arch)
}

func formatSize(bytes int64) string {
	if bytes <= 0 {
		return ""
	}
	gb := float64(bytes) / (1024 * 1024 * 1024)
	return fmt.Sprintf("~%.1f GB", gb)
}
