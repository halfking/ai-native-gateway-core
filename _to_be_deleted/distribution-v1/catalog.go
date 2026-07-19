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

// ExtendedCatalogProvider can list multiple published versions.
type ExtendedCatalogProvider interface {
	CatalogProvider
	ListPublishedVersions(ctx context.Context, limit int) ([]PublishedVersion, error)
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
	meta := s.catalogMeta()
	supporters, _ := s.store.CountSupporters(ctx)

	groups, latest := s.buildVersionGroups(ctx, meta)
	if latest.Version == "" {
		latest = VersionGroup{
			Version:  meta.fallback.Version,
			BuildSeq: meta.fallback.BuildSeq,
			Items:    s.defaultItems(meta.fallback.Version),
		}
		groups = []VersionGroup{latest}
	}

	return &CatalogResponse{
		Version:      latest.Version,
		BuildSeq:     latest.BuildSeq,
		Channel:      "stable",
		ReleaseDate:  latest.ReleaseDate,
		Items:        latest.Items,
		Versions:     groups,
		Supporters:   supporters,
		GitRepoURL:   meta.gitRepo,
		GitBranch:    meta.gitBranch,
		DocsURL:      meta.docsURL,
		ContactEmail: meta.contactEmail,
	}, nil
}

type catalogMetaBundle struct {
	fallback     VersionFallback
	gitRepo      string
	gitBranch    string
	docsURL      string
	contactEmail string
}

func (s *CatalogService) catalogMeta() catalogMetaBundle {
	gitRepo := os.Getenv("GIT_REPO_URL")
	if gitRepo == "" {
		gitRepo = "https://github.com/halfking/ai-native-gateway"
	}
	gitBranch := os.Getenv("GIT_REPO_BRANCH")
	if gitBranch == "" {
		gitBranch = "main"
	}
	docsURL := os.Getenv("DOCS_URL")
	if docsURL == "" {
		docsURL = "https://llmgo.kxpms.cn/docs"
	}
	contactEmail := os.Getenv("CONTACT_EMAIL")
	if contactEmail == "" {
		contactEmail = "huangxutao@kxpms.cn"
	}
	return catalogMetaBundle{
		fallback:     s.fallback,
		gitRepo:      gitRepo,
		gitBranch:    gitBranch,
		docsURL:      docsURL,
		contactEmail: contactEmail,
	}
}

func (s *CatalogService) buildVersionGroups(ctx context.Context, meta catalogMetaBundle) ([]VersionGroup, VersionGroup) {
	var published []PublishedVersion
	if ext, ok := s.releases.(ExtendedCatalogProvider); ok {
		if rows, err := ext.ListPublishedVersions(ctx, 12); err == nil && len(rows) > 0 {
			published = rows
		}
	}
	if len(published) == 0 {
		version := meta.fallback.Version
		buildSeq := meta.fallback.BuildSeq
		if s.releases != nil {
			if v, bs, pub, err := s.releases.LatestPublishedVersion(ctx); err == nil && v != "" {
				version, buildSeq = v, bs
				if pub != nil {
					published = append(published, PublishedVersion{Version: version, BuildSeq: buildSeq, PublishedAt: pub})
				}
			}
		}
		if len(published) == 0 {
			published = []PublishedVersion{{Version: version, BuildSeq: buildSeq}}
		}
	}

	groups := make([]VersionGroup, 0, len(published))
	var latest VersionGroup
	for i, row := range published {
		group := s.versionGroup(ctx, row, meta)
		groups = append(groups, group)
		if i == 0 {
			latest = group
		}
	}
	return groups, latest
}

func (s *CatalogService) versionGroup(ctx context.Context, row PublishedVersion, meta catalogMetaBundle) VersionGroup {
	items := s.itemsForVersion(ctx, row.Version)
	releaseDate := ""
	if row.PublishedAt != nil {
		releaseDate = row.PublishedAt.Format(time.RFC3339)
	}
	installDoc := meta.docsURL
	if meta.gitRepo != "" {
		installDoc = fmt.Sprintf("%s/blob/%s/docs/DEPLOYMENT_GUIDE.md", strings.TrimRight(meta.gitRepo, "/"), meta.gitBranch)
	}
	return VersionGroup{
		Version:       row.Version,
		BuildSeq:      row.BuildSeq,
		ReleaseDate:   releaseDate,
		Items:         items,
		InstallDocURL: installDoc,
	}
}

func (s *CatalogService) itemsForVersion(ctx context.Context, version string) []CatalogItem {
	artifacts, _ := s.store.ListArtifacts(ctx, version)
	if len(artifacts) > 0 {
		items := make([]CatalogItem, 0, len(artifacts))
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
		return items
	}
	return s.defaultItems(version)
}

func (s *CatalogService) defaultItems(version string) []CatalogItem {
	items := make([]CatalogItem, 0, len(defaultPlatforms))
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
	return items
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
