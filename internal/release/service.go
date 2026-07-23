package release

import (
	"context"
	"fmt"
)

// Service 版本管理服务
type Service struct {
	repo *Repository
}

// NewService 创建服务
func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

// CreateRelease 创建版本
func (s *Service) CreateRelease(ctx context.Context, req CreateReleaseRequest, createdBy string) (*Release, error) {
	// 检查版本是否已存在
	existing, _ := s.repo.GetReleaseByVersion(ctx, req.Version)
	if existing != nil && existing.BuildSeq >= req.BuildSeq {
		return nil, fmt.Errorf("release version %s (build %d) already exists", req.Version, req.BuildSeq)
	}

	release := &Release{
		Version:             req.Version,
		FullVersion:         req.FullVersion,
		BuildSeq:            req.BuildSeq,
		GitSHA:              req.GitSHA,
		BuildDate:           req.BuildDate,
		ReleaseType:         req.ReleaseType,
		ReleaseNotes:        req.ReleaseNotes,
		ChangelogURL:        req.ChangelogURL,
		UpgradeFromVersions: req.UpgradeFromVersions,
		BreakingChanges:     req.BreakingChanges,
		MinPostgresVersion:  req.MinPostgresVersion,
		MinRedisVersion:     req.MinRedisVersion,
		MinGoVersion:        req.MinGoVersion,
		MinNodeVersion:      req.MinNodeVersion,
		Status:              "draft",
		IsPublic:            req.IsPublic,
		CreatedBy:           createdBy,
	}

	err := s.repo.CreateRelease(ctx, release)
	if err != nil {
		return nil, fmt.Errorf("failed to create release: %w", err)
	}

	return release, nil
}

// GetRelease 获取版本详情
func (s *Service) GetRelease(ctx context.Context, id int64) (*Release, error) {
	return s.repo.GetReleaseByID(ctx, id)
}

// GetLatestRelease 获取最新版本
func (s *Service) GetLatestRelease(ctx context.Context, isPublic bool) (*Release, error) {
	return s.repo.GetLatestRelease(ctx, isPublic)
}

// ListReleases 列表查询
func (s *Service) ListReleases(ctx context.Context, query ListReleasesQuery) ([]ReleaseOverview, int, error) {
	return s.repo.ListReleases(ctx, query)
}

// PublishRelease 发布版本
func (s *Service) PublishRelease(ctx context.Context, id int64, updatedBy string) error {
	release, err := s.repo.GetReleaseByID(ctx, id)
	if err != nil {
		return err
	}

	if release.Status == "published" {
		return fmt.Errorf("release already published")
	}

	// 更新状态为 published
	err = s.repo.UpdateReleaseStatus(ctx, id, "published", updatedBy)
	if err != nil {
		return fmt.Errorf("failed to publish release: %w", err)
	}

	// 如果是 stable 版本，设置为最新
	if release.ReleaseType == "stable" {
		err = s.repo.SetLatestRelease(ctx, id)
		if err != nil {
			return fmt.Errorf("failed to set as latest: %w", err)
		}
	}

	return nil
}

// DeprecateRelease 废弃版本
func (s *Service) DeprecateRelease(ctx context.Context, id int64, updatedBy string) error {
	return s.repo.UpdateReleaseStatus(ctx, id, "deprecated", updatedBy)
}

// AddFile 添加文件
func (s *Service) AddFile(ctx context.Context, releaseID int64, req AddFileRequest) (*ReleaseFile, error) {
	// 检查版本是否存在
	_, err := s.repo.GetReleaseByID(ctx, releaseID)
	if err != nil {
		return nil, fmt.Errorf("release not found")
	}

	file := &ReleaseFile{
		ReleaseID:   releaseID,
		Filename:    req.Filename,
		FilePath:    req.FilePath,
		FileSize:    req.FileSize,
		FileHash:    req.FileHash,
		Platform:    req.Platform,
		OS:          req.OS,
		Arch:        req.Arch,
		DownloadURL: req.DownloadURL,
		ShareURL:    req.ShareURL,
	}

	err = s.repo.AddFile(ctx, file)
	if err != nil {
		return nil, fmt.Errorf("failed to add file: %w", err)
	}

	return file, nil
}

// GetFiles 获取版本的所有文件
func (s *Service) GetFiles(ctx context.Context, releaseID int64) ([]ReleaseFile, error) {
	return s.repo.GetFilesByReleaseID(ctx, releaseID)
}

// RecordDownload 记录下载
func (s *Service) RecordDownload(ctx context.Context, releaseID, fileID int64) error {
	// 增加版本下载次数
	err := s.repo.IncrementDownloadCount(ctx, releaseID)
	if err != nil {
		return err
	}

	// 增加文件下载次数
	if fileID > 0 {
		err = s.repo.IncrementFileDownloadCount(ctx, fileID)
		if err != nil {
			return err
		}
	}

	return nil
}

// AddTest 添加测试记录
func (s *Service) AddTest(ctx context.Context, releaseID int64, req AddTestRequest) (*ReleaseTest, error) {
	// 检查版本是否存在
	_, err := s.repo.GetReleaseByID(ctx, releaseID)
	if err != nil {
		return nil, fmt.Errorf("release not found")
	}

	test := &ReleaseTest{
		ReleaseID:       releaseID,
		TestType:        req.TestType,
		TestName:        req.TestName,
		TestStatus:      req.TestStatus,
		TestOutput:      req.TestOutput,
		TestDuration:    req.TestDuration,
		ErrorMessage:    req.ErrorMessage,
		TestEnvironment: req.TestEnvironment,
		Tester:          req.Tester,
	}

	err = s.repo.AddTest(ctx, test)
	if err != nil {
		return nil, fmt.Errorf("failed to add test: %w", err)
	}

	return test, nil
}

// GetTests 获取版本的所有测试
func (s *Service) GetTests(ctx context.Context, releaseID int64) ([]ReleaseTest, error) {
	return s.repo.GetTestsByReleaseID(ctx, releaseID)
}
